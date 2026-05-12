# Claude Gateway v2

## Quick Commands — Restart / Rebuild

Run from `/workspace/projects/claude-gateway/`:

```bash
# Restart entire stack (recreates containers — picks up mount/compose changes)
docker compose down && docker compose up -d

# Restart sidecar only (e.g. after sidecar code change, no mount changes)
docker compose restart sidecar

# Full rebuild + restart (after Dockerfile or dependency changes)
docker compose down && docker compose build && docker compose up -d

# Tail logs after restart
docker compose logs -f              # all services
docker compose logs -f sidecar      # sidecar only
docker compose logs -f gateway      # gateway only
```

> **Note:** `docker compose restart` reuses existing containers and will NOT pick up
> changes to volumes/mounts in `docker-compose.yml`. Use `down` + `up` for mount changes.

---

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

- `.env` — Secrets (TELEGRAM_BOT_TOKEN, ALLOWED_USER_IDS, CLAUDE_CONFIG_DIR, DEEPSEEK_API_KEY)
- `configs/gateway.yaml` — Cron, watchers, rate limits, memory
- `memory/bootstrap/*.md` — Static context injected into every Claude request

## Auto-compaction

The sidecar tracks per-session context utilisation on every `/ask` result
(`input + cache_read + cache_creation` against the model's window). When
usage crosses `COMPACT_THRESHOLD` (default 0.80), the currently-active
provider's model writes a markdown bullet summary of the conversation,
the SDK resume id is dropped, and the summary is queued as the seed
prompt for the next `/ask` — the user sees one notification line and
continues without losing thread.

| Surface | Behaviour |
|---|---|
| Threshold | `COMPACT_THRESHOLD` env (default `0.80`) |
| Summary writer | The currently-active provider/model (so DeepSeek summarises DeepSeek sessions, etc.) |
| Old SDK session | Discarded from the sidecar's in-memory map; superseded summary persisted to `session_compactions` table for memory retrieval |
| Notification | One-line Telegram message ("🗜 Conversation auto-compacted at 82% of context window. Fresh session seeded with summary; continue normally.") |
| Manual trigger | `/compact` Telegram command — same flow, ignores threshold |
| Disable | Set `COMPACT_THRESHOLD=2.0` (effectively unreachable) |

Implementation: `sidecar/index.js` (runCompaction, handleCompact, pendingSeed),
gateway side: `internal/agent/claude.go` (CompactSession),
`internal/bot/commands.go` (`/compact` handler, `handleCompactionEvent`),
`internal/db` (migration v6: `session_compactions`).

## Multi-provider via Anthropic-compatible endpoints

The single sidecar can route requests to providers other than native Claude
without code changes to the sidecar's agent loop. Each request carries a
`provider` field; when set, the sidecar injects `ANTHROPIC_BASE_URL` and
`ANTHROPIC_AUTH_TOKEN` per-call via the SDK's `options.env`. Skills, tool
use, MCP, and session resume all keep working.

Currently registered: `claude` (native), `deepseek` (when `DEEPSEEK_API_KEY`
is set). Switch at runtime with `/framework deepseek` in Telegram.

To add another vendor with an Anthropic-compatible endpoint, append to the
`PROVIDERS` map in `sidecar/index.js` and register a backend in
`cmd/gateway/main.go` via `agent.NewClientWithProvider(name, provider, url, timeout)`.
Sessions are namespaced per provider on the wire so cross-provider state
never collides.

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
