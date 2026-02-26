package ratelimiter

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/you/claude-gateway/internal/agent"
)

func TestCanProceed_UnderLimits(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)

	if !l.CanProceed(1000) {
		t.Error("Expected CanProceed=true when no usage recorded")
	}
}

func TestCanProceed_OverRPM(t *testing.T) {
	l := New(10, 40000, 8000, 1.0) // margin=1.0 for simpler math

	// Record 10 requests
	for i := 0; i < 10; i++ {
		l.Record(&agent.Usage{InputTokens: 100, OutputTokens: 50})
	}

	if l.CanProceed(100) {
		t.Error("Expected CanProceed=false after exceeding RPM")
	}
}

func TestCanProceed_OverITPM(t *testing.T) {
	l := New(100, 1000, 8000, 1.0)

	l.Record(&agent.Usage{InputTokens: 500, OutputTokens: 50})

	// 500 existing + 600 estimated = 1100 > 1000
	if l.CanProceed(600) {
		t.Error("Expected CanProceed=false when estimated ITPM exceeds limit")
	}

	// 500 existing + 400 estimated = 900 < 1000
	if !l.CanProceed(400) {
		t.Error("Expected CanProceed=true when estimated ITPM is under limit")
	}
}

func TestRecord_NilUsage(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)

	// Should not panic
	l.Record(nil)
}

func TestGetStatus(t *testing.T) {
	l := New(50, 40000, 8000, 0.85)
	l.Record(&agent.Usage{InputTokens: 1000, OutputTokens: 200})

	s := l.GetStatus()
	if s.RPM != "1/50" {
		t.Errorf("RPM = %q, want 1/50", s.RPM)
	}
	if s.ITPM != "1000/40000" {
		t.Errorf("ITPM = %q, want 1000/40000", s.ITPM)
	}
	if s.OTPM != "200/8000" {
		t.Errorf("OTPM = %q, want 200/8000", s.OTPM)
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

	// Record to fill RPM
	for i := 0; i < 10; i++ {
		l.Record(&agent.Usage{InputTokens: 100, OutputTokens: 50})
	}

	if l.CanProceed(100) {
		t.Error("Expected CanProceed=false when at RPM limit")
	}

	// Manually set window start to >1 min ago to force reset
	l.mu.Lock()
	l.windowStart = time.Now().Add(-2 * time.Minute)
	l.mu.Unlock()

	if !l.CanProceed(100) {
		t.Error("Expected CanProceed=true after window reset")
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
		l.Record(&agent.Usage{InputTokens: 100, OutputTokens: 50})
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
			l.Record(&agent.Usage{InputTokens: 10, OutputTokens: 5})
		}
	}()

	time.Sleep(5 * time.Millisecond)
	l.Close()
	<-done
}
