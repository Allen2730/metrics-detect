package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Ingestion IngestionConfig `json:"ingestion"`
	AI        AIConfig        `json:"ai"`
	Detection DetectionConfig `json:"detection"`
	Report    ReportConfig    `json:"report"`
	Log       LogConfig       `json:"log"`
}

type IngestionConfig struct {
	Mode          string `json:"mode"`           // "file" | "prometheus"
	FilePath      string `json:"file_path"`
	PrometheusURL string `json:"prometheus_url"`
}

type AIConfig struct {
	Provider   string `json:"provider"` // "claude" | "deepseek"
	APIKey     string `json:"api_key"`
	APIKeyFile string `json:"api_key_file"`
	Model      string `json:"model"`
	Timeout    int    `json:"timeout"`
	Enabled    bool   `json:"enabled"`
}

type DetectionConfig struct {
	DeprecatedPrefixes       []string `json:"deprecated_prefixes"`
	DeprecatedSuffixes       []string `json:"deprecated_suffixes"`
	KnownServiceNames        []string `json:"known_service_names"`
	HighCardinalityThreshold int      `json:"high_cardinality_threshold"`
	RedundantPatterns        []string `json:"redundant_patterns"`
	// ServiceLabelKeys 按优先级排列的服务名标签 key 列表，孤儿检测时依次尝试
	ServiceLabelKeys []string `json:"service_label_keys"`
}

type ReportConfig struct {
	OutputDir      string `json:"output_dir"`
	EnableMarkdown bool   `json:"enable_markdown"`
	EnableExcel    bool   `json:"enable_excel"`
}

type LogConfig struct {
	Level string `json:"level"`
	File  string `json:"file"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	cfg.AI.APIKey = cfg.AI.resolveAPIKey()
	return &cfg, nil
}

// resolveAPIKey 优先级：环境变量 > api_key_file > api_key 字段
func (a *AIConfig) resolveAPIKey() string {
	envVar := "CLAUDE_API_KEY"
	if strings.ToLower(a.Provider) == "deepseek" {
		envVar = "DEEPSEEK_API_KEY"
	}
	if key := os.Getenv(envVar); key != "" {
		return key
	}
	if a.APIKeyFile != "" {
		data, err := os.ReadFile(a.APIKeyFile)
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return a.APIKey
}
