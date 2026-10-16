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
)

// investigation 承载一次事故调查的完整状态与循环逻辑。
// 它将原先集中在 Runtime.Run 中的局部变量与闭包收敛为结构体字段，
// 并把一轮调查拆成职责清晰的步骤，便于阅读、测试与复用。
type investigation struct {
	r                *Runtime
	ctx              context.Context
	incident         string
	definitions      []llm.ToolDefinition
	evidenceStore    evidence.Store
	diagnosisManager *diagnosis.Manager
	sm               *StateMachine
	report           *Report
	messages         []llm.Message
	toolCallsUsed    int
	toolCallCount    map[string]int
	forceFinal       bool
	finalPromptAdded bool
	maxCalls         int
	maxRounds        int
	skillRefs        []SkillReference
	skillBriefs      []SkillBrief
	loader           *skillLoader
}

func newInvestigation(
	r *Runtime,
	ctx context.Context,
	incident string,
	maxCalls int,
	definitions []llm.ToolDefinition,
) *investigation {
	// 每次事件调查都创建独立的证据存储和假设管理器。
	evidenceStore := evidence.NewStore()

	inv := &investigation{
		r:                r,
		ctx:              ctx,
		incident:         incident,
		definitions:      definitions,
		evidenceStore:    evidenceStore,
		diagnosisManager: diagnosis.NewManager(evidenceStore),
		sm:               NewStateMachine(),
		maxCalls:         maxCalls,
		maxRounds:        maxCalls + 4,
		toolCallCount:    make(map[string]int),
		report: &Report{
			Incident:            incident,
			Hypotheses:          []string{},
			Evidence:            []evidence.Evidence{},
			Plans:               []InvestigationPlan{},
			DiagnosisHypotheses: []diagnosis.Hypothesis{},
			Knowledge:           []SkillReference{},
			ObservedFacts:       []string{},
			SupportingEvidence:  []string{},
			RefutingEvidence:    []string{},
			UnverifiedIssues:    []string{},
			NextSteps:           []string{},
		},
	}

	if r.skills != nil {
		inv.loader = newSkillLoader(maxLoadedSkills)
	}

	return inv
}

// run 执行整场调查：初始化上下文后逐轮运行，直到终局诊断或预算/轮数耗尽。
func (inv *investigation) run() (*Report, error) {
	if err := inv.transition(StatePlanning); err != nil {
		return inv.fail(err)
	}

	inv.buildInitialContext()

	// 单轮基线：直接强制终局，跳过规划器与工具执行。
	if inv.r.singleShot {
		inv.forceFinal = true
	}

	for round := 1; round <= inv.maxRounds; round++ {
		done, err := inv.runRound(round)
		if err != nil {
			return inv.fail(err)
		}
		if done {
			return inv.report, nil
		}
	}

	return inv.fail(fmt.Errorf(
		"agent did not produce a final answer within %d rounds",
		inv.maxRounds,
	))
}

func (inv *investigation) transition(next State) error {
	if err := inv.sm.Transition(next); err != nil {
		return err
	}
	inv.report.State = inv.sm.Current()
	inv.report.StateHistory = inv.sm.History()
	return nil
}

func (inv *investigation) fail(err error) (*Report, error) {
	if inv.sm.Current() != StateCompleted &&
		inv.sm.Current() != StateFailed {
		_ = inv.transition(StateFailed)
	}
	inv.report.ToolCalls = inv.toolCallsUsed
	inv.report.DiagnosisHypotheses = inv.diagnosisManager.List()
	return inv.report, err
}

// buildInitialContext 组装初始系统提示、用户事故消息与技能上下文。
func (inv *investigation) buildInitialContext() {
	// 技能属于参考知识，不创建当前事故 Evidence。
	inv.report.Knowledge = inv.skillRefs

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
  ],
  "observed_facts": [
    "facts directly observed from tool results"
  ],
  "supporting_evidence": [
    "observations supporting the leading hypothesis"
  ],
  "refuting_evidence": [
    "observations against it, or an empty array"
  ],
  "unverified_issues": [
    "what remains unknown or unverified"
  ],
  "confidence": "high|medium|low",
  "next_steps": [
    "what would verify the root cause"
  ]
}

