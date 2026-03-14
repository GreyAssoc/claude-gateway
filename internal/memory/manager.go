// Package memory manages persistent memory layers: bootstrap files, curated entries, and daily summaries.
package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/you/claude-gateway/internal/db"
	"github.com/you/claude-gateway/internal/format"
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
	claudeMDPaths []string

	mu         sync.RWMutex
	bootstraps []*BootstrapFile
}

// NewManager creates a memory manager.
func NewManager(database *db.DB, bootstrapDir, dailyDir string, maxCurated, maxDailyDays int, claudeMDPaths ...string) *Manager {
	return &Manager{
		db:            database,
		bootstrapDir:  bootstrapDir,
		dailyDir:      dailyDir,
		maxCurated:    maxCurated,
		maxDailyDays:  maxDailyDays,
		claudeMDPaths: claudeMDPaths,
	}
}

// Start loads bootstrap files and verifies hashes.
func (m *Manager) Start(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := m.loadBootstrapFiles(ctx)

	log.Printf("Memory: %d bootstrap files loaded, max %d curated, %d daily days",
		len(m.bootstraps), m.maxCurated, m.maxDailyDays)

	if err != nil {
		return fmt.Errorf("loading bootstrap files: %w", err)
	}
	return nil
}

// AddCuratedMemory saves a user memory with a category and importance.
func (m *Manager) AddCuratedMemory(ctx context.Context, category, content, source string, importance int) (int64, error) {
	if importance < 1 {
		importance = 1
	}
	if importance > 10 {
		importance = 10
	}

	result, err := m.db.ExecContext(ctx,
		`INSERT INTO memory_curated (category, content, source, importance, created_at, updated_at)
		 VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))`,
		category, content, source, importance,
	)
	if err != nil {
		return 0, fmt.Errorf("adding curated memory: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("getting curated memory ID: %w", err)
	}
	return id, nil
}

// RemoveCuratedMemory deletes a curated memory by ID.
func (m *Manager) RemoveCuratedMemory(ctx context.Context, id int) error {
	result, err := m.db.ExecContext(ctx, "DELETE FROM memory_curated WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting curated memory %d: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("memory #%d not found", id)
	}
	return nil
}

// GetCuratedMemories returns top memories by importance.
func (m *Manager) GetCuratedMemories(ctx context.Context, limit int) ([]CuratedMemory, error) {
	rows, err := m.db.QueryContext(ctx,
		"SELECT id, category, content, source, importance, created_at FROM memory_curated ORDER BY importance DESC, created_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying curated memories: %w", err)
	}
	defer rows.Close()

	var memories []CuratedMemory
	for rows.Next() {
		var mem CuratedMemory
		if err := rows.Scan(&mem.ID, &mem.Category, &mem.Content, &mem.Source, &mem.Importance, &mem.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning curated memory: %w", err)
		}
		memories = append(memories, mem)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating curated memories: %w", err)
	}
	return memories, nil
}

