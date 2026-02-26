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
	AgentName  string `json:"agent_name"`
	Tier       int    `json:"tier"`
	Weight     int    `json:"weight"`
	VetoPower  bool   `json:"veto_power"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	RawOutput  string `json:"raw_output,omitempty"`

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

var codeBlockRe = regexp.MustCompile("(?s)```json\\s*(.*?)\\s*```")

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

	// Try code block first — use the last match since agents may include
	// example JSON before their actual verdict block.
	jsonStr := ""
	if allMatches := codeBlockRe.FindAllStringSubmatch(output, -1); len(allMatches) > 0 {
		jsonStr = allMatches[len(allMatches)-1][1]
	} else {
		// Fallback: find a valid JSON object containing "verdict" by scanning
		// backwards from the last occurrence of "verdict" to find opening braces,
		// then use json.Decoder to find the matching close.
		jsonStr = findVerdictJSON(output)
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

// findVerdictJSON scans the text for a valid JSON object containing "verdict".
// Limits the scan window to the last 8000 characters to avoid O(n^2) on large outputs.
func findVerdictJSON(text string) string {
	// Limit scan window to avoid O(n^2) on large outputs with many braces
	const maxScanWindow = 8000
	if len(text) > maxScanWindow {
		text = text[len(text)-maxScanWindow:]
	}

	idx := strings.LastIndex(text, `"verdict"`)
	if idx < 0 {
		return ""
	}

	// Scan backwards from "verdict" to find candidate opening braces.
	// Limit iterations to prevent excessive CPU on pathological input.
	const maxAttempts = 50
	attempts := 0
	for start := idx - 1; start >= 0; start-- {
		if text[start] != '{' {
			continue
		}
		attempts++
		if attempts > maxAttempts {
			break
		}
		dec := json.NewDecoder(strings.NewReader(text[start:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil {
			// Verify it actually contains a "verdict" field
			var check map[string]any
			if json.Unmarshal(raw, &check) == nil {
				if _, ok := check["verdict"]; ok {
					return string(raw)
				}
			}
		}
	}
	return ""
}

// IsApproval returns true if the verdict is an approval.
func (v Verdict) IsApproval() bool {
	return v.VerdictStr == "APPROVE" || v.VerdictStr == "APPROVED"
}

// IsRejection returns true if the verdict is a rejection.
func (v Verdict) IsRejection() bool {
	return v.VerdictStr == "REJECT" || v.VerdictStr == "REJECTED"
}

// IsError returns true if the verdict is an error.
func (v Verdict) IsError() bool {
	return v.VerdictStr == "ERROR"
}

// Icon returns a short status label for the verdict.
func (v Verdict) Icon() string {
	switch {
	case v.IsApproval():
		return "PASS"
	case v.IsRejection():
		return "FAIL"
	case v.IsError():
		return "ERR"
	default:
		return "WARN"
	}
}

// IssuesBySeverity returns issues filtered by severity.
func (v Verdict) IssuesBySeverity(severity string) []Issue {
	var filtered []Issue
	for _, iss := range v.Issues {
		if strings.EqualFold(iss.Severity, severity) {
			filtered = append(filtered, iss)
		}
	}
	return filtered
}
