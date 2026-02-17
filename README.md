# Claude Gateway

A Go-based Telegram gateway for Claude Code with Google Workspace integration.

## Architecture

```
┌──────────────────────────────────────────────────┐
│                 Go Gateway                        │
│                                                   │
│  ┌───────────┐  ┌───────────┐  ┌──────────────┐  │
│  │ Telegram   │  │   Cron    │  │  Watchers    │  │
│  │   Bot      │  │ Scheduler │  │ (Gmail/Cal)  │  │
│  └─────┬──────┘  └─────┬─────┘  └──────┬───────┘  │
│        │               │               │          │
│  ┌─────▼───────────────▼───────────────▼────────┐ │
│  │            Rate Limiter / Queue               │ │
│  └──────────────────┬───────────────────────────┘ │
│                     │ HTTP (localhost:3001)        │
└─────────────────────┼─────────────────────────────┘
                      │
┌─────────────────────▼─────────────────────────────┐
│              Node.js Sidecar                       │
│  ┌─────────────────────────────────────────────┐   │
│  │         Claude Code SDK                      │   │
│  │         + MCP (Google Workspace)             │   │
│  └─────────────────────────────────────────────┘   │
└────────────────────────────────────────────────────┘
```

## Prerequisites

- Docker & Docker Compose
- Telegram Bot Token (from @BotFather)
- Anthropic API Key
- Google Cloud OAuth credentials (for Gmail, Calendar, Drive, Sheets)

## Quick Start

### 1. Clone and configure

```bash
cp .env.example .env
# Edit .env with your credentials
```

### 2. Google OAuth Setup (one-time, on local machine)

```bash
# Install the Google Workspace MCP server locally
pip install workspace-mcp

# Set your OAuth credentials
export GOOGLE_OAUTH_CLIENT_ID="your-client-id"
export GOOGLE_OAUTH_CLIENT_SECRET="your-secret"

# Run once to trigger the browser-based consent flow
uvx workspace-mcp --tools gmail drive calendar sheets

# Copy the resulting tokens
cp -r ~/.config/workspace-mcp ./google-tokens/
```

### 3. Run

```bash
docker compose up -d
```

### 4. Talk to your bot on Telegram

- Send any message to interact with Claude
- `/status` — check gateway health, rate limits, watchers
- `/reset` — clear current conversation session
- `/cron` — list active scheduled jobs
- `/cost` — show token usage for current session

## Configuration

All configuration is in `.env` and `configs/gateway.yaml`.

### Environment Variables

| Variable | Required | Description |
|---|---|---|
| `ANTHROPIC_API_KEY` | Yes | Your Anthropic API key |
| `TELEGRAM_BOT_TOKEN` | Yes | Telegram bot token from @BotFather |
| `ALLOWED_USER_IDS` | Yes | Comma-separated Telegram user IDs |
| `GOOGLE_OAUTH_CLIENT_ID` | Yes | Google Cloud OAuth client ID |
| `GOOGLE_OAUTH_CLIENT_SECRET` | Yes | Google Cloud OAuth client secret |
| `WORK_DIR` | No | Working directory for Claude (default: /home/user/project) |
| `SIDECAR_URL` | No | Sidecar URL (default: http://sidecar:3001) |

### Gateway Config (configs/gateway.yaml)

Controls cron schedules, watcher intervals, rate limits, and more.

## Project Structure

```
claude-gateway/
├── cmd/gateway/          # Go entrypoint
│   └── main.go
├── internal/
│   ├── agent/            # Claude sidecar HTTP client
│   ├── bot/              # Telegram bot handlers
│   ├── config/           # Config loading
│   ├── cron/             # Cron scheduler
│   ├── ratelimiter/      # Token-aware rate limiter
│   └── watcher/          # Gmail/Calendar watchers
├── sidecar/              # Node.js Claude Code SDK wrapper
│   ├── index.js
│   └── package.json
├── configs/
│   └── gateway.yaml
├── scripts/
│   └── entrypoint.sh
├── docker-compose.yml
├── Dockerfile.gateway
├── Dockerfile.sidecar
└── .env.example
```
