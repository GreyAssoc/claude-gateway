package agent

import (
	"fmt"
	"testing"
)

// TestCloseIdempotent verifies that Close() can be called multiple times
// without panicking (double-close of c.done channel).
func TestCloseIdempotent(t *testing.T) {
	c := &Client{
		done:        make(chan struct{}),
		reconnectCh: make(chan struct{}, 1),
		pending:     make(map[string]*pendingReq),
	}

	// First close should succeed
	if err := c.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}

	// Second close should not panic
	if err := c.Close(); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}
}

// TestFailAllPendingDelivers verifies that failAllPending delivers error
// events to all pending request channels.
func TestFailAllPendingDelivers(t *testing.T) {
	c := &Client{
		pending: make(map[string]*pendingReq),
	}

	// Set up 3 pending requests
	ch1 := make(chan wsResponse, 256)
	ch2 := make(chan wsResponse, 256)
	ch3 := make(chan wsResponse, 256)
	c.pending["1"] = &pendingReq{ch: ch1}
	c.pending["2"] = &pendingReq{ch: ch2}
	c.pending["3"] = &pendingReq{ch: ch3}

	c.failAllPending(errTestSentinel)

	// All channels should have received an error
	for i, ch := range []chan wsResponse{ch1, ch2, ch3} {
		select {
		case resp := <-ch:
			if resp.Type != "error" {
				t.Errorf("channel %d: type = %q, want %q", i+1, resp.Type, "error")
			}
			if resp.Error != errTestSentinel.Error() {
				t.Errorf("channel %d: error = %q, want %q", i+1, resp.Error, errTestSentinel.Error())
			}
		default:
			t.Errorf("channel %d: no message delivered", i+1)
		}
	}

	// Pending map should be empty
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if len(c.pending) != 0 {
		t.Errorf("pending map should be empty, got %d entries", len(c.pending))
	}
}

// TestFailAllPendingFullChannel verifies that failAllPending does not block
// when a channel is full, and still clears the pending map.
func TestFailAllPendingFullChannel(t *testing.T) {
	c := &Client{
		pending: make(map[string]*pendingReq),
	}

	// Create a channel with buffer=1, fill it so the next send would block
	ch := make(chan wsResponse, 1)
	ch <- wsResponse{Type: "progress", Text: "filling buffer"}
	c.pending["full"] = &pendingReq{ch: ch}

	// Also add a normal (empty) channel
	chOK := make(chan wsResponse, 256)
	c.pending["ok"] = &pendingReq{ch: chOK}

	// Should not block — the full channel's error is dropped gracefully
	c.failAllPending(errTestSentinel)

	// Normal channel should have the error
	select {
	case resp := <-chOK:
		if resp.Type != "error" {
			t.Errorf("ok channel: type = %q, want %q", resp.Type, "error")
		}
	default:
		t.Error("ok channel: no message delivered")
	}

	// Full channel should still have only the original message (error was dropped)
	select {
	case resp := <-ch:
		if resp.Type != "progress" {
			t.Errorf("full channel: expected original 'progress' message, got %q", resp.Type)
		}
	default:
		t.Error("full channel: expected original message")
	}

	// Pending map should be empty
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if len(c.pending) != 0 {
		t.Errorf("pending map should be empty, got %d entries", len(c.pending))
	}
}

var errTestSentinel = fmt.Errorf("test connection lost")
