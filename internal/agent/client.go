// Package agent provides a WebSocket client for communicating with the Claude CLI sidecar.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
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
	Text          string `json:"text"`
	SessionID     string `json:"session_id"`
	Usage         *Usage `json:"usage,omitempty"`
	Error         string `json:"error,omitempty"`
	IsRateLimited bool   `json:"is_rate_limited,omitempty"`
}

// AskOptions configures a request to the sidecar.
type AskOptions struct {
	SessionID    string
	Prompt       string
	WorkDir      string
	MaxTurns     int
	SystemPrompt string
	AllowedTools []string
	Model        string
}

// StreamEvent is a single event from the sidecar's streaming response.
type StreamEvent struct {
	Type          string `json:"type"`
	Tool          string `json:"tool,omitempty"`
	Input         string `json:"input,omitempty"`
	Text          string `json:"text,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	Usage         *Usage `json:"usage,omitempty"`
	Error         string `json:"error,omitempty"`
	IsRateLimited bool   `json:"is_rate_limited,omitempty"`
}

// RunAgentOptions configures an isolated CLI agent execution.
type RunAgentOptions struct {
	Prompt       string
	SystemPrompt string
	WorkDir      string
	AllowedTools []string
	TimeoutMs    int
	Model        string
}

// --- WebSocket protocol types ---

// wsRequest is the JSON message sent to the sidecar via WebSocket.
type wsRequest struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	SessionID    string   `json:"session_id,omitempty"`
	Prompt       string   `json:"prompt,omitempty"`
	WorkDir      string   `json:"cwd,omitempty"`
	MaxTurns     int      `json:"max_turns,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Model        string   `json:"model,omitempty"`
	TimeoutMs    int      `json:"timeout_ms,omitempty"`
}

