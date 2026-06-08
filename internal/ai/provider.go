package ai

import "context"

// LLMProvider AI 大模型提供者接口
type LLMProvider interface {
	// Name 返回 provider 标识
	Name() string
	// Analyze 发送 prompt，返回原始响应文本
	Analyze(ctx context.Context, prompt string) (string, error)
}
