// Command mcp-server exposes TraceMind's investigation tools as a Model
// Context Protocol (MCP) server over the stdio transport.
//
// It speaks the MCP subset implemented by internal/mcp (initialize, ping,
// tools/list, tools/call, shutdown) and serves the same mock observation
// dataset used by cmd/agent. No LLM configuration is required because the
// server only executes tools; swap the mock provider for a real datasource
// behind the same Provider interface and the protocol layer stays unchanged.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/liocoder/TraceMind/internal/datasource/mock"
	"github.com/liocoder/TraceMind/internal/mcp"
	"github.com/liocoder/TraceMind/internal/skills"
	"github.com/liocoder/TraceMind/internal/tool"
)

func main() {
	// The MCP stdio protocol owns stdout, so all diagnostics must go to stderr.
	log.SetOutput(os.Stderr)

	datasetPath := flag.String(
		"dataset",
		"evals/datasets/observations.json",
		"Path to the mock observation dataset",
	)
	scenarioID := flag.String(
		"scenario",
		"connection_pool_exhaustion",
		"Scenario ID in the mock observation dataset",
	)
	skillsPath := flag.String(
		"skills",
		"skills",
		"Path to the investigation skills directory",
	)
	flag.Parse()

	provider, err := mock.NewProvider(*datasetPath, *scenarioID)
	if err != nil {
		log.Fatalf("initialize mock data provider: %v", err)
	}

	library, err := skills.Load(*skillsPath)
	if err != nil {
		log.Fatalf("initialize skills library: %v", err)
	}

	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewLogsSearchTool(provider)); err != nil {
		log.Fatalf("register log search tool: %v", err)
	}
	if err := registry.Register(tool.NewMetricsQueryTool(provider)); err != nil {
		log.Fatalf("register metrics query tool: %v", err)
	}
	if err := registry.Register(tool.NewLoadSkillTool(library)); err != nil {
		log.Fatalf("register load_skill tool: %v", err)
	}

	server := mcp.NewServer(registry, os.Stdin, os.Stdout)
	if err := server.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
