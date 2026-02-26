// Package ollama provides an HTTP client for the Ollama API, supporting
// both embedding generation and LLM text generation.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Client communicates with the Ollama HTTP API.
type Client struct {
	baseURL    string
	embedModel string
	llmModel   string
	embedHTTP  *http.Client // short timeout for embeddings
	llmHTTP    *http.Client // longer timeout for generation
}

// NewClient creates a new Ollama client. Panics if baseURL is empty because
// every Client method constructs request URLs from baseURL.
func NewClient(baseURL, embedModel, llmModel string) *Client {
	if baseURL == "" {
		panic("ollama: empty baseURL")
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		embedModel: embedModel,
		llmModel:   llmModel,
		embedHTTP:  &http.Client{Timeout: 30 * time.Second},
		llmHTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// Health checks if Ollama is reachable by querying /api/tags.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("ollama health: %w", err)
	}
	resp, err := c.embedHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("ollama health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama health: status %d", resp.StatusCode)
	}
	return nil
}

// Embed generates a 768-dimensional embedding vector for the given text.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, fmt.Errorf("ollama embed: empty text")
	}

	body, err := json.Marshal(map[string]string{
		"model":  c.embedModel,
		"prompt": text,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama embed marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.embedHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ollama embed: status %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama embed decode: %w", err)
	}
	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama embed: empty embedding returned")
	}
	return result.Embedding, nil
}

// EmbedBatch generates embeddings for multiple texts sequentially.
func (c *Client) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, 0, len(texts))
	for _, text := range texts {
		emb, err := c.Embed(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("ollama embed batch: %w", err)
		}
		results = append(results, emb)
	}
	return results, nil
}

// Generate sends a prompt to the LLM and returns the response text.
func (c *Client) Generate(ctx context.Context, prompt, systemPrompt string) (string, error) {
	return c.generate(ctx, prompt, systemPrompt, "")
}

// generate is the internal implementation that supports an optional format parameter.
// When format is "json", Ollama constrains the model's output to valid JSON.
func (c *Client) generate(ctx context.Context, prompt, systemPrompt, format string) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("ollama generate: empty prompt")
	}

	payload := map[string]any{
		"model":  c.llmModel,
		"prompt": prompt,
		"stream": false,
	}
	if systemPrompt != "" {
		payload["system"] = systemPrompt
	}
	if format != "" {
		payload["format"] = format
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("ollama generate marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama generate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.llmHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama generate: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("ollama generate: status %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&result); err != nil {
		return "", fmt.Errorf("ollama generate decode: %w", err)
	}
	return result.Response, nil
}

// GenerateJSON sends a prompt to the LLM, expecting JSON output, and unmarshals
// the response into result. Uses Ollama's JSON mode (format: "json") to constrain
// the model to valid JSON output. Falls back to extracting JSON substring if needed.
func (c *Client) GenerateJSON(ctx context.Context, prompt, systemPrompt string, result any) error {
	text, err := c.generate(ctx, prompt, systemPrompt, "json")
	if err != nil {
		return err
	}

	text = strings.TrimSpace(text)

	// Log raw response for debugging (truncated to 500 chars)
	logText := text
	if len(logText) > 500 {
		logText = logText[:500] + "..."
	}
	log.Printf("Ollama GenerateJSON raw response (%d chars): %s", len(text), logText)

	// Try direct unmarshal first
	if err := json.Unmarshal([]byte(text), result); err == nil {
		return nil
	}

	// Fallback: extract JSON substring between first { and last }
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		jsonStr := text[start : end+1]
		if err := json.Unmarshal([]byte(jsonStr), result); err == nil {
			log.Printf("Ollama GenerateJSON: parsed via {}-extraction fallback")
			return nil
		}
	}

	// Fallback: try array extraction between [ and ]
	start = strings.Index(text, "[")
	end = strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		jsonStr := text[start : end+1]
		if err := json.Unmarshal([]byte(jsonStr), result); err == nil {
			log.Printf("Ollama GenerateJSON: parsed via []-extraction fallback")
			return nil
		}
	}

	return fmt.Errorf("ollama generate json: could not parse response as JSON: %.200s", text)
}
