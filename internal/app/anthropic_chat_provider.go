package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type AnthropicChatProvider struct {
	APIKey    string
	Model     string
	Version   string
	MaxTokens int
}

type AnthropicRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []OpenAIMessage `json:"messages"`
	Stream    bool            `json:"stream,omitempty"`
}

type AnthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type AnthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *AnthropicChatProvider) GenerateChat(ctx context.Context, systemPrompt string, messages []OpenAIMessage) (string, error) {
	reqBody := AnthropicRequest{
		Model:     p.Model,
		MaxTokens: p.MaxTokens,
		System:    systemPrompt,
		Messages:  anthropicMessages(messages),
	}
	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("Anthropic request marshal failed: %w", err)
	}

	req, err := p.newRequest(ctx, reqBytes)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Anthropic request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Anthropic API error: %d %s", resp.StatusCode, string(bodyBytes))
	}

	var anthropicResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		return "", fmt.Errorf("Anthropic response decode failed: %w", err)
	}

	var out strings.Builder
	for _, block := range anthropicResp.Content {
		if block.Type == "text" {
			out.WriteString(block.Text)
		}
	}
	if out.Len() == 0 {
		return "", fmt.Errorf("Anthropic response contained no text")
	}
	return out.String(), nil
}

func (p *AnthropicChatProvider) StreamChat(ctx context.Context, systemPrompt string, messages []OpenAIMessage, onDelta func(string) error) (string, error) {
	reqBody := AnthropicRequest{
		Model:     p.Model,
		MaxTokens: p.MaxTokens,
		System:    systemPrompt,
		Messages:  anthropicMessages(messages),
		Stream:    true,
	}
	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("Anthropic stream request marshal failed: %w", err)
	}

	req, err := p.newRequest(ctx, reqBytes)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Anthropic stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Anthropic API error: %d %s", resp.StatusCode, string(bodyBytes))
	}

	var accumulated strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event AnthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if event.Type == "error" {
			return "", fmt.Errorf("Anthropic stream error: %s %s", event.Error.Type, event.Error.Message)
		}
		if event.Type != "content_block_delta" || event.Delta.Type != "text_delta" || event.Delta.Text == "" {
			continue
		}
		accumulated.WriteString(event.Delta.Text)
		if err := onDelta(accumulated.String()); err != nil {
			return "", err
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("Anthropic stream read failed: %w", err)
	}
	return accumulated.String(), nil
}

func (p *AnthropicChatProvider) newRequest(ctx context.Context, reqBytes []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("Anthropic request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", p.Version)
	return req, nil
}

func anthropicMessages(messages []OpenAIMessage) []OpenAIMessage {
	out := make([]OpenAIMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		out = append(out, msg)
	}
	return out
}
