// Package bot implements the Telegram bot that bridges user messages to Claude.
package bot

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/agentsync"
	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/cron"
	"github.com/you/claude-gateway/internal/ctxbuilder"
	"github.com/you/claude-gateway/internal/db"
	"github.com/you/claude-gateway/internal/gworkspace"
	"github.com/you/claude-gateway/internal/journal"
	"github.com/you/claude-gateway/internal/memory"
	"github.com/you/claude-gateway/internal/orchestrator"
	"github.com/you/claude-gateway/internal/people"
	"github.com/you/claude-gateway/internal/pgdb"
	"github.com/you/claude-gateway/internal/profile"
	"github.com/you/claude-gateway/internal/ratelimiter"
	"github.com/you/claude-gateway/internal/skills"
)

const maxTelegramMessage = 4000

// SessionUsage tracks per-session token costs.
type SessionUsage struct {
	InputTokens  int
	OutputTokens int
	Requests     int
	StartedAt    time.Time
	LastActive   time.Time
}

// maxConcurrentHandlers limits how many message handlers run simultaneously.
const maxConcurrentHandlers = 10

type runSnapshot struct {
	Display string
	Started time.Time
	Actions int
	Lines   []string
	Preview string
	GPU     string // last GPU queue line from sidecar, e.g. "GPU: queued 2 of 3"
}

type reqCancel struct {
	id        uint64
	cancel    context.CancelFunc
	Framework string
	mu        sync.Mutex
	snap      runSnapshot
}

