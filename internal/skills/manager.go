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
	db        *db.DB
	dirs      []string
	agentDirs []string

	mu         sync.RWMutex
	skills     map[string]*Skill
	agents     map[string]*Skill     // orchestrator DNA (coding/, design/, …)
	chatAgents map[string]*ChatAgent // folder agents with private skills
	onChat     func([]ChatAgent)     // fired after each folder scan (Postgres register)
	watcher    *fsnotify.Watcher
	done       chan struct{}
	stopOnce   sync.Once
}

// NewManager creates a skills manager.
func NewManager(database *db.DB, dirs []string) *Manager {
	return &Manager{
		db:         database,
		dirs:       dirs,
		skills:     make(map[string]*Skill),
		agents:     make(map[string]*Skill),
		chatAgents: make(map[string]*ChatAgent),
		done:       make(chan struct{}),
	}
}

// SetAgentDirs sets extra trees to scan for on-demand agent/persona prompts
// (.claude/agents). Call before Start.
func (m *Manager) SetAgentDirs(dirs []string) {
	m.agentDirs = dirs
}

// SetChatAgentsListener is called after every folder scan (startup and fsnotify).
// Used to upsert Telegram specialists into PostgreSQL. Must not call back into
// the manager while holding its lock — scanChatAgents releases the lock first.
func (m *Manager) SetChatAgentsListener(fn func([]ChatAgent)) {
	m.mu.Lock()
	m.onChat = fn
	m.mu.Unlock()
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

	for _, dir := range m.agentDirs {
		if err := m.scanAgentTree(dir); err != nil {
			log.Printf("Skills: warning scanning agents %s: %v", dir, err)
		}
	}
	m.scanChatAgents()

	count := len(m.GetActiveSkills())
	m.mu.RLock()
	nAgents := len(m.agents)
	nChat := len(m.chatAgents)
	m.mu.RUnlock()
	log.Printf("Skills: %d global skills, %d orchestrator DNA files, %d chat agents", count, nAgents, nChat)
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
	alts := []string{cmd, strings.ReplaceAll(cmd, "_", "-"), strings.ReplaceAll(cmd, "-", "_")}
	m.mu.RLock()
	defer m.mu.RUnlock()
	match := func(got string) bool {
		for _, a := range alts {
			if got == a {
				return true
			}
		}
		return false
	}
	for _, s := range m.skills {
		if s.Enabled && match(s.Command) {
			return *s, true
		}
	}
	for _, ag := range m.chatAgents {
		for _, s := range ag.Skills {
			if match(s.Command) {
				return s, true
			}
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

// BuildSkillIndex returns a lightweight index of skill names and descriptions.
// Always injected so Claude knows what skills exist without the full content.
func (m *Manager) BuildSkillIndex() string {
	active := m.GetActiveSkills()
	if len(active) == 0 {
		return ""
	}
	return FormatSkillIndex(active)
}

// BuildMatchedSkillsSection returns the skill index plus full content for
// any skills whose name or description matches the user message.
// This keeps prompt size small (~200 tokens) when no skills match,
// and only injects full content for relevant skills (0-3 matches).
func (m *Manager) BuildMatchedSkillsSection(message string) string {
	return m.BuildLibraryPrompt(message, 0)
}

// BuildHarnessPrompt is kept for callers; it now uses the shared library
// injector (skills + agents/personas, skip always-on bodies).
func (m *Manager) BuildHarnessPrompt(message string, maxChars int) string {
	return m.BuildLibraryPrompt(message, maxChars)
}

// BuildLibraryPrompt returns a name index of skills and agents plus full
// content for items that match the user message (max 3). Always-on harness
// skill bodies are omitted (the sidecar already injects those).
func (m *Manager) BuildLibraryPrompt(message string, maxChars int) string {
	return m.buildLibrary(message, maxChars)
}

func (m *Manager) GetAgents() []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Skill, 0, len(m.agents))
	for _, s := range m.agents {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) buildLibrary(message string, maxChars int) string {
	active := m.GetActiveSkills()
	chat := m.GetChatAgents()
	if len(active) == 0 && len(chat) == 0 {
		return ""
	}

	index := FormatSkillIndex(active)
	if len(chat) > 0 {
		if index != "" {
			index += "\n\n"
		}
		index += FormatChatAgentIndex(chat)
	}

	matched := MatchSkills(active, message)
	var extra []Skill
	seen := map[string]bool{}
	for _, s := range matched {
		if s.Always {
			continue
		}
		if seen[s.Kind+"/"+s.Name] {
			continue
		}
		seen[s.Kind+"/"+s.Name] = true
		extra = append(extra, s)
	}
	if len(extra) > maxMatchedSkills {
		extra = extra[:maxMatchedSkills]
	}
	if len(extra) == 0 {
		return index
	}

	xml := FormatSkillsXML(extra)
	if maxChars > 0 && len(xml) > maxChars {
		xml = xml[:maxChars] + "\n<!-- truncated library content -->"
	}
	return index + "\n\n" + xml
}

func FormatAgentIndex(agents []Skill) string {
	if len(agents) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<agents>\n")
	sb.WriteString("<agent_index>\n")
	sb.WriteString("<!-- Injected on demand when the user names one. Do not dump this tree. -->\n")
	for _, s := range agents {
		desc := s.Description
		if desc == "" {
			desc = s.Kind
			if desc == "" {
				desc = "agent"
			}
		}
		sb.WriteString(fmt.Sprintf("- %s (%s): %s\n", s.Name, s.Kind, desc))
	}
	sb.WriteString("</agent_index>\n")
	sb.WriteString("When the user asks to use one of these (e.g. \"go purist\", \"elon musk\", \"surveyor email drafter\"), follow that file's instructions for this turn.\n")
	sb.WriteString("</agents>")
	return sb.String()
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

func (m *Manager) scanAgentTree(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			rel, relErr := filepath.Rel(absDir, path)
			if relErr == nil && rel != "." && !strings.Contains(rel, string(filepath.Separator)) && isChatAgentDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isAgentPromptFile(path, info.Name()) {
			return nil
		}
		if info.Size() > maxSkillFileSize {
			log.Printf("Skills: skip oversized agent %s", path)
			return nil
		}
		if err := m.loadAgentFile(path); err != nil {
			log.Printf("Skills: error loading agent %s: %v", path, err)
		}
		return nil
	})
}

func isAgentPromptFile(path, name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".md") {
		return false
	}
	if lower == "readme.md" || lower == "consensus-protocol.md" {
		return false
	}
	if strings.HasPrefix(lower, "gateway-system-") || strings.HasPrefix(lower, "andy-system-") {
		return false
	}
	if isSkillFilename(name) {
		return false
	}
	return true
}

