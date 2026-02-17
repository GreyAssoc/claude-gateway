package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Usage holds token consumption data from a Claude interaction.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Response is what the sidecar returns.
type Response struct {
	Text      string `json:"text"`
	SessionID string `json:"session_id"`
	Usage     *Usage `json:"usage,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Client talks to the Node.js sidecar over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
	mu         sync.RWMutex
	healthy    bool
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		healthy: true,
	}
}

// Ask sends a prompt to Claude via the sidecar and returns the response.
func (c *Client) Ask(ctx context.Context, sessionID, prompt, workDir string, maxTurns int) (*Response, error) {
	payload := map[string]interface{}{
		"session_id": sessionID,
		"prompt":     prompt,
		"cwd":        workDir,
	}
	if maxTurns > 0 {
		payload["max_turns"] = maxTurns
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/ask", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.setHealthy(false)
		return nil, fmt.Errorf("sidecar request failed: %w", err)
	}
	defer resp.Body.Close()

	c.setHealthy(true)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sidecar returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Error != "" {
		return &result, fmt.Errorf("claude error: %s", result.Error)
	}

	return &result, nil
}

// ResetSession clears conversation history for a session.
func (c *Client) ResetSession(ctx context.Context, sessionID string) error {
	payload, _ := json.Marshal(map[string]string{"session_id": sessionID})

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/reset", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// Health checks if the sidecar is responding.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.setHealthy(false)
		return fmt.Errorf("sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.setHealthy(false)
		return fmt.Errorf("sidecar unhealthy: status %d", resp.StatusCode)
	}

	c.setHealthy(true)
	return nil
}

// IsHealthy returns the last known health status.
func (c *Client) IsHealthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthy
}

func (c *Client) setHealthy(h bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.healthy = h
}
