// Package ctxbuilder assembles system prompts and enriched user prompts from
// PostgreSQL-backed memory, conversation history, people, and user profile.
// Named ctxbuilder (not context) to avoid shadowing the stdlib context package.
package ctxbuilder

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/format"
	"github.com/you/claude-gateway/internal/journal"
	"github.com/you/claude-gateway/internal/ollama"
	"github.com/you/claude-gateway/internal/people"
	"github.com/you/claude-gateway/internal/pgdb"
	"github.com/you/claude-gateway/internal/profile"
)

// Builder assembles context from PostgreSQL for injection into Claude prompts.
type Builder struct {
	db             *pgdb.DB
	embedder       *ollama.Client
	peopleMgr      *people.Manager
	profiler       *profile.Profiler
	journalMgr     *journal.Manager
	recentTurns    int
	replayMessages int
	semanticHits   int
	minSimilarity  float64
}

// NewBuilder creates a new context builder. Panics if db is nil because
// every Builder method depends on a valid database connection.
func NewBuilder(db *pgdb.DB, embedder *ollama.Client, peopleMgr *people.Manager, profiler *profile.Profiler, journalMgr *journal.Manager, cfg config.ContextConfig) *Builder {
	if db == nil {
		panic("ctxbuilder: nil db")
	}
	return &Builder{
		db:             db,
		embedder:       embedder,
		peopleMgr:      peopleMgr,
		profiler:       profiler,
		journalMgr:     journalMgr,
		recentTurns:    cfg.RecentTurns,
		replayMessages: cfg.ReplayMessages,
		semanticHits:   cfg.SemanticHits,
		minSimilarity:  cfg.MinSimilarity,
	}
}

// BuildSystemPrompt assembles the full system prompt from DB-backed memory.
// Called only when the SDK session is dead/new.
// Includes: bootstrap, self, preferences, user profile, and last N messages.
func (b *Builder) BuildSystemPrompt(ctx context.Context, sessionID string) (string, error) {
	var parts []string

	// 1. User profile aspects (confidence >= 0.4)
	if b.profiler != nil {
		if profileSection, err := b.profiler.FormatProfileForContext(ctx); err == nil && profileSection != "" {
			parts = append(parts, profileSection)
		}
	}

	// 2. Memory entries by category: self → bootstrap → preference
	entries, err := b.db.GetActiveMemory(ctx)
	if err != nil {
		log.Printf("Context builder: failed to load memory entries: %v", err)
	} else {
		selfEntries, bootstrapEntries, prefEntries := categorizeEntries(entries)

		if section := formatMemorySection("self_context", selfEntries); section != "" {
			parts = append(parts, section)
		}
		if section := formatMemorySection("bootstrap_instructions", bootstrapEntries); section != "" {
			parts = append(parts, section)
		}
		if section := formatMemorySection("user_preferences", prefEntries); section != "" {
			parts = append(parts, section)
		}
	}

	// 3. Last N messages (count-based replay for continuity).
	// Scoped to this agent/session so @SEOagent cannot see @YouTubeagent turns.
	var recentConvs []pgdb.Conversation
	if sessionID != "" {
		recentConvs, err = b.db.RecentConversations(ctx, sessionID, b.replayMessages)
	} else {
		recentConvs, err = b.db.RecentConversationsAllSessions(ctx, b.replayMessages)
	}
	if err != nil {
		log.Printf("Context builder: failed to load recent conversations: %v", err)
	} else {
		log.Printf("Context builder: found %d recent conversations for replay", len(recentConvs))
	}
	if err == nil && len(recentConvs) > 0 {
		var convBuf strings.Builder
		convBuf.WriteString("<recent_conversation_history>\n")
		convBuf.WriteString("<!-- Last messages replayed for continuity -->\n")
		for _, c := range recentConvs {
			timestamp := c.CreatedAt.Format("2006-01-02 15:04")
			escapedContent := format.CDATAEscape(c.Content)
			fmt.Fprintf(&convBuf, "<msg role=\"%s\" time=\"%s\"><![CDATA[%s]]></msg>\n",
				c.Role, timestamp, escapedContent)
		}
		convBuf.WriteString("</recent_conversation_history>")
		parts = append(parts, convBuf.String())
	}

	if len(parts) == 0 {
		return "", nil
	}

	return strings.Join(parts, "\n\n"), nil
}

