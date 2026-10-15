package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 是 TraceMind 的完整配置。
type Config struct {
	LLM   LLMConfig   `yaml:"llm"`
	Agent AgentConfig `yaml:"agent"`
}

// LLMConfig 是大模型客户端配置。
type LLMConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

// AgentConfig 是 Agent Runtime 配置。
type AgentConfig struct {
	MaxToolCalls   int `yaml:"max_tool_calls"`
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// MaxTokens 是单次事件调查的估算 token 硬上限。
	// 小于等于 0 表示不启用 token 预算。
	MaxTokens int `yaml:"max_tokens"`
}

// localConfig 只用于读取本地敏感配置，不应提交到 Git。
type localConfig struct {
	LLM struct {
		APIKey string `yaml:"api_key"`
	} `yaml:"llm"`
}

// Load 加载配置文件。
// 先读取公开配置，再使用同目录下 config.local.yaml 中的密钥覆盖公开配置。
// 本地配置文件不存在时不会报错，但最终 API Key 不能为空。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// 加载本地配置，允许其中的 API Key 覆盖公开配置。
	localPath := filepath.Join(filepath.Dir(path), "config.local.yaml")
	localData, err := os.ReadFile(localPath)
	if err == nil {
		var local localConfig
		if err := yaml.Unmarshal(localData, &local); err != nil {
			return nil, fmt.Errorf("parse local config file: %w", err)
		}

		if key := strings.TrimSpace(local.LLM.APIKey); key != "" {
			cfg.LLM.APIKey = key
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read local config file: %w", err)
	}

	// 清理配置项中的首尾空白。
	cfg.LLM.APIKey = strings.TrimSpace(cfg.LLM.APIKey)
	cfg.LLM.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.LLM.BaseURL), "/")
	cfg.LLM.Model = strings.TrimSpace(cfg.LLM.Model)

	// 设置默认值。
	if cfg.LLM.BaseURL == "" {
		cfg.LLM.BaseURL = "https://api.deepseek.com"
	}
	if cfg.Agent.MaxToolCalls <= 0 {
		cfg.Agent.MaxToolCalls = 3
	}
	if cfg.Agent.TimeoutSeconds <= 0 {
		cfg.Agent.TimeoutSeconds = 60
	}

	// 校验必要配置。
	if cfg.LLM.APIKey == "" {
		return nil, fmt.Errorf(
			"LLM API key is required: set llm.api_key in config.local.yaml",
		)
	}
	if cfg.LLM.Model == "" {
		return nil, fmt.Errorf("LLM model is required: set llm.model in config.yaml")
	}

	return &cfg, nil
}
