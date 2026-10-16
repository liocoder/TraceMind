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

	// 终局报告的证据链结构：每条结论对应的已观察事实、支持/反驳证据、
	// 未验证事项、置信程度与下一步验证动作。
	// 这些字段由模型在终局诊断中填写，供审计与评测使用。
	ObservedFacts      []string `json:"observed_facts,omitempty"`
	SupportingEvidence []string `json:"supporting_evidence,omitempty"`
	RefutingEvidence   []string `json:"refuting_evidence,omitempty"`
	UnverifiedIssues   []string `json:"unverified_issues,omitempty"`
	Confidence         string   `json:"confidence,omitempty"`
	NextSteps          []string `json:"next_steps,omitempty"`

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
	singleShot   bool
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

// SetSingleShot 开启单轮 LLM 基线模式：
// 不调用规划器、不执行任何工具，直接请求一次终局诊断。
// 用于评测对照（评估多轮调查与技能机制的实际增益）。
func (r *Runtime) SetSingleShot(enabled bool) {
	if r != nil {
		r.singleShot = enabled
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

	// 后续调查循环（规划、预算、执行、证据与假设、终局）由
	// investigation 结构体统一承载，Run 只负责校验与编排。
	return newInvestigation(r, ctx, incident, maxCalls, definitions).run()
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
		Summary            string   `json:"summary"`
		Hypotheses         []string `json:"hypotheses"`
		ObservedFacts      []string `json:"observed_facts"`
		SupportingEvidence []string `json:"supporting_evidence"`
		RefutingEvidence   []string `json:"refuting_evidence"`
		UnverifiedIssues   []string `json:"unverified_issues"`
		Confidence         string   `json:"confidence"`
		NextSteps          []string `json:"next_steps"`
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

	// 证据链结构字段：容忍缺失，缺失时归一为空数组。
	if result.ObservedFacts == nil {
		result.ObservedFacts = []string{}
	}
	if result.SupportingEvidence == nil {
		result.SupportingEvidence = []string{}
	}
	if result.RefutingEvidence == nil {
		result.RefutingEvidence = []string{}
	}
	if result.UnverifiedIssues == nil {
		result.UnverifiedIssues = []string{}
	}
	if result.NextSteps == nil {
		result.NextSteps = []string{}
	}
	for i, item := range result.ObservedFacts {
		result.ObservedFacts[i] = strings.TrimSpace(item)
	}
	for i, item := range result.SupportingEvidence {
		result.SupportingEvidence[i] = strings.TrimSpace(item)
	}
	for i, item := range result.RefutingEvidence {
		result.RefutingEvidence[i] = strings.TrimSpace(item)
	}
	for i, item := range result.UnverifiedIssues {
		result.UnverifiedIssues[i] = strings.TrimSpace(item)
	}
	for i, item := range result.NextSteps {
		result.NextSteps[i] = strings.TrimSpace(item)
	}

	report.ObservedFacts = result.ObservedFacts
	report.SupportingEvidence = result.SupportingEvidence
	report.RefutingEvidence = result.RefutingEvidence
	report.UnverifiedIssues = result.UnverifiedIssues
	report.Confidence = strings.TrimSpace(result.Confidence)
	report.NextSteps = result.NextSteps
	return nil
}
