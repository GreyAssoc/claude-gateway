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
	"github.com/you/claude-gateway/internal/format"
	"github.com/you/claude-gateway/internal/orchestrator"
	"github.com/you/claude-gateway/internal/pgdb"
	"github.com/you/claude-gateway/internal/skills"
)

func (b *Bot) handleCommand(ctx context.Context, msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		text := "Claude Gateway v2 is ready.\n\n" +
			"Send me any message and I'll process it with Claude Code.\n\n" +
			"Commands:\n" +
			"/bootstrapset — add/update bootstrap instruction\n" +
			"/contactadd — add contact detail to person\n" +
			"/context — current context layers\n" +
			"/cost — token usage this session\n" +
			"/cronadd name \"schedule\" prompt — add a cron job\n" +
			"/crondel name — remove a cron job\n" +
			"/crondisable name — disable a cron job\n" +
			"/cronenable name — enable a cron job\n" +
			"/cronlist — list scheduled jobs\n" +
			"/dbcheck — check PostgreSQL conversations\n" +
			"/framework [name] — show/switch AI framework\n" +
			"/journal — view daily journal\n" +
			"/journalsearch — search journal entries\n" +
			"/memorieslist — list curated memories\n" +
			"/model — show actual API model in use\n" +
			"/memoryforget — remove a memory by text match\n" +
			"/memorylist — list memories by category\n" +
			"/memoryremember <text> — save a memory\n" +
			"/peoplelist — list all people\n" +
			"/personadd — add person to contacts\n" +
			"/persondel — remove a person\n" +
			"/personfind — find person by name\n" +
			"/profile — view user profile\n" +
			"/profiledel — delete profile aspect\n" +
			"/profileset — set profile aspect\n" +
			"/reset — clear conversation session\n" +
			"/reviewauto [dir] — auto-detect language and review\n" +
			"/reviewautosec [dir] — auto-detect with security focus\n" +
			"/skills — list loaded skills\n" +
			"/status — gateway health & rate limits\n" +
			"/stop — stop the current request"

		if b.skillsManager != nil {
			var cmds []string
			for _, s := range b.skillsManager.GetActiveSkills() {
				if s.Command != "" {
					desc := s.Description
					if desc == "" {
						desc = s.Name
					}
					cmds = append(cmds, fmt.Sprintf("/%s — %s", s.Command, desc))
				}
			}
			if len(cmds) > 0 {
				text += "\n\nSkill Commands:\n" + strings.Join(cmds, "\n")
			}
		}
		b.sendMessage(msg.Chat.ID, text)

	case "bootstrapset":
		b.handleSetBootstrap(ctx, msg)
	case "contactadd":
		b.handleAddContact(ctx, msg)
	case "context":
		b.handleContext(ctx, msg)
	case "cost":
		b.handleCost(msg)
	case "cronadd":
		b.handleCronAdd(msg)
	case "crondel":
		b.handleCronDel(msg)
	case "crondisable":
		b.handleCronDisable(msg)
	case "cronenable":
		b.handleCronEnable(msg)
	case "cronlist":
		b.handleCronList(msg)
	case "dbcheck":
		b.handleDBCheck(ctx, msg)
	case "framework":
		b.handleFramework(ctx, msg)
	case "gauntlet":
		b.handleGauntlet(ctx, msg)
	case "journal":
		b.handleJournal(ctx, msg)
	case "journalsearch":
		b.handleJournalSearch(ctx, msg)
	case "memorieslist":
		b.handleMemories(ctx, msg)
	case "model":
		b.handleModel(msg)
	case "memoryforget":
		b.handleForget(ctx, msg)
	case "memorylist":
		b.handleListMemory(ctx, msg)
	case "memoryremember":
		b.handleRemember(ctx, msg)
	case "orchestratoragents":
		b.handleAgentsList(msg)
	case "orchestratorruns":
		b.handleRunsList(ctx, msg)
	case "peoplelist":
		b.handleListPeople(ctx, msg)
	case "personadd":
		b.handleAddPerson(ctx, msg)
	case "persondel":
		b.handleDelPerson(ctx, msg)
	case "personfind":
		b.handleFindPerson(ctx, msg)
	case "profile":
		b.handleProfile(ctx, msg)
	case "profiledel":
		b.handleDelProfile(ctx, msg)
	case "profileset":
		b.handleSetProfile(ctx, msg)
	case "reset":
		b.handleReset(ctx, msg)
	case "reviewauto":
		b.handleAutoReview(ctx, msg, false)
	case "reviewautosec":
		b.handleAutoReview(ctx, msg, true)
	case "reviewdesign":
		b.handleOrchestratorTeam(ctx, msg, "design")
	case "reviewfe":
		b.handleOrchestratorTeam(ctx, msg, "fe-dev-team")
	case "reviewfesec":
		b.handleOrchestratorTeam(ctx, msg, "fe-sec-team")
	case "reviewgo":
		b.handleOrchestratorTeam(ctx, msg, "go-dev-team")
	case "reviewgosec":
		b.handleOrchestratorTeam(ctx, msg, "go-sec-team")
	case "reviewintegration":
		b.handleOrchestratorTeam(ctx, msg, "integration")
	case "reviewpython":
		b.handleOrchestratorTeam(ctx, msg, "python-dev-team")
	case "reviewpythonsec":
		b.handleOrchestratorTeam(ctx, msg, "python-sec-team")
	case "skills":
		b.handleSkills(msg)
	case "skilldetail":
		b.handleSkillDetail(msg)
	case "skillenable":
		b.handleSkillToggle(ctx, msg, true)
	case "skilldisable":
		b.handleSkillToggle(ctx, msg, false)
	case "status":
		b.handleStatus(ctx, msg)
	case "stop":
		b.handleStop(msg)
	case "today":
		b.handleToday(ctx, msg)

	default:
		// Check if this is a skill command
		if b.skillsManager != nil {
			if skill, ok := b.skillsManager.GetSkillByCommand(msg.Command()); ok {
				b.handleSkillCommand(ctx, msg, skill)
				return
			}
		}
		b.sendMessage(msg.Chat.ID, "Unknown command. Try /start for available commands.")
	}
}