// BuildEnrichedPrompt assembles per-message context from recent turns,
// semantic search, people, facts, and journal entries.
// This is prepended to the user's message on every request.
// maxEnrichedPromptChars is the total character budget for the enriched prompt.
// Prevents unbounded growth from concatenating many sections.
const maxEnrichedPromptChars = 32000

// EnrichmentOptions controls how much conversation is packed into the
// per-message prompt. DeepSeek/Grok use the defaults; Qwen uses Wide
// because it has no Claude Code session to remember prior turns.
type EnrichmentOptions struct {
	Budget         int
	RecentTurns    int
	KeepFullTurns  int
	FullTurnChars  int
	OlderTurnChars int
}

func defaultEnrichment() EnrichmentOptions {
	return EnrichmentOptions{
		Budget:         maxEnrichedPromptChars,
		KeepFullTurns:  4,
		FullTurnChars:  3000,
		OlderTurnChars: 500,
	}
}

// QwenWideEnrichment: vLLM max_model_len is 131072 and 16k is reserved
// for output, so input must stay under ~114k tokens. Keep recent chat
// small enough that tools+system still fit.
func QwenWideEnrichment() EnrichmentOptions {
	return EnrichmentOptions{
		Budget:         40000,
		RecentTurns:    16,
		KeepFullTurns:  6,
		FullTurnChars:  2000,
		OlderTurnChars: 600,
	}
}

func (b *Builder) BuildEnrichedPrompt(ctx context.Context, sessionID, userMessage string, peopleNames []string, sessionAlive bool) (string, error) {
	return b.BuildEnrichedPromptWith(ctx, sessionID, userMessage, peopleNames, sessionAlive, defaultEnrichment())
}

