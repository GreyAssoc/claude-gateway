package skills

import (
	"database/sql"
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

// Manager loads, watches, and serves skills from disk and SQLite.
type Manager struct {
	db   *db.DB
	dirs []string

	mu      sync.RWMutex
	skills  map[string]*Skill
	watcher *fsnotify.Watcher
	done    chan struct{}
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
	if err := m.loadFromDB(); err != nil {
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

// Stop halts the file watcher.
func (m *Manager) Stop() {
	close(m.done)
	if m.watcher != nil {
		m.watcher.Close()
	}
}

// GetActiveSkills returns all enabled skills sorted by precedence (highest first).
func (m *Manager) GetActiveSkills() []*Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var active []*Skill
	for _, s := range m.skills {
		if s.Enabled {
			active = append(active, s)
		}
	}

	sort.Slice(active, func(i, j int) bool {
		return active[i].Precedence > active[j].Precedence
	})

	return active
}

// GetAllSkills returns all skills regardless of enabled state.
func (m *Manager) GetAllSkills() []*Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []*Skill
	for _, s := range m.skills {
		all = append(all, s)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Name < all[j].Name
	})

	return all
}

// EnableSkill enables a skill by name.
func (m *Manager) EnableSkill(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.skills[name]
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}

	s.Enabled = true
	_, err := m.db.Exec("UPDATE skills SET enabled = 1, updated_at = datetime('now') WHERE name = ?", name)
	return err
}

// DisableSkill disables a skill by name.
func (m *Manager) DisableSkill(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.skills[name]
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}

	s.Enabled = false
	_, err := m.db.Exec("UPDATE skills SET enabled = 0, updated_at = datetime('now') WHERE name = ?", name)
	return err
}

// GetSkill returns a skill by name.
func (m *Manager) GetSkill(name string) (*Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.skills[name]
	return s, ok
}

// BuildSystemPromptSection returns the skills section for the system prompt.
func (m *Manager) BuildSystemPromptSection() string {
	active := m.GetActiveSkills()
	if len(active) == 0 {
		return ""
	}
	return FormatSkillsXML(active)
}

func (m *Manager) loadFromDB() error {
	rows, err := m.db.Query("SELECT name, source_path, content, content_hash, precedence, enabled FROM skills")
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
			return err
		}
		s.Enabled = enabled == 1
		m.skills[s.Name] = &s
	}

	return rows.Err()
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

func (m *Manager) loadSkillFile(path string, precedence int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	skill, err := ParseSkillMD(string(data), path, precedence)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already in DB with same hash
	existing, ok := m.skills[skill.Name]
	if ok && existing.ContentHash == skill.ContentHash {
		return nil // unchanged
	}

	// Preserve enabled state from existing
	if ok {
		skill.Enabled = existing.Enabled
	}

	m.skills[skill.Name] = skill

	// Upsert to DB
	return m.upsertToDB(skill)
}

func (m *Manager) upsertToDB(s *Skill) error {
	_, err := m.db.Exec(
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
	return err
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
			}
		case err, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Skills: watcher error: %v", err)
		}
	}
}

func (m *Manager) precedenceForPath(path string) int {
	absPath, _ := filepath.Abs(path)
	for i, dir := range m.dirs {
		absDir, _ := filepath.Abs(dir)
		if strings.HasPrefix(absPath, absDir) {
			return i
		}
	}
	return 0
}

func isSkillFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".md") &&
		(strings.Contains(lower, "skill") || strings.HasPrefix(lower, "skill"))
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
		lines = append(lines, fmt.Sprintf("[%s] *%s* — %s", icon, s.Name, desc))
	}

	return strings.Join(lines, "\n")
}

// Ensure sql import is used via QueryRow pattern
var _ = sql.ErrNoRows
