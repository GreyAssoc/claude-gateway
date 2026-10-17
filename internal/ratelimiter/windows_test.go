package ratelimiter

import "testing"

func TestContextWindow_PerFramework(t *testing.T) {
	cases := []struct {
		framework string
		model     string
		want      int
	}{
		{"deepseek", "deepseek-v4-pro", 128_000},
		{"deepseek", "", 128_000},
		{"deepseek-flash", "deepseek-flash", 1_000_000},
		{"deepseek-flash", "", 1_000_000},
		{"grok", "grok-4.5", 2_000_000},
		{"grok", "", 2_000_000},
		{"claude", "claude-opus-4-6", 200_000},
		{"claude", "claude-opus-4-7[1m]", 1_000_000},
		{"qwen", "qwen3.8-27b", 131_072},
		{"qwen", "", 131_072},
		{"", "no-such-model", defaultContextWindow},
	}
	for _, tc := range cases {
		if got := ContextWindow(tc.framework, tc.model); got != tc.want {
			t.Errorf("ContextWindow(%q, %q) = %d, want %d", tc.framework, tc.model, got, tc.want)
		}
	}
}

func TestUnlimited(t *testing.T) {
	if !Unlimited("qwen") || !Unlimited("Qwen3.8-27b") {
		t.Fatal("local qwen should be unlimited")
	}
	if Unlimited("deepseek") || Unlimited("deepseek-flash") || Unlimited("grok") || Unlimited("claude") {
		t.Fatal("paid frameworks are not unlimited")
	}
}