// gatewayIntentMatch checks if a natural language message is asking about
// something the gateway can answer directly (cron jobs, people, profile, etc.).
// Returns true if handled, false if the message should proceed to Claude.
// This prevents Claude from wasting tokens searching the filesystem for
// gateway-internal data it has no access to.
func (b *Bot) gatewayIntentMatch(ctx context.Context, msg *tgbotapi.Message) bool {
	lower := strings.ToLower(strings.TrimSpace(msg.Text))

	// Cron/scheduled jobs
	if containsAny(lower, "cron job", "cron list", "scheduled job", "list cron", "show cron",
		"list all cron", "my cron", "active cron", "scheduled task", "list scheduled") {
		b.handleCronList(msg)
		return true
	}

	// People/contacts
	if containsAny(lower, "list people", "list contacts", "show contacts", "my contacts",
		"show people", "who do i know", "list all people", "all contacts") {
		b.handleListPeople(ctx, msg)
		return true
	}

	// Profile
	if containsAny(lower, "show my profile", "my profile", "view profile", "show profile",
		"what do you know about me", "user profile") {
		b.handleProfile(ctx, msg)
		return true
	}

	// Memories
	if containsAny(lower, "list memories", "show memories", "my memories", "all memories",
		"what do you remember", "list all memories") {
		b.handleMemories(ctx, msg)
		return true
	}

	// Journal
	if containsAny(lower, "today's journal", "todays journal", "show journal", "daily journal",
		"journal entry", "view journal") && !strings.Contains(lower, "search") {
		b.handleJournal(ctx, msg)
		return true
	}

	// Status
	if containsAny(lower, "gateway status", "system status", "show status", "health check",
		"rate limit", "rate limits") {
		b.handleStatus(ctx, msg)
		return true
	}

	// Framework switching
	if b.matchFrameworkSwitch(ctx, msg, lower) {
		return true
	}

	return false
}

// matchFrameworkSwitch detects natural language requests to switch or query
// the AI framework. Returns true if handled.
func (b *Bot) matchFrameworkSwitch(_ context.Context, msg *tgbotapi.Message, lower string) bool {
	// Query: "which framework", "what framework", "current framework", "what backend"
	if containsAny(lower, "which framework", "what framework", "current framework",
		"what backend", "which backend", "active framework", "active backend",
		"which model", "what model are you") {
		b.handleFramework(context.Background(), msg)
		return true
	}

	// Switch: detect intent + target
	if !containsAny(lower, "switch", "use ", "change to", "swap to", "move to",
		"enable opencode", "enable claude", "activate opencode", "activate claude",
		"switch to opencode", "switch to claude") {
		return false
	}

	// Extract target framework from the message
	target := ""
	switch {
	case containsAny(lower, "opencode", "open code", "open-code", "zen"):
		target = "opencode"
	case containsAny(lower, "claude", "claude code", "sidecar"):
		target = "claude"
	}

	if target == "" {
		// They said "switch framework" but didn't specify which — show the menu
		b.handleFramework(context.Background(), msg)
		return true
	}

	// Synthesize a command message with the target as argument
	// so handleFramework can process it.
	switcher, ok := b.agent.(*agent.Switcher)
	if !ok {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Framework switching not available.\nActive backend: *%s*", b.agent.Name()))
		return true
	}

	if switcher.Active() == target {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Already using *%s*.", target))
		return true
	}

	if err := switcher.Switch(target); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed: %v", err))
		return true
	}

	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Switched to *%s*.", target))
	return true
}

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func (b *Bot) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	if msg.Text == "" {
		return
	}

	// Intercept "stop" as plain text while a request is active.
	if strings.EqualFold(strings.TrimSpace(msg.Text), "stop") {
		b.activeReqsMu.Lock()
		_, active := b.activeReqs[msg.Chat.ID]
		b.activeReqsMu.Unlock()
		if active {
			b.handleStop(msg)
			return
		}
	}

	// Step 0: Intercept natural language requests about gateway features.
	// These are things Claude cannot answer (no access to cron scheduler,
	// people DB, etc.) so we handle them directly.
	if b.gatewayIntentMatch(ctx, msg) {
		return
	}

	// People detection: extract known names from message text for context enrichment.
	// This replaces the triage LLM layer with a fast in-process lookup.
	var peopleNames []string
	if b.peopleManager != nil {
		if names, err := b.peopleManager.MatchNames(ctx, msg.Text); err == nil {
			peopleNames = names
		}
	}

	// Check rate limits (estimate ~1000 input tokens for a typical request)
	if !b.limiter.CanProceed(1000) {
		status := b.limiter.GetStatus()
		position := b.limiter.Enqueue(func() {
			// Use Background: queued requests have independent lifecycles
			// and should not be cancelled by the original handler's context.
			b.processMessageWith(context.Background(), msg, msg.Text, "", peopleNames)
		})
		if position < 0 {
			b.sendMessage(msg.Chat.ID,
				fmt.Sprintf("Rate limited. Queue full, please try again later.\nWindow resets in %s.",
					status.WindowResets))
		} else {
			b.sendMessage(msg.Chat.ID,
				fmt.Sprintf("Rate limited. Queued at position %d.\nWindow resets in %s.\nHeadroom: %.0f%%",
					position, status.WindowResets, status.Headroom*100))
		}
		return
	}

	b.processMessageWith(ctx, msg, msg.Text, "", peopleNames)
}

