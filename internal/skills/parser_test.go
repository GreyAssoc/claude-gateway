package skills

import (
	"os"
	"strings"
	"testing"
)

func TestParseSkillMD(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		sourcePath  string
		precedence  int
		wantName    string
		wantDesc    string
		wantCmd     string
		wantBody    string
		wantEnabled bool
		wantErr     bool
	}{
		{
			name: "full frontmatter",
			content: `---
name: my-skill
description: Does something useful
---
This is the body.`,
			sourcePath:  "/skills/SKILL-my-skill.md",
			precedence:  1,
			wantName:    "my-skill",
			wantDesc:    "Does something useful",
			wantBody:    "This is the body.",
			wantEnabled: true,
		},
		{
			name: "frontmatter with command",
			content: `---
name: notices
description: Generate party wall notices
command: notices
---
# Instructions here`,
			sourcePath:  "/skills/skill-notices.md",
			precedence:  0,
			wantName:    "notices",
			wantDesc:    "Generate party wall notices",
			wantCmd:     "notices",
			wantBody:    "# Instructions here",
			wantEnabled: true,
		},
		{
			name: "command with leading slash stripped",
			content: `---
name: soc
description: Schedule of Condition
command: /soc
---
Body.`,
			sourcePath:  "/skills/skill-soc.md",
			precedence:  0,
			wantName:    "soc",
			wantDesc:    "Schedule of Condition",
			wantCmd:     "soc",
			wantBody:    "Body.",
			wantEnabled: true,
		},
		{
			name:        "no frontmatter uses filename",
			content:     "Just a body with no frontmatter.",
			sourcePath:  "/skills/SKILL-helper.md",
			precedence:  0,
			wantName:    "helper",
			wantDesc:    "",
			wantBody:    "Just a body with no frontmatter.",
			wantEnabled: true,
		},
		{
			name: "empty name in frontmatter returns error",
			content: `---
name:
description: something
---
Body here.`,
			sourcePath: "/skills/SKILL-fallback.md",
			precedence: 0,
			wantErr:    true,
		},
		{
			name: "CDATA in content is preserved",
			content: `---
name: cdata-test
description: tests cdata
---
Content with ]]> CDATA end marker.`,
			sourcePath:  "/skills/SKILL-cdata.md",
			precedence:  0,
			wantName:    "cdata-test",
			wantDesc:    "tests cdata",
			wantBody:    "Content with ]]> CDATA end marker.",
			wantEnabled: true,
		},
		{
			name: "quoted name and description",
			content: `---
name: "quoted-name"
description: 'quoted desc'
---
Body.`,
			sourcePath:  "/skills/test.md",
			precedence:  0,
			wantName:    "quoted-name",
			wantDesc:    "quoted desc",
			wantBody:    "Body.",
			wantEnabled: true,
		},
		{
			name: "harness always flags",
			content: `---
name: google-workspace
description: Check Gmail inbox
always: true
harness: true
---
Use GmailSearch.`,
			sourcePath:  "/harness/skills/skill-google-workspace.md",
			precedence:  0,
			wantName:    "google-workspace",
			wantDesc:    "Check Gmail inbox",
			wantBody:    "Use GmailSearch.",
			wantEnabled: true,
		},
		{
			name:       "empty content with no name returns error",
			content:    "",
			sourcePath: "/skills/.md",
			precedence: 0,
			wantErr:    true,
		},
		{
			name: "unclosed frontmatter treated as body",
			content: `---
name: unclosed
This has no closing delimiter.`,
			sourcePath:  "/skills/SKILL-something.md",
			precedence:  0,
			wantName:    "something",
			wantDesc:    "",
			wantBody:    "---\nname: unclosed\nThis has no closing delimiter.",
			wantEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skill, err := ParseSkillMD(tt.content, tt.sourcePath, tt.precedence)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if skill.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", skill.Name, tt.wantName)
			}
			if skill.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", skill.Description, tt.wantDesc)
			}
			if skill.Command != tt.wantCmd {
				t.Errorf("Command = %q, want %q", skill.Command, tt.wantCmd)
			}
			if skill.Content != tt.wantBody {
				t.Errorf("Content = %q, want %q", skill.Content, tt.wantBody)
			}
			if skill.Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", skill.Enabled, tt.wantEnabled)
			}
			if skill.Precedence != tt.precedence {
				t.Errorf("Precedence = %d, want %d", skill.Precedence, tt.precedence)
			}
			if skill.ContentHash == "" {
				t.Error("ContentHash should not be empty")
			}
		})
	}
}

func TestScanAgentsAndMatchPurist(t *testing.T) {
	dir := "/workspace/.claude/agents"
	if _, err := os.Stat(dir); err != nil {
		t.Skip("workspace agents not mounted")
	}
	m := NewManager(nil, nil)
	m.SetAgentDirs([]string{dir})
	if err := m.scanAgentTree(dir); err != nil {
		t.Fatal(err)
	}
	m.scanChatAgents()
	agents := m.GetAgents()
	if len(agents) < 10 {
		t.Fatalf("expected many agents, got %d", len(agents))
	}
	got := m.BuildLibraryPrompt("review this with the go purist", 20000)
	if strings.Contains(got, "skill-youtube") || strings.Contains(strings.ToLower(got), "transcriptapi") {
		t.Fatal("youtube skill must not leak into the default library")
	}
	if _, ok := m.GetChatAgent("go-purist"); !ok {
		t.Fatal("expected go-purist folder chat agent")
	}
	for _, a := range agents {
		if strings.HasPrefix(a.Name, "gateway-system") {
			t.Fatalf("generated gateway-system file leaked: %s", a.Name)
		}
	}
}

