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
| **SaaS builder** | `@saas-builder` multi-role pipeline with project ledger under `<project>/saas-builder/` (see [example](#example-agent-saas-builder)) |
| **Agent creator** | `@agent-creator` scaffolds/edits other Telegram specialists and review teams (see [example](#example-agent-agent-creator)) |
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

## Example agent: `@agent-creator`

Specialists are folders under `.claude/agents/<handle>/`. Each has a DNA markdown file (and optional private `skill-*.md` files). The gateway registers them for Telegram as `@Handle`. **`@agent-creator`** is the meta-agent that **designs and writes those specialists** (and review teams) for you.

### What it is for

You talk to `@agent-creator` in Telegram when you want a new agent or to change an existing one. It does not run forex, email, or other product work unless you ask it to copy a pattern from an existing agent file. Its job is **scaffold and edit agent DNA**.

### What it can create

| Kind | Where it writes | How you use it |
|---|---|---|
| **Specialist agent** | `.claude/agents/<handle>/<handle>.md` (+ optional `skill-*.md`) | Message `@Handle` in Telegram |
| **Review team** | `.claude/teams/coding/<team-name>/*.md` | Orchestrator `/review…` commands |

Specialist DNA typically includes:

- YAML frontmatter: `name`, optional `project:` (pinned cwd — no “find the repo” hunting), optional `framework:` (`qwen` / `deepseek` / `grok` / `claude`)
- A bold project path on the first body line when the agent owns a repo
- **Conversation first** — chat without tools until the ask is concrete (file, bug, test, deploy)
- Hard rules (e.g. never commit/push/deploy or touch live money unless asked)

### How a session with it feels

```
You:    @agent-creator let's make an agent for client invoices
Agent:  Asks a few clarifying questions (handle, one-line job, project path, must-not-do)
You:    handle grey-invoices, project /workspace/projects/invoices, no deploy
Agent:  Writes .claude/agents/grey-invoices/grey-invoices.md
        (and a skill file if needed)
        Tells you to try @GreyInvoices — gateway watches the agents folder
```

Private skill example in that folder: `skill-agent-scaffold.md` (`command: agent-scaffold`) — the checklist it follows when scaffolding a new specialist.

### Why this example matters

`@agent-creator` shows the gateway’s agent model in one picture:

1. **Agents are files**, not only prompts baked into Go.
2. **Telegram `@handles`** map to folders the gateway syncs into Postgres.
3. **Pinned projects + frameworks** keep specialists on the right repo and model.
4. You can grow the system by **talking to an agent that writes other agents**, then immediately messaging the new `@Handle`.

Other specialists (`@forex-trading`, website agents, …) follow the same folder/DNA pattern as `@agent-creator`; they differ in job, project pin, and skills. **`@saas-builder` is a separate class** — see below.

---

## Example agent: `@saas-builder`

`@saas-builder` is the gateway’s **product-building pipeline agent**. Unlike most specialists, it is not pinned to one repo in YAML. You name the project directory and the kind of work; the gateway drives a **four-role loop** until that work is done (or you stop it).

Frontmatter: `class: class-saas-builder`. That class is unique — other agents keep the standard path (including Qwen). SaaS builder **refuses Qwen**; use DeepSeek or Grok (`/models`).

### What it is for

Build or extend a micro-SaaS (or any product tree under `/workspace/projects/`) with disciplined roles:

| Role | Job |
|---|---|
| **Intake** | Interview one question at a time; write `<project>/saas-builder/docs/scope.md` from existing specs + gaps |
| **Architecture** | Own architecture + backlog; issue task specs; integrate |
| **Coding** | Implement exactly one task spec; tests + result package |
| **Review** | Independently verify diff vs spec; verdict only — does not rewrite code |

The **gateway** is the driver (`next.sh` between roles). Each role runs in a **fresh sidecar session**. Progress lives in the **product**, not in the agent folder.

### Where files go

| Location | Contents |
|---|---|
| `<project>/` | Real product source (app code, README, …) |
| `<project>/saas-builder/` | Ledger, scope, handoffs, task notes, copied templates, optional `PLAN.md` |
| `.claude/agents/saas-builder/` | Role prompts + templates (**read/copy only** — never edit to fit a product) |

### How a session feels

```
You:    @saas-builder
        /workspace/projects/calendar
        a single feature

Agent:  Intake reads existing docs, fills saas-builder/docs/scope.md,
        asks one question about the first gap
        …

Gateway: Scope ready — start building?

You:    yes

Gateway: Architecture → Coding → Review (fresh sessions) until the
         named work is built (one slice for a single feature)
```

You can send directory + kind of work in one message. Kind of work options: entire project, single feature, refactor (same tree), plan-first, or a custom outcome.

### Control commands

| You send | What happens |
|---|---|
| `yes` / `/start_building` | Run Architecture → Coding → Review until the ledger is done |
| `no` | Keep scope on disk; do not build yet |
| `/progress` | Phase, repo, kind of work, slice, task, next actor, in-flight? |
| `/stop` | Pause; cancel in-flight role |
| `/resume` (or `/pipeline_resume`) | Continue the same project (does not skip gates or switch repos) |
| Another project directory after `/stop` or finish | Start/resume that project; previous `saas-builder/` left intact |

The loop also pauses on human gates, Intake returning, no progress twice, or 40 role steps — then `/resume` continues.

### Why this example matters

Together with `@agent-creator`:

1. **`@agent-creator`** *writes agents* (files under `.claude/agents/`).
2. **`@saas-builder`** *builds products* (files under a project tree) with a gated multi-role pipeline the gateway orchestrates.

That is the two-layer agent story: create specialists when you need new handles; use saas-builder when you need an entire feature or product built with intake → architecture → coding → review.

Full operator detail: [USER_GUIDE.md § SaaS builder](USER_GUIDE.md) · class note: [`docs/autonomous-agent-class.md`](docs/autonomous-agent-class.md).

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
