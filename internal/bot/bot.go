package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/db"
	"github.com/you/claude-gateway/internal/memory"
	"github.com/you/claude-gateway/internal/orchestrator"
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
}

// Bot wraps the Telegram bot with gateway integrations.
type Bot struct {
	api        *tgbotapi.BotAPI
	agent      *agent.Client
	limiter    *ratelimiter.Limiter
	cfg        *config.Config
	database   *db.DB
	sessions   map[int64]*SessionUsage
	sessionsMu sync.RWMutex

	// CronStatus is set by the cron scheduler for /cron command
	CronStatus func() string

	// SystemPromptBuilder builds the system prompt for requests.
	// Set by skills/memory managers after initialization.
	SystemPromptBuilder func() string

	// SkillsManager provides access to skills for bot commands.
	SkillsManager *skills.Manager

	// MemoryManager provides access to memory for bot commands.
	MemoryManager *memory.Manager

	// MemoryCompactor handles daily memory compaction.
	MemoryCompactor *memory.Compactor

	// Orchestrator runs agent team reviews.
	Orchestrator *orchestrator.Engine
}

func New(cfg *config.Config, agentClient *agent.Client, limiter *ratelimiter.Limiter, database *db.DB) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.Telegram.BotToken)
	if err != nil {
		return nil, fmt.Errorf("creating telegram bot: %w", err)
	}

	log.Printf("Telegram bot authorized as @%s", api.Self.UserName)

	return &Bot{
		api:      api,
		agent:    agentClient,
		limiter:  limiter,
		cfg:      cfg,
		database: database,
		sessions: make(map[int64]*SessionUsage),
	}, nil
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
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return
		case update := <-updates:
			if update.Message == nil {
				continue
			}
			go b.handleUpdate(ctx, update.Message)
		}
	}
}

func (b *Bot) handleUpdate(ctx context.Context, msg *tgbotapi.Message) {
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

func (b *Bot) handleCommand(ctx context.Context, msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		b.sendMessage(msg.Chat.ID,
			"Claude Gateway v2 is ready.\n\n"+
				"Send me any message and I'll process it with Claude Code.\n\n"+
				"Commands:\n"+
				"/status — gateway health & rate limits\n"+
				"/reset — clear conversation session\n"+
				"/cost — token usage this session\n"+
				"/cron — active scheduled jobs\n"+
				"/skills — list loaded skills\n"+
				"/memories — list curated memories\n"+
				"/remember <text> — save a memory\n"+
				"/context — current context layers")

	case "status":
		b.handleStatus(ctx, msg)

	case "reset":
		b.handleReset(ctx, msg)

	case "cost":
		b.handleCost(msg)

	case "cron":
		b.handleCronList(msg)

	case "skills":
		b.handleSkills(msg)

	case "skill":
		b.handleSkillDetail(msg)

	case "skill_enable":
		b.handleSkillToggle(msg, true)

	case "skill_disable":
		b.handleSkillToggle(msg, false)

	case "remember":
		b.handleRemember(msg)

	case "forget":
		b.handleForget(msg)

	case "memories":
		b.handleMemories(msg)

	case "today":
		b.handleToday(msg)

	case "context":
		b.handleContext(msg)

	case "review_go":
		b.handleOrchestratorTeam(ctx, msg, "go-dev-team")

	case "review_go_sec":
		b.handleOrchestratorTeam(ctx, msg, "go-sec-team")

	case "review_fe":
		b.handleOrchestratorTeam(ctx, msg, "fe-dev-team")

	case "review_fe_sec":
		b.handleOrchestratorTeam(ctx, msg, "fe-sec-team")

	case "review_design":
		b.handleOrchestratorTeam(ctx, msg, "design")

	case "review_integration":
		b.handleOrchestratorTeam(ctx, msg, "integration")

	case "gauntlet":
		b.handleGauntlet(ctx, msg)

	case "agents":
		b.handleAgentsList(msg)

	case "runs":
		b.handleRunsList(msg)

	default:
		b.sendMessage(msg.Chat.ID, "Unknown command. Try /status, /reset, /cost, /cron, /skills, or /agents.")
	}
}

func (b *Bot) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	if msg.Text == "" {
		return
	}

	// Check rate limits (estimate ~1000 input tokens for a typical request)
	if !b.limiter.CanProceed(1000) {
		status := b.limiter.GetStatus()
		position := b.limiter.Enqueue(func() {
			b.processMessage(ctx, msg)
		})
		b.sendMessage(msg.Chat.ID,
			fmt.Sprintf("Rate limited. Queued at position %d.\nWindow resets in %s.\nHeadroom: %.0f%%",
				position, status.WindowResets, status.Headroom*100))
		return
	}

	b.processMessage(ctx, msg)
}