func (b *Builder) BuildEnrichedPromptWith(ctx context.Context, sessionID, userMessage string, peopleNames []string, sessionAlive bool, opts EnrichmentOptions) (string, error) {
	if opts.Budget <= 0 {
		opts.Budget = maxEnrichedPromptChars
	}
	if opts.KeepFullTurns <= 0 {
		opts.KeepFullTurns = 4
	}
	if opts.FullTurnChars <= 0 {
		opts.FullTurnChars = 3000
	}
	if opts.OlderTurnChars <= 0 {
		opts.OlderTurnChars = 500
	}
	budget := opts.Budget
	if len(userMessage) < 50 && budget > 8000 && opts.Budget == maxEnrichedPromptChars {
		budget = 8000
	}

	var parts []string
	totalLen := 0

	// addSection appends a section if within the character budget.
	// Returns false when the budget is exhausted.
	addSection := func(section string) bool {
		if totalLen+len(section) > budget {
			return false
		}
		parts = append(parts, section)
		totalLen += len(section)
		return true
	}

	// 0. Bootstrap instructions — now injected via SessionStart hook.
	// The hook fires on startup, resume, compact, and clear events,
	// so bootstrap content survives compaction without per-message re-injection.
	// This saves ~5,850 tokens per message on alive sessions.

	// 1. Recent conversation turns (current session)
	nRecent := opts.RecentTurns
	if nRecent <= 0 {
		nRecent = b.recentTurns
	}
	recentConvs, err := b.db.RecentConversations(ctx, sessionID, nRecent)
	if err != nil {
		log.Printf("Context builder: failed to load recent turns: %v", err)
	} else if len(recentConvs) > 0 {
		var convBuf strings.Builder
		convBuf.WriteString("<conversation_history>\n")
		for i, c := range recentConvs {
			timestamp := c.CreatedAt.Format("15:04")
			// TruncateTail keeps the END of the message — questions,
			// action items, and "shall I do X?" are always at the end.
			limit := opts.OlderTurnChars
			if i >= len(recentConvs)-opts.KeepFullTurns {
				limit = opts.FullTurnChars
			}
			content := format.TruncateTail(c.Content, limit)
			fmt.Fprintf(&convBuf, "<turn role=\"%s\" time=\"%s\">%s</turn>\n",
				c.Role, timestamp, format.CDATAEscape(content))
		}
		convBuf.WriteString("</conversation_history>")
		addSection(convBuf.String())
	}

	// 2. Semantic search (older relevant messages, excluding recency window)
	if totalLen < budget && b.embedder != nil && userMessage != "" {
		emb, err := b.embedder.Embed(ctx, userMessage)
		if err == nil {
			// Collect IDs from recency window to exclude
			var excludeIDs []int64
			for _, c := range recentConvs {
				excludeIDs = append(excludeIDs, c.ID)
			}

			hits, err := b.db.SearchSimilar(ctx, emb, excludeIDs, b.semanticHits, b.minSimilarity, sessionID)
			if err != nil {
				log.Printf("Context builder: semantic search failed: %v", err)
			} else if len(hits) > 0 {
				var hitBuf strings.Builder
				hitBuf.WriteString("<related_past_conversations>\n")
				for _, h := range hits {
					date := h.CreatedAt.Format("2006-01-02")
					content := format.Truncate(h.Content, 500)
					fmt.Fprintf(&hitBuf, "<hit similarity=\"%.2f\" date=\"%s\">\n[%s]: %s\n</hit>\n",
						h.Similarity, date, strings.ToUpper(h.Role), format.CDATAEscape(content))
				}
				hitBuf.WriteString("</related_past_conversations>")
				addSection(hitBuf.String())
			}
		}
	}

	// 3. People context (from names extracted by triager)
	if totalLen < budget && b.peopleMgr != nil && len(peopleNames) > 0 {
		matches, err := b.peopleMgr.FindPeople(ctx, peopleNames)
		if err != nil {
			log.Printf("Context builder: people lookup partial failure: %v", err)
		}
		if len(matches) > 0 {
			var peopleBuf strings.Builder
			peopleBuf.WriteString("<people_context>\n")
			for _, p := range matches {
				name := format.XMLEscapeAttr(p.FullName)
				role := format.XMLEscapeAttr(p.Role)
				company := format.XMLEscapeAttr(p.Company)
				fmt.Fprintf(&peopleBuf, "<person name=\"%s\" role=\"%s\" company=\"%s\">\n", name, role, company)
				for _, c := range p.Contacts {
					fmt.Fprintf(&peopleBuf, "Contact: %s: %s", c.ContactType, c.Value)
					if c.Label != "" {
						fmt.Fprintf(&peopleBuf, " (%s)", c.Label)
					}
					peopleBuf.WriteByte('\n')
				}
				if p.Notes != "" {
					fmt.Fprintf(&peopleBuf, "Notes: %s\n", p.Notes)
				}
				peopleBuf.WriteString("</person>\n")
			}
			peopleBuf.WriteString("</people_context>")
			addSection(peopleBuf.String())
		}
	}

	// 4. Relevant facts (keyword match against user message)
	// The db is guaranteed non-nil by the constructor, so this section
	// always runs when v2 is active — no embedder dependency needed.
	if totalLen < budget && userMessage != "" {
		factEntries, err := b.db.GetMemoryByCategory(ctx, "fact")
		if err == nil && len(factEntries) > 0 {
			// Simple keyword matching for relevant facts (cheaper than embedding each)
			var relevant []pgdb.MemoryEntry
			msgLower := strings.ToLower(userMessage)
			for _, f := range factEntries {
				contentLower := strings.ToLower(f.Content)
				keyLower := strings.ToLower(f.Key)
				if strings.Contains(msgLower, keyLower) || containsAnyWord(msgLower, contentLower) {
					relevant = append(relevant, f)
				}
			}
			// Also include high-importance facts regardless
			for _, f := range factEntries {
				if f.Importance >= 8 {
					found := false
					for _, r := range relevant {
						if r.ID == f.ID {
							found = true
							break
						}
					}
					if !found {
						relevant = append(relevant, f)
					}
				}
			}
			if len(relevant) > 10 {
				relevant = relevant[:10]
			}
			if len(relevant) > 0 {
				var factBuf strings.Builder
				factBuf.WriteString("<remembered_facts>\n")
				for _, f := range relevant {
					fmt.Fprintf(&factBuf, "- %s (%s, importance:%d)\n",
						format.CDATAEscape(f.Content), f.Category, f.Importance)
				}
				factBuf.WriteString("</remembered_facts>")
				addSection(factBuf.String())
			}
		}
	}

	// 5. Profile documents (full reference docs, injected when topically relevant)
	if totalLen < budget && userMessage != "" {
		profileDocs, err := b.db.GetMemoryByCategory(ctx, "profile_doc")
		if err == nil && len(profileDocs) > 0 {
			msgLower := strings.ToLower(userMessage)

			// Trigger keywords that indicate a profile doc should be injected
			emailTriggers := []string{"email", "draft", "compose", "write to", "reply to", "respond to", "send a message", "writing style"}
			triggered := false
			for _, trigger := range emailTriggers {
				if strings.Contains(msgLower, trigger) {
					triggered = true
					break
				}
			}

			var relevant []pgdb.MemoryEntry
			for _, doc := range profileDocs {
				keyLower := strings.ToLower(doc.Key)
				// Match if triggered and doc key contains related terms
				if triggered && (strings.Contains(keyLower, "email") || strings.Contains(keyLower, "writing")) {
					relevant = append(relevant, doc)
					continue
				}
				// Also match via keyword overlap between message and doc key
				if containsAnyWord(msgLower, keyLower) {
					relevant = append(relevant, doc)
				}
			}

			if len(relevant) > 2 {
				relevant = relevant[:2]
			}
			if len(relevant) > 0 {
				var docBuf strings.Builder
				docBuf.WriteString("<profile_documents>\n")
				docBuf.WriteString("<!-- Reference documents about the user. Use these to match their style. -->\n")
				for _, doc := range relevant {
					content := doc.Content
					if len(content) > 16000 {
						content = content[:16000] + "\n\n[Truncated — full document is " + fmt.Sprintf("%d", len(doc.Content)) + " chars]"
					}
					fmt.Fprintf(&docBuf, "<doc key=%q>\n<![CDATA[%s]]>\n</doc>\n",
						doc.Key, format.CDATAEscape(content))
				}
				docBuf.WriteString("</profile_documents>")
				addSection(docBuf.String())
			}
		}
	}

	// 6. Recent journal entries (last 3 days)
	if totalLen < budget && b.journalMgr != nil {
		entries, err := b.journalMgr.GetRecentEntries(ctx, 3)
		if err == nil && len(entries) > 0 {
			if section := journal.FormatJournalForContext(entries); section != "" {
				addSection(section)
			}
		}
	}

	if len(parts) == 0 {
		return "", nil
	}

	return strings.Join(parts, "\n\n"), nil
}

