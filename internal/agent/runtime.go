package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liocoder/TraceMind/internal/diagnosis"
	"github.com/liocoder/TraceMind/internal/evidence"
	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/skills"
	"github.com/liocoder/TraceMind/internal/tool"
)

type Report struct {
	Incident            string                 `json:"incident"`
	State               State                  `json:"state"`
	StateHistory        []State                `json:"state_history"`
	ToolCalls           int                    `json:"tool_calls"`
	Summary             string                 `json:"summary"`
	Hypotheses          []string               `json:"hypotheses"`
	Evidence            []evidence.Evidence    `json:"evidence"`
	Plans               []InvestigationPlan    `json:"plans,omitempty"`
	DiagnosisHypotheses []diagnosis.Hypothesis `json:"diagnosis_hypotheses,omitempty"`

	// Knowledge 保存最终使用的参考技能，便于审计来源。
	// 这些条目不属于 Evidence，不参与当前事故证据计数。
	Knowledge []SkillReference `json:"knowledge,omitempty"`
}

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
	planner      Planner
	maxToolCalls int
	maxTokens    int
	skills       *skills.Library
}

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

func newRuntime(tools *tool.Registry, client completionClient) *Runtime {
	return &Runtime{
		tools:   tools,
		client:  client,
		planner: NewPlanner(client),
	}
}

func (r *Runtime) SetPlanner(planner Planner) {
	if r != nil && planner != nil {
		r.planner = planner
	}
}

// SetMaxToolCalls 设置单次事件调查允许执行的最大工具调用次数。
func (r *Runtime) SetMaxToolCalls(max int) {
	if r != nil && max > 0 {
		r.maxToolCalls = max
	}
}

// SetMaxTokens 设置单次事件调查允许的估算 token 硬上限。
// 不设置或小于等于 0 时不启用 token 预算。
func (r *Runtime) SetMaxTokens(max int) {
	if r != nil && max > 0 {
		r.maxTokens = max
	}
}

// SetSkillsLibrary 设置可选的三级技能库。
// 不配置技能库时，Runtime 保留原有诊断能力。
func (r *Runtime) SetSkillsLibrary(lib *skills.Library) {
	if r != nil {
		r.skills = lib
	}
}

