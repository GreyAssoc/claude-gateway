package bot

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/ctxbuilder"
	"github.com/you/claude-gateway/internal/db"
	"github.com/you/claude-gateway/internal/format"
	"github.com/you/claude-gateway/internal/orchestrator"
	"github.com/you/claude-gateway/internal/pgdb"
	"github.com/you/claude-gateway/internal/ratelimiter"
	"github.com/you/claude-gateway/internal/skills"
)

// handleCompactionEvent surfaces an auto- or manual-compaction event to
// the user and persists it for later memory retrieval. Best-effort: a
// failure to notify or persist must not break the live chat path.
func (b *Bot) handleCompactionEvent(chatID int64, sessionID string, c *agent.Compaction) {
	if c == nil {
		return
	}

	// Telegram notification — Claude-Code-style auto-compact line.
	var sb strings.Builder
	if c.TriggeredBy == "manual" {
		sb.WriteString("🗜 *Compacted on request.* ")
	} else {
		sb.WriteString("🗜 *Conversation auto-compacted")
		if c.UsageRatioPct > 0 {
			sb.WriteString(fmt.Sprintf(" at %d%% of context window", c.UsageRatioPct))
		}
		sb.WriteString(".* ")
	}
	sb.WriteString("Fresh session seeded with summary; continue normally.")
	notice := sb.String()
	if _, err := b.api.Send(tgbotapi.NewMessage(chatID, notice)); err != nil {
		log.Printf("Compaction notice send failed: %v", err)
	}

	if b.database == nil {
		return
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()
	if err := b.database.RecordCompaction(dbCtx, db.CompactionRecord{
		SessionID:       sessionID,
		OldSDKSessionID: c.OldSDKSessionID,
		Summary:         c.Summary,
		Reason:          c.Reason,
		TriggeredBy:     c.TriggeredBy,
		UsageRatioPct:   c.UsageRatioPct,
		ContextWindow:   c.ContextWindow,
		TokensAtTrigger: c.TokensAtTrigger,
		Provider:        c.Provider,
		Model:           c.Model,
	}); err != nil {
		log.Printf("Failed to record compaction: %v", err)
	}
}

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
			"/cronadd name \"schedule\" [@agent] [model] prompt — add a cron job\n" +
			"/crondel name — remove a cron job\n" +
			"/crondisable name — disable a cron job\n" +
			"/cronenable name — enable a cron job\n" +
			"/cronlist — list scheduled jobs\n" +
			"/cronrun name — run a cron job now\n" +
			"/dbcheck — check PostgreSQL conversations\n" +
			"/framework [name] — framework for this agent and every process it starts (inherit to drop)\n" +
			"/googleauth [code|status] — Google Workspace OAuth\n" +
			"/grokauth [status] — sign in SuperGrok (subscription, not API)\n" +
			"/journal — view daily journal\n" +
			"/journalsearch — search journal entries\n" +
			"/memorieslist — list curated memories\n" +
			"/model — show actual API model in use\n" +
			"/models — model for this agent and every process it starts (Qwen / DeepSeek / Grok)\n" +
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
			"/compact — summarise session and start fresh (preserves context)\n" +
			"/reset — clear conversation session\n" +
			"/reviewauto [dir] — auto-detect language and review\n" +
			"/reviewautosec [dir] — auto-detect with security focus\n" +
			"/skills — list loaded skills\n" +
			"/status — gateway health & rate limits\n" +
			"/progress [@Name] — peek at a running agent without cancelling\n" +
			"/stop — stop the current agent (@Name or all)\n" +
			"/sync — sync agents & commands between Claude ↔ OpenCode\n" +
			"/who — which agent you are chatting with\n" +
			"/agents — list folder agents and their private skills\n\n" +
			"Address an agent with @Name (e.g. @SEOagent). Messages without @ stay with the current agent. Other agents keep working and notify you when they finish. Peek at a running agent with /progress or @Name (no extra text) — a new task message will not cancel it; use /stop."

		if b.skillsManager != nil {
			var cmds []string
			for _, s := range b.skillsManager.GetCommandSkills() {
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
	case "cronrun":
		b.handleCronRun(msg)
	case "dbcheck":
		b.handleDBCheck(ctx, msg)
	case "framework":
		b.handleFramework(ctx, msg)
	case "googleauth":
		b.handleGoogleAuth(ctx, msg)
	case "grokauth":
		b.handleGrokAuth(ctx, msg)
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
	case "models":
		b.handleModels(msg)
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
	case "compact":
		b.handleCompact(ctx, msg)
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
	case "who", "chats":
		b.handleWho(msg)
	case "progress":
		b.handleProgress(msg)
	case "default", "home", "main":
		b.handleDefaultChat(msg)
	case "agents":
		b.handleAgentCatalog(msg)
	case "stop":
		b.handleStop(msg)
	case "sync":
		b.handleSync(msg)
	case "today":
		b.handleToday(ctx, msg)

	default:
		// Check if this is a skill command
		if b.skillsManager != nil {
			if skill, ok := b.skillsManager.GetSkillByCommand(msg.Command()); ok {
				b.handleSkillCommand(ctx, msg, skill)
				return
			}
			if b.tryAgentSlashCommand(ctx, msg) {
				return
			}
		}
		b.sendMessage(msg.Chat.ID, "Unknown command. Try /start or /agents.")
	}
}

// maxGatewayLookupLen is the longest message we will treat as a gateway
// lookup. Work briefs, paste-ins, and multi-clause instructions are always
// longer than a "show journal" request.
const maxGatewayLookupLen = 80

// gatewayLookupPhrases maps a normalised whole utterance to a lookup kind.
// Matching is exact (after light politeness stripping), not substring.
// A previous contains() matcher intercepted any message containing
// "daily journal" — including multi-page implementation briefs — and
// replied "No journal entry for <today>" instead of sending the work
// to the model. Same class of bug as NL model-switching, which is
// /models only.
var gatewayLookupPhrases = buildGatewayLookups()

func buildGatewayLookups() map[string]string {
	m := make(map[string]string)
	add := func(kind string, cores ...string) {
		verbs := []string{
			"",
			"show ", "view ", "list ", "get ", "display ",
			"show me ", "show me the ", "show me my ",
			"show the ", "show my ", "show all ",
			"view the ", "view my ",
			"list the ", "list my ", "list all ",
		}
		for _, core := range cores {
			for _, v := range verbs {
				m[v+core] = kind
			}
		}
	}
	add("journal",
		"journal", "daily journal", "journal entry",
		"today's journal", "todays journal", "today journal",
		"today's journal entry", "todays journal entry",
	)
	add("cron",
		"cron", "cron job", "cron jobs", "cron list",
		"scheduled job", "scheduled jobs", "scheduled task", "scheduled tasks",
		"active cron", "my cron",
	)
	add("people",
		"people", "contacts", "all people", "all contacts",
		"my contacts", "my people",
	)
	add("profile",
		"profile", "my profile", "user profile",
	)
	add("memories",
		"memories", "my memories", "all memories",
	)
	add("status",
		"status", "gateway status", "system status",
		"health check", "rate limit", "rate limits",
	)
	// Phrases that don't fit verb+core expansion.
	m["who do i know"] = "people"
	m["what do you know about me"] = "profile"
	m["what do you remember"] = "memories"
	m["list scheduled"] = "cron"
	m["list cron"] = "cron"
	m["show cron"] = "cron"
	return m
}

// normalizeGatewayLookup lowercases, collapses space, strips trailing
// punctuation and a light politeness wrapper. Returns "" for messages
// that cannot be a short lookup (empty, multiline, too long).
func normalizeGatewayLookup(text string) string {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxGatewayLookupLen {
		return ""
	}
	s = strings.TrimRight(s, ".!?,")
	for {
		next := s
		for _, p := range []string{"please ", "pls ", "can you ", "could you ", "would you "} {
			next = strings.TrimPrefix(next, p)
		}
		next = strings.TrimSuffix(next, " please")
		next = strings.TrimSuffix(next, " pls")
		next = strings.TrimSpace(next)
		if next == s {
			break
		}
		s = next
	}
	return s
}

// classifyGatewayIntent returns a gateway lookup kind for short
// natural-language queries the gateway can answer directly (cron, people,
// profile, memories, journal, status). Returns "" when the message should
// go to the model.
func classifyGatewayIntent(text string) string {
	s := normalizeGatewayLookup(text)
	if s == "" {
		return ""
	}
	return gatewayLookupPhrases[s]
}

// gatewayIntentMatch checks if a natural language message is asking about
// something the gateway can answer directly (cron jobs, people, profile, etc.).
// Returns true if handled, false if the message should proceed to Claude.
// This prevents Claude from wasting tokens searching the filesystem for
// gateway-internal data it has no access to.
func (b *Bot) gatewayIntentMatch(ctx context.Context, msg *tgbotapi.Message) bool {
	switch classifyGatewayIntent(msg.Text) {
	case "cron":
		b.handleCronList(msg)
		return true
	case "people":
		b.handleListPeople(ctx, msg)
		return true
	case "profile":
		b.handleProfile(ctx, msg)
		return true
	case "memories":
		b.handleMemories(ctx, msg)
		return true
	case "journal":
		b.handleJournal(ctx, msg)
		return true
	case "status":
		b.handleStatus(ctx, msg)
		return true
	default:
		// Do not intercept talk about models/frameworks. Natural-language
		// "switch to qwen" / "use qwen" / "which model" matching made it
		// impossible to discuss local models — the bot just switched to Qwen.
		// Model changes are /models only (slash command or its inline picker).
		return false
	}
}

func (b *Bot) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	if msg.Text == "" {
		return
	}

	// Intercept "." as a quick command menu.
	if strings.TrimSpace(msg.Text) == "." {
		b.sendCommandMenu(msg)
		return
	}

	// Telegram's @ picker is usernames only. Sending "@" lists specialists.
	if strings.TrimSpace(msg.Text) == "@" {
		b.sendAgentPicker(msg.Chat.ID)
		return
	}

	// "stop" must never be rate-limited or queued — otherwise you cannot
	// abort a DeepSeek wait when the shared limiter is at 0% headroom.
	if strings.EqualFold(strings.TrimSpace(msg.Text), "stop") {
		b.handleStop(msg)
		return
	}

	if teams := parseReviewTeamRequest(msg.Text); len(teams) > 0 && b.orchestrator != nil {
		for _, team := range teams {
			b.handleOrchestratorTeamWithTask(ctx, msg, team, msg.Text)
		}
		return
	}

	// Step 0: Intercept natural language requests about gateway features.
	// These are things Claude cannot answer (no access to cron scheduler,
	// people DB, etc.) so we handle them directly.
	if b.gatewayIntentMatch(ctx, msg) {
		return
	}

	// Project detection: resolve project from user text and set active project.
	// This doesn't intercept the message — it just sets the active project file
	// so hooks inject the right context (server, URL, deploy path, etc.).
	if projName, _ := resolveProject(msg.Text); projName != "" {
		setActiveProjectFile(projName)
	}

	// People detection: extract known names from message text for context enrichment.
	// This replaces the triage LLM layer with a fast in-process lookup.
	var peopleNames []string
	if b.peopleManager != nil {
		if names, err := b.peopleManager.MatchNames(ctx, msg.Text); err == nil {
			peopleNames = names
		}
	}

	// Each agent conversation is counted against its own framework window.
	// Qwen is local and is never queued. A full DeepSeek window does not
	// block Grok, and two DeepSeek agents do not share one input total.
	turn, prompt, switchOnly := b.routeAgentMessage(msg)
	if switchOnly {
		b.sendFocusMessage(msg.Chat.ID, turn)
		return
	}

	scope := b.usageScope(msg.Chat.ID, turn)
	if !b.admitOrExplain(msg.Chat.ID, scope, 1000, func() {
		// Use Background: queued requests have independent lifecycles
		// and should not be cancelled by the original handler's context.
		b.processMessageWith(context.Background(), msg, prompt, "", peopleNames, turn)
	}) {
		return
	}

	b.processMessageWith(ctx, msg, prompt, "", peopleNames, turn)
}