// RecordExchange persists a user/assistant exchange with embeddings.
// Called from a background goroutine — errors are returned so the caller can log them.
//
// Deprecated: Use RecordExchangeSync + EnrichWithEmbeddings for the
// "save now, embed later" pattern that avoids the race condition where
// a quick follow-up message arrives before the DB write completes.
func (b *Builder) RecordExchange(ctx context.Context, sessionID, userMessage, assistantResponse string) error {
	// Embed and save user message
	var userEmb []float32
	if b.embedder != nil {
		if emb, err := b.embedder.Embed(ctx, userMessage); err == nil {
			userEmb = emb
		}
	}
	userTokens := len(userMessage) / 4 // rough estimate
	if err := b.db.SaveConversation(ctx, sessionID, "user", userMessage, userEmb, userTokens); err != nil {
		log.Printf("Context builder: failed to save user message: %v", err)
		return err
	}

	// Embed and save assistant response
	var assistEmb []float32
	if b.embedder != nil {
		if emb, err := b.embedder.Embed(ctx, assistantResponse); err == nil {
			assistEmb = emb
		}
	}
	assistTokens := len(assistantResponse) / 4
	if err := b.db.SaveConversation(ctx, sessionID, "assistant", assistantResponse, assistEmb, assistTokens); err != nil {
		log.Printf("Context builder: failed to save assistant message: %v", err)
		return err
	}

	log.Printf("Context builder: recorded exchange for session %s (user=%d chars, assistant=%d chars, embeddings=%v/%v)",
		sessionID, len(userMessage), len(assistantResponse), userEmb != nil, assistEmb != nil)
	return nil
}

