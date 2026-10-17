package ratelimiter

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/you/claude-gateway/internal/agent"
)

func TestAdmit_UnderLimits(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)
	d := l.Admit(Scope{Framework: "deepseek", SessionID: "a", Model: "deepseek-v4-pro"}, 1000)
	if !d.OK {
		t.Error("Expected admit when no usage recorded")
	}
	if d.Usage.ContextWindow != 128_000 {
		t.Errorf("window = %d, want 128000", d.Usage.ContextWindow)
	}
}

func TestAdmit_OverRPM(t *testing.T) {
	l := New(10, 40000, 8000, 1.0)
	scope := Scope{Framework: "claude", SessionID: "s", Model: "claude-opus-4-6"}
	for i := 0; i < 10; i++ {
		l.Record(scope, &agent.Usage{InputTokens: 100, OutputTokens: 50}, nil, true)
	}
	d := l.Admit(scope, 100)
	if d.OK || d.Reason != ReasonRPM {
		t.Fatalf("admit = %+v, want RPM block", d)
	}
}

func TestAdmit_ContextIsThisConversationOnly(t *testing.T) {
	l := New(100, 80_000, 16_000, 0.90)
	deepseek := Scope{Framework: "deepseek", SessionID: "tg-forex", Model: "deepseek-v4-pro"}
	grok := Scope{Framework: "grok", SessionID: "tg-research", Model: "grok-4.5"}
	otherDeepseek := Scope{Framework: "deepseek", SessionID: "tg-grey", Model: "deepseek-v4-pro"}

	// Sidecar summed every tool round into input_tokens (88555) and counted
	// thinking in output (49309). Context occupancy of this one prompt is 40k.
	// The sum must not become the counter, and it must not land on grok.
	l.Record(deepseek, &agent.Usage{InputTokens: 88555, OutputTokens: 49309}, &agent.ContextStatus{
		TokensUsed:    40_000,
		ContextWindow: 128_000,
	}, true)

	got := l.Admit(deepseek, 1000)
	if !got.OK {
		t.Fatalf("deepseek blocked: %+v", got)
	}
	if got.Usage.ContextTokens != 40_000 || got.Usage.ContextWindow != 128_000 {
		t.Fatalf("deepseek usage = %+v, want 40000/128000", got.Usage)
	}
	if got.Usage.OutputTokens != 49309 {
		t.Fatalf("output = %d, want this turn's 49309", got.Usage.OutputTokens)
	}

	other := l.Admit(grok, 1000)
	if !other.OK || other.Usage.ContextTokens != 0 || other.Usage.ContextWindow != 2_000_000 {
		t.Fatalf("grok should be an empty 2M window, got %+v", other)
	}

	l.Record(otherDeepseek, &agent.Usage{InputTokens: 90_000, OutputTokens: 10_000}, &agent.ContextStatus{
		TokensUsed:    90_000,
		ContextWindow: 128_000,
	}, true)
	// 90k stays on that session. The forex session is still 40k, not 130k.
	forex := l.Admit(deepseek, 1000)
	if forex.Usage.ContextTokens != 40_000 {
		t.Fatalf("forex context = %d, want 40000 (not pooled with the other deepseek agent)", forex.Usage.ContextTokens)
	}
	grey := l.Admit(otherDeepseek, 1000)
	if !grey.OK || grey.Usage.ContextTokens != 90_000 {
		t.Fatalf("grey = %+v", grey)
	}
}

func TestAdmit_ReplacesInsteadOfSummingRounds(t *testing.T) {
	l := New(100, 80_000, 16_000, 1)
	scope := Scope{Framework: "deepseek", SessionID: "s", Model: "deepseek-v4-pro"}
	l.Record(scope, nil, &agent.ContextStatus{TokensUsed: 50_000, ContextWindow: 128_000}, true)
	l.Record(scope, nil, &agent.ContextStatus{TokensUsed: 60_000, ContextWindow: 128_000}, true)
	d := l.Admit(scope, 0)
	if d.Usage.ContextTokens != 60_000 {
		t.Fatalf("context = %d, want 60000 replaced, not 110000 summed", d.Usage.ContextTokens)
	}
}

