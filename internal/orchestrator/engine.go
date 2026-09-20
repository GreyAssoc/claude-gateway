// Package orchestrator runs multi-agent code review teams with consensus-based verdicts.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/db"
	"github.com/you/claude-gateway/internal/format"
)

// Engine manages orchestrator team runs.
type Engine struct {
	db           *db.DB
	agentClient  agent.Backend
	agentDNADirs []string
	workspaceDir string
	timeout      time.Duration
}

// RunResult holds the outcome of a team run.
type RunResult struct {
	Team      string
	Task      string
	Consensus Consensus
	Verdicts  []Verdict
	Duration  time.Duration
	RunID     int64
}

// NewEngine creates an orchestrator engine.
func NewEngine(database *db.DB, agentClient agent.Backend, agentDNADirs []string, workspaceDir string, timeoutSeconds int) *Engine {
	RegisterDiscoveredTeams(agentDNADirs)
	log.Printf("Orchestrator: %d teams available: %s", len(TeamNames()), strings.Join(TeamNames(), ", "))
	return &Engine{
		db:           database,
		agentClient:  agentClient,
		agentDNADirs: agentDNADirs,
		workspaceDir: workspaceDir,
		timeout:      time.Duration(timeoutSeconds) * time.Second,
	}
}

// RunTeam executes all agents in a team sequentially and returns the consensus.
func (e *Engine) RunTeam(ctx context.Context, teamName, task string) (*RunResult, error) {
	return e.RunTeamStreaming(ctx, teamName, task, nil)
}

// RunTeamStreaming executes all agents with streaming output via callback.
func (e *Engine) RunTeamStreaming(ctx context.Context, teamName, task string, onProgress func(msg string)) (*RunResult, error) {
	team, ok := GetTeam(teamName)
	if !ok {
		return nil, fmt.Errorf("unknown team: %s", teamName)
	}

	start := time.Now()
	if onProgress != nil {
		onProgress(fmt.Sprintf("Starting %s (%d agents)...", teamName, len(team.Agents)))
	}

	runID, err := e.createRun(ctx, teamName, task)
	if err != nil {
		log.Printf("Orchestrator: warning creating run record: %v", err)
	}

	var verdicts []Verdict
	for i, ag := range team.Agents {
		if onProgress != nil {
			onProgress(fmt.Sprintf("[%d/%d] Running %s...", i+1, len(team.Agents), ag.Name))
		}

		verdict := RunAgent(ctx, e.agentClient, ag, task, e.agentDNADirs, e.workspaceDir, e.timeout)
		LogVerdict(verdict)
		verdicts = append(verdicts, verdict)

		if runID > 0 {
			e.saveVerdict(ctx, runID, verdict)
		}

		// Report individual result
		if onProgress != nil {
			onProgress(fmt.Sprintf("[%s] %s — %d issues", verdict.Icon(), ag.Name, len(verdict.Issues)))
		}

		if ctx.Err() != nil {
			break
		}
	}

	consensus := ApplyConsensus(verdicts)
	duration := time.Since(start)

	if runID > 0 {
		e.completeRun(ctx, runID, consensus, verdicts, duration)
	}

	if onProgress != nil {
		onProgress(fmt.Sprintf("Complete: %s (%v)", consensus.Summary, duration.Round(time.Second)))
	}

	return &RunResult{
		Team:      teamName,
		Task:      task,
		Consensus: consensus,
		Verdicts:  verdicts,
		Duration:  duration,
		RunID:     runID,
	}, nil
}

// RunGauntlet runs a full validation pipeline: dev team → security team.
func (e *Engine) RunGauntlet(ctx context.Context, task, language string, onProgress func(msg string)) ([]*RunResult, error) {
	var teams []string
	switch language {
	case "go":
		teams = []string{"go-dev-team", "go-sec-team"}
	case "fe", "frontend":
		teams = []string{"fe-dev-team", "fe-sec-team"}
	default:
		// Full gauntlet
		teams = []string{"go-dev-team", "go-sec-team", "fe-dev-team", "fe-sec-team", "integration"}
	}

	var results []*RunResult
	for _, teamName := range teams {
		result, err := e.RunTeamStreaming(ctx, teamName, task, onProgress)
		if err != nil {
			return results, err
		}
		results = append(results, result)

		// If blocked, stop the pipeline
		if result.Consensus.Blocked {
			if onProgress != nil {
				onProgress(fmt.Sprintf("Pipeline halted: blocked by %s", strings.Join(result.Consensus.BlockedBy, ", ")))
			}
			break
		}
	}

	return results, nil
}