// processMessageWith sends a message to Claude with streaming progress.
// When systemPrompt is non-empty, it overrides the default system prompt builder.
// peopleNames are used for enriched context (people profile injection).
func (b *Bot) processMessageWith(ctx context.Context, msg *tgbotapi.Message, prompt, systemPrompt string, peopleNames []string) {
	// Send "Working..." indicator — we'll edit this message in-place with progress
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
		Prompt:    prompt,
		WorkDir:   b.cfg.WorkDir,
		Model:     b.cfg.Sidecar.DefaultModel,
	}

	// System prompt injection:
	// - If an explicit override is provided (skill commands), always use it.
	// - Otherwise, check if the SDK session is alive in the sidecar.
	//   If dead/new, build system prompt from PostgreSQL (or legacy memory manager).
	needsSystemPrompt := true // tracks whether session is new (used for enriched prompt)
	if systemPrompt != "" {
		opts.SystemPrompt = systemPrompt
		// Explicit system prompt = fresh session context
	} else {
		status, checkErr := b.agent.CheckSession(ctx, sessionID)
		if checkErr == nil && status.Alive && status.IdleSeconds < 1800 {
			needsSystemPrompt = false
		}

		if needsSystemPrompt {
			// Reset the sidecar's in-memory session so it does NOT try to
			// resume the expired SDK session. Without this, the SDK ignores
			// the new system prompt (which contains conversation history)
			// and Claude loses all prior context.
			if checkErr == nil && status.Alive {
				resetCtx, resetCancel := context.WithTimeout(ctx, 5*time.Second)
				if err := b.agent.ResetSession(resetCtx, sessionID); err != nil {
					log.Printf("Failed to reset stale session %s: %v", sessionID, err)
				} else {
					log.Printf("Reset stale session %s (idle %ds)", sessionID, status.IdleSeconds)
				}
				resetCancel()
			}
			// Build system prompt from PostgreSQL if available.
			// Use a dedicated timeout so a hanging DB doesn't block indefinitely.
			if b.contextBuilder != nil {
				buildCtx, buildCancel := context.WithTimeout(ctx, 10*time.Second)
				sp, err := b.contextBuilder.BuildSystemPrompt(buildCtx)
				buildCancel()
				if err != nil {
					log.Printf("System prompt build failed: %v", err)
				} else if sp != "" {
					opts.SystemPrompt = sp
					log.Printf("Built system prompt from PostgreSQL (%d chars)", len(sp))
				}
			}
			// Append skill index (lightweight — names + descriptions only).
			// Full skill content is injected per-message via BuildMatchedSkillsSection.
			if b.skillsManager != nil {
				if skillSection := b.skillsManager.BuildSkillIndex(); skillSection != "" {
					if opts.SystemPrompt != "" {
						opts.SystemPrompt += "\n\n" + skillSection
					} else {
						opts.SystemPrompt = skillSection
					}
				}
			}
			// Fallback to legacy memory manager if no contextBuilder
			if opts.SystemPrompt == "" && b.systemPromptBuilder != nil {
				opts.SystemPrompt = b.systemPromptBuilder(ctx)
			}
		}
	}

	// Enrich user prompt with per-message context (recent turns, semantic hits, people, facts).
	// Skip for skill commands which have their own focused context.
	// Bootstrap is only injected on new sessions — alive sessions already have it
	// in the system prompt and the SDK manages context retention.
	sessionAlive := !needsSystemPrompt
	if b.contextBuilder != nil && systemPrompt == "" {
		enrichCtx, enrichCancel := context.WithTimeout(ctx, 10*time.Second)
		enriched, enrichErr := b.contextBuilder.BuildEnrichedPrompt(enrichCtx, sessionID, prompt, peopleNames, sessionAlive)
		enrichCancel()
		if enrichErr != nil {
			log.Printf("Enriched prompt build failed: %v", enrichErr)
		} else if enriched != "" {
			// Append matched skills to enriched prompt — only skills relevant
			// to this message get their full content injected.
			if b.skillsManager != nil {
				if skillSection := b.skillsManager.BuildMatchedSkillsSection(prompt); skillSection != "" {
					enriched += "\n\n" + skillSection
				}
			}
			opts.Prompt = enriched + "\n\n---\n\n" + prompt
		}
	}

	// Per-request timeout: 30 minutes balances complex multi-file tasks
	// against semaphore slot exhaustion from stuck requests.
	reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Minute)
	defer reqCancel()

	// Register cancel so /stop can interrupt this request.
	b.activeReqsMu.Lock()
	b.activeReqs[msg.Chat.ID] = reqCancel
	b.activeReqsMu.Unlock()
	defer func() {
		b.activeReqsMu.Lock()
		delete(b.activeReqs, msg.Chat.ID)
		b.activeReqsMu.Unlock()
	}()

	// Rolling progress state — mutex-protected for concurrent access
	// between the onProgress callback and the flush goroutine.
	var mu sync.Mutex
	var progressLines []string   // tool activity log
	var responsePreview strings.Builder // accumulates Claude's response text
	var editPending bool
	startTime := time.Now()
	lastEdit := time.Time{}

	// editProgress updates the "Working..." message with the latest state.
	// When response text is streaming, shows it as a live preview so the
	// user sees Claude "typing". Otherwise shows tool activity.
	editProgress := func() {
		mu.Lock()
		lines := progressLines
		nActions := len(progressLines)
		if len(lines) > 15 {
			lines = lines[len(lines)-15:]
		}
		preview := responsePreview.String()
		editPending = false
		lastEdit = time.Now()
		mu.Unlock()

		elapsed := time.Since(startTime).Truncate(time.Second)

		var text string
		if preview != "" {
			// Response text is flowing — show it as a live preview.
			// Truncate from the front to show the most recent text, keeping
			// within Telegram's message limit.
			const maxPreview = maxTelegramMessage - 100
			if len(preview) > maxPreview {
				preview = "..." + preview[len(preview)-maxPreview:]
			}
			text = fmt.Sprintf("Responding... %s | %d actions\n\n%s", elapsed, nActions, preview)
		} else if nActions > 0 {
			text = fmt.Sprintf("Working... %s (%d actions)\n\n%s", elapsed, nActions, strings.Join(lines, "\n"))
		} else {
			text = fmt.Sprintf("Working... %s", elapsed)
		}

		if thinkingMsg.MessageID != 0 {
			edit := tgbotapi.NewEditMessageText(msg.Chat.ID, thinkingMsg.MessageID, text)
			if _, err := b.api.Send(edit); err != nil {
				log.Printf("Failed to edit progress message: %v", err)
			}
		}
	}

	// Flush goroutine: ticks every 3 seconds to flush pending edits.
	// Also forces an edit every 30s even without new events, so the
	// elapsed timer stays up to date and the user knows it's alive.
	flushDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		editsWithoutProgress := 0
		for {
			select {
			case <-flushDone:
				return
			case <-reqCtx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				pending := editPending
				mu.Unlock()
				if pending {
					editProgress()
					editsWithoutProgress = 0
				} else {
					editsWithoutProgress++
					// Every 15s (5 ticks × 3s), update elapsed time regardless
					if editsWithoutProgress >= 5 {
						editProgress()
						editsWithoutProgress = 0
					}
				}
			}
		}
	}()

	// onProgress callback — called for each streaming progress event.
	// Separates tool activity (shown as action log) from response text
	// (shown as live preview).
	onProgress := func(ev agent.StreamEvent) {
		mu.Lock()
		if ev.Tool != "" {
			icon := toolIcon(ev.Tool)
			progressLines = append(progressLines, fmt.Sprintf("%s %s: %s", icon, ev.Tool, ev.Input))
		} else if ev.Text != "" {
			responsePreview.WriteString(ev.Text)
			responsePreview.WriteByte('\n')
		} else {
			mu.Unlock()
			return
		}

		sinceLastEdit := time.Since(lastEdit)
		mu.Unlock()

		// Edit immediately if 3+ seconds since last edit, otherwise mark pending
		if sinceLastEdit >= 3*time.Second {
			editProgress()
		} else {
			mu.Lock()
			editPending = true
			mu.Unlock()
		}
	}

	// Call Claude via sidecar with streaming
	resp, err := b.agent.AskStreaming(reqCtx, opts, onProgress)

	// Stop flush goroutine
	close(flushDone)

	// Delete progress message (best-effort)
	if thinkingMsg.MessageID != 0 {
		del := tgbotapi.NewDeleteMessage(msg.Chat.ID, thinkingMsg.MessageID)
		if _, err := b.api.Send(del); err != nil {
			log.Printf("Failed to delete progress message: %v", err)
		}
	}

	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}

	// Track the API-reported model for /model command
	if resp.Model != "" {
		b.lastAPIModelMu.Lock()
		b.lastAPIModel[msg.Chat.ID] = resp.Model
		b.lastAPIModelMu.Unlock()
	}

	// Record usage
	if resp.Usage != nil {
		b.limiter.Record(resp.Usage)
		b.recordSessionUsage(msg.Chat.ID, resp.Usage)

		// Record to DB with a dedicated timeout so queued requests
		// (whose parent ctx is context.Background()) don't block indefinitely.
		if b.database != nil {
			dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer dbCancel()
			if dbErr := b.database.RecordSession(dbCtx,
				sessionID, "user", msg.Text,
				resp.Usage.InputTokens, resp.Usage.OutputTokens,
				resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens,
			); dbErr != nil {
				log.Printf("Failed to record session: %v", dbErr)
			}
		}
	}

	// Send response, splitting if needed
	text := resp.Text
	if text == "" {
		text = "Done (no text output)."
	}

	b.sendLongMessage(msg.Chat.ID, text)

	// Post-process: record exchange to PostgreSQL for persistent memory.
	// Save synchronously (fast DB inserts, no embedding) so the next
	// message's BuildEnrichedPrompt always sees these turns. Embedding
	// generation (slow, Ollama) runs in the background afterward.
	if b.contextBuilder != nil {
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		ids, saveErr := b.contextBuilder.RecordExchangeSync(saveCtx, sessionID, prompt, resp.Text)
		saveCancel()
		if saveErr != nil {
			log.Printf("Failed to save exchange: %v", saveErr)
		} else {
			// Enrich with embeddings in background — non-blocking
			b.bgWg.Add(1)
			go func() {
				defer b.bgWg.Done()
				bgCtx, bgCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer bgCancel()
				b.contextBuilder.EnrichWithEmbeddings(bgCtx, ids, prompt, resp.Text)
			}()
		}
	}

	// Post-process: update user profile observations (background, non-blocking)
	if b.profiler != nil && systemPrompt == "" {
		b.bgWg.Add(1)
		go func(userMsg, assistantResp string) {
			defer b.bgWg.Done()
			bgCtx, bgCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer bgCancel()
			if profErr := b.profiler.UpdateFromConversation(bgCtx, userMsg, assistantResp); profErr != nil {
				log.Printf("Profile update failed (non-fatal): %v", profErr)
			}
		}(prompt, resp.Text)
	}
}