func (b *Bot) processMessage(ctx context.Context, msg *tgbotapi.Message) {
	// Send "working" indicator
	thinking := tgbotapi.NewMessage(msg.Chat.ID, "Working...")
	thinkingMsg, err := b.api.Send(thinking)
	if err != nil {
		log.Printf("Failed to send thinking message: %v", err)
	}

	// Create a session ID from the chat ID
	sessionID := fmt.Sprintf("tg-%d", msg.Chat.ID)

	// Build request options
	opts := agent.AskOptions{
		SessionID: sessionID,
		Prompt:    msg.Text,
		WorkDir:   b.cfg.WorkDir,
	}

	// Inject system prompt if builder is available
	if b.SystemPromptBuilder != nil {
		opts.SystemPrompt = b.SystemPromptBuilder()
	}

	// Call Claude via sidecar
	resp, err := b.agent.AskWithOptions(ctx, opts)

	// Delete "working" message
	if thinkingMsg.MessageID != 0 {
		del := tgbotapi.NewDeleteMessage(msg.Chat.ID, thinkingMsg.MessageID)
		b.api.Send(del)
	}

	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}

	// Record usage
	if resp.Usage != nil {
		b.limiter.Record(resp.Usage)
		b.recordSessionUsage(msg.Chat.ID, resp.Usage)

		// Record to DB
		if b.database != nil {
			_ = b.database.RecordSession(
				sessionID, "user", msg.Text,
				resp.Usage.InputTokens, resp.Usage.OutputTokens,
				resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens,
			)
		}
	}

	// Send response, splitting if needed
	text := resp.Text
	if text == "" {
		text = "Done (no text output)."
	}

	b.sendLongMessage(msg.Chat.ID, text)
}

func (b *Bot) handleStatus(ctx context.Context, msg *tgbotapi.Message) {
	rl := b.limiter.GetStatus()
	sidecarStatus := "Healthy"
	if err := b.agent.Health(ctx); err != nil {
		sidecarStatus = fmt.Sprintf("Error: %v", err)
	}

	lines := []string{
		"*Gateway Status*",
		"",
		fmt.Sprintf("Sidecar: %s", sidecarStatus),
		"",
		"*Rate Limits:*",
		fmt.Sprintf("  RPM: %s", rl.RPM),
		fmt.Sprintf("  ITPM: %s", rl.ITPM),
		fmt.Sprintf("  OTPM: %s", rl.OTPM),
		fmt.Sprintf("  Headroom: %.0f%%", rl.Headroom*100),
		fmt.Sprintf("  Window resets: %s", rl.WindowResets),
	}

	if rl.QueueDepth > 0 {
		lines = append(lines, fmt.Sprintf("  Queued: %d", rl.QueueDepth))
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, strings.Join(lines, "\n"))
	reply.ParseMode = tgbotapi.ModeMarkdown
	b.api.Send(reply)
}

func (b *Bot) handleReset(ctx context.Context, msg *tgbotapi.Message) {
	sessionID := fmt.Sprintf("tg-%d", msg.Chat.ID)
	if err := b.agent.ResetSession(ctx, sessionID); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Reset failed: %v", err))
		return
	}

	b.sessionsMu.Lock()
	delete(b.sessions, msg.Chat.ID)
	b.sessionsMu.Unlock()

	b.sendMessage(msg.Chat.ID, "Session cleared. Fresh start.")
}

func (b *Bot) handleCost(msg *tgbotapi.Message) {
	b.sessionsMu.RLock()
	usage, ok := b.sessions[msg.Chat.ID]
	b.sessionsMu.RUnlock()

	if !ok {
		b.sendMessage(msg.Chat.ID, "No usage recorded for this session yet.")
		return
	}

	duration := time.Since(usage.StartedAt).Round(time.Minute)
	b.sendMessage(msg.Chat.ID, fmt.Sprintf(
		"*Session Cost*\n\n"+
			"Requests: %d\n"+
			"Input tokens: %s\n"+
			"Output tokens: %s\n"+
			"Session duration: %s",
		usage.Requests,
		formatTokens(usage.InputTokens),
		formatTokens(usage.OutputTokens),
		duration.String(),
	))
}

func (b *Bot) handleCronList(msg *tgbotapi.Message) {
	if b.CronStatus != nil {
		b.sendMessage(msg.Chat.ID, b.CronStatus())
		return
	}
	b.sendMessage(msg.Chat.ID, "No cron jobs configured.")
}

func (b *Bot) handleRemember(msg *tgbotapi.Message) {
	if b.MemoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	text := strings.TrimSpace(msg.CommandArguments())
	if text == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /remember <text to remember>")
		return
	}
	id, err := b.MemoryManager.AddCuratedMemory("general", text, "user", 5)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Remembered (#%d). Use /forget %d to remove.", id, id))
}

func (b *Bot) handleForget(msg *tgbotapi.Message) {
	if b.MemoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	idStr := strings.TrimSpace(msg.CommandArguments())
	if idStr == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /forget <id>")
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		b.sendMessage(msg.Chat.ID, "Invalid ID. Use a number.")
		return
	}
	if err := b.MemoryManager.RemoveCuratedMemory(id); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Memory #%d forgotten.", id))
}

func (b *Bot) handleMemories(msg *tgbotapi.Message) {
	if b.MemoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	b.sendLongMessage(msg.Chat.ID, b.MemoryManager.CuratedMemoriesText())
}

