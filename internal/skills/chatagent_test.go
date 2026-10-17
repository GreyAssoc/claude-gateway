package skills

import (
	"os"
	"strings"
	"testing"
)

func TestChatAgentFoldersIsolateSkills(t *testing.T) {
	dir := "/workspace/.claude/agents"
	if _, err := os.Stat(dir); err != nil {
		t.Skip("agents dir not mounted")
	}
	m := NewManager(nil, nil)
	m.SetAgentDirs([]string{dir})
	m.scanChatAgents()

	yt, ok := m.GetChatAgent("youtube")
	if !ok {
		t.Fatal("expected youtube chat agent folder")
	}
	if len(yt.Skills) == 0 {
		t.Fatal("youtube folder should own skill-youtube.md")
	}
	for _, s := range yt.Skills {
		if s.Owner != "youtube" {
			t.Errorf("youtube skill %s owner=%q", s.Name, s.Owner)
		}
	}

	docs, ok := m.GetChatAgent("grey-docs")
	if !ok {
		t.Fatal("expected grey-docs chat agent")
	}
	if len(docs.Skills) < 3 {
		t.Fatalf("grey-docs should own several skills, got %d", len(docs.Skills))
	}

	if _, ok := m.GetChatAgent("tbt-youtube"); !ok {
		t.Fatal("empty tbt-youtube folder should still be a chat agent")
	}

	for _, s := range m.GetActiveSkills() {
		if s.Name == "youtube" || strings.Contains(s.Name, "notices") {
			t.Fatalf("agent-private skill %q leaked into global GetActiveSkills", s.Name)
		}
	}

	lib := m.BuildLibraryPrompt("fetch a youtube transcript", 20000)
	if strings.Contains(strings.ToLower(lib), "transcriptapi") {
		t.Fatal("youtube skill body leaked into default library")
	}
}

func TestExtractProjectPath(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		handle string
		want   string
	}{
		{
			name:   "bold path at top",
			raw:    "# Forex Trading Agent\n\n## Tier: Primary owner of `/workspace/projects/forex-trading/`\n\nYou own this.",
			handle: "forex-trading",
			want:   "/workspace/projects/forex-trading",
		},
		{
			name: "yaml project wins",
			raw: `---
name: forex-trading
project: /workspace/projects/forex-trading
---
# Forex
Also mentions /workspace/projects/claude-gateway/memory
`,
			handle: "forex-trading",
			want:   "/workspace/projects/forex-trading",
		},
		{
			name: "yaml after html comment",
			raw: `<!-- agentsync:generated -->
---
project: /workspace/projects/grey-docs
---
# Grey docs
`,
			handle: "grey-docs",
			want:   "/workspace/projects/grey-docs",
		},
		{
			name:   "prefer handle when several paths",
			raw:    "See /workspace/projects/claude-gateway/ and `/workspace/projects/forex-trading/`.",
			handle: "forex-trading",
			want:   "/workspace/projects/forex-trading",
		},
		{
			name:   "skip claude-gateway noise",
			raw:    "Registry in /workspace/projects/claude-gateway/hooks. Own `/workspace/projects/grey-website/`.",
			handle: "grey-website",
			want:   "/workspace/projects/grey-website",
		},
		{
			name:   "reject traversal",
			raw:    "---\nproject: /workspace/projects/../etc\n---\n",
			handle: "x",
			want:   "",
		},
		{
			name:   "reject non-workspace",
			raw:    "---\nproject: /etc/passwd\n---\n",
			handle: "x",
			want:   "",
		},
		{
			name:   "empty",
			raw:    "You are a persona. No repo.",
			handle: "persona-elon",
			want:   "",
		},
		{
			name: "example yaml in another agent must not pin forex",
			raw: `# Agent Creator

Project-owning agents pin the repo:

` + "```yaml\n---\nname: forex-trading\nproject: /workspace/projects/forex-trading\n---\n```" + `

Do not investigate forex-trading unless asked.
`,
			handle: "agent-creator",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractProjectPath(tt.raw, tt.handle)
			if got != tt.want {
				t.Fatalf("ExtractProjectPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadChatAgentPinsForexProject(t *testing.T) {
	dir := "/workspace/.claude/agents"
	if _, err := os.Stat(dir); err != nil {
		t.Skip("agents dir not mounted")
	}
	m := NewManager(nil, nil)
	m.SetAgentDirs([]string{dir})
	m.scanChatAgents()
	ag, ok := m.GetChatAgent("forex-trading")
	if !ok {
		t.Fatal("expected forex-trading chat agent")
	}
	if ag.ProjectPath != "/workspace/projects/forex-trading" {
		t.Fatalf("ProjectPath = %q", ag.ProjectPath)
	}

	creator, ok := m.GetChatAgent("agent-creator")
	if !ok {
		t.Fatal("expected agent-creator")
	}
	if creator.ProjectPath != "" {
		t.Fatalf("agent-creator must not inherit a project path, got %q", creator.ProjectPath)
	}

	if ag.Framework != "" {
		t.Fatalf("forex-trading framework = %q, want empty so Qwen stays the gateway default", ag.Framework)
	}
	em, ok := m.GetChatAgent("grey-email")
	if !ok {
		t.Fatal("expected grey-email")
	}
	if em.Framework != "" {
		t.Fatalf("grey-email framework = %q, want empty so Qwen stays the gateway default", em.Framework)
	}
}

func TestExtractFramework(t *testing.T) {
	if got := ExtractFramework("---\nframework: deepseek\n---\n# x\n"); got != "deepseek" {
		t.Fatalf("yaml framework = %q", got)
	}
	if got := ExtractFramework("---\nframework: deepseek-flash\n---\n# x\n"); got != "deepseek-flash" {
		t.Fatalf("yaml flash = %q", got)
	}
	if got := NormalizeFramework("DeepSeek V4.1 Flash"); got != "deepseek-flash" {
		t.Fatalf("flash label = %q", got)
	}
	if got := ExtractFramework("---\nmodel: grok-4.5\n---\n"); got != "grok" {
		t.Fatalf("model alias = %q", got)
	}
	if got := NormalizeFramework("Qwen3.8 (local)"); got != "qwen" {
		t.Fatalf("qwen alias = %q", got)
	}
	if got := ExtractFramework("# no yaml\n"); got != "" {
		t.Fatalf("missing = %q", got)
	}
}

func TestIsChatAgentDir(t *testing.T) {
	if isChatAgentDir("coding") || isChatAgentDir("design") {
		t.Fatal("orchestrator buckets must not be chat agents")
	}
	if !isChatAgentDir("youtube") || !isChatAgentDir("grey-docs") {
		t.Fatal("youtube and grey-docs should be chat agents")
	}
	if !isSkillFilename("skill-youtube.md") || isSkillFilename("elon-musk.md") {
		t.Fatal("skill filename detection")
	}
}
