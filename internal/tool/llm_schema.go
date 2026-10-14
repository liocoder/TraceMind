package tool

import (
	"encoding/json"
	"fmt"

	"github.com/liocoder/TraceMind/internal/llm"
)

// ToLLMDefinitions 将已注册工具转换为模型可识别的 Function Calling 定义。
// 工具列表来自 Registry，参数 Schema 在这里集中维护。
func (r *Registry) ToLLMDefinitions() ([]llm.ToolDefinition, error) {
	tools := r.List()
	definitions := make([]llm.ToolDefinition, 0, len(tools))

	for _, t := range tools {
		var parameters map[string]any

		switch t.Name() {
		case "search_logs":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Keywords or incident description to search in service logs",
					},
				},
				"required":             []string{"query"},
				"additionalProperties": false,
			}

		case "query_metrics":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"service": map[string]any{
						"type":        "string",
						"description": "Name of the service whose health metrics should be queried",
					},
				},
				"required":             []string{"service"},
				"additionalProperties": false,
			}

		default:
			return nil, fmt.Errorf(
				"missing LLM parameter schema for tool %q",
				t.Name(),
			)
		}

		schemaJSON, err := json.Marshal(parameters)
		if err != nil {
			return nil, fmt.Errorf(
				"marshal schema for tool %q: %w",
				t.Name(),
				err,
			)
		}

		definitions = append(definitions, llm.ToolDefinition{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  json.RawMessage(schemaJSON),
			},
		})
	}

	return definitions, nil
}
