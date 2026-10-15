package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/liocoder/TraceMind/internal/agent"
	"github.com/liocoder/TraceMind/internal/config"
	"github.com/liocoder/TraceMind/internal/datasource/mock"
	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/tool"
)

func main() {
	datasetPath := flag.String(
		"dataset",
		"evals/datasets/observations.json",
		"Path to the mock observation dataset",
	)
	scenarioID := flag.String(
		"scenario",
		"default",
		"Scenario ID in the mock observation dataset",
	)
	flag.Parse()

	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	client, err := llm.NewClient(cfg.LLM)
	if err != nil {
		log.Fatal(err)
	}

	provider, err := mock.NewProvider(*datasetPath, *scenarioID)
	if err != nil {
		log.Fatalf("initialize mock runbooks provider: %v", err)
	}

	registry := tool.NewRegistry()

	if err := registry.Register(tool.NewLogsSearchTool(provider)); err != nil {
		log.Fatalf("register log search tool: %v", err)
	}
	if err := registry.Register(tool.NewMetricsQueryTool(provider)); err != nil {
		log.Fatalf("register metrics query tool: %v", err)
	}

	definitions, err := registry.ToLLMDefinitions()
	if err != nil {
		log.Fatal(err)
	}

	maxToolCalls := cfg.Agent.MaxToolCalls
	if maxToolCalls <= 0 {
		maxToolCalls = 3
	}

	timeoutSeconds := cfg.Agent.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	systemPrompt := `You are an incident diagnosis agent.

Investigate incidents using the available tools.
Use tool results as evidence. Never invent observations.
Distinguish observed facts from hypotheses.
After gathering sufficient evidence, provide a concise diagnosis.

Important tool budget rules:
- Respect the remaining tool-call budget provided by the user.
- Never request more tool calls than the remaining budget.
- When the remaining budget is zero, do not request any tools.
- Instead, immediately provide your final diagnosis using the evidence already collected.
- Clearly state uncertainty and do not claim that unobserved systems were verified.`

	messages := []llm.Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: "The order service frequently times out and some requests return HTTP 502. Investigate the incident.",
		},
	}

	toolCallsUsed := 0

	// 最多执行 maxToolCalls 次工具调用。
	// 额外的模型轮次仅用于生成最终诊断，不执行工具。
	for round := 1; round <= maxToolCalls+2; round++ {
		remaining := maxToolCalls - toolCallsUsed

		fmt.Printf(
			"\n========== Model round %d (remaining tool calls: %d) ==========\n",
			round,
			remaining,
		)

		// 预算耗尽时，不再向模型提供工具定义，避免继续调用工具。
		availableTools := definitions
		if remaining <= 0 {
			availableTools = nil
			messages = append(messages, llm.Message{
				Role: "user",
				Content: fmt.Sprintf(
					"Your tool-call budget is exhausted. You have collected evidence from previous tool results. Do not call any tools. Provide your final incident diagnosis now. Separate observed facts, likely cause, and remaining uncertainty. The maximum tool-call budget was %d.",
					maxToolCalls,
				),
			})
		} else {
			// 明确告知模型剩余预算。
			messages = append(messages, llm.Message{
				Role: "system",
				Content: fmt.Sprintf(
					"You have %d tool calls remaining. Request no more than %d tool calls in this response. If fewer calls are sufficient, use fewer. If evidence is sufficient, provide a final answer without calling tools.",
					remaining,
					remaining,
				),
			})
		}

		completion, err := client.Complete(ctx, messages, availableTools)
		if err != nil {
			log.Fatalf("call model: %v", err)
		}

		assistantMessage := completion.Message
		if assistantMessage.Role == "" {
			assistantMessage.Role = "assistant"
		}

		// 模型正常返回最终回答。
		if len(assistantMessage.ToolCalls) == 0 {
			fmt.Println("\n========== Final answer ==========")
			fmt.Println(assistantMessage.Content)
			fmt.Printf("\nTotal tool calls executed: %d/%d\n", toolCallsUsed, maxToolCalls)
			return
		}

		// 预算耗尽后，绝不执行工具。
		if remaining <= 0 {
			log.Println("Model requested tools after budget exhaustion; requesting a final answer.")

			// 不执行这些调用，也不把未执行的工具调用消息加入对话历史。
			// 重新要求模型生成普通文本回答。
			messages = append(messages, llm.Message{
				Role:    "assistant",
				Content: "I will stop tool use and provide the diagnosis based on the evidence already collected.",
			})
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "Return your final diagnosis as plain text now. Do not call tools.",
			})
			continue
		}

		// 防止模型单轮请求数超过剩余预算。
		allowedCount := len(assistantMessage.ToolCalls)
		if allowedCount > remaining {
			allowedCount = remaining
		}

		// 只保留允许执行的工具调用，避免产生未执行的工具调用记录。
		assistantMessage.ToolCalls = assistantMessage.ToolCalls[:allowedCount]
		messages = append(messages, assistantMessage)

		execution, err := agent.ExecuteToolCalls(
			ctx,
			registry,
			assistantMessage.ToolCalls,
		)
		if err != nil {
			log.Fatalf("execute tool calls: %v", err)
		}

		// 按尝试执行的调用数计入预算，防止失败调用导致预算绕过。
		toolCallsUsed += allowedCount

		for i, call := range assistantMessage.ToolCalls {
			fmt.Printf("Tool: %s\n", call.Function.Name)
			fmt.Printf("Arguments: %s\n", call.Function.Arguments)

			if i < len(execution.Messages) {
				fmt.Printf("Result: %s\n", execution.Messages[i].Content)
			}
		}

		messages = append(messages, execution.Messages...)
	}

	log.Fatalf(
		"agent stopped without a final answer after %d tool calls",
		toolCallsUsed,
	)
}
