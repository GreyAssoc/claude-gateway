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

// GenerateOpts controls Ollama generation parameters for performance tuning.
type GenerateOpts struct {
	// NumPredict caps the maximum tokens generated. Lower = faster for short outputs.
	// 0 means use Ollama's default.
	NumPredict int
	// Temperature controls randomness. 0 = greedy (fastest), 1 = default sampling.
	// Negative means use Ollama's default.
	Temperature float64
	// KeepAlive tells Ollama how long to keep the model loaded after the request.
	// Examples: "10m", "1h", "0" (unload immediately). Empty = Ollama default (5m).
	KeepAlive string
}

// Generate sends a prompt to the LLM and returns the response text.
func (c *Client) Generate(ctx context.Context, prompt, systemPrompt string) (string, error) {
	return c.generate(ctx, prompt, systemPrompt, "", nil)
}

// runnerDied reports Ollama's GPU-runner crash. vLLM holds the host GPU, so
// loading qwen2.5:7b onto it returns this and kills the runner. A CPU retry
// succeeds.
func runnerDied(err error) bool {
	return err != nil && strings.Contains(err.Error(), "llama runner process has terminated")
}

// generate is the internal implementation that supports an optional format parameter
// and performance options. When format is "json", Ollama constrains output to valid JSON.
// A GPU runner crash is retried once on CPU.
func (c *Client) generate(ctx context.Context, prompt, systemPrompt, format string, opts *GenerateOpts) (string, error) {
	out, err := c.doGenerate(ctx, prompt, systemPrompt, format, opts, false)
	if err == nil || !runnerDied(err) {
		return out, err
	}
	log.Printf("ollama: %s runner crashed; retrying on CPU", c.llmModel)
	return c.doGenerate(ctx, prompt, systemPrompt, format, opts, true)
}

// doGenerate performs one /api/generate call. forceCPU sets num_gpu=0 so the
// model does not try to share the GPU with vLLM.
func (c *Client) doGenerate(ctx context.Context, prompt, systemPrompt, format string, opts *GenerateOpts, forceCPU bool) (string, error) {
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
	options := map[string]any{}
	if opts != nil {
		if opts.NumPredict > 0 {
			options["num_predict"] = opts.NumPredict
		}
		if opts.Temperature >= 0 {
			options["temperature"] = opts.Temperature
		}
		if opts.KeepAlive != "" {
			payload["keep_alive"] = opts.KeepAlive
		}
	}
	if forceCPU {
		options["num_gpu"] = 0
	}
	if len(options) > 0 {
		payload["options"] = options
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

// FastClassifyOpts are tuned for short JSON classification tasks like triage.
// Greedy decoding (temperature=0), capped output (256 tokens), model kept warm (10m).
var FastClassifyOpts = &GenerateOpts{
	NumPredict:  256,
	Temperature: 0,
	KeepAlive:   "10m",
}

// GenerateJSON sends a prompt to the LLM, expecting JSON output, and unmarshals
// the response into result. Uses Ollama's JSON mode (format: "json") to constrain
// the model to valid JSON output. Falls back to extracting JSON substring if needed.
func (c *Client) GenerateJSON(ctx context.Context, prompt, systemPrompt string, result any) error {
	return c.GenerateJSONWithOpts(ctx, prompt, systemPrompt, result, nil)
}

// GenerateJSONWithOpts is like GenerateJSON but accepts performance tuning options.
func (c *Client) GenerateJSONWithOpts(ctx context.Context, prompt, systemPrompt string, result any, opts *GenerateOpts) error {
	text, err := c.generate(ctx, prompt, systemPrompt, "json", opts)
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