func (r *Runtime) Run(
	ctx context.Context,
	incident string,
) (*Report, error) {
	incident = strings.TrimSpace(incident)

	if incident == "" {
		return nil, errors.New("incident description cannot be empty")
	}
	if r == nil {
		return nil, errors.New("runtime cannot be nil")
	}
	if r.tools == nil {
		return nil, errors.New("tool registry cannot be nil")
	}
	if r.client == nil {
		return nil, errors.New("LLM client is not configured")
	}
	if r.planner == nil {
		return nil, errors.New("planner is not configured")
	}
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}

	maxCalls := r.maxToolCalls
	if maxCalls <= 0 {
		return nil, errors.New(
			"max tool calls is not configured: call SetMaxToolCalls with cfg.Agent.MaxToolCalls",
		)
	}

	definitions, err := r.tools.ToLLMDefinitions()
	if err != nil {
		return nil, fmt.Errorf("build tool definitions: %w", err)
	}

	// 每次事件调查都创建独立的证据存储和假设管理器。
	evidenceStore := evidence.NewStore()
	diagnosisManager := diagnosis.NewManager(evidenceStore)
	sm := NewStateMachine()

	report := &Report{
		Incident:            incident,
		Hypotheses:          []string{},
		Evidence:            []evidence.Evidence{},
		Plans:               []InvestigationPlan{},
		DiagnosisHypotheses: []diagnosis.Hypothesis{},
		Knowledge:           []SkillReference{},
	}

	toolCallsUsed := 0
	toolCallCount := make(map[string]int)

	// 调查预算耗尽或没有有价值的调查动作后，只允许生成最终诊断。
	forceFinal := false
	finalPromptAdded := false

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
		report.DiagnosisHypotheses = diagnosisManager.List()
		return report, err
	}

	if err := transition(StatePlanning); err != nil {
		return fail(err)
	}

	// 技能上下文：L1 索引常驻，L2 摘要按事故描述命中注入。
	// 技能属于参考知识，不创建当前事故 Evidence。
	var skillRefs []SkillReference
	var skillBriefs []SkillBrief
	var loader *skillLoader

	if r.skills != nil {
		loader = newSkillLoader(maxLoadedSkills)
	}

	report.Knowledge = skillRefs

	systemPrompt := `You are TraceMind, an evidence-driven incident diagnosis agent.

Investigate incidents using the available tools.
Base conclusions on actual tool results. Never invent observations.
Distinguish observed facts from hypotheses.
Do not claim a root cause is confirmed unless the evidence supports it.
Investigate downstream dependencies when evidence suggests they may be involved.
If evidence is insufficient, clearly state the uncertainty.

Follow the investigation plan provided for each round.
Only use the tool permitted by the current plan.
If the evidence is sufficient, return a final diagnosis without calling tools.

Reference skill knowledge is background guidance only.
It is not an observation from the current incident.
Never cite a skill or historical incident as proof that its cause applies here.
Use current incident tool results to support or refute root-cause hypotheses.

When finished, return valid JSON only:
{
  "summary": "Concise diagnosis including observed facts and uncertainty",
  "hypotheses": [
    "A plausible explanation that still needs verification"
  ]
}

The summary must be a string.
Hypotheses must be an array of strings.
Do not include Markdown code fences around the JSON.`

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{
			Role:    "user",
			Content: "Investigate this incident: " + incident,
		},
	}

	if r.skills != nil && loader != nil {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: r.skills.Index(),
		})

		skillMessage, refs, briefs := buildSkillContext(
			r.skills,
			buildKnowledgeQuery(incident, nil, nil),
			loader,
		)
		if skillMessage != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: skillMessage,
			})
		}

		skillRefs = refs
		skillBriefs = briefs
		report.Knowledge = skillRefs
	}

	// 每轮最多执行一个工具调用。
	// 额外轮次用于完成调查后的最终诊断。
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

		var plan InvestigationPlan
		var availableTools []llm.ToolDefinition

		// 仅在调查预算尚有剩余时调用规划器。
		if remaining > 0 && !forceFinal {
			candidates := make([]ToolCandidate, 0)

			for _, registered := range r.tools.List() {
				candidates = append(candidates, ToolCandidate{
					Name:        registered.Name(),
					Description: registered.Description(),
				})
			}

			plan, err = r.planner.Plan(PlanningInput{
				Incident:      incident,
				Evidence:      evidenceStore.List(),
				Hypotheses:    diagnosisManager.List(),
				Tools:         candidates,
				ToolCallCount: toolCallCount,
				Knowledge:     skillBriefs,
			})

			if err != nil {
				if errors.Is(err, ErrNoUsefulAction) {
					forceFinal = true
				} else if errors.Is(err, ErrPlannerDegraded) {
					// 模型输出/API 瞬态异常：不终止整个调查，改为优雅终局。
					fmt.Printf(
						"[TraceMind] planner degraded (%v); closing investigation\n",
						err,
					)
					forceFinal = true
				} else {
					return fail(fmt.Errorf("plan investigation: %w", err))
				}
			} else {
				availableTools = filterToolDefinitions(
					definitions,
					plan.ToolName,
				)

				if len(availableTools) == 0 {
					return fail(fmt.Errorf(
						"planner selected unknown tool %q",
						plan.ToolName,
					))
				}

				report.Plans = append(report.Plans, plan)

				planJSON, err := json.Marshal(plan)
				if err != nil {
					return fail(fmt.Errorf(
						"marshal investigation plan: %w",
						err,
					))
				}

				messages = append(messages, llm.Message{
					Role: "system",
					Content: fmt.Sprintf(
						"Investigation plan for round %d:\n%s\n"+
							"Only call the selected tool in this round. "+
							"Make at most one tool call. "+
							"If sufficient evidence already exists, "+
							"return the final JSON diagnosis instead.",
						round,
						string(planJSON),
					),
				})
			}
		}

		// 预算耗尽后不再向模型提供工具定义。
		if remaining <= 0 {
			forceFinal = true
		}

		// token 预算：超出硬上限后强制终局，最多再允许一次最终诊断调用。
		if r.maxTokens > 0 {
			estimated := estimateMessagesTokens(messages) +
				estimateToolDefinitionsTokens(availableTools)

			if estimated > r.maxTokens {
				if forceFinal {
					return fail(fmt.Errorf(
						"token budget exceeded: estimated %d > %d tokens",
						estimated,
						r.maxTokens,
					))
				}

				fmt.Printf(
					"[TraceMind] token budget exceeded "+
						"(estimated %d/%d); closing investigation\n",
					estimated,
					r.maxTokens,
				)
				forceFinal = true
				availableTools = nil
			}
		}

		if forceFinal && !finalPromptAdded {
			messages = append(messages, llm.Message{
				Role: "system",
				Content: "Investigation is now closed. Do not call tools. " +
					"Do not request additional observations. " +
					"Return the final JSON diagnosis using only the " +
					"evidence already collected. Explicitly state " +
					"important uncertainties and unverified causes.",
			})
			finalPromptAdded = true
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
			return fail(errors.New("LLM returned a nil completion"))
		}

		assistantMessage := completion.Message
		if assistantMessage.Role == "" {
			assistantMessage.Role = "assistant"
		}

		if len(assistantMessage.ToolCalls) == 0 {
			if err := applyFinalAnswer(
				report,
				assistantMessage.Content,
			); err != nil {
				return fail(err)
			}

			report.DiagnosisHypotheses = diagnosisManager.List()

			if err := transition(StateCompleted); err != nil {
				return fail(err)
			}

			report.ToolCalls = toolCallsUsed
			report.Knowledge = skillRefs
			return report, nil
		}

		calls := assistantMessage.ToolCalls

		// 工具调用消息必须紧接着收到每个 call 对应的工具响应。
		messages = append(messages, assistantMessage)

		// 最终诊断阶段禁止执行工具。
		if forceFinal || remaining <= 0 || plan.ToolName == "" {
			for _, call := range calls {
				messages = append(messages, llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content: "Tool call not executed: investigation is closed. " +
						"Return the final diagnosis using existing evidence.",
				})
			}

			forceFinal = true
			continue
		}

		// 强制执行调查计划：任何一个调用不符合计划时，
		// 本轮所有调用都不执行，并为每个调用补齐协议响应。
		validCalls := true
		for _, call := range calls {
			if call.Function.Name != plan.ToolName {
				validCalls = false
				break
			}
		}

		if !validCalls {
			for _, call := range calls {
				messages = append(messages, llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content: fmt.Sprintf(
						"Tool call not executed: this round permits only %q. "+
							"Follow the plan or return the final diagnosis.",
						plan.ToolName,
					),
				})
			}
			continue
		}

		// 一个调查轮次最多执行一个调用。
		allowedCount := 1
		if allowedCount > remaining {
			allowedCount = remaining
		}
		if allowedCount <= 0 {
			forceFinal = true
			continue
		}

		if err := transition(StateExecuting); err != nil {
			return fail(err)
		}

		// 即使工具执行失败，也计入调用预算。
		toolCallsUsed += allowedCount
		report.ToolCalls = toolCallsUsed
		toolCallCount[plan.ToolName]++

		execution, err := ExecuteToolCalls(
			ctx,
			r.tools,
			calls[:allowedCount],
		)
		if err != nil {
			return fail(fmt.Errorf("execute tool calls: %w", err))
		}
		if execution == nil {
			return fail(errors.New("tool executor returned nil result"))
		}

		// 以 tool_call_id 建立映射，避免依赖结果切片顺序。
		resultByCallID := make(map[string]llm.Message, len(execution.Messages))
		for _, message := range execution.Messages {
			if message.ToolCallID != "" {
				resultByCallID[message.ToolCallID] = message
			}
		}

		toolMessages := make([]llm.Message, 0, len(calls))

		for i, call := range calls {
			if i >= allowedCount {
				// 这些调用没有执行，只添加满足 API 协议所需的响应。
				toolMessages = append(toolMessages, llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content: "Tool call not executed: only one tool call is " +
						"permitted per investigation round. Use the actual " +
						"tool results and continue according to the plan.",
				})
				continue
			}

			resultMessage, ok := resultByCallID[call.ID]
			if !ok {
				toolMessages = append(toolMessages, llm.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content: "Tool execution returned no response for this call. " +
						"Treat the observation as unavailable and continue " +
						"only with verified evidence.",
				})
				continue
			}

			resultMessage.Role = "tool"
			resultMessage.ToolCallID = call.ID
			toolMessages = append(toolMessages, resultMessage)
		}

		// 所有工具响应必须紧跟在 assistant 工具调用消息之后。
		messages = append(messages, toolMessages...)

		// 只打印真正执行的调用，并通过 ID 获取其真实结果。
		for i := 0; i < allowedCount; i++ {
			call := calls[i]

			fmt.Printf(
				"[TraceMind] tool=%s arguments=%s\n",
				call.Function.Name,
				call.Function.Arguments,
			)

			resultMessage, ok := resultByCallID[call.ID]
			if ok {
				fmt.Printf(
					"[TraceMind] result=%s\n",
					resultMessage.Content,
				)
			} else {
				fmt.Printf(
					"[TraceMind] result=%s\n",
					"Tool execution returned no response for this call.",
				)
			}
		}

		// 记录技能加载状态：load_skill 计入加载预算，容量满时按 LRU 淘汰。
		// 技能内容作为参考知识注入后续轮次，不进入证据存储。
		if plan.ToolName == "load_skill" && loader != nil {
			for _, call := range calls[:allowedCount] {
				name := parseLoadSkillName(call.Function.Arguments)
				if name == "" {
					continue
				}

				if evicted := loader.Load(name); evicted != "" {
					fmt.Printf(
						"[TraceMind] skill loaded=%q evicted=%q (max %d)\n",
						name,
						evicted,
						loader.max,
					)
				} else {
					fmt.Printf("[TraceMind] skill loaded=%q\n", name)
				}
			}
		}

		// 保存当前事故证据，并在本次事件范围内生成唯一 ID。
		for _, item := range execution.Evidence {
			item.ID = fmt.Sprintf("ev-%03d", len(report.Evidence)+1)
			if item.CreatedAt.IsZero() {
				item.CreatedAt = time.Now().UTC()
			}

			if err := evidenceStore.Add(item); err != nil {
				return fail(fmt.Errorf("store evidence: %w", err))
			}
			report.Evidence = append(report.Evidence, item)
		}

		// 工具调用轮次结束后，再统一评估假设。
		// 评估失败不终止调查：保留上一轮假设评估，仅打印告警。
		if len(execution.Evidence) > 0 {
			if err := evaluateHypotheses(
				ctx,
				r.client,
				incident,
				evidenceStore,
				diagnosisManager,
			); err != nil {
				fmt.Printf(
					"[TraceMind] hypothesis evaluation degraded (%v); keeping previous assessment\n",
					err,
				)
			} else {
				report.DiagnosisHypotheses = diagnosisManager.List()

				hypothesisJSON, err := json.Marshal(
					report.DiagnosisHypotheses,
				)
				if err != nil {
					return fail(fmt.Errorf("marshal hypotheses: %w", err))
				}

				messages = append(messages, llm.Message{
					Role: "system",
					Content: "Updated structured hypothesis assessment:\n" +
						string(hypothesisJSON) +
						"\nUse this assessment together with actual tool results. " +
						"A supported hypothesis is not automatically a " +
						"confirmed root cause.",
				})
			}
		}

		// 新证据可能改变技能匹配方向，因此重新计算技能上下文。
		// 技能库异常时保留上一轮知识，不能影响事故诊断主流程。
		if r.skills != nil && loader != nil && len(execution.Evidence) > 0 {
			skillMessage, refs, briefs := buildSkillContext(
				r.skills,
				buildKnowledgeQuery(
					incident,
					evidenceStore.List(),
					diagnosisManager.List(),
				),
				loader,
			)

			if skillMessage != "" {
				messages = append(messages, llm.Message{
					Role:    "system",
					Content: skillMessage,
				})
			}

			skillRefs = mergeSkillRefs(skillRefs, refs)
			skillBriefs = briefs
			report.Knowledge = skillRefs
		}

		if err := transition(StateObserving); err != nil {
			return fail(err)
		}

		// 预算耗尽后，下一轮只请求最终诊断。
		if toolCallsUsed >= maxCalls {
			forceFinal = true
		}
	}

	return fail(fmt.Errorf(
		"agent did not produce a final answer within %d rounds",
		maxRounds,
	))
}

