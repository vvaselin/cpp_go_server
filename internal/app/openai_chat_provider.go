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

type OpenAIChatProvider struct {
	APIKey string
	Model  string
}

func (p *OpenAIChatProvider) GenerateChat(ctx context.Context, systemPrompt string, messages []OpenAIMessage) (string, error) {
	reqMessages := append([]OpenAIMessage{{Role: "system", Content: systemPrompt}}, messages...)
	reqBody := OpenAIRequest{
		Model:          p.Model,
		Messages:       reqMessages,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}
	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("OpenAI request marshal failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBytes))
	if err != nil {
		return "", fmt.Errorf("OpenAI request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("OpenAI request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OpenAI API error: %d %s", resp.StatusCode, string(bodyBytes))
	}

	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return "", fmt.Errorf("OpenAI response decode failed: %w", err)
	}
	if len(openAIResp.Choices) == 0 {
		return "", fmt.Errorf("OpenAI response contained no choices")
	}
	return openAIResp.Choices[0].Message.Content, nil
}

func (p *OpenAIChatProvider) StreamChat(ctx context.Context, systemPrompt string, messages []OpenAIMessage, onDelta func(string) error) (string, error) {
	reqMessages := append([]OpenAIMessage{{Role: "system", Content: systemPrompt}}, messages...)
	reqBody := OpenAIStreamRequest{
		Model:          p.Model,
		Messages:       reqMessages,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
		Stream:         true,
	}
	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("OpenAI stream request marshal failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBytes))
	if err != nil {
		return "", fmt.Errorf("OpenAI stream request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("OpenAI stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OpenAI API error: %d %s", resp.StatusCode, string(bodyBytes))
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
		if data == "[DONE]" {
			break
		}

		var chunk OpenAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}
		accumulated.WriteString(content)
		if err := onDelta(accumulated.String()); err != nil {
			return "", err
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("OpenAI stream read failed: %w", err)
	}
	return accumulated.String(), nil
}