// toolIcon returns an emoji icon for a Claude tool name.
func toolIcon(tool string) string {
	switch tool {
	case "Read":
		return "📖"
	case "Write":
		return "✏️"
	case "Edit":
		return "📝"
	case "Bash":
		return "⚡"
	case "Glob":
		return "🔍"
	case "Grep":
		return "🔎"
	case "WebFetch":
		return "🌐"
	default:
		if strings.HasPrefix(tool, "mcp__") {
			return "🔌"
		}
		return "🔧"
	}
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
	if _, err := b.api.Send(reply); err != nil {
		log.Printf("Failed to send status message: %v", err)
	}
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

func (b *Bot) handleStop(msg *tgbotapi.Message) {
	b.activeReqsMu.Lock()
	cancel, ok := b.activeReqs[msg.Chat.ID]
	b.activeReqsMu.Unlock()

	if !ok {
		b.sendMessage(msg.Chat.ID, "Nothing running to stop.")
		return
	}

	// Cancel the context — AskStreaming will return ctx.Err()
	cancel()

	// Also tell the sidecar to abort the SDK session
	sessionID := fmt.Sprintf("tg-%d", msg.Chat.ID)
	b.agent.AbortSession(sessionID)

	b.sendMessage(msg.Chat.ID, "Stopped.")
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
	b.sendMarkdownMessage(msg.Chat.ID, fmt.Sprintf(
		"*Session Cost*\n\n"+
			"Requests: %d\n"+
			"Input tokens: %s\n"+
			"Output tokens: %s\n"+
			"Session duration: %s",
		usage.Requests,
		format.Tokens(usage.InputTokens),
		format.Tokens(usage.OutputTokens),
		duration.String(),
	))
}

func (b *Bot) handleCronList(msg *tgbotapi.Message) {
	if b.cronStatus != nil {
		b.sendMessage(msg.Chat.ID, b.cronStatus())
		return
	}
	b.sendMessage(msg.Chat.ID, "No cron jobs configured.")
}

func (b *Bot) handleRemember(ctx context.Context, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.CommandArguments())
	if text == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /remember <text to remember>")
		return
	}

	// Write to PostgreSQL if available
	if b.contextBuilder != nil && b.pgDB != nil {
		pgDB := b.pgDB
		if _, err := pgDB.SaveMemoryEntry(ctx, "fact", "", text, "user", 5); err != nil {
			b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
			return
		}
		b.sendMessage(msg.Chat.ID, "Remembered. Use /forget <text> to remove.")
		return
	}

	// Fallback to SQLite
	if b.memoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	id, err := b.memoryManager.AddCuratedMemory(ctx, "general", text, "user", 5)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Remembered (#%d). Use /forget %d to remove.", id, id))
}

