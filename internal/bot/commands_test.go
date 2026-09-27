package bot

import (
	"context"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestParseCronAddTail(t *testing.T) {
	tests := []struct {
		in, agent, fw, prompt string
	}{
		{"Check my calendar", "", "", "Check my calendar"},
		{"@grey-website deepseek-flash Audit linking", "grey-website", "deepseek-flash", "Audit linking"},
		{"@forex-trading qwen Summarise uncommitted work", "forex-trading", "qwen", "Summarise uncommitted work"},
		{"flash Inbox pulse", "", "deepseek-flash", "Inbox pulse"},
		{"@grey-email Check unread mail", "grey-email", "", "Check unread mail"},
	}
	for _, tt := range tests {
		a, fw, p := parseCronAddTail(tt.in)
		if a != tt.agent || fw != tt.fw || p != tt.prompt {
			t.Errorf("parseCronAddTail(%q) = (%q,%q,%q) want (%q,%q,%q)", tt.in, a, fw, p, tt.agent, tt.fw, tt.prompt)
		}
	}
}

func TestSelectableModels_DeepSeekAndGrok(t *testing.T) {
	want := map[string]string{
		"deepseek-flash": "deepseek-flash",
		"deepseek":       "deepseek-v4-pro",
		"grok":           "grok-4.5",
		"qwen":           "qwen3.8-27b",
	}

	if len(selectableModels) != len(want) {
		t.Errorf("selectableModels has %d entries, want %d", len(selectableModels), len(want))
	}

	seen := make(map[string]string, len(selectableModels))
	for _, m := range selectableModels {
		if m.Label == "" {
			t.Error("model label must not be empty")
		}
		if m.Key == "" {
			t.Errorf("model %q has empty key", m.Label)
		}
		if m.Backend == "" {
			t.Errorf("model %q has empty backend", m.Label)
		}
		if m.Model == "" {
			t.Errorf("model %q has empty vendor model id", m.Label)
		}
		cb := "model:" + m.Key
		if len(cb) > 64 {
			t.Errorf("callback data %q is %d bytes, Telegram limit is 64", cb, len(cb))
		}
		if prev, dup := seen[m.Key]; dup {
			t.Errorf("duplicate key %q (%q and %q)", m.Key, prev, m.Label)
		}
		seen[m.Key] = m.Model
	}

	for key, model := range want {
		got, ok := seen[key]
		if !ok {
			t.Errorf("missing key %q", key)
			continue
		}
		if got != model {
			t.Errorf("key %q model = %q, want %q", key, got, model)
		}
	}

	flash, ok := lookupSelectable("flash")
	if !ok || flash.Model != "deepseek-flash" || flash.Backend != "deepseek" {
		t.Fatalf("lookupSelectable(flash) = %+v ok=%v", flash, ok)
	}
	if providerOf("deepseek-flash") != "deepseek" {
		t.Fatalf("providerOf(deepseek-flash) = %q", providerOf("deepseek-flash"))
	}
	if providerOf("deepseek") != "deepseek" {
		t.Fatalf("providerOf(deepseek) = %q", providerOf("deepseek"))
	}
}

func TestConfiguredModelName(t *testing.T) {
	tests := []struct {
		framework string
		want      string
	}{
		{"deepseek", "deepseek-v4-pro"},
		{"deepseek-flash", "deepseek-flash"},
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

func TestClassifyGatewayIntent(t *testing.T) {
	lookups := []struct {
		text string
		kind string
	}{
		{"journal", "journal"},
		{"daily journal", "journal"},
		{"today's journal", "journal"},
		{"todays journal", "journal"},
		{"show journal", "journal"},
		{"view journal", "journal"},
		{"journal entry", "journal"},
		{"show me today's journal", "journal"},
		{"please show me the daily journal", "journal"},
		{"Show journal?", "journal"},
		{"list people", "people"},
		{"show contacts", "people"},
		{"my contacts", "people"},
		{"who do i know", "people"},
		{"my profile", "profile"},
		{"show profile", "profile"},
		{"what do you know about me", "profile"},
		{"list memories", "memories"},
		{"what do you remember", "memories"},
		{"cron jobs", "cron"},
		{"list cron", "cron"},
		{"show cron", "cron"},
		{"gateway status", "status"},
		{"show status", "status"},
		{"health check", "status"},
	}
	for _, tt := range lookups {
		if got := classifyGatewayIntent(tt.text); got != tt.kind {
			t.Errorf("classifyGatewayIntent(%q) = %q, want %q", tt.text, got, tt.kind)
		}
	}

	notLookups := []string{
		"implement the daily journal table",
		"Next: Track 4.2 — daily journal.",
		"the journal entry format should be JSON",
		"add a cron job that runs every day",
		"list people in the people table and add a unique constraint",
		"show me my profile page in the frontend",
		"qwen",
		"switch to deepseek",
		"use grok",
		`yes, commit & push track 4.1. then do the following:
Commit and push Track 4.1 on its own.

───

Next: Track 4.2 — daily journal. User-scoped “what happened this week,” not session summary. /journal slash command stays 4.4.

Do

1. Table cog_daily_journal: user_id, date (date, not timestamptz), summary, decisions_json, actions_json, topics_json, projects_json, stats_json. UNIQUE (user_id, date). Inline migrate.
2. Store: Get/Upsert/ListLastN(user_id, n). No auto-extract from chat.`,
	}
	for _, text := range notLookups {
		if got := classifyGatewayIntent(text); got != "" {
			t.Errorf("classifyGatewayIntent(%q) = %q, want empty (must reach the model)", text, got)
		}
	}
}

func TestGatewayIntentMatchDoesNotInterceptWorkInstructions(t *testing.T) {
	// Regression: a Track 4.2 brief containing "daily journal" was hijacked
	// as /journal and the bot replied "No journal entry for <today>".
	b := &Bot{}
	ctx := context.Background()
	msg := &tgbotapi.Message{Text: `yes, commit & push track 4.1. then do the following:
Commit and push Track 4.1 on its own.

───

Next: Track 4.2 — daily journal. User-scoped “what happened this week,” not session summary. /journal slash command stays 4.4.`}
	if b.gatewayIntentMatch(ctx, msg) {
		t.Fatal("gatewayIntentMatch intercepted a work brief containing \"daily journal\"")
	}
}

func TestGatewayIntentMatchDoesNotSwitchModelsFromChat(t *testing.T) {
	// Regression: mentioning qwen / ollama / vllm / "local model" in ordinary
	// chat used to hijack the message and switch the backend. Switching is
	// /models only.
	b := &Bot{}
	ctx := context.Background()
	messages := []string{
		"qwen",
		"tell me about qwen",
		"use qwen for this",
		"switch to qwen",
		"switch to the local qwen model",
		"should I switch to qwen?",
		"don't use qwen",
		"compare qwen and claude",
		"local model",
		"vllm",
		"ollama",
		"enable qwen",
		"activate vllm",
		"which model is qwen using",
		"what model are you",
		"which framework",
		"use grok",
		"switch to claude",
		"switch to deepseek",
	}
	for _, text := range messages {
		msg := &tgbotapi.Message{Text: text}
		if b.gatewayIntentMatch(ctx, msg) {
			t.Errorf("gatewayIntentMatch(%q) = true, want false (NL must not switch models)", text)
		}
	}
}

func TestParseReviewTeamRequest(t *testing.T) {
	got := parseReviewTeamRequest("run the go-dev & go-sec agent teams")
	if len(got) != 2 || got[0] != "go-dev-team" || got[1] != "go-sec-team" {
		t.Fatalf("got %#v", got)
	}
	if parseReviewTeamRequest("write some go code") != nil {
		t.Fatal("generic go work must not launch teams")
	}
	if parseReviewTeamRequest("I ran the go-dev team myself") != nil {
		t.Fatal("past-tense report must not re-launch")
	}
}

func TestAppendStreamTextJoinsGrokTokens(t *testing.T) {
	var b strings.Builder
	for _, tok := range []string{"💭 The", " user", " wants", " a", " review."} {
		appendStreamText(&b, tok)
	}
	appendStreamText(&b, "\nReviewing the uncommitted Phase 5 work.")
	got := b.String()
	want := "💭 The user wants a review.\nReviewing the uncommitted Phase 5 work."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestIsNoiseProgress(t *testing.T) {
	noise := []string{
		"Session initialized",
		"[system:thinking_tokens]",
		`[claude-code:unrecognized_model] {"model":"deepseek-v4-pro"}`,
		"thinking_tokens",
		"💭 The go env command timed out",
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
