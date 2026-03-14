package agentsync

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// --- Agent name mapping ---

// agentDirMap maps OpenCode flat prefixes to Claude Code nested directory paths.
// Used for OpenCode→Claude direction to reconstruct the nested structure.
var agentDirMap = map[string]string{
	"go-":                "coding/go-team/",
	"fe-":                "coding/frontend-team/",
	"design-":            "design/",
	"api-guardian":        "coding/integration-team/",
	"data-flow-failure":  "coding/integration-team/",
	"data-flow-integrity":"coding/",
	"browser-":           "coding/browser-team/",
	"logging-audit":      "coding/",
	"design-adherence":   "coding/",
	"mql-":               "coding/mql-team/",
	"cross-language":     "coding/security-review/",
	"kotlin-":            "coding/kotlin-team/",
	"mobile-":            "coding/kotlin-team/",
	"android-":           "coding/kotlin-team/",
	"coding-agent":       "coding/",
	"project-assessment": "assessment/",
	"ui-ux-":             "research/",
	"page-content":       "research/",
	"seo-":               "research/",
	"article-writer":     "grey-team/",
	"quality-review":     "grey-team/",
	"topic-research":     "grey-team/",
	"seo-content":        "grey-team/",
	"surveyor-email":     "grey-team/",
}

// agentNameMap maps OpenCode flat names ↔ Claude Code filenames where the
// naming convention isn't a simple suffix strip. Only exceptions go here.
var opencodeToClaudeName = map[string]string{
	"go-security.md":         "go-security-agent.md",
	"go-hacker.md":           "go-hacker-agent.md",
	"fe-security.md":         "fe-security-agent.md",
	"fe-hacker.md":           "fe-hacker-agent.md",
	"design-security.md":     "security-agent.md",
	"design-compliance.md":   "compliance-agent.md",
	"design-data-integrity.md": "data-integrity-agent.md",
	"design-performance.md":  "performance-agent.md",
	"design-error-handling.md": "error-handling-agent.md",
	"api-guardian.md":         "api-guardian-agent.md",
	"data-flow-failure.md":   "data-flow-and-failure-agents.md",
	"data-flow-integrity.md": "data-integrity-flow-agent.md",
	"browser-functional.md":  "functional-agents.md",
	"browser-security.md":    "security-agents.md",
	"logging-audit.md":       "logging-audit-agent.md",
	"design-adherence.md":    "design-adherence-agent.md",
	"project-assessment.md":  "project-assessment-agent.md",
}

// claudeAgentToOpenCodeName converts a Claude Code agent path to an OpenCode flat filename.
// e.g. "coding/go-team/go-purist.md" → "go-purist.md"
//      "design/security-agent.md" → "design-security.md"
func claudeAgentToOpenCodeName(fullPath, baseDir string) string {
	relPath, _ := filepath.Rel(baseDir, fullPath)
	baseName := filepath.Base(relPath)
	dirPath := filepath.Dir(relPath)

	// Check reverse map first (Claude→OpenCode direction).
	for ocName, claudeName := range opencodeToClaudeName {
		expectedDir := agentDirForOpenCode(ocName)
		if baseName == claudeName && strings.TrimSuffix(dirPath+"/", "./") == expectedDir {
			return ocName
		}
	}

	// Default: if the file is in a subdirectory like design/, prefix it.
	switch {
	case strings.HasPrefix(dirPath, "design"):
		// design/foo-agent.md → design-foo.md (strip -agent suffix)
		name := strings.TrimSuffix(baseName, "-agent.md")
		return "design-" + name + ".md"
	case strings.Contains(dirPath, "go-team"):
		return baseName // already prefixed with go-
	case strings.Contains(dirPath, "frontend-team"):
		return baseName // already prefixed with fe-
	case strings.Contains(dirPath, "kotlin-team"):
		return baseName
	case strings.Contains(dirPath, "browser-team"):
		name := strings.TrimSuffix(baseName, "-agents.md")
		name = strings.TrimSuffix(name, "-agent.md")
		return "browser-" + name + ".md"
	case strings.Contains(dirPath, "integration-team"):
		name := strings.TrimSuffix(baseName, "-agent.md")
		name = strings.TrimSuffix(name, "-agents.md")
		return name + ".md"
	case strings.Contains(dirPath, "security-review"):
		return baseName
	case strings.Contains(dirPath, "mql-team"):
		return baseName
	case strings.HasPrefix(dirPath, "grey-team"):
		return baseName
	case strings.HasPrefix(dirPath, "research"):
		return baseName
	case strings.HasPrefix(dirPath, "assessment"):
		return baseName
	default:
		return baseName
	}
}

// opencodeAgentToClaudePath converts an OpenCode flat filename to a Claude Code
// nested relative path. e.g. "go-purist.md" → "coding/go-team/go-purist.md"
func opencodeAgentToClaudePath(ocName string) string {
	// Check explicit mapping first.
	if claudeName, ok := opencodeToClaudeName[ocName]; ok {
		dir := agentDirForOpenCode(ocName)
		return filepath.Join(dir, claudeName)
	}

	// Infer directory from prefix.
	dir := agentDirForOpenCode(ocName)
	if dir != "" {
		return filepath.Join(dir, ocName)
	}
	return ocName
}

