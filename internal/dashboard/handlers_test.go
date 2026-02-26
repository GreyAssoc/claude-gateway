package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/ratelimiter"
)

func newTestServer() *Server {
	cfg := &config.Config{
		Dashboard: config.Dashboard{Port: 0},
	}
	limiter := ratelimiter.New(50, 40000, 8000, 0.85)
	return &Server{
		cfg:       cfg,
		limiter:   limiter,
		mux:       http.NewServeMux(),
		startTime: time.Now(),
	}
}

func TestHandleStatus(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()

	s.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
	if resp["version"] != "v2" {
		t.Errorf("version = %q, want %q", resp["version"], "v2")
	}
	if resp["uptime"] == "" {
		t.Error("uptime should not be empty")
	}
}

func TestHandleRateLimits(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest("GET", "/api/rate-limits", nil)
	w := httptest.NewRecorder()

	s.handleRateLimits(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}
	if _, ok := resp["current"]; !ok {
		t.Error("response should contain 'current' key")
	}
}

func TestHandleSkills_NilManager(t *testing.T) {
	s := newTestServer()
	s.skills = nil

	req := httptest.NewRequest("GET", "/api/skills", nil)
	w := httptest.NewRecorder()

	s.handleSkills(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d elements", len(resp))
	}
}

func TestHandleMemory_NilManager(t *testing.T) {
	s := newTestServer()
	s.memory = nil

	req := httptest.NewRequest("GET", "/api/memory", nil)
	w := httptest.NewRecorder()

	s.handleMemory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d elements", len(resp))
	}
}

func TestHandleMemoryDaily_NilManager(t *testing.T) {
	s := newTestServer()
	s.memory = nil

	req := httptest.NewRequest("GET", "/api/memory/daily", nil)
	w := httptest.NewRecorder()

	s.handleMemoryDaily(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp []any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d elements", len(resp))
	}
}

func TestHandleAgents(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest("GET", "/api/agents", nil)
	w := httptest.NewRecorder()

	s.handleAgents(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}
	// Should return agents from the orchestrator teams registry
	if len(resp) == 0 {
		t.Error("expected at least one agent")
	}
	// Verify each agent has required fields
	for _, ag := range resp {
		if _, ok := ag["name"]; !ok {
			t.Error("agent missing 'name' field")
		}
		if _, ok := ag["team"]; !ok {
			t.Error("agent missing 'team' field")
		}
		if _, ok := ag["tier"]; !ok {
			t.Error("agent missing 'tier' field")
		}
	}
}

func TestWithAuth_NoToken_Localhost(t *testing.T) {
	s := newTestServer()
	s.cfg.Dashboard.BearerToken = ""

	handler := s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("localhost with no token: status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestWithAuth_NoToken_Remote(t *testing.T) {
	s := newTestServer()
	s.cfg.Dashboard.BearerToken = ""

	handler := s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("remote with no token: status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestWithAuth_ValidToken(t *testing.T) {
	s := newTestServer()
	s.cfg.Dashboard.BearerToken = "test-secret-token"

	handler := s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer test-secret-token")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("valid token: status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestWithAuth_InvalidToken(t *testing.T) {
	s := newTestServer()
	s.cfg.Dashboard.BearerToken = "test-secret-token"

	handler := s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid token: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAPIRateLimiter_AllowsBurst(t *testing.T) {
	var rl apiRateLimiter

	// Should allow up to burst limit
	for i := 0; i < apiRateLimitBurst; i++ {
		if !rl.allow("127.0.0.1") {
			t.Errorf("request %d should be allowed within burst", i+1)
		}
	}
}

func TestAPIRateLimiter_BlocksExcess(t *testing.T) {
	var rl apiRateLimiter

	// Exhaust burst
	for i := 0; i < apiRateLimitBurst; i++ {
		rl.allow("127.0.0.1")
	}

	// Next request should be blocked
	if rl.allow("127.0.0.1") {
		t.Error("request beyond burst should be blocked")
	}
}

func TestAPIRateLimiter_PerIP(t *testing.T) {
	var rl apiRateLimiter

	// Exhaust burst for one IP
	for i := 0; i < apiRateLimitBurst; i++ {
		rl.allow("10.0.0.1")
	}

	// Different IP should still be allowed
	if !rl.allow("10.0.0.2") {
		t.Error("different IP should have its own bucket")
	}
}

func TestWithAPIRateLimit(t *testing.T) {
	s := newTestServer()

	handler := s.withAPIRateLimit(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Should pass within burst limit
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("first request: status = %d, want %d", w.Code, http.StatusOK)
	}

	// Exhaust remaining burst
	for i := 0; i < apiRateLimitBurst; i++ {
		w = httptest.NewRecorder()
		handler(w, req)
	}

	// Should now be rate limited
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("after burst: status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
}
