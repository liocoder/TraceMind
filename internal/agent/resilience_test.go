package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liocoder/TraceMind/internal/llm"
)

// scriptedPlanner 按预设错误返回计划，用于验证 runtime 的降级路径。
type scriptedPlanner struct {
	planErr error
	called  bool
}

func (p *scriptedPlanner) Plan(input PlanningInput) (InvestigationPlan, error) {
	return p.PlanContext(context.Background(), input)
}

func (p *scriptedPlanner) PlanContext(
	ctx context.Context,
	input PlanningInput,
) (InvestigationPlan, error) {
	p.called = true
	return InvestigationPlan{}, p.planErr
}

func TestRuntimeDegradesOnPlannerError(t *testing.T) {
	client := &fakeCompletionClient{
		complete: func(
			ctx context.Context,
			messages []llm.Message,
			definitions []llm.ToolDefinition,
		) (*llm.Completion, error) {
			return &llm.Completion{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary":"evidence was insufficient","hypotheses":[]}`,
				},
				FinishReason: "stop",
			}, nil
		},
	}

	runtime := newTestRuntime(t, 3, client)
	planner := &scriptedPlanner{planErr: ErrPlannerDegraded}
	runtime.SetPlanner(planner)

	report, err := runtime.Run(
		context.Background(),
		"order service returns HTTP 502",
	)
	if err != nil {
		t.Fatalf("Run should not fail on a degraded planner, got %v", err)
	}
	if report == nil {
		t.Fatal("expected a report")
	}
	if report.State != StateCompleted {
		t.Fatalf("expected StateCompleted, got %q", report.State)
	}
	if !planner.called {
		t.Fatal("expected the planner to be called")
	}
	if len(report.Plans) != 0 {
		t.Fatalf("expected no plans after degraded planning, got %+v", report.Plans)
	}
	if report.Summary != "evidence was insufficient" {
		t.Fatalf("unexpected summary %q", report.Summary)
	}
}

func TestRuntimeFailsOnNonDegradedPlannerError(t *testing.T) {
	client := &fakeCompletionClient{
		complete: func(
			ctx context.Context,
			messages []llm.Message,
			definitions []llm.ToolDefinition,
		) (*llm.Completion, error) {
			return &llm.Completion{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary":"x","hypotheses":[]}`,
				},
				FinishReason: "stop",
			}, nil
		},
	}

	runtime := newTestRuntime(t, 3, client)
	runtime.SetPlanner(&scriptedPlanner{
		planErr: errors.New("planner client is not configured"),
	})

	report, err := runtime.Run(
		context.Background(),
		"order service returns HTTP 502",
	)
	if err == nil {
		t.Fatal("expected a hard failure for a config-class planner error")
	}
	if report == nil {
		t.Fatal("expected a partial report alongside the error")
	}
	if !strings.Contains(err.Error(), "plan investigation") {
		t.Fatalf("unexpected error message %q", err)
	}
}

func TestStripCodeFences(t *testing.T) {
	for _, test := range []struct {
		in   string
		want string
	}{
		{`{"summary":"ok","hypotheses":[]}`, `{"summary":"ok","hypotheses":[]}`},
		{"```json\n{\"summary\":\"ok\",\"hypotheses\":[]}\n```", `{"summary":"ok","hypotheses":[]}`},
		{"```\n{\"summary\":\"ok\"}\n```", `{"summary":"ok"}`},
		{"```json\n{\"summary\":\"ok\"}\n```\n", `{"summary":"ok"}`},
		{"  {\"summary\":\"ok\"}  ", `{"summary":"ok"}`},
	} {
		if got := stripCodeFences(test.in); got != test.want {
			t.Fatalf("stripCodeFences(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

// blockingPlanner 阻塞在计划调用上，直到 context 被取消，用于验证
// Runtime 是否把控制权（取消/超时）真正传给规划器。
type blockingPlanner struct {
	entered chan struct{}
	once    sync.Once
}

func (p *blockingPlanner) Plan(input PlanningInput) (InvestigationPlan, error) {
	return p.PlanContext(context.Background(), input)
}

func (p *blockingPlanner) PlanContext(
	ctx context.Context,
	input PlanningInput,
) (InvestigationPlan, error) {
	p.once.Do(func() { close(p.entered) })
	<-ctx.Done()
	return InvestigationPlan{}, ctx.Err()
}

func TestRuntimeCancellationPropagatesToPlanner(t *testing.T) {
	client := &fakeCompletionClient{
		complete: func(
			ctx context.Context,
			messages []llm.Message,
			definitions []llm.ToolDefinition,
		) (*llm.Completion, error) {
			return &llm.Completion{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"summary":"x","hypotheses":[]}`,
				},
				FinishReason: "stop",
			}, nil
		},
	}

	runtime := newTestRuntime(t, 3, client)
	planner := &blockingPlanner{entered: make(chan struct{})}
	runtime.SetPlanner(planner)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runtime.Run(ctx, "order service returns HTTP 502")
	}()

	select {
	case <-planner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("planner was never called")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
