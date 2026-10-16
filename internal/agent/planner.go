package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liocoder/TraceMind/internal/diagnosis"
	"github.com/liocoder/TraceMind/internal/evidence"
	"github.com/liocoder/TraceMind/internal/llm"
)

var (
	ErrNoAvailableTools = errors.New("no available investigation tools")
	ErrNoUsefulAction   = errors.New("no useful investigation action found")
)

type ToolCandidate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type PlanningInput struct {
	Incident      string
	Evidence      []evidence.Evidence
	Hypotheses    []diagnosis.Hypothesis
	Tools         []ToolCandidate
	ToolCallCount map[string]int
	Knowledge     []SkillBrief
}

type InvestigationPlan struct {
	ToolName            string `json:"tool_name"`
	Rationale           string `json:"rationale"`
	TargetHypothesisID  string `json:"target_hypothesis_id,omitempty"`
	ExpectedObservation string `json:"expected_observation"`
}

// ErrPlannerDegraded 表示规划器因模型输出或 LLM API 异常而无法给出可用计划。
// 与配置类错误不同，它不应终止整个调查，而应触发优雅终局（forceFinal）。
var ErrPlannerDegraded = errors.New("planner degraded")

// plannerDegraded 包装模型输出类错误，使 runtime 可用 errors.Is 识别，
// 同时保留底层错误链。cause 为 nil 时只包装 ErrPlannerDegraded。
func plannerDegraded(cause error, format string, args ...any) error {
	if cause == nil {
		return fmt.Errorf(format+": %w", append(args, ErrPlannerDegraded)...)
	}
	return fmt.Errorf(format+": %w: %w", append(args, cause, ErrPlannerDegraded)...)
}

type Planner interface {
	Plan(input PlanningInput) (InvestigationPlan, error)
	PlanContext(ctx context.Context, input PlanningInput) (InvestigationPlan, error)
}

type plannerClient interface {
	Complete(
		ctx context.Context,
		messages []llm.Message,
		tools []llm.ToolDefinition,
	) (*llm.Completion, error)
}

// LLMPlanner 通过模型推理选择下一步调查工具。
// 模型只负责提出计划，实际工具执行由 Runtime 控制。
type LLMPlanner struct {
	client plannerClient
}

// NewPlanner 创建 LLM Planner。
// 不传 Client 时，只有在实际调用 Plan 时才会返回 Client 未配置错误。
func NewPlanner(clients ...plannerClient) *LLMPlanner {
	var client plannerClient
	if len(clients) > 0 {
		client = clients[0]
	}
	return &LLMPlanner{client: client}
}

func NewLLMPlanner(client plannerClient) *LLMPlanner {
	return NewPlanner(client)
}

// Plan 保留原有 Planner 接口，兼容尚未改成 PlanContext 的调用方。
func (p *LLMPlanner) Plan(input PlanningInput) (InvestigationPlan, error) {
	return p.PlanContext(context.Background(), input)
}