func mdPlain(s string) string {
	return strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"`", "\\`",
		"[", "\\[",
	).Replace(s)
}

func (b *Bot) usageScope(chatID int64, turn agentSlot) ratelimiter.Scope {
	fw := b.frameworkFor(chatID, turn)
	return ratelimiter.Scope{
		Framework: fw,
		SessionID: sessionIDFor(chatID, turn.Key),
		Model:     b.modelForFramework(fw),
	}
}

// admitOrExplain returns true when the caller should run the turn now.
// RPM exhaustion queues. A full context window stops only this conversation.
func (b *Bot) admitOrExplain(chatID int64, scope ratelimiter.Scope, estimate int, queued func()) bool {
	if b.limiter == nil || ratelimiter.Unlimited(scope.Framework) {
		return true
	}
	d := b.limiter.Admit(scope, estimate)
	if d.OK {
		return true
	}
	if d.Reason == ratelimiter.ReasonContext {
		model := d.Usage.Model
		if model == "" {
			model = scope.Model
		}
		if model == "" {
			model = scope.Framework
		}
		b.sendMessage(chatID, fmt.Sprintf(
			"This conversation is at %s / %s of the %s context window (%s).\nOther agents are not counted in this number.\nSend /compact to continue this one.",
			format.Tokens(d.Usage.ContextTokens),
			format.Tokens(d.Usage.ContextWindow),
			scope.Framework,
			model,
		))
		return false
	}
	status := d.Status
	position := b.limiter.EnqueueChat(chatID, queued)
	if position < 0 {
		b.sendMessage(chatID, fmt.Sprintf(
			"Rate limited (requests this minute).\nQueue full. Resets in %s.\nRPM %s",
			status.WindowResets, status.RPM))
	} else {
		b.sendMessage(chatID, fmt.Sprintf(
			"Rate limited (requests this minute).\nQueued at position %d. Resets in %s.\nRPM %s",
			position, status.WindowResets, status.RPM))
	}
	return false
}

func (b *Bot) routeAgentMessage(msg *tgbotapi.Message) (turn agentSlot, prompt string, switchOnly bool) {
	prompt = msg.Text
	call := parseAgentCall(msg.Text)
	if call.Mentioned {
		self := ""
		if b.api != nil {
			self = b.api.Self.UserName
		}
		if self != "" && strings.EqualFold(call.Handle, self) {
			turn = b.getFocus(msg.Chat.ID)
			return turn, prompt, false
		}
		turn = b.resolveSlot(call.Handle)
		b.setFocus(msg.Chat.ID, turn)
		if call.Rest == "" {
			return turn, "", true
		}
		return turn, call.Rest, false
	}
	return b.getFocus(msg.Chat.ID), prompt, false
}

