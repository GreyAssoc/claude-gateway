// Package journal provides AI-generated daily summaries of conversations,
// decisions, and actions. Uses the local 7B model for summarization.
package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/you/claude-gateway/internal/ollama"
	"github.com/you/claude-gateway/internal/pgdb"
)

// Manager manages daily journal entries.
type Manager struct {
	db       *pgdb.DB
	llm      *ollama.Client
	embedder *ollama.Client
}

// DayEntry represents a daily journal entry with parsed fields.
type DayEntry struct {
	Date      string
	Summary   string
	Decisions []Decision
	Actions   []Action
	Topics    []string
	Projects  []string
	Stats     DayStats
}

// Decision represents a decision made during the day.
type Decision struct {
	Decision  string `json:"decision"`
	Context   string `json:"context"`
	Timestamp string `json:"timestamp"`
}

// Action represents an action taken or pending.
type Action struct {
	Action    string `json:"action"`
	Status    string `json:"status"`
	Project   string `json:"project"`
	Timestamp string `json:"timestamp"`
}

// DayStats holds message and token counts for the day.
type DayStats struct {
	Messages     int `json:"messages"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	LocalHandled int `json:"local_handled"`
}

// NewManager creates a new journal manager. Panics if db is nil because
// every Manager method depends on a valid database connection.
func NewManager(db *pgdb.DB, llm, embedder *ollama.Client) *Manager {
	if db == nil {
		panic("journal: nil db")
	}
	return &Manager{db: db, llm: llm, embedder: embedder}
}

const journalSystemPrompt = `Summarize this day's conversations. Return JSON:
{
  "summary": "narrative paragraph of what was discussed and accomplished",
  "decisions": [{"decision": "...", "context": "why", "timestamp": "HH:MM"}],
  "actions": [{"action": "...", "status": "done|pending|blocked", "project": "...", "timestamp": "HH:MM"}],
  "topics": ["topic1", "topic2"],
  "projects": ["project1"]
}

Focus on: what was decided, what was built/changed, what's pending.
Be specific — include file names, feature names, technical details.
Keep the summary concise (2-4 sentences).
Return valid JSON only, no markdown fences.`

// journalResponse matches the JSON output from the 7B model.
type journalResponse struct {
	Summary   string     `json:"summary"`
	Decisions []Decision `json:"decisions"`
	Actions   []Action   `json:"actions"`
	Topics    []string   `json:"topics"`
	Projects  []string   `json:"projects"`
}

// GenerateDailySummary creates a journal entry for the given date by summarizing
// the day's conversations using the 7B model. Falls back to simple stats on error.
func (j *Manager) GenerateDailySummary(ctx context.Context, date string) (*DayEntry, error) {
	// Validate date format before passing to PostgreSQL
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, fmt.Errorf("journal: invalid date format %q (expected YYYY-MM-DD): %w", date, err)
	}

	// Get the day's conversations
	convs, err := j.db.ConversationsByDate(ctx, date)
	if err != nil {
		return nil, fmt.Errorf("journal: loading conversations for %s: %w", date, err)
	}
	if len(convs) == 0 {
		return nil, nil // nothing to summarize
	}

	// Build conversation text for the LLM
	var convoText strings.Builder
	totalTokens := 0
	for _, c := range convs {
		timestamp := c.CreatedAt.Format("15:04")
		line := fmt.Sprintf("[%s] %s: %s\n\n", timestamp, strings.ToUpper(c.Role), c.Content)
		// Check BEFORE writing to avoid overshooting the limit
		if convoText.Len()+len(line) > 8000 {
			convoText.WriteString("[... remaining messages omitted ...]\n")
			break
		}
		convoText.WriteString(line)
		totalTokens += c.TokenEstimate
	}

	stats := DayStats{
		Messages: len(convs),
	}

	entry := &DayEntry{
		Date:  date,
		Stats: stats,
	}

	// Try 7B model summarization
	if j.llm != nil {
		var resp journalResponse
		err := j.llm.GenerateJSON(ctx, convoText.String(), journalSystemPrompt, &resp)
		if err == nil && resp.Summary != "" {
			entry.Summary = resp.Summary
			entry.Decisions = resp.Decisions
			entry.Actions = resp.Actions
			entry.Topics = resp.Topics
			entry.Projects = resp.Projects
		} else {
			if err != nil {
				log.Printf("Journal 7B summarization failed, using simple stats: %v", err)
			}
			entry.Summary = j.buildSimpleSummary(convs, date)
		}
	} else {
		entry.Summary = j.buildSimpleSummary(convs, date)
	}

	// Serialize for DB storage. Untyped nil produces "[]"; typed nil slices
	// (e.g., []Decision(nil)) passed as any produce "null" via json.Marshal,
	// but dbEntryToDayEntry handles both gracefully.
	marshalOrEmpty := func(v any) json.RawMessage {
		if v == nil {
			return []byte("[]")
		}
		b, err := json.Marshal(v)
		if err != nil {
			log.Printf("Journal marshal error: %v", err)
			return []byte("[]")
		}
		return b
	}
	decisionsJSON := marshalOrEmpty(entry.Decisions)
	actionsJSON := marshalOrEmpty(entry.Actions)
	topicsJSON := marshalOrEmpty(entry.Topics)
	projectsJSON := marshalOrEmpty(entry.Projects)
	statsJSON, err := json.Marshal(entry.Stats)
	if err != nil {
		log.Printf("Journal stats marshal error: %v", err)
		statsJSON = []byte("{}")
	}

	dbEntry := &pgdb.JournalEntry{
		Date:      date,
		Summary:   entry.Summary,
		Decisions: decisionsJSON,
		Actions:   actionsJSON,
		Topics:    topicsJSON,
		Projects:  projectsJSON,
		Stats:     statsJSON,
	}

	// Generate embedding for semantic search
	var embedding []float32
	if j.embedder != nil {
		if emb, err := j.embedder.Embed(ctx, entry.Summary); err == nil {
			embedding = emb
		}
	}

	if err := j.db.UpsertJournalEntry(ctx, date, dbEntry, embedding); err != nil {
		return nil, fmt.Errorf("journal: saving entry for %s: %w", date, err)
	}

	return entry, nil
}

// GetJournalEntry returns the journal entry for a date.
func (j *Manager) GetJournalEntry(ctx context.Context, date string) (*DayEntry, error) {
	dbEntry, err := j.db.GetJournalEntry(ctx, date)
	if err != nil {
		return nil, err
	}
	if dbEntry == nil {
		return nil, nil
	}
	return dbEntryToDayEntry(dbEntry), nil
}

// GetRecentEntries returns the last N journal entries.
func (j *Manager) GetRecentEntries(ctx context.Context, days int) ([]DayEntry, error) {
	dbEntries, err := j.db.GetRecentJournalEntries(ctx, days)
	if err != nil {
		return nil, err
	}
	entries := make([]DayEntry, 0, len(dbEntries))
	for i := range dbEntries {
		entries = append(entries, *dbEntryToDayEntry(&dbEntries[i]))
	}
	return entries, nil
}

// SearchJournal finds journal entries semantically similar to the query.
func (j *Manager) SearchJournal(ctx context.Context, query string, limit int) ([]DayEntry, error) {
	if j.embedder == nil {
		return nil, fmt.Errorf("journal search unavailable: embedder not configured")
	}
	emb, err := j.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("journal search embed: %w", err)
	}
	dbEntries, err := j.db.SearchJournal(ctx, emb, limit, 0.5)
	if err != nil {
		return nil, err
	}
	entries := make([]DayEntry, 0, len(dbEntries))
	for i := range dbEntries {
		entries = append(entries, *dbEntryToDayEntry(&dbEntries[i]))
	}
	return entries, nil
}

// FormatJournalForContext formats recent journal entries for inclusion in enriched prompt.
func FormatJournalForContext(entries []DayEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<recent_journal>\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "[%s] %s\n", e.Date, e.Summary)
		for _, a := range e.Actions {
			fmt.Fprintf(&b, "  Action: %s (%s)", a.Action, a.Status)
			if a.Project != "" {
				fmt.Fprintf(&b, " [%s]", a.Project)
			}
			b.WriteByte('\n')
		}
		for _, d := range e.Decisions {
			fmt.Fprintf(&b, "  Decision: %s\n", d.Decision)
		}
	}
	b.WriteString("</recent_journal>")
	return b.String()
}

func (j *Manager) buildSimpleSummary(convs []pgdb.Conversation, date string) string {
	return fmt.Sprintf("Exchanged %d messages on %s.", len(convs), date)
}

func dbEntryToDayEntry(e *pgdb.JournalEntry) *DayEntry {
	if e == nil {
		return &DayEntry{}
	}
	entry := &DayEntry{
		Date:    e.Date,
		Summary: e.Summary,
	}
	if err := json.Unmarshal(e.Decisions, &entry.Decisions); err != nil && len(e.Decisions) > 0 {
		log.Printf("Journal unmarshal decisions for %s: %v", e.Date, err)
	}
	if err := json.Unmarshal(e.Actions, &entry.Actions); err != nil && len(e.Actions) > 0 {
		log.Printf("Journal unmarshal actions for %s: %v", e.Date, err)
	}
	if err := json.Unmarshal(e.Topics, &entry.Topics); err != nil && len(e.Topics) > 0 {
		log.Printf("Journal unmarshal topics for %s: %v", e.Date, err)
	}
	if err := json.Unmarshal(e.Projects, &entry.Projects); err != nil && len(e.Projects) > 0 {
		log.Printf("Journal unmarshal projects for %s: %v", e.Date, err)
	}
	if err := json.Unmarshal(e.Stats, &entry.Stats); err != nil && len(e.Stats) > 0 {
		log.Printf("Journal unmarshal stats for %s: %v", e.Date, err)
	}
	return entry
}