func (b *Bot) handleForget(ctx context.Context, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.CommandArguments())
	if text == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /forget <text fragment to match>")
		return
	}

	// PostgreSQL path: match by content fragment (ILIKE)
	if b.contextBuilder != nil && b.pgDB != nil {
		pgDB := b.pgDB
		n, err := pgDB.DeactivateByContent(ctx, text)
		if err != nil {
			b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
			return
		}
		if n == 0 {
			b.sendMessage(msg.Chat.ID, "No matching memories found.")
			return
		}
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Deactivated %d matching memory entries.", n))
		return
	}

	// Fallback to SQLite (ID-based)
	if b.memoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	id, err := strconv.Atoi(text)
	if err != nil || id <= 0 {
		b.sendMessage(msg.Chat.ID, "Invalid ID. Use a positive number.")
		return
	}
	if err := b.memoryManager.RemoveCuratedMemory(ctx, id); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Memory #%d forgotten.", id))
}

func (b *Bot) handleMemories(ctx context.Context, msg *tgbotapi.Message) {
	// PostgreSQL path
	if b.contextBuilder != nil && b.pgDB != nil {
		pgDB := b.pgDB
		entries, err := pgDB.GetActiveMemory(ctx)
		if err != nil {
			b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
			return
		}
		if len(entries) == 0 {
			b.sendMessage(msg.Chat.ID, "No active memories.")
			return
		}
		b.sendLongMessage(msg.Chat.ID, formatMemoryEntries("Active memories", entries))
		return
	}

	// Fallback to SQLite
	if b.memoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	b.sendLongMessage(msg.Chat.ID, b.memoryManager.CuratedMemoriesText(ctx))
}

func (b *Bot) handleToday(ctx context.Context, msg *tgbotapi.Message) {
	if b.memoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	summary, err := b.memoryManager.GetTodaySummary(ctx)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	if summary == "" {
		b.sendMessage(msg.Chat.ID, "No summary for today yet.")
		return
	}
	b.sendMarkdownMessage(msg.Chat.ID, fmt.Sprintf("*Today's Summary*\n\n%s", summary))
}

func (b *Bot) handleContext(ctx context.Context, msg *tgbotapi.Message) {
	if b.memoryManager == nil {
		b.sendMessage(msg.Chat.ID, "Memory manager not initialized.")
		return
	}
	b.sendLongMessage(msg.Chat.ID, b.memoryManager.StatusText(ctx))
}

func (b *Bot) handleOrchestratorTeam(ctx context.Context, msg *tgbotapi.Message, teamName string) {
	if b.orchestrator == nil {
		b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
		return
	}
	task := strings.TrimSpace(msg.CommandArguments())
	if task == "" {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Usage: /review_%s <task description>", strings.ReplaceAll(teamName, "-", "_")))
		return
	}

	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Running %s...", teamName))

	result, err := b.orchestrator.RunTeamStreaming(ctx, teamName, task,
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
	if b.orchestrator == nil {
		b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
		return
	}
	task := strings.TrimSpace(msg.CommandArguments())
	if task == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /gauntlet <task description>")
		return
	}

	b.sendMessage(msg.Chat.ID, "Running full gauntlet pipeline...")

	results, err := b.orchestrator.RunGauntlet(ctx, task, "",
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

func (b *Bot) handleAutoReview(ctx context.Context, msg *tgbotapi.Message, securityFocus bool) {
	if b.orchestrator == nil {
		b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
		return
	}

	projectDir := strings.TrimSpace(msg.CommandArguments())
	if projectDir == "" {
		projectDir = "/workspace" // default
	}

	b.sendMessage(msg.Chat.ID, fmt.Sprintf("🔍 Detecting language in %s...", projectDir))

	// Detect language
	stats, err := orchestrator.DetectProjectLanguages(projectDir)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error detecting language: %v", err))
		return
	}

	primary := stats.PrimaryLanguage()
	if primary == "unknown" {
		b.sendMessage(msg.Chat.ID, "❌ Could not detect project language. Please specify a team manually.")
		return
	}

	allLangs := stats.AllLanguages()
	teamName := orchestrator.GetReviewTeamForLanguage(primary, securityFocus)

	focusType := "development"
	if securityFocus {
		focusType = "security"
	}

	b.sendMessage(msg.Chat.ID, fmt.Sprintf(
		"📊 Detected languages: %s\n"+
			"🎯 Primary: %s\n"+
			"👥 Using team: %s (%s focus)",
		strings.Join(allLangs, ", "),
		primary,
		teamName,
		focusType,
	))

	task := fmt.Sprintf("Review the code in %s", projectDir)

	result, err := b.orchestrator.RunTeamStreaming(ctx, teamName, task,
		func(progress string) {
			b.sendMessage(msg.Chat.ID, progress)
		})
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("❌ Error: %v", err))
		return
	}

	report := orchestrator.FormatConsensusText(teamName, task, result.Consensus, result.Verdicts)
	b.sendLongMessage(msg.Chat.ID, report)
}

func (b *Bot) handleAgentsList(msg *tgbotapi.Message) {
	b.sendLongMessage(msg.Chat.ID, orchestrator.ListAgentsText())
}

func (b *Bot) handleRunsList(ctx context.Context, msg *tgbotapi.Message) {
	if b.orchestrator == nil {
		b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
		return
	}
	b.sendLongMessage(msg.Chat.ID, b.orchestrator.RecentRunsText(ctx, 10))
}

func (b *Bot) handleSkills(msg *tgbotapi.Message) {
	if b.skillsManager == nil {
		b.sendMessage(msg.Chat.ID, "Skills manager not initialized.")
		return
	}
	b.sendMessage(msg.Chat.ID, b.skillsManager.StatusText())
}

func (b *Bot) handleSkillDetail(msg *tgbotapi.Message) {
	if b.skillsManager == nil {
		b.sendMessage(msg.Chat.ID, "Skills manager not initialized.")
		return
	}
	name := strings.TrimSpace(msg.CommandArguments())
	if name == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /skill <name>")
		return
	}
	s, ok := b.skillsManager.GetSkill(name)
	if !ok {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Skill %q not found.", name))
		return
	}
	status := "enabled"
	if !s.Enabled {
		status = "disabled"
	}
	preview := format.Truncate(s.Content, 500)
	b.sendMarkdownMessage(msg.Chat.ID, fmt.Sprintf(
		"*%s* (%s)\nPrecedence: %d\nSource: %s\n\n%s",
		s.Name, status, s.Precedence, s.SourcePath, preview))
}