// PlanContext 允许 Runtime 传入 context，支持请求取消与超时控制。
func (p *LLMPlanner) PlanContext(
	ctx context.Context,
	input PlanningInput,
) (InvestigationPlan, error) {
	if strings.TrimSpace(input.Incident) == "" {
		return InvestigationPlan{}, errors.New("incident description cannot be empty")
	}

	available := make(map[string]ToolCandidate, len(input.Tools))
	for _, candidate := range input.Tools {
		candidate.Name = strings.TrimSpace(candidate.Name)
		if candidate.Name == "" {
			continue
		}
		available[candidate.Name] = candidate
	}

	if len(available) == 0 {
		return InvestigationPlan{}, ErrNoAvailableTools
	}

	if p == nil || p.client == nil {
		return InvestigationPlan{}, errors.New("LLM planner client is not configured")
	}

	// 只向模型传入去重后的工具列表，避免同名工具产生歧义。
	tools := make([]ToolCandidate, 0, len(available))
	for _, candidate := range input.Tools {
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			continue
		}
		if _, ok := available[name]; !ok {
			continue
		}
		tools = append(tools, available[name])
		delete(available, name)
	}

	// 恢复可用工具映射，供模型输出校验使用。
	available = make(map[string]ToolCandidate, len(tools))
	for _, candidate := range tools {
		available[candidate.Name] = candidate
	}

	payload := struct {
		Incident      string                  `json:"incident"`
		Evidence      []evidence.Evidence     `json:"evidence"`
		Hypotheses    []diagnosis.Hypothesis  `json:"hypotheses"`
		Tools         []ToolCandidate         `json:"available_tools"`
		ToolCallCount map[string]int          `json:"tool_call_count"`
		Knowledge     []plannerKnowledgeBrief `json:"skill_knowledge,omitempty"`
	}{
		Incident:      input.Incident,
		Evidence:      input.Evidence,
		Hypotheses:    input.Hypotheses,
		Tools:         tools,
		ToolCallCount: input.ToolCallCount,
	}

	for _, brief := range input.Knowledge {
		content := brief.Description
		if brief.Content != "" {
			content = brief.Content
		}

		payload.Knowledge = append(payload.Knowledge, plannerKnowledgeBrief{
			Name:             brief.Name,
			Level:            brief.Level,
			Content:          truncateText(content, 2500),
			RecommendedTools: brief.RecommendedTools,
		})
	}

	inputJSON, err := json.Marshal(payload)
	if err != nil {
		return InvestigationPlan{}, fmt.Errorf("encode planner input: %w", err)
	}

	systemPrompt := `You are the investigation planner for TraceMind, an incident diagnosis agent.

Choose exactly ONE useful next investigation tool, or return NO_ACTION when further investigation is not useful.

You receive the incident description, observations collected from tools, current hypotheses, available tools, tool-call counts, and optional skill knowledge.

Rules:
1. Choose based on the incident, actual observations, and unresolved uncertainties.
2. Do not assume a root cause merely because it is mentioned in the incident or a skill.
3. Skill knowledge is guidance, not evidence from the current incident.
4. Prefer a tool that distinguishes competing hypotheses or verifies a prediction.
5. Avoid repeating a query that has already provided sufficient information.
6. Select only an exact tool name from available_tools.
7. Never invent a tool, observation, hypothesis ID, or tool result.
8. If useful investigation remains possible, continue investigating.
9. If evidence is sufficient or no available tool can reduce uncertainty, return NO_ACTION.
10. Return valid JSON only. Do not use Markdown fences.

For a tool call, return:
{
"action": "CALL_TOOL",
"tool_name": "exact available tool name",
"rationale": "why this is the most useful next step",
"target_hypothesis_id": "an existing hypothesis ID or empty string",
"expected_observation": "what evidence the call should seek"
}

For no further action, return:
{
"action": "NO_ACTION",
"tool_name": "",
"rationale": "why no further tool call is useful",
"target_hypothesis_id": "",
"expected_observation": ""
}`

	completion, err := p.client.Complete(ctx, []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: string(inputJSON)},
	}, nil)
	if err != nil {
		return InvestigationPlan{}, plannerDegraded(
			err,
			"LLM planning request",
		)
	}
	if completion == nil {
		return InvestigationPlan{}, plannerDegraded(
			nil,
			"LLM planner returned nil completion",
		)
	}

	content := strings.TrimSpace(completion.Message.Content)
	if content == "" {
		return InvestigationPlan{}, plannerDegraded(
			nil,
			"LLM planner returned empty content",
		)
	}

	var result struct {
		Action              string `json:"action"`
		ToolName            string `json:"tool_name"`
		Rationale           string `json:"rationale"`
		TargetHypothesisID  string `json:"target_hypothesis_id"`
		ExpectedObservation string `json:"expected_observation"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		if repaired := repairJSON(content); repaired != content {
			if repairErr := json.Unmarshal([]byte(repaired), &result); repairErr == nil {
				// 修复成功，继续使用 result。
			} else {
				return InvestigationPlan{}, plannerDegraded(
					repairErr,
					"decode LLM planning response (repair failed); response=%q",
					truncateText(content, 500),
				)
			}
		} else {
			return InvestigationPlan{}, plannerDegraded(
				err,
				"decode LLM planning response; response=%q",
				truncateText(content, 500),
			)
		}
	}

	switch strings.ToUpper(strings.TrimSpace(result.Action)) {
	case "NO_ACTION":
		return InvestigationPlan{}, ErrNoUsefulAction
	case "CALL_TOOL":
		// 继续校验工具名及计划内容。
	default:
		return InvestigationPlan{}, plannerDegraded(
			nil,
			"LLM planner returned unsupported action %q",
			result.Action,
		)
	}

	result.ToolName = strings.TrimSpace(result.ToolName)
	if _, ok := available[result.ToolName]; !ok {
		return InvestigationPlan{}, plannerDegraded(
			nil,
			"LLM planner selected unavailable tool %q",
			result.ToolName,
		)
	}

	result.Rationale = strings.TrimSpace(result.Rationale)
	result.ExpectedObservation = strings.TrimSpace(result.ExpectedObservation)
	result.TargetHypothesisID = strings.TrimSpace(result.TargetHypothesisID)

	if result.Rationale == "" {
		return InvestigationPlan{}, plannerDegraded(
			nil,
			"LLM planner returned empty rationale",
		)
	}
	if result.ExpectedObservation == "" {
		return InvestigationPlan{}, plannerDegraded(
			nil,
			"LLM planner returned empty expected_observation",
		)
	}

	if result.TargetHypothesisID != "" {
		found := false
		for _, hypothesis := range input.Hypotheses {
			if hypothesis.ID == result.TargetHypothesisID {
				found = true
				break
			}
		}
		if !found {
			return InvestigationPlan{}, plannerDegraded(
				nil,
				"LLM planner referenced unknown hypothesis ID %q",
				result.TargetHypothesisID,
			)
		}
	}

	return InvestigationPlan{
		ToolName:            result.ToolName,
		Rationale:           result.Rationale,
		TargetHypothesisID:  result.TargetHypothesisID,
		ExpectedObservation: result.ExpectedObservation,
	}, nil
}

type plannerKnowledgeBrief struct {
	Name             string   `json:"name"`
	Level            string   `json:"level"`
	Content          string   `json:"content"`
	RecommendedTools []string `json:"recommended_tools,omitempty"`
}

func truncateText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}

	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}

	return string(runes[:maxRunes]) + "..."
}

// 以下辅助函数保留，以免项目中其他代码仍有引用。
func selectTargetHypothesis(hypotheses []diagnosis.Hypothesis) string {
	bestID := ""
	bestPriority := -1
	bestConfidence := 2.0

	for _, hypothesis := range hypotheses {
		if hypothesis.ID == "" || hypothesis.Status == diagnosis.StatusRefuted {
			continue
		}

		priority := 0
		switch hypothesis.Status {
		case diagnosis.StatusProposed:
			priority = 3
		case diagnosis.StatusInconclusive:
			priority = 2
		case diagnosis.StatusSupported:
			priority = 1
		default:
			continue
		}

		if priority > bestPriority ||
			(priority == bestPriority && hypothesis.Confidence < bestConfidence) {
			bestID = hypothesis.ID
			bestPriority = priority
			bestConfidence = hypothesis.Confidence
		}
	}

	return bestID
}

func findHypothesis(
	hypotheses []diagnosis.Hypothesis,
	id string,
) *diagnosis.Hypothesis {
	for i := range hypotheses {
		if id != "" && hypotheses[i].ID == id {
			return &hypotheses[i]
		}
	}
	return nil
}

func newPlan(
	toolName string,
	rationale string,
	targetHypothesisID string,
	expectedObservation string,
) InvestigationPlan {
	return InvestigationPlan{
		ToolName:            toolName,
		Rationale:           rationale,
		TargetHypothesisID:  targetHypothesisID,
		ExpectedObservation: expectedObservation,
	}
}

func containsAny(value string, keywords ...string) bool {
	value = strings.ToLower(value)
	for _, keyword := range keywords {
		if strings.Contains(value, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func evidenceMentionsDependency(items []evidence.Evidence) bool {
	for _, item := range items {
		if containsAny(
			item.Content,
			"payment-service",
			"downstream",
			"下游",
			"依赖服务",
		) {
			return true
		}
	}
	return false
}