func (e *reqCancel) copySnap() (runSnapshot, bool) {
	if e == nil {
		return runSnapshot{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snap.Started.IsZero() {
		return runSnapshot{}, false
	}
	s := e.snap
	s.Lines = append([]string(nil), e.snap.Lines...)
	return s, true
}

func (e *reqCancel) setSnap(s runSnapshot) {
	if e == nil {
		return
	}
	e.mu.Lock()
	s.Lines = append([]string(nil), s.Lines...)
	e.snap = s
	e.mu.Unlock()
}

// Bot wraps the Telegram bot with gateway integrations.
type Bot struct {
	api        *tgbotapi.BotAPI
	agent      agent.Backend
	limiter    *ratelimiter.Limiter
	cfg        *config.Config
	database   *db.DB
	sessions   map[int64]*SessionUsage
	sessionsMu sync.RWMutex

	// sem limits concurrent handler goroutines.
	sem chan struct{}
	// handlerWg tracks in-flight handler goroutines for clean shutdown.
	handlerWg sync.WaitGroup

	// activeReqs maps "chatID:slot" → in-flight cancel so /stop can abort
	// one agent without killing the others.
	activeReqs     map[string]*reqCancel
	activeReqsMu   sync.Mutex
	activeReqSeq   atomic.Uint64
	currentAgent   map[int64]agentSlot
	currentAgentMu sync.RWMutex

	// slotFW is a per-chat per-agent runtime framework override (qwen/deepseek/grok).
	// Empty / missing means inherit DNA YAML then the gateway default (Switcher.Active).
	slotFW   map[string]string
	slotFWMu sync.RWMutex

	// lastAPIModel tracks the most recent API-reported model per chat.
	lastAPIModel   map[int64]string
	lastAPIModelMu sync.RWMutex

	// skipQwenHistory: after /reset, do not re-inject the 30-turn DB dump
	// on the next Qwen message (that overflowed the 131k window).
	// Keyed by sidecar session ID so named agents do not share the flag.
	skipQwenHistory   map[string]bool
	skipQwenHistoryMu sync.Mutex
	// bgWg tracks fire-and-forget background goroutines (RecordExchange, fact
	// extraction, profile update) so shutdown waits for them to finish before
	// closing the database.
	bgWg sync.WaitGroup

	cronStatus          func() string
	cronScheduler       *cron.Scheduler
	systemPromptBuilder func(ctx context.Context) string
	skillsManager       *skills.Manager
	memoryManager       *memory.Manager
	memoryCompactor     *memory.Compactor
	orchestrator        *orchestrator.Engine

	// v2: PostgreSQL-backed memory, context, journal, profile
	pgDB           *pgdb.DB
	contextBuilder *ctxbuilder.Builder
	peopleManager  *people.Manager
	journal        *journal.Manager
	profiler       *profile.Profiler
	agentSyncer    *agentsync.Syncer

	// Google Workspace OAuth service — used by /googleauth and shared with
	// the sidecar's gog binary via a writable token volume.
	google *gworkspace.GoogleService
}

// Option configures optional Bot dependencies.
type Option func(*Bot)

// WithCronStatus sets the function that returns cron job status text.
func WithCronStatus(fn func() string) Option {
	return func(b *Bot) { b.cronStatus = fn }
}

// WithSystemPromptBuilder sets the function that builds the system prompt.
func WithSystemPromptBuilder(fn func(ctx context.Context) string) Option {
	return func(b *Bot) { b.systemPromptBuilder = fn }
}

// WithSkillsManager sets the skills manager.
func WithSkillsManager(sm *skills.Manager) Option {
	return func(b *Bot) { b.skillsManager = sm }
}

// WithMemoryManager sets the memory manager.
func WithMemoryManager(mm *memory.Manager) Option {
	return func(b *Bot) { b.memoryManager = mm }
}

// WithMemoryCompactor sets the memory compactor.
func WithMemoryCompactor(mc *memory.Compactor) Option {
	return func(b *Bot) { b.memoryCompactor = mc }
}

// WithOrchestrator sets the orchestrator engine.
func WithOrchestrator(oe *orchestrator.Engine) Option {
	return func(b *Bot) { b.orchestrator = oe }
}

// WithPgDB sets the PostgreSQL database for persistent memory.
func WithPgDB(d *pgdb.DB) Option {
	return func(b *Bot) { b.pgDB = d }
}

// WithContextBuilder sets the context builder for system/enriched prompts.
func WithContextBuilder(cb *ctxbuilder.Builder) Option {
	return func(b *Bot) { b.contextBuilder = cb }
}

// WithPeopleManager sets the people manager.
func WithPeopleManager(pm *people.Manager) Option {
	return func(b *Bot) { b.peopleManager = pm }
}

// WithJournal sets the daily journal manager.
func WithJournal(j *journal.Manager) Option {
	return func(b *Bot) { b.journal = j }
}

// WithProfiler sets the user profiler.
func WithProfiler(p *profile.Profiler) Option {
	return func(b *Bot) { b.profiler = p }
}

// WithAgentSyncer sets the agent/command syncer.
func WithAgentSyncer(s *agentsync.Syncer) Option {
	return func(b *Bot) { b.agentSyncer = s }
}

// WithGoogle sets the Google Workspace OAuth service used by /googleauth.
// The same service writes the token JSON the sidecar's gog binary reads.
func WithGoogle(g *gworkspace.GoogleService) Option {
	return func(b *Bot) { b.google = g }
}

// New creates a new Bot. Optional dependencies are configured via Option functions.
func New(cfg *config.Config, agentClient agent.Backend, limiter *ratelimiter.Limiter, database *db.DB, opts ...Option) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.Telegram.BotToken)
	if err != nil {
		return nil, fmt.Errorf("creating telegram bot: %w", err)
	}
	api.Client = &http.Client{Timeout: 30 * time.Second}

	log.Printf("Telegram bot authorized as @%s", api.Self.UserName)

	// Initialize the project registry for project detection from user messages.
	// Try Docker path first (configs/ is mounted), then local dev path.
	for _, p := range []string{
		"/app/configs/project-registry.json",
		"configs/project-registry.json",
		"hooks/context/project-registry.json",
	} {
		if _, err := os.Stat(p); err == nil {
			setRegistryPath(p)
			break
		}
	}

	b := &Bot{
		api:             api,
		agent:           agentClient,
		limiter:         limiter,
		cfg:             cfg,
		database:        database,
		sessions:        make(map[int64]*SessionUsage),
		sem:             make(chan struct{}, maxConcurrentHandlers),
		activeReqs:      make(map[string]*reqCancel),
		currentAgent:    make(map[int64]agentSlot),
		slotFW:          make(map[string]string),
		lastAPIModel:    make(map[int64]string),
		skipQwenHistory: make(map[string]bool),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b, nil
}

// SetCronStatus sets the cron status function. This exists as a separate method
// because the cron scheduler requires the bot (for SendNotification), creating a
// circular dependency that cannot be resolved through the constructor.
func (b *Bot) SetCronStatus(fn func() string) {
	b.cronStatus = fn
}

// SetCronScheduler gives the bot direct access to the cron scheduler for
// dynamic job management (add/remove/enable/disable via Telegram commands).
func (b *Bot) SetCronScheduler(s *cron.Scheduler) {
	b.cronScheduler = s
}

// WaitBackground blocks until all fire-and-forget background goroutines
// (RecordExchange, fact extraction, profile update) have finished. Call
// during shutdown after the bot's Run has returned, before closing the DB.
func (b *Bot) WaitBackground() {
	b.bgWg.Wait()
}

// SendNotification sends a message to the configured notification chat.
func (b *Bot) SendNotification(text string) {
	if b.cfg.Telegram.NotifyChatID == 0 {
		return
	}
	b.sendMessage(b.cfg.Telegram.NotifyChatID, text)
}

// Run starts the bot polling loop. Blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	b.registerTelegramCommands()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			b.handlerWg.Wait()
			return
		case update := <-updates:
			// Handle inline keyboard callback queries (e.g. /models selection)
			if update.CallbackQuery != nil {
				go b.handleCallbackQuery(ctx, update.CallbackQuery)
				continue
			}
			if update.Message == nil {
				continue
			}
			// Acquire semaphore slot, but respect context cancellation
			// to avoid blocking shutdown when all slots are occupied.
			select {
			case b.sem <- struct{}{}:
			case <-ctx.Done():
				b.api.StopReceivingUpdates()
				b.handlerWg.Wait()
				return
			}
			b.handlerWg.Add(1)
			go func(msg *tgbotapi.Message) {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("Handler panic recovered: %v", r)
						b.SendNotification(fmt.Sprintf("PANIC in handler: %v", r))
					}
				}()
				defer b.handlerWg.Done()
				defer func() { <-b.sem }()
				b.handleUpdate(ctx, msg)
			}(update.Message)
		}
	}
}

