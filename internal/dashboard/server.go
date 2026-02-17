package dashboard

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"

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
	cfg     *config.Config
	db      *db.DB
	limiter *ratelimiter.Limiter
	skills  *skills.Manager
	memory  *memory.Manager
	mux     *http.ServeMux
}

// NewServer creates a dashboard server.
func NewServer(cfg *config.Config, database *db.DB, limiter *ratelimiter.Limiter, skillsMgr *skills.Manager, memMgr *memory.Manager) *Server {
	s := &Server{
		cfg:     cfg,
		db:      database,
		limiter: limiter,
		skills:  skillsMgr,
		memory:  memMgr,
		mux:     http.NewServeMux(),
	}

	s.registerRoutes()
	return s
}

// Start begins serving the dashboard on the configured port.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.Dashboard.Port)
	log.Printf("Dashboard: starting on %s", addr)

	srv := &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Dashboard: error: %v", err)
		}
	}()

	return nil
}

func (s *Server) registerRoutes() {
	// Static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Printf("Dashboard: error embedding static files: %v", err)
	}
	s.mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// API endpoints with auth
	s.mux.HandleFunc("/api/status", s.withAuth(s.handleStatus))
	s.mux.HandleFunc("/api/rate-limits", s.withAuth(s.handleRateLimits))
	s.mux.HandleFunc("/api/skills", s.withAuth(s.handleSkills))
	s.mux.HandleFunc("/api/skills/", s.withAuth(s.handleSkillToggle))
	s.mux.HandleFunc("/api/memory", s.withAuth(s.handleMemory))
	s.mux.HandleFunc("/api/memory/daily", s.withAuth(s.handleMemoryDaily))
	s.mux.HandleFunc("/api/sessions", s.withAuth(s.handleSessions))
	s.mux.HandleFunc("/api/agents", s.withAuth(s.handleAgents))
	s.mux.HandleFunc("/api/orchestrator/runs", s.withAuth(s.handleOrchestratorRuns))
	s.mux.HandleFunc("/api/cron", s.withAuth(s.handleCron))
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := s.cfg.Dashboard.BearerToken

		// If no token configured, only allow localhost
		if token == "" {
			host := r.RemoteAddr
			if !strings.HasPrefix(host, "127.0.0.1") && !strings.HasPrefix(host, "[::1]") && !strings.HasPrefix(host, "localhost") {
				http.Error(w, "Dashboard: localhost only (no bearer token configured)", http.StatusForbidden)
				return
			}
			next(w, r)
			return
		}

		// Check bearer token
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}
