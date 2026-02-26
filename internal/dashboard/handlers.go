package dashboard

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/you/claude-gateway/internal/orchestrator"
	"github.com/you/claude-gateway/internal/skills"
)

func (s *Server) writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Dashboard: JSON encode error: %v", err)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	type statusResponse struct {
		Status  string `json:"status"`
		Uptime  string `json:"uptime"`
		Version string `json:"version"`
	}

	s.writeJSON(w, statusResponse{
		Status:  "ok",
		Uptime:  time.Since(s.startTime).Round(time.Second).String(),
		Version: "v2",
	})
}

func (s *Server) handleRateLimits(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, map[string]any{"current": s.limiter.GetStatus()})
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		s.writeJSON(w, []any{})
		return
	}

	type skillResponse struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Enabled     bool   `json:"enabled"`
		Precedence  int    `json:"precedence"`
		SourcePath  string `json:"source_path"`
		ContentLen  int    `json:"content_length"`
	}

	all := s.skills.GetAllSkills()
	resp := make([]skillResponse, 0, len(all))
	for _, sk := range all {
		resp = append(resp, skillResponse{
			Name:        sk.Name,
			Description: sk.Description,
			Enabled:     sk.Enabled,
			Precedence:  sk.Precedence,
			SourcePath:  sk.SourcePath,
			ContentLen:  len(sk.Content),
		})
	}

	s.writeJSON(w, resp)
}

func (s *Server) handleSkillToggle(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		http.Error(w, "Skills not available", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	newEnabled, err := s.skills.ToggleSkill(r.Context(), name)
	if err != nil {
		if errors.Is(err, skills.ErrSkillNotFound) {
			http.Error(w, "Skill not found", http.StatusNotFound)
		} else {
			log.Printf("Dashboard: skill toggle error: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	s.writeJSON(w, map[string]any{"name": name, "enabled": newEnabled})
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		s.writeJSON(w, []any{})
		return
	}

	curated, err := s.memory.GetCuratedMemories(r.Context(), 100)
	if err != nil {
		log.Printf("Dashboard: memory query error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, curated)
}

func (s *Server) handleMemoryDaily(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		s.writeJSON(w, []any{})
		return
	}

	daysStr := r.URL.Query().Get("days")
	days := 7
	if daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 {
			days = d
			if days > 365 {
				days = 365
			}
		}
	}

	dailies, err := s.memory.GetRecentDailySummaries(r.Context(), days)
	if err != nil {
		log.Printf("Dashboard: daily memory query error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, dailies)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	type sessionRow struct {
		SessionID    string `json:"session_id"`
		Source       string `json:"source"`
		InputTokens  int   `json:"input_tokens"`
		OutputTokens int   `json:"output_tokens"`
		CreatedAt    string `json:"created_at"`
	}

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT session_id, source, input_tokens, output_tokens, created_at
		 FROM session_history ORDER BY created_at DESC LIMIT 50`,
	)
	if err != nil {
		log.Printf("Dashboard: session query error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var sessions []sessionRow
	var scanErrors int
	for rows.Next() {
		var sr sessionRow
		if err := rows.Scan(&sr.SessionID, &sr.Source, &sr.InputTokens, &sr.OutputTokens, &sr.CreatedAt); err != nil {
			scanErrors++
			continue
		}
		sessions = append(sessions, sr)
	}
	if scanErrors > 0 {
		log.Printf("Dashboard: skipped %d session rows due to scan errors", scanErrors)
	}
	if err := rows.Err(); err != nil {
		log.Printf("Dashboard: rows iteration error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, sessions)
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	type agentResponse struct {
		Name    string `json:"name"`
		Team    string `json:"team"`
		Tier    int    `json:"tier"`
		Weight  int    `json:"weight"`
		HasVeto bool   `json:"has_veto"`
		DNAFile string `json:"dna_file"`
	}

	var agents []agentResponse
	for _, teamName := range orchestrator.TeamNames() {
		team, _ := orchestrator.GetTeam(teamName)
		for _, ag := range team.Agents {
			agents = append(agents, agentResponse{
				Name:    ag.Name,
				Team:    teamName,
				Tier:    ag.Tier,
				Weight:  ag.Weight,
				HasVeto: ag.HasVetoPower,
				DNAFile: ag.DNAFile,
			})
		}
	}

	s.writeJSON(w, agents)
}

func (s *Server) handleOrchestratorRuns(w http.ResponseWriter, r *http.Request) {
	type runResponse struct {
		ID         int64  `json:"id"`
		Team       string `json:"team"`
		Task       string `json:"task"`
		Outcome    string `json:"outcome"`
		DurationMs int64  `json:"duration_ms"`
		CreatedAt  string `json:"created_at"`
	}

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, team, task, outcome, duration_ms, created_at
		 FROM orchestrator_runs ORDER BY created_at DESC LIMIT 20`,
	)
	if err != nil {
		log.Printf("Dashboard: orchestrator runs query error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var runs []runResponse
	var scanErrors int
	for rows.Next() {
		var rr runResponse
		if err := rows.Scan(&rr.ID, &rr.Team, &rr.Task, &rr.Outcome, &rr.DurationMs, &rr.CreatedAt); err != nil {
			scanErrors++
			continue
		}
		runs = append(runs, rr)
	}
	if scanErrors > 0 {
		log.Printf("Dashboard: skipped %d orchestrator run rows due to scan errors", scanErrors)
	}
	if err := rows.Err(); err != nil {
		log.Printf("Dashboard: rows iteration error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, runs)
}

