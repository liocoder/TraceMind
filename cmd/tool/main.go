package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

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

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.Agent.TimeoutSeconds)*time.Second,
	)
	defer cancel()

	completion, err := client.ChatWithTools(
		ctx,
		`You are an incident diagnosis agent.
Choose tools based on the incident.
Use search_logs to investigate relevant log errors.
Use query_metrics to inspect a service's health metrics.
Do not invent tool results.
If evidence is not available yet, request an appropriate tool call.`,
		"The order service frequently times out and some requests return HTTP 502. Investigate the incident.",
		definitions,
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Finish reason:", completion.FinishReason)
	fmt.Println("Assistant message:", completion.Message.Content)

	if len(completion.Message.ToolCalls) == 0 {
		fmt.Println("No tool calls returned.")
		return
	}

	for i, call := range completion.Message.ToolCalls {
		fmt.Printf("\nTool call %d\n", i+1)
		fmt.Println("ID:", call.ID)
		fmt.Println("Name:", call.Function.Name)
		fmt.Println("Arguments:", call.Function.Arguments)
	}
}
