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
	return "Query simulated service health metrics, including latency, error rate, connection pool usage, and CPU utilization."
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
		return Result{}, fmt.Errorf("service must be a non-empty string")
	}

	service = strings.ToLower(strings.TrimSpace(service))

	var content string

	switch service {
	case "order-service":
		content = "service=order-service, p95_latency_ms=1850, error_rate=8.5%, connection_pool_usage=98%, cpu_usage=42%, status=degraded"

	case "payment-service":
		content = "service=payment-service, p95_latency_ms=3200, error_rate=12.6%, connection_pool_usage=76%, cpu_usage=38%, status=degraded"

	case "database", "mysql", "order-db":
		content = "service=database, p95_latency_ms=35, error_rate=0.1%, connection_pool_usage=45%, cpu_usage=51%, status=healthy"

	case "gateway", "api-gateway":
		content = "service=api-gateway, p95_latency_ms=2100, error_rate=7.9%, upstream_timeout_count=126, status=degraded"

	default:
		return Result{
			Content: fmt.Sprintf(
				"service=%s, status=unknown, error=no simulated metrics available for this service",
				service,
			),
			Metadata: map[string]string{
				"source":  "mock-metrics-store",
				"service": service,
			},
		}, nil
	}

	return Result{
		Content: content,
		Metadata: map[string]string{
			"source":  "mock-metrics-store",
			"service": service,
		},
	}, nil
}
