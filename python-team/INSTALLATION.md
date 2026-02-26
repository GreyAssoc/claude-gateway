# Python Team Installation Guide

## Files Created

### Python Team Agents
Located in: `/workspace/projects/claude-gateway/python-team/`

1. **python-purist.md** - PEP 8, type hints, Pythonic idioms
2. **python-pragmatist.md** - Simplicity, stdlib first, working code
3. **python-pessimist.md** - Defensive coding, error handling
4. **python-security-agent.md** - Tier 1 security agent with VETO POWER
5. **python-hacker-agent.md** - Tier 2 attacker mindset

### Enhancement Files
- **coding-agent-enhanced.md** - Reference for Python additions (prepared by agent)
- **/workspace/python-additions-for-coding-agent.md** - Detailed Python additions
- **/workspace/apply_python_additions.py** - Automation script

## Installation Steps

### Step 1: Move Python Team Agents

```bash
# Create target directory
mkdir -p /workspace/.claude/agents/coding/python-team

# Copy Python team agent DNA files
cp /workspace/projects/claude-gateway/python-team/python-*.md \
   /workspace/.claude/agents/coding/python-team/
```

### Step 2: Enhance Coding Agent

```bash
# Run the automation script to add Python support
python3 /workspace/apply_python_additions.py

# This will update: /workspace/.claude/agents/coding/coding-agent.md
```

Or manually apply the changes from `/workspace/python-additions-for-coding-agent.md`

### Step 3: Update Orchestrator Teams

Edit: `/workspace/projects/claude-gateway/internal/orchestrator/teams.go`

Add Python teams after the existing teams:

```go
// Python Development Team
{
    ID:          "python-dev-team",
    Name:        "Python Development Team",
    Description: "Python code review focusing on Pythonic idioms, stdlib usage, and correctness",
    AgentDNADir: agentDNADir,
    Agents: []Agent{
        {Name: "PythonPragmatist", Tier: 1, Weight: 3, DNAFile: "python-team/python-pragmatist.md", CanWrite: false, HasVetoPower: false},
        {Name: "PythonPurist", Tier: 2, Weight: 2, DNAFile: "python-team/python-purist.md", CanWrite: false, HasVetoPower: false},
        {Name: "PythonPessimist", Tier: 3, Weight: 1, DNAFile: "python-team/python-pessimist.md", CanWrite: false, HasVetoPower: false},
    },
},

// Python Security Team
{
    ID:          "python-sec-team",
    Name:        "Python Security Team",
    Description: "Python security review with veto power + attacker mindset",
    AgentDNADir: agentDNADir,
    Agents: []Agent{
        {Name: "PythonSecurityAgent", Tier: 1, Weight: 5, DNAFile: "python-team/python-security-agent.md", CanWrite: false, HasVetoPower: true},
        {Name: "PythonHackerAgent", Tier: 2, Weight: 3, DNAFile: "python-team/python-hacker-agent.md", CanWrite: false, HasVetoPower: false},
        {Name: "PythonPessimist", Tier: 3, Weight: 1, DNAFile: "python-team/python-pessimist.md", CanWrite: false, HasVetoPower: false},
    },
},
```

### Step 4: Update Bot Commands

Edit: `/workspace/projects/claude-gateway/internal/bot/commands.go`

Add to the command switch (around line 73):

```go
case "review_python":
    b.handleOrchestratorTeam(ctx, msg, "python-dev-team")

case "review_python_sec":
    b.handleOrchestratorTeam(ctx, msg, "python-sec-team")
```

Update the /start help text (around line 25) to include:

```
/review_python — Python dev team review
/review_python_sec — Python security team review
```

### Step 5: Add Language Detection

Create: `/workspace/projects/claude-gateway/internal/orchestrator/lang_detect.go`

