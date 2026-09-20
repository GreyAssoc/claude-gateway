package bot

import (
	"strings"
	"testing"

	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/skills"
)

func TestParseAgentCall(t *testing.T) {
	tests := []struct {
		in        string
		mentioned bool
		handle    string
		rest      string
	}{
		{"hello", false, "", ""},
		{"@SEOagent", true, "SEOagent", ""},
		{"@SEOagent optimise the site", true, "SEOagent", "optimise the site"},
		{"@SEOagent: do it", true, "SEOagent", "do it"},
		{"@YouTubeagent research X", true, "YouTubeagent", "research X"},
		{"@a", false, "", ""},
		{"email me@host.com", false, "", ""},
		{"  @GoPurist review this  ", true, "GoPurist", "review this"},
	}
	for _, tt := range tests {
		got := parseAgentCall(tt.in)
		if got.Mentioned != tt.mentioned || got.Handle != tt.handle || got.Rest != tt.rest {
			t.Errorf("parseAgentCall(%q) = %+v, want mentioned=%v handle=%q rest=%q",
				tt.in, got, tt.mentioned, tt.handle, tt.rest)
		}
	}
}

func TestNormalizeAndPrettyHandle(t *testing.T) {
	if got := normalizeHandle("SEO-agent"); got != "seoagent" {
		t.Errorf("normalizeHandle = %q", got)
	}
	if got := prettyHandle("seo-analyst-agent"); got != "SeoAnalystAgent" {
		t.Errorf("prettyHandle = %q", got)
	}
}

func TestIsMainAssistantHandle(t *testing.T) {
	for _, h := range []string{"default", "Default", "@home", "orchestrator", "main"} {
		if !isMainAssistantHandle(strings.TrimPrefix(h, "@")) {
			t.Errorf("%q should return to main assistant", h)
		}
	}
	if isMainAssistantHandle("youtube") {
		t.Error("youtube is not the main assistant")
	}
}

func TestSessionIDFor(t *testing.T) {
	if got := sessionIDFor(42, defaultAgentKey); got != "tg-42" {
		t.Errorf("default session = %q", got)
	}
	if got := sessionIDFor(42, "seoagent"); got != "tg-42-ag-seoagent" {
		t.Errorf("named session = %q", got)
	}
}

func TestFormatAgentReply(t *testing.T) {
	got := formatAgentReply("SEOagent", "Home titles updated.", false)
	if !strings.HasPrefix(got, "@SEOagent\n\n") {
		t.Errorf("connected reply = %q", got)
	}
	got = formatAgentReply("SEOagent", "TASK COMPLETE. Need you to pick a keyword?", true)
	if !strings.HasPrefix(got, "Notification from @SEOagent: Task complete, I need you to answer some questions.") {
		t.Errorf("notification = %q", got)
	}
}

func TestLooksLikeProgressPeek(t *testing.T) {
	yes := []string{"", "?", "hi", "progress", "any update?", "how's it going", "what are you doing", "catch me up"}
	for _, s := range yes {
		if !looksLikeProgressPeek(s) {
			t.Errorf("looksLikeProgressPeek(%q) = false, want true", s)
		}
	}
	no := []string{"fix the deploy script", "update the dashboard CSS", "status of the live orders on Pepperstone"}
	for _, s := range no {
		if looksLikeProgressPeek(s) {
			t.Errorf("looksLikeProgressPeek(%q) = true, want false", s)
		}
	}
}

func TestFrameworkForSlotOverride(t *testing.T) {
	b := &Bot{slotFW: map[string]string{}}
	turn := agentSlot{Key: "forextrading", Display: "ForexTrading", Framework: "qwen"}
	if got := b.frameworkFor(1, turn); got != "qwen" {
		t.Fatalf("dna pin = %q", got)
	}
	b.setSlotFramework(1, turn.Key, "deepseek")
	if got := b.frameworkFor(1, turn); got != "deepseek" {
		t.Fatalf("override = %q", got)
	}
	b.setSlotFramework(1, turn.Key, "deepseek-flash")
	if got := b.frameworkFor(1, turn); got != "deepseek-flash" {
		t.Fatalf("flash override = %q", got)
	}
	if providerOf(b.frameworkFor(1, turn)) != "deepseek" {
		t.Fatal("flash must still use the deepseek provider")
	}
	b.setSlotFramework(1, turn.Key, "")
	if got := b.frameworkFor(1, turn); got != "qwen" {
		t.Fatalf("inherit after clear = %q", got)
	}
	bare := agentSlot{Key: "default", Display: "default"}
	if got := b.frameworkFor(1, bare); got != "qwen" {
		t.Fatalf("gateway default = %q", got)
	}
}

func TestGPUQueueRankAndProgress(t *testing.T) {
	if gpuQueueRank("GPU: generating (@forextrading)") != 1 {
		t.Fatal("generating should rank 1")
	}
	if gpuQueueRank("GPU: starting (@greywebsite)") != 1 {
		t.Fatal("starting should rank with generating, not as queued")
	}
	if gpuQueueRank("GPU: queued 2 of 3 (@greyemail, behind @forextrading)") != 2 {
		t.Fatal("queued 2 should rank 2")
	}
	if gpuQueueRank("tools (not on GPU)") != 100 {
		t.Fatal("tools should rank last")
	}
	got := applyGPUProgress("", "model", "starting")
	if got != "GPU: starting" {
		t.Fatalf("bare starting = %q", got)
	}
	got = applyGPUProgress("", "model", "GPU: queued 2 of 3 (@greyemail, behind @forextrading)")
	if !strings.Contains(got, "queued 2 of 3") {
		t.Fatalf("applyGPUProgress queued = %q", got)
	}
	got = applyGPUProgress(got, "model", "first token")
	if got != "GPU: generating" {
		t.Fatalf("first token should flip queued to generating, got %q", got)
	}
	got = applyGPUProgress("GPU: starting (@greywebsite)", "model", "first token")
	if got != "GPU: generating" {
		t.Fatalf("first token should flip starting to generating, got %q", got)
	}
	got = applyGPUProgress("GPU: generating (@greywebsite)", "Bash", "go build")
	if got != "tools (not on GPU)" {
		t.Fatalf("bash should clear generating, got %q", got)
	}
	got = applyGPUProgress("GPU: starting (@greywebsite)", "Bash", "ls")
	if got != "tools (not on GPU)" {
		t.Fatalf("bash should clear starting, got %q", got)
	}
}

