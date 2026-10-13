package main

import (
	"context"
	"fmt"
	"github.com/liocoder/TraceMind/internal/config"
	"os"
	"time"

	"github.com/liocoder/TraceMind/internal/llm"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	client, err := llm.NewClient(cfg.LLM)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		65*time.Second,
	)
	defer cancel()

	answer, err := client.Chat(
		ctx,
		"You are a senior site reliability engineer. Be concise and evidence-oriented.",
		"订单服务频繁超时并返回502。请列出三个可能根因，并说明每个根因需要什么证据验证。不要假装已经查过日志。",
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println(answer)
}
