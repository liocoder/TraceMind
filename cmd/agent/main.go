package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/liocoder/TraceMind/internal/agent"
	"github.com/liocoder/TraceMind/internal/config"
	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/tool"
)

func main() {
	configPath := flag.String(
		"config",
		"config.yaml",
		"Path to the configuration file",
	)
	incidentFlag := flag.String(
		"incident",
		"",
		"Incident description to investigate",
	)
	flag.Parse()

	incident := strings.TrimSpace(*incidentFlag)
	if incident == "" {
		fmt.Println("Enter the incident description:")
		fmt.Println("Example: The order service frequently times out and returns HTTP 502.")
		fmt.Print("> ")

		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil && len(input) == 0 {
			log.Fatalf("read incident description: %v", err)
		}
		incident = strings.TrimSpace(input)
	}

	if incident == "" {
		log.Fatal("incident description cannot be empty")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	client, err := llm.NewClient(cfg.LLM)
	if err != nil {
		log.Fatalf("initialize LLM client: %v", err)
	}

	registry := tool.NewRegistry()

	if err := registry.Register(&tool.MockLogSearch{}); err != nil {
		log.Fatalf("register log tool: %v", err)
	}

	if err := registry.Register(&tool.MockMetricsQuery{}); err != nil {
		log.Fatalf("register metrics tool: %v", err)
	}

	runtime := agent.NewRuntime(registry, client)

	if cfg.Agent.MaxToolCalls > 0 {
		runtime.SetMaxToolCalls(cfg.Agent.MaxToolCalls)
	}

	timeoutSeconds := cfg.Agent.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	report, err := runtime.Run(ctx, incident)
	if err != nil {
		if report != nil {
			fmt.Fprintln(os.Stderr, "Partial report:")
			if encodeErr := printJSON(os.Stderr, report); encodeErr != nil {
				log.Printf("encode partial report: %v", encodeErr)
			}
		}
		log.Fatalf("incident diagnosis failed: %v", err)
	}

	if err := printJSON(os.Stdout, report); err != nil {
		log.Fatalf("encode report: %v", err)
	}
}

func printJSON(out *os.File, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
