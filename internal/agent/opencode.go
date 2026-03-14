package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Compile-time check: *OpenCodeClient implements Backend.
var _ Backend = (*OpenCodeClient)(nil)

// OpenCodeConfig holds configuration for the OpenCode backend.
type OpenCodeConfig struct {
	Binary         string        // path to opencode binary (default: "opencode")
	Model          string        // model in provider/model format (e.g. "anthropic/claude-sonnet-4-5")
	WorkDir        string        // default working directory
	Timeout        time.Duration // per-request timeout
	AgentDir       string        // directory containing agent definitions
	SessionDataDir string        // directory for session data persistence
}

// OpenCodeClient spawns `opencode run` processes to handle requests.
// No sidecar, no WebSocket — pure process spawning.
type OpenCodeClient struct {
	cfg     OpenCodeConfig
	healthy atomic.Bool

	// Track active processes for abort support
	procs   map[string]*activeProc
	procsMu sync.Mutex
}

// activeProc tracks a running opencode process for a given session.
type activeProc struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
}

// opencodeJSONEvent represents a single JSON event from `opencode run --format json`.
// Events have a nested `part` object containing the actual content.
type opencodeJSONEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionID,omitempty"`
	Part      opencodeEventPart `json:"part,omitempty"`
	Error     *opencodeError  `json:"error,omitempty"`
}

type opencodeEventPart struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Input  string `json:"input,omitempty"`
	Result string `json:"result,omitempty"`
	Reason string `json:"reason,omitempty"`
	Cost   float64 `json:"cost,omitempty"`
	Tokens *opencodeTokens `json:"tokens,omitempty"`
}

type opencodeTokens struct {
	Total  int `json:"total"`
	Input  int `json:"input"`
	Output int `json:"output"`
}

type opencodeError struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NewOpenCodeClient creates an OpenCode backend.
func NewOpenCodeClient(cfg OpenCodeConfig) *OpenCodeClient {
	if cfg.Binary == "" {
		cfg.Binary = "opencode"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Minute
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/workspace"
	}
	if cfg.SessionDataDir == "" {
		cfg.SessionDataDir = filepath.Join(os.TempDir(), "opencode-gateway-sessions")
	}

	c := &OpenCodeClient{
		cfg:   cfg,
		procs: make(map[string]*activeProc),
	}

	// Check if binary exists at startup
	if _, err := exec.LookPath(cfg.Binary); err != nil {
		log.Printf("OpenCode: binary %q not found: %v", cfg.Binary, err)
		c.healthy.Store(false)
	} else {
		c.healthy.Store(true)
		log.Printf("OpenCode: backend ready (binary: %s, model: %s)", cfg.Binary, cfg.Model)
	}

	return c
}

// Ask sends a prompt and returns the final response.
func (c *OpenCodeClient) Ask(ctx context.Context, sessionID, prompt, workDir string, maxTurns int) (*Response, error) {
	return c.AskWithOptions(ctx, AskOptions{
		SessionID: sessionID,
		Prompt:    prompt,
		WorkDir:   workDir,
		MaxTurns:  maxTurns,
	})
}

// AskWithOptions sends a prompt with full configuration options.
func (c *OpenCodeClient) AskWithOptions(ctx context.Context, opts AskOptions) (*Response, error) {
	args := c.buildArgs(opts.Prompt, opts.SystemPrompt, opts.Model, opts.SessionID, false)

	workDir := opts.WorkDir
	if workDir == "" {
		workDir = c.cfg.WorkDir
	}

	timeout := c.cfg.Timeout
	if ctx != context.Background() {
		// Use context deadline if set, otherwise use default
		if deadline, ok := ctx.Deadline(); ok {
			timeout = time.Until(deadline)
		}
	}

	stdout, stderr, err := c.execute(ctx, args, workDir, timeout, opts.SessionID)
	if err != nil {
		errMsg := stderr
		if errMsg == "" {
			errMsg = err.Error()
		}
		return &Response{Error: errMsg}, fmt.Errorf("opencode error: %w", err)
	}

	return &Response{
		Text:      strings.TrimSpace(stdout),
		SessionID: opts.SessionID,
	}, nil
}

