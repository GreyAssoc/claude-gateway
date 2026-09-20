package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterDiscoveredTeamsAddsNewFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "coding", "auto-test-team")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: AutoTester\ntier: 3\nweight: 10\n---\n# Auto tester\n"
	if err := os.WriteFile(filepath.Join(dir, "auto-tester.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	n := RegisterDiscoveredTeams([]string{root})
	if n < 1 {
		t.Fatalf("expected to register at least 1 agent, got %d", n)
	}
	team, ok := GetTeam("auto-test-team")
	if !ok {
		t.Fatal("auto-test-team not registered")
	}
	if len(team.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(team.Agents))
	}
	if team.Agents[0].Name != "AutoTester" {
		t.Errorf("name = %q, want AutoTester", team.Agents[0].Name)
	}
	if team.Agents[0].DNAFile != "coding/auto-test-team/auto-tester.md" {
		t.Errorf("DNAFile = %q", team.Agents[0].DNAFile)
	}
	if team.Agents[0].Tier != 3 || team.Agents[0].Weight != 10 {
		t.Errorf("tier/weight = %d/%d", team.Agents[0].Tier, team.Agents[0].Weight)
	}
}

func TestRegisterDiscoveredTeamsMergesIntoGoDev(t *testing.T) {
	teamsMu.Lock()
	orig := append([]Agent(nil), allTeams["go-dev-team"].Agents...)
	teamsMu.Unlock()
	t.Cleanup(func() {
		teamsMu.Lock()
		tcopy := allTeams["go-dev-team"]
		tcopy.Agents = orig
		allTeams["go-dev-team"] = tcopy
		teamsMu.Unlock()
	})

	root := t.TempDir()
	dir := filepath.Join(root, "coding", "go-dev-team")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go-extra.md"), []byte("# extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// existing files also present should not duplicate
	if err := os.WriteFile(filepath.Join(dir, "go-purist.md"), []byte("# purist\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	RegisterDiscoveredTeams([]string{root})
	team, ok := GetTeam("go-dev-team")
	if !ok {
		t.Fatal("go-dev-team missing")
	}
	var extra, purist int
	for _, a := range team.Agents {
		if a.DNAFile == "coding/go-dev-team/go-extra.md" {
			extra++
		}
		if a.DNAFile == "coding/go-dev-team/go-purist.md" {
			purist++
		}
	}
	if extra != 1 {
		t.Errorf("go-extra appearances = %d, want 1", extra)
	}
	if purist != 1 {
		t.Errorf("go-purist appearances = %d, want 1 (no duplicate)", purist)
	}
}
