package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoTeamsUseSplitDNA(t *testing.T) {
	dev, ok := GetTeam("go-dev-team")
	if !ok {
		t.Fatal("go-dev-team missing")
	}
	for _, ag := range dev.Agents {
		if !strings.HasPrefix(ag.DNAFile, "coding/go-dev-team/") {
			t.Errorf("%s DNAFile = %q, want coding/go-dev-team/...", ag.Name, ag.DNAFile)
		}
	}

	sec, ok := GetTeam("go-sec-team")
	if !ok {
		t.Fatal("go-sec-team missing")
	}
	for _, ag := range sec.Agents {
		if !strings.HasPrefix(ag.DNAFile, "coding/go-sec-team/") {
			t.Errorf("%s DNAFile = %q, want coding/go-sec-team/...", ag.Name, ag.DNAFile)
		}
	}
}

func TestReviewToolsAllowReportWrite(t *testing.T) {
	got := reviewToolsOnly()
	want := map[string]bool{"Read": true, "Glob": true, "Grep": true, "Write": true}
	if len(got) != len(want) {
		t.Fatalf("reviewToolsOnly() = %v, want Read/Glob/Grep/Write", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected tool %q", name)
		}
	}
	for name := range want {
		found := false
		for _, g := range got {
			if g == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestGoTeamDNAFilesExist(t *testing.T) {
	roots := []string{"/workspace/.claude/teams", "/app/teams"}
	var root string
	for _, r := range roots {
		if _, err := os.Stat(r); err == nil {
			root = r
			break
		}
	}
	if root == "" {
		t.Skip("teams DNA dir not mounted")
	}
	for _, teamName := range []string{"go-dev-team", "go-sec-team"} {
		team, _ := GetTeam(teamName)
		for _, ag := range team.Agents {
			p := filepath.Join(root, ag.DNAFile)
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s missing DNA file %s: %v", ag.Name, p, err)
			}
		}
	}
}
