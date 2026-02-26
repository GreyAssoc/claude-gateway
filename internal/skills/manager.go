// Package skills loads, watches, and serves prompt skill files from disk and SQLite.
package skills

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/you/claude-gateway/internal/db"
)

// ErrSkillNotFound is returned when a skill name is not in the manager.
var ErrSkillNotFound = errors.New("skill not found")

// Manager loads, watches, and serves skills from disk and SQLite.
type Manager struct {
	db   *db.DB
	dirs []string

	mu       sync.RWMutex
	skills   map[string]*Skill
	watcher  *fsnotify.Watcher
	done     chan struct{}
	stopOnce sync.Once
}

// NewManager creates a skills manager.
func NewManager(database *db.DB, dirs []string) *Manager {
	return &Manager{
		db:     database,
		dirs:   dirs,
		skills: make(map[string]*Skill),
		done:   make(chan struct{}),
	}
}

// Start loads skills from DB and disk, then starts watching for changes.
func (m *Manager) Start() error {
	// Load from DB first (preserves enabled/disabled state)
	if err := m.loadFromDB(context.Background()); err != nil {
		log.Printf("Skills: warning loading from DB: %v", err)
	}

	// Scan directories and upsert
	for precedence, dir := range m.dirs {
		if err := m.scanDir(dir, precedence); err != nil {
			log.Printf("Skills: warning scanning %s: %v", dir, err)
		}
	}

	// Start file watcher
	if err := m.startWatcher(); err != nil {
		log.Printf("Skills: warning starting watcher: %v", err)
	}

	count := len(m.GetActiveSkills())
	log.Printf("Skills: %d active skills loaded", count)
	return nil
}

// Stop halts the file watcher. Safe to call multiple times.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.done)
		if m.watcher != nil {
			m.watcher.Close()
		}
	})
}

// GetActiveSkills returns all enabled skills sorted by precedence (highest first).
// Returns value copies to prevent callers from racing with concurrent Enabled toggles.
func (m *Manager) GetActiveSkills() []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var active []Skill
	for _, s := range m.skills {
		if s.Enabled {
			active = append(active, *s)
		}
	}

	sort.Slice(active, func(i, j int) bool {
		return active[i].Precedence > active[j].Precedence
	})

	return active
}

// GetAllSkills returns all skills regardless of enabled state.
// Returns value copies to prevent callers from racing with concurrent Enabled toggles.
func (m *Manager) GetAllSkills() []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []Skill
	for _, s := range m.skills {
		all = append(all, *s)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Name < all[j].Name
	})

	return all
}

// EnableSkill enables a skill by name.
func (m *Manager) EnableSkill(ctx context.Context, name string) error {
	return m.setSkillEnabled(ctx, name, true)
}

// DisableSkill disables a skill by name.
func (m *Manager) DisableSkill(ctx context.Context, name string) error {
	return m.setSkillEnabled(ctx, name, false)
}

// setSkillEnabled sets the enabled state of a skill. DB is written first to ensure consistency.
func (m *Manager) setSkillEnabled(ctx context.Context, name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.skills[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}

	if _, err := m.db.ExecContext(ctx, "UPDATE skills SET enabled = ?, updated_at = datetime('now') WHERE name = ?", boolToInt(enabled), name); err != nil {
		return fmt.Errorf("updating skill %q enabled state: %w", name, err)
	}
	s.Enabled = enabled
	return nil
}

// ToggleSkill atomically toggles a skill's enabled state, returning the new state.
// This avoids TOCTOU races from separate GetSkill + Enable/Disable calls.
func (m *Manager) ToggleSkill(ctx context.Context, name string) (enabled bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.skills[name]
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}

	newState := !s.Enabled
	if _, err := m.db.ExecContext(ctx, "UPDATE skills SET enabled = ?, updated_at = datetime('now') WHERE name = ?", boolToInt(newState), name); err != nil {
		return false, fmt.Errorf("toggling skill %q: %w", name, err)
	}
	s.Enabled = newState
	return newState, nil
}

// GetSkillByCommand returns an enabled skill that matches the given Telegram command.
func (m *Manager) GetSkillByCommand(cmd string) (Skill, bool) {
	if cmd == "" {
		return Skill{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.skills {
		if s.Command == cmd && s.Enabled {
			return *s, true
		}
	}
	return Skill{}, false
}

// GetSkill returns a copy of a skill by name.
func (m *Manager) GetSkill(name string) (Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.skills[name]
	if !ok {
		return Skill{}, false
	}
	return *s, true
}

// BuildSystemPromptSection returns the skills section for the system prompt.
func (m *Manager) BuildSystemPromptSection() string {
	active := m.GetActiveSkills()
	if len(active) == 0 {
		return ""
	}
	return FormatSkillsXML(active)
}

func (m *Manager) loadFromDB(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, "SELECT name, source_path, content, content_hash, precedence, enabled FROM skills")
	if err != nil {
		return err
	}
	defer rows.Close()

	m.mu.Lock()
	defer m.mu.Unlock()

	for rows.Next() {
		var s Skill
		var enabled int
		if err := rows.Scan(&s.Name, &s.SourcePath, &s.Content, &s.ContentHash, &s.Precedence, &enabled); err != nil {
			return fmt.Errorf("scanning skill row: %w", err)
		}
		s.Enabled = enabled == 1
		m.skills[s.Name] = &s
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating skill rows: %w", err)
	}
	return nil
}

func (m *Manager) scanDir(dir string, precedence int) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // directory doesn't exist yet, not an error
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isSkillFile(name) {
			continue
		}

		path := filepath.Join(absDir, name)
		if err := m.loadSkillFile(path, precedence); err != nil {
			log.Printf("Skills: error loading %s: %v", path, err)
		}
	}

	return nil
}

