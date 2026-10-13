package tool

import (
	"context"
	"fmt"
	"sort"
)

type Result struct {
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Tool interface {
	Name() string
	Description() string
	Execute(ctx context.Context, input map[string]any) (Result, error)
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("cannot register a nil tool")
	}

	name := t.Name()
	if name == "" {
		return fmt.Errorf("tool name cannot be empty")
	}

	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q is already registered", name)
	}

	r.tools[name] = t
	return nil
}

func (r *Registry) Execute(
	ctx context.Context,
	name string,
	input map[string]any,
) (Result, error) {
	t, exists := r.tools[name]
	if !exists {
		return Result{}, fmt.Errorf("tool %q not found", name)
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	return t.Execute(ctx, input)
}

func (r *Registry) List() []Tool {
	result := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})

	return result
}
