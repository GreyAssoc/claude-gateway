package bot

import (
	"testing"
)

func TestSelectableModels_DeepSeekAndGrok(t *testing.T) {
	want := map[string]string{
		"deepseek": "deepseek-v4-pro",
		"grok":     "grok-4.5",
		"qwen":     "qwen3.8-27b",
	}

	if len(selectableModels) != len(want) {
		t.Errorf("selectableModels has %d entries, want %d", len(selectableModels), len(want))
	}

	seen := make(map[string]string, len(selectableModels))
	for _, m := range selectableModels {
		if m.Label == "" {
			t.Error("model label must not be empty")
		}
		if m.Backend == "" {
			t.Errorf("model %q has empty backend", m.Label)
		}
		if m.Model == "" {
			t.Errorf("model %q has empty vendor model id", m.Label)
		}
		// Telegram inline button callback_data is capped at 64 bytes.
		cb := "model:" + m.Backend
		if len(cb) > 64 {
			t.Errorf("callback data %q is %d bytes, Telegram limit is 64", cb, len(cb))
		}
		if prev, dup := seen[m.Backend]; dup {
			t.Errorf("duplicate backend %q (%q and %q)", m.Backend, prev, m.Label)
		}
		seen[m.Backend] = m.Model
	}

	for backend, model := range want {
		got, ok := seen[backend]
		if !ok {
			t.Errorf("missing backend %q", backend)
			continue
		}
		if got != model {
			t.Errorf("backend %q model = %q, want %q", backend, got, model)
		}
	}
}

func TestConfiguredModelName(t *testing.T) {
	tests := []struct {
		framework string
		want      string
	}{
		{"deepseek", "deepseek-v4-pro"},
		{"grok", "grok-4.5"},
		{"qwen", "qwen3.8-27b"},
		{"ollama", "qwen3.8-27b"},
		{"vllm", "qwen3.8-27b"},
		{"opencode", "opencode-go/glm-5"},
		{"claude", "claude-sonnet-4-6"},
		{"", "claude-sonnet-4-6"},
	}
	for _, tt := range tests {
		got := configuredModelName(tt.framework, "claude-sonnet-4-6", "opencode-go/glm-5", "", "")
		if got != tt.want {
			t.Errorf("configuredModelName(%q) = %q, want %q", tt.framework, got, tt.want)
		}
	}

	got := configuredModelName("qwen", "claude-sonnet-4-6", "opencode-go/glm-5", "qwen3.8:27b-q4", "")
	if got != "qwen3.8:27b-q4" {
		t.Errorf("configuredModelName(qwen, ollama custom) = %q, want qwen3.8:27b-q4", got)
	}
	got = configuredModelName("qwen", "claude-sonnet-4-6", "opencode-go/glm-5", "ignored", "qwen3.8-27b")
	if got != "qwen3.8-27b" {
		t.Errorf("configuredModelName(qwen, vllm) = %q, want qwen3.8-27b", got)
	}
}

func TestIsNoiseProgress(t *testing.T) {
	noise := []string{
		"Session initialized",
		"[system:thinking_tokens]",
		`[claude-code:unrecognized_model] {"model":"deepseek-v4-pro"}`,
		"thinking_tokens",
	}
	for _, s := range noise {
		if !isNoiseProgress(s) {
			t.Errorf("isNoiseProgress(%q) = false, want true", s)
		}
	}
	if isNoiseProgress("DeepSeek V4 Pro is a reasoning model") {
		t.Error("real answer text was treated as noise")
	}
}
