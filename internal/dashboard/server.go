// Package dashboard serves a web UI and JSON API for gateway monitoring.
package dashboard

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/db"
	"github.com/you/claude-gateway/internal/memory"
	"github.com/you/claude-gateway/internal/ratelimiter"
	"github.com/you/claude-gateway/internal/skills"
)

//go:embed static/*
var staticFiles embed.FS

// Server serves the web dashboard.
type Server struct {
	cfg       *config.Config
	db        *db.DB
	limiter   *ratelimiter.Limiter
	skills    *skills.Manager
	memory    *memory.Manager
	mux       *http.ServeMux
	srv       *http.Server
	startTime time.Time
	apiRL     apiRateLimiter
}

// apiRateLimiter implements a simple per-IP rate limiter for dashboard API endpoints.
type apiRateLimiter struct {
	mu      sync.Mutex
	clients map[string]*ipBucket
}

type ipBucket struct {
	tokens   int
	lastSeen time.Time
}

const (
	apiRateLimitPerMinute = 60
	apiRateLimitBurst     = 10
)

func (rl *apiRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.clients == nil {
		rl.clients = make(map[string]*ipBucket)
	}

	now := time.Now()
	b, ok := rl.clients[ip]
	if !ok {
		rl.clients[ip] = &ipBucket{tokens: apiRateLimitBurst - 1, lastSeen: now}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastSeen)
	refill := int(elapsed.Seconds() * float64(apiRateLimitPerMinute) / 60)
	b.tokens += refill
	if b.tokens > apiRateLimitBurst {
		b.tokens = apiRateLimitBurst
	}
	b.lastSeen = now

	if b.tokens <= 0 {
		return false
	}
	b.tokens--

	// Evict stale entries (idle > 5 minutes)
	if len(rl.clients) > 100 {
		for k, v := range rl.clients {
			if now.Sub(v.lastSeen) > 5*time.Minute {
				delete(rl.clients, k)
			}
		}
	}

	return true
}

// NewServer creates a dashboard server.
func NewServer(cfg *config.Config, database *db.DB, limiter *ratelimiter.Limiter, skillsMgr *skills.Manager, memMgr *memory.Manager) (*Server, error) {
	s := &Server{
		cfg:       cfg,
		db:        database,
		limiter:   limiter,
		skills:    skillsMgr,
		memory:    memMgr,
		mux:       http.NewServeMux(),
		startTime: time.Now(),
	}

	if err := s.registerRoutes(); err != nil {
		return nil, err
	}
	return s, nil
}

// Start begins serving the dashboard on the configured port.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.Dashboard.Port)

	// When no bearer token is configured, bind to localhost only to prevent
	// accidental exposure behind a reverse proxy where RemoteAddr is the proxy IP.
	if s.cfg.Dashboard.BearerToken == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", s.cfg.Dashboard.Port)
		log.Printf("Dashboard: no DASHBOARD_TOKEN configured — binding to %s only. "+
			"Set DASHBOARD_TOKEN to allow remote access.", addr)
	}
	log.Printf("Dashboard: starting on %s", addr)

	s.srv = &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Dashboard: error: %v", err)
		}
	}()

	return nil
}

// Shutdown gracefully stops the dashboard server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) registerRoutes() error {
	// Static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("dashboard: embedded static files missing: %w", err)
	}
	fileServer := http.FileServer(http.FS(staticFS))
	s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		fileServer.ServeHTTP(w, r)
	}))

	// API endpoints with auth and rate limiting
	api := func(h http.HandlerFunc) http.HandlerFunc {
		return s.withAPIRateLimit(s.withAuth(h))
	}
	s.mux.HandleFunc("/api/status", api(s.handleStatus))
	s.mux.HandleFunc("/api/rate-limits", api(s.handleRateLimits))
	s.mux.HandleFunc("/api/skills", api(s.handleSkills))
	s.mux.HandleFunc("POST /api/skills/{name}/toggle", api(s.handleSkillToggle))
	s.mux.HandleFunc("/api/memory", api(s.handleMemory))
	s.mux.HandleFunc("/api/memory/daily", api(s.handleMemoryDaily))
	s.mux.HandleFunc("/api/sessions", api(s.handleSessions))
	s.mux.HandleFunc("/api/agents", api(s.handleAgents))
	s.mux.HandleFunc("/api/orchestrator/runs", api(s.handleOrchestratorRuns))
	return nil
}

func (s *Server) withAPIRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !s.apiRL.allow(ip) {
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := s.cfg.Dashboard.BearerToken

		// If no token configured, only allow localhost.
		// WARNING: behind a reverse proxy, RemoteAddr is the proxy's IP.
		// Always configure a bearer token when exposed through a proxy.
		if token == "" {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			if host != "127.0.0.1" && host != "::1" {
				http.Error(w, "Dashboard: localhost only (configure DASHBOARD_TOKEN for remote access)", http.StatusForbidden)
				return
			}
			next(w, r)
			return
		}

		// Check bearer token with constant-time comparison
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + token
		if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}
