package skills

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ChatAgent is a top-level folder under .claude/agents (e.g. youtube/, grey-docs/).
// Its skill-*.md files are private to that @handle.
type ChatAgent struct {
	Handle      string
	Dir         string
	DNA         string
	ProjectPath string // owned repo, e.g. /workspace/projects/forex-trading
	Framework   string // qwen, deepseek, grok, claude — empty means inherit gateway default
	Skills      []Skill
}

// orchestratorBuckets are review-team trees, not Telegram chat agents.
var orchestratorBuckets = map[string]bool{
	"coding":     true,
	"design":     true,
	"research":   true,
	"assessment": true,
}

func isSkillFilename(name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".md") {
		return false
	}
	base := strings.TrimSuffix(lower, ".md")
	return base == "skill" || strings.HasPrefix(base, "skill-")
}

func isChatAgentDir(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	return !orchestratorBuckets[strings.ToLower(name)]
}

func (m *Manager) scanChatAgents() {
	next := make(map[string]*ChatAgent)
	for _, root := range m.agentDirs {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !isChatAgentDir(e.Name()) {
				continue
			}
			dir := filepath.Join(abs, e.Name())
			ag := loadChatAgentFolder(dir, e.Name())
			if ag == nil {
				continue
			}
			next[ag.Handle] = ag
		}
	}
	m.mu.Lock()
	m.chatAgents = next
	fn := m.onChat
	m.mu.Unlock()
	log.Printf("Skills: %d chat agents (folder-scoped skills)", len(next))
	if fn != nil {
		fn(m.GetChatAgents())
	}
}

func loadChatAgentFolder(dir, folderName string) *ChatAgent {
	handle := strings.ToLower(folderName)
	ag := &ChatAgent{
		Handle: handle,
		Dir:    dir,
	}

	var dnaParts []string
	var skills []Skill

	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if isSkillFilename(name) {
			if info.Size() > maxSkillFileSize {
				return nil
			}
			s, err := loadSkillFromPath(path, 2)
			if err != nil {
				log.Printf("Skills: skip agent skill %s: %v", path, err)
				return nil
			}
			s.Owner = handle
			s.Kind = "skill"
			skills = append(skills, *s)
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		if strings.EqualFold(name, "readme.md") || strings.EqualFold(name, "consensus-protocol.md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		parsed, err := ParseSkillMD(string(data), path, 2)
		if err != nil {
			dnaParts = append(dnaParts, string(data))
			return nil
		}
		if parsed.Content != "" {
			dnaParts = append(dnaParts, parsed.Content)
		} else {
			dnaParts = append(dnaParts, string(data))
		}
		return nil
	})

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	ag.Skills = skills
	if len(dnaParts) > 0 {
		ag.DNA = strings.Join(dnaParts, "\n\n---\n\n")
	} else {
		ag.DNA = fmt.Sprintf("You are @%s, a specialist agent. Use only your attached skills and the shared general skills. Do not invent tools from other agents.", prettyFolderHandle(folderName))
	}
	ag.ProjectPath = ExtractProjectPath(ag.DNA, folderName)
	ag.Framework = ExtractFramework(ag.DNA)
	// Namesake DNA file wins (YAML `project:` / `framework:`).
	nameFile := filepath.Join(dir, folderName+".md")
	if data, err := os.ReadFile(nameFile); err == nil {
		raw := string(data)
		if p := ExtractProjectPath(raw, folderName); p != "" {
			ag.ProjectPath = p
		}
		if fw := ExtractFramework(raw); fw != "" {
			ag.Framework = fw
		}
	}
	return ag
}

// workspaceProjectRe matches /workspace/projects/<name> (one path segment).
var workspaceProjectRe = regexp.MustCompile(`/workspace/projects/[A-Za-z0-9][A-Za-z0-9._-]*`)

// ExtractProjectPath finds the repo an agent owns.
// Priority: YAML frontmatter `project:` / `project_path:` / `workdir:` / `cwd:`,
// then a /workspace/projects/<handle> mention in the first ~2.5k of the DNA
// (the "bold path at the top" convention). Other project paths in examples are ignored.
func ExtractProjectPath(raw, handle string) string {
	if p := projectPathFromYAML(raw); p != "" {
		return p
	}
	return projectPathFromText(raw, handle)
}

func yamlFrontmatterValue(raw string, keys ...string) string {
	fm := firstFrontmatter(raw)
	if fm == "" {
		return ""
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[strings.ToLower(k)] = true
	}
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if !want[strings.ToLower(strings.TrimSpace(key))] {
			continue
		}
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if val != "" {
			return val
		}
	}
	return ""
}

func projectPathFromYAML(raw string) string {
	return sanitizeProjectPath(yamlFrontmatterValue(raw, "project", "project_path", "workdir", "cwd"))
}

// ExtractFramework reads YAML `framework:` / `backend:` / `provider:` / `model:`.
// Empty means inherit the gateway default (Qwen).
func ExtractFramework(raw string) string {
	v := yamlFrontmatterValue(raw, "framework", "backend", "provider")
	if v == "" {
		v = yamlFrontmatterValue(raw, "model")
	}
	return NormalizeFramework(v)
}

