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
)

// Engine manages orchestrator team runs.
type Engine struct {
	db           *db.DB
	agentClient  *agent.Client
	agentDNADir  string
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
func NewEngine(database *db.DB, agentClient *agent.Client, agentDNADir, workspaceDir string, timeoutSeconds int) *Engine {
	return &Engine{
		db:           database,
		agentClient:  agentClient,
		agentDNADir:  agentDNADir,
		workspaceDir: workspaceDir,
		timeout:      time.Duration(timeoutSeconds) * time.Second,
	}
}

// RunTeam executes all agents in a team sequentially and returns the consensus.
func (e *Engine) RunTeam(ctx context.Context, teamName, task string) (*RunResult, error) {
	team, ok := GetTeam(teamName)
	if !ok {
		return nil, fmt.Errorf("unknown team: %s (available: %s)", teamName, strings.Join(TeamNames(), ", "))
	}

	start := time.Now()
	log.Printf("Orchestrator: running team %s (%d agents)", teamName, len(team.Agents))

	// Create run record
	runID, err := e.createRun(teamName, task)
	if err != nil {
		log.Printf("Orchestrator: warning creating run record: %v", err)
	}

	// Run each agent sequentially (independence: agents can't see each other's output)
	var verdicts []Verdict
	for _, ag := range team.Agents {
		log.Printf("Orchestrator: running %s...", ag.Name)

		verdict := RunAgent(ctx, e.agentClient, ag, task, e.agentDNADir, e.workspaceDir, e.timeout)
		LogVerdict(verdict)
		verdicts = append(verdicts, verdict)

		// Save verdict to DB
		if runID > 0 {
			e.saveVerdict(runID, verdict)
		}

		// Check for context cancellation
		if ctx.Err() != nil {
			break
		}
	}

	// Apply consensus
	consensus := ApplyConsensus(verdicts)
	duration := time.Since(start)

	log.Printf("Orchestrator: %s complete in %v — %s", teamName, duration, consensus.Summary)

	// Update run record
	if runID > 0 {
		e.completeRun(runID, consensus, verdicts, duration)
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

	runID, _ := e.createRun(teamName, task)

	var verdicts []Verdict
	for i, ag := range team.Agents {
		if onProgress != nil {
			onProgress(fmt.Sprintf("[%d/%d] Running %s...", i+1, len(team.Agents), ag.Name))
		}

		verdict := RunAgent(ctx, e.agentClient, ag, task, e.agentDNADir, e.workspaceDir, e.timeout)
		LogVerdict(verdict)
		verdicts = append(verdicts, verdict)

		if runID > 0 {
			e.saveVerdict(runID, verdict)
		}

		// Report individual result
		if onProgress != nil {
			icon := "?"
			switch {
			case verdict.IsApproval():
				icon = "PASS"
			case verdict.IsRejection():
				icon = "FAIL"
			default:
				icon = "WARN"
			}
			onProgress(fmt.Sprintf("[%s] %s — %d issues", icon, ag.Name, len(verdict.Issues)))
		}

		if ctx.Err() != nil {
			break
		}
	}

	consensus := ApplyConsensus(verdicts)
	duration := time.Since(start)

	if runID > 0 {
		e.completeRun(runID, consensus, verdicts, duration)
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
func (e *Engine) ListRecentRuns(limit int) ([]RunSummary, error) {
	rows, err := e.db.Query(
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
		team := AllTeams[teamName]
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
func (e *Engine) RecentRunsText(limit int) string {
	runs, err := e.ListRecentRuns(limit)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if len(runs) == 0 {
		return "No orchestrator runs yet."
	}

	var sb strings.Builder
	sb.WriteString("*Recent Orchestrator Runs*\n\n")
	for _, r := range runs {
		task := r.Task
		if len(task) > 60 {
			task = task[:60] + "..."
		}
		sb.WriteString(fmt.Sprintf("#%d [%s] %s — %s (%dms)\n", r.ID, r.Outcome, r.Team, task, r.DurationMs))
	}
	return sb.String()
}

func (e *Engine) createRun(team, task string) (int64, error) {
	result, err := e.db.Exec(
		`INSERT INTO orchestrator_runs (team, task, outcome, created_at)
		 VALUES (?, ?, 'running', datetime('now'))`,
		team, task,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (e *Engine) saveVerdict(runID int64, v Verdict) {
	issuesJSON, _ := json.Marshal(v.Issues)
	_, err := e.db.Exec(
		`INSERT INTO orchestrator_verdicts (run_id, agent_name, verdict, confidence, issues_json, duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		runID, v.AgentName, v.VerdictStr, v.Confidence, string(issuesJSON), v.DurationMs,
	)
	if err != nil {
		log.Printf("Orchestrator: error saving verdict: %v", err)
	}
}

func (e *Engine) completeRun(runID int64, consensus Consensus, verdicts []Verdict, duration time.Duration) {
	outcome := "approved"
	if consensus.Blocked {
		outcome = "blocked"
	} else if !consensus.Approved {
		outcome = "rejected"
	}

	consensusJSON, _ := json.Marshal(consensus)
	verdictsJSON, _ := json.Marshal(verdicts)

	_, err := e.db.Exec(
		`UPDATE orchestrator_runs SET outcome = ?, consensus_json = ?, verdicts_json = ?, duration_ms = ?
		 WHERE id = ?`,
		outcome, string(consensusJSON), string(verdictsJSON), duration.Milliseconds(), runID,
	)
	if err != nil {
		log.Printf("Orchestrator: error completing run: %v", err)
	}
}
