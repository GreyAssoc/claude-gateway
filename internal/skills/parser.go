package skills

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// Skill represents a parsed SKILL.md file.
type Skill struct {
	Name        string
	Description string
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
		Content:     body,
		SourcePath:  sourcePath,
		ContentHash: hash,
		Precedence:  precedence,
		Enabled:     true,
	}, nil
}

// FormatSkillsXML formats a slice of skills into a compact XML block for injection.
func FormatSkillsXML(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<skills>\n")
	for _, s := range skills {
		sb.WriteString(fmt.Sprintf(`<skill name="%s">`, s.Name))
		sb.WriteString("\n")
		sb.WriteString(s.Content)
		sb.WriteString("\n</skill>\n")
	}
	sb.WriteString("</skills>")
	return sb.String()
}

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

func extractNameFromPath(path string) string {
	// Get filename
	parts := strings.Split(path, "/")
	filename := parts[len(parts)-1]

	// Remove extension
	if idx := strings.LastIndex(filename, "."); idx > 0 {
		filename = filename[:idx]
	}

	// Convert SKILL to a reasonable name
	filename = strings.TrimPrefix(filename, "SKILL-")
	filename = strings.TrimPrefix(filename, "SKILL_")
	filename = strings.ToLower(filename)

	return filename
}
