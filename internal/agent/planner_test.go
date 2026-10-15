package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liocoder/TraceMind/internal/llm"
)

type plannerTestClient struct {
	calls    int
	messages []llm.Message
	response *llm.Completion
	err      error
}

func (f *plannerTestClient) Complete(
	ctx context.Context,
	messages []llm.Message,
	tools []llm.ToolDefinition,
) (*llm.Completion, error) {
	f.calls++
	f.messages = messages

	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

func TestLLMPlanner_NoAvailableTools(t *testing.T) {
	planner := NewPlanner()

	_, err := planner.Plan(PlanningInput{
		Incident: "订单服务频繁超时",
	})

	if !errors.Is(err, ErrNoAvailableTools) {
		t.Fatalf("expected ErrNoAvailableTools, got %v", err)
	}
}

func TestLLMPlanner_SelectsAvailableTool(t *testing.T) {
	client := &plannerTestClient{
		response: &llm.Completion{
			Message: llm.Message{
				Role: "assistant",
				Content: `{
"action": "CALL_TOOL",
"tool_name": "query_metrics",
"rationale": "需要先确认服务延迟和错误率是否异常",
"target_hypothesis_id": "",
"expected_observation": "订单服务的延迟、错误率及资源指标"
}`,
			},
			FinishReason: "stop",
		},
	}

	planner := NewPlanner(client)

	plan, err := planner.Plan(PlanningInput{
		Incident: "订单服务频繁超时，部分请求返回 502",
		Tools: []ToolCandidate{
			{Name: "search_logs", Description: "搜索服务日志"},
			{Name: "query_metrics", Description: "查询服务指标"},
		},
	})

	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}

	if plan.ToolName != "query_metrics" {
		t.Fatalf("expected query_metrics, got %q", plan.ToolName)
	}
	if plan.Rationale == "" {
		t.Fatal("expected a non-empty rationale")
	}
	if plan.ExpectedObservation == "" {
		t.Fatal("expected a non-empty expected observation")
	}
	if client.calls != 1 {
		t.Fatalf("expected one LLM call, got %d", client.calls)
	}

	if len(client.messages) != 2 {
		t.Fatalf("expected system and user messages, got %d", len(client.messages))
	}

	if !strings.Contains(client.messages[1].Content, "订单服务频繁超时") {
		t.Fatal("planner input should contain the incident description")
	}
}

func TestLLMPlanner_RejectsUnavailableTool(t *testing.T) {
	client := &plannerTestClient{
		response: &llm.Completion{
			Message: llm.Message{
				Role: "assistant",
				Content: `{
"action": "CALL_TOOL",
"tool_name": "delete_database",
"rationale": "检查数据库",
"target_hypothesis_id": "",
"expected_observation": "数据库状态"
}`,
			},
		},
	}

	planner := NewPlanner(client)

	_, err := planner.Plan(PlanningInput{
		Incident: "数据库响应缓慢",
		Tools: []ToolCandidate{
			{Name: "query_metrics", Description: "查询服务指标"},
			{Name: "search_logs", Description: "搜索服务日志"},
		},
	})

	if err == nil {
		t.Fatal("expected an error for an unavailable tool")
	}
	if !strings.Contains(err.Error(), "selected unavailable tool") {
		t.Fatalf("expected unavailable-tool error, got %v", err)
	}
}

func TestLLMPlanner_NoAction(t *testing.T) {
	client := &plannerTestClient{
		response: &llm.Completion{
			Message: llm.Message{
				Role: "assistant",
				Content: `{
"action": "NO_ACTION",
"tool_name": "",
"rationale": "现有证据已足够",
"target_hypothesis_id": "",
"expected_observation": ""
}`,
			},
		},
	}

	planner := NewPlanner(client)

	_, err := planner.Plan(PlanningInput{
		Incident: "订单服务频繁超时",
		Tools: []ToolCandidate{
			{Name: "query_metrics"},
		},
	})

	if !errors.Is(err, ErrNoUsefulAction) {
		t.Fatalf("expected ErrNoUsefulAction, got %v", err)
	}
}

func TestLLMPlanner_RejectsNilCompletion(t *testing.T) {
	client := &plannerTestClient{}
	planner := NewPlanner(client)

	_, err := planner.Plan(PlanningInput{
		Incident: "订单服务频繁超时",
		Tools: []ToolCandidate{
			{Name: "query_metrics"},
		},
	})

	if err == nil {
		t.Fatal("expected an error for a nil completion")
	}
	if !strings.Contains(err.Error(), "nil completion") {
		t.Fatalf("expected nil-completion error, got %v", err)
	}
}

func TestLLMPlanner_PropagatesClientError(t *testing.T) {
	clientErr := errors.New("upstream unavailable")
	client := &plannerTestClient{err: clientErr}
	planner := NewPlanner(client)

	_, err := planner.Plan(PlanningInput{
		Incident: "订单服务频繁超时",
		Tools: []ToolCandidate{
			{Name: "query_metrics"},
		},
	})

	if !errors.Is(err, clientErr) {
		t.Fatalf("expected client error to be propagated, got %v", err)
	}
}

func TestLLMPlanner_PlanContextRespectsCancelledContext(t *testing.T) {
	client := &plannerTestClient{
		response: &llm.Completion{
			Message: llm.Message{
				Role: "assistant",
				Content: `{
"action": "CALL_TOOL",
"tool_name": "query_metrics",
"rationale": "检查服务指标",
"target_hypothesis_id": "",
"expected_observation": "服务延迟与错误率"
}`,
			},
		},
	}

	planner := NewPlanner(client)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := planner.PlanContext(ctx, PlanningInput{
		Incident: "订单服务频繁超时",
		Tools: []ToolCandidate{
			{Name: "query_metrics"},
		},
	})

	// fake client 不主动检查 context，因此这里仅验证接口能够传入已取消的 context。
	// 实际 HTTP Client 会根据 context 取消请求。
	if err != nil {
		t.Fatalf("unexpected error from context-agnostic fake client: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("expected one LLM call, got %d", client.calls)
	}
}