func TestFormatIdleStatusAndProgressAllNote(t *testing.T) {
	idle := formatIdleStatus("grey-website")
	if !strings.Contains(idle, "@grey-website is idle") {
		t.Fatalf("idle = %q", idle)
	}
	if !strings.Contains(idle, "A new message starts a task") {
		t.Fatalf("idle missing start hint: %q", idle)
	}
	note := progressAllNote("grey-website", false)
	if !strings.Contains(note, "@grey-website (this chat) is idle") {
		t.Fatalf("idle focus note = %q", note)
	}
	if !strings.Contains(note, "Only in-flight agents are listed") {
		t.Fatalf("omission note = %q", note)
	}
	working := progressAllNote("forex-trading", true)
	if strings.Contains(working, "is idle") {
		t.Fatalf("working focus should not say idle: %q", working)
	}
	if !strings.Contains(working, "Only in-flight agents are listed") {
		t.Fatalf("working still needs omission note: %q", working)
	}
}

func TestNamedAgentSystemPromptPinsProject(t *testing.T) {
	got := namedAgentSystemPrompt(agentSlot{
		Display:     "ForexTrading",
		DNAName:     "forex-trading",
		DNA:         "You own forex.",
		ProjectPath: "/workspace/projects/forex-trading",
	})
	if !strings.Contains(got, "WORKING DIRECTORY: /workspace/projects/forex-trading") {
		t.Fatalf("prompt should name cwd:\n%s", got)
	}
	if !strings.Contains(got, "same working style as @default") {
		t.Fatal("must work like default")
	}
	if !strings.Contains(got, "Never use Task") {
		t.Fatal("must forbid Task/subagents")
	}
	if !strings.Contains(got, "go env") {
		t.Fatal("must forbid toolchain archaeology")
	}
}

func TestAgentSlotWorkDir(t *testing.T) {
	s := agentSlot{ProjectPath: "/workspace/projects/forex-trading/"}
	if got := s.workDir("/workspace"); got != "/workspace/projects/forex-trading" {
		t.Fatalf("workDir = %q", got)
	}
	if got := (agentSlot{}).workDir("/workspace"); got != "/workspace" {
		t.Fatalf("default workDir = %q", got)
	}
}

func TestMatchAgentDNAPrefersFolder(t *testing.T) {
	cat := []agentMatch{
		{Name: "forex-trading", Key: "forextrading", DNA: "orphan GetAgents copy"},
		{Name: "forex-trading", Key: "forextrading", Dir: "/workspace/.claude/agents/forex-trading", DNA: "folder", Skills: []skills.Skill{{Name: "x"}}},
	}
	hit, amb := matchAgentDNA("forex-trading", cat)
	if hit == nil || hit.Dir == "" || amb != nil {
		t.Fatalf("folder agent should win, hit=%v amb=%v", hit, amb)
	}
}

func TestSwitchMessageIncludesModel(t *testing.T) {
	b := &Bot{slotFW: map[string]string{}, cfg: &config.Config{}}
	slot := agentSlot{
		Key: "forextrading", Display: "forex-trading", DNAName: "forex-trading",
		Framework: "qwen", Dir: "/workspace/.claude/agents/forex-trading",
	}
	got := b.switchMessage(1, slot)
	if !strings.Contains(got, "Now chatting with @forex-trading.") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "Model:") || !strings.Contains(got, "qwen") {
		t.Fatalf("missing model line:\n%s", got)
	}
	if strings.Contains(got, "No unique agent folder") {
		t.Fatal(got)
	}
}

func TestMatchAgentDNA(t *testing.T) {
	cat := []agentMatch{
		{Name: "seo-analyst-agent", Key: "seoanalystagent", DNA: "analyst"},
		{Name: "seo-content-agent", Key: "seocontentagent", DNA: "content"},
		{Name: "go-purist", Key: "gopurist", DNA: "purist"},
	}
	hit, amb := matchAgentDNA("GoPurist", cat)
	if hit == nil || hit.Name != "go-purist" || amb != nil {
		t.Fatalf("GoPurist hit=%v amb=%v", hit, amb)
	}
	hit, amb = matchAgentDNA("SEOagent", cat)
	if hit != nil || amb != nil {
		t.Fatalf("SEOagent should be an ad-hoc named thread when two SEO files exist, hit=%v amb=%v", hit, amb)
	}
	hit, amb = matchAgentDNA("seo-analyst-agent", cat)
	if hit == nil || hit.Name != "seo-analyst-agent" || amb != nil {
		t.Fatalf("full DNA name should bind, hit=%v amb=%v", hit, amb)
	}
	hit, amb = matchAgentDNA("YouTubeagent", cat)
	if hit != nil || amb != nil {
		t.Fatalf("YouTubeagent should be ad-hoc, hit=%v amb=%v", hit, amb)
	}
}