// NormalizeFramework maps aliases (qwen3.8, vllm, DeepSeek V4 Pro) to switcher names.
func NormalizeFramework(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, `"'`)
	if s == "" {
		return ""
	}
	switch {
	case s == "ollama" || s == "vllm" || strings.HasPrefix(s, "qwen"):
		return "qwen"
	case s == "deepseek-flash" || s == "deepseek-v4-flash" || s == "deepseek-v4.1-flash" ||
		(strings.Contains(s, "deepseek") && strings.Contains(s, "flash")):
		return "deepseek-flash"
	case strings.Contains(s, "deepseek"):
		return "deepseek"
	case strings.Contains(s, "grok"):
		return "grok"
	case strings.Contains(s, "claude"):
		return "claude"
	case strings.Contains(s, "opencode"):
		return "opencode"
	default:
		return s
	}
}

func firstFrontmatter(raw string) string {
	s := strings.TrimSpace(raw)
	for strings.HasPrefix(s, "<!--") {
		i := strings.Index(s, "-->")
		if i < 0 {
			break
		}
		s = strings.TrimSpace(s[i+3:])
	}
	if !strings.HasPrefix(s, "---") {
		return ""
	}
	rest := s[3:]
	end := strings.Index(rest, "---")
	if end <= 0 {
		return ""
	}
	return rest[:end]
}

func projectPathFromText(raw, handle string) string {
	head := raw
	if len(head) > 2500 {
		head = head[:2500]
	}
	matches := workspaceProjectRe.FindAllString(head, -1)
	if len(matches) == 0 {
		return ""
	}
	seen := make(map[string]bool)
	var unique []string
	for _, m := range matches {
		p := sanitizeProjectPath(m)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		unique = append(unique, p)
	}
	if len(unique) == 0 {
		return ""
	}
	want := strings.ToLower(strings.TrimSpace(handle))
	if want == "" {
		return ""
	}
	for _, p := range unique {
		if strings.EqualFold(path.Base(p), want) {
			return p
		}
	}
	// Example YAML in agent-creator (and similar docs) mentions other repos.
	// Never steal a sibling project's path just because it appears in the DNA.
	return ""
}

func sanitizeProjectPath(val string) string {
	p := strings.TrimSpace(val)
	p = strings.Trim(p, "`\"'")
	p = strings.TrimRight(p, "/")
	if p == "" || strings.Contains(p, "\x00") {
		return ""
	}
	p = path.Clean(p)
	if p == "." || strings.Contains(p, "..") {
		return ""
	}
	switch {
	case strings.HasPrefix(p, "/workspace/projects/"):
		rest := strings.TrimPrefix(p, "/workspace/projects/")
		if rest == "" || rest == "." {
			return ""
		}
		return p
	case strings.HasPrefix(p, "/clients/"):
		if p == "/clients" || p == "/clients/" {
			return ""
		}
		return p
	default:
		return ""
	}
}

func prettyFolderHandle(folder string) string {
	parts := strings.FieldsFunc(folder, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		p = strings.ToLower(p)
		b.WriteString(strings.ToUpper(p[:1]))
		if len(p) > 1 {
			b.WriteString(p[1:])
		}
	}
	if b.Len() == 0 {
		return folder
	}
	return b.String()
}

func loadSkillFromPath(path string, precedence int) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxSkillFileSize {
		return nil, fmt.Errorf("too large")
	}
	return ParseSkillMD(string(data), path, precedence)
}

// GetChatAgents returns folder-based Telegram agents.
func (m *Manager) GetChatAgents() []ChatAgent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ChatAgent, 0, len(m.chatAgents))
	for _, a := range m.chatAgents {
		cp := *a
		cp.Skills = append([]Skill(nil), a.Skills...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out
}

// GetChatAgent looks up a folder agent by handle (youtube, grey-docs).
func (m *Manager) GetChatAgent(handle string) (ChatAgent, bool) {
	return m.FindChatAgent(handle)
}

// FindChatAgent matches folder name, @Handle, or compacted form (gopurist).
func (m *Manager) FindChatAgent(query string) (ChatAgent, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.TrimPrefix(q, "@")
	if q == "" {
		return ChatAgent{}, false
	}
	compact := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	qc := compact(q)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if a, ok := m.chatAgents[q]; ok && a != nil {
		cp := *a
		cp.Skills = append([]Skill(nil), a.Skills...)
		return cp, true
	}
	for _, a := range m.chatAgents {
		if a == nil {
			continue
		}
		if compact(a.Handle) == qc {
			cp := *a
			cp.Skills = append([]Skill(nil), a.Skills...)
			return cp, true
		}
	}
	return ChatAgent{}, false
}

// GetCommandSkills returns global and per-agent skills that expose a /command.
func (m *Manager) GetCommandSkills() []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Skill
	for _, s := range m.skills {
		if s.Enabled && s.Command != "" {
			out = append(out, *s)
		}
	}
	for _, ag := range m.chatAgents {
		for _, s := range ag.Skills {
			if s.Command != "" {
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func FormatChatAgentIndex(agents []ChatAgent) string {
	if len(agents) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<chat_agents>\n")
	sb.WriteString("Address one with @Handle (e.g. @Youtube). Each has private skills.\n")
	for _, a := range agents {
		fmt.Fprintf(&sb, "- @%s\n", prettyFolderHandle(a.Handle))
	}
	sb.WriteString("</chat_agents>")
	return sb.String()
}
