package tool

import (
	"context"
	"fmt"
	"strings"
)

type MockLogSearch struct{}

func (m *MockLogSearch) Name() string {
	return "search_logs"
}

func (m *MockLogSearch) Description() string {
	return "Search service logs for errors related to an incident."
}

func (m *MockLogSearch) Execute(
	ctx context.Context,
	input map[string]any,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	query, ok := input["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return Result{}, fmt.Errorf("query must be a non-empty string")
	}

	// Simulated logs for the first end-to-end demo.
	logs := []string{
		"2026-10-13T10:00:01Z ERROR order-service request timeout",
		"2026-10-13T10:00:02Z WARN payment-service latency elevated",
		"2026-10-13T10:00:03Z ERROR order-service connection pool exhausted",
	}

	return Result{
		Content: strings.Join(logs, "\n"),
		Metadata: map[string]string{
			"source": "mock-log-store",
			"query":  query,
			"count":  fmt.Sprintf("%d", len(logs)),
		},
	}, nil
}
