// Package ratelimiter tracks request rate and per-conversation context use.
//
// RPM is one shared minute window. Token totals are not. Each agent
// conversation has its own counter, sized to that framework's context window.
// A DeepSeek turn is never added to a Grok turn, and two DeepSeek agents are
// never added to each other: each conversation already holds its own window.
package ratelimiter

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/you/claude-gateway/internal/agent"
)

const (
	// ReasonRPM is a shared request-per-minute block. Token totals are not involved.
	ReasonRPM = "rpm"
	// ReasonContext means this conversation alone is at its model's window.
	ReasonContext = "context"

	conversationTTL  = 30 * time.Minute
	maxConversations = 200
)

// DBRecorder is an optional interface for persisting rate limit snapshots.
type DBRecorder interface {
	RecordRateLimitSnapshot(ctx context.Context, requests, inputTokens, outputTokens int, headroom float64) error
}

// Status is the gateway-wide view. Token fields are not a shared pool.
type Status struct {
	RPM           string              `json:"rpm"`
	ITPM          string              `json:"itpm"`
	OTPM          string              `json:"otpm"`
	Headroom      float64             `json:"headroom"`
	QueueDepth    int                 `json:"queue_depth"`
	WindowResets  string              `json:"window_resets"`
	Conversations []ConversationUsage `json:"conversations"`
}

// ConversationUsage is one agent conversation against its own context window.
// ContextTokens is the latest prompt occupancy, not the sum of every tool
// round and not any other agent's tokens. OutputTokens is that turn only.
type ConversationUsage struct {
	Framework     string  `json:"framework"`
	SessionID     string  `json:"session_id"`
	Model         string  `json:"model"`
	ContextTokens int     `json:"context_tokens"`
	ContextWindow int     `json:"context_window"`
	OutputTokens  int     `json:"output_tokens"`
	Headroom      float64 `json:"headroom"`
}

// Scope names the conversation a turn belongs to.
type Scope struct {
	Framework string
	SessionID string
	Model     string
}

func (s Scope) key() string {
	fw := s.Framework
	if fw == "" {
		fw = "claude"
	}
	sid := s.SessionID
	if sid == "" {
		sid = "default"
	}
	return fw + "\x00" + sid
}

// AdmitResult is the decision for one conversation.
type AdmitResult struct {
	OK     bool
	Reason string
	Usage  ConversationUsage
	Status Status
}

// Limiter tracks RPM plus one context counter per conversation.
type Limiter struct {
	mu sync.Mutex

	maxRPM int
	margin float64

	requests    int
	windowStart time.Time

	conversations map[string]*conversation

	queue   []queuedRequest
	queueWg sync.WaitGroup

	db       DBRecorder
	dbWrites chan dbSnapshot
	dbLoopWg sync.WaitGroup
	closed   bool
}

