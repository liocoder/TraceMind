package tool

import (
	"context"
	"fmt"
	"strings"
)

type MockMetricsQuery struct{}

func (m *MockMetricsQuery) Name() string {
	return "query_metrics"
}

func (m *MockMetricsQuery) Description() string {
	return "Query simulated service health metrics."
}

func (m *MockMetricsQuery) Execute(
	ctx context.Context,
	input map[string]any,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	service, ok := input["service"].(string)
	if !ok || strings.TrimSpace(service) == "" {
		return Result{}, fmt.Errorf(
			"service must be a non-empty string",
		)
	}

	// Simulated metrics for the first end-to-end demo.
	content := fmt.Sprintf(
		"service=%s, p95_latency_ms=1850, error_rate=8.5%%, connection_pool_usage=98%%",
		service,
	)

	return Result{
		Content: content,
		Metadata: map[string]string{
			"source":  "mock-metrics-store",
			"service": service,
		},
	}, nil
}