func filterToolDefinitions(
	definitions []llm.ToolDefinition,
	allowedName string,
) []llm.ToolDefinition {
	result := make([]llm.ToolDefinition, 0, 1)

	for _, definition := range definitions {
		data, err := json.Marshal(definition)
		if err != nil {
			continue
		}

		var object map[string]any
		if err := json.Unmarshal(data, &object); err != nil {
			continue
		}

		name := ""
		if function, ok := object["function"].(map[string]any); ok {
			name, _ = function["name"].(string)
		}
		if name == "" {
			name, _ = object["name"].(string)
		}

		if name == allowedName {
			result = append(result, definition)
		}
	}

	return result
}

func applyFinalAnswer(report *Report, content string) error {
	if report == nil {
		return errors.New("report cannot be nil")
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("LLM returned an empty final answer")
	}

	// 容忍模型把 JSON 包在 Markdown 围栏里（```json ... ```）。
	content = stripCodeFences(content)

	var result struct {
		Summary    string   `json:"summary"`
		Hypotheses []string `json:"hypotheses"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		if repaired := repairJSON(content); repaired != content {
			if repairErr := json.Unmarshal([]byte(repaired), &result); repairErr == nil {
				// 修复成功，继续使用 result。
			} else {
				return fmt.Errorf(
					"parse final diagnosis JSON: %v (repair also failed: %v); response=%q",
					err,
					repairErr,
					truncateText(content, 500),
				)
			}
		} else {
			return fmt.Errorf(
				"parse final diagnosis JSON: %w; response=%q",
				err,
				truncateText(content, 500),
			)
		}
	}

	result.Summary = strings.TrimSpace(result.Summary)
	if result.Summary == "" {
		return errors.New("final diagnosis summary cannot be empty")
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

// stripCodeFences 去掉 ```json ... ``` 之类的 Markdown 围栏，只保留内部 JSON。
func stripCodeFences(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}

	if idx := strings.Index(trimmed, "\n"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimSuffix(trimmed, "```")

	return strings.TrimSpace(trimmed)
}

// buildKnowledgeQuery 将事故描述、最新证据和候选假设组合为检索查询。
// 查询内容用于寻找参考资料，不会被写入 Evidence Store。
func buildKnowledgeQuery(
	incident string,
	items []evidence.Evidence,
	hypotheses []diagnosis.Hypothesis,
) string {
	var parts []string

	if incident = strings.TrimSpace(incident); incident != "" {
		parts = append(parts, incident)
	}

	// 限制查询长度，只取最近的五条证据。
	start := 0
	if len(items) > 5 {
		start = len(items) - 5
	}

	for _, item := range items[start:] {
		content := strings.TrimSpace(item.Content)
		if content != "" {
			parts = append(parts, content)
		}
	}

	// 未被反驳的假设仅作为检索词，不能视为已确认事实。
	count := 0
	for _, hypothesis := range hypotheses {
		if hypothesis.Status == diagnosis.StatusRefuted {
			continue
		}

		title := strings.TrimSpace(hypothesis.Title)
		description := strings.TrimSpace(hypothesis.Description)

		if title == "" && description == "" {
			continue
		}

		parts = append(parts, title+" "+description)
		count++
		if count >= 3 {
			break
		}
	}

	return strings.Join(parts, "\n")
}
