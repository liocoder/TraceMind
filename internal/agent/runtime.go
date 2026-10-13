package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/liocoder/TraceMind/internal/evidence"
	"github.com/liocoder/TraceMind/internal/tool"
)

const MaxToolCalls = 3

type Report struct {
	Incident     string              `json:"incident"`
	State        State               `json:"state"`
	StateHistory []State             `json:"state_history"`
	ToolCalls    int                 `json:"tool_calls"`
	Summary      string              `json:"summary"`
	Hypotheses   []string            `json:"hypotheses"`
	Evidence     []evidence.Evidence `json:"evidence"`
}

type Runtime struct {
	tools *tool.Registry
}

func NewRuntime(tools *tool.Registry) *Runtime {
	return &Runtime{tools: tools}
}

func (r *Runtime) Run(
	ctx context.Context,
	incident string,
) (*Report, error) {
	incident = strings.TrimSpace(incident)
	if incident == "" {
		return nil, fmt.Errorf("incident description cannot be empty")
	}

	sm := NewStateMachine()

	report := &Report{
		Incident:   incident,
		Hypotheses: []string{},
		Evidence:   []evidence.Evidence{},
	}

	transition := func(next State) error {
		if err := sm.Transition(next); err != nil {
			return err
		}
		report.State = sm.Current()
		report.StateHistory = sm.History()
		return nil
	}

	fail := func(err error) (*Report, error) {
		if sm.Current() != StateCompleted &&
			sm.Current() != StateFailed {
			_ = transition(StateFailed)
		}
		return report, err
	}

	if err := transition(StatePlanning); err != nil {
		return fail(err)
	}

	// The initial plan is deterministic:
	// 1. Search logs.
	// 2. Query service metrics.
	actions := []struct {
		name  string
		input map[string]any
	}{
		{
			name: "search_logs",
			input: map[string]any{
				"query": incident,
			},
		},
		{
			name: "query_metrics",
			input: map[string]any{
				"service": "order-service",
			},
		},
	}

	for i, action := range actions {
		if report.ToolCalls >= MaxToolCalls {
			return fail(fmt.Errorf(
				"tool call budget exceeded: limit=%d",
				MaxToolCalls,
			))
		}

		if err := ctx.Err(); err != nil {
			return fail(err)
		}

		if i > 0 {
			if err := transition(StatePlanning); err != nil {
				return fail(err)
			}
		}

		if err := transition(StateExecuting); err != nil {
			return fail(err)
		}

		result, err := r.tools.Execute(
			ctx,
			action.name,
			action.input,
		)
		report.ToolCalls++

		if err != nil {
			return fail(fmt.Errorf(
				"execute tool %q: %w",
				action.name,
				err,
			))
		}

		if err := transition(StateObserving); err != nil {
			return fail(err)
		}

		source := action.name
		if value, ok := result.Metadata["source"]; ok {
			source = value
		}

		report.Evidence = append(
			report.Evidence,
			evidence.New(
				fmt.Sprintf("ev-%03d", report.ToolCalls),
				source,
				result.Content,
			),
		)
	}

	// This is still a deterministic baseline, not LLM reasoning.
	logs := report.Evidence[0].Content
	metrics := report.Evidence[1].Content

	if strings.Contains(
		strings.ToLower(logs),
		"connection pool exhausted",
	) && strings.Contains(
		strings.ToLower(metrics),
		"connection_pool_usage=98%",
	) {
		report.Summary = "Logs and metrics both indicate possible connection pool saturation."
		report.Hypotheses = []string{
			"Order service connection pool saturation may be contributing to request timeouts.",
		}
	} else {
		report.Summary = "Evidence was collected, but the current rules did not identify a predefined root-cause pattern."
		report.Hypotheses = []string{
			"More evidence is needed to determine the root cause.",
		}
	}

	if err := transition(StateCompleted); err != nil {
		return fail(err)
	}

	return report, nil
}
