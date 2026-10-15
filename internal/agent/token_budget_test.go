package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liocoder/TraceMind/internal/llm"
)

func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens(""); got != 0 {
		t.Fatalf("empty string should estimate 0, got %d", got)
	}

	// 纯 ASCII：约 4 字符 1 token。
	if got := estimateTokens(strings.Repeat("a", 400)); got != 100 {
		t.Fatalf("400 ASCII chars should estimate 100, got %d", got)
	}

	// 纯 CJK：约 1 字符 1 token。
	if got := estimateTokens(strings.Repeat("连", 50)); got != 50 {
		t.Fatalf("50 CJK chars should estimate 50, got %d", got)
	}

	// 混合文本的估算至少非负且不为 0。
	if got := estimateTokens("hello 连接池 exhausted 123"); got <= 0 {
		t.Fatalf("mixed text should estimate > 0, got %d", got)
	}
}

func TestEstimateMessagesTokens(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: strings.Repeat("s", 400)},
		{Role: "user", Content: strings.Repeat("连", 100)},
		{
			Role:       "tool",
			Content:    "result",
			ToolCallID: "call-1",
		},
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-1",
					Type: "function",
					Function: llm.ToolFunction{
						Name:      "query_metrics",
						Arguments: `{"service":"order-service"}`,
					},
				},
			},
		},
	}

	got := estimateMessagesTokens(messages)
	if got <= 0 {
		t.Fatalf("expected a positive estimate, got %d", got)
	}
}

func TestEstimateToolDefinitionsTokens(t *testing.T) {
	definitions := []llm.ToolDefinition{
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        "query_metrics",
				Description: "query service metrics",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		},
	}

	if got := estimateToolDefinitionsTokens(definitions); got <= 0 {
		t.Fatalf("expected a positive estimate, got %d", got)
	}

	if got := estimateToolDefinitionsTokens(nil); got != 0 {
		t.Fatalf("nil definitions should estimate 0, got %d", got)
	}
}
