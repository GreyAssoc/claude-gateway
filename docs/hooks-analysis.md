# Claude Code Hooks Analysis & Recommendations

**Date:** 2026-03-14
**Source:** Reddit user tips + claude-gateway codebase comparison
**Purpose:** Identify hook improvements and new hook opportunities

---

## Current Hook Inventory

| Hook | File | Event | Purpose |
|------|------|-------|---------|
| Context Injection | `session-start.sh` | SessionStart | Injects `environment.md` + `personality.md` + edit manifest |
| Anti-Pattern Checker | `anti-pattern-check.sh` | PostToolUse (Edit/Write) | Regex validation via `patterns.json` |
| Compaction Shield | `compaction-shield.sh` | PostToolUse (Edit/Write) | Tracks edits to `/tmp/claude-edit-manifest.md` |
| Retry Detector | `retry-detector.sh` | PostToolUse (Bash) | Blocks after 3 consecutive failures of same command |

**Config:** `configs/claude-code-settings.json`
**Hook scripts:** `hooks/`

---

## Reddit User's Hook System (Reference)

A Reddit user shared their hook architecture which achieves a **27x context compression ratio** (1,508 lines consumed vs 41,220 lines raw) using hooks + custom context tools. Their system:

| Hook | Event | What It Does |
|------|-------|-------------|
| Context Injection | SessionStart | Injects ~50 lines of critical context: project rules, task state, architecture constraints, codebase health scores |
| Anti-Pattern Checker | PostToolUse (Edit/Write) | Regex patterns against changed code, blocks violations in <50ms |
| Test Impact Suggester | PostToolUse (Edit/Write) | Looks up pre-computed test impact graph, suggests targeted test commands |
| Compaction Shield | PostToolUse (Edit/Write) | AST diffing tracks what functions were added/modified/removed, survives compaction |
| Uncommitted Changes Guard | Stop | Checks for active tasks without closure and uncommitted changes |
| Context-First Gate | PreToolUse (Read) | Blocks reading large files directly, redirects to context scripts returning compact summaries |
| Retry Detector | PostToolUse (Bash) | Detects 3+ retries of same failing command, interrupts with "try different approach" |
| Workflow Enforcer | UserPromptSubmit | Intercepts task IDs, injects structured workflow instructions |

**Key insight:** Hooks + custom scripts + skills = amazing combination. Skills are optional (agent can ignore), but hooks pester the agent until it corrects course.

---

## Comparison: What We Have vs Reddit

| Hook | Reddit | Us | Gap |
|------|--------|-----|-----|
| SessionStart — Context Injection | ✅ ~50 lines + task state + health scores | ✅ environment.md + personality.md + edit manifest | **Ours is simpler** — no task state, no health scores |
| Anti-Pattern Checker | ✅ Regex patterns, <50ms | ✅ patterns.json, regex against Edit/Write | **Parity** |
| Compaction Shield | ✅ AST diffing (function-level) | ✅ File-level tracking (timestamp + path + line counts) | **Theirs is deeper** — AST-level vs file-level |
| Retry Detector | ✅ 3+ retries → interrupt | ✅ 3 consecutive failures → block | **Parity** |
| Test Impact Suggester | ✅ Pre-computed test graph | ❌ Not implemented | **Gap** |
| Uncommitted Changes Guard | ✅ Checks tasks + uncommitted changes on Stop | ❌ Not implemented | **Gap — HIGH RISK** |
| Context-First Gate | ✅ Blocks large reads, returns compact summaries | ❌ Not implemented | **Gap** |
| Workflow Enforcer | ✅ Task ID → structured workflow | ❌ Not implemented | **Gap** |

---

## Recommendations: From Reddit (Not Yet Implemented)

### 1. Stop — Uncommitted Changes Guard (HIGH PRIORITY)

**Problem:** Claude stops mid-task, changes are orphaned. The sidecar spawns isolated CLI processes for `/gauntlet` and `/auto-review` — if those stop mid-edit, work is lost.

