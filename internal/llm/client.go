package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/liocoder/TraceMind/internal/config"
)

// Client 是大模型 API 客户端。
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// Message 表示 Chat Completions API 中的一条消息。
// ToolCallID 用于把工具执行结果关联回模型发起的工具调用。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolDefinition 描述一个可以提供给模型使用的工具。
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition 描述工具名称、用途和参数结构。
type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall 是模型返回的一次工具调用请求。
// Arguments 是 JSON 字符串，需要由调用方进一步解析和校验。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction 包含模型选择的函数名称及参数。
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Completion 是一次模型响应。
type Completion struct {
	Message      Message
	FinishReason string
}

// chatRequest 是发送给 Chat Completions API 的请求。
type chatRequest struct {
	Model    string           `json:"model"`
	Messages []Message        `json:"messages"`
	Tools    []ToolDefinition `json:"tools,omitempty"`
}

// chatResponse 是 Chat Completions API 的响应。
type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
}

// NewClient 创建一个大模型客户端。
func NewClient(cfg config.LLMConfig) (*Client, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("llm.api_key is required")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("llm.model is required")
	}

	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, nil
}

func (c *Client) Chat(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
) (string, error) {
	completion, err := c.Complete(ctx, []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}, nil)
	if err != nil {
		return "", err
	}

	if len(completion.Message.ToolCalls) > 0 {
		return "", fmt.Errorf("unexpected tool calls in Chat response")
	}

	return completion.Message.Content, nil
}

// ChatWithTools 发送一轮对话，并允许模型选择是否调用工具。
// 此方法只返回模型的决定，不会实际执行工具。
func (c *Client) ChatWithTools(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	tools []ToolDefinition,
) (*Completion, error) {
	return c.Complete(ctx, []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}, tools)
}

// Complete 发送完整消息历史，可选附带工具定义。
// 后续 Agent Runtime 可以用它连续发送 assistant、tool 等消息。
func (c *Client) Complete(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
) (*Completion, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages cannot be empty")
	}

	payload := chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call model API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(
			"model API returned HTTP %d: %s",
			resp.StatusCode,
			string(respBody),
		)
	}

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("model API returned no choices")
	}

	choice := result.Choices[0]

	return &Completion{
		Message:      choice.Message,
		FinishReason: choice.FinishReason,
	}, nil
}
