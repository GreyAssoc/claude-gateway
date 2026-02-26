// Package watcher polls external services (Gmail, Calendar) and sends notifications.
package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/ratelimiter"
)

// NotifyFunc sends notifications to the user.
type NotifyFunc func(text string)

// Manager runs all watchers.
type Manager struct {
	agent   *agent.Client
	limiter *ratelimiter.Limiter
	notify  NotifyFunc
	workDir string

	watchers []*watchTask
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

type watchTask struct {
	name     string
	enabled  bool
	interval time.Duration
	runFn    func(ctx context.Context)
}

// NewManager creates a watcher manager that polls external services for notifications.
func NewManager(agentClient *agent.Client, limiter *ratelimiter.Limiter, notify NotifyFunc, workDir string) *Manager {
	return &Manager{
		agent:   agentClient,
		limiter: limiter,
		notify:  notify,
		workDir: workDir,
	}
}

// Configure sets up watchers from config.
func (m *Manager) Configure(watchersCfg config.WatchersConfig) {
	if watchersCfg.Gmail.Enabled {
		gw := newGmailWatcher(m.agent, m.limiter, m.notify, m.workDir, watchersCfg.Gmail)
		m.watchers = append(m.watchers, &watchTask{
			name:     "gmail",
			enabled:  true,
			interval: safeInterval(watchersCfg.Gmail.IntervalSeconds, "gmail"),
			runFn:    gw.check,
		})
	}

	if watchersCfg.Calendar.Enabled {
		cw := newCalendarWatcher(m.agent, m.limiter, m.notify, m.workDir, watchersCfg.Calendar)
		m.watchers = append(m.watchers, &watchTask{
			name:     "calendar",
			enabled:  true,
			interval: safeInterval(watchersCfg.Calendar.IntervalSeconds, "calendar"),
			runFn:    cw.check,
		})
	}
}

// Start begins all configured watchers. Call Stop() to halt them.
func (m *Manager) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	for _, w := range m.watchers {
		if !w.enabled {
			continue
		}
		m.wg.Add(1)
		go func(w *watchTask) {
			defer m.wg.Done()
			m.runLoop(ctx, w)
		}(w)
		log.Printf("Watcher %q started (interval: %s)", w.name, w.interval)
	}
}

// Stop halts all watchers and waits for them to finish.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

func (m *Manager) runLoop(ctx context.Context, w *watchTask) {
	// Initial delay to avoid startup burst
	select {
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runFn(ctx)
		}
	}
}

// safeInterval validates watcher interval and returns a safe duration.
// Prevents time.NewTicker(0) panic when IntervalSeconds is zero or negative.
func safeInterval(seconds int, name string) time.Duration {
	if seconds <= 0 {
		log.Printf("Watcher %q: interval %ds invalid, defaulting to 60s", name, seconds)
		return 60 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

// --- Gmail Watcher ---

type gmailWatcher struct {
	agent      *agent.Client
	limiter    *ratelimiter.Limiter
	notify     NotifyFunc
	workDir    string
	prompt     string
	mu         sync.Mutex
	lastCheck  time.Time
}

func newGmailWatcher(a *agent.Client, l *ratelimiter.Limiter, n NotifyFunc, wd string, cfg config.WatcherConfig) *gmailWatcher {
	return &gmailWatcher{
		agent:     a,
		limiter:   l,
		notify:    n,
		workDir:   wd,
		prompt:    cfg.Prompt,
		lastCheck: time.Now(),
	}
}

func (g *gmailWatcher) check(ctx context.Context) {
	g.mu.Lock()
	since := g.lastCheck
	// Update lastCheck before the API call so that failed checks advance the
	// timestamp, preventing duplicate notification windows on next success.
	g.lastCheck = time.Now()
	g.mu.Unlock()

	if !g.limiter.CanProceed(500) {
		return // silently skip when rate limited
	}

	prompt := strings.ReplaceAll(g.prompt, "{since}", since.Format(time.RFC3339))

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	resp, err := g.agent.Ask(reqCtx, "watcher-gmail", prompt, g.workDir, 2)
	if err != nil {
		log.Printf("Gmail watcher error: %v", err)
		return
	}

	if resp.Usage != nil {
		g.limiter.Record(resp.Usage)
	}

	output := strings.TrimSpace(resp.Text)
	if output == "" || strings.EqualFold(output, "NONE") {
		return
	}

	if g.notify != nil {
		g.notify(fmt.Sprintf("📬 *New Mail*\n\n%s", output))
	}
}

// --- Calendar Watcher ---

type calendarWatcher struct {
	agent          *agent.Client
	limiter        *ratelimiter.Limiter
	notify         NotifyFunc
	workDir        string
	prompt         string
	mu             sync.Mutex
	notifiedEvents map[string]time.Time
}

type calendarEvent struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Time     string `json:"time"`
	Location string `json:"location"`
}

func newCalendarWatcher(a *agent.Client, l *ratelimiter.Limiter, n NotifyFunc, wd string, cfg config.WatcherConfig) *calendarWatcher {
	return &calendarWatcher{
		agent:          a,
		limiter:        l,
		notify:         n,
		workDir:        wd,
		prompt:         cfg.Prompt,
		notifiedEvents: make(map[string]time.Time),
	}
}

func (c *calendarWatcher) check(ctx context.Context) {
	if !c.limiter.CanProceed(500) {
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	resp, err := c.agent.Ask(reqCtx, "watcher-calendar", c.prompt, c.workDir, 2)
	if err != nil {
		log.Printf("Calendar watcher error: %v", err)
		return
	}

	if resp.Usage != nil {
		c.limiter.Record(resp.Usage)
	}

	output := strings.TrimSpace(resp.Text)
	if output == "" || output == "[]" {
		return
	}

	// Try to parse as JSON array
	var events []calendarEvent
	if err := json.Unmarshal([]byte(output), &events); err != nil {
		// If not valid JSON, just forward the text
		if c.notify != nil {
			c.notify(fmt.Sprintf("📅 *Calendar*\n\n%s", output))
		}
		return
	}

	// Cap events per check to prevent runaway notifications
	const maxEventsPerCheck = 50
	if len(events) > maxEventsPerCheck {
		events = events[:maxEventsPerCheck]
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	for _, event := range events {
		key := event.ID
		if key == "" {
			key = event.Title + "|" + event.Time
		}

		if _, seen := c.notifiedEvents[key]; seen {
			continue
		}
		c.notifiedEvents[key] = now

		loc := event.Location
		if loc == "" {
			loc = "No location"
		}

		if c.notify != nil {
			c.notify(fmt.Sprintf("⏰ *Upcoming*: %s\n📍 %s\n🕐 %s", event.Title, loc, event.Time))
		}
	}

	// Evict entries older than 24 hours
	for key, notifiedAt := range c.notifiedEvents {
		if now.Sub(notifiedAt) > 24*time.Hour {
			delete(c.notifiedEvents, key)
		}
	}

	// Hard cap to prevent unbounded growth within a 24h window.
	// O(n) per eviction, but bounded: the 24h eviction above handles normal
	// churn, so this hard-cap path rarely fires and only evicts 1-2 entries.
	const maxNotifiedEvents = 10000
	if len(c.notifiedEvents) > maxNotifiedEvents {
		for len(c.notifiedEvents) > maxNotifiedEvents {
			var oldestKey string
			var oldestTime time.Time
			first := true
			for k, t := range c.notifiedEvents {
				if first || t.Before(oldestTime) {
					oldestKey = k
					oldestTime = t
					first = false
				}
			}
			delete(c.notifiedEvents, oldestKey)
		}
	}
}