The summary must be a string.
Hypotheses must be an array of strings.
The structured fields make the diagnosis auditable; use them honestly
and leave arrays empty when nothing applies.
Do not include Markdown code fences around the JSON.`

	inv.messages = []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "Investigate this incident: " + inv.incident},
	}

	// 技能上下文：L1 索引常驻，L2 摘要按事故描述命中注入。
	if inv.r.skills != nil && inv.loader != nil && !inv.r.singleShot {
		inv.messages = append(inv.messages, llm.Message{
			Role:    "system",
			Content: inv.r.skills.Index(),
		})

		skillMessage, refs, briefs := buildSkillContext(
			inv.r.skills,
			buildKnowledgeQuery(inv.incident, nil, nil),
			inv.loader,
		)
		if skillMessage != "" {
			inv.messages = upsertSkillContextMessage(inv.messages, skillMessage)
		}

		inv.skillRefs = refs
		inv.skillBriefs = briefs
		inv.report.Knowledge = inv.skillRefs
	}
}

// runRound 执行一轮调查。
// 返回 done=true 表示已产出终局报告；否则继续下一轮。
func (inv *investigation) runRound(round int) (bool, error) {
	if err := inv.ctx.Err(); err != nil {
		return false, err
	}

	if inv.sm.Current() == StateObserving {
		if err := inv.transition(StatePlanning); err != nil {
			return false, err
		}
	}

	remaining := inv.maxCalls - inv.toolCallsUsed

	plan, availableTools, err := inv.planNext(round, remaining)
	if err != nil {
		return false, err
	}

	availableTools, err = inv.enforceBudgets(remaining, availableTools)
	if err != nil {
		return false, err
	}

	completion, err := inv.complete(availableTools)
	if err != nil {
		return false, err
	}

	assistantMessage := completion.Message
	if assistantMessage.Role == "" {
		assistantMessage.Role = "assistant"
	}

	// 模型未请求工具：调查完成，解析终局诊断。
	if len(assistantMessage.ToolCalls) == 0 {
		if err := applyFinalAnswer(
			inv.report,
			assistantMessage.Content,
		); err != nil {
			return false, err
		}

		inv.report.DiagnosisHypotheses = inv.diagnosisManager.List()

		if err := inv.transition(StateCompleted); err != nil {
			return false, err
		}

		inv.report.ToolCalls = inv.toolCallsUsed
		inv.report.Knowledge = inv.skillRefs
		return true, nil
	}

	calls := assistantMessage.ToolCalls

	// 工具调用消息必须紧接着收到每个 call 对应的工具响应。
	inv.messages = append(inv.messages, assistantMessage)

	// 调查已关闭：拒绝执行工具，补齐协议响应，直接进入终局。
	if inv.forceFinal || remaining <= 0 || plan.ToolName == "" {
		inv.rejectClosedRound(calls)
		return false, nil
	}

	// 强制执行调查计划：任何一个调用不符合计划时整轮拒绝。
	if !callsMatchPlan(calls, plan.ToolName) {
		inv.rejectPlanViolation(plan, calls)
		return false, nil
	}

	executed, err := inv.executeRound(plan, calls, remaining)
	if err != nil {
		return false, err
	}
	if !executed {
		// 本轮没有可用的调用配额：保持 forceFinal，直接进入下一轮。
		return false, nil
	}

	if err := inv.transition(StateObserving); err != nil {
		return false, err
	}

	// 预算耗尽后，下一轮只请求最终诊断。
	if inv.toolCallsUsed >= inv.maxCalls {
		inv.forceFinal = true
	}

	return false, nil
}

// planNext 调用规划器确定本轮要执行的工具，并注入计划消息。
// 规划器判定无需继续调查或输出降级时，转入强制终局而非失败。
func (inv *investigation) planNext(
	round int,
	remaining int,
) (InvestigationPlan, []llm.ToolDefinition, error) {
	var plan InvestigationPlan

	// 仅在调查预算尚有剩余时调用规划器。
	if remaining <= 0 || inv.forceFinal {
		return plan, nil, nil
	}

	candidates := make([]ToolCandidate, 0)
	for _, registered := range inv.r.tools.List() {
		candidates = append(candidates, ToolCandidate{
			Name:        registered.Name(),
			Description: registered.Description(),
		})
	}

	var err error
	plan, err = inv.r.planner.PlanContext(inv.ctx, PlanningInput{
		Incident:      inv.incident,
		Evidence:      inv.evidenceStore.List(),
		Hypotheses:    inv.diagnosisManager.List(),
		Tools:         candidates,
		ToolCallCount: inv.toolCallCount,
		Knowledge:     inv.skillBriefs,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrNoUsefulAction):
			inv.forceFinal = true
			return plan, nil, nil
		case errors.Is(err, ErrPlannerDegraded):
			// 模型输出/API 瞬态异常：不终止整个调查，改为优雅终局。
			fmt.Printf(
				"[TraceMind] planner degraded (%v); closing investigation\n",
				err,
			)
			inv.forceFinal = true
			return plan, nil, nil
		default:
			return plan, nil, fmt.Errorf("plan investigation: %w", err)
		}
	}

	availableTools := filterToolDefinitions(inv.definitions, plan.ToolName)
	if len(availableTools) == 0 {
		return plan, nil, fmt.Errorf(
			"planner selected unknown tool %q",
			plan.ToolName,
		)
	}

	inv.report.Plans = append(inv.report.Plans, plan)

	planJSON, err := json.Marshal(plan)
	if err != nil {
		return plan, nil, fmt.Errorf("marshal investigation plan: %w", err)
	}

	inv.messages = append(inv.messages, llm.Message{
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

	return plan, availableTools, nil
}

// enforceBudgets 应用调用预算与 token 预算，并在强制终局时注入终局提示。
func (inv *investigation) enforceBudgets(
	remaining int,
	availableTools []llm.ToolDefinition,
) ([]llm.ToolDefinition, error) {
	// 预算耗尽后不再向模型提供工具定义。
	if remaining <= 0 {
		inv.forceFinal = true
	}

	// token 预算：超出硬上限后强制终局，最多再允许一次最终诊断调用。
	if inv.r.maxTokens > 0 {
		estimated := estimateMessagesTokens(inv.messages) +
			estimateToolDefinitionsTokens(availableTools)

		if estimated > inv.r.maxTokens {
			if inv.forceFinal {
				return nil, fmt.Errorf(
					"token budget exceeded: estimated %d > %d tokens",
					estimated,
					inv.r.maxTokens,
				)
			}

			fmt.Printf(
				"[TraceMind] token budget exceeded "+
					"(estimated %d/%d); closing investigation\n",
				estimated,
				inv.r.maxTokens,
			)
			inv.forceFinal = true
			availableTools = nil
		}
	}

	if inv.forceFinal && !inv.finalPromptAdded {
		inv.messages = append(inv.messages, llm.Message{
			Role: "system",
			Content: "Investigation is now closed. Do not call tools. " +
				"Do not request additional observations. " +
				"Return the final JSON diagnosis using only the " +
				"evidence already collected. Explicitly state " +
				"important uncertainties and unverified causes.",
		})
		inv.finalPromptAdded = true
	}

	return availableTools, nil
}

// complete 发送当前消息历史与可用工具定义给模型。
func (inv *investigation) complete(
	availableTools []llm.ToolDefinition,
) (*llm.Completion, error) {
	completion, err := inv.r.client.Complete(
		inv.ctx,
		inv.messages,
		availableTools,
	)
	if err != nil {
		return nil, fmt.Errorf("call LLM: %w", err)
	}
	if completion == nil {
		return nil, errors.New("LLM returned a nil completion")
	}
	return completion, nil
}

// rejectClosedRound 在调查已关闭时为所有调用补齐“不执行”的协议响应。
func (inv *investigation) rejectClosedRound(calls []llm.ToolCall) {
	for _, call := range calls {
		inv.messages = append(inv.messages, llm.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content: "Tool call not executed: investigation is closed. " +
				"Return the final diagnosis using existing evidence.",
		})
	}
	inv.forceFinal = true
}

// rejectPlanViolation 当调用与计划不一致时，为所有调用补齐拒绝响应。
func (inv *investigation) rejectPlanViolation(
	plan InvestigationPlan,
	calls []llm.ToolCall,
) {
	for _, call := range calls {
		inv.messages = append(inv.messages, llm.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content: fmt.Sprintf(
				"Tool call not executed: this round permits only %q. "+
					"Follow the plan or return the final diagnosis.",
				plan.ToolName,
			),
		})
	}
}

func callsMatchPlan(calls []llm.ToolCall, toolName string) bool {
	for _, call := range calls {
		if call.Function.Name != toolName {
			return false
		}
	}
	return true
}

// executeRound 执行模型请求的工具调用，并完成结果回填、证据入库、
// 假设评估与技能上下文刷新。返回 executed=false 表示本轮无配额可执行。
func (inv *investigation) executeRound(
	plan InvestigationPlan,
	calls []llm.ToolCall,
	remaining int,
) (bool, error) {
	// 一个调查轮次最多执行一个调用。
	allowedCount := 1
	if allowedCount > remaining {
		allowedCount = remaining
	}
	if allowedCount <= 0 {
		inv.forceFinal = true
		return false, nil
	}

	if err := inv.transition(StateExecuting); err != nil {
		return false, err
	}

	// 即使工具执行失败，也计入调用预算。
	inv.toolCallsUsed += allowedCount
	inv.report.ToolCalls = inv.toolCallsUsed
	inv.toolCallCount[plan.ToolName]++

	execution, err := ExecuteToolCalls(
		inv.ctx,
		inv.r.tools,
		calls[:allowedCount],
	)
	if err != nil {
		return false, fmt.Errorf("execute tool calls: %w", err)
	}
	if execution == nil {
		return false, errors.New("tool executor returned nil result")
	}

	// 以 tool_call_id 建立映射，避免依赖结果切片顺序。
	resultByCallID := make(map[string]llm.Message, len(execution.Messages))
	for _, message := range execution.Messages {
		if message.ToolCallID != "" {
			resultByCallID[message.ToolCallID] = message
		}
	}

	// 所有工具响应必须紧跟在 assistant 工具调用消息之后。
	inv.messages = append(
		inv.messages,
		inv.buildToolMessages(calls, allowedCount, resultByCallID)...,
	)

	inv.printExecutedCalls(calls, allowedCount, resultByCallID)
	inv.recordSkillLoads(plan, calls, allowedCount)

	if err := inv.storeEvidence(execution); err != nil {
		return false, err
	}

	if err := inv.evaluateAndUpdateHypotheses(execution); err != nil {
		return false, err
	}

	inv.refreshSkillContext(execution)

	return true, nil
}

// buildToolMessages 为每个调用构造工具响应消息，保证协议要求的配对完整。
func (inv *investigation) buildToolMessages(
	calls []llm.ToolCall,
	allowedCount int,
	resultByCallID map[string]llm.Message,
) []llm.Message {
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

	return toolMessages
}

// printExecutedCalls 打印真正执行的调用及其真实结果，便于观察调查过程。
func (inv *investigation) printExecutedCalls(
	calls []llm.ToolCall,
	allowedCount int,
	resultByCallID map[string]llm.Message,
) {
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
}

// recordSkillLoads 记录 load_skill 的加载状态；技能容量满时按 LRU 淘汰。
func (inv *investigation) recordSkillLoads(
	plan InvestigationPlan,
	calls []llm.ToolCall,
	allowedCount int,
) {
	if plan.ToolName != "load_skill" || inv.loader == nil {
		return
	}

	for _, call := range calls[:allowedCount] {
		name := parseLoadSkillName(call.Function.Arguments)
		if name == "" {
			continue
		}

		if evicted := inv.loader.Load(name); evicted != "" {
			fmt.Printf(
				"[TraceMind] skill loaded=%q evicted=%q (max %d)\n",
				name,
				evicted,
				inv.loader.max,
			)
		} else {
			fmt.Printf("[TraceMind] skill loaded=%q\n", name)
		}
	}
}

// storeEvidence 保存当前事故证据，并在本次事件范围内生成唯一 ID。
func (inv *investigation) storeEvidence(
	execution *ToolExecutionResult,
) error {
	for _, item := range execution.Evidence {
		item.ID = fmt.Sprintf("ev-%03d", len(inv.report.Evidence)+1)
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}

		if err := inv.evidenceStore.Add(item); err != nil {
			return fmt.Errorf("store evidence: %w", err)
		}
		inv.report.Evidence = append(inv.report.Evidence, item)
	}

	return nil
}

// evaluateAndUpdateHypotheses 在工具调用轮次结束后统一评估假设。
// 评估失败不终止调查：保留上一轮假设评估，仅打印告警。
func (inv *investigation) evaluateAndUpdateHypotheses(
	execution *ToolExecutionResult,
) error {
	if len(execution.Evidence) == 0 {
		return nil
	}

	if err := evaluateHypotheses(
		inv.ctx,
		inv.r.client,
		inv.incident,
		inv.evidenceStore,
		inv.diagnosisManager,
	); err != nil {
		fmt.Printf(
			"[TraceMind] hypothesis evaluation degraded (%v); keeping previous assessment\n",
			err,
		)
		return nil
	}

	inv.report.DiagnosisHypotheses = inv.diagnosisManager.List()

	hypothesisJSON, err := json.Marshal(
		inv.report.DiagnosisHypotheses,
	)
	if err != nil {
		return fmt.Errorf("marshal hypotheses: %w", err)
	}

	inv.messages = append(inv.messages, llm.Message{
		Role: "system",
		Content: "Updated structured hypothesis assessment:\n" +
			string(hypothesisJSON) +
			"\nUse this assessment together with actual tool results. " +
			"A supported hypothesis is not automatically a " +
			"confirmed root cause.",
	})

	return nil
}

// refreshSkillContext 每轮重建技能上下文并整条替换旧的技能消息；
// 技能库异常时保留上一轮知识，不能影响事故诊断主流程。
// 触发条件用 Executed（确有工具执行）而非 Evidence（load_skill 不产证据）。
func (inv *investigation) refreshSkillContext(
	execution *ToolExecutionResult,
) {
	if inv.r.skills == nil || inv.loader == nil || execution.Executed == 0 {
		return
	}

	skillMessage, refs, briefs := buildSkillContext(
		inv.r.skills,
		buildKnowledgeQuery(
			inv.incident,
			inv.evidenceStore.List(),
			inv.diagnosisManager.List(),
		),
		inv.loader,
	)

	if skillMessage != "" {
		inv.messages = upsertSkillContextMessage(inv.messages, skillMessage)
	}

	inv.skillRefs = mergeSkillRefs(inv.skillRefs, refs)
	inv.skillBriefs = briefs
	inv.report.Knowledge = inv.skillRefs
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
