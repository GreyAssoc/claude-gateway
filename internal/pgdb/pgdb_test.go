package pgdb

import (
	"context"
	"strings"
	"testing"
)

// TestVectorToString verifies pgvector literal formatting.
func TestVectorToString(t *testing.T) {
	tests := []struct {
		name  string
		input []float32
		want  string
	}{
		{"nil", nil, ""},
		{"empty", []float32{}, ""},
		{"single", []float32{0.5}, "[0.5]"},
		{"multiple", []float32{0.1, 0.2, 0.3}, "[0.1,0.2,0.3]"},
		{"negative", []float32{-0.5, 1.0}, "[-0.5,1]"},
		{"zero", []float32{0}, "[0]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vectorToString(tt.input)
			if got != tt.want {
				t.Errorf("vectorToString(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestDeactivateByContentWildcardRejection verifies that wildcard-only fragments are rejected.
// Only tests the validation guard — non-wildcard cases require a real DB.
func TestDeactivateByContentWildcardRejection(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
	}{
		{"single percent", "%"},
		{"single underscore", "_"},
		{"multiple percent", "%%"},
		{"mixed wildcards", "%_%"},
		{"underscores only", "___"},
	}

	d := &DB{} // nil inner db — only wildcard-rejection path runs
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := d.DeactivateByContent(context.Background(), tt.fragment)
			if err == nil {
				t.Errorf("expected error for wildcard-only fragment %q, got nil", tt.fragment)
			}
			if !strings.Contains(err.Error(), "non-wildcard") {
				t.Errorf("expected wildcard rejection error, got: %v", err)
			}
		})
	}
}

// TestNullString verifies nil-for-empty behavior.
func TestNullString(t *testing.T) {
	t.Run("empty returns nil", func(t *testing.T) {
		if got := nullString(""); got != nil {
			t.Errorf("nullString(\"\") = %v, want nil", got)
		}
	})
	t.Run("non-empty returns pointer", func(t *testing.T) {
		got := nullString("hello")
		if got == nil {
			t.Fatal("nullString(\"hello\") = nil, want non-nil")
		}
		if *got != "hello" {
			t.Errorf("*nullString(\"hello\") = %q, want %q", *got, "hello")
		}
	})
}

// TestPruneAutoFactsKeepMaxGuard verifies that PruneAutoFacts rejects zero and negative keepMax.
func TestPruneAutoFactsKeepMaxGuard(t *testing.T) {
	d := &DB{} // nil inner db — only guard path runs
	tests := []struct {
		name    string
		keepMax int
	}{
		{"zero", 0},
		{"negative", -1},
		{"very negative", -100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := d.PruneAutoFacts(context.Background(), tt.keepMax)
			if err == nil {
				t.Errorf("expected error for keepMax=%d, got nil", tt.keepMax)
			}
			if !strings.Contains(err.Error(), "must be positive") {
				t.Errorf("expected 'must be positive' error, got: %v", err)
			}
		})
	}
}

// TestRecentConversationsLimitGuard verifies that non-positive limits return nil.
func TestRecentConversationsLimitGuard(t *testing.T) {
	d := &DB{} // nil inner db — only guard path runs
	tests := []struct {
		name  string
		limit int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			convs, err := d.RecentConversations(context.Background(), "test-session", tt.limit)
			if err != nil {
				t.Errorf("expected nil error, got: %v", err)
			}
			if convs != nil {
				t.Errorf("expected nil result for limit=%d, got %d conversations", tt.limit, len(convs))
			}
		})
	}
}

// TestRecentConversationsAllSessionsLimitGuard verifies that non-positive limits return nil.
func TestRecentConversationsAllSessionsLimitGuard(t *testing.T) {
	d := &DB{}
	convs, err := d.RecentConversationsAllSessions(context.Background(), 0)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if convs != nil {
		t.Errorf("expected nil result for limit=0, got %d conversations", len(convs))
	}

	convs, err = d.RecentConversationsAllSessions(context.Background(), -5)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if convs != nil {
		t.Errorf("expected nil result for limit=-5, got %d conversations", len(convs))
	}
}

// TestSearchSimilarLimitGuard verifies that non-positive limits return nil.
func TestSearchSimilarLimitGuard(t *testing.T) {
	d := &DB{}
	emb := []float32{0.1, 0.2}
	hits, err := d.SearchSimilar(context.Background(), emb, nil, 0, 0.5)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if hits != nil {
		t.Errorf("expected nil result for limit=0, got %d hits", len(hits))
	}

	hits, err = d.SearchSimilar(context.Background(), emb, nil, -1, 0.5)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if hits != nil {
		t.Errorf("expected nil result for limit=-1, got %d hits", len(hits))
	}
}

// TestConversationsByDateValidation verifies date format validation.
func TestConversationsByDateValidation(t *testing.T) {
	d := &DB{} // nil inner db — only validation path runs
	tests := []struct {
		name string
		date string
	}{
		{"empty", ""},
		{"invalid format", "2026/02/25"},
		{"month-day-year", "02-25-2026"},
		{"just year", "2026"},
		{"invalid date", "2026-13-45"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := d.ConversationsByDate(context.Background(), tt.date)
			if err == nil {
				t.Errorf("expected error for date=%q, got nil", tt.date)
			}
			if !strings.Contains(err.Error(), "invalid date") {
				t.Errorf("expected 'invalid date' error, got: %v", err)
			}
		})
	}
}