// processMessageWith sends a message to Claude with streaming progress.
// When systemPrompt is non-empty, it overrides the default system prompt builder.
// peopleNames are used for enriched context (people profile injection).
func (b *Bot) processMessageWith(ctx context.Context, msg *tgbotapi.Message, prompt, systemPrompt string, peopleNames []string, turn agentSlot) {
	if turn.Key == "" {
		turn = agentSlot{Key: defaultAgentKey, Display: defaultAgentDisplay}
	}
	tag := "@" + turn.Display

	// A new prompt on a busy slot used to cancel the in-flight run.
	// Check-ins peek at progress; anything else is refused so the task keeps going.
	if snap, ok := b.slotSnapshot(msg.Chat.ID, turn.Key); ok {
		b.sendMessage(msg.Chat.ID, formatRunReport(turn, snap, looksLikeProgressPeek(prompt)))
		return
	}

	// Send "Working..." indicator — we'll edit this message in-place with progress
	thinking := tgbotapi.NewMessage(msg.Chat.ID, tag+" — Working...")
	thinkingMsg, err := b.api.Send(thinking)
	if err != nil {
		log.Printf("Failed to send thinking message: %v", err)
	}

	sessionID := sessionIDFor(msg.Chat.ID, turn.Key)
	if turn.Key != defaultAgentKey && systemPrompt == "" {
		systemPrompt = namedAgentSystemPrompt(turn)
		if n := len(turn.Skills); n > 0 {
			names := make([]string, 0, n)
			for _, s := range turn.Skills {
				names = append(names, s.Name)
			}
			systemPrompt += "\nPrivate skills (Read the files in your folder if needed): " + strings.Join(names, ", ")
		}
	}

	// Build request options
	backend, fw := b.backendFor(msg.Chat.ID, turn)
	opts := agent.AskOptions{
		SessionID: sessionID,
		Prompt:    prompt,
		WorkDir:   turn.workDir(b.cfg.WorkDir),
		Model:     b.modelForFramework(fw),
		MaxTurns:  40,
	}

	// Local Qwen via vLLM: skip the huge Claude bootstrap/profile system
	// prompt (that used to blow a 65k window). Harness skills still go
	// through. At 150k, recent conversation history IS injected so Qwen
	// has the same "what were we just doing" memory as DeepSeek/Grok.
	localQwen := isLocalQwen(fw)

	// System prompt injection:
	// - If an explicit override is provided (skill commands), always use it.
	// - Otherwise, check if the SDK session is alive in the sidecar.
	//   If dead/new, build system prompt from PostgreSQL (or legacy memory manager).
	needsSystemPrompt := true // tracks whether session is new (used for enriched prompt)
	if localQwen && systemPrompt == "" {
		if b.skillsManager != nil {
			opts.SystemPrompt = b.skillsManager.BuildLibraryPrompt(prompt, 8000)
		}
	} else if systemPrompt != "" {
		opts.SystemPrompt = systemPrompt
		if turn.Key == defaultAgentKey {
			// Skill command / explicit override = fresh session context
		} else if !localQwen {
			checkCtx, checkCancel := context.WithTimeout(ctx, 2*time.Second)
			status, checkErr := backend.CheckSession(checkCtx, sessionID)
			checkCancel()
			if checkErr == nil && status.Alive && status.IdleSeconds < 1800 {
				needsSystemPrompt = false
			}
		}
	} else if localQwen {
		// Qwen sessions live in the sidecar; skip SDK check-session (it used
		// the immortal handler ctx and could stall the Working bubble).
	} else {
		checkCtx, checkCancel := context.WithTimeout(ctx, 2*time.Second)
		status, checkErr := backend.CheckSession(checkCtx, sessionID)
		checkCancel()
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
				if err := backend.ResetSession(resetCtx, sessionID); err != nil {
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
				sp, err := b.contextBuilder.BuildSystemPrompt(buildCtx, sessionID)
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
		var (
			enriched  string
			enrichErr error
		)
		if localQwen {
			b.skipQwenHistoryMu.Lock()
			skipHist := b.skipQwenHistory[sessionID]
			if skipHist {
				delete(b.skipQwenHistory, sessionID)
			}
			b.skipQwenHistoryMu.Unlock()
			if skipHist {
				log.Printf("Qwen: skipping DB history dump after /reset")
				enriched, enrichErr = "", nil
			} else {
				enriched, enrichErr = b.contextBuilder.BuildEnrichedPromptWith(enrichCtx, sessionID, prompt, peopleNames, sessionAlive, ctxbuilder.QwenWideEnrichment())
			}
		} else {
			enriched, enrichErr = b.contextBuilder.BuildEnrichedPrompt(enrichCtx, sessionID, prompt, peopleNames, sessionAlive)
		}
		enrichCancel()
		if enrichErr != nil {
			log.Printf("Enriched prompt build failed: %v", enrichErr)
		} else if enriched != "" {
			// Append matched skills to enriched prompt — only skills relevant
			// to this message get their full content injected.
			if b.skillsManager != nil {
				cap := 48000
				if localQwen {
					cap = 0 // already in system prompt
				}
				if cap > 0 {
					if skillSection := b.skillsManager.BuildLibraryPrompt(prompt, cap); skillSection != "" {
						enriched += "\n\n" + skillSection
					}
				}
			}
			opts.Prompt = enriched + "\n\n---\n\n" + prompt
		}
	}

	// Per-request timeout: 120 minutes accommodates full pipeline tests
	// and long multi-file tasks while still capping stuck requests so
	// they release their semaphore slot eventually.
	reqCtx, reqCancel := context.WithTimeout(ctx, 120*time.Minute)

	// Register cancel so /stop can interrupt THIS agent. Other agents keep running.
	// Do not replace an in-flight run — that used to kill the task on a check-in.
	run, started, unbind := b.tryBeginRun(reqKey(msg.Chat.ID, turn.Key), reqCancel)
	if !started {
		reqCancel()
		if thinkingMsg.MessageID != 0 {
			del := tgbotapi.NewDeleteMessage(msg.Chat.ID, thinkingMsg.MessageID)
			if _, err := b.api.Send(del); err != nil {
				log.Printf("Failed to delete progress message: %v", err)
			}
		}
		snap, ok := run.copySnap()
		if !ok {
			snap, _ = b.slotSnapshot(msg.Chat.ID, turn.Key)
		}
		b.sendMessage(msg.Chat.ID, formatRunReport(turn, snap, looksLikeProgressPeek(prompt)))
		return
	}
	defer unbind()
	run.Framework = fw
	run.setSnap(runSnapshot{Display: turn.Display, Started: time.Now()})

	// Rolling progress state — mutex-protected for concurrent access
	// between the onProgress callback and the flush goroutine.
	var mu sync.Mutex
	var progressLines []string          // tool activity log
	var responsePreview strings.Builder // accumulates Claude's response text
	var gpuStatus string
	var editPending bool
	startTime := time.Now()
	lastEdit := time.Time{}

	// editProgress updates the "Working..." message with the latest state.
	// When response text is streaming, shows it as a live preview so the
	// user sees Claude "typing". Otherwise shows tool activity.
	editProgress := func() {
		mu.Lock()
		allLines := append([]string(nil), progressLines...)
		nActions := len(allLines)
		preview := responsePreview.String()
		gpu := gpuStatus
		editPending = false
		lastEdit = time.Now()
		mu.Unlock()
		lines := allLines
		if len(lines) > 15 {
			lines = lines[len(lines)-15:]
		}

		elapsed := time.Since(startTime).Truncate(time.Second)
		bg := ""
		if !b.isFocused(msg.Chat.ID, turn.Key) {
			bg = " (background)"
		}
		gpuBit := ""
		if gpu != "" {
			gpuBit = " | " + gpu
		}

		var text string
		if preview != "" {
			// Response text is flowing — show it as a live preview.
			// Truncate from the front to show the most recent text, keeping
			// within Telegram's message limit.
			const maxPreview = maxTelegramMessage - 120
			if len(preview) > maxPreview {
				preview = "..." + preview[len(preview)-maxPreview:]
			}
			text = fmt.Sprintf("%s — Responding%s... %s | %d actions%s\n\n%s", tag, bg, elapsed, nActions, gpuBit, preview)
		} else if nActions > 0 {
			text = fmt.Sprintf("%s — Working%s... %s (%d actions)%s\n\n%s", tag, bg, elapsed, nActions, gpuBit, strings.Join(lines, "\n"))
		} else {
			text = fmt.Sprintf("%s — Working%s... %s%s", tag, bg, elapsed, gpuBit)
		}

		if thinkingMsg.MessageID != 0 {
			edit := tgbotapi.NewEditMessageText(msg.Chat.ID, thinkingMsg.MessageID, text)
			if _, err := b.api.Send(edit); err != nil {
				log.Printf("Failed to edit progress message: %v", err)
			}
		}
		run.setSnap(runSnapshot{
			Display: turn.Display,
			Started: startTime,
			Actions: nActions,
			Lines:   allLines,
			Preview: preview,
			GPU:     gpu,
		})
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
			gpuStatus = applyGPUProgress(gpuStatus, ev.Tool, ev.Input)
			icon := toolIcon(ev.Tool)
			line := fmt.Sprintf("%s %s: %s", icon, ev.Tool, ev.Input)
			n := len(progressLines)
			if n == 0 || progressLines[n-1] != line {
				progressLines = append(progressLines, line)
			}
		} else if ev.Text != "" && !isNoiseProgress(ev.Text) {
			appendStreamText(&responsePreview, ev.Text)
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
	resp, err := backend.AskStreaming(reqCtx, opts, onProgress)

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
		// /stop already told the user. context.Canceled is that cancel
		// (or gateway shutdown) — not a failure to surface again.
		if isUserCancel(err) {
			log.Printf("Agent @%s stopped", turn.Display)
			return
		}
		notify := !b.isFocused(msg.Chat.ID, turn.Key)
		b.sendLongMessage(msg.Chat.ID, formatAgentReply(turn.Display, fmt.Sprintf("Error: %v", err), notify))
		return
	}

	// Track the API-reported model for /model command
	if resp.Model != "" {
		b.lastAPIModelMu.Lock()
		b.lastAPIModel[msg.Chat.ID] = resp.Model
		b.lastAPIModelMu.Unlock()
	}

	// Record this conversation's context occupancy on its own counter.
	// Qwen is stored too, but it does not consume RPM.
	scope := ratelimiter.Scope{
		Framework: fw,
		SessionID: sessionID,
		Model:     opts.Model,
	}
	if resp.Model != "" {
		scope.Model = resp.Model
	}
	if b.limiter != nil && (resp.Usage != nil || resp.Context != nil) {
		b.limiter.Record(scope, resp.Usage, resp.Context, !ratelimiter.Unlimited(fw))
	}

	// Record usage
	if resp.Usage != nil {
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

	// Final Telegram message is the answer only. Qwen thinking is streamed
	// into the live "Working..." bubble (then that bubble is deleted above).
	text := strings.TrimSpace(resp.Text)
	if text == "" {
		text = "Done (no text output)."
	}

	notify := !b.isFocused(msg.Chat.ID, turn.Key)
	b.sendLongMessage(msg.Chat.ID, formatAgentReply(turn.Display, text, notify))

	// Auto-compaction notice: if the sidecar compacted this turn,
	// surface a Claude-Code-style notification to the user and persist
	// the event so the memory module can recall the summary later.
	if resp.Compaction != nil && resp.Compaction.Triggered && resp.Compaction.OK {
		if b.limiter != nil {
			b.limiter.ResetScope(scope)
		}
		b.handleCompactionEvent(msg.Chat.ID, sessionID, resp.Compaction)
	}

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
// appendStreamText concatenates model stream tokens. Grok emits one
// subword per event; a newline per event made Telegram show one word
// per line. Do not insert breaks here — the sidecar prefixes 💭 once
// per thinking burst and puts a newline when leaving thinking.
func appendStreamText(dst *strings.Builder, chunk string) {
	if chunk == "" || dst == nil {
		return
	}
	dst.WriteString(chunk)
}

// isNoiseProgress hides SDK internals that leak through the stream
// (DeepSeek thinking ticks, Claude Code model warnings, session chatter).
func isNoiseProgress(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || t == "Session initialized" {
		return true
	}
	if strings.Contains(t, "💭") {
		return true
	}
	if strings.Contains(t, "thinking_tokens") || strings.Contains(t, "unrecognized_model") {
		return true
	}
	if strings.HasPrefix(t, "[system:") || strings.HasPrefix(t, "[claude-code:") {
		return true
	}
	return false
}

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

	focus := b.getFocus(msg.Chat.ID)
	lines := []string{
		"*Gateway Status*",
		"",
		fmt.Sprintf("Chatting with: @%s", focus.Display),
		fmt.Sprintf("Sidecar: %s", sidecarStatus),
		"",
		"*Requests this minute:*",
		fmt.Sprintf("  RPM: %s", rl.RPM),
		fmt.Sprintf("  Headroom: %.0f%%", rl.Headroom*100),
		fmt.Sprintf("  Window resets: %s", rl.WindowResets),
		"",
		"*Context (each conversation, not pooled):*",
	}
	if len(rl.Conversations) == 0 {
		lines = append(lines, "  (none yet)")
	}
	for i, c := range rl.Conversations {
		if i >= 12 {
			lines = append(lines, fmt.Sprintf("  … and %d more", len(rl.Conversations)-12))
			break
		}
		model := c.Model
		if model == "" {
			model = c.Framework
		}
		lines = append(lines, fmt.Sprintf("  %s · %s · %s / %s · out %s this turn",
			mdPlain(c.SessionID), mdPlain(model),
			format.Tokens(c.ContextTokens), format.Tokens(c.ContextWindow),
			format.Tokens(c.OutputTokens)))
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
	turn := b.getFocus(msg.Chat.ID)
	sessionID := sessionIDFor(msg.Chat.ID, turn.Key)
	var err error
	if sw, ok := b.agent.(*agent.Switcher); ok {
		err = sw.ResetSessionEverywhere(ctx, sessionID)
	} else {
		err = b.agent.ResetSession(ctx, sessionID)
	}
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Reset failed: %v", err))
		return
	}
	if b.limiter != nil {
		b.limiter.ResetScope(b.usageScope(msg.Chat.ID, turn))
	}

	b.sessionsMu.Lock()
	delete(b.sessions, msg.Chat.ID)
	b.sessionsMu.Unlock()

	b.skipQwenHistoryMu.Lock()
	if b.skipQwenHistory == nil {
		b.skipQwenHistory = make(map[string]bool)
	}
	b.skipQwenHistory[sessionID] = true
	b.skipQwenHistoryMu.Unlock()

	b.sendMessage(msg.Chat.ID, fmt.Sprintf("@%s session cleared. Fresh start.", turn.Display))
}

// handleCompact runs a manual session compaction: the active model
// summarises the session, the summary becomes the seed of a fresh
// session, and the user is notified. Unlike /reset, prior context is
// preserved (as a summary) rather than discarded.
func (b *Bot) handleCompact(ctx context.Context, msg *tgbotapi.Message) {
	turn := b.getFocus(msg.Chat.ID)
	sessionID := sessionIDFor(msg.Chat.ID, turn.Key)
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("🗜 @%s — summarising conversation and preparing fresh session…", turn.Display))

	be, _ := b.backendFor(msg.Chat.ID, turn)
	c, err := be.CompactSession(ctx, sessionID)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Compact failed: %v", err))
		return
	}
	if c == nil {
		b.sendMessage(msg.Chat.ID, "Nothing to compact — no active session.")
		return
	}
	if !c.OK {
		errMsg := c.Error
		if errMsg == "" {
			errMsg = "unknown error"
		}
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Compact failed: %s", errMsg))
		return
	}

	// Reuse the shared notify+persist path.
	b.handleCompactionEvent(msg.Chat.ID, sessionID, c)
}