func TestAdmit_BlocksOnlyTheFullConversation(t *testing.T) {
	l := New(100, 80_000, 16_000, 0.90)
	full := Scope{Framework: "deepseek", SessionID: "full", Model: "deepseek-v4-pro"}
	open := Scope{Framework: "deepseek", SessionID: "open", Model: "deepseek-v4-pro"}
	l.Record(full, nil, &agent.ContextStatus{TokensUsed: 120_000, ContextWindow: 128_000}, true)
	l.Record(open, nil, &agent.ContextStatus{TokensUsed: 10_000, ContextWindow: 128_000}, true)

	blocked := l.Admit(full, 1000)
	if blocked.OK || blocked.Reason != ReasonContext {
		t.Fatalf("full conversation = %+v, want context block", blocked)
	}
	allowed := l.Admit(open, 1000)
	if !allowed.OK || allowed.Usage.ContextTokens != 10_000 {
		t.Fatalf("other deepseek conversation should proceed, got %+v", allowed)
	}
}

func TestAdmit_QwenDoesNotConsumeRPM(t *testing.T) {
	l := New(1, 80_000, 16_000, 1)
	qwen := Scope{Framework: "qwen", SessionID: "local", Model: "qwen3.8-27b"}
	l.Record(qwen, &agent.Usage{InputTokens: 150_000, OutputTokens: 20_000}, &agent.ContextStatus{
		TokensUsed:    150_000,
		ContextWindow: 131_072,
	}, false)
	if d := l.Admit(qwen, 1000); !d.OK {
		t.Fatalf("qwen should be unlimited, got %+v", d)
	}
	paid := Scope{Framework: "deepseek", SessionID: "paid", Model: "deepseek-v4-pro"}
	if d := l.Admit(paid, 1000); !d.OK {
		t.Fatalf("qwen tokens blocked deepseek: %+v", d)
	}
	if s := l.GetStatus(); s.RPM != "0/1" {
		t.Fatalf("RPM = %s, want 0/1", s.RPM)
	}
}

func TestRecord_NilUsage(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)
	l.Record(Scope{}, nil, nil, true)
}

func TestGetStatus(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)
	l.Record(Scope{Framework: "grok", SessionID: "s", Model: "grok-4.5"}, &agent.Usage{InputTokens: 1000, OutputTokens: 200}, nil, true)

	s := l.GetStatus()
	if s.RPM != "1/50" {
		t.Errorf("RPM = %q, want 1/50", s.RPM)
	}
	if s.ITPM != "per-conversation" || s.OTPM != "per-conversation" {
		t.Errorf("token fields = %q %q, want per-conversation", s.ITPM, s.OTPM)
	}
	if len(s.Conversations) != 1 {
		t.Fatalf("conversations = %d, want 1", len(s.Conversations))
	}
	c := s.Conversations[0]
	if c.ContextTokens != 1000 || c.OutputTokens != 200 || c.ContextWindow != 2_000_000 {
		t.Errorf("conversation = %+v", c)
	}
	if s.Headroom <= 0 || s.Headroom > 1 {
		t.Errorf("Headroom = %f, want between 0 and 1", s.Headroom)
	}
}

func TestEnqueue_RespectsMaxQueue(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)

	// Fill queue to max (100)
	for i := 0; i < 100; i++ {
		pos := l.Enqueue(func() {})
		if pos < 0 {
			t.Fatalf("Enqueue failed at position %d", i)
		}
	}

	// 101st should fail
	pos := l.Enqueue(func() {})
	if pos != -1 {
		t.Errorf("Expected -1 for full queue, got %d", pos)
	}
}

