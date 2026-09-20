package agent

import (
	"context"
	"testing"
)

type stubBackend struct{ name string }

func (s *stubBackend) Ask(context.Context, string, string, string, int) (*Response, error) {
	return nil, nil
}
func (s *stubBackend) AskWithOptions(context.Context, AskOptions) (*Response, error) {
	return nil, nil
}
func (s *stubBackend) AskStreaming(context.Context, AskOptions, func(StreamEvent)) (*Response, error) {
	return nil, nil
}
func (s *stubBackend) RunIsolatedAgent(context.Context, RunAgentOptions) (*Response, error) {
	return nil, nil
}
func (s *stubBackend) AbortSession(string)                        {}
func (s *stubBackend) ResetSession(context.Context, string) error { return nil }
func (s *stubBackend) CheckSession(context.Context, string) (*SessionStatus, error) {
	return nil, nil
}
func (s *stubBackend) CompactSession(context.Context, string) (*Compaction, error) {
	return nil, nil
}
func (s *stubBackend) Health(context.Context) error { return nil }
func (s *stubBackend) IsHealthy() bool              { return true }
func (s *stubBackend) Close() error                 { return nil }
func (s *stubBackend) Name() string                 { return s.name }

func TestDurablePrefersQwen(t *testing.T) {
	claude := &stubBackend{name: "claude"}
	grok := &stubBackend{name: "grok"}
	qwen := &stubBackend{name: "qwen"}

	sw, err := NewSwitcher("claude", claude, grok, qwen)
	if err != nil {
		t.Fatalf("NewSwitcher: %v", err)
	}

	if got := sw.Durable("qwen").Name(); got != "qwen" {
		t.Errorf("Durable(qwen) = %q, want qwen", got)
	}
	if got := sw.Durable("").Name(); got != "qwen" {
		t.Errorf("Durable empty prefer = %q, want qwen (not grok/active)", got)
	}
	if got := sw.Durable("missing").Name(); got != "qwen" {
		t.Errorf("Durable unknown prefer = %q, want qwen fallback", got)
	}
	if got := sw.Durable("grok").Name(); got != "grok" {
		t.Errorf("Durable(grok) = %q, want grok override", got)
	}
}
