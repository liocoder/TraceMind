package agent

import (
	"errors"
	"strings"

	"github.com/liocoder/TraceMind/internal/diagnosis"
	"github.com/liocoder/TraceMind/internal/evidence"
)

var (
	ErrNoAvailableTools = errors.New("no available investigation tools")
	ErrNoUsefulAction   = errors.New("no useful investigation action found")
)

type ToolCandidate struct {
	Name        string
	Description string
}

type PlanningInput struct {
	Incident      string
	Evidence      []evidence.Evidence
	Hypotheses    []diagnosis.Hypothesis
	Tools         []ToolCandidate
	ToolCallCount map[string]int
}

type InvestigationPlan struct {
	ToolName            string `json:"tool_name"`
	Rationale           string `json:"rationale"`
	TargetHypothesisID  string `json:"target_hypothesis_id,omitempty"`
	ExpectedObservation string `json:"expected_observation"`
}

type Planner interface {
	Plan(input PlanningInput) (InvestigationPlan, error)
}

type RulePlanner struct{}

func NewPlanner() *RulePlanner {
	return &RulePlanner{}
}

func (p *RulePlanner) Plan(input PlanningInput) (InvestigationPlan, error) {
	if len(input.Tools) == 0 {
		return InvestigationPlan{}, ErrNoAvailableTools
	}

	toolByName := make(map[string]ToolCandidate, len(input.Tools))
	for _, candidate := range input.Tools {
		toolByName[candidate.Name] = candidate
	}

	hasMetrics := false
	hasLogs := false

	for _, item := range input.Evidence {
		source := strings.ToLower(item.Source)
		content := strings.ToLower(item.Content)

		if strings.Contains(source, "metrics") &&
			!strings.Contains(content, "no simulated metrics available") &&
			!strings.Contains(content, "status=unknown") {
			hasMetrics = true
		}

		if strings.Contains(source, "log") {
			hasLogs = true
		}
	}

	incident := strings.ToLower(input.Incident)
	hasLatencyIssue := containsAny(incident,
		"latency", "p99", "p95", "延迟", "耗时", "变慢")
	hasErrorIssue := containsAny(incident,
		"502", "500", "timeout", "error", "异常", "超时", "错误")
	hasDependencyClue := containsAny(incident,
		"下游", "依赖", "payment", "database", "数据库", "支付")

	if !hasMetrics {
		if candidate, ok := toolByName["query_metrics"]; ok {
			return InvestigationPlan{
				ToolName:            candidate.Name,
				Rationale:           "先获取服务运行指标，确认延迟、错误率和连接池使用情况。",
				ExpectedObservation: "获得服务健康指标，判断是否存在连接池饱和、错误率升高或其他资源瓶颈。",
			}, nil
		}
	}

	if !hasLogs {
		if candidate, ok := toolByName["search_logs"]; ok {
			return InvestigationPlan{
				ToolName:            candidate.Name,
				Rationale:           "通过错误日志定位超时和异常发生的具体环节。",
				ExpectedObservation: "确认是否出现请求超时、连接池耗尽或下游依赖异常等日志线索。",
			}, nil
		}
	}

	var targetID string
	for _, hypothesis := range input.Hypotheses {
		if hypothesis.Status == diagnosis.StatusRefuted {
			continue
		}
		if hypothesis.ID != "" {
			targetID = hypothesis.ID
			break
		}
	}

	if hasDependencyClue || evidenceMentionsDependency(input.Evidence) {
		if candidate, ok := toolByName["query_metrics"]; ok &&
			input.ToolCallCount[candidate.Name] < 2 {
			return InvestigationPlan{
				ToolName:            candidate.Name,
				Rationale:           "现有证据提示可能存在下游依赖问题，需要进一步核对相关服务的健康状态。",
				TargetHypothesisID:  targetID,
				ExpectedObservation: "检查相关依赖的延迟和错误率，判断下游劣化是否可能导致订单服务连接池耗尽。",
			}, nil
		}
	}

	if !hasLogs {
		if candidate, ok := toolByName["search_logs"]; ok {
			return InvestigationPlan{
				ToolName:            candidate.Name,
				Rationale:           "当前缺少日志证据，需要定位异常发生的具体环节。",
				TargetHypothesisID:  targetID,
				ExpectedObservation: "获取与超时、连接池或下游调用有关的错误日志。",
			}, nil
		}
	}

	for _, candidate := range input.Tools {
		if input.ToolCallCount[candidate.Name] == 0 {
			return InvestigationPlan{
				ToolName:            candidate.Name,
				Rationale:           "补充尚未执行的调查手段，验证现有诊断是否存在遗漏。",
				TargetHypothesisID:  targetID,
				ExpectedObservation: "获取新的独立证据，以验证或排除现有根因假设。",
			}, nil
		}
	}

	if hasLatencyIssue || hasErrorIssue {
		return InvestigationPlan{}, ErrNoUsefulAction
	}

	return InvestigationPlan{}, ErrNoUsefulAction
}

func containsAny(value string, keywords ...string) bool {
	for _, keyword := range keywords {
		if strings.Contains(value, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func evidenceMentionsDependency(items []evidence.Evidence) bool {
	for _, item := range items {
		content := strings.ToLower(item.Content)
		if containsAny(content,
			"payment-service",
			"downstream",
			"下游",
			"依赖服务",
			"latency elevated",
		) {
			return true
		}
	}
	return false
}