type conversation struct {
	framework     string
	sessionID     string
	model         string
	window        int
	contextTokens int
	outputTokens  int
	updated       time.Time
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

// New creates a limiter. rpm is the shared request cap. itpm and otpm are
// ignored: token admission uses each framework's context window, per conversation.
func New(rpm, _, _ int, safetyMargin float64) *Limiter {
	if safetyMargin <= 0 || safetyMargin > 1 {
		safetyMargin = 0.9
	}
	return &Limiter{
		maxRPM:        rpm,
		margin:        safetyMargin,
		windowStart:   time.Now(),
		conversations: make(map[string]*conversation),
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
		close(l.dbWrites)
	}
	l.db = db
	ch := make(chan dbSnapshot, 64)
	l.dbWrites = ch
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
	l.queue = nil
	l.closed = true
	l.mu.Unlock()

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
		l.dbLoopWg.Wait()
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

// Admit reports whether this conversation may start a turn.
// Other conversations' tokens are not consulted. Local Qwen always passes.
func (l *Limiter) Admit(scope Scope, estimatedInput int) AdmitResult {
	if estimatedInput < 0 {
		estimatedInput = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.resetRPMIfNewWindow()
	l.expireConversations()
	st := l.statusLocked()

	if Unlimited(scope.Framework) {
		return AdmitResult{OK: true, Status: st, Usage: l.usageLocked(scope)}
	}
	if l.maxRPM > 0 && float64(l.requests) >= float64(l.maxRPM)*l.margin {
		return AdmitResult{OK: false, Reason: ReasonRPM, Status: st, Usage: l.usageLocked(scope)}
	}

	usage := l.usageLocked(scope)
	limit := int(float64(usage.ContextWindow) * l.margin)
	if usage.ContextWindow > 0 && limit > 0 && usage.ContextTokens+estimatedInput >= limit {
		return AdmitResult{OK: false, Reason: ReasonContext, Status: st, Usage: usage}
	}
	return AdmitResult{OK: true, Status: st, Usage: usage}
}

// Record stores this conversation's latest context occupancy.
// It replaces the previous figure. It does not add the sidecar's summed
// tool-round input onto any shared pool, and it does not touch other scopes.
// countRequest is false for local Qwen so those turns do not consume RPM.
//
// When the sidecar attached Context, TokensUsed is the occupancy (the latest
// prompt). usage.InputTokens is the sum of every API round in the turn and
// is not the context counter.
func (l *Limiter) Record(scope Scope, usage *agent.Usage, ctx *agent.ContextStatus, countRequest bool) {
	if usage == nil && ctx == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.resetRPMIfNewWindow()
	if countRequest {
		l.requests++
	}

	c := l.conversation(scope)
	if ctx != nil {
		if ctx.ContextWindow > 0 {
			c.window = ctx.ContextWindow
		}
		// Latest prompt size. Zero means the sidecar had no occupancy figure;
		// do not wipe a real measurement, and do not fall back to the summed
		// tool-round input_tokens while Context is present.
		if ctx.TokensUsed > 0 {
			c.contextTokens = ctx.TokensUsed
		}
	} else if usage != nil {
		c.contextTokens = usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	}
	if usage != nil {
		c.outputTokens = usage.OutputTokens
	}
	c.updated = time.Now()
	l.expireConversations()

	if !countRequest || l.dbWrites == nil {
		return
	}
	view := c.usage()
	select {
	case l.dbWrites <- dbSnapshot{
		requests:     l.requests,
		inputTokens:  view.ContextTokens,
		outputTokens: view.OutputTokens,
		headroom:     view.Headroom,
	}:
	default:
		log.Println("Rate limiter: snapshot dropped (channel full)")
	}
}

// ResetScope clears one conversation after compaction. Other agents stay put.
func (l *Limiter) ResetScope(scope Scope) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.conversations[scope.key()]
	if !ok {
		return
	}
	c.contextTokens = 0
	c.outputTokens = 0
	c.updated = time.Now()
}

// GetStatus returns RPM plus every live conversation's own window.
func (l *Limiter) GetStatus() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resetRPMIfNewWindow()
	l.expireConversations()
	return l.statusLocked()
}

func (l *Limiter) statusLocked() Status {
	elapsed := time.Since(l.windowStart)
	remaining := time.Minute - elapsed
	if remaining < 0 {
		remaining = 0
	}
	convs := make([]ConversationUsage, 0, len(l.conversations))
	for _, c := range l.conversations {
		convs = append(convs, c.usage())
	}
	sort.Slice(convs, func(i, j int) bool {
		if convs[i].Framework != convs[j].Framework {
			return convs[i].Framework < convs[j].Framework
		}
		return convs[i].SessionID < convs[j].SessionID
	})
	return Status{
		RPM:           fmt.Sprintf("%d/%d", l.requests, l.maxRPM),
		ITPM:          "per-conversation",
		OTPM:          "per-conversation",
		Headroom:      l.rpmHeadroom(),
		QueueDepth:    len(l.queue),
		WindowResets:  fmt.Sprintf("%.0fs", remaining.Seconds()),
		Conversations: convs,
	}
}

func (l *Limiter) usageLocked(scope Scope) ConversationUsage {
	if c, ok := l.conversations[scope.key()]; ok {
		return c.usage()
	}
	window := ContextWindow(scope.Framework, scope.Model)
	return ConversationUsage{
		Framework:     scope.Framework,
		SessionID:     scope.SessionID,
		Model:         scope.Model,
		ContextWindow: window,
		Headroom:      1,
	}
}

func (c *conversation) usage() ConversationUsage {
	window := c.window
	if window <= 0 {
		window = ContextWindow(c.framework, c.model)
	}
	headroom := 1.0
	if window > 0 {
		limit := float64(window)
		if limit > 0 {
			headroom = 1 - float64(c.contextTokens)/limit
		}
	}
	if headroom < 0 {
		headroom = 0
	}
	model := c.model
	return ConversationUsage{
		Framework:     c.framework,
		SessionID:     c.sessionID,
		Model:         model,
		ContextTokens: c.contextTokens,
		ContextWindow: window,
		OutputTokens:  c.outputTokens,
		Headroom:      headroom,
	}
}

func (l *Limiter) rpmHeadroom() float64 {
	if l.maxRPM <= 0 {
		return 1
	}
	h := 1 - float64(l.requests)/(float64(l.maxRPM)*l.margin)
	if h < 0 {
		return 0
	}
	return h
}

func (l *Limiter) conversation(scope Scope) *conversation {
	k := scope.key()
	c, ok := l.conversations[k]
	if !ok {
		c = &conversation{
			framework: scope.Framework,
			sessionID: scope.SessionID,
			window:    ContextWindow(scope.Framework, scope.Model),
		}
		l.conversations[k] = c
	}
	if scope.Model != "" {
		c.model = scope.Model
	}
	if scope.Framework != "" {
		c.framework = scope.Framework
	}
	if c.window <= 0 {
		c.window = ContextWindow(c.framework, c.model)
	}
	return c
}

func (l *Limiter) expireConversations() {
	now := time.Now()
	for k, c := range l.conversations {
		if !c.updated.IsZero() && now.Sub(c.updated) > conversationTTL {
			delete(l.conversations, k)
		}
	}
	for len(l.conversations) > maxConversations {
		var oldestK string
		var oldest time.Time
		first := true
		for k, c := range l.conversations {
			if first || c.updated.Before(oldest) {
				first = false
				oldest = c.updated
				oldestK = k
			}
		}
		if oldestK == "" {
			return
		}
		delete(l.conversations, oldestK)
	}
}

func (l *Limiter) resetRPMIfNewWindow() {
	if time.Since(l.windowStart) <= time.Minute {
		return
	}
	l.requests = 0
	l.windowStart = time.Now()

	// One queued request per RPM window. Context counters are not cleared:
	// a model's window does not expire at 60 seconds, and other conversations
	// must keep their own figures.
	for len(l.queue) > 0 {
		req := l.queue[0]
		l.queue = l.queue[1:]
		if time.Since(req.addedAt) > 5*time.Minute {
			continue
		}
		l.queueWg.Add(1)
		go func() {
			defer l.queueWg.Done()
			req.callback()
		}()
		break
	}
}

// Enqueue adds a callback to be executed when RPM capacity is available.
// Returns queue position, or -1 if the queue is full.
func (l *Limiter) Enqueue(cb func()) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enqueue(0, cb)
}

// EnqueueChat is Enqueue tagged with a Telegram chat so /stop can drop
// that chat's waiting items without firing them.
func (l *Limiter) EnqueueChat(chatID int64, cb func()) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enqueue(chatID, cb)
}

func (l *Limiter) enqueue(chatID int64, cb func()) int {
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
