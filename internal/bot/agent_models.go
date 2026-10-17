package bot

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// agentModelsPath is the on-disk copy of per-agent /models selections.
// The grey-website content pipeline reads the same file so a review or
// repair started from a shell uses the model selected for @grey-website.
// Processes the agent itself starts also receive AGENT_PROVIDER and
// AGENT_MODEL from the sidecar, which wins over this file.
const defaultAgentModelsPath = "/workspace/projects/claude-gateway/data/agent-models.json"

type agentModelSlot struct {
	Framework string `json:"framework"`
	Updated   string `json:"updated"`
}

type agentModelFile struct {
	Slots map[string]agentModelSlot `json:"slots"`
}

func agentModelsPath() string {
	if p := os.Getenv("AGENT_MODELS_FILE"); p != "" {
		return p
	}
	return defaultAgentModelsPath
}

func (b *Bot) loadAgentModels() {
	if b.agentModelsPath == "" {
		return
	}
	data, err := os.ReadFile(b.agentModelsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("agent models: read %s: %v", b.agentModelsPath, err)
		}
		return
	}
	var file agentModelFile
	if err := json.Unmarshal(data, &file); err != nil {
		log.Printf("agent models: parse %s: %v", b.agentModelsPath, err)
		return
	}
	b.slotFWMu.Lock()
	defer b.slotFWMu.Unlock()
	if b.slotFW == nil {
		b.slotFW = make(map[string]string)
	}
	for key, slot := range file.Slots {
		if slot.Framework != "" {
			b.slotFW[key] = slot.Framework
		}
	}
}

// writeAgentModels persists the current overrides. An empty framework is
// already absent from current. Timestamps stay put when the selection did
// not change, so the newest selection for an agent is the one a shell-started
// pipeline follows.
func (b *Bot) writeAgentModels(current map[string]string) {
	if b.agentModelsPath == "" {
		return
	}
	prev := agentModelFile{}
	if data, err := os.ReadFile(b.agentModelsPath); err == nil {
		_ = json.Unmarshal(data, &prev)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	out := agentModelFile{Slots: make(map[string]agentModelSlot, len(current))}
	for key, fw := range current {
		if fw == "" {
			continue
		}
		updated := now
		if prev.Slots != nil && prev.Slots[key].Framework == fw && prev.Slots[key].Updated != "" {
			updated = prev.Slots[key].Updated
		}
		out.Slots[key] = agentModelSlot{Framework: fw, Updated: updated}
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		log.Printf("agent models: encode: %v", err)
		return
	}
	body = append(body, '\n')
	dir := filepath.Dir(b.agentModelsPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("agent models: mkdir %s: %v", dir, err)
		return
	}
	tmp := b.agentModelsPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		log.Printf("agent models: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, b.agentModelsPath); err != nil {
		log.Printf("agent models: rename %s: %v", b.agentModelsPath, err)
	}
}
