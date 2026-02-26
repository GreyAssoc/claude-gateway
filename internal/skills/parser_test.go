package skills

import (
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

