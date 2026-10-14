package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liocoder/TraceMind/internal/evidence"
	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/tool"
)

const MaxToolCalls = 3

type Report struct {
	Incident     string              `json:"incident"`
	State        State               `json:"state"`
	StateHistory []State             `json:"state_history"`
	ToolCalls    int                 `json:"tool_calls"`
	Summary      string              `json:"summary"`
	Hypotheses   []string            `json:"hypotheses"`
	Evidence     []evidence.Evidence `json:"evidence"`
}

// completionClient 抽象模型调用，便于在测试中使用模拟客户端。
type completionClient interface {
	Complete(
		ctx context.Context,
		messages []llm.Message,
		tools []llm.ToolDefinition,
	) (*llm.Completion, error)
}

type Runtime struct {
	tools        *tool.Registry
	client       completionClient
	maxToolCalls int
}

// NewRuntime 保持原有接口，正常运行时使用真实 LLM 客户端。
func NewRuntime(
	tools *tool.Registry,
	clients ...*llm.Client,
) *Runtime {
	var client completionClient

	if len(clients) > 0 {
		client = clients[0]
	}

	return newRuntime(tools, client)
}

func newRuntime(
	tools *tool.Registry,
	client completionClient,
) *Runtime {
	return &Runtime{
		tools:        tools,
		client:       client,
		maxToolCalls: MaxToolCalls,
	}
}

func (r *Runtime) SetMaxToolCalls(max int) {
	if max > 0 {
		r.maxToolCalls = max
	}
}

