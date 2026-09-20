package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/format"
)

// reviewToolsOnly returns tools for review agents. Write is included so
// agents can save a findings report under reports/; Bash/Edit are omitted
// so they cannot rewrite the code under review.
func reviewToolsOnly() []string {
	return []string{"Read", "Glob", "Grep", "Write"}
}

// maxDNAFileSize is the maximum size for an agent DNA file (100 KB).
const maxDNAFileSize = 100 * 1024

func resolveDNAFile(dirs []string, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("DNA not found: empty path")
	}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("DNA path traversal blocked: %s", rel)
	}
	var lastErr error
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			lastErr = err
			continue
		}
		absDNA, err := filepath.Abs(filepath.Join(absDir, clean))
		if err != nil {
			lastErr = err
			continue
		}
		sep := string(filepath.Separator)
		if absDNA != absDir && !strings.HasPrefix(absDNA, absDir+sep) {
			lastErr = fmt.Errorf("DNA path traversal blocked: %s", rel)
			continue
		}
		if _, err := os.Stat(absDNA); err != nil {
			lastErr = err
			continue
		}
		return absDNA, nil
	}
	if lastErr != nil {
		return "", fmt.Errorf("DNA not found: %s (%v)", rel, lastErr)
	}
	return "", fmt.Errorf("DNA not found: %s", rel)
}

// errorVerdict creates an ERROR verdict for an agent with timing info.
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

// RunAgent executes a single agent via the sidecar and returns its verdict.
// Each agent gets a unique session so they can't see each other's output.
func RunAgent(ctx context.Context, agentClient agent.Backend, ag Agent, task string, agentDNADirs []string, workspaceDir string, timeout time.Duration) Verdict {
	start := time.Now()

	dnaPath, err := resolveDNAFile(agentDNADirs, ag.DNAFile)
	if err != nil {
		return errorVerdict(ag, err.Error(), start)
	}

	dna, err := os.ReadFile(dnaPath)
	if err != nil {
		return errorVerdict(ag, fmt.Sprintf("DNA not found: %s (%v)", ag.DNAFile, err), start)
	}
	if len(dna) > maxDNAFileSize {
		return errorVerdict(ag, fmt.Sprintf("DNA file too large: %s (%d bytes, max %d)", ag.DNAFile, len(dna), maxDNAFileSize), start)
	}

	// Build system prompt (DNA) and user prompt (task) separately.
	// DNA goes in system_prompt for potential prompt caching benefit.
	systemPrompt := buildSystemPrompt(ag, string(dna))
	userPrompt := buildUserPrompt(ag, task)

	// Determine allowed tools
	var allowedTools []string
	if !ag.CanWrite {
		allowedTools = reviewToolsOnly()
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
		return errorVerdict(ag, errMsg, start)
	}

	if resp.IsRateLimited {
		v := errorVerdict(ag, "rate limited (429)", start)
		v.RawOutput = resp.Text
		return v
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

// LogVerdict logs a verdict summary. Used during team runs.
func LogVerdict(v Verdict) {
	log.Printf("  [%s] %s (T%d, %dms) issues=%d notes=%s",
		v.Icon(), v.AgentName, v.Tier, v.DurationMs, len(v.Issues), format.Truncate(v.Notes, 100))
}
