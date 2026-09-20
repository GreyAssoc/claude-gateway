// Package ratelimiter tracks API usage against per-minute rate limits with queuing support.
package ratelimiter

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/you/claude-gateway/internal/agent"
)

// DBRecorder is an optional interface for persisting rate limit snapshots.
type DBRecorder interface {
	RecordRateLimitSnapshot(ctx context.Context, requests, inputTokens, outputTokens int, headroom float64) error
}

// Status represents the current rate limit state.
type Status struct {
	RPM          string  `json:"rpm"`
	ITPM         string  `json:"itpm"`
	OTPM         string  `json:"otpm"`
	Headroom     float64 `json:"headroom"`
	QueueDepth   int     `json:"queue_depth"`
	WindowResets string  `json:"window_resets"`
}

// Limiter tracks API usage against rate limits using a rolling window.
type Limiter struct {
	// mu protects all mutable fields below.
	mu sync.Mutex

	maxRPM  int
	maxITPM int
	maxOTPM int
	margin  float64

	requests     int
	inputTokens  int
	outputTokens int
	windowStart  time.Time

	// Queued requests when over limit
	queue   []queuedRequest
	queueWg sync.WaitGroup // tracks goroutines spawned from queued callbacks

	// Optional DB recorder for snapshots
	db        DBRecorder
	dbWrites  chan dbSnapshot
	dbLoopWg  sync.WaitGroup // tracks the dbWriteLoop goroutine
	closed    bool
}

type dbSnapshot struct {
	requests     int
	inputTokens  int
	outputTokens int
	headroom     float64
}

type queuedRequest struct {
	callback func()
	addedAt  time.Time
	chatID   int64
}

// New creates a rate limiter with the given per-minute limits and safety margin.
func New(rpm, itpm, otpm int, safetyMargin float64) *Limiter {
	return &Limiter{
		maxRPM:      rpm,
		maxITPM:     itpm,
		maxOTPM:     otpm,
		margin:      safetyMargin,
		windowStart: time.Now(),
	}
}

// SetDB attaches a database recorder and starts a background writer.
// Subsequent calls close the previous writer first.
// No-op if the limiter has already been closed.
func (l *Limiter) SetDB(db DBRecorder) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	if l.dbWrites != nil {
		close(l.dbWrites) // close previous writer goroutine
	}
	l.db = db
	ch := make(chan dbSnapshot, 64)
	l.dbWrites = ch
	// Capture db locally so the goroutine never races with a future SetDB call.
	localDB := l.db
	l.dbLoopWg.Add(1)
	l.mu.Unlock()
	go func() {
		defer l.dbLoopWg.Done()
		l.dbWriteLoop(ch, localDB)
	}()
}

// Close shuts down the background DB writer goroutine, drains the pending
// queue, and waits for any in-flight queued callback goroutines to finish.
func (l *Limiter) Close() {
	l.mu.Lock()
	ch := l.dbWrites
	l.dbWrites = nil
	l.queue = nil // drain queue to prevent post-shutdown callbacks
	l.closed = true
	l.mu.Unlock()

	// Wait for queued callbacks with a timeout to avoid blocking shutdown
	// indefinitely if the sidecar is unreachable (HTTP client timeout is 300s+).
	done := make(chan struct{})
	go func() {
		l.queueWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		log.Println("Rate limiter: timed out waiting for queued callbacks during shutdown")
	}

	if ch != nil {
		close(ch)
		l.dbLoopWg.Wait() // Wait for dbWriteLoop to drain and exit
	}
}

func (l *Limiter) dbWriteLoop(ch chan dbSnapshot, db DBRecorder) {
	var consecutiveFailures int
	for snap := range ch {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := db.RecordRateLimitSnapshot(ctx, snap.requests, snap.inputTokens, snap.outputTokens, snap.headroom); err != nil {
			consecutiveFailures++
			if consecutiveFailures >= 5 {
				log.Printf("ERROR: rate limit snapshot write failing consecutively (%d): %v", consecutiveFailures, err)
			} else {
				log.Printf("Failed to record rate limit snapshot: %v", err)
			}
		} else {
			consecutiveFailures = 0
		}
		cancel()
	}
}

// CanProceed checks if a request with the estimated input tokens can be made.
func (l *Limiter) CanProceed(estimatedInputTokens int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.resetIfNewWindow()

	effectiveRPM := float64(l.maxRPM) * l.margin
	effectiveITPM := float64(l.maxITPM) * l.margin
	effectiveOTPM := float64(l.maxOTPM) * l.margin

	return float64(l.requests) < effectiveRPM &&
		float64(l.inputTokens+estimatedInputTokens) < effectiveITPM &&
		float64(l.outputTokens) < effectiveOTPM
}