func (b *Bot) handleStop(msg *tgbotapi.Message) {
	dropped := 0
	if b.limiter != nil {
		dropped = b.limiter.DropQueuedFor(msg.Chat.ID)
	}

	args := strings.TrimSpace(msg.CommandArguments())
	if args == "" && strings.EqualFold(strings.TrimSpace(msg.Text), "stop") {
		args = ""
	}

	stopOne := func(slot string) bool {
		stopped := b.cancelSlot(msg.Chat.ID, slot)
		b.agent.AbortSession(sessionIDFor(msg.Chat.ID, slot))
		return stopped
	}

	var stopped bool
	label := ""
	switch {
	case strings.EqualFold(args, "all"):
		slots := b.runningSlots(msg.Chat.ID)
		n := b.cancelAllSlots(msg.Chat.ID)
		seen := map[string]bool{}
		for _, s := range slots {
			seen[s] = true
			b.agent.AbortSession(sessionIDFor(msg.Chat.ID, s))
		}
		for _, s := range []string{defaultAgentKey, orchestratorSlot, b.getFocus(msg.Chat.ID).Key} {
			if !seen[s] {
				b.agent.AbortSession(sessionIDFor(msg.Chat.ID, s))
			}
		}
		stopped = n > 0
		label = "all agents"
	case strings.HasPrefix(args, "@") || args != "":
		handle := strings.TrimPrefix(args, "@")
		if p := parseAgentCall("@" + handle); p.Mentioned {
			handle = p.Handle
		}
		if strings.EqualFold(handle, orchestratorSlot) {
			stopped = stopOne(orchestratorSlot)
			label = "@orchestrator"
		} else {
			slot := b.resolveSlot(handle)
			stopped = stopOne(slot.Key)
			label = "@" + slot.Display
		}
	default:
		focus := b.getFocus(msg.Chat.ID)
		stopped = stopOne(focus.Key)
		label = "@" + focus.Display
	}

	if !stopped && dropped == 0 {
		b.sendMessage(msg.Chat.ID, "Nothing running to stop.")
		return
	}

	text := "Stopped " + label + "."
	if dropped > 0 {
		text = fmt.Sprintf("Stopped %s. Dropped %d queued message(s).", label, dropped)
	}
	b.sendMessage(msg.Chat.ID, text)
}

