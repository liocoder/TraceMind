package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/liocoder/TraceMind/internal/datasource"
)

type Dataset struct {
	Scenarios map[string]Scenario `json:"scenarios"`
}

type Scenario struct {
	Metrics []datasource.MetricsSnapshot `json:"metrics"`
	Logs    []datasource.LogEntry        `json:"logs"`
}

type Provider struct {
	scenario Scenario
}

func NewProvider(path, scenarioID string) (*Provider, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mock dataset %q: %w", path, err)
	}

	var dataset Dataset
	if err := json.Unmarshal(data, &dataset); err != nil {
		return nil, fmt.Errorf("decode mock dataset %q: %w", path, err)
	}

	if len(dataset.Scenarios) == 0 {
		return nil, fmt.Errorf("mock dataset %q contains no scenarios", path)
	}

	scenarioID = strings.TrimSpace(scenarioID)

	if scenarioID == "" {
		if _, exists := dataset.Scenarios["default"]; exists {
			scenarioID = "default"
		} else {
			return nil, fmt.Errorf(
				"scenario ID is required for mock dataset %q; available scenarios: %s",
				path,
				strings.Join(sortedScenarioNames(dataset.Scenarios), ", "),
			)
		}
	}

	scenario, exists := dataset.Scenarios[scenarioID]
	if !exists {
		return nil, fmt.Errorf(
			"scenario %q not found in dataset %q; available scenarios: %s",
			scenarioID,
			path,
			strings.Join(sortedScenarioNames(dataset.Scenarios), ", "),
		)
	}

	scenario.Metrics = append(
		[]datasource.MetricsSnapshot(nil),
		scenario.Metrics...,
	)
	scenario.Logs = append(
		[]datasource.LogEntry(nil),
		scenario.Logs...,
	)

	return &Provider{
		scenario: scenario,
	}, nil
}

func (p *Provider) QueryMetrics(
	ctx context.Context,
	req datasource.MetricsQuery,
) ([]datasource.MetricsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("mock provider cannot be nil")
	}

	service := datasource.NormalizeServiceName(req.Service)
	if service == "" {
		return nil, fmt.Errorf("service cannot be empty")
	}

	wantedNames := make(map[string]struct{}, len(req.Names))
	for _, name := range req.Names {
		name = strings.TrimSpace(name)
		if name != "" {
			wantedNames[name] = struct{}{}
		}
	}

	results := make([]datasource.MetricsSnapshot, 0)

	for _, snapshot := range p.scenario.Metrics {
		if datasource.NormalizeServiceName(snapshot.Service) != service {
			continue
		}
		if !inTimeRange(snapshot.Timestamp, req.StartTime, req.EndTime) {
			continue
		}

		values := make(map[string]datasource.MetricValue)
		for name, value := range snapshot.Values {
			if len(wantedNames) > 0 {
				if _, exists := wantedNames[name]; !exists {
					continue
				}
			}
			values[name] = value
		}

		results = append(results, datasource.MetricsSnapshot{
			Service:   snapshot.Service,
			Timestamp: snapshot.Timestamp,
			Status:    snapshot.Status,
			Values:    values,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.Before(results[j].Timestamp)
	})

	return results, nil
}

func (p *Provider) SearchLogs(
	ctx context.Context,
	req datasource.LogsQuery,
) ([]datasource.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("mock provider cannot be nil")
	}

	query := strings.ToLower(strings.TrimSpace(req.Query))
	service := datasource.NormalizeServiceName(req.Service)

	results := make([]datasource.LogEntry, 0)

	for _, entry := range p.scenario.Logs {
		if service != "" &&
			datasource.NormalizeServiceName(entry.Service) != service {
			continue
		}
		if !inTimeRange(entry.Timestamp, req.StartTime, req.EndTime) {
			continue
		}
		if query != "" && !matchesQuery(entry, query) {
			continue
		}

		results = append(results, entry)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.Before(results[j].Timestamp)
	})

	return results, nil
}

func matchesQuery(entry datasource.LogEntry, query string) bool {
	content := strings.ToLower(
		entry.Service + " " + entry.Level + " " + entry.Message,
	)
	if strings.Contains(content, query) {
		return true
	}

	for _, token := range strings.FieldsFunc(query, func(r rune) bool {
		return !isTokenCharacter(r)
	}) {
		if len([]rune(token)) < 3 || isStopWord(token) {
			continue
		}
		if strings.Contains(content, token) {
			return true
		}
	}

	return false
}

func isTokenCharacter(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r > 127
}

func isStopWord(token string) bool {
	switch token {
	case "the", "and", "for", "with", "from", "into",
		"service", "services", "frequently", "returns",
		"return", "some", "部分", "服务", "发生", "出现":
		return true
	default:
		return false
	}
}

func inTimeRange(
	timestamp, start, end time.Time,
) bool {
	if !start.IsZero() && timestamp.Before(start) {
		return false
	}
	if !end.IsZero() && timestamp.After(end) {
		return false
	}
	return true
}

func sortedScenarioNames(scenarios map[string]Scenario) []string {
	names := make([]string, 0, len(scenarios))
	for name := range scenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
