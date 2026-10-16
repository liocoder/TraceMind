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
	"github.com/liocoder/TraceMind/internal/datasource/mock"
	"github.com/liocoder/TraceMind/internal/llm"
	"github.com/liocoder/TraceMind/internal/skills"
	"github.com/liocoder/TraceMind/internal/tool"
)

func main() {
	configPath := flag.String(
		"config",
		"config.local.yaml",
		"Path to the configuration file",
	)
	incidentFlag := flag.String(
		"incident",
		"",
		"Incident description to investigate",
	)
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
	noSkills := flag.Bool(
		"no-skills",
		false,
		"Disable the skills library (ablation baseline)",
	)
	singleShot := flag.Bool(
		"single-shot",
		false,
		"Single-round LLM baseline without tools (ablation baseline)",
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

	provider, err := mock.NewProvider(*datasetPath, *scenarioID)
	if err != nil {
		log.Fatalf("initialize mock provider: %v", err)
	}

	registry := tool.NewRegistry()

	if err := registry.Register(tool.NewLogsSearchTool(provider)); err != nil {
		log.Fatalf("register log tool: %v", err)
	}

	if err := registry.Register(tool.NewMetricsQueryTool(provider)); err != nil {
		log.Fatalf("register metrics tool: %v", err)
	}

	runtime := agent.NewRuntime(registry, client)

	// 消融基线开关：
	// -no-skills   关闭技能库与 load_skill 工具（对照"无 Skill 多轮 Agent"）
	// -single-shot 单轮 LLM 基线，不经规划与工具调查直接输出诊断
	if !*noSkills && !*singleShot {
		library, err := skills.Load(*skillsPath)
		if err != nil {
			log.Fatalf("initialize skills library: %v", err)
		}

		if err := registry.Register(tool.NewLoadSkillTool(library)); err != nil {
			log.Fatalf("register load_skill tool: %v", err)
		}

		runtime.SetSkillsLibrary(library)
	}

	if *singleShot {
		runtime.SetSingleShot(true)
	}

	if cfg.Agent.MaxToolCalls > 0 {
		runtime.SetMaxToolCalls(cfg.Agent.MaxToolCalls)
	}

	if cfg.Agent.MaxTokens > 0 {
		runtime.SetMaxTokens(cfg.Agent.MaxTokens)
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

	fmt.Printf("Investigating incident: %s\n", incident)
	fmt.Printf("Mock dataset: %s\n", *datasetPath)
	fmt.Printf("Mock scenario: %s\n\n", *scenarioID)

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
