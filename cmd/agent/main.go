package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/liocoder/TraceMind/internal/agent"
	"github.com/liocoder/TraceMind/internal/tool"
)

func main() {
	fmt.Println("TraceMind - Incident Diagnosis Agent")
	fmt.Print("Describe the incident: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "read incident: %v\n", err)
		}
		os.Exit(1)
	}

	incident := strings.TrimSpace(scanner.Text())
	if incident == "" {
		fmt.Fprintln(os.Stderr, "incident description cannot be empty")
		os.Exit(1)
	}

	registry := tool.NewRegistry()

	if err := registry.Register(&tool.MockLogSearch{}); err != nil {
		fmt.Fprintf(os.Stderr, "register log tool: %v\n", err)
		os.Exit(1)
	}

	if err := registry.Register(&tool.MockMetricsQuery{}); err != nil {
		fmt.Fprintf(os.Stderr, "register metrics tool: %v\n", err)
		os.Exit(1)
	}

	runtime := agent.NewRuntime(registry)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	report, err := runtime.Run(ctx, incident)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnosis failed: %v\n", err)
		os.Exit(1)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode report: %v\n", err)
		os.Exit(1)
	}
}