func (b *Bot) handleWho(msg *tgbotapi.Message) {
	focus := b.getFocus(msg.Chat.ID)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Chatting with @%s\n", focus.Display)
	if focus.DNAName != "" {
		fmt.Fprintf(&sb, "Bound file: %s\n", focus.DNAName)
	}
	running := b.runningSlots(msg.Chat.ID)
	if len(running) > 0 {
		sb.WriteString("\nWorking now:\n")
		for _, s := range running {
			slot := agentSlot{Key: s, Display: s}
			if s == defaultAgentKey {
				slot.Display = defaultAgentDisplay
			} else if s == orchestratorSlot {
				slot.Display = orchestratorSlot
			} else {
				slot = b.resolveSlot(s)
				if slot.Display == "" {
					slot.Display = s
				}
			}
			mark := ""
			if s == focus.Key {
				mark = " (this chat)"
			}
			if snap, ok := b.slotSnapshot(msg.Chat.ID, s); ok {
				elapsed := time.Since(snap.Started).Truncate(time.Second)
				gpu := ""
				if snap.GPU != "" {
					gpu = " — " + snap.GPU
				}
				last := ""
				if n := len(snap.Lines); n > 0 {
					last = " — " + snap.Lines[n-1]
				}
				fmt.Fprintf(&sb, "- @%s%s — %s, %d actions%s%s\n", slot.Display, mark, elapsed, snap.Actions, gpu, last)
			} else {
				fmt.Fprintf(&sb, "- @%s%s\n", slot.Display, mark)
			}
		}
		sb.WriteString("\nPeek without cancelling: /progress or @Name with no extra text.\n")
		sb.WriteString("A new task message will not cancel a running agent — use /stop first.\n")
	} else {
		sb.WriteString("\nNo agent is working in the background.\n")
	}
	sb.WriteString("\nType / to scroll commands. Type @ and send it, or /agents, for the agent list.")
	b.sendMessage(msg.Chat.ID, sb.String())
}

func (b *Bot) sendFocusMessage(chatID int64, turn agentSlot) {
	text := b.switchMessage(chatID, turn)
	if snap, ok := b.slotSnapshot(chatID, turn.Key); ok {
		text += "\n\n" + formatRunReport(turn, snap, true)
	} else {
		text += "\n\n" + formatIdleStatus(turn.Display)
	}
	b.sendMessage(chatID, text)
}

func (b *Bot) handleProgress(msg *tgbotapi.Message) {
	args := strings.TrimSpace(msg.CommandArguments())
	sendOne := func(slot agentSlot) {
		snap, ok := b.slotSnapshot(msg.Chat.ID, slot.Key)
		if !ok {
			b.sendMessage(msg.Chat.ID, formatIdleStatus(slot.Display))
			return
		}
		b.sendMessage(msg.Chat.ID, formatRunReport(slot, snap, true))
	}
	switch {
	case args == "":
		sendOne(b.getFocus(msg.Chat.ID))
	case strings.EqualFold(args, "all"):
		focus := b.getFocus(msg.Chat.ID)
		slots := b.runningSlots(msg.Chat.ID)
		if len(slots) == 0 {
			b.sendMessage(msg.Chat.ID, "Nothing is working right now.\n"+formatIdleStatus(focus.Display)+"\n\nOnly in-flight agents appear on /progress all. Idle specialists are omitted.")
			return
		}
		type row struct {
			slot agentSlot
			snap runSnapshot
		}
		var rows []row
		for _, s := range slots {
			slot := agentSlot{Key: s, Display: s}
			if s == defaultAgentKey {
				slot.Display = defaultAgentDisplay
			} else if s == orchestratorSlot {
				slot.Display = orchestratorSlot
			} else {
				slot = b.resolveSlot(s)
			}
			snap, _ := b.slotSnapshot(msg.Chat.ID, slot.Key)
			if snap.Display == "" {
				snap.Display = slot.Display
			}
			rows = append(rows, row{slot: slot, snap: snap})
		}
		sort.SliceStable(rows, func(i, j int) bool {
			return gpuQueueRank(rows[i].snap.GPU) < gpuQueueRank(rows[j].snap.GPU)
		})
		var q strings.Builder
		q.WriteString("GPU queue (one Qwen generation at a time):\n")
		for _, r := range rows {
			name := r.snap.Display
			if name == "" {
				name = r.slot.Display
			}
			if r.snap.GPU != "" {
				fmt.Fprintf(&q, "- @%s — %s\n", name, r.snap.GPU)
			} else {
				fmt.Fprintf(&q, "- @%s — tools/other (not waiting on GPU)\n", name)
			}
		}
		focusWorking := false
		for _, r := range rows {
			if r.slot.Key == focus.Key {
				focusWorking = true
				break
			}
		}
		q.WriteString(progressAllNote(focus.Display, focusWorking))
		b.sendMessage(msg.Chat.ID, q.String())
		for _, r := range rows {
			sendOne(r.slot)
		}
	default:
		handle := strings.TrimPrefix(args, "@")
		if p := parseAgentCall("@" + handle); p.Mentioned {
			handle = p.Handle
		}
		if strings.EqualFold(handle, orchestratorSlot) {
			sendOne(agentSlot{Key: orchestratorSlot, Display: orchestratorSlot})
			return
		}
		sendOne(b.resolveSlot(handle))
	}
}

func (b *Bot) handleDefaultChat(msg *tgbotapi.Message) {
	turn := agentSlot{Key: defaultAgentKey, Display: defaultAgentDisplay}
	b.setFocus(msg.Chat.ID, turn)
	b.sendFocusMessage(msg.Chat.ID, turn)
}

func (b *Bot) handleAgentCatalog(msg *tgbotapi.Message) {
	focus := b.getFocus(msg.Chat.ID)
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Chatting with @%s", focus.Display))
	b.sendAgentPicker(msg.Chat.ID)
}

func (b *Bot) tryAgentSlashCommand(ctx context.Context, msg *tgbotapi.Message) bool {
	if b.skillsManager == nil {
		return false
	}
	ag, ok := b.skillsManager.FindChatAgent(msg.Command())
	if !ok {
		return false
	}
	turn := b.resolveSlot(ag.Handle)
	b.setFocus(msg.Chat.ID, turn)
	rest := strings.TrimSpace(msg.CommandArguments())
	if rest == "" {
		b.sendFocusMessage(msg.Chat.ID, turn)
		return true
	}
	var peopleNames []string
	if b.peopleManager != nil {
		if names, err := b.peopleManager.MatchNames(ctx, rest); err == nil {
			peopleNames = names
		}
	}
	scope := b.usageScope(msg.Chat.ID, turn)
	if !b.admitOrExplain(msg.Chat.ID, scope, 1000, func() {
		b.processMessageWith(context.Background(), msg, rest, "", peopleNames, turn)
	}) {
		return true
	}
	b.processMessageWith(ctx, msg, rest, "", peopleNames, turn)
	return true
}

