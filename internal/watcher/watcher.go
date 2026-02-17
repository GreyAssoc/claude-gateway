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

	watchers []*watcher
	cancel   context.CancelFunc
}

type watcher struct {
	name     string
	enabled  bool
	interval time.Duration
	runFn    func(ctx context.Context)
}

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
		m.watchers = append(m.watchers, &watcher{
			name:     "gmail",
			enabled:  true,
			interval: time.Duration(watchersCfg.Gmail.IntervalSeconds) * time.Second,
			runFn:    gw.check,
		})
	}

	if watchersCfg.Calendar.Enabled {
		cw := newCalendarWatcher(m.agent, m.limiter, m.notify, m.workDir, watchersCfg.Calendar)
		m.watchers = append(m.watchers, &watcher{
			name:     "calendar",
			enabled:  true,
			interval: time.Duration(watchersCfg.Calendar.IntervalSeconds) * time.Second,
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
		go m.runLoop(ctx, w)
		log.Printf("Watcher %q started (interval: %s)", w.name, w.interval)
	}
}

// Stop halts all watchers.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *Manager) runLoop(ctx context.Context, w *watcher) {
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
	g.mu.Unlock()

	if !g.limiter.CanProceed(500) {
		return // silently skip when rate limited
	}

	prompt := strings.ReplaceAll(g.prompt, "{since}", since.Format(time.RFC3339))

	resp, err := g.agent.Ask(ctx, "watcher-gmail", prompt, g.workDir, 2)
	if err != nil {
		log.Printf("Gmail watcher error: %v", err)
		return
	}

	if resp.Usage != nil {
		g.limiter.Record(resp.Usage)
	}

	g.mu.Lock()
	g.lastCheck = time.Now()
	g.mu.Unlock()

	output := strings.TrimSpace(resp.Text)
	if output == "" || strings.EqualFold(output, "NONE") {
		return
	}

	g.notify(fmt.Sprintf("📬 *New Mail*\n\n%s", output))
}

// --- Calendar Watcher ---

type calendarWatcher struct {
	agent          *agent.Client
	limiter        *ratelimiter.Limiter
	notify         NotifyFunc
	workDir        string
	prompt         string
	mu             sync.Mutex
	notifiedEvents map[string]bool
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
		notifiedEvents: make(map[string]bool),
	}
}

func (c *calendarWatcher) check(ctx context.Context) {
	if !c.limiter.CanProceed(500) {
		return
	}

	resp, err := c.agent.Ask(ctx, "watcher-calendar", c.prompt, c.workDir, 2)
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
		c.notify(fmt.Sprintf("📅 *Calendar*\n\n%s", output))
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, event := range events {
		key := event.ID
		if key == "" {
			key = event.Title + event.Time
		}

		if c.notifiedEvents[key] {
			continue
		}
		c.notifiedEvents[key] = true

		loc := event.Location
		if loc == "" {
			loc = "No location"
		}

		c.notify(fmt.Sprintf("⏰ *Upcoming*: %s\n📍 %s\n🕐 %s", event.Title, loc, event.Time))
	}

	// Cleanup old entries (keep last 100)
	if len(c.notifiedEvents) > 100 {
		c.notifiedEvents = make(map[string]bool)
	}
}