func (m *Manager) loadAgentFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	skill, err := ParseSkillMD(string(data), path, 2)
	if err != nil {
		return err
	}
	kind := "agent"
	if strings.Contains(filepath.ToSlash(path), "/personas/") {
		kind = "persona"
	}
	skill.Kind = kind
	skill.Enabled = true
	m.mu.Lock()
	m.agents[skill.Name] = skill
	m.mu.Unlock()
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
	for _, dir := range m.agentDirs {
		m.watchTree(dir)
	}

	go m.watchLoop()
	return nil
}

func (m *Manager) watchTree(root string) {
	if m.watcher == nil {
		return
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return
	}
	_ = filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.IsDir() {
			return nil
		}
		if err := m.watcher.Add(path); err != nil {
			log.Printf("Skills: cannot watch agent dir %s: %v", path, err)
		}
		return nil
	})
}

func (m *Manager) isChatAgentOwnedFile(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	for _, dir := range m.agentDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absDir, absPath)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		first, _, _ := strings.Cut(rel, string(filepath.Separator))
		if first != "" && isChatAgentDir(first) {
			return true
		}
	}
	return false
}

func (m *Manager) pathInAgentDirs(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	for _, dir := range m.agentDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if absPath == absDir || strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
			return true
		}
	}
	return false
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
			if m.pathInAgentDirs(event.Name) {
				if event.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = m.watcher.Add(event.Name)
					}
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
					log.Printf("Skills: agent tree change: %s", event.Name)
					m.scanChatAgents()
					base := filepath.Base(event.Name)
					// Folder specialists live in chatAgents. Do not also load their
					// .md into m.agents — that made @forex-trading match twice and
					// bind neither ("No unique agent folder matched").
					if isAgentPromptFile(event.Name, base) && !m.isChatAgentOwnedFile(event.Name) && event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
						if err := m.loadAgentFile(event.Name); err != nil {
							log.Printf("Skills: error reloading DNA %s: %v", event.Name, err)
						}
					}
				}
				continue
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
	lines = append(lines, fmt.Sprintf("*Global skills* (%d)\n", len(all)))

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

	chat := m.GetChatAgents()
	if len(chat) > 0 {
		lines = append(lines, "\n*Per-agent skills*")
		for _, a := range chat {
			if len(a.Skills) == 0 {
				lines = append(lines, fmt.Sprintf("@%s — (no skills yet)", prettyFolderHandle(a.Handle)))
				continue
			}
			names := make([]string, 0, len(a.Skills))
			for _, s := range a.Skills {
				if s.Command != "" {
					names = append(names, s.Name+" (/"+s.Command+")")
				} else {
					names = append(names, s.Name)
				}
			}
			lines = append(lines, fmt.Sprintf("@%s — %s", prettyFolderHandle(a.Handle), strings.Join(names, ", ")))
		}
	}

	return strings.Join(lines, "\n")
}
