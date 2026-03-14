# Hooks Roadmap

Analysis of hook opportunities for the Claude Gateway system, based on patterns from production hook usage in the wild.

## Current State

- **SessionStart hook** — implemented. Injects bootstrap context (environment + personality) on startup, resume, compact, and clear events.
- **Context injection** — handled in Go via `BuildSystemPromptSection()` + `BuildEnrichedPrompt()`.
- **Workflow enforcement** — handled by the skills system (auto-detected, injected per-message).
- **Session continuity** — enriched prompt with recent turns + semantic search.

The Go gateway enriches *before* sending to the sidecar. Hooks run *during* the Claude session, intercepting tool calls in real-time. This is the key distinction — hooks catch problems at the point of action, not just at prompt composition time.

---

## Tier 1 — High Impact, Low Complexity

### 1. PostToolUse (Bash) — Retry Detector

Tracks the last 3 bash commands in a temp file. Blocks when Claude retries the same failing command 3+ times, forcing it to try a different approach.

**Why it matters:** Steve sends a Telegram message, Claude starts a long task, hits a failing command, and burns tokens retrying while Steve watches "Working..." for minutes. This prevents runaway token burns.

- Complexity: ~30 lines of shell + a temp file
- Token savings: Prevents runaway burns (potentially thousands of tokens)
- Hook type: `PostToolUse` with matcher `tool_name == "Bash"`

### 2. PostToolUse (Edit/Write) — Anti-Pattern Checker

Regex-based checker that runs after every file edit. Catches violations of project rules before they land. Patterns maintained in a JSON file for easy updates.

**Patterns to enforce:**
- `float` for financial math (must use `Decimal`)
- React/Vue/Angular/jQuery imports (vanilla JS only)
- CGO usage (`CGO_ENABLED`, `import "C"`)
- Framework detection in HTML/JS files
- Wrong import patterns in Go code
- Dependency direction violations

- Complexity: ~50 lines of shell + patterns.json
- Hook type: `PostToolUse` with matcher `tool_name in ["Edit", "Write"]`

### 3. PostToolUse (Edit/Write) — Compaction Shield

Tracks what Claude has edited during the session — appends `{file, line_range, action}` to a session-local manifest. When compaction fires, the SessionStart hook reads this manifest back so Claude doesn't lose track of what it was doing.

**Why it matters:** Telegram sessions can run very long (30-min TTL, multi-turn). When compaction hits, Claude loses track of what it edited. This prevents redone work, forgotten file changes, and incomplete implementations.

- Complexity: ~40 lines of shell
- Hook type: `PostToolUse` with matcher `tool_name in ["Edit", "Write"]`
- Pairs with: SessionStart hook (reads manifest back after compaction)

---

## Tier 2 — Medium Impact

### 4. Stop — Uncommitted Changes Guard

When Claude finishes a task, checks: are there modified files in `/workspace` that aren't committed? Are there files in `/workspace/tasks` that haven't been delivered? Warns before stopping.

- Complexity: ~20 lines of shell (git status + file checks)
- Hook type: `Stop`
- Prevents: Lost work, abandoned task files

### 5. PreToolUse (Read) — Large File Gate

Blocks reads of files over N lines and suggests `head -30` or a summary script instead. Prevents context window waste on large files (CHANGELOG, large configs, generated files).

- Complexity: ~25 lines of shell (wc -l check + redirect)
- Hook type: `PreToolUse` with matcher `tool_name == "Read"`
- Saves: Context window on oversized files

---

## Tier 3 — Nice to Have

### 6. UserPromptSubmit — Task Router

When Steve types task-like messages, inject structured metadata (project path, client name) before the skill fires. Lower priority since the skills system already covers intent detection and workflow injection.

### 7. PostToolUse (Edit/Write) — Test Suggester

After editing a production file, look up which tests cover it and suggest targeted test commands. Less relevant currently — the gateway doesn't have a large mapped test suite. Would become valuable if one is built.

---

## Implementation Order

1. **Retry Detector** — simplest, immediate payoff, prevents token waste
2. **Anti-Pattern Checker** — enforces non-negotiable coding constraints at the tool level
3. **Compaction Shield** — pairs with existing SessionStart hook for full compaction survival
4. **Stop Guard** — catches lost work
5. **Large File Gate** — context window optimisation

---

## Hook Configuration Reference

All hooks are configured in `configs/claude-code-settings.json` and copied into the container at `/root/.claude/settings.json`. Hook scripts live in `hooks/` and are copied to `/app/hooks/` in the Docker image.

```json
"hooks": {
  "SessionStart": [{ "hooks": [{ "type": "command", "command": "/app/hooks/session-start.sh", "timeout": 5 }] }],
  "PostToolUse": [
    { "matcher": { "tool_name": "Bash" }, "hooks": [{ "type": "command", "command": "/app/hooks/retry-detector.sh", "timeout": 3 }] },
    { "matcher": { "tool_name": ["Edit", "Write"] }, "hooks": [{ "type": "command", "command": "/app/hooks/anti-pattern-check.sh", "timeout": 3 }] },
    { "matcher": { "tool_name": ["Edit", "Write"] }, "hooks": [{ "type": "command", "command": "/app/hooks/compaction-shield.sh", "timeout": 3 }] }
  ],
  "Stop": [{ "hooks": [{ "type": "command", "command": "/app/hooks/uncommitted-guard.sh", "timeout": 5 }] }],
  "PreToolUse": [
    { "matcher": { "tool_name": "Read" }, "hooks": [{ "type": "command", "command": "/app/hooks/large-file-gate.sh", "timeout": 3 }] }
  ]
}
```

---

*Created: 2026-03-14*
