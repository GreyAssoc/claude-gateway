package ctxbuilder

import "testing"

// TestContainsAnyWord verifies the keyword matching helper.
// Fix #11/#20: both inputs are expected to be already lowercased by the caller.
func TestContainsAnyWord(t *testing.T) {
	tests := []struct {
		name    string
		message string
		content string
		want    bool
	}{
		{"two matching words", "i love golang programming today", "golang programming rocks", true},
		{"single matching word", "i love golang today", "golang is great", false},
		{"short words ignored", "the cat sat on mat", "the cat sat mat", false}, // all <4 chars
		{"no match", "hello world", "completely different sentence", false},
		{"empty message", "", "golang programming", false},
		{"empty content", "golang programming", "", false},
		{"both empty", "", "", false},
		{"exact boundary 4 chars", "this test word", "this test plan here", true}, // "this" and "test" are 4 chars
		{"3 char words excluded", "the big red fox", "the big red box", false}, // "the","big","red" are 3 chars
		{"already lowercase", "meeting about project deadline", "project deadline approaching", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsAnyWord(tt.message, tt.content)
			if got != tt.want {
				t.Errorf("containsAnyWord(%q, %q) = %v, want %v", tt.message, tt.content, got, tt.want)
			}
		})
	}
}
