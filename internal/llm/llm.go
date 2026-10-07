// Package llm is a minimal client for OpenAI-compatible /chat/completions
// endpoints (Ollama, llama-server, vLLM).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL     string
	Model       string
	APIKey      string
	Temperature float64
	HTTP        *http.Client
}

func New(baseURL, model, apiKey string, temperature float64, timeout time.Duration) *Client {
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Model:       model,
		APIKey:      apiKey,
		Temperature: temperature,
		HTTP:        &http.Client{Timeout: timeout},
	}
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}

type response struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

func (c *Client) Chat(ctx context.Context, msgs []Message) (string, error) {
	body, _ := json.Marshal(request{Model: c.Model, Messages: msgs, Temperature: c.Temperature})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("chat: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		s := string(data)
		if len(s) > 300 {
			s = s[:300] + "..."
		}
		return "", fmt.Errorf("chat: %s: %s", resp.Status, s)
	}
	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("chat: decode: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("chat: empty response")
	}
	return strings.TrimSpace(r.Choices[0].Message.Content), nil
}