// Record logs actual usage after a request completes.
func (l *Limiter) Record(usage *agent.Usage) {
	if usage == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.resetIfNewWindow()
	l.requests++

	// Only uncached input tokens count towards ITPM
	l.inputTokens += usage.InputTokens
	l.outputTokens += usage.OutputTokens

	// Send snapshot to buffered writer (non-blocking)
	if l.dbWrites != nil {
		select {
		case l.dbWrites <- dbSnapshot{
			requests:     l.requests,
			inputTokens:  l.inputTokens,
			outputTokens: l.outputTokens,
			headroom:     l.calcHeadroom(),
		}:
		default:
			log.Println("Rate limiter: snapshot dropped (channel full)")
		}
	}
}

// GetStatus returns current rate limit utilization.
func (l *Limiter) GetStatus() Status {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.resetIfNewWindow()

	elapsed := time.Since(l.windowStart)
	remaining := time.Minute - elapsed
	if remaining < 0 {
		remaining = 0
	}

	headroom := l.calcHeadroom()

	return Status{
		RPM:          fmt.Sprintf("%d/%d", l.requests, l.maxRPM),
		ITPM:         fmt.Sprintf("%d/%d", l.inputTokens, l.maxITPM),
		OTPM:         fmt.Sprintf("%d/%d", l.outputTokens, l.maxOTPM),
		Headroom:     headroom,
		QueueDepth:   len(l.queue),
		WindowResets: fmt.Sprintf("%.0fs", remaining.Seconds()),
	}
}

// calcHeadroom returns the minimum headroom across all dimensions. Must hold mu.
func (l *Limiter) calcHeadroom() float64 {
	headroom := 1.0
	if l.maxRPM > 0 {
		rpmUtil := float64(l.requests) / (float64(l.maxRPM) * l.margin)
		headroom = min(headroom, 1.0-rpmUtil)
	}
	if l.maxITPM > 0 {
		itpmUtil := float64(l.inputTokens) / (float64(l.maxITPM) * l.margin)
		headroom = min(headroom, 1.0-itpmUtil)
	}
	if l.maxOTPM > 0 {
		otpmUtil := float64(l.outputTokens) / (float64(l.maxOTPM) * l.margin)
		headroom = min(headroom, 1.0-otpmUtil)
	}
	if headroom < 0 {
		headroom = 0
	}
	return headroom
}

func (l *Limiter) resetIfNewWindow() {
	if time.Since(l.windowStart) > time.Minute {
		l.requests = 0
		l.inputTokens = 0
		l.outputTokens = 0
		l.windowStart = time.Now()

		// Process queued requests.
		// Only one fires per window reset. Since the window just started,
		// capacity is always available — no CanProceed re-check needed.
		for len(l.queue) > 0 {
			req := l.queue[0]
			l.queue = l.queue[1:]

			// Drop stale requests (older than 5 minutes)
			if time.Since(req.addedAt) > 5*time.Minute {
				continue
			}

			l.queueWg.Add(1)
			go func() {
				defer l.queueWg.Done()
				req.callback()
			}()
			break // one per window reset
		}
	}
}

// Enqueue adds a callback to be executed when capacity is available.
// Returns queue position, or -1 if the queue is full.
func (l *Limiter) Enqueue(cb func()) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	const maxQueue = 100
	if len(l.queue) >= maxQueue {
		return -1
	}

	l.queue = append(l.queue, queuedRequest{
		callback: cb,
		addedAt:  time.Now(),
	})

	return len(l.queue)
}

// EnqueueChat is Enqueue tagged with a Telegram chat so /stop can drop
// that chat's waiting items without firing them.
func (l *Limiter) EnqueueChat(chatID int64, cb func()) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	const maxQueue = 100
	if len(l.queue) >= maxQueue {
		return -1
	}

	l.queue = append(l.queue, queuedRequest{
		callback: cb,
		addedAt:  time.Now(),
		chatID:   chatID,
	})

	return len(l.queue)
}

// DropQueuedFor removes queued callbacks for chatID. Returns how many were dropped.
func (l *Limiter) DropQueuedFor(chatID int64) int {
	if l == nil || chatID == 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.queue[:0]
	dropped := 0
	for _, req := range l.queue {
		if req.chatID == chatID {
			dropped++
			continue
		}
		kept = append(kept, req)
	}
	l.queue = kept
	return dropped
}
