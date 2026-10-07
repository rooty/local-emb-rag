// Package embed is a client for OpenAI-compatible /embeddings endpoints
// (Ollama, llama-server, vLLM).
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Model   string
	APIKey  string
	// Dims truncates vectors (Matryoshka); 0 keeps the full size.
	Dims int
	HTTP *http.Client
}

func New(baseURL, model, apiKey string, dims int, timeout time.Duration) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		APIKey:  apiKey,
		Dims:    dims,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

type request struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type response struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one L2-normalized vector per input, truncated to Dims.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	body, _ := json.Marshal(request{Model: c.Model, Input: inputs})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embeddings: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: %s: %s", resp.Status, truncate(string(data), 300))
	}
	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("embeddings: decode: %w", err)
	}
	if len(r.Data) != len(inputs) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(r.Data), len(inputs))
	}
	out := make([][]float32, len(inputs))
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= len(out) || out[d.Index] != nil {
			return nil, fmt.Errorf("embeddings: bad index %d in response", d.Index)
		}
		v, err := Normalize(d.Embedding, c.Dims)
		if err != nil {
			return nil, fmt.Errorf("embeddings: input %d: %w", d.Index, err)
		}
		out[d.Index] = v
	}
	return out, nil
}

// Normalize truncates v to dims (when 0 < dims < len(v)) and scales it to unit length.
// Truncating before normalizing is how Matryoshka embeddings are meant to be shortened.
func Normalize(v []float64, dims int) ([]float32, error) {
	if dims > 0 {
		if dims > len(v) {
			return nil, fmt.Errorf("model returned %d dims, config asks for %d", len(v), dims)
		}
		v = v[:dims]
	}
	var sum float64
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("vector contains NaN/Inf (with EmbeddingGemma 2 this usually means FP16 inference)")
		}
		sum += x * x
	}
	if sum == 0 {
		return nil, fmt.Errorf("zero vector")
	}
	norm := math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x / norm)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