// agentDirForOpenCode returns the Claude Code directory for an OpenCode agent name.
func agentDirForOpenCode(ocName string) string {
	// Try longest prefix match.
	bestKey := ""
	bestDir := ""
	for prefix, dir := range agentDirMap {
		if strings.HasPrefix(ocName, prefix) && len(prefix) > len(bestKey) {
			bestKey = prefix
			bestDir = dir
		}
	}
	return bestDir
}

// --- Agent content translation ---

// tierTemperatureMap maps agent types to temperature values for OpenCode.
var tierTemperatureMap = map[string]float64{
	"security": 0.1,
	"hacker":   0.1,
	"compliance": 0.1,
	"data-integrity": 0.1,
	"purist":    0.2,
	"pragmatist": 0.3,
	"pessimist": 0.2,
	"performance": 0.2,
	"error-handling": 0.2,
	"evaluation": 0.3,
	"adherence": 0.2,
	"logging":   0.1,
	"audit":     0.1,
	"guardian":  0.2,
	"assessment": 0.3,
	"researcher": 0.3,
	"content":   0.3,
	"writer":    0.3,
	"coding":    0.2,
}

// inferTemperature guesses temperature from agent filename.
func inferTemperature(name string) float64 {
	nameLower := strings.ToLower(name)
	for keyword, temp := range tierTemperatureMap {
		if strings.Contains(nameLower, keyword) {
			return temp
		}
	}
	return 0.2 // default
}

// inferDescription extracts a short description from markdown content.
// Looks for the first heading or first non-empty paragraph.
func inferDescription(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			// Use heading text as description.
			return strings.TrimPrefix(line, "# ")
		}
		if strings.HasPrefix(line, "## ") {
			return strings.TrimPrefix(line, "## ")
		}
		// First non-heading, non-empty line.
		if len(line) > 80 {
			return line[:77] + "..."
		}
		return line
	}
	return "Agent"
}

// claudeAgentToOpenCode translates a Claude Code agent (plain markdown) to
// OpenCode format (YAML frontmatter + markdown).
func claudeAgentToOpenCode(content, ocName string) string {
	desc := inferDescription(content)
	temp := inferTemperature(ocName)

	var sb strings.Builder
	sb.WriteString(syncMarker)
	sb.WriteString("\n---\n")
	sb.WriteString(fmt.Sprintf("description: %s\n", desc))
	sb.WriteString("mode: subagent\n")
	sb.WriteString(fmt.Sprintf("temperature: %.1f\n", temp))
	sb.WriteString("tools:\n")
	sb.WriteString("  write: false\n")
	sb.WriteString("  edit: false\n")
	sb.WriteString("  bash: false\n")
	sb.WriteString("---\n\n")
	sb.WriteString(content)
	return sb.String()
}

// opencodeAgentToClaude translates an OpenCode agent (YAML frontmatter + markdown)
// to Claude Code format (plain markdown).
func opencodeAgentToClaude(content string) string {
	body := stripFrontmatter(content)
	return syncMarker + "\n" + body
}

// --- Command translation ---

var bashBlockRe = regexp.MustCompile("(?s)```bash\\s*\n(.*?)```")

// claudeCmdToOpenCode converts a Claude Code command to OpenCode format.
func claudeCmdToOpenCode(content string) string {
	// Extract first line as description.
	desc := ""
	lines := strings.SplitN(content, "\n", 2)
	if len(lines) > 0 {
		desc = strings.TrimSpace(lines[0])
	}
	if desc == "" {
		desc = "Command"
	}

	// Convert bash code blocks to !-prefixed inline commands.
	body := bashBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := bashBlockRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		cmd := strings.TrimSpace(sub[1])
		return fmt.Sprintf("Execute: `!%s`", cmd)
	})

	// Remove the "Execute this command:" line if present (it's now integrated).
	body = strings.Replace(body, "Execute this command:\nExecute:", "Execute:", 1)

	var sb strings.Builder
	sb.WriteString(syncMarker)
	sb.WriteString("\n---\n")
	sb.WriteString(fmt.Sprintf("description: %s\n", desc))
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	return sb.String()
}

var inlineExecRe = regexp.MustCompile("`!([^`]+)`")

// opencodeCmdToClaude converts an OpenCode command to Claude Code format.
func opencodeCmdToClaude(content string) string {
	body := stripFrontmatter(content)

	// Convert !-prefixed inline commands to bash code blocks.
	body = inlineExecRe.ReplaceAllStringFunc(body, func(match string) string {
		sub := inlineExecRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		cmd := sub[1]
		return fmt.Sprintf("```bash\n%s\n```", cmd)
	})

	// Replace "Execute: ```bash" pattern with "Execute this command:"
	body = strings.Replace(body, "Execute: ```bash", "Execute this command:\n```bash", 1)

	return syncMarker + "\n" + body
}

// --- Helpers ---

// stripFrontmatter removes YAML frontmatter (---...---) from the beginning of content.
func stripFrontmatter(content string) string {
	// Trim any sync marker first.
	content = strings.Replace(content, syncMarker+"\n", "", 1)
	content = strings.TrimSpace(content)

	if !strings.HasPrefix(content, "---") {
		return content
	}
	// Find the closing ---.
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return content
	}
	// Skip past closing --- and any following newlines.
	body := rest[idx+4:]
	return strings.TrimLeft(body, "\n")
}
