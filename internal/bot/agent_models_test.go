package bot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentModelsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-models.json")
	seed := []byte(`{
  "slots": {
    "7:greywebsite": {"framework": "deepseek", "updated": "2020-01-01T00:00:00Z"},
    "7:greyemail": {"framework": "grok", "updated": "2020-01-02T00:00:00Z"}
  }
}
`)
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	b := &Bot{agentModelsPath: path, slotFW: map[string]string{}}
	b.loadAgentModels()
	if got := b.slotFrameworkOverride(7, "greywebsite"); got != "deepseek" {
		t.Fatalf("loaded grey-website = %q", got)
	}
	if got := b.slotFrameworkOverride(7, "greyemail"); got != "grok" {
		t.Fatalf("loaded grey-email = %q", got)
	}

	b.setSlotFramework(7, "greywebsite", "deepseek")
	file := readAgentModelFile(t, path)
	if file.Slots["7:greywebsite"].Updated != "2020-01-01T00:00:00Z" {
		t.Fatalf("unchanged selection moved timestamp to %q", file.Slots["7:greywebsite"].Updated)
	}

	b.setSlotFramework(7, "greywebsite", "grok")
	file = readAgentModelFile(t, path)
	if file.Slots["7:greywebsite"].Framework != "grok" {
		t.Fatalf("framework = %q", file.Slots["7:greywebsite"].Framework)
	}
	if file.Slots["7:greywebsite"].Updated == "2020-01-01T00:00:00Z" || file.Slots["7:greywebsite"].Updated == "" {
		t.Fatalf("changed selection kept timestamp %q", file.Slots["7:greywebsite"].Updated)
	}
	if file.Slots["7:greyemail"].Framework != "grok" {
		t.Fatal("writing grey-website dropped grey-email")
	}

	b.setSlotFramework(7, "greywebsite", "")
	next := &Bot{agentModelsPath: path, slotFW: map[string]string{}}
	next.loadAgentModels()
	if got := next.slotFrameworkOverride(7, "greywebsite"); got != "" {
		t.Fatalf("cleared selection reloaded as %q", got)
	}
	if got := next.slotFrameworkOverride(7, "greyemail"); got != "grok" {
		t.Fatalf("grey-email after clear = %q", got)
	}
}

func readAgentModelFile(t *testing.T, path string) agentModelFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file agentModelFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	return file
}
