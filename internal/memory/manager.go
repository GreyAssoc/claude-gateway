package memory

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/you/claude-gateway/internal/db"
)

// BootstrapFile represents a loaded bootstrap file (SOUL.md, IDENTITY.md, etc).
type BootstrapFile struct {
	Name        string
	Content     string
	ContentHash string
}

// CuratedMemory represents a user-created or auto-extracted memory.
type CuratedMemory struct {
	ID         int
	Category   string
	Content    string
	Source     string
	Importance int
	CreatedAt  string
}

// DailyMemory represents a day's summarized interactions.
type DailyMemory struct {
	Date      string
	Summary   string
	KeyEvents string
}

// Manager handles persistent memory: bootstrap files, daily logs, curated entries.
type Manager struct {
	db            *db.DB
	bootstrapDir  string
	dailyDir      string
	maxCurated    int
	maxDailyDays  int

	mu         sync.RWMutex
	bootstraps []*BootstrapFile
}

// NewManager creates a memory manager.
func NewManager(database *db.DB, bootstrapDir, dailyDir string, maxCurated, maxDailyDays int) *Manager {
	return &Manager{
		db:           database,
		bootstrapDir: bootstrapDir,
		dailyDir:     dailyDir,
		maxCurated:   maxCurated,
		maxDailyDays: maxDailyDays,
	}
}

// Start loads bootstrap files and verifies hashes.
func (m *Manager) Start() error {
	if err := m.loadBootstrapFiles(); err != nil {
		log.Printf("Memory: warning loading bootstrap: %v", err)
	}

	log.Printf("Memory: %d bootstrap files loaded, max %d curated, %d daily days",
		len(m.bootstraps), m.maxCurated, m.maxDailyDays)
	return nil
}