func (b *Bot) handleUpdate(ctx context.Context, msg *tgbotapi.Message) {
	// msg.From is nil for channel posts and anonymous messages
	if msg.From == nil {
		return
	}

	// Auth check
	if !b.cfg.Telegram.AllowedUserIDs[msg.From.ID] {
		log.Printf("Unauthorized user %d (%s)", msg.From.ID, msg.From.UserName)
		return
	}

	// Route commands
	if msg.IsCommand() {
		b.handleCommand(ctx, msg)
		return
	}

	// Regular message — send to Claude
	b.handleMessage(ctx, msg)
}

// handleCallbackQuery processes inline keyboard button presses.
func (b *Bot) handleCallbackQuery(_ context.Context, cq *tgbotapi.CallbackQuery) {
	// Acknowledge the callback to remove the "loading" spinner
	ack := tgbotapi.NewCallback(cq.ID, "")
	if _, err := b.api.Request(ack); err != nil {
		log.Printf("Failed to ack callback query: %v", err)
	}

	data := cq.Data
	chatID := cq.Message.Chat.ID

	switch {
	case strings.HasPrefix(data, "model:"):
		modelID := strings.TrimPrefix(data, "model:")
		b.switchSelectableModel(chatID, modelID)

		// Delete the keyboard message to keep chat clean
		del := tgbotapi.NewDeleteMessage(chatID, cq.Message.MessageID)
		if _, err := b.api.Request(del); err != nil {
			log.Printf("Failed to delete models keyboard: %v", err)
		}

	case strings.HasPrefix(data, "agent:"):
		handle := strings.TrimPrefix(data, "agent:")
		fake := &tgbotapi.Message{Chat: cq.Message.Chat, From: cq.From, Text: "@" + handle}
		b.handleMessage(context.Background(), fake)
		del := tgbotapi.NewDeleteMessage(chatID, cq.Message.MessageID)
		if _, err := b.api.Request(del); err != nil {
			log.Printf("Failed to delete agent picker: %v", err)
		}

	case strings.HasPrefix(data, "menu:"):
		// Delete the menu message to keep chat clean
		del := tgbotapi.NewDeleteMessage(chatID, cq.Message.MessageID)
		if _, err := b.api.Request(del); err != nil {
			log.Printf("Failed to delete command menu: %v", err)
		}

		// Synthesize a command message and route it
		cmd := strings.TrimPrefix(data, "menu:")
		fakeMsg := &tgbotapi.Message{
			Chat: cq.Message.Chat,
			From: cq.From,
			Text: "/" + cmd,
		}
		fakeMsg.Entities = []tgbotapi.MessageEntity{{
			Type:   "bot_command",
			Offset: 0,
			Length: len("/" + cmd),
		}}
		b.handleCommand(context.Background(), fakeMsg)

	default:
		log.Printf("Unknown callback data: %s", data)
	}
}

