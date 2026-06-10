package claude

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Provider Claude API 实现
type Provider struct {
	client *anthropic.Client
	model  string
}

func New(apiKey, model string) *Provider {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Provider{client: &client, model: model}
}

func (p *Provider) Name() string { return "claude" }

func (p *Provider) Analyze(ctx context.Context, prompt string) (string, error) {
	msg, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: 4096,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		// 截断错误信息以防 SDK 在错误描述中包含 API Key
		errMsg := err.Error()
		if len(errMsg) > 256 {
			errMsg = errMsg[:256] + "...(truncated)"
		}
		return "", fmt.Errorf("claude API call failed: %s", errMsg)
	}
	if len(msg.Content) == 0 {
		return "", fmt.Errorf("claude returned empty response")
	}
	return msg.Content[0].Text, nil
}
