package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type ChatProvider interface {
	GenerateChat(ctx context.Context, systemPrompt string, messages []OpenAIMessage) (string, error)
	StreamChat(ctx context.Context, systemPrompt string, messages []OpenAIMessage, onDelta func(string) error) (string, error)
}

func getChatProvider() (ChatProvider, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("CHAT_AI_PROVIDER")))
	if provider == "" {
		provider = "openai"
	}

	switch provider {
	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is not configured")
		}
		model := os.Getenv("OPENAI_MODEL")
		if model == "" {
			model = "gpt-4o-mini"
		}
		return &OpenAIChatProvider{APIKey: apiKey, Model: model}, nil
	case "claude", "anthropic":
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY is not configured")
		}
		model := os.Getenv("ANTHROPIC_MODEL")
		if model == "" {
			model = "claude-sonnet-4-5"
		}
		version := os.Getenv("ANTHROPIC_VERSION")
		if version == "" {
			version = "2023-06-01"
		}
		maxTokens := 4096
		if raw := strings.TrimSpace(os.Getenv("ANTHROPIC_MAX_TOKENS")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("ANTHROPIC_MAX_TOKENS must be a positive integer")
			}
			maxTokens = parsed
		}
		return &AnthropicChatProvider{APIKey: apiKey, Model: model, Version: version, MaxTokens: maxTokens}, nil
	default:
		return nil, fmt.Errorf("unsupported CHAT_AI_PROVIDER: %s", provider)
	}
}