// AddCuratedMemory saves a user memory with a category and importance.
func (m *Manager) AddCuratedMemory(category, content, source string, importance int) (int64, error) {
	if importance < 1 {
		importance = 1
	}
	if importance > 10 {
		importance = 10
	}

	result, err := m.db.Exec(
		`INSERT INTO memory_curated (category, content, source, importance, created_at, updated_at)
		 VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))`,
		category, content, source, importance,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// RemoveCuratedMemory deletes a curated memory by ID.
func (m *Manager) RemoveCuratedMemory(id int) error {
	result, err := m.db.Exec("DELETE FROM memory_curated WHERE id = ?", id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("memory #%d not found", id)
	}
	return nil
}

// GetCuratedMemories returns top memories by importance.
func (m *Manager) GetCuratedMemories(limit int) ([]CuratedMemory, error) {
	rows, err := m.db.Query(
		"SELECT id, category, content, source, importance, created_at FROM memory_curated ORDER BY importance DESC, created_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []CuratedMemory
	for rows.Next() {
		var mem CuratedMemory
		if err := rows.Scan(&mem.ID, &mem.Category, &mem.Content, &mem.Source, &mem.Importance, &mem.CreatedAt); err != nil {
			return nil, err
		}
		memories = append(memories, mem)
	}
	return memories, rows.Err()
}

// SaveDailySummary stores today's memory summary.
func (m *Manager) SaveDailySummary(date, summary, keyEventsJSON string) error {
	_, err := m.db.Exec(
		`INSERT INTO memory_daily (date, summary, key_events, created_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(date) DO UPDATE SET summary = excluded.summary, key_events = excluded.key_events`,
		date, summary, keyEventsJSON,
	)
	return err
}

// GetRecentDailySummaries returns the last N days of summaries.
func (m *Manager) GetRecentDailySummaries(days int) ([]DailyMemory, error) {
	rows, err := m.db.Query(
		"SELECT date, summary, key_events FROM memory_daily ORDER BY date DESC LIMIT ?",
		days,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []DailyMemory
	for rows.Next() {
		var dm DailyMemory
		if err := rows.Scan(&dm.Date, &dm.Summary, &dm.KeyEvents); err != nil {
			return nil, err
		}
		summaries = append(summaries, dm)
	}
	return summaries, rows.Err()
}

// GetTodaySummary returns today's summary if it exists.
func (m *Manager) GetTodaySummary() (string, error) {
	today := time.Now().Format("2006-01-02")
	var summary string
	err := m.db.QueryRow("SELECT summary FROM memory_daily WHERE date = ?", today).Scan(&summary)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return summary, err
}

// BuildSystemPromptSection builds the memory section for the system prompt.
// Layers: bootstrap (cached) → curated (partially cached) → daily context (not cached)
func (m *Manager) BuildSystemPromptSection() string {
	var parts []string

	// Layer 1: Bootstrap files
	m.mu.RLock()
	for _, bf := range m.bootstraps {
		parts = append(parts, fmt.Sprintf("<bootstrap name=\"%s\">\n%s\n</bootstrap>", bf.Name, bf.Content))
	}
	m.mu.RUnlock()

	// Layer 3: Curated memories
	curated, err := m.GetCuratedMemories(m.maxCurated)
	if err == nil && len(curated) > 0 {
		var memLines []string
		for _, mem := range curated {
			memLines = append(memLines, fmt.Sprintf("- [%s] (importance:%d) %s", mem.Category, mem.Importance, mem.Content))
		}
		parts = append(parts, "<memories>\n"+strings.Join(memLines, "\n")+"\n</memories>")
	}

	// Layer 4: Recent daily summaries
	dailies, err := m.GetRecentDailySummaries(m.maxDailyDays)
	if err == nil && len(dailies) > 0 {
		var dayLines []string
		for _, d := range dailies {
			dayLines = append(dayLines, fmt.Sprintf("[%s] %s", d.Date, d.Summary))
		}
		parts = append(parts, "<recent_context>\n"+strings.Join(dayLines, "\n")+"\n</recent_context>")
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "\n\n")
}

// StatusText returns a formatted status for Telegram.
func (m *Manager) StatusText() string {
	var lines []string

	m.mu.RLock()
	lines = append(lines, fmt.Sprintf("*Memory Status*\n\nBootstrap files: %d", len(m.bootstraps)))
	for _, bf := range m.bootstraps {
		lines = append(lines, fmt.Sprintf("  - %s (%d chars)", bf.Name, len(bf.Content)))
	}
	m.mu.RUnlock()

	curated, err := m.GetCuratedMemories(100)
	if err == nil {
		lines = append(lines, fmt.Sprintf("\nCurated memories: %d", len(curated)))
	}

	dailies, err := m.GetRecentDailySummaries(m.maxDailyDays)
	if err == nil {
		lines = append(lines, fmt.Sprintf("Daily summaries: %d", len(dailies)))
	}

	return strings.Join(lines, "\n")
}

// CuratedMemoriesText returns formatted curated memories for display.
func (m *Manager) CuratedMemoriesText() string {
	curated, err := m.GetCuratedMemories(50)
	if err != nil {
		return fmt.Sprintf("Error loading memories: %v", err)
	}
	if len(curated) == 0 {
		return "No curated memories yet. Use /remember <text> to add one."
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("*Curated Memories* (%d)\n", len(curated)))
	for _, mem := range curated {
		lines = append(lines, fmt.Sprintf("#%d [%s] (P%d) %s", mem.ID, mem.Category, mem.Importance, mem.Content))
	}
	return strings.Join(lines, "\n")
}

func (m *Manager) loadBootstrapFiles() error {
	absDir, err := filepath.Abs(m.bootstrapDir)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var bootstraps []*BootstrapFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}

		path := filepath.Join(absDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("Memory: warning reading %s: %v", path, err)
			continue
		}

		content := string(data)
		hash := fmt.Sprintf("%x", sha256.Sum256(data))

		bf := &BootstrapFile{
			Name:        strings.TrimSuffix(name, filepath.Ext(name)),
			Content:     content,
			ContentHash: hash,
		}
		bootstraps = append(bootstraps, bf)

		// Upsert to DB
		m.upsertBootstrapToDB(bf)
	}

	// Sort by name for consistent ordering
	sort.Slice(bootstraps, func(i, j int) bool {
		return bootstraps[i].Name < bootstraps[j].Name
	})

	m.mu.Lock()
	m.bootstraps = bootstraps
	m.mu.Unlock()

	return nil
}

func (m *Manager) upsertBootstrapToDB(bf *BootstrapFile) {
	_, err := m.db.Exec(
		`INSERT INTO bootstrap_files (name, content, content_hash, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(name) DO UPDATE SET
		   content = excluded.content,
		   content_hash = excluded.content_hash,
		   updated_at = excluded.updated_at`,
		bf.Name, bf.Content, bf.ContentHash,
	)
	if err != nil {
		log.Printf("Memory: error upserting bootstrap %s: %v", bf.Name, err)
	}
}
