package ratelimiter

import (
	"fmt"
	"sync"
	"time"

	"github.com/you/claude-gateway/internal/agent"
)

// DBRecorder is an optional interface for persisting rate limit snapshots.
type DBRecorder interface {
	RecordRateLimitSnapshot(requests, inputTokens, outputTokens int, headroom float64) error
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
	queue []queuedRequest

	// Optional DB recorder for snapshots
	db DBRecorder
}

type queuedRequest struct {
	callback func()
	addedAt  time.Time
}

func New(rpm, itpm, otpm int, safetyMargin float64) *Limiter {
	return &Limiter{
		maxRPM:      rpm,
		maxITPM:     itpm,
		maxOTPM:     otpm,
		margin:      safetyMargin,
		windowStart: time.Now(),
	}
}

// SetDB attaches a database recorder for persisting snapshots.
func (l *Limiter) SetDB(db DBRecorder) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.db = db
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

	// Async-write snapshot to DB if available
	if l.db != nil {
		headroom := l.calcHeadroom()
		db := l.db
		reqs := l.requests
		inp := l.inputTokens
		out := l.outputTokens
		go func() {
			_ = db.RecordRateLimitSnapshot(reqs, inp, out, headroom)
		}()
	}
}

// GetStatus returns current rate limit utilization.
func (l *Limiter) GetStatus() Status {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.resetIfNewWindow()

	elapsed := time.Since(l.windowStart)
	remaining := time.Minute - elapsed

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

		// Process queued requests
		for len(l.queue) > 0 {
			req := l.queue[0]
			l.queue = l.queue[1:]

			// Drop stale requests (older than 5 minutes)
			if time.Since(req.addedAt) > 5*time.Minute {
				continue
			}

			go req.callback()
			break // one per window reset
		}
	}
}

// Enqueue adds a callback to be executed when capacity is available.
func (l *Limiter) Enqueue(cb func()) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.queue = append(l.queue, queuedRequest{
		callback: cb,
		addedAt:  time.Now(),
	})

	return len(l.queue)
}