func (b *Bot) handleCost(msg *tgbotapi.Message) {
	var lines []string
	lines = append(lines, "*Context windows*", "Each conversation is its own input. They are not added together.", "")
	if b.limiter != nil {
		prefix := fmt.Sprintf("tg-%d", msg.Chat.ID)
		n := 0
		for _, c := range b.limiter.GetStatus().Conversations {
			if c.SessionID != prefix && !strings.HasPrefix(c.SessionID, prefix+"-ag-") {
				continue
			}
			n++
			model := c.Model
			if model == "" {
				model = c.Framework
			}
			lines = append(lines, fmt.Sprintf("%s · %s · %s / %s · out %s this turn",
				mdPlain(c.SessionID), mdPlain(model),
				format.Tokens(c.ContextTokens), format.Tokens(c.ContextWindow),
				format.Tokens(c.OutputTokens)))
		}
		if n == 0 {
			lines = append(lines, "(no conversation counters yet)")
		}
	}

	b.sessionsMu.RLock()
	usage, ok := b.sessions[msg.Chat.ID]
	b.sessionsMu.RUnlock()
	if ok {
		duration := time.Since(usage.StartedAt).Round(time.Minute)
		lines = append(lines, "",
			"*Billed this chat (sum of API calls, not a context window)*",
			fmt.Sprintf("Requests: %d", usage.Requests),
			fmt.Sprintf("Input tokens: %s", format.Tokens(usage.InputTokens)),
			fmt.Sprintf("Output tokens: %s", format.Tokens(usage.OutputTokens)),
			fmt.Sprintf("Session duration: %s", duration.String()),
		)
	}
	if len(lines) == 3 {
		b.sendMessage(msg.Chat.ID, "No usage recorded for this session yet.")
		return
	}
	b.sendMarkdownMessage(msg.Chat.ID, strings.Join(lines, "\n"))
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

func parseReviewTeamRequest(text string) []string {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" || len(t) > 280 {
		return nil
	}
	if strings.HasPrefix(t, "i ran") || strings.HasPrefix(t, "i have run") || strings.Contains(t, "couldn't") || strings.Contains(t, "could not") {
		return nil
	}
	launch := strings.HasPrefix(t, "run ") || strings.Contains(t, "run the") ||
		strings.Contains(t, "please run") || strings.Contains(t, "launch the") ||
		strings.HasPrefix(t, "review ")
	if !launch {
		return nil
	}
	var teams []string
	add := func(name string) {
		for _, x := range teams {
			if x == name {
				return
			}
		}
		teams = append(teams, name)
	}
	if strings.Contains(t, "go-dev") || strings.Contains(t, "go dev") {
		add("go-dev-team")
	}
	if strings.Contains(t, "go-sec") || strings.Contains(t, "go sec") || strings.Contains(t, "go-security") {
		add("go-sec-team")
	}
	if strings.Contains(t, "fe-dev") || strings.Contains(t, "fe dev") {
		add("fe-dev-team")
	}
	if strings.Contains(t, "fe-sec") || strings.Contains(t, "fe sec") {
		add("fe-sec-team")
	}
	return teams
}

func (b *Bot) handleOrchestratorTeam(ctx context.Context, msg *tgbotapi.Message, teamName string) {
	task := strings.TrimSpace(msg.CommandArguments())
	if task == "" {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Usage: /reviewgo or /reviewgosec <task description>\nOr: run the %s on <what to review>", teamName))
		return
	}
	b.handleOrchestratorTeamWithTask(ctx, msg, teamName, task)
}

// bindStoppable registers a cancel func so /stop can abort this work.
// Orchestrator commands previously used the process-lifetime context, so
// isolated vLLM requests kept the GPU busy until docker compose down.
func (b *Bot) bindStoppable(ctx context.Context, chatID int64, slot string) (context.Context, func()) {
	reqCtx, cancel := context.WithCancel(ctx)
	unbind := b.replaceCancel(reqKey(chatID, slot), cancel)
	return reqCtx, unbind
}