// SaveDailySummary stores today's memory summary.
func (m *Manager) SaveDailySummary(ctx context.Context, date, summary, keyEventsJSON string) error {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO memory_daily (date, summary, key_events, created_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(date) DO UPDATE SET summary = excluded.summary, key_events = excluded.key_events`,
		date, summary, keyEventsJSON,
	)
	if err != nil {
		return fmt.Errorf("saving daily summary for %s: %w", date, err)
	}
	return nil
}

// GetRecentDailySummaries returns the last N days of summaries.
func (m *Manager) GetRecentDailySummaries(ctx context.Context, days int) ([]DailyMemory, error) {
	rows, err := m.db.QueryContext(ctx,
		"SELECT date, summary, key_events FROM memory_daily ORDER BY date DESC LIMIT ?",
		days,
	)
	if err != nil {
		return nil, fmt.Errorf("querying daily summaries: %w", err)
	}
	defer rows.Close()

	var summaries []DailyMemory
	for rows.Next() {
		var dm DailyMemory
		if err := rows.Scan(&dm.Date, &dm.Summary, &dm.KeyEvents); err != nil {
			return nil, fmt.Errorf("scanning daily summary: %w", err)
		}
		summaries = append(summaries, dm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating daily summaries: %w", err)
	}
	return summaries, nil
}

// GetTodaySummary returns today's summary if it exists.
func (m *Manager) GetTodaySummary(ctx context.Context) (string, error) {
	today := time.Now().Format("2006-01-02")
	var summary string
	err := m.db.QueryRowContext(ctx, "SELECT summary FROM memory_daily WHERE date = ?", today).Scan(&summary)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting today summary: %w", err)
	}
	return summary, nil
}

// BuildSystemPromptSection builds the memory section for the system prompt.
// Layers: bootstrap (cached) → curated (partially cached) → daily context (not cached)
func (m *Manager) BuildSystemPromptSection(ctx context.Context) string {
	// Add a defensive timeout to prevent indefinite blocking when called
	// from queued requests that may use context.Background().
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var parts []string

	// Layer 1: Bootstrap files — now injected via SessionStart hook.
	// The hook fires on startup, resume, compact, and clear events,
	// so bootstrap content survives compaction without system prompt injection.

	// Layer 2: CLAUDE.md files (read live — always fresh, even after compaction)
	for _, mdPath := range m.claudeMDPaths {
		data, err := os.ReadFile(mdPath)
		if err != nil {
			log.Printf("Memory: warning reading CLAUDE.md %s: %v", mdPath, err)
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		name := filepath.Base(filepath.Dir(mdPath)) + "/" + filepath.Base(mdPath)
		escapedName := format.XMLEscapeAttr(name)
		escapedContent := format.CDATAEscape(content)
		parts = append(parts, fmt.Sprintf("<claude_md name=\"%s\">\n<![CDATA[%s]]>\n</claude_md>", escapedName, escapedContent))
	}

	// Layer 3: Curated memories (CDATA-wrapped to prevent XML injection)
	curated, err := m.GetCuratedMemories(ctx, m.maxCurated)
	if err == nil && len(curated) > 0 {
		var memLines []string
		for _, mem := range curated {
			escapedContent := format.CDATAEscape(mem.Content)
			memLines = append(memLines, fmt.Sprintf("- [%s] (importance:%d) <![CDATA[%s]]>", mem.Category, mem.Importance, escapedContent))
		}
		parts = append(parts, "<memories>\n"+strings.Join(memLines, "\n")+"\n</memories>")
	}

	// Layer 3: Recent daily summaries
	dailies, err := m.GetRecentDailySummaries(ctx, m.maxDailyDays)
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
func (m *Manager) StatusText(ctx context.Context) string {
	var lines []string

	m.mu.RLock()
	lines = append(lines, fmt.Sprintf("*Memory Status*\n\nBootstrap files: %d", len(m.bootstraps)))
	for _, bf := range m.bootstraps {
		lines = append(lines, fmt.Sprintf("  - %s (%d chars)", bf.Name, len(bf.Content)))
	}
	m.mu.RUnlock()

	if len(m.claudeMDPaths) > 0 {
		lines = append(lines, fmt.Sprintf("\nCLAUDE.md files: %d", len(m.claudeMDPaths)))
		for _, p := range m.claudeMDPaths {
			lines = append(lines, fmt.Sprintf("  - %s", p))
		}
	}

	curated, err := m.GetCuratedMemories(ctx, 100)
	if err == nil {
		lines = append(lines, fmt.Sprintf("\nCurated memories: %d", len(curated)))
	}

	dailies, err := m.GetRecentDailySummaries(ctx, m.maxDailyDays)
	if err == nil {
		lines = append(lines, fmt.Sprintf("Daily summaries: %d", len(dailies)))
	}

	return strings.Join(lines, "\n")
}

// CuratedMemoriesText returns formatted curated memories for display.
func (m *Manager) CuratedMemoriesText(ctx context.Context) string {
	curated, err := m.GetCuratedMemories(ctx, 50)
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

func (m *Manager) loadBootstrapFiles(ctx context.Context) error {
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

		// Skip oversized files to prevent OOM
		const maxBootstrapSize int64 = 1 << 20 // 1 MB
		info, err := entry.Info()
		if err != nil {
			log.Printf("Memory: warning stat %s: %v", path, err)
			continue
		}
		if info.Size() > maxBootstrapSize {
			log.Printf("Memory: skipping oversized bootstrap file %s (%d bytes, max %d)", name, info.Size(), maxBootstrapSize)
			continue
		}

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

		// Upsert to DB (best-effort — don't fail load on DB error)
		if err := m.upsertBootstrapToDB(ctx, bf); err != nil {
			log.Printf("Memory: warning upserting bootstrap %s: %v", bf.Name, err)
		}
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

func (m *Manager) upsertBootstrapToDB(ctx context.Context, bf *BootstrapFile) error {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO bootstrap_files (name, content, content_hash, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(name) DO UPDATE SET
		   content = excluded.content,
		   content_hash = excluded.content_hash,
		   updated_at = excluded.updated_at`,
		bf.Name, bf.Content, bf.ContentHash,
	)
	return err
}
