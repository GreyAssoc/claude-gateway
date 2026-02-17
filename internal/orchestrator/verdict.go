package orchestrator

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Issue represents a problem found by an agent.
type Issue struct {
	Severity    string `json:"severity"` // critical, high, medium, low
	Category    string `json:"category"`
	Description string `json:"description"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Fix         string `json:"fix,omitempty"`
}

// Verdict is the result of a single agent's review.
type Verdict struct {
	// Metadata from agent config
	AgentName  string
	Tier       int
	Weight     int
	VetoPower  bool
	DurationMs int64
	Error      string
	RawOutput  string

	// Parsed from agent JSON output
	VerdictStr string  `json:"verdict"`
	Confidence int     `json:"confidence"`
	Issues     []Issue `json:"issues"`
	Notes      string  `json:"notes"`
}

// jsonVerdict is the struct we unmarshal from agent output.
type jsonVerdict struct {
	Agent      string  `json:"agent"`
	Verdict    string  `json:"verdict"`
	Confidence int     `json:"confidence"`
	Issues     []Issue `json:"issues"`
	Notes      string  `json:"notes"`
}

var (
	codeBlockRe = regexp.MustCompile("(?s)```json\\s*(.*?)\\s*```")
	rawJSONRe   = regexp.MustCompile(`(?s)\{[\s\S]*"verdict"[\s\S]*?\}`)
)

// ParseVerdict extracts a JSON verdict from agent output text.
func ParseVerdict(ag Agent, output string) Verdict {
	v := Verdict{
		AgentName: ag.Name,
		Tier:      ag.Tier,
		Weight:    ag.Weight,
		VetoPower: ag.HasVetoPower,
		RawOutput: output,
	}

	if output == "" {
		v.Error = "empty output"
		v.VerdictStr = "ERROR"
		return v
	}

	// Try code block first
	jsonStr := ""
	if matches := codeBlockRe.FindStringSubmatch(output); len(matches) > 1 {
		jsonStr = matches[1]
	} else if matches := rawJSONRe.FindString(output); matches != "" {
		jsonStr = matches
	}

	if jsonStr == "" {
		v.Error = "no JSON verdict found in output"
		v.VerdictStr = "ERROR"
		return v
	}

	var parsed jsonVerdict
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		v.Error = "failed to parse verdict JSON: " + err.Error()
		v.VerdictStr = "ERROR"
		return v
	}

	v.VerdictStr = strings.ToUpper(parsed.Verdict)
	v.Confidence = parsed.Confidence
	v.Issues = parsed.Issues
	v.Notes = parsed.Notes

	return v
}

// IsApproval returns true if the verdict is an approval.
func (v *Verdict) IsApproval() bool {
	return v.VerdictStr == "APPROVE" || v.VerdictStr == "APPROVED"
}

// IsRejection returns true if the verdict is a rejection.
func (v *Verdict) IsRejection() bool {
	return v.VerdictStr == "REJECT" || v.VerdictStr == "REJECTED"
}

// IsError returns true if the verdict is an error.
func (v *Verdict) IsError() bool {
	return v.VerdictStr == "ERROR"
}

// IssuesBySeverity returns issues filtered by severity.
func (v *Verdict) IssuesBySeverity(severity string) []Issue {
	var filtered []Issue
	for _, iss := range v.Issues {
		if strings.EqualFold(iss.Severity, severity) {
			filtered = append(filtered, iss)
		}
	}
	return filtered
}
