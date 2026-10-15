package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/liocoder/TraceMind/internal/datasource"
)

type MetricsQueryTool struct {
	Provider datasource.MetricsProvider
}

func NewMetricsQueryTool(
	provider datasource.MetricsProvider,
) *MetricsQueryTool {
	return &MetricsQueryTool{Provider: provider}
}

func (t *MetricsQueryTool) Name() string {
	return "query_metrics"
}

func (t *MetricsQueryTool) Description() string {
	return "Query service health metrics. Provide a service name to inspect its observed metrics."
}

func (t *MetricsQueryTool) Execute(
	ctx context.Context,
	input map[string]any,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t == nil || t.Provider == nil {
		return Result{}, fmt.Errorf("metrics provider is not configured")
	}

	service, ok := input["service"].(string)
	if !ok || strings.TrimSpace(service) == "" {
		return Result{}, fmt.Errorf("service must be a non-empty string")
	}

	service = datasource.NormalizeServiceName(service)

	snapshots, err := t.Provider.QueryMetrics(ctx, datasource.MetricsQuery{
		Service: service,
	})
	if err != nil {
		return Result{}, fmt.Errorf("query metrics for %q: %w", service, err)
	}

	return Result{
		Content: formatMetrics(snapshots),
		Metadata: map[string]string{
			"source":  "metrics-provider",
			"service": service,
			"count":   fmt.Sprintf("%d", len(snapshots)),
		},
	}, nil
}

func formatMetrics(snapshots []datasource.MetricsSnapshot) string {
	if len(snapshots) == 0 {
		return "No metrics available for the requested service and time range."
	}

	var builder strings.Builder

	for i, snapshot := range snapshots {
		if i > 0 {
			builder.WriteString("\n")
		}

		fmt.Fprintf(&builder, "service=%s", snapshot.Service)

		if !snapshot.Timestamp.IsZero() {
			fmt.Fprintf(
				&builder,
				", timestamp=%s",
				snapshot.Timestamp.UTC().Format(time.RFC3339),
			)
		}
		if snapshot.Status != "" {
			fmt.Fprintf(&builder, ", status=%s", snapshot.Status)
		}

		names := make([]string, 0, len(snapshot.Values))
		for name := range snapshot.Values {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			value := snapshot.Values[name]
			fmt.Fprintf(
				&builder,
				", %s=%s",
				name,
				formatMetricValue(value),
			)
		}
	}

	return builder.String()
}

func formatMetricValue(value datasource.MetricValue) string {
	return fmt.Sprintf("%s%s", formatNumber(value.Value), value.Unit)
}

func formatNumber(value float64) string {
	formatted := fmt.Sprintf("%.6f", value)
	formatted = strings.TrimRight(formatted, "0")
	formatted = strings.TrimRight(formatted, ".")
	if formatted == "" || formatted == "-0" {
		return "0"
	}
	return formatted
}
