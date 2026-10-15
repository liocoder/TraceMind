package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liocoder/TraceMind/internal/datasource"
)

type LogsSearchTool struct {
	Provider datasource.LogsProvider
}

func NewLogsSearchTool(
	provider datasource.LogsProvider,
) *LogsSearchTool {
	return &LogsSearchTool{Provider: provider}
}

func (t *LogsSearchTool) Name() string {
	return "search_logs"
}

func (t *LogsSearchTool) Description() string {
	return "Search service logs using keywords from an incident description."
}

func (t *LogsSearchTool) Execute(
	ctx context.Context,
	input map[string]any,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t == nil || t.Provider == nil {
		return Result{}, fmt.Errorf("logs provider is not configured")
	}

	query, ok := input["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return Result{}, fmt.Errorf("query must be a non-empty string")
	}

	entries, err := t.Provider.SearchLogs(ctx, datasource.LogsQuery{
		Query: strings.TrimSpace(query),
	})
	if err != nil {
		return Result{}, fmt.Errorf("search logs: %w", err)
	}

	var builder strings.Builder

	for i, entry := range entries {
		if i > 0 {
			builder.WriteString("\n")
		}

		if !entry.Timestamp.IsZero() {
			builder.WriteString(entry.Timestamp.UTC().Format(time.RFC3339))
			builder.WriteString(" ")
		}
		if entry.Level != "" {
			builder.WriteString(strings.ToUpper(entry.Level))
			builder.WriteString(" ")
		}
		if entry.Service != "" {
			builder.WriteString("service=")
			builder.WriteString(entry.Service)
			builder.WriteString(" ")
		}
		builder.WriteString(entry.Message)
	}

	content := builder.String()
	if len(entries) == 0 {
		content = "No matching logs found."
	}

	return Result{
		Content: content,
		Metadata: map[string]string{
			"source": "logs-provider",
			"query":  strings.TrimSpace(query),
			"count":  fmt.Sprintf("%d", len(entries)),
		},
	}, nil
}
