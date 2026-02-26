package ollama

import "testing"

// TestNewClientEmptyBaseURLPanics verifies that NewClient panics with empty baseURL.
func TestNewClientEmptyBaseURLPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic with empty baseURL, got none")
		}
		msg, ok := r.(string)
		if !ok || msg != "ollama: empty baseURL" {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()
	NewClient("", "model", "llm")
}

// TestNewClientValidBaseURL verifies that NewClient succeeds with a valid baseURL.
func TestNewClientValidBaseURL(t *testing.T) {
	c := NewClient("http://localhost:11434", "embed-model", "llm-model")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.baseURL != "http://localhost:11434" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "http://localhost:11434")
	}
}

// TestNewClientTrimsTrailingSlash verifies trailing slash is trimmed.
func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://localhost:11434/", "embed-model", "llm-model")
	if c.baseURL != "http://localhost:11434" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "http://localhost:11434")
	}
}
