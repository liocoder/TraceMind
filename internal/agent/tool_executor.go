package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liocoder/TraceMind/internal/evidence"
	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/tool"
)

// ToolExecutionResult 保存一轮工具执行的结果。
type ToolExecutionResult struct {
	Messages []llm.Message
	Evidence []evidence.Evidence
	Executed int
}

// ExecuteToolCalls 执行模型返回的工具调用。
// 这里只执行 Registry 中已经注册的工具，不允许模型直接执行任意代码。
func ExecuteToolCalls(
	ctx context.Context,
	registry *tool.Registry,
	calls []llm.ToolCall,
) (*ToolExecutionResult, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry cannot be nil")
	}

	result := &ToolExecutionResult{
		Messages: make([]llm.Message, 0, len(calls)),
		Evidence: make([]evidence.Evidence, 0, len(calls)),
	}

	for i, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if call.ID == "" {
			return nil, fmt.Errorf(
				"tool call at index %d has an empty ID",
				i,
			)
		}

		if call.Type != "" && call.Type != "function" {
			result.Messages = append(result.Messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("unsupported tool call type %q", call.Type),
			})
			continue
		}

		if call.Function.Name == "" {
			result.Messages = append(result.Messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "tool name cannot be empty",
			})
			continue
		}

		// 将模型返回的 JSON 参数解析成 Go map。
		var input map[string]any
		if err := json.Unmarshal(
			[]byte(call.Function.Arguments),
			&input,
		); err != nil {
			result.Messages = append(result.Messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("invalid tool arguments JSON: %v", err),
			})
			continue
		}

		if input == nil {
			result.Messages = append(result.Messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "tool arguments must be a JSON object",
			})
			continue
		}

		// Registry 只会执行已注册的工具。
		toolResult, err := registry.Execute(
			ctx,
			call.Function.Name,
			input,
		)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			result.Messages = append(result.Messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content: fmt.Sprintf(
					"tool execution failed: %v",
					err,
				),
			})
			continue
		}

		result.Executed++

		// 把执行结果转换为模型下一轮可以读取的 tool 消息。
		result.Messages = append(result.Messages, llm.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    toolResult.Content,
		})

		// 只为成功执行的工具结果建立证据。
		source := call.Function.Name
		if value, ok := toolResult.Metadata["source"]; ok && value != "" {
			source = value
		}

		result.Evidence = append(
			result.Evidence,
			evidence.New(
				fmt.Sprintf("ev-tool-%03d", i+1),
				source,
				toolResult.Content,
			),
		)
	}

	return result, nil
}
