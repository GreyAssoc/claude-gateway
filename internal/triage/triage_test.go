package triage

import (
	"context"
	"testing"
)

// TestFormatAnalysis verifies the logging format helper.
func TestFormatAnalysis(t *testing.T) {
	tests := []struct {
		name string
		a    *Analysis
		want string
	}{
		{"nil", nil, "nil"},
		{"local", &Analysis{
			CanHandleLocally: true,
			LocalResponse:    "Hi!",
			PeopleNames:      []string{"Alice"},
			Topics:           []string{"greeting"},
		}, "LOCAL (people=[Alice], topics=[greeting])"},
		{"claude", &Analysis{
			CanHandleLocally: false,
			PeopleNames:      []string{"Bob"},
			Topics:           []string{"code", "review"},
		}, "CLAUDE (people=[Bob], topics=[code review])"},
		{"empty slices", &Analysis{
			CanHandleLocally: false,
		}, "CLAUDE (people=[], topics=[])"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatAnalysis(tt.a)
			if got != tt.want {
				t.Errorf("FormatAnalysis() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTriagerIsAvailable verifies nil-safety of the availability check.
func TestTriagerIsAvailable(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		tr := NewTriager(nil)
		if tr.IsAvailable() {
			t.Error("expected IsAvailable() = false with nil LLM")
		}
	})
}

// TestAnalyzeNilLLM verifies that Analyze returns safe fallback when LLM is nil.
// "hello" hits the hardcoded greeting map, so it IS locally handleable.
// Non-greeting messages with nil LLM fall through to the safe fallback.
func TestAnalyzeNilLLM(t *testing.T) {
	tr := NewTriager(nil)

	// Greeting: handled locally via hardcoded map (no LLM needed)
	a, err := tr.Analyze(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !a.CanHandleLocally {
		t.Error("expected CanHandleLocally = true for hardcoded greeting 'hello'")
	}

	// Non-greeting with nil LLM: safe fallback to Claude
	a, err = tr.Analyze(context.Background(), "review the auth code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.CanHandleLocally {
		t.Error("expected CanHandleLocally = false for non-greeting with nil LLM")
	}

	// Affirmative: must NEVER be handled locally, even though it's short
	a, err = tr.Analyze(context.Background(), "yes pls")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.CanHandleLocally {
		t.Error("expected CanHandleLocally = false for affirmative 'yes pls'")
	}
}
