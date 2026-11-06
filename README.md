# Claude Gateway v2

**Your self-hosted AI chief of staff — always on, always learning, always yours.**

Claude Gateway turns Claude (and compatible models) into a **persistent, context-aware assistant** you reach through **Telegram**. It remembers conversations, learns preferences, watches email and calendar, runs scheduled jobs, reviews code with multi-agent teams, and works on your projects — on **your** infrastructure.

| | |
|---|---|
| **Stack** | Go gateway + Node.js sidecar · PostgreSQL + pgvector · Ollama / vLLM · Docker Compose |
| **Primary interface** | Telegram bot |
| **Auth** | Claude subscription OAuth (plus DeepSeek / Grok / local Qwen routes) |
| **Docs** | [USER_GUIDE.md](USER_GUIDE.md) · [PITCH.md](PITCH.md) · [DOCKER.md](DOCKER.md) · [CLAUDE.md](CLAUDE.md) |

---

## What this project will ultimately be

When complete, Claude Gateway is a **personal operating layer** between you and AI:

1. **One place to talk** — Telegram (and a small web dashboard) instead of a dozen chat tabs.
2. **Memory that survives** — conversations, facts, people, journals, and profiles in PostgreSQL with vector search — across days and months, not one browser session.
3. **Tools with guardrails** — Gmail, Calendar, Drive, Sheets, web, and workspace files, with deny rules so destructive actions stay blocked.
4. **Automation that runs itself** — cron briefings, watchers, skills, and specialist `@agents` that keep working while you are away.
5. **Multi-model routing** — Claude when you need it; DeepSeek, Grok, or local Qwen when cost/latency matter; same skills and session patterns.
6. **Safer long sessions** — auto-compaction, `/stop`, progress for concurrent agents, and (roadmap) hooks that catch bad tool loops mid-flight.
7. **Agentic build workflows** — e.g. `@saas-builder` intake → architecture → coding → review against a real project directory.

The long-term product is not “a Claude wrapper.” It is a **self-hosted, memory-backed agent runtime** you control end to end.

---

## What has been built

### Runtime architecture

```
Telegram User
    ↓
Go Gateway (dashboard :8080)
├── Telegram bot + commands
├── Triage (local LLM when available)
├── Context builder (memory, people, skills, profile)
├── Rate limiter (RPM / input / output tokens)
├── Cron scheduler
├── Gmail & Calendar watchers
├── Multi-agent orchestrator (code review / autofix)
├── Named specialists (@agents) + saas-builder pipeline
└── Web dashboard
    ↓
Node.js Sidecar (:3001)
├── Claude Agent SDK (/ask, sessions, tools, MCP)
├── Multi-provider Anthropic-compatible routes
├── Auto-compaction + /compact
└── /run-agent, /reset, /health
    ↓
PostgreSQL + pgvector   ·   Ollama / vLLM (host)   ·   Google OAuth (optional)
```

Two Compose services (`gateway`, `sidecar`) plus host PostgreSQL and optional local LLM.

### Capabilities already in place

| Area | What you get |
|---|---|
| **Persistent memory** | Bootstrap + curated + daily layers; semantic search; session continuity |
| **People / contacts** | Fuzzy + semantic match; auto-injected when you mention someone |
| **Profile / voice** | Writing-style docs condensed into aspects; full doc for email drafts |
| **Daily journal** | AI summaries of the day; searchable later |
| **Cron** | Morning briefing, EOD, weekly review; `/cronadd` from Telegram; YAML for durable jobs |
| **Watchers** | Gmail (~2 min) and Calendar (~5 min) notifications with rate-limit respect |
| **Google Workspace** | Gmail / Calendar / Drive / Sheets via your OAuth; delete/trash denied; Drive mostly read-only |
| **Web** | WebSearch, WebFetch, Playwright |
| **Code review** | Multi-agent teams (purist / pragmatist / pessimist / security veto / hacker); `/review_*`, `/autoreview`, `/autofix` |
| **Skills** | Hot-loaded markdown skills (`harness/skills`, `skills/`); chainable |
| **Local triage** | Ollama/vLLM pre-screen to save tokens; graceful degrade if local LLM is down |
| **Multi-provider** | `claude`, `deepseek`, `grok`, `qwen` (default often Qwen); per-agent `/models` / YAML `framework:` |
| **Session control** | `/stop`, progress (`/progress`, `/who`), auto-compaction at context threshold |
| **SaaS builder** | `@saas-builder` multi-role pipeline with project ledger under `<project>/saas-builder/` |
| **Install** | Interactive `install.sh` wizard (Linux / macOS / Windows 11 WSL2) + `make` / Compose |

### Repo layout (high level)