**Critical design constraint:** There is NO single repo at `/workspace`. Each project under `/workspace/projects/` has its own independent git repo. The hook MUST be project-aware:

1. Determine which project(s) Claude edited during this session (from the edit manifest)
2. For each affected project, `cd` into that project's directory and run `git status`
3. Only report uncommitted changes for the project(s) actually worked on
4. Never assume a repo exists at workspace root

**Implementation:**
```bash
# hooks/stop-guard.sh (Stop event)
#
# 1. Read /tmp/claude-edit-manifest.md to find which files were edited
# 2. Extract unique project directories from file paths:
#    /workspace/projects/claude-gateway/internal/foo.go → claude-gateway
#    /workspace/projects/ptp-dev/cmd/main.go → ptp-dev
# 3. For each project dir that is a git repo:
#    cd /workspace/projects/$PROJECT && git status --porcelain
# 4. If any project has uncommitted changes, output warning:
#    "UNCOMMITTED CHANGES in $PROJECT: N files modified, M untracked.
#     Commit or stash before stopping."
# 5. Also check for active edit manifest entries without completion
# 6. Also check for running background processes (orchestrator agents)
#
# Output warning JSON if any issues found
```

**Example output:**
```
WARNING: Uncommitted changes detected before stop:

[claude-gateway] 3 files modified, 1 untracked:
  M internal/bot/bot.go
  M internal/bot/commands.go
  M configs/gateway.yaml
  ? internal/bot/new_handler.go

Commit these changes or explain why they should be left uncommitted.
```

**Expected benefit:** Eliminates "Claude just stopped mid-task and I lost work" problem. Works correctly with the multi-repo workspace layout.

### 2. PreToolUse (Read) — Context-First Gate (HIGH PRIORITY)

**Problem:** Codebase has 15,390 lines of Go + 460 lines of Node.js. Large files like `commands.go` (~1000 lines), `bot.go` (~600 lines), `builder.go` (~350 lines) eat context fast.

**Implementation:**
```bash
# hooks/context-gate.sh (PreToolUse Read)
# For files over N lines (e.g., 300), inject a reminder:
# "This file is X lines. Consider using offset/limit for targeted reads."
# For forbidden large files (CHANGELOG, etc.), block entirely.
```

**Expected benefit:** Context conservation, especially important in long sessions.

### 3. UserPromptSubmit — Workflow Enforcer (MEDIUM)

**Problem:** When Claude Code runs directly in the sidecar container, there's no equivalent to the ctxbuilder's context assembly.

**Implementation:**
```bash
# hooks/workflow-enforcer.sh (UserPromptSubmit)
# Detect patterns:
# - Task-like messages → inject reminder to read relevant CLAUDE.md
# - Project references → inject project isolation warning
# - Orchestrator commands → inject proper workflow steps
```

**Expected benefit:** Structured task execution instead of ad-hoc diving in.

### 4. PostToolUse (Edit/Write) — Test Impact Suggester (LOW — build later)

**Problem:** "Run all tests" is slow, Claude guessing which tests is unreliable.

**Implementation:** Create a mapping file (`test-impact.json`) from source files to test files. After edits, suggest targeted `go test` commands.

**Expected benefit:** Faster test feedback loops. Low priority until test coverage grows.

---

## Outside-the-Box Hook Ideas (Claude-Gateway Specific)

### 5. PreToolUse (Bash + Read) — Project Isolation Enforcer (CRITICAL)

**Problem:** The workspace's #1 rule is project isolation. Currently enforced only by CLAUDE.md instructions — Claude can still accidentally cross boundaries.

**Implementation:**
```bash
# hooks/isolation-enforcer.sh (PreToolUse Bash + Read)
# Track current working project directory
# Detect if command/read references files in a DIFFERENT project
# Example: working in claude-gateway/ but reading ptp-dev/internal/...
# Action: block with "ISOLATION VIOLATION: You are working on claude-gateway
#         but this command accesses ptp-dev"
```

