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
	}, nil
}

// FormatSkillsXML formats a slice of skills into a compact XML block for injection.
// Includes an auto-generated index so Claude can quickly match skills by name/description
// without scanning through all the CDATA content blocks.
// Uses CDATA sections to prevent XML injection from skill content.
func FormatSkillsXML(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<skills>\n")

	// Auto-generated index for quick skill detection
	sb.WriteString("<skill_index>\n")
	sb.WriteString("<!-- Auto-generated from loaded skill files. Match by intent, not exact phrasing. -->\n")
	for _, s := range skills {
		desc := s.Description
		if desc == "" {
			desc = s.Name
		}
		name := format.XMLEscapeAttr(s.Name)
		sb.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
	}
	sb.WriteString("</skill_index>\n\n")

	for _, s := range skills {
		// Escape name attribute value and wrap content in CDATA
		name := format.XMLEscapeAttr(s.Name)
		sb.WriteString(fmt.Sprintf(`<skill name="%s">`, name))
		sb.WriteString("\n<![CDATA[")
		sb.WriteString(format.CDATAEscape(s.Content))
		sb.WriteString("]]>\n</skill>\n")
	}
	sb.WriteString("</skills>")
	return sb.String()
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
