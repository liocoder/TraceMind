package agent

import (
	"encoding/json"

	"github.com/liocoder/TraceMind/internal/llm"
)

// estimateTokens 轻量估算文本 token 数：ASCII 约 4 字符 1 token，CJK 约 1 字符 1 token。
// 这是确定性近似，用于 token 预算的硬上限控制，不追求与具体模型分词器一致。
func estimateTokens(value string) int {
	ascii, cjk := 0, 0

	for _, r := range value {
		if r > 127 {
			cjk++
		} else {
			ascii++
		}
	}

	return ascii/4 + cjk
}

// estimateMessagesTokens 估算消息历史的 token 消耗。
func estimateMessagesTokens(messages []llm.Message) int {
	total := 0

	for _, message := range messages {
		total += estimateTokens(message.Role)
		total += estimateTokens(message.Content)

		if message.ToolCallID != "" {
			total += estimateTokens(message.ToolCallID)
		}

		for _, call := range message.ToolCalls {
			total += estimateTokens(call.ID)
			total += estimateTokens(call.Type)
			total += estimateTokens(call.Function.Name)
			total += estimateTokens(call.Function.Arguments)
		}
	}

	return total
}

// estimateToolDefinitionsTokens 估算工具定义的 token 消耗。
func estimateToolDefinitionsTokens(definitions []llm.ToolDefinition) int {
	if len(definitions) == 0 {
		return 0
	}

	data, err := json.Marshal(definitions)
	if err != nil {
		return 0
	}

	return estimateTokens(string(data))
}