**Expected benefit:** Mechanical enforcement of the most critical workspace rule. No more relying on Claude "remembering" the rule.

### 6. PostToolUse (Bash) — Docker Safety Net (HIGH)

**Problem:** The sidecar runs Docker commands. Accidental container destruction is catastrophic.

**Implementation:**
```bash
# hooks/docker-safety.sh (PostToolUse Bash)
# Catch and warn/block:
# - docker rm / docker stop on gateway or sidecar containers
# - docker-compose down when there are active sessions
# - docker system prune
# - docker volume rm on gateway-data
```

**Expected benefit:** Prevents accidental self-destruction of the running system.

### 7. PostToolUse (Edit) — Go Compilation Check (HIGH)

**Problem:** Syntax errors or type mismatches in Go code aren't caught until `make build`, which may be much later.

**Implementation:**
```bash
# hooks/go-compile-check.sh (PostToolUse Edit/Write)
# After editing .go files, run quick `go vet ./...` or `go build ./...`
# If it fails, inject the error immediately
# Timeout: 5s (compilation is fast for this codebase size)
```

**Expected benefit:** Catch compile errors in <5s instead of discovering them minutes later. Much faster than the Reddit user's test impact suggester.

### 8. PostToolUse (Bash) — Client Directory Write Guard (HIGH)

**Problem:** `/clients/current` and `/clients/past` are mounted read-only, but Claude could try creative write approaches.

**Implementation:**
```bash
# hooks/client-guard.sh (PreToolUse Bash + Write)
# Detect any write attempts to /clients/ paths
# Block with: "Client directories are READ-ONLY. Copy files to
#             /workspace/tasks/ before working on them."
```

**Expected benefit:** Enforces read-only client data rule mechanically.

### 9. SessionStart — Active Task State Injection (MEDIUM)

**Problem:** Current session-start only injects static bootstrap files. No dynamic state.

**Implementation:**
```bash
# Enhanced session-start.sh:
# 1. Query SQLite for last session's edit manifest and unfinished work
# 2. Check git status in /workspace/projects/claude-gateway/ for uncommitted changes
# 3. Include current cron job statuses
# 4. Inject rate limiter headroom so Claude knows its token budget
# 5. Show active task list if any
```

**Expected benefit:** Claude starts every session (and every compaction recovery) with full situational awareness.

### 10. PostToolUse (Bash) — gog Command Auditor (MEDIUM)

**Problem:** Claude can send emails, create calendar events, etc. via gog CLI. No audit trail beyond container logs.

**Implementation:**
```bash
# hooks/gog-auditor.sh (PostToolUse Bash)
# Detect all gog commands in bash output
# Log to /tmp/gog-audit.log with timestamp, command, and result
# For email sends: log recipient + subject
# For calendar creates: log event title + time
```

**Expected benefit:** Full audit trail of all external actions. Useful for debugging and accountability.

### 11. PreToolUse (Write) — Backup Critical Files (MEDIUM)

**Problem:** Overwriting key files (`gateway.yaml`, `.env`, `docker-compose.yml`, `index.js`, `main.go`) could be catastrophic.

**Implementation:**
```bash
# hooks/backup-critical.sh (PreToolUse Write)
# Critical files list: gateway.yaml, .env, docker-compose.yml,
#                      sidecar/index.js, cmd/gateway/main.go
# Before overwrite: cp file /tmp/claude-backups/filename.timestamp
# Max 5 backups per file (rotate oldest)
```

**Expected benefit:** Cheap insurance against destructive overwrites. Easy rollback.

### 12. PostToolUse (Bash) — Build/Deploy Gate (LOW)

**Problem:** Claude runs `make build`, `make up`, or deployment commands and assumes success without verification.