// maxSkillFileSize is the maximum size for a skill file (1 MB).
const maxSkillFileSize = 1 << 20

func (m *Manager) loadSkillFile(path string, precedence int) error {
	// Read first, then check size to eliminate TOCTOU race
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > maxSkillFileSize {
		return fmt.Errorf("skill file %s too large (%d bytes, max %d)", filepath.Base(path), len(data), maxSkillFileSize)
	}

	skill, err := ParseSkillMD(string(data), path, precedence)
	if err != nil {
		return err
	}

	m.mu.Lock()

	// Check if already loaded with same hash
	existing, ok := m.skills[skill.Name]
	if ok && existing.ContentHash == skill.ContentHash {
		m.mu.Unlock()
		return nil // unchanged
	}

	// Preserve enabled state from existing
	if ok {
		skill.Enabled = existing.Enabled
	}
	m.mu.Unlock()

	// NOTE: The lock is intentionally released before the DB write to avoid
	// holding it during potentially slow I/O. This creates a window where
	// a concurrent setSkillEnabled could change the enabled state. The
	// invariants that make this safe:
	//   1. If upsertToDB fails, in-memory state is unchanged (no partial update).
	//   2. After DB succeeds, we re-acquire the lock and re-read the current
	//      enabled state, so any concurrent toggle is preserved.
	//   3. The DB upsert does NOT write the enabled column, so there's no
	//      conflict between this path and setSkillEnabled's DB write.
	if err := m.upsertToDB(context.Background(), skill); err != nil {
		return err
	}

	// Re-acquire lock and apply. Re-read enabled state in case it changed
	// between lock releases (e.g., concurrent setSkillEnabled call).
	m.mu.Lock()
	if cur, exists := m.skills[skill.Name]; exists {
		skill.Enabled = cur.Enabled
	}
	m.skills[skill.Name] = skill
	m.mu.Unlock()

	return nil
}

func (m *Manager) upsertToDB(ctx context.Context, s *Skill) error {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO skills (name, source_path, content, content_hash, precedence, enabled, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(name) DO UPDATE SET
		   source_path = excluded.source_path,
		   content = excluded.content,
		   content_hash = excluded.content_hash,
		   precedence = excluded.precedence,
		   updated_at = excluded.updated_at`,
		s.Name, s.SourcePath, s.Content, s.ContentHash, s.Precedence, boolToInt(s.Enabled),
	)
	if err != nil {
		return fmt.Errorf("upserting skill %q: %w", s.Name, err)
	}
	return nil
}

func (m *Manager) startWatcher() error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	m.watcher = w

	for i, dir := range m.dirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if _, err := os.Stat(absDir); err != nil {
			continue
		}
		if err := w.Add(absDir); err != nil {
			log.Printf("Skills: cannot watch %s: %v", absDir, err)
			continue
		}
		log.Printf("Skills: watching %s (precedence %d)", absDir, i)
	}

	go m.watchLoop()
	return nil
}

func (m *Manager) watchLoop() {
	for {
		select {
		case <-m.done:
			return
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			if !isSkillFile(filepath.Base(event.Name)) {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				log.Printf("Skills: detected change: %s", event.Name)
				// Determine precedence from directory
				precedence := m.precedenceForPath(event.Name)
				if err := m.loadSkillFile(event.Name, precedence); err != nil {
					log.Printf("Skills: error reloading %s: %v", event.Name, err)
				} else {
					log.Printf("Skills: reloaded %s", event.Name)
				}
			}
			if event.Op&fsnotify.Remove != 0 {
				log.Printf("Skills: file removed: %s", event.Name)
				m.removeSkillByPath(event.Name)
			}
		case err, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Skills: watcher error: %v", err)
		}
	}
}

// removeSkillByPath removes a skill from the in-memory map and disables it in DB
// when its source file is deleted from disk.
func (m *Manager) removeSkillByPath(path string) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	m.mu.Lock()
	var nameToRemove string
	for name, s := range m.skills {
		sAbs, err := filepath.Abs(s.SourcePath)
		if err != nil {
			sAbs = s.SourcePath
		}
		if sAbs == absPath {
			nameToRemove = name
			break
		}
	}
	if nameToRemove != "" {
		delete(m.skills, nameToRemove)
	}
	m.mu.Unlock()

	if nameToRemove != "" {
		if _, err := m.db.ExecContext(context.Background(), "UPDATE skills SET enabled = 0, updated_at = datetime('now') WHERE name = ?", nameToRemove); err != nil {
			log.Printf("Skills: warning disabling removed skill %q in DB: %v", nameToRemove, err)
		}
		log.Printf("Skills: removed %q (source file deleted)", nameToRemove)
	}
}

func (m *Manager) precedenceForPath(path string) int {
	absPath, err := filepath.Abs(path)
	if err != nil {
		log.Printf("Skills: error resolving path %s: %v", path, err)
		return 0
	}
	for i, dir := range m.dirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
			return i
		}
	}
	return 0
}

func isSkillFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".md") && strings.Contains(lower, "skill")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// StatusText returns a formatted status for Telegram /skills command.
func (m *Manager) StatusText() string {
	all := m.GetAllSkills()
	if len(all) == 0 {
		return "No skills loaded."
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("*Skills* (%d loaded)\n", len(all)))

	for _, s := range all {
		icon := "ON"
		if !s.Enabled {
			icon = "OFF"
		}
		desc := s.Description
		if desc == "" {
			desc = "(no description)"
		}
		cmdInfo := ""
		if s.Command != "" {
			cmdInfo = fmt.Sprintf(" (/%s)", s.Command)
		}
		lines = append(lines, fmt.Sprintf("[%s] *%s*%s — %s", icon, s.Name, cmdInfo, desc))
	}

	return strings.Join(lines, "\n")
}