func (b *Bot) handleToday(msg *tgbotapi.Message) {
	if b.MemoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	summary, err := b.MemoryManager.GetTodaySummary()
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	if summary == "" {
		b.sendMessage(msg.Chat.ID, "No summary for today yet.")
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("*Today's Summary*\n\n%s", summary))
}

func (b *Bot) handleContext(msg *tgbotapi.Message) {
	if b.MemoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	b.sendLongMessage(msg.Chat.ID, b.MemoryManager.StatusText())
}

func (b *Bot) handleOrchestratorTeam(ctx context.Context, msg *tgbotapi.Message, teamName string) {
	if b.Orchestrator == nil {
		b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
		return
	}
	task := strings.TrimSpace(msg.CommandArguments())
	if task == "" {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Usage: /review_%s <task description>", strings.ReplaceAll(teamName, "-", "_")))
		return
	}

	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Running %s...", teamName))

	result, err := b.Orchestrator.RunTeamStreaming(ctx, teamName, task,
		func(progress string) {
			b.sendMessage(msg.Chat.ID, progress)
		})
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}

	report := orchestrator.FormatConsensusText(teamName, task, result.Consensus, result.Verdicts)
	b.sendLongMessage(msg.Chat.ID, report)
}

func (b *Bot) handleGauntlet(ctx context.Context, msg *tgbotapi.Message) {
	if b.Orchestrator == nil {
		b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
		return
	}
	task := strings.TrimSpace(msg.CommandArguments())
	if task == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /gauntlet <task description>")
		return
	}

	b.sendMessage(msg.Chat.ID, "Running full gauntlet pipeline...")

	results, err := b.Orchestrator.RunGauntlet(ctx, task, "",
		func(progress string) {
			b.sendMessage(msg.Chat.ID, progress)
		})
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Gauntlet error: %v", err))
	}

	for _, result := range results {
		report := orchestrator.FormatConsensusText(result.Team, task, result.Consensus, result.Verdicts)
		b.sendLongMessage(msg.Chat.ID, report)
	}
}

func (b *Bot) handleAgentsList(msg *tgbotapi.Message) {
	b.sendLongMessage(msg.Chat.ID, orchestrator.ListAgentsText())
}

func (b *Bot) handleRunsList(msg *tgbotapi.Message) {
	if b.Orchestrator == nil {
		b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
		return
	}
	b.sendLongMessage(msg.Chat.ID, b.Orchestrator.RecentRunsText(10))
}

func (b *Bot) handleSkills(msg *tgbotapi.Message) {
	if b.SkillsManager == nil {
		b.sendMessage(msg.Chat.ID, "Skills manager not initialized.")
		return
	}
	b.sendMessage(msg.Chat.ID, b.SkillsManager.StatusText())
}

func (b *Bot) handleSkillDetail(msg *tgbotapi.Message) {
	if b.SkillsManager == nil {
		b.sendMessage(msg.Chat.ID, "Skills manager not initialized.")
		return
	}
	name := strings.TrimSpace(msg.CommandArguments())
	if name == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /skill <name>")
		return
	}
	s, ok := b.SkillsManager.GetSkill(name)
	if !ok {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Skill %q not found.", name))
		return
	}
	status := "enabled"
	if !s.Enabled {
		status = "disabled"
	}
	preview := s.Content
	if len(preview) > 500 {
		preview = preview[:500] + "..."
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf(
		"*%s* (%s)\nPrecedence: %d\nSource: %s\n\n%s",
		s.Name, status, s.Precedence, s.SourcePath, preview))
}

func (b *Bot) handleSkillToggle(msg *tgbotapi.Message, enable bool) {
	if b.SkillsManager == nil {
		b.sendMessage(msg.Chat.ID, "Skills manager not initialized.")
		return
	}
	name := strings.TrimSpace(msg.CommandArguments())
	if name == "" {
		action := "enable"
		if !enable {
			action = "disable"
		}
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Usage: /skill_%s <name>", action))
		return
	}
	var err error
	if enable {
		err = b.SkillsManager.EnableSkill(name)
	} else {
		err = b.SkillsManager.DisableSkill(name)
	}
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	action := "enabled"
	if !enable {
		action = "disabled"
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Skill %q %s.", name, action))
}

// --- helpers ---

func (b *Bot) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("Failed to send message to %d: %v", chatID, err)
	}
}

func (b *Bot) sendLongMessage(chatID int64, text string) {
	for i := 0; i < len(text); i += maxTelegramMessage {
		end := i + maxTelegramMessage
		if end > len(text) {
			end = len(text)
		}
		b.sendMessage(chatID, text[i:end])
	}
}

func (b *Bot) recordSessionUsage(chatID int64, usage *agent.Usage) {
	b.sessionsMu.Lock()
	defer b.sessionsMu.Unlock()

	s, ok := b.sessions[chatID]
	if !ok {
		s = &SessionUsage{StartedAt: time.Now()}
		b.sessions[chatID] = s
	}

	s.InputTokens += usage.InputTokens
	s.OutputTokens += usage.OutputTokens
	s.Requests++
}

func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return strconv.Itoa(n)
}