**Implementation:**
```bash
# hooks/deploy-gate.sh (PostToolUse Bash)
# Detect: make build, make up, docker-compose up, deployment commands
# Inject reminder: "Verify build succeeded. Check logs with: make logs"
```

**Expected benefit:** Prevents "I deployed it" when the build actually failed.

### 13. Stop — Session Summary to Memory (LOW)

**Problem:** When Claude's session ends, context is lost until nightly compaction.

**Implementation:**
```bash
# hooks/session-summary.sh (Stop event)
# Auto-generate compact summary of what was done
# Write to daily memory directory or append to edit manifest
# Feeds into memory compactor's nightly job
```

**Expected benefit:** No work is lost between sessions, even before nightly compaction runs.

---

## Implementation Priority

### Phase 1 — Immediate (High ROI, Low Effort)

| # | Hook | Event | Risk Mitigated |
|---|------|-------|----------------|
| 1 | Uncommitted Changes Guard | Stop | Lost work |
| 2 | Project Isolation Enforcer | PreToolUse (Bash/Read) | Cross-project contamination |
| 3 | Backup Critical Files | PreToolUse (Write) | Destructive overwrites |

### Phase 2 — Next Sprint (Medium Effort, High Value)

| # | Hook | Event | Benefit |
|---|------|-------|---------|
| 4 | Go Compilation Check | PostToolUse (Edit/Write) | Instant error feedback |
| 5 | Enhanced Session Start | SessionStart | Richer context on resume |
| 6 | gog Command Auditor | PostToolUse (Bash) | Audit trail for external actions |
| 7 | Docker Safety Net | PostToolUse (Bash) | Prevent self-destruction |
| 8 | Client Directory Write Guard | PreToolUse (Bash/Write) | Enforce read-only client data |

### Phase 3 — Build When Needed

| # | Hook | Event | Benefit |
|---|------|-------|---------|
| 9 | Workflow Enforcer | UserPromptSubmit | Structured task execution |
| 10 | Context-First Gate | PreToolUse (Read) | Context conservation |
| 11 | Test Impact Suggester | PostToolUse (Edit/Write) | Targeted test runs |
| 12 | Build/Deploy Gate | PostToolUse (Bash) | Deployment verification |
| 13 | Session Summary to Memory | Stop | Auto-documentation |

---

## Key Takeaways from Reddit User

1. **Hooks > Skills for enforcement** — Skills are suggestions Claude can ignore. Hooks pester the agent until it corrects course.
2. **Context compression is massive** — Their system achieved 27x compression (1,508 lines vs 41,220 lines raw reads) with equivalent knowledge gaps.
3. **Categorise files** — Production files, config files, large files, forbidden files — each category gets different hook treatment.
4. **AST-level tracking beats file-level** — Their compaction shield uses AST diffing to track function-level changes, not just "file was edited".
5. **The hook→script→context pipeline** — Hooks trigger scripts that return curated context. This is more powerful than hooks alone.
6. **Failure escalation** — Detect repeated failures → warn → stop process. Prevents burning tokens on loops.

---

## Files to Create/Modify

### New Hook Scripts
- `hooks/stop-guard.sh` — Uncommitted changes guard
- `hooks/isolation-enforcer.sh` — Project isolation enforcement
- `hooks/backup-critical.sh` — Critical file backup
- `hooks/go-compile-check.sh` — Go compilation validation
- `hooks/docker-safety.sh` — Docker command safety
- `hooks/client-guard.sh` — Client directory write protection
- `hooks/gog-auditor.sh` — gog command audit trail
- `hooks/context-gate.sh` — Large file read gate

### Modified Files
- `configs/claude-code-settings.json` — Register new hooks
- `hooks/session-start.sh` — Enhanced with dynamic state injection

### New Support Files
- `hooks/critical-files.txt` — List of files requiring backup before overwrite
- `hooks/test-impact.json` — Source-to-test file mapping (Phase 3)
