package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/liocoder/TraceMind/internal/diagnosis"
	"github.com/liocoder/TraceMind/internal/evidence"
)

var (
	ErrNoAvailableTools = errors.New("no available investigation tools")
	ErrNoUsefulAction   = errors.New("no useful investigation action found")
)

// ToolCandidate describes a tool that the planner may select.
// It intentionally does not depend on the concrete tool registry.
type ToolCandidate struct {
	Name        string
	Description string
}

// PlanningInput contains the current investigation context.
type PlanningInput struct {
	Incident string

	Evidence      []evidence.Evidence
	Hypotheses    []diagnosis.Hypothesis
	Tools         []ToolCandidate
	ToolCallCount map[string]int
}

// InvestigationPlan describes the next investigation action.
type InvestigationPlan struct {
	ToolName            string `json:"tool_name"`
	Rationale           string `json:"rationale"`
	TargetHypothesisID  string `json:"target_hypothesis_id,omitempty"`
	ExpectedObservation string `json:"expected_observation"`
}

// Planner selects the next investigation action.
type Planner interface {
	Plan(input PlanningInput) (InvestigationPlan, error)
}

// RulePlanner makes deterministic, explainable investigation decisions.
type RulePlanner struct{}

func NewPlanner() *RulePlanner {
	return &RulePlanner{}
}

type scoredTool struct {
	tool   ToolCandidate
	score  int
	reason string
}

// Plan selects a tool based on the incident, collected evidence,
// current hypotheses, and previous tool usage.
func (p *RulePlanner) Plan(input PlanningInput) (InvestigationPlan, error) {
	if p == nil {
		return InvestigationPlan{}, errors.New("planner cannot be nil")
	}

	incident := strings.TrimSpace(input.Incident)
	if incident == "" {
		return InvestigationPlan{}, errors.New("incident cannot be empty")
	}
	if len(input.Tools) == 0 {
		return InvestigationPlan{}, ErrNoAvailableTools
	}

	tools := make([]ToolCandidate, 0, len(input.Tools))
	seen := make(map[string]struct{}, len(input.Tools))

	for _, candidate := range input.Tools {
		candidate.Name = strings.TrimSpace(candidate.Name)
		candidate.Description = strings.TrimSpace(candidate.Description)

		if candidate.Name == "" {
			continue
		}
		if _, exists := seen[candidate.Name]; exists {
			continue
		}

		seen[candidate.Name] = struct{}{}
		tools = append(tools, candidate)
	}

	if len(tools) == 0 {
		return InvestigationPlan{}, ErrNoAvailableTools
	}

	scored := make([]scoredTool, 0, len(tools))
	for _, candidate := range tools {
		score, reason := scoreTool(candidate, input)
		used := input.ToolCallCount[candidate.Name]

		// Prefer tools that have not yet been called, all else equal.
		score -= used * 3

		scored = append(scored, scoredTool{
			tool:   candidate,
			score:  score,
			reason: reason,
		})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].tool.Name < scored[j].tool.Name
	})

	best := scored[0]
	if best.score < 0 {
		return InvestigationPlan{}, ErrNoUsefulAction
	}

	targetID := selectTargetHypothesis(input.Hypotheses)
	expected := expectedObservation(best.tool, best.reason)

	return InvestigationPlan{
		ToolName:            best.tool.Name,
		Rationale:           best.reason,
		TargetHypothesisID:  targetID,
		ExpectedObservation: expected,
	}, nil
}

func scoreTool(candidate ToolCandidate, input PlanningInput) (int, string) {
	name := strings.ToLower(candidate.Name)
	description := strings.ToLower(candidate.Description)
	toolText := name + " " + description

	score := 0
	reasons := make([]string, 0, 3)

	// With no observations yet, start with logs when available.
	if len(input.Evidence) == 0 {
		if containsAny(toolText, "log", "日志", "error", "错误") {
			score += 10
			reasons = append(reasons, "尚无调查证据，优先查询日志以定位异常线索")
		}
		if containsAny(toolText, "metric", "指标", "monitor", "监控") {
			score += 6
			reasons = append(reasons, "尚无调查证据，可先获取服务运行指标")
		}
	}

	observations := strings.ToLower(input.Incident)
	for _, item := range input.Evidence {
		observations += " " + strings.ToLower(item.Source)
		observations += " " + strings.ToLower(item.Content)
	}

	if containsAny(observations,
		"connection pool", "连接池", "pool exhausted", "连接耗尽") {
		if containsAny(toolText, "metric", "指标", "connection", "连接池") {
			score += 12
			reasons = append(reasons, "已有线索指向连接池问题，优先核查连接池及等待指标")
		}
	}

	if containsAny(observations,
		"timeout", "超时", "502", "error", "异常", "failed", "失败") {
		if containsAny(toolText, "log", "日志", "error", "错误") {
			score += 8
			reasons = append(reasons, "当前故障存在错误或超时，查询日志有助于确认具体异常")
		}
		if containsAny(toolText, "metric", "指标", "latency", "延迟") {
			score += 5
			reasons = append(reasons, "当前故障涉及错误或超时，指标可用于判断影响范围")
		}
	}

	for _, hypothesis := range input.Hypotheses {
		if hypothesis.Status == diagnosis.StatusRefuted {
			continue
		}

		hypothesisText := strings.ToLower(
			hypothesis.Title + " " + hypothesis.Description,
		)

		if containsAny(hypothesisText,
			"connection pool", "连接池", "database", "数据库") &&
			containsAny(toolText, "metric", "指标", "database", "数据库", "connection", "连接") {
			score += 4
			reasons = append(reasons, "该工具有助于验证数据库或连接池相关假设")
		}

		if containsAny(hypothesisText,
			"downstream", "下游", "payment", "支付") &&
			containsAny(toolText, "log", "日志", "trace", "链路", "dependency", "依赖") {
			score += 4
			reasons = append(reasons, "该工具有助于验证下游依赖相关假设")
		}
	}

	reason := "该工具与当前调查上下文的匹配度最高"
	if len(reasons) > 0 {
		reason = strings.Join(uniqueStrings(reasons), "；")
	}

	return score, reason
}

func selectTargetHypothesis(hypotheses []diagnosis.Hypothesis) string {
	candidates := make([]diagnosis.Hypothesis, 0, len(hypotheses))
	for _, h := range hypotheses {
		if h.Status == diagnosis.StatusRefuted {
			continue
		}
		candidates = append(candidates, h)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Confidence != candidates[j].Confidence {
			return candidates[i].Confidence > candidates[j].Confidence
		}
		return candidates[i].ID < candidates[j].ID
	})

	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].ID
}

func expectedObservation(tool ToolCandidate, rationale string) string {
	description := strings.TrimSpace(tool.Description)
	if description == "" {
		return fmt.Sprintf("获取工具 %q 返回的调查信息，用于验证当前判断", tool.Name)
	}
	return fmt.Sprintf("通过“%s”获取新的观测，以检验：%s", description, rationale)
}

func containsAny(text string, keywords ...string) bool {
	for _, keyword := range keywords {
		if strings.Contains(text, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))

	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}
