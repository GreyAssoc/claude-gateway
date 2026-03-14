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
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/agentsync"
	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/ctxbuilder"
	"github.com/you/claude-gateway/internal/db"
	"github.com/you/claude-gateway/internal/cron"
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

	// activeReqs maps chat ID → cancel function for the in-flight request,
	// allowing /stop to interrupt a running Claude session.
	activeReqs   map[int64]context.CancelFunc
	activeReqsMu sync.Mutex

	// lastAPIModel tracks the most recent API-reported model per chat.
	lastAPIModel   map[int64]string
	lastAPIModelMu sync.RWMutex
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
		api:        api,
		agent:      agentClient,
		limiter:    limiter,
		cfg:        cfg,
		database:   database,
		sessions:     make(map[int64]*SessionUsage),
		sem:          make(chan struct{}, maxConcurrentHandlers),
		activeReqs:   make(map[int64]context.CancelFunc),
		lastAPIModel: make(map[int64]string),
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

// registerTelegramCommands registers bot commands with Telegram so users
// get autocomplete in the command menu. Includes both built-in commands
// and skill commands (skills with a non-empty Command field).
func (b *Bot) registerTelegramCommands() {
	commands := []tgbotapi.BotCommand{
		{Command: "bootstrapset", Description: "Add/update bootstrap instruction"},
		{Command: "contactadd", Description: "Add contact detail to person"},
		{Command: "context", Description: "Current context layers"},
		{Command: "cost", Description: "Token usage this session"},
		{Command: "cronadd", Description: "Add cron job: name \"schedule\" prompt"},
		{Command: "crondel", Description: "Remove a cron job"},
		{Command: "crondisable", Description: "Disable a cron job"},
		{Command: "cronenable", Description: "Enable a cron job"},
		{Command: "cronlist", Description: "List scheduled jobs"},
		{Command: "dbcheck", Description: "Check PostgreSQL conversations"},
		{Command: "framework", Description: "Show/switch AI framework"},
		{Command: "journal", Description: "View daily journal"},
		{Command: "journalsearch", Description: "Search journal entries"},
		{Command: "memorieslist", Description: "List curated memories"},
		{Command: "model", Description: "Show actual API model in use"},
		{Command: "memoryforget", Description: "Remove a memory by text match"},
		{Command: "memorylist", Description: "List memories by category"},
		{Command: "memoryremember", Description: "Save a memory"},
		{Command: "peoplelist", Description: "List all people"},
		{Command: "personadd", Description: "Add person to contacts"},
		{Command: "persondel", Description: "Remove a person"},
		{Command: "personfind", Description: "Find person by name"},
		{Command: "profile", Description: "View user profile"},
		{Command: "profiledel", Description: "Delete profile aspect"},
		{Command: "profileset", Description: "Set profile aspect"},
		{Command: "reset", Description: "Clear conversation session"},
		{Command: "skills", Description: "List loaded skills"},
		{Command: "start", Description: "Show available commands"},
		{Command: "status", Description: "Gateway health & rate limits"},
		{Command: "stop", Description: "Stop the current request"},
		{Command: "sync", Description: "Sync agents/commands Claude ↔ OpenCode"},
	}

	builtinCount := len(commands)

	if b.skillsManager != nil {
		for _, s := range b.skillsManager.GetActiveSkills() {
			if s.Command != "" {
				desc := s.Description
				if desc == "" {
					desc = s.Name
				}
				// Telegram limits description to 256 characters (UTF-8 runes, not bytes)
				if utf8.RuneCountInString(desc) > 256 {
					runes := []rune(desc)
					desc = string(runes[:253]) + "..."
				}
				commands = append(commands, tgbotapi.BotCommand{
					Command:     s.Command,
					Description: desc,
				})
			}
		}
	}

	cfg := tgbotapi.NewSetMyCommands(commands...)
	if _, err := b.api.Request(cfg); err != nil {
		log.Printf("Failed to register Telegram commands: %v", err)
	} else {
		log.Printf("Registered %d Telegram commands (%d from skills)", len(commands), len(commands)-builtinCount)
	}
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
