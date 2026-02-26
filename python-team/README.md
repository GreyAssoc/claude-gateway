# Python Team for Claude Gateway

This directory contains the Python review team implementation for the Claude Gateway orchestrator.

## Quick Start

```bash
# Test the implementation
bash test.sh

# Deploy to production
bash deploy.sh

# Restart gateway to load new teams
cd /workspace/projects/claude-gateway
docker-compose restart gateway
```

## What's Included

### Agent DNA Files (5)

1. **python-purist.md** (8.3KB) - Tier 4
   - PEP 8 enforcement
   - Type hints (PEP 484)
   - Pythonic idioms
   - Documentation (PEP 257)
   - Standard library usage

2. **python-pragmatist.md** (8.0KB) - Tier 4
   - Simplicity over complexity
   - Code duplication detection
   - Dead code removal
   - Stdlib-first approach
   - Minimum viable complexity

3. **python-pessimist.md** (9.6KB) - Tier 4
   - Defensive coding
   - Error handling
   - Edge case coverage
   - Resource management
   - Thread safety

4. **python-security-agent.md** (12.3KB) - Tier 1 (VETO POWER)
   - SQL injection prevention
   - Command injection prevention
   - Path traversal protection
   - Secrets management
   - Deserialization safety
   - Input validation
   - Cryptography best practices

5. **python-hacker-agent.md** (16.5KB) - Tier 2 (CRITICAL)
   - Adversarial testing
   - Pickle exploitation
   - SSTI attacks
   - XXE, ReDoS, SSRF
   - Timing attacks
   - Race conditions

### Coding Agent Enhancement

**python-coding-agent-section.md** (20.2KB)
- Python-specific fix patterns
- Type hints and annotations
- Context managers
- Exception handling
- Security patterns (SQL, path traversal, command injection)
- Testing with pytest
- Build commands
- Anti-patterns

### Infrastructure

**Orchestrator Updates:**
- `internal/orchestrator/teams.go` - Team definitions
- `internal/orchestrator/language.go` - Language detection
- `internal/orchestrator/language_test.go` - Tests

**Bot Updates:**
- `internal/bot/commands.go` - Added /review_python and /review_python_sec

**Documentation:**
- `README.md` - This file
- `IMPLEMENTATION_SUMMARY.md` - Comprehensive implementation guide
- `test.sh` - Test script
- `deploy.sh` - Deployment script

## Architecture

```
┌─────────────────────┐
│   User Request      │
│  (via Bot/API)      │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ Language Detection  │
│   (.py files)       │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│  Select Teams       │
│  - python-dev-team  │
│  - python-sec-team  │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│   Run Agents in     │
│   Parallel/Series   │
├─────────────────────┤
│ T4: Purist          │
│ T4: Pragmatist      │
│ T4: Pessimist       │
│ T1: Security (VETO) │
│ T2: Hacker          │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│  Consensus Engine   │
│  (Weighted Votes)   │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│  Result             │
│  APPROVE/REJECT/    │
│  NEEDS_CHANGES      │
└─────────────────────┘
```

## Agent Tiers

| Tier | Weight | Veto | Agents |
|------|--------|------|--------|
| 1 | 1000 | ✅ | PythonSecurity |
| 2 | 100 | ❌ | PythonHacker |
| 4 | 1 | ❌ | PythonPurist, PythonPragmatist, PythonPessimist |

## Usage Examples

### Via Telegram Bot

```
/review_python Review the Flask app in /workspace/projects/google/app.py

/review_python_sec Check /workspace/projects/google/app.py for security vulnerabilities

/gauntlet Run all teams on the codebase
```

### Programmatic

```go
import "github.com/you/claude-gateway/internal/orchestrator"

// Auto-detect language and get teams
files := []string{"app.py", "utils.py"}
teams := orchestrator.GetReviewTeamsForFiles(files)
// Returns: ["python-dev-team", "python-sec-team"]

// Run review
engine := orchestrator.NewEngine(agentClient, dnaDir, workspaceDir)
result, err := engine.RunTeam(ctx, "python-dev-team", "Review these files")

// Check consensus
if result.Consensus.Approved {
    log.Println("All agents approved!")
} else {
    for _, issue := range result.Consensus.Issues {
        log.Printf("%s: %s at %s:%d",
            issue.Severity, issue.Description, issue.File, issue.Line)
    }
}
```

## Language Detection

Supported extensions:
- `.py` → Python → python-dev-team, python-sec-team

Detection functions:
```go
// Single file
lang := orchestrator.DetectLanguage("app.py")  // LanguagePython

// Multiple files
counts := orchestrator.DetectLanguages([]string{"app.py", "utils.py"})
// Returns: map[LanguagePython:2]

// Primary language
primary := orchestrator.GetPrimaryLanguage(files)  // LanguagePython

// Get teams
teams := orchestrator.GetReviewTeams(LanguagePython)
// Returns: ["python-dev-team", "python-sec-team"]

// Multi-language projects
allTeams := orchestrator.GetAllReviewTeamsForFiles([]string{
    "main.go", "app.py", "utils.js"
})
// Returns: ["go-dev-team", "go-sec-team", "python-dev-team",
//           "python-sec-team", "fe-dev-team", "fe-sec-team"]
```

## Testing

```bash
# Run all tests
bash test.sh

# Run specific tests
cd /workspace/projects/claude-gateway
go test ./internal/orchestrator/language_test.go -v

# Test build
go build ./cmd/gateway

# Test on Google project
/review_python Review /workspace/projects/google/app.py
```

