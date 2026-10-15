package datasource

import (
	"context"
	"strings"
	"time"
)

// MetricValue represents one numeric metric and its unit.
type MetricValue struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
}

// MetricsSnapshot represents metrics collected for a service
// at a particular point in time.
type MetricsSnapshot struct {
	Service   string                 `json:"service"`
	Timestamp time.Time              `json:"timestamp"`
	Status    string                 `json:"status,omitempty"`
	Values    map[string]MetricValue `json:"values"`
}

// LogEntry represents one log event.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Service   string    `json:"service"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// MetricsQuery describes a metrics query.
type MetricsQuery struct {
	Service   string
	Names     []string
	StartTime time.Time
	EndTime   time.Time
}

// LogsQuery describes a log search.
type LogsQuery struct {
	Query     string
	Service   string
	StartTime time.Time
	EndTime   time.Time
}

// MetricsProvider provides service metrics.
type MetricsProvider interface {
	QueryMetrics(
		ctx context.Context,
		req MetricsQuery,
	) ([]MetricsSnapshot, error)
}

// LogsProvider searches service logs.
type LogsProvider interface {
	SearchLogs(
		ctx context.Context,
		req LogsQuery,
	) ([]LogEntry, error)
}

// Provider combines the datasource capabilities required by TraceMind.
type Provider interface {
	MetricsProvider
	LogsProvider
}

// NormalizeServiceName maps common service aliases to canonical names.
func NormalizeServiceName(service string) string {
	switch normalize(service) {
	case "订单服务", "订单", "order", "order_service", "order-service":
		return "order-service"
	case "支付服务", "支付", "payment", "payment_service", "payment-service":
		return "payment-service"
	case "数据库", "订单数据库", "mysql", "order-db", "order_db", "database":
		return "database"
	case "网关", "api网关", "gateway", "api_gateway", "api-gateway":
		return "api-gateway"
	default:
		return normalize(service)
	}
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