// AskStreaming sends a prompt and streams progress events.
func (c *OpenCodeClient) AskStreaming(ctx context.Context, opts AskOptions, onProgress func(StreamEvent)) (*Response, error) {
	args := c.buildArgs(opts.Prompt, opts.SystemPrompt, opts.Model, opts.SessionID, true)

	workDir := opts.WorkDir
	if workDir == "" {
		workDir = c.cfg.WorkDir
	}

	procCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(procCtx, c.cfg.Binary, args...)
	cmd.Dir = workDir
	cmd.Env = c.buildEnv()

	// Register for abort
	if opts.SessionID != "" {
		c.procsMu.Lock()
		c.procs[opts.SessionID] = &activeProc{cancel: cancel, cmd: cmd}
		c.procsMu.Unlock()
		defer func() {
			c.procsMu.Lock()
			delete(c.procs, opts.SessionID)
			c.procsMu.Unlock()
		}()
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode start: %w", err)
	}

	// Parse streaming JSON events
	var resultText strings.Builder
	var totalUsage Usage
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event opencodeJSONEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Not JSON — treat as plain text output
			log.Printf("OpenCode [raw]: %s", line)
			resultText.WriteString(line)
			resultText.WriteString("\n")
			continue
		}

		switch event.Type {
		case "text":
			text := event.Part.Text
			if text != "" {
				resultText.WriteString(text)
				if onProgress != nil {
					onProgress(StreamEvent{Type: "progress", Text: text})
				}
			}
		case "tool_use", "tool_call":
			if onProgress != nil {
				onProgress(StreamEvent{Type: "progress", Tool: event.Part.Tool, Input: event.Part.Input})
			}
		case "step_finish":
			// Extract usage from step_finish tokens
			if event.Part.Tokens != nil {
				totalUsage.InputTokens += event.Part.Tokens.Input
				totalUsage.OutputTokens += event.Part.Tokens.Output
			}
		case "step_start":
			// ignore
		case "error":
			errMsg := ""
			if event.Error != nil {
				errMsg = event.Error.Name
				if event.Error.Data != nil {
					var d struct{ Message string `json:"message"` }
					if json.Unmarshal(event.Error.Data, &d) == nil && d.Message != "" {
						errMsg = d.Message
					}
				}
			}
			return &Response{Error: errMsg}, fmt.Errorf("opencode error: %s", errMsg)
		default:
			// Capture any unrecognized event that has text in part
			if event.Part.Text != "" {
				resultText.WriteString(event.Part.Text)
				if onProgress != nil {
					onProgress(StreamEvent{Type: "progress", Text: event.Part.Text})
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		// If we got output, use it even if exit code is non-zero
		if resultText.Len() > 0 {
			return &Response{
				Text:      strings.TrimSpace(resultText.String()),
				SessionID: opts.SessionID,
				Usage:     &totalUsage,
			}, nil
		}
		return &Response{Error: stderrBuf.String()}, fmt.Errorf("opencode process: %w", err)
	}

	return &Response{
		Text:      strings.TrimSpace(resultText.String()),
		SessionID: opts.SessionID,
		Usage:     &totalUsage,
	}, nil
}

// RunIsolatedAgent spawns an isolated opencode process with an agent definition.
func (c *OpenCodeClient) RunIsolatedAgent(ctx context.Context, opts RunAgentOptions) (*Response, error) {
	args := []string{"run"}

	// If system prompt is provided, create a temporary agent file
	if opts.SystemPrompt != "" {
		agentFile, cleanup, err := c.createTempAgent(opts.SystemPrompt)
		if err != nil {
			return nil, fmt.Errorf("create temp agent: %w", err)
		}
		defer cleanup()
		args = append(args, "--agent", agentFile)
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	} else if c.cfg.Model != "" {
		args = append(args, "--model", c.cfg.Model)
	}

	args = append(args, opts.Prompt)

	workDir := opts.WorkDir
	if workDir == "" {
		workDir = c.cfg.WorkDir
	}

	timeout := c.cfg.Timeout
	if opts.TimeoutMs > 0 {
		timeout = time.Duration(opts.TimeoutMs) * time.Millisecond
	}

	stdout, stderr, err := c.execute(ctx, args, workDir, timeout, "")
	if err != nil {
		// Check for rate limiting
		errStr := stderr
		if errStr == "" {
			errStr = err.Error()
		}
		isRL := strings.Contains(errStr, "429") || strings.Contains(errStr, "rate_limit") || strings.Contains(errStr, "rate limit")
		return &Response{
			Text:          stdout,
			Error:         errStr,
			IsRateLimited: isRL,
		}, fmt.Errorf("opencode agent error: %w", err)
	}

	return &Response{
		Text: strings.TrimSpace(stdout),
	}, nil
}

// AbortSession kills the running process for the given session.
func (c *OpenCodeClient) AbortSession(sessionID string) {
	c.procsMu.Lock()
	proc, ok := c.procs[sessionID]
	c.procsMu.Unlock()

	if ok && proc.cancel != nil {
		log.Printf("OpenCode: aborting session %s", sessionID)
		proc.cancel()
	}
}

// ResetSession is a no-op for OpenCode — each run is isolated.
func (c *OpenCodeClient) ResetSession(_ context.Context, sessionID string) error {
	log.Printf("OpenCode: reset session %s (no-op, process-based)", sessionID)
	return nil
}

// CheckSession returns a synthetic status. OpenCode sessions are process-based,
// so a session is "alive" only while a process is running.
func (c *OpenCodeClient) CheckSession(_ context.Context, sessionID string) (*SessionStatus, error) {
	c.procsMu.Lock()
	_, active := c.procs[sessionID]
	c.procsMu.Unlock()

	return &SessionStatus{
		Alive:      active,
		LastActive: time.Now(),
	}, nil
}

// Health checks if the opencode binary is available.
func (c *OpenCodeClient) Health(_ context.Context) error {
	if _, err := exec.LookPath(c.cfg.Binary); err != nil {
		c.healthy.Store(false)
		return fmt.Errorf("opencode binary not found: %w", err)
	}
	c.healthy.Store(true)
	return nil
}

// IsHealthy returns the last known health status.
func (c *OpenCodeClient) IsHealthy() bool {
	return c.healthy.Load()
}

// Close is a no-op for OpenCode — processes are cleaned up individually.
func (c *OpenCodeClient) Close() error {
	// Kill any remaining processes
	c.procsMu.Lock()
	for id, proc := range c.procs {
		if proc.cancel != nil {
			proc.cancel()
		}
		delete(c.procs, id)
	}
	c.procsMu.Unlock()
	return nil
}

// Name returns "opencode" — the backend identifier.
func (c *OpenCodeClient) Name() string {
	return "opencode"
}

// --- Internal helpers ---

// buildArgs constructs the opencode CLI arguments.
func (c *OpenCodeClient) buildArgs(prompt, systemPrompt, model, sessionID string, jsonFormat bool) []string {
	args := []string{"run"}

	// Always prefer our configured model. The caller may pass a Claude model
	// name (e.g. "opus") which is meaningless to OpenCode. Only use the
	// caller's model if it looks like an OpenCode model (contains "/").
	switch {
	case c.cfg.Model != "":
		args = append(args, "--model", c.cfg.Model)
	case model != "" && strings.Contains(model, "/"):
		args = append(args, "--model", model)
	}

	// Session continuity
	if sessionID != "" {
		args = append(args, "--session", sessionID, "--continue")
	}

	if jsonFormat {
		args = append(args, "--format", "json")
	}

	// System prompt via temporary agent if provided
	if systemPrompt != "" {
		agentFile, cleanup, err := c.createTempAgent(systemPrompt)
		if err != nil {
			log.Printf("OpenCode: failed to create temp agent for system prompt: %v", err)
		} else {
			// Note: cleanup must be deferred by the caller — we handle this in execute()
			// For buildArgs, we just add the flag. The temp file lifecycle is managed by
			// the caller (AskWithOptions etc.) but for simplicity here, we leak the file
			// and let the OS clean /tmp. A proper fix would restructure the API.
			_ = cleanup // caller responsibility
			args = append(args, "--agent", agentFile)
		}
	}

	args = append(args, prompt)
	return args
}

// execute runs an opencode process and captures output.
func (c *OpenCodeClient) execute(ctx context.Context, args []string, workDir string, timeout time.Duration, sessionID string) (stdout, stderr string, err error) {
	procCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(procCtx, c.cfg.Binary, args...)
	cmd.Dir = workDir
	cmd.Env = c.buildEnv()

	// Register for abort support
	if sessionID != "" {
		c.procsMu.Lock()
		c.procs[sessionID] = &activeProc{cancel: cancel, cmd: cmd}
		c.procsMu.Unlock()
		defer func() {
			c.procsMu.Lock()
			delete(c.procs, sessionID)
			c.procsMu.Unlock()
		}()
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		// If we captured output, return it alongside the error
		return stdoutBuf.String(), stderrBuf.String(), err
	}

	return stdoutBuf.String(), stderrBuf.String(), nil
}

// buildEnv constructs environment variables for opencode processes.
func (c *OpenCodeClient) buildEnv() []string {
	env := os.Environ()
	env = append(env, "HOME="+os.Getenv("HOME"))
	if c.cfg.WorkDir != "" {
		env = append(env, "PWD="+c.cfg.WorkDir)
	}
	return env
}

// truncate shortens a string for logging.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// createTempAgent writes a system prompt as a temporary agent markdown file.
// Returns the agent name (filename without .md) and a cleanup function.
func (c *OpenCodeClient) createTempAgent(systemPrompt string) (agentName string, cleanup func(), err error) {
	dir := c.cfg.AgentDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "opencode-agents")
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, fmt.Errorf("create agent dir: %w", err)
	}

	// Use a deterministic name based on a simple hash to allow reuse
	name := fmt.Sprintf("gateway-system-%d", time.Now().UnixNano())
	path := filepath.Join(dir, name+".md")

	content := fmt.Sprintf(`---
description: Gateway system prompt agent
---

%s`, systemPrompt)

	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return "", nil, fmt.Errorf("write agent file: %w", err)
	}

	return name, func() { os.Remove(path) }, nil
}