func TestGoogleWorkspaceSkillFile(t *testing.T) {
	data, err := os.ReadFile("../../harness/skills/skill-google-workspace.md")
	if err != nil {
		t.Fatalf("harness skill missing: %v", err)
	}
	skill, err := ParseSkillMD(string(data), "skill-google-workspace.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if skill.Name != "google-workspace" || !skill.Always || !skill.Harness {
		t.Fatalf("name=%q always=%v harness=%v", skill.Name, skill.Always, skill.Harness)
	}
	if !strings.Contains(strings.ToLower(skill.Description), "gmail") {
		t.Fatalf("description should mention gmail: %q", skill.Description)
	}
	matched := MatchSkills([]Skill{*skill}, "check my emails")
	if len(matched) != 1 {
		t.Fatalf("expected 'check my emails' to match google-workspace, got %d", len(matched))
	}
}

func TestHarnessWebAndLocalContextSkillFiles(t *testing.T) {
	webRaw, err := os.ReadFile("../../harness/skills/skill-web.md")
	if err != nil {
		t.Fatal(err)
	}
	web, err := ParseSkillMD(string(webRaw), "skill-web.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if web.Name != "web" || !web.Always || !web.Harness {
		t.Fatalf("web skill flags: %#v", web)
	}
	if len(MatchSkills([]Skill{*web}, "search the web for the rust book")) != 1 {
		t.Fatal("expected 'search the web' to match web skill")
	}
	if len(MatchSkills([]Skill{*web}, "read this website")) != 1 {
		t.Fatal("expected website phrasing to match web skill")
	}

	locRaw, err := os.ReadFile("../../harness/skills/skill-local-context.md")
	if err != nil {
		t.Fatal(err)
	}
	loc, err := ParseSkillMD(string(locRaw), "skill-local-context.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if loc.Name != "local-context" || !loc.Always {
		t.Fatalf("local-context flags: %#v", loc)
	}
	if len(MatchSkills([]Skill{*loc}, "what client folder is redan")) != 1 {
		t.Fatal("expected client-folder phrasing to match local-context")
	}
}

func TestCatalogNameHit(t *testing.T) {
	if !catalogNameHit("use the go purist on this file", "go-purist") {
		t.Fatal("go purist should match go-purist")
	}
	if !catalogNameHit("draft as elon musk", "elon-musk") {
		t.Fatal("elon musk should match elon-musk")
	}
	if catalogNameHit("write some go code", "go-purist") {
		t.Fatal("generic 'go' must not match go-purist")
	}
}

func TestParseSkillMD_HarnessAlways(t *testing.T) {
	skill, err := ParseSkillMD(`---
name: google-workspace
description: Check Gmail inbox
always: true
harness: true
---
Use GmailSearch.`, "/harness/skills/skill-google-workspace.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !skill.Always || !skill.Harness {
		t.Fatalf("Always=%v Harness=%v, want both true", skill.Always, skill.Harness)
	}
}

func TestMatchSkillsPreferHarness(t *testing.T) {
	skills := []Skill{
		{Name: "emails", Description: "Check starred emails and draft", Enabled: true, Content: "use Task tool"},
		{Name: "google-workspace", Description: "Check Gmail inbox, read email threads, send mail, list calendar", Always: true, Harness: true, Enabled: true, Content: "use GmailSearch"},
	}
	got := MatchSkillsPreferHarness(skills, "check my emails")
	if len(got) != 1 || got[0].Name != "google-workspace" {
		t.Fatalf("prefer harness: got %#v", got)
	}
}

func TestBuildHarnessPromptSkipsAlwaysOn(t *testing.T) {
	m := NewManager(nil, nil)
	m.skills["google-workspace"] = &Skill{
		Name: "google-workspace", Description: "Check Gmail inbox, read email threads",
		Always: true, Harness: true, Enabled: true, Content: "use GmailSearch",
	}
	m.skills["emails"] = &Skill{
		Name: "emails", Description: "Check starred emails and draft",
		Enabled: true, Content: "use Task tool",
	}
	got := m.BuildHarnessPrompt("check my emails", 8000)
	if !strings.Contains(got, "google-workspace") {
		t.Fatalf("index should list google-workspace, got: %s", got)
	}
	if strings.Contains(got, "use GmailSearch") {
		t.Fatal("always-on body should be skipped (sidecar injects it)")
	}
}

func TestFormatSkillsXML_CDATAInjection(t *testing.T) {
	// Ensure ]]> in content is safely escaped in CDATA sections.
	skills := []Skill{
		{Name: "test", Content: "Hello ]]> world", Enabled: true},
	}
	xml := FormatSkillsXML(skills)
	if xml == "" {
		t.Fatal("Expected non-empty XML")
	}
	// The ]]> should be split into ]]]]><![CDATA[> to prevent CDATA breakout
	if !strings.Contains(xml, "]]]]><![CDATA[>") {
		t.Errorf("Expected CDATA escaping of ]]>, got: %s", xml)
	}
}

func TestFormatSkillsXML_Empty(t *testing.T) {
	xml := FormatSkillsXML(nil)
	if xml != "" {
		t.Errorf("Expected empty string for nil skills, got: %q", xml)
	}
}