func TestWindowReset(t *testing.T) {
	l := New(10, 40000, 8000, 1.0)

	scope := Scope{Framework: "claude", SessionID: "s", Model: "claude-opus-4-6"}
	for i := 0; i < 10; i++ {
		l.Record(scope, &agent.Usage{InputTokens: 100, OutputTokens: 50}, nil, true)
	}

	if l.Admit(scope, 100).OK {
		t.Error("Expected admit=false when at RPM limit")
	}

	// RPM window expires. The conversation's context figure must stay.
	l.mu.Lock()
	l.windowStart = time.Now().Add(-2 * time.Minute)
	l.mu.Unlock()

	d := l.Admit(scope, 100)
	if !d.OK {
		t.Error("Expected admit=true after RPM window reset")
	}
	if d.Usage.ContextTokens != 100 {
		t.Errorf("context = %d, want 100 kept across the RPM reset", d.Usage.ContextTokens)
	}
}

func TestDropQueuedFor(t *testing.T) {
	l := New(1, 40000, 8000, 1.0)
	l.Record(Scope{Framework: "claude", SessionID: "s"}, &agent.Usage{InputTokens: 100, OutputTokens: 50}, nil, true)
	if l.Admit(Scope{Framework: "claude", SessionID: "s"}, 100).OK {
		t.Fatal("need a full window so enqueue is used")
	}
	fired := 0
	posA := l.EnqueueChat(11, func() { fired++ })
	posB := l.EnqueueChat(22, func() { fired++ })
	if posA != 1 || posB != 2 {
		t.Fatalf("positions %d %d", posA, posB)
	}
	if n := l.DropQueuedFor(11); n != 1 {
		t.Fatalf("dropped %d, want 1", n)
	}
	if n := l.DropQueuedFor(11); n != 0 {
		t.Fatalf("second drop %d", n)
	}
	st := l.GetStatus()
	if st.QueueDepth != 1 {
		t.Fatalf("queue depth %d, want 1 (chat 22)", st.QueueDepth)
	}
}

func TestClose_DrainsQueue(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)

	l.Enqueue(func() {})
	l.Enqueue(func() {})

	l.Close()

	l.mu.Lock()
	qLen := len(l.queue)
	l.mu.Unlock()

	if qLen != 0 {
		t.Errorf("Queue length after Close = %d, want 0", qLen)
	}
}

func TestSetDB_AfterClose_NoOp(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)

	l.Close()

	// SetDB after Close should not create an orphaned goroutine or panic.
	l.SetDB(nil)

	l.mu.Lock()
	ch := l.dbWrites
	l.mu.Unlock()

	if ch != nil {
		t.Error("Expected dbWrites to remain nil after SetDB on closed limiter")
	}
}

// mockDBRecorder implements DBRecorder for testing.
type mockDBRecorder struct {
	calls atomic.Int32
}

func (m *mockDBRecorder) RecordRateLimitSnapshot(_ context.Context, _, _, _ int, _ float64) error {
	m.calls.Add(1)
	time.Sleep(10 * time.Millisecond) // simulate work
	return nil
}

func TestClose_WaitsForDBWriteLoop(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)
	mock := &mockDBRecorder{}
	l.SetDB(mock)

	// Record several snapshots to fill the channel
	for i := 0; i < 5; i++ {
		l.Record(Scope{Framework: "claude", SessionID: "s"}, &agent.Usage{InputTokens: 100, OutputTokens: 50}, nil, true)
	}

	// Close must wait for the dbWriteLoop goroutine to drain
	l.Close()

	// After Close returns, dbWriteLoop must have processed all pending snapshots
	if mock.calls.Load() == 0 {
		t.Error("Expected dbWriteLoop to have processed snapshots before Close returned")
	}
}

func TestClose_ConcurrentAccess(t *testing.T) {
	// Verify no data race when Close and Record are called concurrently.
	l := New(50, 40000, 8000, 0.85)
	mock := &mockDBRecorder{}
	l.SetDB(mock)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			l.Record(Scope{Framework: "claude", SessionID: "s"}, &agent.Usage{InputTokens: 10, OutputTokens: 5}, nil, true)
		}
	}()

	time.Sleep(5 * time.Millisecond)
	l.Close()
	<-done
}