// ListRecentRuns returns the most recent orchestrator runs.
func (e *Engine) ListRecentRuns(ctx context.Context, limit int) ([]RunSummary, error) {
	rows, err := e.db.QueryContext(ctx,
		`SELECT id, team, task, outcome, duration_ms, created_at
		 FROM orchestrator_runs ORDER BY created_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []RunSummary
	for rows.Next() {
		var r RunSummary
		if err := rows.Scan(&r.ID, &r.Team, &r.Task, &r.Outcome, &r.DurationMs, &r.CreatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// RunSummary is a compact view of a past run.
type RunSummary struct {
	ID         int64
	Team       string
	Task       string
	Outcome    string
	DurationMs int64
	CreatedAt  string
}

// ListAgentsText returns a formatted list of all agents for Telegram.
func ListAgentsText() string {
	var sb strings.Builder
	sb.WriteString("*Orchestrator Agents*\n\n")

	for _, teamName := range TeamNames() {
		team, _ := GetTeam(teamName)
		sb.WriteString(fmt.Sprintf("*%s*\n", teamName))
		for _, ag := range team.Agents {
			veto := ""
			if ag.HasVetoPower {
				veto = " [VETO]"
			}
			sb.WriteString(fmt.Sprintf("  T%d %s%s\n", ag.Tier, ag.Name, veto))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// RecentRunsText returns a formatted list of recent runs for Telegram.
func (e *Engine) RecentRunsText(ctx context.Context, limit int) string {
	runs, err := e.ListRecentRuns(ctx, limit)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if len(runs) == 0 {
		return "No orchestrator runs yet."
	}

	var sb strings.Builder
	sb.WriteString("*Recent Orchestrator Runs*\n\n")
	for _, r := range runs {
		task := format.Truncate(r.Task, 60)
		sb.WriteString(fmt.Sprintf("#%d [%s] %s — %s (%dms)\n", r.ID, r.Outcome, r.Team, task, r.DurationMs))
	}
	return sb.String()
}

func (e *Engine) createRun(ctx context.Context, team, task string) (int64, error) {
	result, err := e.db.ExecContext(ctx,
		`INSERT INTO orchestrator_runs (team, task, outcome, created_at)
		 VALUES (?, ?, 'running', datetime('now'))`,
		team, task,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (e *Engine) saveVerdict(ctx context.Context, runID int64, v Verdict) {
	issuesJSON, err := json.Marshal(v.Issues)
	if err != nil {
		log.Printf("Orchestrator: error marshaling issues: %v", err)
		issuesJSON = []byte("[]")
	}
	if _, err = e.db.ExecContext(ctx,
		`INSERT INTO orchestrator_verdicts (run_id, agent_name, verdict, confidence, issues_json, duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		runID, v.AgentName, v.VerdictStr, v.Confidence, string(issuesJSON), v.DurationMs,
	); err != nil {
		log.Printf("Orchestrator: error saving verdict: %v", err)
	}
}

func (e *Engine) completeRun(ctx context.Context, runID int64, consensus Consensus, verdicts []Verdict, duration time.Duration) {
	outcome := "approved"
	if consensus.Blocked {
		outcome = "blocked"
	} else if !consensus.Approved {
		outcome = "rejected"
	}

	consensusJSON, err := json.Marshal(consensus)
	if err != nil {
		log.Printf("Orchestrator: error marshaling consensus: %v", err)
		consensusJSON = []byte("{}")
	}
	verdictsJSON, err := json.Marshal(verdicts)
	if err != nil {
		log.Printf("Orchestrator: error marshaling verdicts: %v", err)
		verdictsJSON = []byte("[]")
	}

	if _, err = e.db.ExecContext(ctx,
		`UPDATE orchestrator_runs SET outcome = ?, consensus_json = ?, verdicts_json = ?, duration_ms = ?
		 WHERE id = ?`,
		outcome, string(consensusJSON), string(verdictsJSON), duration.Milliseconds(), runID,
	); err != nil {
		log.Printf("Orchestrator: error completing run: %v", err)
	}
}