func (b *Bot) handleOrchestratorTeamWithTask(ctx context.Context, msg *tgbotapi.Message, teamName, task string) {
	if b.orchestrator == nil {
		b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
		return
	}
	task = strings.TrimSpace(task)
	if task == "" {
		task = "Review the current uncommitted work."
	}

	fw := ""
	if sw, ok := b.agent.(*agent.Switcher); ok {
		fw = sw.Active()
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Running %s via %s...", teamName, fw))

	reqCtx, unbind := b.bindStoppable(ctx, msg.Chat.ID, orchestratorSlot)
	defer unbind()

	result, err := b.orchestrator.RunTeamStreaming(reqCtx, teamName, task,
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

	reqCtx, unbind := b.bindStoppable(ctx, msg.Chat.ID, orchestratorSlot)
	defer unbind()

	results, err := b.orchestrator.RunGauntlet(reqCtx, task, "",
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

	reqCtx, unbind := b.bindStoppable(ctx, msg.Chat.ID, orchestratorSlot)
	defer unbind()

	result, err := b.orchestrator.RunTeamStreaming(reqCtx, teamName, task,
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

func (b *Bot) handleSync(msg *tgbotapi.Message) {
	if b.agentSyncer == nil {
		b.sendMessage(msg.Chat.ID, "Agent syncer not initialized.")
		return
	}

	b.sendMessage(msg.Chat.ID, "Syncing agents & commands between Claude ↔ OpenCode...")

	stats := b.agentSyncer.FullSync()

	text := fmt.Sprintf(
		"Sync complete.\n\n"+
			"Agents:\n"+
			"  Claude → OpenCode: %d synced\n"+
			"  OpenCode → Claude: %d synced\n"+
			"  Claude total: %d\n"+
			"  OpenCode total: %d\n\n"+
			"Commands:\n"+
			"  Claude → OpenCode: %d synced\n"+
			"  OpenCode → Claude: %d synced",
		stats.ClaudeToOpenCodeAgents,
		stats.OpenCodeToClaudeAgents,
		stats.ClaudeAgentsTotal,
		stats.OpenCodeAgentsTotal,
		stats.ClaudeToOpenCodeCmds,
		stats.OpenCodeToClaudeCmds,
	)
	b.sendMessage(msg.Chat.ID, text)
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

	turn := agentSlot{Key: defaultAgentKey, Display: defaultAgentDisplay}
	if skill.Owner != "" {
		turn = b.resolveSlot(skill.Owner)
		b.setFocus(msg.Chat.ID, turn)
	}

	sessionID := sessionIDFor(msg.Chat.ID, turn.Key)
	be, _ := b.backendFor(msg.Chat.ID, turn)
	_ = be.ResetSession(ctx, sessionID)
	if turn.Key == defaultAgentKey {
		b.sessionsMu.Lock()
		delete(b.sessions, msg.Chat.ID)
		b.sessionsMu.Unlock()
	}

	var parts []string
	if turn.Key != defaultAgentKey {
		parts = append(parts, namedAgentSystemPrompt(turn))
		if len(turn.Skills) > 0 {
			parts = append(parts, skills.FormatSkillsXML(turn.Skills))
		}
	}
	if b.memoryManager != nil {
		if section := b.memoryManager.BuildSystemPromptSection(ctx); section != "" {
			parts = append(parts, section)
		}
	}
	parts = append(parts, skills.FormatSkillsXML([]skills.Skill{skill}))
	systemPrompt := strings.Join(parts, "\n\n")

	scope := b.usageScope(msg.Chat.ID, turn)
	if !b.admitOrExplain(msg.Chat.ID, scope, 1000, func() {
		b.processMessageWith(context.Background(), msg, args, systemPrompt, nil, turn)
	}) {
		return
	}

	b.processMessageWith(ctx, msg, args, systemPrompt, nil, turn)
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

	var buf strings.Builder
	agents, agErr := b.pgDB.ListAgents(ctx, true)
	if agErr != nil {
		fmt.Fprintf(&buf, "Agents query failed: %v\n\n", agErr)
	} else {
		fmt.Fprintf(&buf, "PostgreSQL agents (%d active):\n", len(agents))
		for _, a := range agents {
			fmt.Fprintf(&buf, "  #%d @%s (%s)\n", a.ID, a.Handle, a.Source)
		}
		buf.WriteByte('\n')
	}

	convs, err := b.pgDB.RecentConversationsAllSessions(ctx, 10)
	if err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("%sQuery failed: %v", buf.String(), err))
		return
	}

	if len(convs) == 0 {
		buf.WriteString("No conversations in PostgreSQL. RecordExchange may not be saving.")
		b.sendMessage(msg.Chat.ID, buf.String())
		return
	}

	fmt.Fprintf(&buf, "Last %d messages:\n\n", len(convs))
	for _, c := range convs {
		content := c.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		agent := c.AgentHandle
		if agent == "" {
			agent = "?"
		}
		fmt.Fprintf(&buf, "[%s] @%s | %s | %s: %s\n",
			c.CreatedAt.Format("15:04:05"), agent, c.SessionID, c.Role, content)
	}
	b.sendMessage(msg.Chat.ID, buf.String())
}

// handleCronAdd creates a new cron job at runtime.
// Usage: /cronadd name "schedule" [@agent] [model] prompt
func (b *Bot) handleCronAdd(msg *tgbotapi.Message) {
	if b.cronScheduler == nil {
		b.sendMessage(msg.Chat.ID, "Cron scheduler not available.")
		return
	}

	args := msg.CommandArguments()
	usage := "Usage: /cronadd name \"schedule\" [@agent] [model] prompt\n\n" +
		"Examples:\n" +
		"/cronadd standup \"30 9 * * 1-5\" Check my calendar\n" +
		"/cronadd seo_links \"0 8 * * 1\" @grey-website deepseek-flash Audit internal linking\n" +
		"/cronadd fx_health \"0 7 * * 1-5\" @forex-trading qwen Summarise uncommitted work"
	if args == "" {
		b.sendMessage(msg.Chat.ID, usage)
		return
	}

	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		b.sendMessage(msg.Chat.ID, usage)
		return
	}
	name := parts[0]
	rest := parts[1]

	if !strings.HasPrefix(rest, "\"") {
		b.sendMessage(msg.Chat.ID, "Schedule must be in quotes: /cronadd name \"0 9 * * *\" [@agent] [model] prompt")
		return
	}
	endQuote := strings.Index(rest[1:], "\"")
	if endQuote < 0 {
		b.sendMessage(msg.Chat.ID, "Missing closing quote for schedule.")
		return
	}
	schedule := rest[1 : endQuote+1]
	tail := strings.TrimSpace(rest[endQuote+2:])
	agentHandle, framework, prompt := parseCronAddTail(tail)

	if prompt == "" {
		b.sendMessage(msg.Chat.ID, "Prompt cannot be empty.")
		return
	}

	job := config.CronJob{
		Schedule:  schedule,
		Prompt:    prompt,
		Enabled:   true,
		Agent:     agentHandle,
		Framework: framework,
	}
	if err := b.cronScheduler.AddDynamic(name, job); err != nil {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Failed: %v", err))
		return
	}

	var extra strings.Builder
	if agentHandle != "" {
		fmt.Fprintf(&extra, "\nAgent: @%s", agentHandle)
	}
	if framework != "" {
		fmt.Fprintf(&extra, "\nModel: %s (`%s`)", frameworkLabel(framework), b.modelForFramework(framework))
	} else {
		extra.WriteString("\nModel: CRON_FRAMEWORK default (Qwen)")
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Cron job %q added.\nSchedule: %s%s\nPrompt: %s", name, schedule, extra.String(), prompt))
}

// parseCronAddTail reads optional @agent and model token, then the prompt.
func parseCronAddTail(rest string) (agentHandle, framework, prompt string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", ""
	}
	if strings.HasPrefix(rest, "@") {
		p := parseAgentCall(rest)
		if p.Mentioned {
			agentHandle = p.Handle
			rest = p.Rest
		}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return agentHandle, "", ""
	}
	tok := strings.ToLower(strings.Trim(fields[0], "/"))
	if _, ok := lookupSelectable(tok); ok {
		sel, _ := lookupSelectable(tok)
		framework = sel.Key
		prompt = strings.TrimSpace(rest[len(fields[0]):])
		return agentHandle, framework, prompt
	}
	return agentHandle, "", rest
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

// handleCronRun runs a configured job immediately (does not wait for the schedule).
func (b *Bot) handleCronRun(msg *tgbotapi.Message) {
	if b.cronScheduler == nil {
		b.sendMessage(msg.Chat.ID, "Cron scheduler not available.")
		return
	}
	name := strings.TrimSpace(msg.CommandArguments())
	if name == "" {
		b.sendMessage(msg.Chat.ID, "Usage: /cronrun name\n\nTry /cronlist for job names.")
		return
	}
	if err := b.cronScheduler.RunJob(name); err != nil {
		b.sendMessage(msg.Chat.ID, err.Error())
		return
	}
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("Running *%s* now (Qwen, not /framework). Result arrives when it finishes.", name))
}

// handleFramework shows the current AI framework or switches to a new one.
//
//	/framework          — show current + available
//
// handleModel shows the actual API model from the last response, not a
// self-reported model identity. This is immune to conversation history
// pollution and always reflects what the API actually returned.
func (b *Bot) handleModel(msg *tgbotapi.Message) {
	turn := b.getFocus(msg.Chat.ID)
	framework := b.frameworkFor(msg.Chat.ID, turn)
	configured := b.modelForFramework(framework)

	b.lastAPIModelMu.RLock()
	apiModel := b.lastAPIModel[msg.Chat.ID]
	b.lastAPIModelMu.RUnlock()

	var text strings.Builder
	fmt.Fprintf(&text, "Agent: `@%s`\n", turn.Display)
	fmt.Fprintf(&text, "Framework: `%s` (%s)\n", framework, frameworkLabel(framework))
	fmt.Fprintf(&text, "Configured: `%s`\n", configured)
	if ov := b.slotFrameworkOverride(msg.Chat.ID, turn.Key); ov != "" {
		text.WriteString("Source: selected for this agent; processes it starts use it (`/models inherit` to drop)\n")
	} else if turn.Framework != "" {
		text.WriteString("Source: agent YAML pin\n")
	} else {
		text.WriteString("Source: gateway default (Qwen)\n")
	}
	if apiModel != "" {
		fmt.Fprintf(&text, "Last API model: `%s`", apiModel)
	} else {
		text.WriteString("Last API model: (no request yet — send a message first)")
	}

	b.sendMarkdownMessage(msg.Chat.ID, text.String())
}

// selectableModel is one /models button. Key is unique (callback + slot
// override). Backend is the switcher provider (deepseek/grok/qwen). Two
// DeepSeek rows share Backend and differ by Model.
type selectableModel struct {
	Label   string
	Key     string
	Backend string
	Model   string
}

var selectableModels = []selectableModel{
	{"DeepSeek V4.1 Flash", "deepseek-flash", "deepseek", "deepseek-flash"},
	{"DeepSeek V4 Pro", "deepseek", "deepseek", "deepseek-v4-pro"},
	{"Grok 4.5", "grok", "grok", "grok-4.5"},
	{"Qwen3.8 (local)", "qwen", "qwen", "qwen3.8-27b"},
}

// ResolveSelectable maps a /models name or YAML framework pin to provider + vendor model.
func ResolveSelectable(name string) (key, backend, model, label string, ok bool) {
	m, ok := lookupSelectable(name)
	if !ok {
		return "", "", "", "", false
	}
	return m.Key, m.Backend, m.Model, m.Label, true
}

func lookupSelectable(name string) (selectableModel, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "flash", "v4.1-flash", "v4.1", "deepseek-v4.1-flash", "deepseek-v4-flash", "deepseekflash":
		n = "deepseek-flash"
	}
	var byBackend *selectableModel
	for i := range selectableModels {
		m := &selectableModels[i]
		if n == m.Key || n == m.Model || strings.EqualFold(n, m.Label) {
			return *m, true
		}
		if n == m.Backend && byBackend == nil {
			byBackend = m
		}
	}
	if byBackend != nil {
		return *byBackend, true
	}
	return selectableModel{}, false
}

// providerOf maps a slot override / YAML pin (e.g. deepseek-flash) to the
// switcher backend name (deepseek).
func providerOf(name string) string {
	if m, ok := lookupSelectable(name); ok {
		return m.Backend
	}
	switch name {
	case "ollama", "vllm":
		return "qwen"
	default:
		return name
	}
}

// modelForFramework returns the configured/default model string for a backend.
func (b *Bot) modelForFramework(framework string) string {
	return configuredModelName(framework, b.cfg.Sidecar.DefaultModel, b.cfg.OpenCode.Model, b.cfg.OllamaChatModel, b.cfg.VLLMModel)
}

func configuredModelName(framework, claudeDefault, opencodeDefault, ollamaChat, vllmModel string) string {
	if m, ok := lookupSelectable(framework); ok {
		if m.Key == "qwen" || m.Backend == "qwen" {
			// fall through for vLLM/Ollama env overrides
		} else {
			return m.Model
		}
	}
	switch framework {
	case "deepseek-flash":
		return "deepseek-flash"
	case "deepseek":
		return "deepseek-v4-pro"
	case "grok":
		return "grok-4.5"
	case "qwen", "ollama", "vllm":
		if vllmModel != "" {
			return vllmModel
		}
		if ollamaChat != "" {
			return ollamaChat
		}
		return "qwen3.8-27b"
	case "opencode":
		return opencodeDefault
	default:
		return claudeDefault
	}
}

// /models — show inline keyboard to pick a direct provider (DeepSeek / Grok).
func (b *Bot) handleModels(msg *tgbotapi.Message) {
	// Quick-switch via argument: /models grok  or  /models deepseek
	if arg := strings.TrimSpace(msg.CommandArguments()); arg != "" {
		b.switchSelectableModel(msg.Chat.ID, arg)
		return
	}

	sw, ok := b.agent.(*agent.Switcher)
	if !ok {
		b.sendMessage(msg.Chat.ID, "Framework switching not available.")
		return
	}

	turn := b.getFocus(msg.Chat.ID)
	current := b.frameworkFor(msg.Chat.ID, turn)

	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton
	shown := 0
	for _, m := range selectableModels {
		if !sw.Has(m.Backend) {
			continue
		}
		shown++
		label := m.Label
		if m.Key == current {
			label = "✅ " + label
		}
		btn := tgbotapi.NewInlineKeyboardButtonData(label, "model:"+m.Key)
		row = append(row, btn)
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if shown == 0 {
		b.sendMessage(msg.Chat.ID, "No direct providers registered.\nSet DEEPSEEK_API_KEY, GROK_API_KEY, and/or OLLAMA_CHAT_MODEL, then recreate the gateway.")
		return
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("For @%s: `%s` — `%s`\nPick a model. It applies to this agent and to every process it starts. Gateway default stays Qwen.\n/models inherit to drop the selection.", turn.Display, current, b.modelForFramework(current)))
	reply.ParseMode = tgbotapi.ModeMarkdown
	reply.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	if _, err := b.api.Send(reply); err != nil {
		log.Printf("Failed to send models keyboard: %v", err)
	}
}

// switchSelectableModel switches the active sidecar provider (deepseek / grok).
func (b *Bot) switchSelectableModel(chatID int64, name string) {
	sw, ok := b.agent.(*agent.Switcher)
	if !ok {
		b.sendMessage(chatID, "Framework switching not available.")
		return
	}

	name = strings.ToLower(strings.TrimSpace(name))
	turn := b.getFocus(chatID)
	if name == "inherit" || name == "reset" {
		b.setSlotFramework(chatID, turn.Key, "")
		fw := b.frameworkFor(chatID, turn)
		b.sendMarkdownMessage(chatID, fmt.Sprintf("@%s now inherits *%s* (`%s`).", turn.Display, frameworkLabel(fw), b.modelForFramework(fw)))
		return
	}
	if name == "ollama" || name == "vllm" || strings.HasPrefix(name, "qwen3.8") {
		name = "qwen"
	}
	sel, okSel := lookupSelectable(name)
	backend, key, label := name, name, name
	if okSel {
		backend, key, label = sel.Backend, sel.Key, sel.Label
	}

	if !sw.Has(backend) {
		hint := "Set the matching key (DEEPSEEK_API_KEY, GROK_API_KEY, or OLLAMA_CHAT_MODEL) and recreate the gateway."
		b.sendMessage(chatID, fmt.Sprintf("*%s* is not registered.\n%s", backend, hint))
		return
	}

	old := b.frameworkFor(chatID, turn)
	if old == key {
		b.setSlotFramework(chatID, turn.Key, key)
		b.sendMarkdownMessage(chatID, fmt.Sprintf("@%s already using *%s* (`%s`).", turn.Display, label, b.modelForFramework(key)))
		return
	}
	b.setSlotFramework(chatID, turn.Key, key)
	b.sendMarkdownMessage(chatID, fmt.Sprintf("@%s: `%s` → *%s* (`%s`)\nThis agent and every process it starts, including quality control and repair. Gateway default stays Qwen. /models inherit to undo.", turn.Display, old, label, b.modelForFramework(key)))
}

// /framework claude   — switch to Claude Code
// /framework opencode — switch to OpenCode
func (b *Bot) handleFramework(_ context.Context, msg *tgbotapi.Message) {
	switcher, ok := b.agent.(*agent.Switcher)
	if !ok {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Framework switching not available.\nActive backend: *%s*", b.agent.Name()))
		return
	}

	arg := strings.TrimSpace(msg.CommandArguments())

	turn := b.getFocus(msg.Chat.ID)
	if arg == "" {
		active := b.frameworkFor(msg.Chat.ID, turn)
		available := switcher.Available()
		models := map[string]string{
			"claude":         b.cfg.Sidecar.DefaultModel,
			"opencode":       b.cfg.OpenCode.Model,
			"deepseek":       "deepseek-v4-pro",
			"deepseek-flash": "deepseek-flash",
			"grok":           "grok-4.5",
			"qwen":           b.modelForFramework("qwen"),
		}
		var lines []string
		lines = append(lines, fmt.Sprintf("*@%s framework:* %s (`%s`)", turn.Display, active, b.modelForFramework(active)))
		lines = append(lines, fmt.Sprintf("Gateway default: `%s` (unchanged by this command)", switcher.Active()))
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
		lines = append(lines, "Switch this agent: /framework <name>")
		lines = append(lines, "Drop override: /framework inherit")
		b.sendMessage(msg.Chat.ID, strings.Join(lines, "\n"))
		return
	}

	arg = strings.ToLower(arg)
	if arg == "inherit" || arg == "reset" {
		b.setSlotFramework(msg.Chat.ID, turn.Key, "")
		fw := b.frameworkFor(msg.Chat.ID, turn)
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("@%s inherits *%s*.", turn.Display, fw))
		return
	}
	if arg == "ollama" || arg == "vllm" || strings.HasPrefix(arg, "qwen3.8") {
		arg = "qwen"
	}
	if sel, ok := lookupSelectable(arg); ok {
		arg = sel.Key
	}
	if !switcher.Has(providerOf(arg)) {
		b.sendMessage(msg.Chat.ID, fmt.Sprintf("Unknown backend: %s (available: %v)", arg, switcher.Available()))
		return
	}
	old := b.frameworkFor(msg.Chat.ID, turn)
	b.setSlotFramework(msg.Chat.ID, turn.Key, arg)
	b.sendMessage(msg.Chat.ID, fmt.Sprintf("@%s: `%s` → *%s* (`%s`). This agent and every process it starts. Gateway default stays Qwen.", turn.Display, old, arg, b.modelForFramework(arg)))
}

// sendCommandMenu sends a grid of tappable inline buttons for quick access to commands.
// Triggered by typing "." (period).
func (b *Bot) sendCommandMenu(msg *tgbotapi.Message) {
	rows := [][]tgbotapi.InlineKeyboardButton{
		{
			tgbotapi.NewInlineKeyboardButtonData("📋 Tasks", "menu:tasks"),
			tgbotapi.NewInlineKeyboardButtonData("📊 Status", "menu:status"),
			tgbotapi.NewInlineKeyboardButtonData("✅ Todos", "menu:todos"),
		},
		{
			tgbotapi.NewInlineKeyboardButtonData("🤖 Models", "menu:models"),
			tgbotapi.NewInlineKeyboardButtonData("🔨 Build", "menu:build"),
			tgbotapi.NewInlineKeyboardButtonData("📐 Plan", "menu:plan"),
		},
		{
			tgbotapi.NewInlineKeyboardButtonData("⏰ Cron", "menu:cronlist"),
			tgbotapi.NewInlineKeyboardButtonData("🧹 Clear", "menu:clear"),
			tgbotapi.NewInlineKeyboardButtonData("🔊 Verbose", "menu:verbose"),
		},
		{
			tgbotapi.NewInlineKeyboardButtonData("💓 Heartbeat", "menu:heartbeat"),
			tgbotapi.NewInlineKeyboardButtonData("📂 CWD", "menu:cwd"),
			tgbotapi.NewInlineKeyboardButtonData("💰 Cost", "menu:cost"),
		},
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, "Pick a command:")
	reply.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	if _, err := b.api.Send(reply); err != nil {
		log.Printf("Failed to send command menu: %v", err)
	}
}
