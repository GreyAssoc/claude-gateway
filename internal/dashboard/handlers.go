package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/you/claude-gateway/internal/orchestrator"
)

func (s *Server) writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	type statusResponse struct {
		Status  string `json:"status"`
		Uptime  string `json:"uptime"`
		Version string `json:"version"`
	}

	s.writeJSON(w, statusResponse{
		Status:  "ok",
		Uptime:  time.Since(startTime).Round(time.Second).String(),
		Version: "v2",
	})
}

var startTime = time.Now()

func (s *Server) handleRateLimits(w http.ResponseWriter, r *http.Request) {
	status := s.limiter.GetStatus()

	type rateLimitResponse struct {
		Current struct {
			RPM          string  `json:"rpm"`
			ITPM         string  `json:"itpm"`
			OTPM         string  `json:"otpm"`
			Headroom     float64 `json:"headroom"`
			QueueDepth   int     `json:"queue_depth"`
			WindowResets string  `json:"window_resets"`
		} `json:"current"`
	}

	resp := rateLimitResponse{}
	resp.Current.RPM = status.RPM
	resp.Current.ITPM = status.ITPM
	resp.Current.OTPM = status.OTPM
	resp.Current.Headroom = status.Headroom
	resp.Current.QueueDepth = status.QueueDepth
	resp.Current.WindowResets = status.WindowResets

	s.writeJSON(w, resp)
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		s.writeJSON(w, []interface{}{})
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
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.skills == nil {
		http.Error(w, "Skills not available", http.StatusServiceUnavailable)
		return
	}

	// Extract skill name from path: /api/skills/{name}/toggle
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/skills/"), "/")
	if len(parts) < 2 || parts[1] != "toggle" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	name := parts[0]

	sk, ok := s.skills.GetSkill(name)
	if !ok {
		http.Error(w, "Skill not found", http.StatusNotFound)
		return
	}

	var err error
	if sk.Enabled {
		err = s.skills.DisableSkill(name)
	} else {
		err = s.skills.EnableSkill(name)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, map[string]interface{}{"name": name, "enabled": !sk.Enabled})
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		s.writeJSON(w, []interface{}{})
		return
	}

	curated, err := s.memory.GetCuratedMemories(100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, curated)
}

func (s *Server) handleMemoryDaily(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		s.writeJSON(w, []interface{}{})
		return
	}

	daysStr := r.URL.Query().Get("days")
	days := 7
	if daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 {
			days = d
		}
	}

	dailies, err := s.memory.GetRecentDailySummaries(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	rows, err := s.db.Query(
		`SELECT session_id, source, input_tokens, output_tokens, created_at
		 FROM session_history ORDER BY created_at DESC LIMIT 50`,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var sessions []sessionRow
	for rows.Next() {
		var sr sessionRow
		if err := rows.Scan(&sr.SessionID, &sr.Source, &sr.InputTokens, &sr.OutputTokens, &sr.CreatedAt); err != nil {
			continue
		}
		sessions = append(sessions, sr)
	}

	s.writeJSON(w, sessions)
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	type agentResponse struct {
		Name     string `json:"name"`
		Team     string `json:"team"`
		Tier     int    `json:"tier"`
		Weight   int    `json:"weight"`
		HasVeto  bool   `json:"has_veto"`
		DNAFile  string `json:"dna_file"`
	}

	var agents []agentResponse
	for teamName, team := range orchestrator.AllTeams {
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

	rows, err := s.db.Query(
		`SELECT id, team, task, outcome, duration_ms, created_at
		 FROM orchestrator_runs ORDER BY created_at DESC LIMIT 20`,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var runs []runResponse
	for rows.Next() {
		var r runResponse
		if err := rows.Scan(&r.ID, &r.Team, &r.Task, &r.Outcome, &r.DurationMs, &r.CreatedAt); err != nil {
			continue
		}
		runs = append(runs, r)
	}

	s.writeJSON(w, runs)
}

func (s *Server) handleCron(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, map[string]string{"status": "see /cron telegram command"})
}