// ExchangeIDs holds the database row IDs from RecordExchangeSync so the
// caller can pass them to EnrichWithEmbeddings.
type ExchangeIDs struct {
	UserID      int64
	AssistantID int64
}

// RecordExchangeSync saves user and assistant messages to PostgreSQL
// WITHOUT generating embeddings. This is fast (just two DB inserts) and
// ensures the conversation turns are visible to BuildEnrichedPrompt
// immediately, even if the user sends a follow-up within seconds.
//
// Call EnrichWithEmbeddings in a background goroutine afterward to
// generate and attach embeddings for semantic search.
func (b *Builder) RecordExchangeSync(ctx context.Context, sessionID, userMessage, assistantResponse string) (ExchangeIDs, error) {
	userTokens := len(userMessage) / 4
	userID, err := b.db.SaveConversationReturningID(ctx, sessionID, "user", userMessage, userTokens)
	if err != nil {
		return ExchangeIDs{}, fmt.Errorf("save user message: %w", err)
	}

	assistTokens := len(assistantResponse) / 4
	assistID, err := b.db.SaveConversationReturningID(ctx, sessionID, "assistant", assistantResponse, assistTokens)
	if err != nil {
		return ExchangeIDs{}, fmt.Errorf("save assistant message: %w", err)
	}

	log.Printf("Context builder: saved exchange for session %s (user=%d chars id=%d, assistant=%d chars id=%d)",
		sessionID, len(userMessage), userID, len(assistantResponse), assistID)
	return ExchangeIDs{UserID: userID, AssistantID: assistID}, nil
}

// EnrichWithEmbeddings generates embeddings via Ollama and updates the
// conversation rows saved by RecordExchangeSync. Safe to call from a
// background goroutine — failures are logged but non-fatal (the
// conversation text is already persisted).
func (b *Builder) EnrichWithEmbeddings(ctx context.Context, ids ExchangeIDs, userMessage, assistantResponse string) {
	if b.embedder == nil {
		return
	}

	if emb, err := b.embedder.Embed(ctx, userMessage); err != nil {
		log.Printf("Context builder: embed user message (id=%d) failed: %v", ids.UserID, err)
	} else if err := b.db.UpdateConversationEmbedding(ctx, ids.UserID, emb); err != nil {
		log.Printf("Context builder: update user embedding (id=%d) failed: %v", ids.UserID, err)
	}

	if emb, err := b.embedder.Embed(ctx, assistantResponse); err != nil {
		log.Printf("Context builder: embed assistant message (id=%d) failed: %v", ids.AssistantID, err)
	} else if err := b.db.UpdateConversationEmbedding(ctx, ids.AssistantID, emb); err != nil {
		log.Printf("Context builder: update assistant embedding (id=%d) failed: %v", ids.AssistantID, err)
	}

	log.Printf("Context builder: enriched embeddings for ids %d, %d", ids.UserID, ids.AssistantID)
}

// ================================================================
// Helpers
// ================================================================

func categorizeEntries(entries []pgdb.MemoryEntry) (self, bootstrap, pref []pgdb.MemoryEntry) {
	for _, e := range entries {
		switch e.Category {
		case "self":
			self = append(self, e)
		case "bootstrap":
			bootstrap = append(bootstrap, e)
		case "preference":
			pref = append(pref, e)
		}
	}
	return
}

func formatMemorySection(tag string, entries []pgdb.MemoryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>\n", tag)
	for _, e := range entries {
		content := format.CDATAEscape(e.Content)
		if e.Key != "" {
			fmt.Fprintf(&b, "- [%s] <![CDATA[%s]]>\n", e.Key, content)
		} else {
			fmt.Fprintf(&b, "- <![CDATA[%s]]>\n", content)
		}
	}
	fmt.Fprintf(&b, "</%s>", tag)
	return b.String()
}

// containsAnyWord returns true if any word from content appears in message.
// Both message and content are expected to be already lowercased by the caller.
// Uses a simple 4+ char word check, requiring 2 word matches.
func containsAnyWord(message, content string) bool {
	words := strings.Fields(content)
	matches := 0
	for _, w := range words {
		if len(w) >= 4 && strings.Contains(message, w) {
			matches++
			if matches >= 2 {
				return true
			}
		}
	}
	return false
}
