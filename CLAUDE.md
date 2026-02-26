# Claude Gateway v2

## Project Overview

A Go + Node.js gateway that connects Claude to Telegram, with scheduled jobs, Gmail/Calendar watchers, memory management, and multi-agent orchestration.

## Architecture

```
Telegram User
    ↓
Go Gateway (port 8080 dashboard)
├── Telegram Bot (polling)
├── Rate Limiter (RPM/ITPM/OTPM)
├── Skills Manager (auto-loads from skills/)
├── Memory Manager (bootstrap + curated + daily)
├── Cron Scheduler (morning briefing, EOD, weekly)
├── Watchers (Gmail, Calendar polling)
├── Orchestrator (multi-agent code review)
└── Dashboard (web UI)
    ↓
Node.js Sidecar (port 3001)
├── /ask — Claude Agent SDK (multi-turn)
├── /run-agent — Isolated CLI process
├── /reset — Clear session
└── /health — Liveness check
```

## Tech Stack

- **Gateway**: Go 1.22+, modernc.org/sqlite (pure Go, CGO_ENABLED=0)
- **Sidecar**: Node.js 20, @anthropic-ai/claude-agent-sdk, Express
- **Auth**: Claude subscription OAuth (no API key)
- **Deploy**: Docker Compose

## Directory Structure

```
cmd/gateway/main.go          Entry point
internal/
├── agent/client.go           Sidecar HTTP client
├── bot/bot.go                Telegram bot + commands
├── config/config.go          YAML + env loading
├── cron/scheduler.go         Cron job execution
├── dashboard/server.go       Web UI
├── db/db.go                  SQLite wrapper
├── db/migrations.go          Schema migrations (v1-v5)
├── memory/manager.go         Bootstrap + curated + daily
├── orchestrator/engine.go    Multi-agent pipeline
├── ratelimiter/limiter.go    Token tracking + queuing
├── skills/manager.go         Auto-loading skills
└── watcher/watcher.go        Gmail/Calendar polling
sidecar/
├── index.js                  Express + Claude Agent SDK
└── package.json
configs/
├── gateway.yaml              Main config
└── claude-code-settings.json MCP server config
memory/
├── bootstrap/                Static context (injected every request)
└── daily/                    Auto-generated daily summaries
skills/                       Auto-loaded skill prompts
```

## Build & Run

```bash
make build          # Build containers
make up             # Start services
make down           # Stop services
make logs           # Follow all logs
make logs-sidecar   # Sidecar logs only
make logs-gateway   # Gateway logs only
make restart        # Restart everything
```

## Key Config Files

- `.env` — Secrets (TELEGRAM_BOT_TOKEN, ALLOWED_USER_IDS, CLAUDE_CONFIG_DIR)
- `configs/gateway.yaml` — Cron, watchers, rate limits, memory
- `memory/bootstrap/*.md` — Static context injected into every Claude request

## Container Environment

When running inside Docker, Claude has access to:

| Path | Access | Description |
|------|--------|-------------|
| `/workspace` | Read/Write | Main working directory (host WORK_DIR) |
| `/clients/current` | Read-only | Current client files and data |
| `/clients/past` | Read-only | Past/archived client files |

## Build Requirements

- Gateway: `CGO_ENABLED=0` (pure Go SQLite via modernc.org/sqlite)
- Sidecar: Node.js 20+, native Claude Code binary (installed via claude.ai/install.sh)
- SDK: `@anthropic-ai/claude-agent-sdk` (not the old `@anthropic-ai/claude-code` SDK)