// wsResponse is the JSON message received from the sidecar via WebSocket.
type wsResponse struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Tool          string `json:"tool,omitempty"`
	Input         string `json:"input,omitempty"`
	Text          string `json:"text,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	Usage         *Usage `json:"usage,omitempty"`
	Error         string `json:"error,omitempty"`
	IsRateLimited bool   `json:"is_rate_limited,omitempty"`
	// Session detection fields (from check-session response)
	Alive        bool   `json:"alive,omitempty"`
	SDKSessionID string `json:"sdk_session_id,omitempty"`
	LastActive   string `json:"last_active,omitempty"`
	IdleSeconds  *int   `json:"idle_seconds,omitempty"`
}

// SessionStatus is the result of a session liveness check.
type SessionStatus struct {
	Alive        bool
	SDKSessionID string
	LastActive   time.Time
	IdleSeconds  int
}

// pendingReq tracks an in-flight request awaiting responses from the sidecar.
type pendingReq struct {
	ch chan wsResponse
}

// Client talks to the Node.js sidecar over a persistent WebSocket connection.
type Client struct {
	wsURL     string
	healthURL string

	conn      *websocket.Conn
	connDone  chan struct{} // closed when current connection is replaced or client shuts down
	connMu    sync.Mutex   // protects conn and connDone
	writeMu   sync.Mutex   // serializes WebSocket writes

	pending   map[string]*pendingReq
	pendingMu sync.Mutex

	healthClient *http.Client
	healthy      atomic.Bool

	nextID      atomic.Int64
	done        chan struct{}
	closeOnce   sync.Once
	reconnectCh chan struct{}
	bgWg        sync.WaitGroup // tracks maintainConnection, readLoop, pingLoop
}

// NewClient creates a sidecar WebSocket client. It connects immediately and
// reconnects automatically in the background if the connection drops.
//
// Deprecated: The timeout parameter is unused — individual requests use
// context-based timeouts. It will be removed once all callers are updated.
func NewClient(baseURL string, _ time.Duration) *Client {
	// Derive WebSocket URL from HTTP URL
	wsURL := strings.Replace(baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/ws"

	c := &Client{
		wsURL:        wsURL,
		healthURL:    baseURL,
		pending:      make(map[string]*pendingReq),
		healthClient: &http.Client{Timeout: 10 * time.Second},
		done:         make(chan struct{}),
		reconnectCh:  make(chan struct{}, 1),
	}

	// Initial connection — non-blocking, reconnects in background on failure
	if err := c.dial(); err != nil {
		log.Printf("WebSocket: initial connection to %s failed: %v (will retry)", wsURL, err)
		c.triggerReconnect()
	} else {
		log.Printf("WebSocket: connected to %s", wsURL)
	}

	c.bgWg.Add(1)
	go func() {
		defer c.bgWg.Done()
		c.maintainConnection()
	}()

	return c
}

// Close shuts down the WebSocket client, waits for background goroutines
// (maintainConnection, readLoop, pingLoop) to finish, and closes the connection.
// Safe to call multiple times — subsequent calls are no-ops.
func (c *Client) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		close(c.done)
		c.connMu.Lock()
		if c.connDone != nil {
			close(c.connDone)
			c.connDone = nil
		}
		c.connMu.Unlock()
		c.bgWg.Wait()
		c.connMu.Lock()
		defer c.connMu.Unlock()
		if c.conn != nil {
			closeErr = c.conn.Close()
		}
	})
	return closeErr
}

// --- Public API (unchanged signatures) ---

// Ask sends a prompt to Claude via the sidecar and returns the response.
func (c *Client) Ask(ctx context.Context, sessionID, prompt, workDir string, maxTurns int) (*Response, error) {
	return c.AskWithOptions(ctx, AskOptions{
		SessionID: sessionID,
		Prompt:    prompt,
		WorkDir:   workDir,
		MaxTurns:  maxTurns,
	})
}

// AskWithOptions sends a prompt with full configuration options.
func (c *Client) AskWithOptions(ctx context.Context, opts AskOptions) (*Response, error) {
	id := c.genID()
	ch := c.allocReq(id)
	defer c.freeReq(id)

	req := wsRequest{
		ID:           id,
		Type:         "ask",
		SessionID:    opts.SessionID,
		Prompt:       opts.Prompt,
		WorkDir:      opts.WorkDir,
		MaxTurns:     opts.MaxTurns,
		SystemPrompt: opts.SystemPrompt,
		AllowedTools: opts.AllowedTools,
		Model:        opts.Model,
	}

	if err := c.writeJSONRetry(req); err != nil {
		return nil, fmt.Errorf("send ask request: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case resp := <-ch:
			switch resp.Type {
			case "result":
				r := &Response{
					Text:      resp.Text,
					SessionID: resp.SessionID,
					Usage:     resp.Usage,
				}
				if resp.Error != "" {
					return r, fmt.Errorf("claude error: %s", resp.Error)
				}
				return r, nil
			case "error":
				r := &Response{IsRateLimited: resp.IsRateLimited, Error: resp.Error}
				if resp.IsRateLimited {
					return r, fmt.Errorf("rate limited (429): %s", resp.Error)
				}
				return r, fmt.Errorf("claude error: %s", resp.Error)
			default:
				// progress, heartbeat, ack — ignore for non-streaming
				continue
			}
		}
	}
}

// AskStreaming sends a prompt and streams progress events via the callback.
// The onProgress callback is invoked for each progress event. The final
// result event is returned as a *Response.
func (c *Client) AskStreaming(ctx context.Context, opts AskOptions, onProgress func(StreamEvent)) (*Response, error) {
	id := c.genID()
	ch := c.allocReq(id)
	defer c.freeReq(id)

	req := wsRequest{
		ID:           id,
		Type:         "ask",
		SessionID:    opts.SessionID,
		Prompt:       opts.Prompt,
		WorkDir:      opts.WorkDir,
		MaxTurns:     opts.MaxTurns,
		SystemPrompt: opts.SystemPrompt,
		AllowedTools: opts.AllowedTools,
		Model:        opts.Model,
	}

	if err := c.writeJSONRetry(req); err != nil {
		return nil, fmt.Errorf("send ask request: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case resp := <-ch:
			switch resp.Type {
			case "progress":
				if onProgress != nil {
					onProgress(StreamEvent{
						Type:  "progress",
						Tool:  resp.Tool,
						Input: resp.Input,
						Text:  resp.Text,
					})
				}
			case "heartbeat":
				// Keepalive from sidecar — ignore
			case "result":
				return &Response{
					Text:      resp.Text,
					SessionID: resp.SessionID,
					Usage:     resp.Usage,
				}, nil
			case "error":
				r := &Response{IsRateLimited: resp.IsRateLimited, Error: resp.Error}
				if resp.IsRateLimited {
					return r, fmt.Errorf("rate limited (429): %s", resp.Error)
				}
				return nil, fmt.Errorf("claude stream error: %s", resp.Error)
			}
		}
	}
}

// RunIsolatedAgent spawns an isolated claude CLI process via the sidecar.
func (c *Client) RunIsolatedAgent(ctx context.Context, opts RunAgentOptions) (*Response, error) {
	id := c.genID()
	ch := c.allocReq(id)
	defer c.freeReq(id)

	req := wsRequest{
		ID:           id,
		Type:         "run-agent",
		Prompt:       opts.Prompt,
		WorkDir:      opts.WorkDir,
		SystemPrompt: opts.SystemPrompt,
		AllowedTools: opts.AllowedTools,
		Model:        opts.Model,
		TimeoutMs:    opts.TimeoutMs,
	}

	if err := c.writeJSONRetry(req); err != nil {
		return nil, fmt.Errorf("send run-agent request: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case resp := <-ch:
			switch resp.Type {
			case "result":
				r := &Response{
					Text:          resp.Text,
					IsRateLimited: resp.IsRateLimited,
				}
				if resp.Error != "" {
					return r, fmt.Errorf("agent error: %s", resp.Error)
				}
				return r, nil
			case "error":
				r := &Response{IsRateLimited: resp.IsRateLimited, Error: resp.Error}
				if resp.IsRateLimited {
					return r, fmt.Errorf("rate limited (429): %s", resp.Error)
				}
				return r, fmt.Errorf("agent error: %s", resp.Error)
			default:
				continue
			}
		}
	}
}

// ResetSession clears conversation history for a session.
func (c *Client) ResetSession(ctx context.Context, sessionID string) error {
	id := c.genID()
	ch := c.allocReq(id)
	defer c.freeReq(id)

	req := wsRequest{
		ID:        id,
		Type:      "reset",
		SessionID: sessionID,
	}

	if err := c.writeJSONRetry(req); err != nil {
		return fmt.Errorf("send reset request: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-ch:
		if resp.Type == "error" {
			return fmt.Errorf("reset error: %s", resp.Error)
		}
		return nil
	}
}

// CheckSession checks if an SDK session is alive in the sidecar.
func (c *Client) CheckSession(ctx context.Context, sessionID string) (*SessionStatus, error) {
	id := c.genID()
	ch := c.allocReq(id)
	defer c.freeReq(id)

	req := wsRequest{
		ID:        id,
		Type:      "check-session",
		SessionID: sessionID,
	}

	if err := c.writeJSONRetry(req); err != nil {
		return nil, fmt.Errorf("send check-session request: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Type == "error" {
			return nil, fmt.Errorf("check-session error: %s", resp.Error)
		}
		status := &SessionStatus{
			Alive:        resp.Alive,
			SDKSessionID: resp.SDKSessionID,
		}
		if resp.IdleSeconds != nil {
			status.IdleSeconds = *resp.IdleSeconds
		}
		if resp.LastActive != "" {
			if t, err := time.Parse(time.RFC3339, resp.LastActive); err == nil {
				status.LastActive = t
			}
		}
		return status, nil
	}
}

// Health checks if the sidecar HTTP server is responding.
// Uses HTTP (not WebSocket) since Docker healthcheck needs it.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.healthURL+"/health", nil)
	if err != nil {
		return err
	}

	resp, err := c.healthClient.Do(req)
	if err != nil {
		c.healthy.Store(false)
		return fmt.Errorf("sidecar unreachable: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		c.healthy.Store(false)
		return fmt.Errorf("sidecar unhealthy: status %d", resp.StatusCode)
	}

	c.healthy.Store(true)
	return nil
}

// IsHealthy returns the last known health status.
func (c *Client) IsHealthy() bool {
	return c.healthy.Load()
}

// --- Internal: request tracking ---

func (c *Client) genID() string {
	return strconv.FormatInt(c.nextID.Add(1), 10)
}

func (c *Client) allocReq(id string) chan wsResponse {
	ch := make(chan wsResponse, 256)
	c.pendingMu.Lock()
	c.pending[id] = &pendingReq{ch: ch}
	c.pendingMu.Unlock()
	return ch
}

func (c *Client) freeReq(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// --- Internal: WebSocket connection ---

func (c *Client) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected to sidecar")
	}
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if err := conn.WriteJSON(v); err != nil {
		c.triggerReconnect()
		return err
	}
	return nil
}

// writeJSONRetry attempts writeJSON, and on failure waits for reconnection
// before retrying once. Used for user-initiated requests where we want to
// survive a stale connection transparently.
func (c *Client) writeJSONRetry(v any) error {
	err := c.writeJSON(v)
	if err == nil {
		return nil
	}
	log.Printf("WebSocket: write failed (%v), waiting for reconnect...", err)
	// Wait up to 5 seconds for reconnection
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			return fmt.Errorf("write failed and reconnect timed out: %w", err)
		case <-ticker.C:
			if c.IsHealthy() {
				log.Printf("WebSocket: reconnected, retrying write")
				return c.writeJSON(v)
			}
		}
	}
}

// dial establishes a new WebSocket connection to the sidecar.
// Closes the old connection (if any) before assigning the new one, so old
// readLoop/pingLoop goroutines exit promptly via the per-connection done channel.
func (c *Client) dial() error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(c.wsURL, nil)
	if err != nil {
		c.healthy.Store(false)
		return err
	}

	// Set generous read deadline — refreshed on every pong
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})

	// Close old connection and signal old goroutines to exit
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	if c.connDone != nil {
		close(c.connDone)
	}
	connDone := make(chan struct{})
	c.conn = conn
	c.connDone = connDone
	c.connMu.Unlock()
	c.healthy.Store(true)

	c.bgWg.Add(2)
	go func() {
		defer c.bgWg.Done()
		c.readLoop(conn)
	}()
	go func() {
		defer c.bgWg.Done()
		c.pingLoop(conn, connDone)
	}()

	return nil
}

// readLoop reads messages from the WebSocket and routes them to pending requests.
// Each connection gets its own readLoop goroutine. When the connection drops,
// the goroutine exits, fails all pending requests, and triggers reconnection.
func (c *Client) readLoop(conn *websocket.Conn) {
	defer func() {
		c.healthy.Store(false)
		c.failAllPending(fmt.Errorf("websocket connection lost"))
		c.triggerReconnect()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("WebSocket: read error: %v", err)
			}
			return
		}

		var resp wsResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			log.Printf("WebSocket: invalid JSON from sidecar: %v", err)
			continue
		}

		c.pendingMu.Lock()
		req := c.pending[resp.ID]
		c.pendingMu.Unlock()

		if req != nil {
			switch resp.Type {
			case "result", "error":
				// Terminal events must never be dropped — if dropped, the caller
				// hangs until the 60-minute context timeout. Use blocking send.
				req.ch <- resp
			default:
				// Non-terminal events (progress, heartbeat) can be safely dropped.
				select {
				case req.ch <- resp:
				default:
					log.Printf("WebSocket: channel full for request %s, dropping %s event", resp.ID, resp.Type)
				}
			}
		}
	}
}

// pingLoop sends WebSocket pings every 30s to keep the connection alive.
// Exits when the connection is replaced (connDone closed), the connection
// errors, or the client shuts down.
func (c *Client) pingLoop(conn *websocket.Conn, connDone <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-connDone:
			return
		case <-ticker.C:
			c.writeMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := conn.WriteMessage(websocket.PingMessage, nil)
			c.writeMu.Unlock()
			if err != nil {
				log.Printf("WebSocket: ping failed: %v", err)
				c.triggerReconnect()
				return
			}
		}
	}
}

// triggerReconnect signals the maintainConnection goroutine to reconnect.
// Non-blocking: if a reconnect is already pending, this is a no-op.
func (c *Client) triggerReconnect() {
	select {
	case c.reconnectCh <- struct{}{}:
	default:
	}
}

// maintainConnection runs in the background, reconnecting when the
// WebSocket drops. Uses exponential backoff capped at 30 seconds.
func (c *Client) maintainConnection() {
	for {
		select {
		case <-c.done:
			return
		case <-c.reconnectCh:
		}

		backoff := time.Second
		for {
			select {
			case <-c.done:
				return
			default:
			}

			log.Printf("WebSocket: reconnecting to %s...", c.wsURL)
			if err := c.dial(); err != nil {
				log.Printf("WebSocket: reconnect failed: %v (retry in %v)", err, backoff)
				select {
				case <-c.done:
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			log.Printf("WebSocket: reconnected to %s", c.wsURL)
			break
		}
	}
}

// failAllPending sends an error to all in-flight requests and clears the map.
// Called when the WebSocket connection drops.
// Uses non-blocking send with a short timeout to prevent indefinite blocking
// if a consumer goroutine was interrupted without draining its channel.
func (c *Client) failAllPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	for id, req := range c.pending {
		select {
		case req.ch <- wsResponse{Type: "error", Error: err.Error()}:
		default:
			log.Printf("WebSocket: failed to deliver error to request %s (channel full)", id)
		}
		delete(c.pending, id)
	}
}
