package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/you/claude-gateway/internal/agent"
)

// reviewToolsOnly restricts agents to read-only file access.
var reviewToolsOnly = []string{"Read", "Glob", "Grep"}

// RunAgent executes a single agent via the sidecar and returns its verdict.
// Each agent gets a unique session so they can't see each other's output.
func RunAgent(ctx context.Context, agentClient *agent.Client, ag Agent, task, agentDNADir, workspaceDir string, timeout time.Duration) Verdict {
	start := time.Now()

	// Load DNA file
	dnaPath := filepath.Join(agentDNADir, ag.DNAFile)
	dna, err := os.ReadFile(dnaPath)
	if err != nil {
		return Verdict{
			AgentName:  ag.Name,
			Tier:       ag.Tier,
			Weight:     ag.Weight,
			VetoPower:  ag.HasVetoPower,
			VerdictStr: "ERROR",
			Error:      fmt.Sprintf("DNA not found: %s (%v)", ag.DNAFile, err),
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	// Build system prompt (DNA) and user prompt (task) separately.
	// DNA goes in system_prompt for potential prompt caching benefit.
	systemPrompt := buildSystemPrompt(ag, string(dna))
	userPrompt := buildUserPrompt(ag, task)

	// Determine allowed tools
	var allowedTools []string
	if !ag.CanWrite {
		allowedTools = reviewToolsOnly
	}

	// Set up timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Spawn an isolated CLI process via the sidecar.
	// Each agent gets its own separate claude process — no shared context,
	// no inherited biases from the orchestrator or other agents.
	opts := agent.RunAgentOptions{
		Prompt:       userPrompt,
		SystemPrompt: systemPrompt,
		WorkDir:      workspaceDir,
		AllowedTools: allowedTools,
		TimeoutMs:    int(timeout.Milliseconds()),
	}

	resp, err := agentClient.RunIsolatedAgent(timeoutCtx, opts)

	durationMs := time.Since(start).Milliseconds()

	if err != nil {
		errMsg := err.Error()
		if timeoutCtx.Err() == context.DeadlineExceeded {
			errMsg = fmt.Sprintf("timed out after %v", timeout)
		} else if ctx.Err() != nil {
			errMsg = "cancelled"
		}
		return Verdict{
			AgentName:  ag.Name,
			Tier:       ag.Tier,
			Weight:     ag.Weight,
			VetoPower:  ag.HasVetoPower,
			VerdictStr: "ERROR",
			Error:      errMsg,
			DurationMs: durationMs,
		}
	}

	if resp.IsRateLimited {
		return Verdict{
			AgentName:  ag.Name,
			Tier:       ag.Tier,
			Weight:     ag.Weight,
			VetoPower:  ag.HasVetoPower,
			VerdictStr: "ERROR",
			Error:      "rate limited (429)",
			RawOutput:  resp.Text,
			DurationMs: durationMs,
		}
	}

	// Parse verdict from sidecar response
	verdict := ParseVerdict(ag, resp.Text)
	verdict.DurationMs = durationMs

	return verdict
}

func buildSystemPrompt(ag Agent, dna string) string {
	vetoNote := ""
	if ag.HasVetoPower {
		vetoNote = " with VETO POWER"
	}
	return fmt.Sprintf(`You are the %s agent (Tier %d%s).

YOUR DNA (THIS DEFINES YOUR CONSTRAINTS - READ CAREFULLY):

%s`, ag.Name, ag.Tier, vetoNote, dna)
}

func buildUserPrompt(ag Agent, task string) string {
	return fmt.Sprintf(`TASK TO REVIEW:
%s

RULES:
1. You are INDEPENDENT - you cannot see other agents' opinions
2. Judge based ONLY on YOUR constraints from your DNA
3. Be specific about issues (file path, line number, exact problem)
4. You are sandboxed - only /workspace is accessible

IMPORTANT: Think out loud as you review. Explain your reasoning step by step so observers can follow your analysis. Read the relevant files and explain what you find.

After your analysis, provide your final verdict as JSON in this exact format:

`+"```json"+`
{
  "agent": "%s",
  "verdict": "APPROVE" | "REJECT" | "NEEDS_CHANGES",
  "confidence": <1-10>,
  "issues": [
    {
      "severity": "critical|high|medium|low",
      "category": "category",
      "description": "what is wrong",
      "file": "path/to/file",
      "line": 0,
      "fix": "suggested fix"
    }
  ],
  "notes": "summary of your review"
}
`+"```", task, ag.Name)
}

func errorVerdict(ag Agent, errMsg string, start time.Time) Verdict {
	return Verdict{
		AgentName:  ag.Name,
		Tier:       ag.Tier,
		Weight:     ag.Weight,
		VetoPower:  ag.HasVetoPower,
		VerdictStr: "ERROR",
		Error:      errMsg,
		DurationMs: time.Since(start).Milliseconds(),
	}
}

// LogVerdict logs a verdict summary. Used during team runs.
func LogVerdict(v Verdict) {
	icon := "?"
	switch {
	case v.IsApproval():
		icon = "PASS"
	case v.IsRejection():
		icon = "FAIL"
	case v.IsError():
		icon = "ERR"
	default:
		icon = "WARN"
	}
	log.Printf("  [%s] %s (T%d, %dms) issues=%d notes=%s",
		icon, v.AgentName, v.Tier, v.DurationMs, len(v.Issues), truncateStr(v.Notes, 100))
}