func (b *Bot) handleSkillToggle(ctx context.Context, msg *tgbotapi.Message, enable bool) {
	if b.skillsManager == nil {
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
		err = b.skillsManager.EnableSkill(ctx, name)
	} else {
		err = b.skillsManager.DisableSkill(ctx, name)
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

// handleSkillCommand handles a Telegram /command that maps to a skill.
// It resets the session, injects the skill's content as the system prompt
// (alongside memory context), and sends the command arguments as the user's prompt.
func (b *Bot) handleSkillCommand(ctx context.Context, msg *tgbotapi.Message, skill skills.Skill) {
	args := strings.TrimSpace(msg.CommandArguments())
	if args == "" {
		desc := skill.Description
		if desc == "" {
			desc = skill.Name
		}
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Usage: /%s <details>\n\n%s", skill.Command, desc))
		return
	}

	// Reset session so the skill gets a fresh, focused context.
	sessionID := fmt.Sprintf("tg-%d", msg.Chat.ID)
	_ = b.agent.ResetSession(ctx, sessionID)
	b.sessionsMu.Lock()
	delete(b.sessions, msg.Chat.ID)
	b.sessionsMu.Unlock()

	// Build system prompt: memory context (bootstrap, CLAUDE.md, curated, daily)
	// plus ONLY this skill — not all active skills.
	var parts []string
	if b.memoryManager != nil {
		if section := b.memoryManager.BuildSystemPromptSection(ctx); section != "" {
			parts = append(parts, section)
		}
	}
	parts = append(parts, skills.FormatSkillsXML([]skills.Skill{skill}))
	systemPrompt := strings.Join(parts, "\n\n")

	// Rate limit check
	if !b.limiter.CanProceed(1000) {
		status := b.limiter.GetStatus()
		b.sendMessage(msg.Chat.ID,
			fmt.Sprintf("Rate limited. Window resets in %s.", status.WindowResets))
		return
	}

	b.processMessageWith(ctx, msg, args, systemPrompt, nil)
}

// --- v2 command handlers ---

func (b *Bot) handleAddPerson(ctx context.Context, msg *tgbotapi.Message) {
	if b.peopleManager == nil {
		b.sendMessage(msg.Chat.ID, "People manager not initialized.")
		return
	}
	args := strings.TrimSpace(msg.CommandArguments())
	if args == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /addperson Name | preferred name | type | role | company | notes")
		return
	}
	parts := strings.SplitN(args, "|", 6)
	name := strings.TrimSpace(parts[0])
	if name == "" {
		b.sendMessage(msg.Chat.ID, "Name is required.")
		return
	}
	var preferredName, pType, role, company, notes string
	if len(parts) > 1 {
		preferredName = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		pType = strings.TrimSpace(parts[2])
	}
	if len(parts) > 3 {
		role = strings.TrimSpace(parts[3])
	}
	if len(parts) > 4 {
		company = strings.TrimSpace(parts[4])
	}
	if len(parts) > 5 {
		notes = strings.TrimSpace(parts[5])
	}

	person, err := b.peopleManager.AddPerson(ctx, name, preferredName, pType, role, company, notes)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Added: %s (ID: %d)", person.FullName, person.ID))
}

func (b *Bot) handleListPeople(ctx context.Context, msg *tgbotapi.Message) {
	if b.peopleManager == nil {
		b.sendMessage(msg.Chat.ID, "People manager not initialized.")
		return
	}
	people, err := b.peopleManager.ListPeople(ctx)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	if len(people) == 0 {
		b.sendMessage(msg.Chat.ID, "No people in contacts.")
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("People (%d):\n\n", len(people)))
	for _, p := range people {
		sb.WriteString(fmt.Sprintf("#%d %s", p.ID, p.FullName))
		if p.Role != "" {
			sb.WriteString(fmt.Sprintf(" — %s", p.Role))
		}
		if p.Company != "" {
			sb.WriteString(fmt.Sprintf(" @ %s", p.Company))
		}
		sb.WriteByte('\n')
	}
	b.sendLongMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) handleFindPerson(ctx context.Context, msg *tgbotapi.Message) {
	if b.peopleManager == nil {
		b.sendMessage(msg.Chat.ID, "People manager not initialized.")
		return
	}
	name := strings.TrimSpace(msg.CommandArguments())
	if name == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /person <name>")
		return
	}
	person, err := b.peopleManager.FindPerson(ctx, name)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	if person == nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("No person found matching %q.", name))
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s (ID: %d)\n", person.FullName, person.ID))
	if person.PreferredName != "" {
		sb.WriteString(fmt.Sprintf("Known as: %s\n", person.PreferredName))
	}
	if person.Type != "" {
		sb.WriteString(fmt.Sprintf("Type: %s\n", person.Type))
	}
	if person.Role != "" {
		sb.WriteString(fmt.Sprintf("Role: %s\n", person.Role))
	}
	if person.Company != "" {
		sb.WriteString(fmt.Sprintf("Company: %s\n", person.Company))
	}
	if person.Notes != "" {
		sb.WriteString(fmt.Sprintf("Notes: %s\n", person.Notes))
	}
	for _, c := range person.Contacts {
		line := fmt.Sprintf("  %s: %s", c.ContactType, c.Value)
		if c.Label != "" {
			line += fmt.Sprintf(" (%s)", c.Label)
		}
		sb.WriteString(line + "\n")
	}
	b.sendLongMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) handleAddContact(ctx context.Context, msg *tgbotapi.Message) {
	if b.peopleManager == nil {
		b.sendMessage(msg.Chat.ID, "People manager not initialized.")
		return
	}
	args := strings.TrimSpace(msg.CommandArguments())
	// Expected: name | type | value [| label]
	// Uses pipe separators to support multi-word names.
	parts := strings.SplitN(args, "|", 4)
	if len(parts) < 3 {
		b.sendMessage(msg.Chat.ID, "Usage: /addcontact name | type | value [| label]")
		return
	}
	name := strings.TrimSpace(parts[0])
	contactType := strings.TrimSpace(parts[1])
	value := strings.TrimSpace(parts[2])
	var label string
	if len(parts) > 3 {
		label = strings.TrimSpace(parts[3])
	}

	// Find person first
	person, err := b.peopleManager.FindPerson(ctx, name)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error finding person: %v", err))
		return
	}
	if person == nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("No person found matching %q.", name))
		return
	}

	if err := b.peopleManager.AddContact(ctx, person.ID, contactType, value, label); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Added %s contact for %s.", contactType, person.FullName))
}

