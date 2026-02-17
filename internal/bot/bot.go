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
	"github.com/you/claude-gateway/internal/ratelimiter"
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
	sessions   map[int64]*SessionUsage
	sessionsMu sync.RWMutex

	// CronStatus is set by the cron scheduler for /cron command
	CronStatus func() string
}

func New(cfg *config.Config, agentClient *agent.Client, limiter *ratelimiter.Limiter) (*Bot, error) {
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
			"👋 Claude Gateway is ready.\n\n"+
				"Send me any message and I'll process it with Claude Code.\n\n"+
				"Commands:\n"+
				"/status — gateway health & rate limits\n"+
				"/reset — clear conversation session\n"+
				"/cost — token usage this session\n"+
				"/cron — active scheduled jobs")

	case "status":
		b.handleStatus(ctx, msg)

	case "reset":
		b.handleReset(ctx, msg)

	case "cost":
		b.handleCost(msg)

	case "cron":
		b.handleCronList(msg)

	default:
		b.sendMessage(msg.Chat.ID, "Unknown command. Try /status, /reset, /cost, or /cron.")
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
			fmt.Sprintf("⏳ Rate limited. Queued at position %d.\nWindow resets in %s.\nHeadroom: %.0f%%",
				position, status.WindowResets, status.Headroom*100))
		return
	}

	b.processMessage(ctx, msg)
}

func (b *Bot) processMessage(ctx context.Context, msg *tgbotapi.Message) {
	// Send "working" indicator
	thinking := tgbotapi.NewMessage(msg.Chat.ID, "⏳ Working...")
	thinkingMsg, err := b.api.Send(thinking)
	if err != nil {
		log.Printf("Failed to send thinking message: %v", err)
	}

	// Create a session ID from the chat ID
	sessionID := fmt.Sprintf("tg-%d", msg.Chat.ID)

	// Call Claude via sidecar
	resp, err := b.agent.Ask(ctx, sessionID, msg.Text, b.cfg.WorkDir, 0)

	// Delete "working" message
	if thinkingMsg.MessageID != 0 {
		del := tgbotapi.NewDeleteMessage(msg.Chat.ID, thinkingMsg.MessageID)
		b.api.Send(del)
	}

	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("❌ Error: %v", err))
		return
	}

	// Record usage
	if resp.Usage != nil {
		b.limiter.Record(resp.Usage)
		b.recordSessionUsage(msg.Chat.ID, resp.Usage)
	}

	// Send response, splitting if needed
	text := resp.Text
	if text == "" {
		text = "✅ Done (no text output)."
	}

	b.sendLongMessage(msg.Chat.ID, text)
}

func (b *Bot) handleStatus(ctx context.Context, msg *tgbotapi.Message) {
	rl := b.limiter.GetStatus()
	sidecarStatus := "✅ Healthy"
	if err := b.agent.Health(ctx); err != nil {
		sidecarStatus = fmt.Sprintf("❌ %v", err)
	}

	lines := []string{
		"🤖 *Gateway Status*",
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
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("❌ Reset failed: %v", err))
		return
	}

	b.sessionsMu.Lock()
	delete(b.sessions, msg.Chat.ID)
	b.sessionsMu.Unlock()

	b.sendMessage(msg.Chat.ID, "🔄 Session cleared. Fresh start.")
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
		"💰 *Session Cost*\n\n"+
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
