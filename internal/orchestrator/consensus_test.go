package orchestrator

import "testing"

func TestApplyConsensus_UnanimousApprove(t *testing.T) {
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "B", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "C", VerdictStr: "APPROVE", Weight: 1},
	}
	c := ApplyConsensus(verdicts)

	if !c.Approved {
		t.Error("Expected approved")
	}
	if c.Blocked {
		t.Error("Expected not blocked")
	}
	if c.Approvals != 3 {
		t.Errorf("Approvals = %d, want 3", c.Approvals)
	}
}

func TestApplyConsensus_MajorityApprove(t *testing.T) {
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "B", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "C", VerdictStr: "NEEDS_CHANGES", Weight: 1},
	}
	c := ApplyConsensus(verdicts)

	if !c.Approved {
		t.Error("Expected approved with 2/3 majority")
	}
	if c.Approvals != 2 {
		t.Errorf("Approvals = %d, want 2", c.Approvals)
	}
	if c.NeedsChanges != 1 {
		t.Errorf("NeedsChanges = %d, want 1", c.NeedsChanges)
	}
}

func TestApplyConsensus_Tie(t *testing.T) {
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "B", VerdictStr: "NEEDS_CHANGES", Weight: 1},
	}
	c := ApplyConsensus(verdicts)

	// 1 > 2/2 = 1 > 1 = false — tie is NOT approved (strict majority required)
	if c.Approved {
		t.Error("Expected not approved on tie (strict majority required)")
	}
}

func TestApplyConsensus_VetoBlocks(t *testing.T) {
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "B", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "Security", VerdictStr: "REJECT", Weight: 1000, Tier: 1, VetoPower: true},
	}
	c := ApplyConsensus(verdicts)

	if c.Approved {
		t.Error("Expected not approved when veto agent rejects")
	}
	if !c.Blocked {
		t.Error("Expected blocked by veto")
	}
	if len(c.BlockedBy) != 1 || c.BlockedBy[0] != "Security" {
		t.Errorf("BlockedBy = %v, want [Security]", c.BlockedBy)
	}
}

func TestApplyConsensus_VetoApproveDoesNotBlock(t *testing.T) {
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "NEEDS_CHANGES", Weight: 1},
		{AgentName: "Security", VerdictStr: "APPROVE", Weight: 1000, Tier: 1, VetoPower: true},
	}
	c := ApplyConsensus(verdicts)

	// Veto only blocks on REJECT, not on APPROVE
	if c.Blocked {
		t.Error("Veto agent approving should not block")
	}
}

func TestApplyConsensus_AllErrors(t *testing.T) {
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "ERROR", Error: "timeout"},
		{AgentName: "B", VerdictStr: "ERROR", Error: "rate limited"},
	}
	c := ApplyConsensus(verdicts)

	if c.Approved {
		t.Error("Expected not approved when all agents error")
	}
	if c.Errors != 2 {
		t.Errorf("Errors = %d, want 2", c.Errors)
	}
}

func TestApplyConsensus_ErrorsExcluded(t *testing.T) {
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "B", VerdictStr: "ERROR", Error: "timeout"},
		{AgentName: "C", VerdictStr: "NEEDS_CHANGES", Weight: 1},
	}
	c := ApplyConsensus(verdicts)

	// Only 2 valid verdicts (A and C). 1 approval out of 2 = not majority.
	if c.Approved {
		t.Error("Expected not approved: 1/2 valid verdicts is not majority")
	}
	if c.Errors != 1 {
		t.Errorf("Errors = %d, want 1", c.Errors)
	}
}

func TestApplyConsensus_CompromiseScore(t *testing.T) {
	verdicts := []Verdict{
		{
			AgentName:  "A",
			VerdictStr: "APPROVE",
			Weight:     1,
			Issues: []Issue{
				{Severity: "low", Description: "minor"},
			},
		},
		{
			AgentName:  "B",
			VerdictStr: "NEEDS_CHANGES",
			Weight:     100,
			Issues: []Issue{
				{Severity: "high", Description: "serious"},
				{Severity: "medium", Description: "moderate"},
			},
		},
	}
	c := ApplyConsensus(verdicts)

	// A: weight=1 * low(1) = 1
	// B: weight=100 * high(5) + weight=100 * medium(2) = 500 + 200 = 700
	// Total: 701
	if c.CompromiseScore != 701 {
		t.Errorf("CompromiseScore = %.0f, want 701", c.CompromiseScore)
	}
}

func TestApplyConsensus_SingleAgent(t *testing.T) {
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "APPROVE", Weight: 1},
	}
	c := ApplyConsensus(verdicts)

	// 1 > 1/2 = 1 > 0 = true
	if !c.Approved {
		t.Error("Single approving agent should result in approval")
	}
}

func TestApplyConsensus_QuorumNotMet(t *testing.T) {
	// 2 of 3 agents error, only 1 valid APPROVE.
	// Quorum requires validVerdicts >= TotalAgents/2 + 1 = 2.
	// With only 1 valid verdict, quorum is not met.
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "B", VerdictStr: "ERROR", Error: "timeout"},
		{AgentName: "C", VerdictStr: "ERROR", Error: "rate limited"},
	}
	c := ApplyConsensus(verdicts)

	if c.Approved {
		t.Error("Expected not approved: quorum not met (1 valid of 3 agents)")
	}
	if c.Errors != 2 {
		t.Errorf("Errors = %d, want 2", c.Errors)
	}
}

func TestApplyConsensus_QuorumMetWithErrors(t *testing.T) {
	// 1 of 3 agents errors, 2 valid with both APPROVE.
	// Quorum requires validVerdicts >= TotalAgents/2 + 1 = 2. Met.
	verdicts := []Verdict{
		{AgentName: "A", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "B", VerdictStr: "APPROVE", Weight: 1},
		{AgentName: "C", VerdictStr: "ERROR", Error: "timeout"},
	}
	c := ApplyConsensus(verdicts)

	if !c.Approved {
		t.Error("Expected approved: quorum met (2 valid of 3, both approve)")
	}
}
