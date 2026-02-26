package orchestrator

import (
	"fmt"
	"strings"

	"github.com/you/claude-gateway/internal/format"
)

// Consensus represents the aggregated result of all agents in a team.
type Consensus struct {
	Approved        bool
	Blocked         bool
	BlockedBy       []string
	Approvals       int
	Rejections      int
	NeedsChanges    int
	Errors          int
	TotalAgents     int
	CompromiseScore float64
	Summary         string
}

// severityWeights maps issue severity to scoring weight.
var severityWeights = map[string]float64{
	"critical": 10,
	"high":     5,
	"medium":   2,
	"low":      1,
}

// ApplyConsensus evaluates a set of verdicts and produces a consensus result.
func ApplyConsensus(verdicts []Verdict) Consensus {
	c := Consensus{
		TotalAgents: len(verdicts),
	}

	validVerdicts := 0

	for _, v := range verdicts {
		switch {
		case v.IsError():
			c.Errors++
			continue
		case v.IsApproval():
			c.Approvals++
		case v.IsRejection():
			c.Rejections++
		default:
			c.NeedsChanges++
		}
		validVerdicts++

		// Veto check: Tier 1 agents with veto power can block
		if v.VetoPower && v.IsRejection() {
			c.Blocked = true
			c.BlockedBy = append(c.BlockedBy, v.AgentName)
		}

		// Issue weighting
		for _, iss := range v.Issues {
			weight, ok := severityWeights[strings.ToLower(iss.Severity)]
			if !ok {
				weight = 1
			}
			c.CompromiseScore += float64(v.Weight) * weight
		}
	}

	// Approval: simple majority of valid verdicts, unless blocked.
	// Quorum: at least half+1 of total agents must have valid verdicts
	// to prevent a single surviving agent from approving when others error.
	quorum := c.TotalAgents/2 + 1
	if c.Blocked {
		c.Approved = false
	} else if validVerdicts >= quorum {
		c.Approved = c.Approvals > validVerdicts/2
	}

	c.Summary = buildConsensusSummary(c)
	return c
}

func buildConsensusSummary(c Consensus) string {
	var parts []string

	if c.Blocked {
		parts = append(parts, fmt.Sprintf("BLOCKED by: %s", strings.Join(c.BlockedBy, ", ")))
	} else if c.Approved {
		parts = append(parts, "APPROVED")
	} else {
		parts = append(parts, "NOT APPROVED")
	}

	parts = append(parts, fmt.Sprintf(
		"Votes: %d approve, %d reject, %d needs changes, %d errors (of %d agents)",
		c.Approvals, c.Rejections, c.NeedsChanges, c.Errors, c.TotalAgents))

	if c.CompromiseScore > 0 {
		parts = append(parts, fmt.Sprintf("Compromise score: %.0f", c.CompromiseScore))
	}

	return strings.Join(parts, "\n")
}

// FormatConsensusText returns a human-readable consensus report.
func FormatConsensusText(team string, task string, consensus Consensus, verdicts []Verdict) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("*Orchestrator: %s*\n", team))
	sb.WriteString(fmt.Sprintf("Task: %s\n\n", format.Truncate(task, 200)))
	sb.WriteString(fmt.Sprintf("Result: %s\n\n", consensus.Summary))

	for _, v := range verdicts {
		icon := v.Icon()

		vetoLabel := ""
		if v.VetoPower {
			vetoLabel = " [VETO]"
		}

		sb.WriteString(fmt.Sprintf("[%s] %s (T%d)%s", icon, v.AgentName, v.Tier, vetoLabel))
		if v.Confidence > 0 {
			sb.WriteString(fmt.Sprintf(" confidence:%d", v.Confidence))
		}
		sb.WriteString("\n")

		if len(v.Issues) > 0 {
			for _, iss := range v.Issues {
				loc := ""
				if iss.File != "" {
					loc = fmt.Sprintf(" @ %s", iss.File)
					if iss.Line > 0 {
						loc = fmt.Sprintf(" @ %s:%d", iss.File, iss.Line)
					}
				}
				sb.WriteString(fmt.Sprintf("  - [%s] %s%s\n", iss.Severity, iss.Description, loc))
			}
		}

		if v.Notes != "" {
			sb.WriteString(fmt.Sprintf("  Notes: %s\n", format.Truncate(v.Notes, 300)))
		}

		if v.Error != "" {
			sb.WriteString(fmt.Sprintf("  Error: %s\n", v.Error))
		}

		sb.WriteString("\n")
	}

	return sb.String()
}
