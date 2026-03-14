package agentsync

import (
	"testing"
)

func TestClaudeAgentToOpenCodeName(t *testing.T) {
	tests := []struct {
		path    string
		baseDir string
		want    string
	}{
		{"/w/.claude/agents/coding/go-team/go-purist.md", "/w/.claude/agents", "go-purist.md"},
		{"/w/.claude/agents/coding/go-team/go-pragmatist.md", "/w/.claude/agents", "go-pragmatist.md"},
		{"/w/.claude/agents/design/security-agent.md", "/w/.claude/agents", "design-security.md"},
		{"/w/.claude/agents/design/evaluation-agent.md", "/w/.claude/agents", "design-evaluation.md"},
		{"/w/.claude/agents/coding/frontend-team/fe-purist.md", "/w/.claude/agents", "fe-purist.md"},
		{"/w/.claude/agents/coding/browser-team/functional-agents.md", "/w/.claude/agents", "browser-functional.md"},
		{"/w/.claude/agents/coding/integration-team/api-guardian-agent.md", "/w/.claude/agents", "api-guardian.md"},
		{"/w/.claude/agents/coding/logging-audit-agent.md", "/w/.claude/agents", "logging-audit.md"},
		{"/w/.claude/agents/coding/data-integrity-flow-agent.md", "/w/.claude/agents", "data-flow-integrity.md"},
		{"/w/.claude/agents/assessment/project-assessment-agent.md", "/w/.claude/agents", "project-assessment.md"},
		// Missing agents that should be synced
		{"/w/.claude/agents/grey-team/article-writer-agent.md", "/w/.claude/agents", "article-writer-agent.md"},
		{"/w/.claude/agents/grey-team/surveyor-email-drafter.md", "/w/.claude/agents", "surveyor-email-drafter.md"},
		{"/w/.claude/agents/coding/kotlin-team/kotlin-purist.md", "/w/.claude/agents", "kotlin-purist.md"},
		{"/w/.claude/agents/coding/kotlin-team/mobile-security-agent.md", "/w/.claude/agents", "mobile-security-agent.md"},
		{"/w/.claude/agents/research/ui-ux-researcher.md", "/w/.claude/agents", "ui-ux-researcher.md"},
		{"/w/.claude/agents/coding/coding-agent.md", "/w/.claude/agents", "coding-agent.md"},
	}

	for _, tt := range tests {
		got := claudeAgentToOpenCodeName(tt.path, tt.baseDir)
		if got != tt.want {
			t.Errorf("claudeAgentToOpenCodeName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestOpencodeAgentToClaudePath(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"go-purist.md", "coding/go-team/go-purist.md"},
		{"design-security.md", "design/security-agent.md"},
		{"fe-purist.md", "coding/frontend-team/fe-purist.md"},
		{"browser-functional.md", "coding/browser-team/functional-agents.md"},
		{"api-guardian.md", "coding/integration-team/api-guardian-agent.md"},
		{"logging-audit.md", "coding/logging-audit-agent.md"},
		{"project-assessment.md", "assessment/project-assessment-agent.md"},
	}

	for _, tt := range tests {
		got := opencodeAgentToClaudePath(tt.name)
		if got != tt.want {
			t.Errorf("opencodeAgentToClaudePath(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestStripFrontmatter(t *testing.T) {
	input := "---\ndescription: test\nmode: subagent\n---\n\n# Hello\nBody text"
	got := stripFrontmatter(input)
	want := "# Hello\nBody text"
	if got != want {
		t.Errorf("stripFrontmatter = %q, want %q", got, want)
	}
}

func TestClaudeCmdToOpenCode(t *testing.T) {
	input := "Run the design review.\n\nExecute this command:\n```bash\ncd /workspace && go run main.go\n```\n\nDone."
	got := claudeCmdToOpenCode(input)

	if !hasSyncMarker(got) {
		t.Error("missing sync marker")
	}
	if !contains(got, "---\ndescription: Run the design review.\n---") {
		t.Errorf("missing frontmatter in: %s", got)
	}
	if !contains(got, "`!cd /workspace && go run main.go`") {
		t.Errorf("missing converted command in: %s", got)
	}
}

func TestOpencodeCmdToClaude(t *testing.T) {
	input := "---\ndescription: Run design review\n---\n\nRun the design review.\n\nExecute: `!cd /workspace && go run main.go`\n\nDone."
	got := opencodeCmdToClaude(input)

	if !hasSyncMarker(got) {
		t.Error("missing sync marker")
	}
	if !contains(got, "```bash\ncd /workspace && go run main.go\n```") {
		t.Errorf("missing converted command block in: %s", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