## Expected Agent Behavior

### PythonPurist Will Flag:
- ❌ Missing type hints on public functions
- ❌ Bare `except:` clauses
- ❌ Using `os.path` instead of `pathlib`
- ❌ Missing docstrings
- ❌ Non-PEP 8 naming (camelCase variables)
- ❌ `print()` instead of `logging`
- ✅ Accepts: Public attributes, dataclasses, comprehensions

### PythonPragmatist Will Flag:
- ❌ Duplicate code across files
- ❌ Unnecessary abstraction layers
- ❌ Hand-rolled stdlib reimplementations
- ❌ Unused imports/variables
- ❌ Over-engineered solutions
- ✅ Accepts: Direct code, simple solutions, working implementations

### PythonPessimist Will Flag:
- ❌ Missing None checks
- ❌ No error handling
- ❌ Files not using context managers
- ❌ Edge cases not handled (empty lists, large inputs)
- ❌ Thread safety issues
- ✅ Accepts: Defensive code, comprehensive error handling

### PythonSecurity Will VETO:
- 🚫 SQL injection via f-strings in queries
- 🚫 Command injection (`shell=True`)
- 🚫 Path traversal vulnerabilities
- 🚫 Hardcoded secrets
- 🚫 `pickle.load()` on user data
- 🚫 Missing input validation

### PythonHacker Will Flag (Critical):
- 🔴 Exploitable pickle deserialization
- 🔴 SSTI in Jinja2 templates
- 🔴 XXE in XML parsing
- 🔴 ReDoS regex patterns
- 🔴 Timing attack vulnerabilities
- 🔴 Race condition exploits

## Common Review Patterns

### Type Hints
```python
# Will be flagged
def process(data):
    return data.upper()

# Approved
def process(data: str) -> str:
    """Process input string."""
    return data.upper()
```

### Context Managers
```python
# Will be flagged
f = open('file.txt')
data = f.read()
f.close()

# Approved
with open('file.txt') as f:
    data = f.read()
```

### SQL Injection
```python
# WILL BE REJECTED (Security VETO)
sql = f"SELECT * FROM users WHERE id = {user_id}"
db.execute(sql)

# Approved
sql = "SELECT * FROM users WHERE id = ?"
db.execute(sql, (user_id,))
```

### Path Traversal
```python
# WILL BE REJECTED (Security VETO)
path = f"/uploads/{filename}"
with open(path) as f:
    data = f.read()

# Approved
from pathlib import Path
base = Path("/uploads").resolve()
file_path = (base / filename).resolve()
if file_path.is_relative_to(base):
    data = file_path.read_text()
```

## Troubleshooting

### Teams Not Found
```bash
# Check team definitions
grep "python-dev-team" /workspace/projects/claude-gateway/internal/orchestrator/teams.go

# Rebuild
cd /workspace/projects/claude-gateway
go build ./cmd/gateway
```

### Agent DNA Files Not Loading
```bash
# Check files exist
ls -la /workspace/.claude/agents/coding/python-team/

# Check permissions
chmod 644 /workspace/.claude/agents/coding/python-team/*.md
```

### Language Detection Not Working
```bash
# Run tests
go test ./internal/orchestrator/language_test.go -v

# Check file extensions
echo "app.py" | grep '\.py$'  # Should match
```

### Bot Commands Not Responding
```bash
# Check bot command registration
grep "review_python" /workspace/projects/claude-gateway/internal/bot/commands.go

# Restart bot
docker-compose restart gateway
```

## Performance

- **Language Detection**: O(n) file scan, ~1ms for 1000 files
- **Agent Execution**: Parallel where possible, ~5-30s per agent
- **Team Review**: ~15-90s depending on agent count and file size
- **Consensus**: O(n) agents, ~1ms

## Limitations

- Language detection is file extension-based (no content analysis)
- Agents run in isolation (can't see each other's output during review)
- VETO power from Tier 1 agents overrides all others
- Maximum review file size: 100KB per agent DNA file

## Roadmap

### Phase 1 (Complete)
- ✅ Python agent DNA files
- ✅ Language detection
- ✅ Team definitions
- ✅ Bot commands
- ✅ Tests

### Phase 2 (Next)
- [ ] Autofix loop implementation
- [ ] Multi-round review with coding-agent
- [ ] Convergence detection

### Phase 3 (Future)
- [ ] Django-specific team
- [ ] FastAPI-specific team
- [ ] Data science team (pandas, numpy)
- [ ] Async/await patterns team

## Contributing

To add new Python agents:

1. Create DNA file in `/workspace/.claude/agents/coding/python-team/`
2. Follow existing agent structure (Identity, Principles, Examples, Output Format)
3. Add agent to team in `internal/orchestrator/teams.go`
4. Write tests
5. Update documentation

## References

- [PEP 8 -- Style Guide for Python Code](https://peps.python.org/pep-0008/)
- [PEP 257 -- Docstring Conventions](https://peps.python.org/pep-0257/)
- [PEP 484 -- Type Hints](https://peps.python.org/pep-0484/)
- [Python Security Best Practices](https://cheatsheetseries.owasp.org/cheatsheets/Python_Cheat_Sheet.html)
- [OWASP Top 10](https://owasp.org/www-project-top-ten/)

## License

Same as claude-gateway project.

## Support

For issues, contact the gateway maintainers or check the main project documentation.
