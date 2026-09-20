package orchestrator

import (
	"sort"
	"sync"
)

var teamsMu sync.RWMutex

// Agent represents a review agent with its DNA file and permissions.
type Agent struct {
	Name         string
	DNAFile      string // relative to agent DNA dir, e.g. "design/security-agent.md"
	Tier         int
	Weight       int
	HasVetoPower bool
	CanWrite     bool // if false, only Read/Glob/Grep/Write (reports/) tools allowed
}

// Team is a named group of agents that reviews together.
type Team struct {
	Name   string
	Agents []Agent
}

// allTeams defines the available orchestrator teams. Read-only after init.
// Ported from /workspace/orchestrator/main.go team definitions.
var allTeams = map[string]Team{
	"go-dev-team": {
		Name: "go-dev-team",
		Agents: []Agent{
			{Name: "GoPurist", DNAFile: "coding/go-dev-team/go-purist.md", Tier: 4, Weight: 1},
			{Name: "GoPragmatist", DNAFile: "coding/go-dev-team/go-pragmatist.md", Tier: 4, Weight: 1},
			{Name: "GoPessimist", DNAFile: "coding/go-dev-team/go-pessimist.md", Tier: 4, Weight: 1},
		},
	},
	"go-sec-team": {
		Name: "go-sec-team",
		Agents: []Agent{
			{Name: "GoSecurity", DNAFile: "coding/go-sec-team/go-security-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
			{Name: "GoHacker", DNAFile: "coding/go-sec-team/go-hacker-agent.md", Tier: 2, Weight: 100},
		},
	},
	"fe-dev-team": {
		Name: "fe-dev-team",
		Agents: []Agent{
			{Name: "FEPurist", DNAFile: "coding/frontend-team/fe-purist.md", Tier: 4, Weight: 1},
			{Name: "FEPragmatist", DNAFile: "coding/frontend-team/fe-pragmatist.md", Tier: 4, Weight: 1},
			{Name: "FEPessimist", DNAFile: "coding/frontend-team/fe-pessimist.md", Tier: 4, Weight: 1},
		},
	},
	"fe-sec-team": {
		Name: "fe-sec-team",
		Agents: []Agent{
			{Name: "FESecurity", DNAFile: "coding/frontend-team/fe-security-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
			{Name: "FEHacker", DNAFile: "coding/frontend-team/fe-hacker-agent.md", Tier: 2, Weight: 100},
		},
	},
	"design": {
		Name: "design",
		Agents: []Agent{
			{Name: "Security", DNAFile: "design/security-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
			{Name: "Compliance", DNAFile: "design/compliance-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
			{Name: "DataIntegrity", DNAFile: "design/data-integrity-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
			{Name: "Performance", DNAFile: "design/performance-agent.md", Tier: 3, Weight: 10},
			{Name: "ErrorHandling", DNAFile: "design/error-handling-agent.md", Tier: 2, Weight: 100},
		},
	},
	"integration": {
		Name: "integration",
		Agents: []Agent{
			{Name: "APIGuardian", DNAFile: "coding/integration-team/api-guardian-agent.md", Tier: 3, Weight: 10},
			{Name: "CrossLangSecurity", DNAFile: "coding/security-review/cross-language-security.md", Tier: 1, Weight: 1000, HasVetoPower: true},
		},
	},
	"logging-audit": {
		Name: "logging-audit",
		Agents: []Agent{
			{Name: "LoggingAudit", DNAFile: "coding/logging-audit-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
		},
	},
	"data-integrity": {
		Name: "data-integrity",
		Agents: []Agent{
			{Name: "DataFlowIntegrity", DNAFile: "coding/data-integrity-flow-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
		},
	},
	"mobile-review": {
		Name: "mobile-review",
		Agents: []Agent{
			{Name: "KotlinPurist", DNAFile: "coding/kotlin-team/kotlin-purist.md", Tier: 4, Weight: 1},
			{Name: "MobileSecurity", DNAFile: "coding/kotlin-team/mobile-security-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
			{Name: "MobileHacker", DNAFile: "coding/kotlin-team/mobile-hacker-agent.md", Tier: 2, Weight: 100},
		},
	},
	"python-dev-team": {
		Name: "python-dev-team",
		Agents: []Agent{
			{Name: "PythonPurist", DNAFile: "coding/python-team/python-purist.md", Tier: 4, Weight: 1},
			{Name: "PythonPragmatist", DNAFile: "coding/python-team/python-pragmatist.md", Tier: 4, Weight: 1},
			{Name: "PythonPessimist", DNAFile: "coding/python-team/python-pessimist.md", Tier: 4, Weight: 1},
		},
	},
	"python-sec-team": {
		Name: "python-sec-team",
		Agents: []Agent{
			{Name: "PythonSecurity", DNAFile: "coding/python-team/python-security-agent.md", Tier: 1, Weight: 1000, HasVetoPower: true},
			{Name: "PythonHacker", DNAFile: "coding/python-team/python-hacker-agent.md", Tier: 2, Weight: 100},
		},
	},
	"research": {
		Name: "research",
		Agents: []Agent{
			{Name: "UIUXResearcher", DNAFile: "research/ui-ux-researcher.md", Tier: 3, Weight: 10},
		},
	},
}

// TeamNames returns all available team names in sorted order.
func TeamNames() []string {
	teamsMu.RLock()
	defer teamsMu.RUnlock()
	names := make([]string, 0, len(allTeams))
	for name := range allTeams {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetTeam returns a team by name.
func GetTeam(name string) (Team, bool) {
	teamsMu.RLock()
	defer teamsMu.RUnlock()
	t, ok := allTeams[name]
	if !ok {
		return Team{}, false
	}
	t.Agents = append([]Agent(nil), t.Agents...)
	return t, true
}
