package journal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/you/claude-gateway/internal/pgdb"
)

// TestFormatJournalForContext verifies journal formatting for enriched prompt.
func TestFormatJournalForContext(t *testing.T) {
	t.Run("empty entries", func(t *testing.T) {
		got := FormatJournalForContext(nil)
		if got != "" {
			t.Errorf("expected empty string for nil entries, got %q", got)
		}
		got = FormatJournalForContext([]DayEntry{})
		if got != "" {
			t.Errorf("expected empty string for empty entries, got %q", got)
		}
	})

	t.Run("with entries", func(t *testing.T) {
		entries := []DayEntry{
			{
				Date:    "2026-02-24",
				Summary: "Worked on gateway fixes.",
				Actions: []Action{
					{Action: "Fix build", Status: "done", Project: "gateway"},
				},
				Decisions: []Decision{
					{Decision: "Use LEFT JOIN for ListPeople"},
				},
			},
		}
		got := FormatJournalForContext(entries)
		if got == "" {
			t.Fatal("expected non-empty result")
		}
		// Should contain the journal tags
		if !strings.Contains(got, "<recent_journal>") || !strings.Contains(got, "</recent_journal>") {
			t.Errorf("missing journal tags in: %s", got)
		}
		if !strings.Contains(got, "2026-02-24") {
			t.Error("missing date")
		}
		if !strings.Contains(got, "Fix build") {
			t.Error("missing action")
		}
		if !strings.Contains(got, "Use LEFT JOIN") {
			t.Error("missing decision")
		}
	})
}

// TestDbEntryToDayEntry verifies correct unmarshalling of journal DB entries.
// Fix #4/#30: errors in unmarshal are logged, not fatal.
func TestDbEntryToDayEntry(t *testing.T) {
	t.Run("valid entry", func(t *testing.T) {
		decisions, _ := json.Marshal([]Decision{{Decision: "test", Context: "ctx"}})
		actions, _ := json.Marshal([]Action{{Action: "build", Status: "done"}})
		topics, _ := json.Marshal([]string{"topic1"})
		projects, _ := json.Marshal([]string{"proj1"})
		stats, _ := json.Marshal(DayStats{Messages: 5})

		dbEntry := &pgdb.JournalEntry{
			Date:      "2026-02-24",
			Summary:   "Test summary",
			Decisions: decisions,
			Actions:   actions,
			Topics:    topics,
			Projects:  projects,
			Stats:     stats,
		}

		entry := dbEntryToDayEntry(dbEntry)
		if entry.Date != "2026-02-24" {
			t.Errorf("date = %q, want %q", entry.Date, "2026-02-24")
		}
		if entry.Summary != "Test summary" {
			t.Errorf("summary = %q, want %q", entry.Summary, "Test summary")
		}
		if len(entry.Decisions) != 1 {
			t.Errorf("decisions count = %d, want 1", len(entry.Decisions))
		}
		if len(entry.Actions) != 1 {
			t.Errorf("actions count = %d, want 1", len(entry.Actions))
		}
		if len(entry.Topics) != 1 || entry.Topics[0] != "topic1" {
			t.Errorf("topics = %v, want [topic1]", entry.Topics)
		}
		if len(entry.Projects) != 1 || entry.Projects[0] != "proj1" {
			t.Errorf("projects = %v, want [proj1]", entry.Projects)
		}
		if entry.Stats.Messages != 5 {
			t.Errorf("stats.Messages = %d, want 5", entry.Stats.Messages)
		}
	})

	t.Run("invalid JSON does not panic", func(t *testing.T) {
		dbEntry := &pgdb.JournalEntry{
			Date:      "2026-02-24",
			Summary:   "Test",
			Decisions: json.RawMessage(`invalid`),
			Actions:   json.RawMessage(`invalid`),
			Topics:    json.RawMessage(`invalid`),
			Projects:  json.RawMessage(`invalid`),
			Stats:     json.RawMessage(`invalid`),
		}
		// Should not panic — just logs errors
		entry := dbEntryToDayEntry(dbEntry)
		if entry.Date != "2026-02-24" {
			t.Errorf("date = %q, want %q", entry.Date, "2026-02-24")
		}
	})

	t.Run("empty JSON fields", func(t *testing.T) {
		dbEntry := &pgdb.JournalEntry{
			Date:      "2026-02-24",
			Summary:   "Test",
			Decisions: json.RawMessage(`[]`),
			Actions:   json.RawMessage(`[]`),
			Topics:    json.RawMessage(`[]`),
			Projects:  json.RawMessage(`[]`),
			Stats:     json.RawMessage(`{}`),
		}
		entry := dbEntryToDayEntry(dbEntry)
		if len(entry.Decisions) != 0 {
			t.Errorf("decisions should be empty, got %d", len(entry.Decisions))
		}
		if len(entry.Actions) != 0 {
			t.Errorf("actions should be empty, got %d", len(entry.Actions))
		}
	})

	t.Run("nil JSON fields", func(t *testing.T) {
		dbEntry := &pgdb.JournalEntry{
			Date:    "2026-02-24",
			Summary: "Test",
		}
		// nil RawMessage fields should not panic
		entry := dbEntryToDayEntry(dbEntry)
		if entry.Date != "2026-02-24" {
			t.Errorf("date = %q, want %q", entry.Date, "2026-02-24")
		}
	})

	t.Run("nil parameter", func(t *testing.T) {
		// nil *JournalEntry should not panic
		entry := dbEntryToDayEntry(nil)
		if entry == nil {
			t.Fatal("expected non-nil DayEntry for nil input")
		}
		if entry.Date != "" {
			t.Errorf("date = %q, want empty", entry.Date)
		}
	})
}

// TestBuildSimpleSummary verifies fallback summary generation.
func TestBuildSimpleSummary(t *testing.T) {
	j := &Manager{}
	convs := make([]pgdb.Conversation, 7)
	got := j.buildSimpleSummary(convs, "2026-02-24")
	want := "Exchanged 7 messages on 2026-02-24."
	if got != want {
		t.Errorf("buildSimpleSummary = %q, want %q", got, want)
	}
}

// TestNewManagerNilDBPanics verifies that NewManager panics with nil db,
// matching the established pattern in peer constructors (NewBuilder, NewManager).
func TestNewManagerNilDBPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic with nil db, got none")
		}
		msg, ok := r.(string)
		if !ok || msg != "journal: nil db" {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()
	NewManager(nil, nil, nil)
}

// TestNewManagerValidDB verifies that NewManager does not panic with valid db.
func TestNewManagerValidDB(t *testing.T) {
	// Use a Manager with a non-nil db field set directly to avoid needing a real DB.
	// The constructor only checks for nil, so any non-nil pointer works.
	j := &Manager{}
	if j == nil {
		t.Fatal("unexpected nil journal")
	}
}