// registerTelegramCommands publishes the / menu Telegram shows when you type /.
// Agents are included as slash commands because Telegram's @ picker is only
// for usernames, not bot-defined specialists.
func (b *Bot) registerTelegramCommands() {
	commands := buildTelegramCommandList(b.skillsManager)
	if len(commands) == 0 {
		return
	}
	cfg := tgbotapi.NewSetMyCommands(commands...)
	if _, err := b.api.Request(cfg); err != nil {
		log.Printf("Failed to register Telegram commands: %v", err)
		return
	}
	log.Printf("Registered %d Telegram / commands (cap %d)", len(commands), telegramMaxCommands)
}

// --- helpers ---

func (b *Bot) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("Failed to send message to %d: %v", chatID, err)
	}
}

func (b *Bot) sendMarkdownMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdown
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("Failed to send message to %d: %v", chatID, err)
	}
}

func (b *Bot) sendLongMessage(chatID int64, text string) {
	for len(text) > 0 {
		// Telegram counts UTF-8 characters, not bytes. Find the byte offset
		// of the maxTelegramMessage-th rune to split correctly.
		runeCount := utf8.RuneCountInString(text)
		if runeCount <= maxTelegramMessage {
			b.sendMessage(chatID, text)
			return
		}

		// Walk forward to find the byte offset of the maxTelegramMessage-th rune.
		end := 0
		for i := 0; i < maxTelegramMessage; i++ {
			_, size := utf8.DecodeRuneInString(text[end:])
			end += size
		}

		// Prefer splitting at a newline for cleaner multi-part messages.
		if nl := strings.LastIndex(text[:end], "\n"); nl > end/2 {
			end = nl + 1 // include the newline in this chunk
		}

		b.sendMessage(chatID, text[:end])
		text = text[end:]
	}
}

func (b *Bot) recordSessionUsage(chatID int64, usage *agent.Usage) {
	b.sessionsMu.Lock()
	defer b.sessionsMu.Unlock()

	now := time.Now()

	s, ok := b.sessions[chatID]
	if !ok {
		s = &SessionUsage{StartedAt: now}
		b.sessions[chatID] = s
	}

	s.InputTokens += usage.InputTokens
	s.OutputTokens += usage.OutputTokens
	s.Requests++
	s.LastActive = now

	// Evict sessions idle for >24h to prevent unbounded map growth.
	// Hard cap at 1000: if still over after time-based eviction, drop least-recently-active.
	const maxSessions = 1000
	if len(b.sessions) > 50 {
		for id, sess := range b.sessions {
			if now.Sub(sess.LastActive) > 24*time.Hour {
				delete(b.sessions, id)
			}
		}
	}
	if len(b.sessions) > maxSessions {
		// Same eviction pattern as calendarWatcher.check (watcher.go).
		// O(n) per eviction, but bounded: the 24h eviction above handles normal
		// churn, so this hard-cap path rarely fires and only evicts 1-2 entries.
		for len(b.sessions) > maxSessions {
			var oldestID int64
			var oldestTime time.Time
			first := true
			for id, sess := range b.sessions {
				if first || sess.LastActive.Before(oldestTime) {
					oldestID = id
					oldestTime = sess.LastActive
					first = false
				}
			}
			delete(b.sessions, oldestID)
		}
	}
}
