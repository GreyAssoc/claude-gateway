package skills

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/you/claude-gateway/internal/format"
)

// Skill represents a parsed SKILL.md file.
type Skill struct {
	Name        string
	Description string
	Command     string // Telegram /command trigger (empty = no command routing)
	Content     string
	SourcePath  string
	ContentHash string
	Precedence  int // 0=bundled, 1=managed, 2=workspace
	Enabled     bool
	Always      bool   // inject full body on every Qwen/vLLM turn
	Harness     bool   // framework-agnostic; prefer over Claude-only skills on small windows
	Kind        string // "skill", "agent", or "persona"
	Owner       string // chat-agent folder handle; empty = global
}

// ParseSkillMD parses a SKILL.md file. Format:
//
//	---
//	name: skill-name
//	description: what this skill does
//	---
//	<markdown body>
//
// If no YAML frontmatter is present, the filename (minus extension) is used as the name
// and the entire content is treated as the body.
func ParseSkillMD(content, sourcePath string, precedence int) (*Skill, error) {
	name := extractNameFromPath(sourcePath)
	description := ""
	command := ""
	always := false
	harness := false
	body := content

	// Try to extract YAML frontmatter (between --- delimiters)
	if strings.HasPrefix(strings.TrimSpace(content), "---") {
		trimmed := strings.TrimSpace(content)
		rest := trimmed[3:] // skip first ---
		endIdx := strings.Index(rest, "---")
		if endIdx > 0 {
			frontmatter := rest[:endIdx]
			body = strings.TrimSpace(rest[endIdx+3:])

			// Parse frontmatter lines
			for _, line := range strings.Split(frontmatter, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
					name = strings.Trim(name, `"'`)
				}
				if strings.HasPrefix(line, "description:") {
					description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
					description = strings.Trim(description, `"'`)
				}
				if strings.HasPrefix(line, "command:") {
					command = strings.TrimSpace(strings.TrimPrefix(line, "command:"))
					command = strings.Trim(command, `"'`)
					command = strings.TrimPrefix(command, "/") // normalize: strip leading /
				}
				if strings.HasPrefix(line, "always:") {
					always = parseYAMLBool(strings.TrimSpace(strings.TrimPrefix(line, "always:")))
				}
				if strings.HasPrefix(line, "harness:") {
					harness = parseYAMLBool(strings.TrimSpace(strings.TrimPrefix(line, "harness:")))
				}
			}
		}
	}

	if name == "" {
		return nil, fmt.Errorf("skill has no name and no filename: %s", sourcePath)
	}

	hash := hashContent(content)

	return &Skill{
		Name:        name,
		Description: description,
		Command:     command,
		Content:     body,
		SourcePath:  sourcePath,
		ContentHash: hash,
		Precedence:  precedence,
		Enabled:     true,
		Always:      always,
		Harness:     harness,
		Kind:        "skill",
	}, nil
}

func parseYAMLBool(v string) bool {
	v = strings.Trim(strings.ToLower(v), `"'`)
	return v == "true" || v == "yes" || v == "1"
}

// FormatSkillIndex returns a lightweight index of skill names and descriptions.
// Always injected so Claude knows what skills exist and can request them.
func FormatSkillIndex(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<skills>\n")
	sb.WriteString("<skill_index>\n")
	sb.WriteString("<!-- Auto-generated from loaded skill files. Match by intent, not exact phrasing. -->\n")
	sb.WriteString("<!-- Full skill content is injected only when a skill matches the user's message. -->\n")
	for _, s := range skills {
		desc := s.Description
		if desc == "" {
			desc = s.Name
		}
		name := format.XMLEscapeAttr(s.Name)
		sb.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
	}
	sb.WriteString("</skill_index>\n")
	sb.WriteString("</skills>")
	return sb.String()
}

// FormatSkillsXML formats a slice of skills into a compact XML block for injection.
// Includes the full CDATA content for each skill.
// Uses CDATA sections to prevent XML injection from skill content.
func FormatSkillsXML(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<skills>\n")
	for _, s := range skills {
		name := format.XMLEscapeAttr(s.Name)
		kind := s.Kind
		if kind == "" {
			kind = "skill"
		}
		sb.WriteString(fmt.Sprintf(`<skill name="%s" kind="%s">`, name, format.XMLEscapeAttr(kind)))
		sb.WriteString("\n<![CDATA[")
		sb.WriteString(format.CDATAEscape(s.Content))
		sb.WriteString("]]>\n</skill>\n")
	}
	sb.WriteString("</skills>")
	return sb.String()
}

// maxMatchedSkills caps the number of full skill contents injected per message.
const maxMatchedSkills = 3

// MatchSkills returns skills whose name or description matches the user message.
// Uses simple keyword matching: direct name containment or 2+ word overlap
// with the description.
func MatchSkills(skills []Skill, message string) []Skill {
	msgLower := strings.ToLower(message)
	var matched []Skill
	for _, s := range skills {
		nameLower := strings.ToLower(s.Name)
		// Direct name match: "notices" in message matches skill "notices"
		if strings.Contains(msgLower, nameLower) || catalogNameHit(message, s.Name) {
			matched = append(matched, s)
			continue
		}
		// Description word overlap (2+ words of 4+ chars)
		if containsAnyWord(msgLower, strings.ToLower(s.Description)) {
			matched = append(matched, s)
		}
	}
	if len(matched) > maxMatchedSkills {
		matched = matched[:maxMatchedSkills]
	}
	return matched
}

// MatchSkillsPreferHarness is MatchSkills, but if any harness-flagged skill
// matched, drop Claude-only skills. Small-window models (Qwen) should not
// spend tokens on Task-tool / agent workflows they cannot run.
func MatchSkillsPreferHarness(skills []Skill, message string) []Skill {
	matched := MatchSkills(skills, message)
	if len(matched) == 0 {
		return matched
	}
	var harness []Skill
	for _, s := range matched {
		if s.Harness {
			harness = append(harness, s)
		}
	}
	if len(harness) > 0 {
		return harness
	}
	return matched
}

// containsAnyWord returns true if 2+ words (4+ chars each) from content
// appear in message. Both must be pre-lowercased by the caller.
func catalogNameHit(message, name string) bool {
	msg := normalizePhrase(message)
	n := normalizePhrase(name)
	if n == "" || len(n) < 5 {
		return false
	}
	return strings.Contains(msg, n)
}

func normalizePhrase(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

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

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

func extractNameFromPath(path string) string {
	filename := filepath.Base(path)

	// Remove extension
	if ext := filepath.Ext(filename); ext != "" {
		filename = strings.TrimSuffix(filename, ext)
	}

	// Convert SKILL to a reasonable name
	filename = strings.TrimPrefix(filename, "SKILL-")
	filename = strings.TrimPrefix(filename, "SKILL_")
	filename = strings.ToLower(filename)

	return filename
}