func (b *Bot) handleDelPerson(ctx context.Context, msg *tgbotapi.Message) {
	if b.peopleManager == nil {
		b.sendMessage(msg.Chat.ID, "People manager not initialized.")
		return
	}
	idStr := strings.TrimSpace(msg.CommandArguments())
	if idStr == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /delperson <id>")
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		b.sendMessage(msg.Chat.ID, "Invalid ID. Use a positive number.")
		return
	}
	if err := b.peopleManager.RemovePerson(ctx, id); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Person #%d removed.", id))
}

func (b *Bot) handleSetBootstrap(ctx context.Context, msg *tgbotapi.Message) {
	if b.contextBuilder == nil || b.pgDB == nil {
		b.sendMessage(msg.Chat.ID, "Context builder not initialized.")
		return
	}
	text := strings.TrimSpace(msg.CommandArguments())
	if text == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /setbootstrap <instruction text>")
		return
	}
	pgDB := b.pgDB
	if _, err := pgDB.SaveMemoryEntry(ctx, "bootstrap", "", text, "user", 10); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, "Bootstrap instruction saved.")
}

func (b *Bot) handleListMemory(ctx context.Context, msg *tgbotapi.Message) {
	if b.contextBuilder == nil || b.pgDB == nil {
		b.sendMessage(msg.Chat.ID, "Context builder not initialized.")
		return
	}
	category := strings.TrimSpace(msg.CommandArguments())
	pgDB := b.pgDB

	var entries []pgdb.MemoryEntry
	var err error
	if category != "" {
		entries, err = pgDB.GetMemoryByCategory(ctx, category)
	} else {
		entries, err = pgDB.GetActiveMemory(ctx)
	}
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	if len(entries) == 0 {
		if category != "" {
			b.sendMessage(msg.Chat.ID, fmt.Sprintf("No memories in category %q.", category))
		} else {
			b.sendMessage(msg.Chat.ID, "No active memories.")
		}
		return
	}

	b.sendLongMessage(msg.Chat.ID, formatMemoryEntries("Memories", entries))
}

// formatMemoryEntries formats a slice of memory entries for display.
func formatMemoryEntries(title string, entries []pgdb.MemoryEntry) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s (%d):\n\n", title, len(entries)))
	for _, e := range entries {
		content := format.Truncate(e.Content, 100)
		if e.Key != "" {
			sb.WriteString(fmt.Sprintf("[%s/%s] %s (src:%s, imp:%d)\n", e.Category, e.Key, content, e.Source, e.Importance))
		} else {
			sb.WriteString(fmt.Sprintf("[%s] %s (src:%s, imp:%d)\n", e.Category, content, e.Source, e.Importance))
		}
	}
	return sb.String()
}

func (b *Bot) handleJournal(ctx context.Context, msg *tgbotapi.Message) {
	if b.journal == nil {
		b.sendMessage(msg.Chat.ID, "Journal not initialized.")
		return
	}
	dateStr := strings.TrimSpace(msg.CommandArguments())
	if dateStr == "" {
		dateStr = time.Now().Format("2006-01-02")
	}
	entry, err := b.journal.GetJournalEntry(ctx, dateStr)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	if entry == nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("No journal entry for %s.", dateStr))
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Journal: %s\n\n", entry.Date))
	sb.WriteString(entry.Summary)
	if len(entry.Topics) > 0 {
		sb.WriteString(fmt.Sprintf("\n\nTopics: %s", strings.Join(entry.Topics, ", ")))
	}
	if len(entry.Projects) > 0 {
		sb.WriteString(fmt.Sprintf("\nProjects: %s", strings.Join(entry.Projects, ", ")))
	}
	sb.WriteString(fmt.Sprintf("\nMessages: %d | Input tokens: %d | Output tokens: %d",
		entry.Stats.Messages, entry.Stats.InputTokens, entry.Stats.OutputTokens))
	b.sendLongMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) handleJournalSearch(ctx context.Context, msg *tgbotapi.Message) {
	if b.journal == nil {
		b.sendMessage(msg.Chat.ID, "Journal not initialized.")
		return
	}
	query := strings.TrimSpace(msg.CommandArguments())
	if query == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /journal_search <query>")
		return
	}
	entries, err := b.journal.SearchJournal(ctx, query, 5)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	if len(entries) == 0 {
		b.sendMessage(msg.Chat.ID, "No matching journal entries found.")
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Journal search: %q (%d results)\n\n", query, len(entries)))
	for _, e := range entries {
		summary := format.Truncate(e.Summary, 200)
		sb.WriteString(fmt.Sprintf("%s: %s\n\n", e.Date, summary))
	}
	b.sendLongMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) handleProfile(ctx context.Context, msg *tgbotapi.Message) {
	if b.profiler == nil {
		b.sendMessage(msg.Chat.ID, "Profiler not initialized.")
		return
	}
	aspects, err := b.profiler.GetFullProfile(ctx)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	if len(aspects) == 0 {
		b.sendMessage(msg.Chat.ID, "No profile data yet. Profile builds over time from conversations.")
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("User Profile (%d aspects):\n\n", len(aspects)))
	for _, a := range aspects {
		sb.WriteString(fmt.Sprintf("• %s (%.0f%%): %s\n", a.Aspect, a.Confidence*100, a.Content))
	}
	b.sendLongMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) handleSetProfile(ctx context.Context, msg *tgbotapi.Message) {
	if b.profiler == nil {
		b.sendMessage(msg.Chat.ID, "Profiler not initialized.")
		return
	}
	args := strings.TrimSpace(msg.CommandArguments())
	// Expected: <aspect> <content>
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		b.sendMessage(msg.Chat.ID, "Usage: /setprofile <aspect> <content>")
		return
	}
	aspect := strings.TrimSpace(parts[0])
	content := strings.TrimSpace(parts[1])
	// Strip quotes if present
	content = strings.Trim(content, "\"'")

	if err := b.profiler.SetAspect(ctx, aspect, content); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Profile aspect %q set (confidence: 100%%).", aspect))
}

func (b *Bot) handleDelProfile(ctx context.Context, msg *tgbotapi.Message) {
	if b.profiler == nil {
		b.sendMessage(msg.Chat.ID, "Profiler not initialized.")
		return
	}
	aspect := strings.TrimSpace(msg.CommandArguments())
	if aspect == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /delprofile <aspect>")
		return
	}
	if err := b.profiler.DeleteAspect(ctx, aspect); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Profile aspect %q deleted.", aspect))
}

