// Package agent provides AI backend implementations for the gateway.
//
// The Backend interface abstracts the underlying AI framework (Claude Code, OpenCode, etc.)
// so the rest of the gateway (bot, cron, watcher, orchestrator) is framework-agnostic.
package agent

import "context"

// Backend is the interface that all AI framework implementations must satisfy.
// The gateway interacts exclusively through this interface — it never knows
// which framework is handling the request.
type Backend interface {
	// Ask sends a prompt and returns the final response (blocking, non-streaming).
	Ask(ctx context.Context, sessionID, prompt, workDir string, maxTurns int) (*Response, error)

	// AskWithOptions sends a prompt with full configuration options.
	AskWithOptions(ctx context.Context, opts AskOptions) (*Response, error)

	// AskStreaming sends a prompt and streams progress events via the callback.
	// The final result is returned as a *Response.
	AskStreaming(ctx context.Context, opts AskOptions, onProgress func(StreamEvent)) (*Response, error)

	// RunIsolatedAgent spawns an isolated agent process for single-turn tasks.
	RunIsolatedAgent(ctx context.Context, opts RunAgentOptions) (*Response, error)

	// AbortSession sends a fire-and-forget abort signal for the given session.
	AbortSession(sessionID string)

	// ResetSession clears conversation history for a session.
	ResetSession(ctx context.Context, sessionID string) error

	// CheckSession checks if a session is alive.
	CheckSession(ctx context.Context, sessionID string) (*SessionStatus, error)

	// CompactSession asks the backend to summarise the active session and
	// start a fresh one seeded with that summary. Manual trigger; the
	// auto trigger lives inside Ask/AskStreaming (sidecar threshold).
	// Returns the Compaction metadata, or nil if nothing was compacted
	// (e.g. session not present). Backends that don't implement
	// compaction may return (nil, nil) to indicate a silent no-op.
	CompactSession(ctx context.Context, sessionID string) (*Compaction, error)

	// Health checks if the backend is responding.
	Health(ctx context.Context) error

	// IsHealthy returns the last known health status (non-blocking).
	IsHealthy() bool

	// Close shuts down the backend and releases resources.
	Close() error

	// Name returns the backend identifier (e.g. "claude", "opencode").
	Name() string
}
