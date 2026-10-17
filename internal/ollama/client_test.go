package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestRunnerDied(t *testing.T) {
	if !runnerDied(errors.New(`ollama generate: status 500: {"error":"llama runner process has terminated: %!w(<nil>)"}`)) {
		t.Fatal("expected runner crash to be detected")
	}
	if runnerDied(errors.New("ollama generate: status 500: model not found")) {
		t.Fatal("unrelated 500 must not look like a runner crash")
	}
	if runnerDied(nil) {
		t.Fatal("nil error is not a runner crash")
	}
}

func TestGenerateRetriesOnCPUWhenRunnerDies(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode body: %v", err)
		}
		calls = append(calls, payload)
		if len(calls) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"llama runner process has terminated: %!w(<nil>)"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": "ok"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "embed", "qwen2.5:7b")
	got, err := c.Generate(context.Background(), "hi", "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "ok" {
		t.Fatalf("response = %q, want ok", got)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if _, ok := calls[0]["options"]; ok {
		t.Fatalf("first call should not force CPU options, got %#v", calls[0]["options"])
	}
	opts, ok := calls[1]["options"].(map[string]any)
	if !ok {
		t.Fatalf("second call options = %#v", calls[1]["options"])
	}
	numGPU, ok := opts["num_gpu"].(float64)
	if !ok || numGPU != 0 {
		t.Fatalf("second call num_gpu = %#v, want 0", opts["num_gpu"])
	}
}

func TestGenerateDoesNotRetryUnrelatedFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"model not found"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "embed", "qwen2.5:7b")
	_, err := c.Generate(context.Background(), "hi", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