func (b *Bot) handleDBCheck(ctx context.Context, msg *tgbotapi.Message) {
	if b.pgDB == nil {
		b.sendMessage(msg.Chat.ID, "PostgreSQL not connected.")
		return
	}
	// Query last 10 conversations
	convs, err := b.pgDB.RecentConversationsAllSessions(ctx, 10)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Query failed: %v", err))
		return
	}

	if len(convs) == 0 {
		b.sendMessage(msg.Chat.ID, "No conversations in PostgreSQL. RecordExchange may not be saving.")
		return
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "Last %d messages in PostgreSQL:\n\n", len(convs))
	for _, c := range convs {
		content := c.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		fmt.Fprintf(&buf, "[%s] %s | %s: %s\n",
			c.CreatedAt.Format("15:04:05"), c.SessionID, c.Role, content)
	}
	b.sendMessage(msg.Chat.ID, buf.String())
}

// handleCronAdd creates a new cron job at runtime.
// Usage: /cronadd name "schedule" prompt text here
// Example: /cronadd standup "30 9 * * 1-5" Check my calendar and summarise today's meetings
func (b *Bot) handleCronAdd(msg *tgbotapi.Message) {
	if b.cronScheduler == nil {
		b.sendMessage(msg.Chat.ID, "Cron scheduler not available.")
		return
	}

	args := msg.CommandArguments()
	if args == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /cronadd name \"schedule\" prompt text\n\nExample:\n/cronadd standup \"30 9 * * 1-5\" Check my calendar and summarise today's meetings")
		return
	}

	// Parse: name "schedule" prompt...
	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		b.sendMessage(msg.Chat.ID, "Usage: /cronadd name \"schedule\" prompt text")
		return
	}
	name := parts[0]
	rest := parts[1]

	// Extract quoted schedule
	if !strings.HasPrefix(rest, "\"") {
		b.sendMessage(msg.Chat.ID, "Schedule must be in quotes: /cronadd name \"0 9 * * *\" prompt text")
		return
	}
	endQuote := strings.Index(rest[1:], "\"")
	if endQuote < 0 {
		b.sendMessage(msg.Chat.ID, "Missing closing quote for schedule.")
		return
	}
	schedule := rest[1 : endQuote+1]
	prompt := strings.TrimSpace(rest[endQuote+2:])

	if prompt == "" {
		b.sendMessage(msg.Chat.ID, "Prompt cannot be empty.")
		return
	}

	if err := b.cronScheduler.AddDynamic(name, schedule, prompt); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed: %v", err))
		return
	}

	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Cron job %q added.\nSchedule: %s\nPrompt: %s", name, schedule, prompt))
}

func (b *Bot) handleCronDel(msg *tgbotapi.Message) {
	if b.cronScheduler == nil {
		b.sendMessage(msg.Chat.ID, "Cron scheduler not available.")
		return
	}
	name := strings.TrimSpace(msg.CommandArguments())
	if name == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /crondel name")
		return
	}
	if err := b.cronScheduler.RemoveJob(name); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Cron job %q removed.", name))
}

func (b *Bot) handleCronEnable(msg *tgbotapi.Message) {
	if b.cronScheduler == nil {
		b.sendMessage(msg.Chat.ID, "Cron scheduler not available.")
		return
	}
	name := strings.TrimSpace(msg.CommandArguments())
	if name == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /cronenable name")
		return
	}
	if err := b.cronScheduler.EnableJob(name); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Cron job %q enabled.", name))
}

func (b *Bot) handleCronDisable(msg *tgbotapi.Message) {
	if b.cronScheduler == nil {
		b.sendMessage(msg.Chat.ID, "Cron scheduler not available.")
		return
	}
	name := strings.TrimSpace(msg.CommandArguments())
	if name == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /crondisable name")
		return
	}
	if err := b.cronScheduler.DisableJob(name); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed: %v", err))
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Cron job %q disabled.", name))
}

// handleFramework shows the current AI framework or switches to a new one.
//
//	/framework          — show current + available
// handleModel shows the actual API model from the last response, not a
// self-reported model identity. This is immune to conversation history
// pollution and always reflects what the API actually returned.
func (b *Bot) handleModel(msg *tgbotapi.Message) {
	configured := b.cfg.Sidecar.DefaultModel
	framework := "claude"
	if sw, ok := b.agent.(*agent.Switcher); ok {
		framework = sw.Active()
		if framework == "opencode" {
			configured = b.cfg.OpenCode.Model
		}
	}

	b.lastAPIModelMu.RLock()
	apiModel := b.lastAPIModel[msg.Chat.ID]
	b.lastAPIModelMu.RUnlock()

	var text strings.Builder
	fmt.Fprintf(&text, "Framework: `%s`\n", framework)
	fmt.Fprintf(&text, "Configured: `%s`\n", configured)
	if apiModel != "" {
		fmt.Fprintf(&text, "Last API model: `%s`", apiModel)
	} else {
		text.WriteString("Last API model: (no request yet — send a message first)")
	}

	b.sendMarkdownMessage(msg.Chat.ID, text.String())
}

//	/framework claude   — switch to Claude Code
//	/framework opencode — switch to OpenCode
func (b *Bot) handleFramework(_ context.Context, msg *tgbotapi.Message) {
	switcher, ok := b.agent.(*agent.Switcher)
	if !ok {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Framework switching not available.\nActive backend: *%s*", b.agent.Name()))
		return
	}

	arg := strings.TrimSpace(msg.CommandArguments())

	if arg == "" {
		// Show current status with model info
		active := switcher.Active()
		available := switcher.Available()

		// Map framework names to their configured models
		models := map[string]string{
			"claude":   b.cfg.Sidecar.DefaultModel,
			"opencode": b.cfg.OpenCode.Model,
		}

		var lines []string
		lines = append(lines, fmt.Sprintf("*Active framework:* %s", active))
		lines = append(lines, "")
		for _, name := range available {
			icon := "  "
			if name == active {
				icon = "▶ "
			}
			model := models[name]
			if model == "" {
				model = "default"
			}
			lines = append(lines, fmt.Sprintf("%s`%s` — %s", icon, name, model))
		}
		lines = append(lines, "")
		lines = append(lines, "Switch: /framework <name>")

		b.sendMessage(msg.Chat.ID, strings.Join(lines, "\n"))
		return
	}

	// Switch
	if err := switcher.Switch(arg); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed: %v", err))
		return
	}

	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Switched to *%s*.", arg))
}