```go
package orchestrator

import (
    "os"
    "path/filepath"
    "strings"
)

// DetectProjectLanguage scans a directory and returns the primary language(s)
// based on file extensions found.
func DetectProjectLanguage(projectDir string) []string {
    langCounts := make(map[string]int)

    filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() {
            return nil
        }

        ext := strings.ToLower(filepath.Ext(path))
        switch ext {
        case ".go":
            langCounts["go"]++
        case ".py":
            langCounts["python"]++
        case ".js", ".mjs":
            langCounts["javascript"]++
        case ".ts", ".tsx":
            langCounts["typescript"]++
        case ".kt", ".kts":
            langCounts["kotlin"]++
        }
        return nil
    })

    // Determine primary language(s)
    var languages []string
    maxCount := 0

    for lang, count := range langCounts {
        if count > maxCount {
            maxCount = count
            languages = []string{lang}
        } else if count == maxCount && count > 0 {
            languages = append(languages, lang)
        }
    }

    return languages
}

// GetReviewTeamForLanguages returns the appropriate review team based on detected languages
func GetReviewTeamForLanguages(languages []string, securityFocus bool) string {
    if len(languages) == 0 {
        return "go-dev-team" // default
    }

    primary := languages[0]

    if securityFocus {
        switch primary {
        case "go":
            return "go-sec-team"
        case "python":
            return "python-sec-team"
        case "javascript", "typescript":
            return "fe-sec-team"
        case "kotlin":
            return "kotlin-sec-team"
        default:
            return "go-sec-team"
        }
    }

    switch primary {
    case "go":
        return "go-dev-team"
    case "python":
        return "python-dev-team"
    case "javascript", "typescript":
        return "fe-dev-team"
    case "kotlin":
        return "kotlin-dev-team"
    default:
        return "go-dev-team"
    }
}
```

### Step 6: Create Auto-Review Command

Add to bot commands:

```go
case "autoreview":
    b.handleAutoReview(ctx, msg, false)

case "autoreview_sec":
    b.handleAutoReview(ctx, msg, true)
```

Add handler method:

```go
func (b *Bot) handleAutoReview(ctx context.Context, msg *tgbotapi.Message, securityFocus bool) {
    if b.orchestrator == nil {
        b.sendMessage(msg.Chat.ID, "Orchestrator not initialized.")
        return
    }

    projectDir := strings.TrimSpace(msg.CommandArguments())
    if projectDir == "" {
        projectDir = "/workspace" // default
    }

    // Detect language
    languages := orchestrator.DetectProjectLanguage(projectDir)
    if len(languages) == 0 {
        b.sendMessage(msg.Chat.ID, "Could not detect project language")
        return
    }

    teamName := orchestrator.GetReviewTeamForLanguages(languages, securityFocus)

    b.sendMessage(msg.Chat.ID, fmt.Sprintf(
        "Detected languages: %s\\nUsing team: %s",
        strings.Join(languages, ", "),
        teamName,
    ))

    task := fmt.Sprintf("Review the code in %s", projectDir)

    result, err := b.orchestrator.RunTeamStreaming(ctx, teamName, task,
        func(progress string) {
            b.sendMessage(msg.Chat.ID, progress)
        })
    if err != nil {
        b.sendMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err))
        return
    }

    report := orchestrator.FormatConsensusText(teamName, task, result.Consensus, result.Verdicts)
    b.sendLongMessage(msg.Chat.ID, report)
}
```

## Testing

### Test on Google Project

```bash
# Build the gateway
cd /workspace/projects/claude-gateway
go build ./...

# Start the gateway
./bin/gateway

# In Telegram, send:
/review_python Review the Flask app in /workspace/projects/google
```

Or use the auto-detect feature:

```bash
/autoreview /workspace/projects/google
```

## Verification

After installation, verify:

1. ✅ Python team agents exist in `/workspace/.claude/agents/coding/python-team/`
2. ✅ coding-agent.md has Python support
3. ✅ teams.go has python-dev-team and python-sec-team
4. ✅ Bot commands include /review_python and /review_python_sec
5. ✅ Language detection works: `DetectProjectLanguage("/workspace/projects/google")` returns `["python"]`
6. ✅ Auto-review command works on Python projects
