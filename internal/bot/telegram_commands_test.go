package bot

import "testing"

func TestTelegramSafeCommand(t *testing.T) {
	tests := map[string]string{
		"GreyDocs":       "greydocs",
		"grey-docs":      "grey_docs",
		"go-purist":      "go_purist",
		"job-setup":      "job_setup",
		"beats-solution": "beats_solution",
		"youtube":        "youtube",
	}
	for in, want := range tests {
		if got := telegramSafeCommand(in); got != want {
			t.Errorf("telegramSafeCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildTelegramCommandListIncludesCore(t *testing.T) {
	got := buildTelegramCommandList(nil)
	if len(got) == 0 {
		t.Fatal("empty command list")
	}
	if len(got) > telegramMaxCommands {
		t.Fatalf("len=%d exceeds Telegram cap %d", len(got), telegramMaxCommands)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.Command] {
			t.Errorf("duplicate command %q", c.Command)
		}
		seen[c.Command] = true
		if telegramSafeCommand(c.Command) != c.Command {
			t.Errorf("unsafe command %q", c.Command)
		}
	}
	for _, need := range []string{"start", "agents", "who", "progress", "reviewgo", "googleauth"} {
		if !seen[need] {
			t.Errorf("missing %s", need)
		}
	}
}
