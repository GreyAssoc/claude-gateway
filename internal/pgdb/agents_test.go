package pgdb

import "testing"

func TestNormalizeHandle(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"grey-website", "greywebsite"},
		{"ForexTrading", "forextrading"},
		{"default", "default"},
		{"  Grey_Website  ", "greywebsite"},
	}
	for _, tt := range tests {
		if got := NormalizeHandle(tt.in); got != tt.want {
			t.Errorf("NormalizeHandle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAgentKeyFromSession(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "default"},
		{"tg-42", "default"},
		{"tg-42-ag-greywebsite", "greywebsite"},
		{"tg-42-ag-forextrading", "forextrading"},
		{"qwen:tg-42-ag-greywebsite", "greywebsite"},
		{"deepseek:tg-1-ag-forextrading", "forextrading"},
		{"cron-standup", "cron"},
		{"cron-morning_briefing", "cron"},
		{"qwen:cron-standup", "cron"},
		{"watcher-gmail", "watcher"},
		{"watcher-calendar", "watcher"},
		{"orch-gopurist", "orchestrator"},
		{"vllm:tg-9", "default"},
	}
	for _, tt := range tests {
		if got := AgentKeyFromSession(tt.in); got != tt.want {
			t.Errorf("AgentKeyFromSession(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
