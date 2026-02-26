package orchestrator

import "testing"

func testAgent() Agent {
	return Agent{
		Name:    "TestAgent",
		DNAFile: "test.md",
		Tier:    4,
		Weight:  1,
	}
}

func TestParseVerdict_ValidCodeBlock(t *testing.T) {
	output := `Some analysis text here.

` + "```json" + `
{
  "agent": "TestAgent",
  "verdict": "APPROVE",
  "confidence": 8,
  "issues": [
    {"severity": "low", "description": "minor thing"}
  ],
  "notes": "Looks good."
}
` + "```"

	v := ParseVerdict(testAgent(), output)

	if v.VerdictStr != "APPROVE" {
		t.Errorf("VerdictStr = %q, want APPROVE", v.VerdictStr)
	}
	if v.Confidence != 8 {
		t.Errorf("Confidence = %d, want 8", v.Confidence)
	}
	if len(v.Issues) != 1 {
		t.Errorf("Issues count = %d, want 1", len(v.Issues))
	}
	if v.Notes != "Looks good." {
		t.Errorf("Notes = %q, want %q", v.Notes, "Looks good.")
	}
	if v.Error != "" {
		t.Errorf("Error = %q, want empty", v.Error)
	}
}

func TestParseVerdict_EmptyOutput(t *testing.T) {
	v := ParseVerdict(testAgent(), "")

	if v.VerdictStr != "ERROR" {
		t.Errorf("VerdictStr = %q, want ERROR", v.VerdictStr)
	}
	if v.Error != "empty output" {
		t.Errorf("Error = %q, want %q", v.Error, "empty output")
	}
}

func TestParseVerdict_NoJSON(t *testing.T) {
	v := ParseVerdict(testAgent(), "This is just plain text with no JSON at all.")

	if v.VerdictStr != "ERROR" {
		t.Errorf("VerdictStr = %q, want ERROR", v.VerdictStr)
	}
	if v.Error != "no JSON verdict found in output" {
		t.Errorf("Error = %q, want %q", v.Error, "no JSON verdict found in output")
	}
}

func TestParseVerdict_MalformedJSON(t *testing.T) {
	output := "```json\n{invalid json}\n```"
	v := ParseVerdict(testAgent(), output)

	if v.VerdictStr != "ERROR" {
		t.Errorf("VerdictStr = %q, want ERROR", v.VerdictStr)
	}
	if v.Error == "" {
		t.Error("Expected parse error, got empty")
	}
}

func TestParseVerdict_FallbackJSON(t *testing.T) {
	// JSON without code block fences — should be found by fallback scanner
	output := `Analysis goes here.

{
  "agent": "TestAgent",
  "verdict": "NEEDS_CHANGES",
  "confidence": 6,
  "issues": [],
  "notes": "Needs work."
}`

	v := ParseVerdict(testAgent(), output)

	if v.VerdictStr != "NEEDS_CHANGES" {
		t.Errorf("VerdictStr = %q, want NEEDS_CHANGES", v.VerdictStr)
	}
	if v.Confidence != 6 {
		t.Errorf("Confidence = %d, want 6", v.Confidence)
	}
}

func TestParseVerdict_NormalizesCase(t *testing.T) {
	output := `{"verdict": "approve", "confidence": 9, "issues": [], "notes": ""}`
	v := ParseVerdict(testAgent(), output)

	if v.VerdictStr != "APPROVE" {
		t.Errorf("VerdictStr = %q, want APPROVE (uppercased)", v.VerdictStr)
	}
}

func TestParseVerdict_LargeOutput(t *testing.T) {
	// Simulate a very large output with the JSON at the end
	padding := make([]byte, 20000)
	for i := range padding {
		padding[i] = 'x'
	}
	output := string(padding) + `{"verdict": "REJECT", "confidence": 3, "issues": [{"severity": "critical", "description": "bad"}], "notes": "Fail."}`

	v := ParseVerdict(testAgent(), output)

	if v.VerdictStr != "REJECT" {
		t.Errorf("VerdictStr = %q, want REJECT", v.VerdictStr)
	}
	if len(v.Issues) != 1 {
		t.Errorf("Issues count = %d, want 1", len(v.Issues))
	}
}

func TestParseVerdict_MultipleCodeBlocks(t *testing.T) {
	// Agent outputs an example JSON code block before the actual verdict.
	// The parser should pick the LAST code block, not the first.
	output := `Let me show you an example of bad output:

` + "```json" + `
{
  "agent": "TestAgent",
  "verdict": "REJECT",
  "confidence": 2,
  "issues": [{"severity": "critical", "description": "example issue"}],
  "notes": "This is just an example."
}
` + "```" + `

Now here is my actual verdict:

` + "```json" + `
{
  "agent": "TestAgent",
  "verdict": "APPROVE",
  "confidence": 9,
  "issues": [],
  "notes": "All good."
}
` + "```"

	v := ParseVerdict(testAgent(), output)

	if v.VerdictStr != "APPROVE" {
		t.Errorf("VerdictStr = %q, want APPROVE (should pick last code block)", v.VerdictStr)
	}
	if v.Confidence != 9 {
		t.Errorf("Confidence = %d, want 9", v.Confidence)
	}
	if len(v.Issues) != 0 {
		t.Errorf("Issues count = %d, want 0", len(v.Issues))
	}
	if v.Notes != "All good." {
		t.Errorf("Notes = %q, want %q", v.Notes, "All good.")
	}
}

func TestVerdictMethods(t *testing.T) {
	tests := []struct {
		verdictStr string
		isApproval bool
		isReject   bool
		isError    bool
		icon       string
	}{
		{"APPROVE", true, false, false, "PASS"},
		{"APPROVED", true, false, false, "PASS"},
		{"REJECT", false, true, false, "FAIL"},
		{"REJECTED", false, true, false, "FAIL"},
		{"ERROR", false, false, true, "ERR"},
		{"NEEDS_CHANGES", false, false, false, "WARN"},
	}
	for _, tt := range tests {
		v := Verdict{VerdictStr: tt.verdictStr}
		if got := v.IsApproval(); got != tt.isApproval {
			t.Errorf("%s: IsApproval() = %v, want %v", tt.verdictStr, got, tt.isApproval)
		}
		if got := v.IsRejection(); got != tt.isReject {
			t.Errorf("%s: IsRejection() = %v, want %v", tt.verdictStr, got, tt.isReject)
		}
		if got := v.IsError(); got != tt.isError {
			t.Errorf("%s: IsError() = %v, want %v", tt.verdictStr, got, tt.isError)
		}
		if got := v.Icon(); got != tt.icon {
			t.Errorf("%s: Icon() = %q, want %q", tt.verdictStr, got, tt.icon)
		}
	}
}

func TestIssuesBySeverity(t *testing.T) {
	v := Verdict{
		Issues: []Issue{
			{Severity: "critical", Description: "a"},
			{Severity: "high", Description: "b"},
			{Severity: "Critical", Description: "c"},
			{Severity: "low", Description: "d"},
		},
	}
	critical := v.IssuesBySeverity("critical")
	if len(critical) != 2 {
		t.Errorf("critical issues = %d, want 2", len(critical))
	}
	high := v.IssuesBySeverity("high")
	if len(high) != 1 {
		t.Errorf("high issues = %d, want 1", len(high))
	}
}