| Path | Role |
|---|---|
| `cmd/gateway` | Gateway entrypoint |
| `internal/` | Bot, cron, memory, orchestrator, watchers, dashboard, DB, agents, … |
| `sidecar/` | Express + Claude Agent SDK |
| `configs/` | `gateway.yaml`, Claude/MCP settings |
| `memory/` | Bootstrap personality/environment; daily journals |
| `harness/skills/`, `skills/` | Skill prompts |
| `python-team/`, review teams | Multi-agent review personas |
| `hooks/` | Hook context / SessionStart-related material |
| `db/` | SQL init / migrations |
| `docs/` | Hooks analysis & roadmap, autonomous-agent notes |
| `USER_GUIDE.md` | Full operator manual |
| `PITCH.md` | Feature narrative |
| `DOCKER.md` | Container ops |

---

## What is left to build

Primary open work is documented in [`docs/hooks-roadmap.md`](docs/hooks-roadmap.md): **runtime hooks** that intercept tool use *during* a session (the gateway already enriches prompts *before* the sidecar).

### Tier 1 — high impact
| Item | Intent |
|---|---|
| **PostToolUse (Bash) retry detector** | Stop burning tokens on the same failing command |
| **PostToolUse anti-pattern checker** | Block known bad edits (e.g. float money, CGO, forbidden frameworks) |
| **Compaction shield** | Remember what was edited across auto-compact |

### Tier 2
| Item | Intent |
|---|---|
| **Stop — uncommitted changes guard** | Warn before ending with dirty workspace / undelivered tasks |
| **PreToolUse large-file gate** | Avoid dumping huge files into context blindly |

### Tier 3
| Item | Intent |
|---|---|
| **UserPromptSubmit task router** | Stronger routing at prompt submit |
| **Test suggester after edits** | Nudge tests when code changes |

### Other product gaps (from ops reality)
- Harden multi-provider defaults and key rotation docs (keys live in `.env`; never commit them).
- Module path in `go.mod` is still a placeholder (`github.com/you/claude-gateway`) — align with the real GitHub remote when publishing.
- Broader autonomous-agent class work: see [`docs/autonomous-agent-class.md`](docs/autonomous-agent-class.md).
- Continuous polish on rate limits, concurrent specialists, and dashboard observability.

---

## Quick start

```bash
git clone <this-repo>
cd claude-gateway

# Recommended: interactive installer
bash install.sh

# Or manual:
make setup          # creates .env template
# edit .env — TELEGRAM_BOT_TOKEN, ALLOWED_USER_IDS, DB, model keys, etc.
# init Postgres (see USER_GUIDE.md)
make build && make up
```

**Prerequisites:** Docker Compose · Telegram bot token · Claude subscription (OAuth) · PostgreSQL + pgvector · optional Ollama/vLLM on the host.

Full setup, commands, and troubleshooting: **[USER_GUIDE.md](USER_GUIDE.md)**.  
Compose rebuild/restart notes: **[DOCKER.md](DOCKER.md)** and **[CLAUDE.md](CLAUDE.md)**.

```bash
make build && make up
make logs
make down
```

---

## Everyday usage (Telegram)

```
# Talk naturally — memory + people + skills inject automatically
Draft an email to Sarah about the invoice

# Control
/stop
/compact
/who
/progress
/progress all

# Models (per focused agent)
/models deepseek
/models qwen
/models inherit

# Cron
/cronlist
/cronadd standup "30 9 * * 1-5" Check calendar and summarise today's meetings

# Code review
/review_go Review the auth middleware
/autoreview
/autofix

# Specialists
@forex-trading …
@saas-builder …
```

Named agents, SaaS builder flow, Google permissions, and rate limits are detailed in the user guide (§3–§16).

---

## Configuration (no secrets in git)

| File | Purpose |
|---|---|
| `.env` | Secrets and runtime env (**gitignored** — never commit) |
| `configs/gateway.yaml` | Cron, watchers, rate limits, memory |
| `memory/bootstrap/*.md` | Personality + environment injected every request |
| `configs/claude-code-settings.json` | MCP / Claude Code settings |

Rotate API keys in `.env`. Prefer Compose reading the file over exporting keys in the host shell (host env wins over `.env` and has caused stale-key 401s before).

---

## Product principles

- **Self-hosted** — your boxes, your Postgres, your OAuth.
- **Memory is a feature** — not an afterthought chat log.
- **Safety by default** — technical deny rules + bootstrap instructions (e.g. no Gmail delete/trash).
- **Cost awareness** — local triage, rate limits, cancel, compaction.
- **Skills over hardcoding** — drop markdown to teach new workflows.
- **Multi-model** — same gateway, pick the model that fits the job.

---

## Status

Active personal / team infrastructure product (v2). Primary documentation for operators is `USER_GUIDE.md`; this README is the GitHub front door — vision, shipped surface, and remaining work. Deep feature narrative: `PITCH.md`. Hook backlog: `docs/hooks-roadmap.md`.