func (r *Runtime) Run(
	ctx context.Context,
	incident string,
) (*Report, error) {
	incident = strings.TrimSpace(incident)
	if incident == "" {
		return nil, fmt.Errorf("incident description cannot be empty")
	}
	if r == nil {
		return nil, fmt.Errorf("runtime cannot be nil")
	}
	if r.tools == nil {
		return nil, fmt.Errorf("tool registry cannot be nil")
	}
	if r.client == nil {
		return nil, fmt.Errorf("LLM client is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("context cannot be nil")
	}

	definitions, err := r.tools.ToLLMDefinitions()
	if err != nil {
		return nil, fmt.Errorf("build tool definitions: %w", err)
	}

	maxCalls := r.maxToolCalls
	if maxCalls <= 0 {
		maxCalls = MaxToolCalls
	}

	sm := NewStateMachine()

	report := &Report{
		Incident:   incident,
		Hypotheses: []string{},
		Evidence:   []evidence.Evidence{},
	}

	// 必须先声明，后续 fail 闭包会读取该变量。
	toolCallsUsed := 0

	transition := func(next State) error {
		if err := sm.Transition(next); err != nil {
			return err
		}

		report.State = sm.Current()
		report.StateHistory = sm.History()
		return nil
	}

	fail := func(err error) (*Report, error) {
		if sm.Current() != StateCompleted &&
			sm.Current() != StateFailed {
			_ = transition(StateFailed)
		}

		report.ToolCalls = toolCallsUsed
		return report, err
	}

	if err := transition(StatePlanning); err != nil {
		return fail(err)
	}

	systemPrompt := `You are TraceMind, an incident diagnosis agent.

Investigate incidents using the available tools.
Base conclusions on actual tool results. Never invent observations.
Distinguish observed facts from hypotheses.
Do not claim a root cause is confirmed unless the evidence supports it.
Investigate downstream dependencies when the evidence suggests they may be involved.
If evidence is insufficient, clearly state the uncertainty.

When finished, return valid JSON only:
{
  "summary": "Concise diagnosis including observed facts and uncertainty",
  "hypotheses": [
    "A plausible explanation that still needs verification"
  ]
}

The summary must be a string. Hypotheses must be an array of strings.
Do not include Markdown code fences around the JSON.`

	messages := []llm.Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: "Investigate this incident: " + incident,
		},
	}

	// 工具预算限制的是实际发起的工具执行次数。
	// 预算耗尽后允许模型继续生成最终诊断，但不执行额外工具。
	maxRounds := maxCalls + 4

	for round := 1; round <= maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}

		if sm.Current() == StateObserving {
			if err := transition(StatePlanning); err != nil {
				return fail(err)
			}
		}

		remaining := maxCalls - toolCallsUsed
		availableTools := definitions

		if remaining <= 0 {
			availableTools = nil
			messages = append(messages, llm.Message{
				Role: "user",
				Content: "The tool-call budget is exhausted. " +
					"Do not call any tools. " +
					"Return the final diagnosis as valid JSON with " +
					"summary and hypotheses, based only on evidence " +
					"already collected.",
			})
		} else {
			messages = append(messages, llm.Message{
				Role: "system",
				Content: fmt.Sprintf(
					"You have %d tool calls remaining. "+
						"Do not request more than %d calls. "+
						"If you have sufficient evidence, return "+
						"the final JSON diagnosis without calling tools.",
					remaining,
					remaining,
				),
			})
		}

		completion, err := r.client.Complete(
			ctx,
			messages,
			availableTools,
		)
		if err != nil {
			return fail(fmt.Errorf("call LLM: %w", err))
		}
		if completion == nil {
			return fail(fmt.Errorf("LLM returned a nil completion"))
		}

		assistantMessage := completion.Message

		if assistantMessage.Role == "" {
			assistantMessage.Role = "assistant"
		}

		// 模型返回最终文本。
		if len(assistantMessage.ToolCalls) == 0 {
			if err := applyFinalAnswer(report, assistantMessage.Content); err != nil {
				return fail(err)
			}

			if err := transition(StateCompleted); err != nil {
				return fail(err)
			}

			report.ToolCalls = toolCallsUsed
			return report, nil
		}

		calls := assistantMessage.ToolCalls
		messages = append(messages, assistantMessage)

		// 预算耗尽后拒绝模型提出的所有工具调用。
		if remaining <= 0 {
			for _, call := range calls {
				messages = append(messages, llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content: "Tool call rejected: budget exhausted. " +
						"Return the final diagnosis using existing evidence.",
				})
			}
			continue
		}

		allowedCount := len(calls)
		if allowedCount > remaining {
			allowedCount = remaining
		}

		if err := transition(StateExecuting); err != nil {
			return fail(err)
		}

		execution, err := ExecuteToolCalls(
			ctx,
			r.tools,
			calls[:allowedCount],
		)
		if err != nil {
			return fail(fmt.Errorf("execute tool calls: %w", err))
		}

		// 统计已发起的调用，包括单次工具执行失败的情况。
		toolCallsUsed += allowedCount
		report.ToolCalls = toolCallsUsed

		// 为成功采集的证据分配全局唯一 ID。
		for _, item := range execution.Evidence {
			item.ID = fmt.Sprintf("ev-%03d", len(report.Evidence)+1)
			report.Evidence = append(report.Evidence, item)
		}

		toolMessages := make([]llm.Message, 0, len(calls))
		toolMessages = append(toolMessages, execution.Messages...)

		// 对超出预算的调用补充拒绝结果，保持消息结构完整。
		for i := allowedCount; i < len(calls); i++ {
			toolMessages = append(toolMessages, llm.Message{
				Role:       "tool",
				ToolCallID: calls[i].ID,
				Content: "Tool call rejected: the remaining tool-call " +
					"budget has been exhausted. Use existing evidence " +
					"to produce the final diagnosis.",
			})
		}

		// 工具结果按调用顺序返回给模型。
		for i, call := range calls {
			fmt.Printf(
				"[TraceMind] tool=%s arguments=%s\n",
				call.Function.Name,
				call.Function.Arguments,
			)

			if i < len(toolMessages) {
				fmt.Printf(
					"[TraceMind] result=%s\n",
					toolMessages[i].Content,
				)
			}
		}

		messages = append(messages, toolMessages...)

		if err := transition(StateObserving); err != nil {
			return fail(err)
		}
	}

	return fail(fmt.Errorf(
		"agent did not produce a final answer within %d rounds",
		maxRounds,
	))
}

// applyFinalAnswer 严格解析模型返回的最终诊断。
// 无效 JSON 或缺少有效摘要时返回错误，避免异常输出被当作正常诊断。
func applyFinalAnswer(report *Report, content string) error {
	if report == nil {
		return fmt.Errorf("report cannot be nil")
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("LLM returned an empty final answer")
	}

	var result struct {
		Summary    string   `json:"summary"`
		Hypotheses []string `json:"hypotheses"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return fmt.Errorf("parse final diagnosis JSON: %w", err)
	}

	result.Summary = strings.TrimSpace(result.Summary)
	if result.Summary == "" {
		return fmt.Errorf("final diagnosis summary cannot be empty")
	}

	if result.Hypotheses == nil {
		result.Hypotheses = []string{}
	}

	for i, hypothesis := range result.Hypotheses {
		result.Hypotheses[i] = strings.TrimSpace(hypothesis)
		if result.Hypotheses[i] == "" {
			return fmt.Errorf(
				"final diagnosis hypothesis at index %d is empty",
				i,
			)
		}
	}

	report.Summary = result.Summary
	report.Hypotheses = result.Hypotheses

	return nil
}
