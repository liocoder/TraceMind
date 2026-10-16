package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/tool"
)

// referenceOnlyTool 模拟参考知识类工具（如 load_skill）：
// 在 Metadata 中声明 evidence=false，结果不应进入 Evidence Store。
type referenceOnlyTool struct{}

func (t *referenceOnlyTool) Name() string        { return "load_skill" }
func (t *referenceOnlyTool) Description() string { return "reference knowledge" }

func (t *referenceOnlyTool) Execute(
	ctx context.Context,
	input map[string]any,
) (tool.Result, error) {
	return tool.Result{
		Content: "Connection pool exhaustion may cause request timeouts.",
		Metadata: map[string]string{
			"source":   "skills-library",
			"skill":    "connection-pool",
			"evidence": "false",
		},
	}, nil
}

func TestExecuteToolCallsSkipsReferenceEvidence(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(&referenceOnlyTool{}); err != nil {
		t.Fatalf("register reference tool: %v", err)
	}

	result, err := ExecuteToolCalls(
		context.Background(),
		registry,
		[]llm.ToolCall{
			{
				ID:   "call-1",
				Type: "function",
				Function: llm.ToolFunction{
					Name:      "load_skill",
					Arguments: `{"name": "connection-pool"}`,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("ExecuteToolCalls: %v", err)
	}

	if len(result.Evidence) != 0 {
		t.Fatalf(
			"reference knowledge must not produce evidence, got %+v",
			result.Evidence,
		)
	}
	if result.Executed != 1 {
		t.Fatalf("expected the tool to be executed, got %d", result.Executed)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected one tool message, got %d", len(result.Messages))
	}
	if !strings.Contains(
		result.Messages[0].Content,
		"Connection pool exhaustion",
	) {
		t.Fatalf("tool result should still reach the model, got %q", result.Messages[0].Content)
	}
}

func TestUpsertSkillContextMessageReplaces(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "main system prompt"},
		{Role: "user", Content: "investigate"},
		{Role: "system", Content: skillContextMarker + "\nold skill A content"},
		{Role: "assistant", Content: "ok"},
	}

	updated := upsertSkillContextMessage(messages, "new skill C content")

	skillContexts := 0
	for _, message := range updated {
		if strings.HasPrefix(message.Content, skillContextMarker) {
			skillContexts++
			if !strings.Contains(message.Content, "new skill C content") {
				t.Fatalf("old skill content not replaced: %q", message.Content)
			}
			if strings.Contains(message.Content, "old skill A") {
				t.Fatalf("evicted skill content still present: %q", message.Content)
			}
		}
	}
	if skillContexts != 1 {
		t.Fatalf("expected exactly one skill context message, got %d", skillContexts)
	}
	if len(updated) != len(messages) {
		t.Fatalf(
			"replace should keep message count stable, got %d -> %d",
			len(messages),
			len(updated),
		)
	}
}

func TestUpsertSkillContextMessageEmptyContent(t *testing.T) {
	messages := []llm.Message{{Role: "system", Content: skillContextMarker + "\nold"}}
	updated := upsertSkillContextMessage(messages, "")
	if len(updated) != len(messages) {
		t.Fatalf("empty content must not modify messages")
	}
}

func TestApplyFinalAnswerStructuredFields(t *testing.T) {
	report := &Report{}
	content := `{
  "summary": "connection pool exhaustion is the likely cause",
  "hypotheses": ["pool exhaustion"],
  "observed_facts": ["pool usage peaked at 98%"],
  "supporting_evidence": ["metrics show 98% usage"],
  "refuting_evidence": [],
  "unverified_issues": ["whether retries masked errors"],
  "confidence": "medium",
  "next_steps": ["check pool max connections"]
}`

	if err := applyFinalAnswer(report, content); err != nil {
		t.Fatalf("applyFinalAnswer: %v", err)
	}

	if report.Summary != "connection pool exhaustion is the likely cause" {
		t.Fatalf("unexpected summary %q", report.Summary)
	}
	if len(report.ObservedFacts) != 1 || report.ObservedFacts[0] != "pool usage peaked at 98%" {
		t.Fatalf("unexpected observed_facts %+v", report.ObservedFacts)
	}
	if len(report.RefutingEvidence) != 0 {
		t.Fatalf("expected empty refuting_evidence, got %+v", report.RefutingEvidence)
	}
	if report.Confidence != "medium" {
		t.Fatalf("unexpected confidence %q", report.Confidence)
	}
	if len(report.NextSteps) != 1 || report.NextSteps[0] != "check pool max connections" {
		t.Fatalf("unexpected next_steps %+v", report.NextSteps)
	}
}

func TestApplyFinalAnswerDefaultsStructuredFields(t *testing.T) {
	report := &Report{}
	if err := applyFinalAnswer(
		report,
		`{"summary": "uncertain", "hypotheses": ["h"]}`,
	); err != nil {
		t.Fatalf("applyFinalAnswer: %v", err)
	}

	if report.ObservedFacts == nil ||
		report.SupportingEvidence == nil ||
		report.RefutingEvidence == nil ||
		report.UnverifiedIssues == nil ||
		report.NextSteps == nil {
		t.Fatalf("structured fields must default to empty slices, got %+v", report)
	}
}
