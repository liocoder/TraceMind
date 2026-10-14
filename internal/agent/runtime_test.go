package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/tool"
)

// fakeCompletionClient 用于在测试中模拟 LLM，避免调用真实 API。
type fakeCompletionClient struct {
	calls    int
	complete func(
		ctx context.Context,
		messages []llm.Message,
		definitions []llm.ToolDefinition,
	) (*llm.Completion, error)
}

func (f *fakeCompletionClient) Complete(
	ctx context.Context,
	messages []llm.Message,
	definitions []llm.ToolDefinition,
) (*llm.Completion, error) {
	f.calls++

	if f.complete == nil {
		return nil, errors.New("fakeCompletionClient: complete function is nil")
	}

	return f.complete(ctx, messages, definitions)
}

// newTestRuntime 注册测试所需的模拟工具，并创建 Runtime。
func newTestRuntime(
	t *testing.T,
	maxToolCalls int,
	client *fakeCompletionClient,
) *Runtime {
	t.Helper()

	registry := tool.NewRegistry()

	if err := registry.Register(&tool.MockLogSearch{}); err != nil {
		t.Fatalf("register mock log search tool: %v", err)
	}

	if err := registry.Register(&tool.MockMetricsQuery{}); err != nil {
		t.Fatalf("register mock metrics query tool: %v", err)
	}

	runtime := newRuntime(registry, client)
	runtime.maxToolCalls = maxToolCalls

	return runtime
}

// metricsToolCall 构造 query_metrics 工具调用。
func metricsToolCall(callID, service string) llm.ToolCall {
	arguments, err := json.Marshal(map[string]string{
		"service": service,
	})
	if err != nil {
		panic(err)
	}

	return llm.ToolCall{
		ID:   callID,
		Type: "function",
		Function: llm.ToolFunction{
			Name:      "query_metrics",
			Arguments: string(arguments),
		},
	}
}

// finalCompletion 构造符合 Runtime 预期的最终 JSON 回复。
func finalCompletion(summary string) *llm.Completion {
	content, err := json.Marshal(map[string]interface{}{
		"summary":    summary,
		"hypotheses": []string{},
		"evidence":   []string{},
	})
	if err != nil {
		panic(err)
	}

	return &llm.Completion{
		Message: llm.Message{
			Role:    "assistant",
			Content: string(content),
		},
		FinishReason: "stop",
	}
}

func TestRuntimeCompletesDiagnosis(t *testing.T) {
	client := &fakeCompletionClient{}

	client.complete = func(
		ctx context.Context,
		messages []llm.Message,
		definitions []llm.ToolDefinition,
	) (*llm.Completion, error) {
		if client.calls == 1 {
			return &llm.Completion{
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						metricsToolCall("call-1", "order-service"),
					},
				},
				FinishReason: "tool_calls",
			}, nil
		}

		return finalCompletion(
			"Order service connection pool is saturated.",
		), nil
	}

	runtime := newTestRuntime(t, 3, client)

	report, err := runtime.Run(
		context.Background(),
		"Order service frequently times out",
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if report == nil {
		t.Fatal("expected a report, got nil")
	}

	if report.State != StateCompleted {
		t.Fatalf("expected completed, got %s", report.State)
	}

	if report.ToolCalls != 1 {
		t.Fatalf("expected 1 tool call, got %d", report.ToolCalls)
	}

	if report.Summary != "Order service connection pool is saturated." {
		t.Fatalf("unexpected summary: %s", report.Summary)
	}

	if len(report.Evidence) != 1 {
		t.Fatalf("expected 1 evidence item, got %d", len(report.Evidence))
	}

	if report.Evidence[0].ID != "ev-001" {
		t.Fatalf("expected evidence ID ev-001, got %s", report.Evidence[0].ID)
	}

	wantHistory := []State{
		StatePending,
		StatePlanning,
		StateExecuting,
		StateObserving,
		StatePlanning,
		StateCompleted,
	}

	if !reflect.DeepEqual(report.StateHistory, wantHistory) {
		t.Fatalf(
			"unexpected state history: got %v, want %v",
			report.StateHistory,
			wantHistory,
		)
	}
}

func TestRuntimeRejectsEmptyIncident(t *testing.T) {
	client := &fakeCompletionClient{}
	runtime := newTestRuntime(t, 3, client)

	report, err := runtime.Run(context.Background(), "   ")
	if err == nil {
		t.Fatal("expected an error for an empty incident")
	}

	if report != nil {
		t.Fatalf("expected nil report, got %+v", report)
	}

	if client.calls != 0 {
		t.Fatalf("expected no LLM calls, got %d", client.calls)
	}
}

func TestRuntimeHandlesCancelledContext(t *testing.T) {
	client := &fakeCompletionClient{}
	runtime := newTestRuntime(t, 3, client)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := runtime.Run(ctx, "Order service frequently times out")
	if err == nil {
		t.Fatal("expected an error for a cancelled context")
	}

	if report == nil {
		t.Fatal("expected a failure report, got nil")
	}

	if report.State != StateFailed {
		t.Fatalf("expected failed, got %s", report.State)
	}

	if client.calls != 0 {
		t.Fatalf("expected no LLM calls, got %d", client.calls)
	}
}

func TestRuntimeHandlesNilCompletion(t *testing.T) {
	client := &fakeCompletionClient{
		complete: func(
			ctx context.Context,
			messages []llm.Message,
			definitions []llm.ToolDefinition,
		) (*llm.Completion, error) {
			return nil, nil
		},
	}

	runtime := newTestRuntime(t, 3, client)

	report, err := runtime.Run(
		context.Background(),
		"Order service frequently times out",
	)
	if err == nil {
		t.Fatal("expected an error for a nil completion")
	}

	if report == nil {
		t.Fatal("expected a failure report, got nil")
	}

	if report.State != StateFailed {
		t.Fatalf("expected failed, got %s", report.State)
	}
}

func TestApplyFinalAnswerRejectsInvalidJSON(t *testing.T) {
	report := &Report{}

	err := applyFinalAnswer(report, `{"summary":`)
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}

	if report.Summary != "" {
		t.Fatalf("expected empty summary, got %q", report.Summary)
	}
}

func TestApplyFinalAnswerRejectsEmptySummary(t *testing.T) {
	report := &Report{}

	err := applyFinalAnswer(report, `{"summary":"   ","hypotheses":[]}`)
	if err == nil {
		t.Fatal("expected an error for an empty summary")
	}

	if report.Summary != "" {
		t.Fatalf("expected empty summary, got %q", report.Summary)
	}
}
