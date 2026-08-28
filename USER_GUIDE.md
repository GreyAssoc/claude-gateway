# Claude Gateway v2 - User Guide

A personal AI gateway that connects Claude to Telegram with persistent memory, scheduled automation, email/calendar integration, multi-agent code review, and local LLM intelligence.

---

## Table of Contents

1. [Quick Start](#1-quick-start)
2. [Architecture Overview](#2-architecture-overview)
3. [Talking to Claude](#3-talking-to-claude)
4. [Commands Reference](#4-commands-reference)
5. [Memory System](#5-memory-system)
6. [People & Contacts](#6-people--contacts)
7. [Daily Journal](#7-daily-journal)
8. [User Profile](#8-user-profile)
9. [Skills](#9-skills)
10. [Scheduled Jobs (Cron)](#10-scheduled-jobs-cron)
11. [Watchers (Gmail & Calendar)](#11-watchers-gmail--calendar)
12. [Google Workspace](#12-google-workspace)
13. [Web Browsing](#13-web-browsing)
14. [Code Review Orchestrator](#14-code-review-orchestrator)
15. [Dashboard](#15-dashboard)
16. [Rate Limiting](#16-rate-limiting)
17. [Configuration Reference](#17-configuration-reference)
18. [Deployment & Operations](#18-deployment--operations)
19. [Troubleshooting](#19-troubleshooting)

---

## 1. Quick Start

### Prerequisites

- Docker and Docker Compose
- A Telegram bot token (from [@BotFather](https://t.me/BotFather))
- Your Telegram user ID (from [@userinfobot](https://t.me/userinfobot))
- Claude subscription (OAuth login via `claude.ai/install.sh`)
- PostgreSQL with pgvector extension (on the host machine)
- Ollama installed on the host machine (for local LLM; optional but recommended)

### Setup

#### Option A: Interactive Installer (Recommended)

The interactive installer walks you through every configuration step with a wizard-style interface:

```bash
cd /workspace/projects/claude-gateway
bash install.sh
```

The installer configures:
- Owner identity (name, role)
- Bot personality (professional, casual, dry wit, or custom)
- Verbosity level (terse, concise, detailed)
- Telegram bot token and allowed user IDs
- Workspace and Claude config directories
- Google Workspace integration (Gmail, Calendar, Drive)
- PostgreSQL and Dashboard settings
- Custom mount paths for client directories
- Timezone

It generates `.env`, `docker-compose.yml`, bootstrap files, and initialises the database. Supports Linux (native), macOS, and Windows 11 via WSL2.

#### Option B: Manual Setup

```bash
cd /workspace/projects/claude-gateway

# 1. First-time setup (creates .env from template)
make setup

# 2. Edit .env with your credentials
#    TELEGRAM_BOT_TOKEN, ALLOWED_USER_IDS, POSTGRES_PASSWORD, etc.

# 3. Initialize the PostgreSQL database
#    Run as the postgres superuser to create extensions and grant permissions:
sudo -u postgres psql -d gateway -f db/init.sql

# 4. Build containers
make build

# 5. Start everything
make up

# 6. Install Ollama models on the host (first time only)
ollama pull nomic-embed-text
ollama pull qwen2.5:7b
```

Open Telegram, find your bot, and send `/start`.

---

## 2. Architecture Overview

```
You (Telegram)
    |
    v
Go Gateway (port 8080)
├── Telegram Bot ............. Receives messages, sends responses
├── Triage (Ollama 7B) ...... Classifies messages, handles simple ones locally
├── Context Builder ......... Assembles memory, people, facts into prompts
├── Rate Limiter ............ Tracks RPM / input tokens / output tokens
├── Skills Manager .......... Auto-loads skill prompts from skills/ directory
├── Memory Manager .......... Bootstrap context + curated memories + daily summaries
├── People Manager .......... Contact database with fuzzy + semantic search
├── Journal Manager ......... AI-generated daily summaries
├── User Profiler ........... Auto-learns preferences and work patterns
├── Cron Scheduler .......... Morning briefing, EOD summary, weekly review
├── Watchers ................ Gmail and Calendar polling
├── Orchestrator ............ Multi-agent code review teams
└── Dashboard ............... Web UI for monitoring
    |
    v
Node.js Sidecar (port 3001)
├── Claude Agent SDK ........ Multi-turn conversations with tool use
├── Session Management ...... SDK session lifecycle + health checks
└── Google Workspace ........ Gmail, Calendar, Drive access via gog CLI
    |
    v
Ollama (host, port 11434)
├── nomic-embed-text ........ 768-dimensional embeddings (~50ms/call)
└── qwen2.5:7b .............. Local LLM for triage + fact extraction (~1-3s/call)
    |
    v
PostgreSQL (host, port 5432)
├── conversations ........... Every exchange with embeddings for semantic search
├── memory_entries .......... Bootstrap instructions, preferences, auto-learned facts
├── people .................. Contact database with trigram + vector indexes
├── contact_details ......... Phone, email, address for each person
├── daily_journal ........... AI-generated daily summaries
└── user_profile ............ Accumulated knowledge about your patterns
```

### Services

| Service | Language | Purpose |
|---------|----------|---------|
| **gateway** | Go | Core logic, Telegram bot, dashboard, memory, orchestrator |
| **sidecar** | Node.js | Claude Agent SDK bridge, Google Workspace tools |
| **ollama** | C++ (host) | Local LLM for embeddings and message triage (runs on host, not Docker) |

---

## 3. Talking to Claude

### Basic Conversation

Send any message to your bot in Telegram. Claude receives it with your full context (memory, recent history, people, facts) and responds.

### What Happens Behind the Scenes

Every message goes through a 5-stage pipeline:

```
1. TRIAGE ──> Can the local 7B handle this? (greetings, simple lookups)
     │              Yes → respond locally (zero Claude tokens)
     │              No  → continue to Claude
     v
2. CONTEXT BUILD ──> Assemble enriched prompt:
     │   • Recent conversation turns (last 20)
     │   • Semantically similar past conversations (top 5)
     │   • Relevant people context (auto-detected names)
     │   • Remembered facts (keyword + importance matching)
     │   • Recent journal entries (last 3 days)
     v
3. CLAUDE ──> Send to Claude with full context
     │   • Streaming response with live progress updates
     │   • Tool use (file reading, web search, code execution)
     v
4. POST-PROCESS ──> Background tasks (async, non-blocking):
     │   • Extract up to 3 facts from the exchange
     │   • Update user profile observations
     │   • Save exchange to PostgreSQL with embeddings
     v
5. RESPOND ──> Send Claude's response to Telegram
```

### Session Intelligence

The gateway detects when Claude's SDK session has died (idle timeout, sidecar restart, OOM) and automatically:

- Resets the stale sidecar session to prevent the SDK from resuming an expired session
- Replays the last 30 messages from PostgreSQL for continuity
- Rebuilds the full system prompt (bootstrap + profile + preferences)
- You experience seamless conversation even after session loss

When the session is alive, only the lightweight enriched prompt is sent (saving tokens).

### Progress Display

While Claude works, you see a live progress message:

```
Working... (45s)
📖 Read internal/bot/commands.go
⚡ Bash: go build ./...
📝 Edit internal/config/config.go
🔎 Grep: "handleCommand"

[Preview of Claude's response so far...]
```

This updates every 3 seconds and is deleted once the full response arrives.

### Stopping a Request

If Claude misunderstands your instruction or heads in the wrong direction, you can cancel the in-progress request immediately:

```
/stop
```

Or just type `stop` as a plain message — the gateway detects it and cancels without needing the slash prefix.

**What happens:**
1. The gateway cancels the Go context (stops waiting for Claude)
2. An abort signal is sent to the sidecar, which breaks the SDK loop
3. You see "Stopped." and can immediately send a new message with corrected instructions

If nothing is running, `/stop` replies "Nothing running to stop."

### Long Responses

Responses over 4,000 characters are automatically split into multiple Telegram messages at natural line breaks.

---

## 4. Commands Reference

### Gateway Status

| Command | Description |
|---------|-------------|
| `/start` | Show all available commands |
| `/stop` | Stop the current in-progress request (also works as plain text "stop") |
| `/status` | Gateway health, rate limits, queue depth |
| `/cost` | Token usage for current session (requests, input/output tokens) |
| `/reset` | Clear Claude session and start fresh |
| `/cron` | List active scheduled jobs and their status |
| `/context` | Show current memory context layers |
| `/dbcheck` | Check PostgreSQL conversation storage (recent messages) |

### Memory

| Command | Description |
|---------|-------------|
| `/remember <text>` | Save a fact or preference to persistent memory |
| `/forget <text>` | Remove matching memory entries (fuzzy ILIKE match) |
| `/memories` | List all active memories with category, source, importance |
| `/setbootstrap <text>` | Add a high-priority bootstrap instruction |
| `/listmemory [category]` | List memories filtered by category |
| `/today` | Show today's auto-generated daily summary |

### People & Contacts

| Command | Description |
|---------|-------------|
| `/addperson <details>` | Add a person (pipe-separated fields) |
| `/people` | List all people in contacts |
| `/person <name>` | Find a person by name (fuzzy match) |
| `/addcontact <details>` | Add contact info to a person |
| `/delperson <id>` | Remove a person by ID |

### Journal

| Command | Description |
|---------|-------------|
| `/journal [date]` | View daily journal entry (default: today) |
| `/journal_search <query>` | Search journal entries semantically |

### User Profile

| Command | Description |
|---------|-------------|
| `/profile` | View your inferred profile with confidence levels |
| `/setprofile <aspect> <content>` | Manually set a profile aspect (confidence 100%) |
| `/delprofile <aspect>` | Delete a profile aspect |

### Cron Management

| Command | Description |
|---------|-------------|
| `/cron` | List all scheduled jobs with status |
| `/cronadd <name> "<schedule>" <prompt>` | Add a new cron job at runtime |
| `/crondel <name>` | Remove a cron job |
| `/cronenable <name>` | Enable a disabled cron job |
| `/crondisable <name>` | Disable (pause) a cron job |

### Skills

| Command | Description |
|---------|-------------|
| `/skills` | List all loaded skills with status |
| `/skill <name>` | Show details for a specific skill |
| `/skill_enable <name>` | Enable a disabled skill |
| `/skill_disable <name>` | Disable a skill |

### Code Review

| Command | Description |
|---------|-------------|
| `/review_go <task>` | Go Dev Team review (Purist + Pragmatist + Pessimist) |
| `/review_go_sec <task>` | Go Security Team (Security [VETO] + Hacker) |
| `/review_fe <task>` | Frontend Dev Team review |
| `/review_fe_sec <task>` | Frontend Security Team |
| `/review_design <task>` | Design review (Security, Data Integrity, Performance) |
| `/review_integration <task>` | Integration review (API Guardian, Cross-Language) |
| `/review_python <task>` | Python Dev Team review |
| `/review_python_sec <task>` | Python Security Team |
| `/autoreview [dir]` | Auto-detect language and run appropriate team |
| `/autoreview_sec [dir]` | Auto-detect language and run security team |
| `/gauntlet <task>` | Run full validation pipeline (all teams) |
| `/agents` | List all available review agents |
| `/runs` | Show recent orchestrator runs (last 10) |

---

## 5. Memory System

The memory system gives Claude persistent knowledge across sessions. It operates in layers, each with a different purpose and lifecycle.

### Layer 1: Bootstrap Memory (Always Injected)

Static `.md` files in `memory/bootstrap/` that define Claude's foundational knowledge. On startup, these files are automatically synced to PostgreSQL (`memory_entries` table with `category='bootstrap'`). The context builder reads from PostgreSQL, so bootstrap content is always available even without disk access.

**Location:** `memory/bootstrap/`
**Current files:**
- `environment.md` — Container layout, available tools, project structure

Edit files directly on disk; they are re-synced on the next gateway startup. Changed files are updated in PostgreSQL automatically (keyed by filename). Use `/reset` after changing bootstrap files to force a new session with the updated system prompt.

### Layer 2: CLAUDE.md Files (Always Fresh)

Project instruction files read live from disk on every request:
- `/workspace/CLAUDE.md`
- `/workspace/projects/CLAUDE.md`

These provide workspace-level development guidelines and project isolation rules.

### Layer 3: Curated Memories (PostgreSQL)

User-created and auto-learned facts stored in PostgreSQL with importance ranking.

**Categories:**
| Category | Purpose | Injection |
|----------|---------|-----------|
| `bootstrap` | Core instructions | Always (highest priority) |
| `self` | Self-referential info | Always |
| `preference` | User preferences | Always |
| `fact` | Learned facts | By relevance + importance |

**Sources:**
| Source | How Created |
|--------|-------------|
| `user` | `/remember` command |
| `auto` | Extracted by post-processor |
| `system` | Set by gateway |
| `file` | Synced from bootstrap `.md` files on startup |

#### Remembering Facts

```
/remember The production server is 68.183.252.237
/remember Always use snake_case in Go code
/remember Sarah's mobile is 07700900000
```

Each memory is saved with:
- Category: `fact` (user memories) or `bootstrap` (`/setbootstrap`)
- Importance: 5 (default for user) or 10 (bootstrap)
- Source: `user`

#### Forgetting

```
/forget production server
```

This deactivates all memory entries where the content matches `%production server%` (case-insensitive). Multiple entries may be deactivated at once.

#### Listing Memories

```
/memories              — Show all active memories
/listmemory            — Same, with category/key detail
/listmemory auto       — Show only auto-learned facts
/listmemory bootstrap  — Show only bootstrap instructions
```

### Layer 4: Daily Summaries (Auto-Generated)

The memory compactor runs at midnight and generates a summary of the day's token usage and topics. These provide Claude with recent work context without replaying full conversations.

**Retention:** Last 7 days (configurable via `memory.max_daily_days`)

### Auto-Learning

When `triage.auto_learn` is enabled (default: `true`), the post-processor automatically extracts facts from every conversation:

- **Max 3 facts per exchange** — prevents noise
- **Importance threshold: 4/10** — only meaningful facts are saved
- **Max 200 auto-facts total** — lowest-importance facts are pruned when exceeded
- **Deduplication** — checks by key before saving; updates if higher importance
- **Tagged as `source: auto`** — distinguishable from user-created memories

**What gets extracted:**
- Concrete facts (names, dates, technical details, server IPs)
- User preferences and decisions
- Project-specific knowledge

**What does NOT get extracted:**
- Vague observations
- Information obvious from context
- The conversation itself

---

## 6. People & Contacts

The people system maintains a contact database that Claude can reference during conversations. When you mention someone by name, the triager detects it and injects their information into the prompt automatically.

### Adding a Person

```
/addperson Sarah Thompson | Sarah | client | Accountant | XYZ Firm | Handles billing
```

Fields are pipe-separated:
1. **Full name** (required)
2. **Preferred name** (optional)
3. **Type** (optional): `employee`, `client`, `vendor`, `contractor`, `personal`, `contact`
4. **Role** (optional)
5. **Company** (optional)
6. **Notes** (optional)

Minimal example:
```
/addperson John Smith
```

### Adding Contact Details

```
/addcontact Sarah | email | sarah@xyz.com | Work
/addcontact Sarah | mobile | 07700900000
/addcontact John | address | 123 Main St, London | Office
```

Fields: `name | type | value | label`

Contact types: `email`, `phone`, `mobile`, `telegram`, `address`, `other`

### Finding People

```
/person Sarah        — Fuzzy name match (trigram similarity)
/person Thompson     — Also works on last names
/people              — List everyone
```

The search uses PostgreSQL trigram matching first, then falls back to semantic (embedding) search if no trigram match is found.

### Automatic Context Injection

When you mention someone in a message, the triager extracts the name and the context builder injects their details:

```
You: "Can you draft an email to Sarah about the invoice?"

Claude sees (in enriched prompt):
<people_context>
Sarah Thompson (Accountant @ XYZ Firm)
  Preferred name: Sarah
  Type: client
  Notes: Handles billing
  Contacts: email: sarah@xyz.com (Work), mobile: 07700900000
</people_context>
```

### Removing People

```
/delperson 5         — Remove person with ID 5
```

The ID is shown in `/people` and `/person` output.

---

## 7. Daily Journal

The journal system generates AI-powered daily summaries of your conversations with Claude.

### How It Works

1. At midnight, the system queries all conversations from the previous day
2. Sends them to the local 7B model (Ollama) for summarization
3. The model extracts: summary, decisions, actions, topics, projects, stats
4. The entry is saved with an embedding for semantic search
5. If the LLM is unavailable, a simple statistical summary is generated instead

Journal entries are also generated when the `end_of_day` cron job runs (6 PM weekdays).

### Viewing Journal Entries

```
/journal              — Today's entry
/journal 2026-02-20   — Specific date
```

**Example output:**
```
Journal: 2026-02-20

Worked on Claude Gateway v2 implementation. Completed the
PostgreSQL schema, Ollama integration, and people management.
Fixed a critical bug in PruneAutoFacts ordering.

Topics: claude-gateway, postgresql, ollama, memory-system
Projects: claude-gateway

Stats: 45 messages | 82K input tokens | 34K output tokens
```

### Searching the Journal

```
/journal_search authentication refactor
```

This performs a semantic search (not keyword) across all journal entries. It embeds your query and finds the 5 most similar entries by cosine similarity.

### What Gets Tracked

| Field | Description |
|-------|-------------|
| `summary` | 2-4 sentence narrative of the day |
| `decisions` | What was decided (with context) |
| `actions` | Tasks taken, pending, or blocked |
| `topics` | Key subjects discussed |
| `projects` | Active projects mentioned |
| `stats` | Message count, token estimates |

---

## 8. User Profile

The profiler builds a model of your work patterns, communication style, and preferences by observing conversations over time.

### How It Works

- After each conversation, the local 7B model extracts observations
- Each observation updates an "aspect" of your profile
- Confidence starts low (0.3) and grows with consistent observations (+0.03-0.08 per evidence)
- Auto-learned aspects cap at 0.95 confidence
- Only aspects with confidence >= 0.4 are injected into Claude's system prompt
- Aspects decay by 0.01/week if not reinforced (minimum floor: 0.1)

### Profile Aspects

| Aspect | What It Tracks |
|--------|----------------|
| `communication_style` | How you phrase requests |
| `query_style` | Brief vs detailed, technical level |
| `work_patterns` | Timing and rhythm observations |
| `priorities` | What seems important right now |
| `email_style` | Tone, formality, sign-off preferences |
| `tools_preferences` | Preferred languages, tools, approaches |
| `projects_focus` | Active projects (auto-detected from journal) |

New aspect types are created automatically if the LLM observes a pattern that doesn't fit existing categories.

### Viewing Your Profile

```
/profile
```

**Example output:**
```
User Profile:

  communication_style (72%): Uses concise imperative sentences,
  prefers direct instructions over polite requests

  query_style (65%): Expects technical, detailed responses with
  code examples. Dislikes over-explanation of basics.

  priorities (58%): Currently focused on Claude Gateway v2
  implementation and trading platform development

  email_style (91%): Formal tone, signs off with "Kind regards",
  uses proper salutations
```

### Explicit Overrides

You can manually set any profile aspect at 100% confidence. This creates a "pinned" aspect that cannot be degraded by auto-learning.

```
/setprofile email_style Formal, always sign off with "Kind regards, Steve"
/setprofile priorities Focus on Claude Gateway v2 this week
```

### Deleting Aspects

```
/delprofile work_patterns
```

---

## 9. Skills

Skills are prompt templates that teach Claude specialized workflows. They're auto-loaded from the `skills/` directory.

### Current Skills

| Skill | Command | Purpose |
|-------|---------|---------|
| **YouTube Research** | `/youtube <query>` | Fetch transcripts, search videos/channels |
| **Notices** | `/notices <project>` | Generate party wall legal notice documents |
| **Schedule of Condition** | `/soc <project>` | Transcribe recorded surveys into standardized documents |
| **Data Spreadsheet** | `/data <file>` | Read and validate Excel DATA spreadsheet structure |
| **Awards** | `/awards <project>` | Generate Party Wall Award documents (D1/D2 and Addendum variants) |
| **Extract Drawing Numbers** | `/extract-drawings <pdf>` | Extract drawing registers from architectural/structural PDFs |

### Using Skills

**Explicit invocation** via command:
```
/youtube MACD indicator tutorial
/notices Redan Place project
```

When a skill command is invoked, the gateway:
1. Resets the Claude session (fresh context)
2. Injects the skill as a focused system prompt
3. Sends your arguments as the user message
4. Claude follows the skill's workflow

**Implicit detection:** Skills are also included in the system prompt during regular conversation. When you mention a task that matches a skill's domain, Claude can use it automatically.

### Managing Skills

```
/skills                    — List all skills with enabled/disabled status
/skill youtube             — Show YouTube skill details
/skill_enable youtube      — Enable a disabled skill
/skill_disable notices     — Disable a skill
```

### Adding Custom Skills

Create a `.md` file in `skills/` with YAML frontmatter:

```markdown
---
name: my-custom-skill
description: One-line description for /skills listing
command: mycmd
---

# My Custom Skill

Instructions for Claude on how to perform this task...
```

The file is auto-detected by the file watcher. No restart required.

---

## 10. Scheduled Jobs (Cron)

Cron jobs run Claude at specified times and send the output to your Telegram notification chat.

### Default Jobs

| Job | Schedule | What It Does |
|-----|----------|--------------|
| **Morning Briefing** | 7:00 AM Mon-Fri | Checks Gmail (last 12h) + calendar (today) + top 3 priorities |
| **End of Day** | 6:00 PM Mon-Fri | Summarizes emails, calendar events, action items for tomorrow |
| **Weekly Review** | 9:00 AM Monday | Checks Google Drive for files modified in the last week |
| **Memory Compaction** | Midnight daily | Internal: compacts memories, generates daily journal |

### Configuration

Jobs are defined in `configs/gateway.yaml`:

```yaml
cron:
  morning_briefing:
    schedule: "0 7 * * 1-5"       # 7 AM, Mon-Fri
    prompt: >
      Check my Gmail for urgent unread messages from the last 12 hours,
      check my calendar for today's events, and give me a concise morning
      briefing. Keep it short — bullet the top 3 priorities.
    enabled: true
```

### Schedule Syntax

Format: `minute hour day month weekday`

| Example | Meaning |
|---------|---------|
| `0 7 * * 1-5` | 7:00 AM, Monday through Friday |
| `0 18 * * 1-5` | 6:00 PM, Monday through Friday |
| `0 9 * * 1` | 9:00 AM, Monday only |
| `0 0 * * *` | Midnight, every day |
| `*/30 * * * *` | Every 30 minutes |

### Template Variables

The `{since}` placeholder is replaced with the timestamp of the last successful run:

```yaml
prompt: "Check Gmail for messages since {since}"
```

### Dynamic Cron Management (Telegram)

You can add, remove, enable, and disable cron jobs at runtime from Telegram without restarting the gateway.

**Adding a job:**
```
/cronadd my_check "*/30 9-17 * * 1-5" Check for new support tickets and summarize
```

Format: `/cronadd <name> "<schedule>" <prompt text>`
- The schedule must be quoted (double quotes)
- The name must be unique (no spaces)
- The job starts immediately in enabled state

**Removing a job:**
```
/crondel my_check
```

**Pausing/resuming a job:**
```
/crondisable morning_briefing    — Pause without removing
/cronenable morning_briefing     — Resume
```

**Listing jobs:**
```
/cron
```

Shows all jobs with their schedule, enabled status, and next run time.

---

## 11. Watchers (Gmail & Calendar)

Watchers poll external services at regular intervals and send notifications when something changes.

### Gmail Watcher

**Interval:** Every 2 minutes
**Behavior:**
- Checks for new emails since the last successful check
- If no new emails: silent (no notification)
- If new emails: sends a list with sender and subject for each

### Calendar Watcher

**Interval:** Every 5 minutes
**Behavior:**
- Checks for events starting in the next 15 minutes
- Deduplicates: each event is only notified once (24-hour rolling window)
- Notification format: `Upcoming: Event Title` / `Location` / `Time`

### Configuration

```yaml
watchers:
  gmail:
    enabled: true
    interval_seconds: 120
    prompt: >
      Check Gmail for messages received after {since}.
      If there are none, reply with exactly "NONE".

  calendar:
    enabled: true
    interval_seconds: 300
    prompt: >
      Check my calendar for events starting in the next 15 minutes.
      Reply with JSON array: [{"id":"...","title":"...","time":"...","location":"..."}].
```

### Rate Limit Behavior

Watchers respect the global rate limits. If the limit is hit, the check is silently skipped until the next interval — no error notification is sent.

---

## 12. Google Workspace

Claude has authenticated access to Gmail, Calendar, Google Drive, and Google Sheets via the `gog` CLI tool. All commands run through the sidecar using your own OAuth credentials.

### Gmail

Claude can search, read, and send emails. Archive operations are permitted.

```
gog gmail search 'newer_than:1d'              # Emails from last 24 hours
gog gmail search 'is:unread newer_than:7d'    # Unread from last week
gog gmail thread <thread_id>                  # Read full thread
gog gmail send --to a@b.com --subject "Hi" --body "Message"
```

**Banned operations:** All delete and trash commands are blocked at both the permissions level (`claude-code-settings.json` deny rules) and via bootstrap instructions. Claude cannot delete emails, labels, filters, or drafts. Archiving (removing the INBOX label) is allowed.

### Calendar

Claude can view events, check for conflicts, and add new events.

```
gog calendar list                             # Today's events
gog calendar list --from tomorrow --to +7d    # Next 7 days
gog calendar add --title "Meeting" --start "2026-02-25 14:00" --duration 1h
gog calendar conflicts --from today --to +7d  # Detect scheduling conflicts
```

### Google Drive (Read-Only)

Claude can list, search, and download files from Drive. Delete, trash, and move operations are blocked.

One folder is writable for uploads: `1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw`. Claude can upload files and create subfolders only in this specific directory.

```
gog drive ls                                  # Root files
gog drive search 'name contains "report"'     # Search files
gog drive download <file_id> --out /workspace/tasks/file.pdf
gog drive upload /workspace/tasks/doc.pdf --parent 1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw
```

### Google Sheets (Read-Only)

Claude can read spreadsheet data but cannot write, update, append, clear, delete, or create sheets.

```
gog sheets get <spreadsheet_id>               # Sheet metadata
gog sheets read <spreadsheet_id> 'Sheet1!A1:Z100'  # Read cell range
```

### Permission Enforcement

Restrictions are enforced at two layers:

1. **Technical** — `claude-code-settings.json` contains `deny` rules that block commands at the tool level. These cannot be bypassed.
2. **Instructional** — `memory/bootstrap/environment.md` documents the restrictions in the system prompt, so Claude understands why commands are blocked and can explain alternatives.

---

## 13. Web Browsing

Claude has full internet access via three tools, ordered by preference:

### WebSearch

Search the web for information. Returns titles, snippets, and URLs. Best for quick lookups and finding current information.

### WebFetch

Fetch and read any web page. Converts HTML to markdown. Best for reading specific URLs, documentation pages, or articles.

### Playwright (Browser Automation)

For JavaScript-heavy pages, login flows, or complex multi-step interactions, Claude can use a headless Chromium browser via the Playwright MCP server. This supports navigation, screenshots, clicking, and typing.

The Playwright MCP server is configured in `claude-code-settings.json`:

```json
{
  "mcpServers": {
    "playwright": {
      "command": "npx",
      "args": ["-y", "@playwright/mcp@latest", "--headless"]
    }
  }
}
```

**Preference order:** Always use WebFetch/WebSearch first (faster, lighter). Use Playwright only when pages require JavaScript rendering or interactive workflows.

---

## 14. Code Review Orchestrator

The orchestrator spawns isolated Claude CLI processes, each with a specialized agent persona, and applies weighted consensus for code review.

### How It Works

1. You specify a team and a task
2. The orchestrator runs each agent sequentially (separate Claude session per agent)
3. Each agent reviews the code through their unique lens
4. Verdicts are collected and weighted consensus is applied
5. Results are sent back to Telegram

### Available Teams

| Team | Agents | Best For |
|------|--------|----------|
| `go-dev-team` | GoPurist, GoPragmatist, GoPessimist | Go code quality |
| `go-sec-team` | GoSecurity [VETO], GoHacker | Go security vulnerabilities |
| `fe-dev-team` | FEPurist, FEPragmatist, FEPessimist | Frontend code quality |
| `fe-sec-team` | FESecurity [VETO], FEHacker | Frontend security |
| `python-dev-team` | PythonPurist, PythonPragmatist, PythonPessimist | Python code quality |
| `python-sec-team` | PythonSecurity [VETO], PythonHacker | Python security |
| `python-dev-team` | PythonPurist, PythonPragmatist, PythonPessimist | Python code quality |
| `python-sec-team` | PythonSecurity [VETO], PythonHacker | Python security |
| `design` | Security, Compliance, DataIntegrity, Performance, ErrorHandling | Architecture review |
| `integration` | APIGuardian, CrossLangSecurity [VETO] | API boundary review |

### Agent Hierarchy

| Tier | Weight | Role | VETO? |
|------|--------|------|-------|
| **Absolute** | 1000 | Security, Compliance, Data Integrity | Yes |
| **Critical** | 100 | Error Handling, Hacker agents | No (but blocking) |
| **Important** | 10 | Performance, Architecture | No |
| **Preferred** | 1 | Design, Documentation | No |

VETO agents can unilaterally block the entire review. Their findings cannot be overruled.

### Auto-Review

If you're not sure which team to use:

```
/autoreview /workspace/projects/claude-gateway
/autoreview_sec /workspace/projects/ptp-dev
```

The orchestrator detects the primary language(s) in the directory and selects the appropriate team automatically.

### Autofix Loop

The autofix command runs an iterative review-fix-verify cycle:

```
Review → Fix → Build/Test → Assess → (repeat until clean or max loops)
```

This is invoked via the `/autofix` skill command and documented in detail in `autofix.md`.

### Viewing Run History

```
/runs                  — Last 10 orchestrator runs
/agents                — List all available agents by team
```

---

## 15. Dashboard

The web dashboard provides monitoring and management at `http://localhost:8080`.

### Access Control

- **No DASHBOARD_TOKEN set:** Only accessible from localhost (127.0.0.1)
- **DASHBOARD_TOKEN set:** Accessible from any IP with `Authorization: Bearer <token>` header

### API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Dashboard web UI |
| `/api/status` | GET | Gateway health, uptime, service status |
| `/api/rate-limits` | GET | Current RPM, ITPM, OTPM usage and headroom |
| `/api/skills` | GET | List all skills with metadata |
| `/api/skills/{name}/toggle` | POST | Enable/disable a skill |
| `/api/memory` | GET | Active curated memories |
| `/api/memory/daily` | GET | Recent daily summaries |
| `/api/sessions` | GET | Active user sessions |
| `/api/agents` | GET | All orchestrator agents and teams |
| `/api/orchestrator/runs` | GET | Recent orchestrator run history |

### Rate Limiting

Dashboard API endpoints are rate-limited to 60 requests/minute per IP with burst of 10.

---

## 16. Rate Limiting

The gateway tracks API usage across three dimensions to stay within your Claude subscription limits.

### Three Dimensions

| Dimension | Default | What It Tracks |
|-----------|---------|----------------|
| **RPM** | 100 | Requests per minute |
| **ITPM** | 80,000 | Input tokens per minute |
| **OTPM** | 16,000 | Output tokens per minute |

Defaults are configured for the **Claude Max 5x plan** ($100/month). Override via environment variables if you're on a different plan.

### Safety Margin

All limits are multiplied by a safety margin (default: 0.90). With defaults, the effective limits are:

- RPM: 90 requests/min
- ITPM: 72,000 input tokens/min
- OTPM: 14,400 output tokens/min

### Queuing Behavior

When any limit is exceeded:
1. The request is queued (not dropped)
2. You see a message: `Rate limited. Queued at position #3. Window resets in 42s.`
3. When the minute window resets, the first queued request executes
4. Maximum queue size: 100 requests

All sources count toward the same limits: user messages, cron jobs, and watchers.

### Checking Status

```
/status
```

Shows current usage, headroom percentage, and queue depth.

---

## 17. Configuration Reference

All configuration lives in `configs/gateway.yaml` with environment variable overrides.

### Core Settings

```yaml
database:
  path: "data/gateway.db"              # SQLite database path

sidecar:
  url: "http://sidecar:3001"           # Sidecar endpoint
  timeout_seconds: 3600                # Max response wait (1 hour)
  health_path: "/health"               # Liveness probe
  default_model: "claude-opus-4-5-20251101"  # Claude model
```

### PostgreSQL & Ollama

```yaml
postgres_url: "${POSTGRES_URL}"                                    # Required for v2 features
ollama_url: "${OLLAMA_URL:-http://host.docker.internal:11434}"     # Local LLM (host)
ollama_embed_model: "nomic-embed-text"                             # 768-dim embeddings
ollama_llm_model: "qwen2.5:7b"                                    # Triage + extraction
```

### Context Builder

```yaml
context:
  recent_turns: 20           # Conversation turns in enriched prompt
  replay_messages: 30        # Messages replayed on new session
  semantic_hits: 5           # Semantic search results per message
  min_similarity: 0.75       # Cosine similarity threshold (0.0-1.0)
```

### Triage

```yaml
triage:
  enabled: true              # Use local LLM to pre-analyze messages
  auto_learn: true           # Auto-extract facts from conversations
  max_auto_facts: 200        # Maximum auto-learned facts stored
  min_importance: 4          # Minimum importance to save (1-10 scale)
```

### Memory

```yaml
memory:
  bootstrap_dir: "memory/bootstrap"    # Static context files
  daily_dir: "memory/daily"            # Auto-generated summaries
  max_curated_inject: 20               # Max facts injected per request
  max_daily_days: 7                    # Days of summaries to keep
  claude_md_paths:                     # Workspace instruction files
    - "/workspace/CLAUDE.md"
    - "/workspace/projects/CLAUDE.md"
```

### Rate Limits

```yaml
ratelimiter:
  rpm: ${RATE_LIMIT_RPM:-100}          # Requests per minute (Max 5x plan)
  itpm: ${RATE_LIMIT_ITPM:-80000}      # Input tokens per minute
  otpm: ${RATE_LIMIT_OTPM:-16000}      # Output tokens per minute
  safety_margin: 0.90                  # Use 90% of limits
```

### Heartbeat

```yaml
heartbeat:
  enabled: true
  interval_seconds: 300      # Check every 5 minutes
  max_failures: 3            # Alert after 3 consecutive failures
```

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Yes | Telegram bot token from @BotFather |
| `ALLOWED_USER_IDS` | Yes | Comma-separated Telegram user IDs |
| `TELEGRAM_CHAT_ID` | No | Chat ID for notifications (cron, alerts) |
| `POSTGRES_URL` | Yes | PostgreSQL connection string |
| `POSTGRES_PASSWORD` | Yes | PostgreSQL password (used in compose) |
| `OLLAMA_URL` | No | Ollama endpoint (default: http://host.docker.internal:11434) |
| `OLLAMA_EMBED_MODEL` | No | Embedding model (default: nomic-embed-text) |
| `OLLAMA_LLM_MODEL` | No | LLM model for triage (default: qwen2.5:7b) |
| `OLLAMA_CHAT_MODEL` | No | Fallback local chat model (Ollama) if vLLM is not configured. |
| `VLLM_URL` | No | vLLM Anthropic/OpenAI endpoint (container: `http://host.docker.internal:8005`). |
| `VLLM_MODEL` | No | vLLM `--served-model-name` for `/framework qwen` (e.g. `qwen3.8-27b`). |
| `CLAUDE_CONFIG_DIR` | Yes | Path to Claude OAuth credentials |
| `DASHBOARD_TOKEN` | No | Bearer token for remote dashboard access |
| `DASHBOARD_PORT` | No | Dashboard port mapping (default: 8080) |
| `GOG_KEYRING_PASSWORD` | No | Google Workspace keyring password |
| `GEMINI_API_KEY` | No | Gemini API key (for audio transcription) |
| `DEEPSEEK_API_KEY` | No | DeepSeek key for `/framework deepseek`. Read from this `.env` file (not the host shell). |
| `RATE_LIMIT_RPM` | No | Override RPM limit |
| `RATE_LIMIT_ITPM` | No | Override ITPM limit |
| `RATE_LIMIT_OTPM` | No | Override OTPM limit |
| `WORK_DIR` | No | Host workspace directory |
| `DB_PATH` | No | SQLite database path |
| `SIDECAR_URL` | No | Override sidecar endpoint |

---

## 18. Deployment & Operations

### Docker Compose Services

```yaml
services:
  gateway:     # Go binary, port 8080 (dashboard)
  sidecar:     # Node.js, port 3001 (Claude SDK)

volumes:
  gateway-data:   # SQLite DB, persists across restarts
```

Ollama runs on the host machine (not in Docker) for direct GPU access and easier model management. The gateway connects via `http://host.docker.internal:11434`.

### Build & Run Commands

```bash
make setup            # First-time setup (creates .env)
make build            # Build Docker images
make up               # Start all services
make down             # Stop all services
make restart          # Restart everything
make restart-sidecar  # Restart sidecar only
make logs             # Follow all logs
make logs-gateway     # Gateway logs only
make logs-sidecar     # Sidecar logs only
make dev              # Run gateway locally (no Docker)
make clean            # Remove containers, volumes, and images
```

**Important:** After modifying Go code, you must rebuild the containers for changes to take effect:

```bash
docker compose up -d --build
```

Running `make up` or `docker compose up -d` alone will NOT pick up Go code changes — the binary inside the container is compiled at build time.

### PostgreSQL Setup (Host)

PostgreSQL runs on the host machine (not in Docker) for better data management.

```bash
# Install PostgreSQL and pgvector (Manjaro/Arch)
sudo pacman -S postgresql
yay -S pgvector

# Create database and user
sudo -u postgres createuser gateway
sudo -u postgres createdb -O gateway gateway
sudo -u postgres psql -c "ALTER USER gateway PASSWORD 'your_password';"

# Install extensions and schema (run as superuser for GRANT statements)
sudo -u postgres psql -d gateway -f db/init.sql
```

**Important:** The `init.sql` script must be run as the `postgres` superuser (not the `gateway` user). It creates extensions (`pgvector`, `pg_trgm`) that require superuser privileges and includes `GRANT ALL` statements that give the `gateway` role proper permissions on all tables and sequences. If you previously ran it as the `gateway` user, re-run it as `postgres` to fix permissions.

### Ollama Setup (Host)

Ollama runs directly on the host machine for GPU access.

```bash
# Install Ollama (see https://ollama.com for your platform)
curl -fsSL https://ollama.com/install.sh | sh

# Pull required models
ollama pull nomic-embed-text      # ~274MB, embeddings
ollama pull qwen2.5:7b            # ~4.5GB, triage + fact extraction

# Verify models are loaded
ollama list
```

The gateway Docker container connects to Ollama on the host via `http://host.docker.internal:11434`. Set `OLLAMA_URL` in `.env` if your setup differs. To keep models loaded in VRAM, set `OLLAMA_KEEP_ALIVE=-1` in your Ollama environment.

### Local Qwen3.8 on the RTX 3090 (vLLM)

vLLM's OpenAI image also serves Anthropic `/v1/messages`, which is what Claude Code talks. Run vLLM on the host (separate compose, GPU) and point the gateway at the published port.

vLLM must be started with tool calling (already in your compose):

```text
--served-model-name qwen3.8-27b
--enable-auto-tool-choice
--tool-call-parser qwen3_coder
--port 8000          # published as host 8005
```

Gateway `.env`:

```bash
VLLM_URL=http://host.docker.internal:8005
VLLM_MODEL=qwen3.8-27b
```

`VLLM_MODEL` must match `--served-model-name`. Then recreate (gateway registers the backend at startup; sidecar JS is in the image):

```bash
# vLLM stack (host GPU) — keep this running
# docker compose up -d   # in the vLLM compose directory

cd /workspace/projects/claude-gateway
docker compose down && docker compose build && docker compose up -d
```

Check from the host that the Anthropic endpoint answers:

```bash
curl -s http://127.0.0.1:8005/v1/models
curl -s http://127.0.0.1:8005/v1/messages \
  -H 'content-type: application/json' \
  -H 'x-api-key: dummy' \
  -H 'anthropic-version: 2023-06-01' \
  -d '{"model":"qwen3.8-27b","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}'
```

Switch in Telegram with `/framework qwen` or `/models` → **Qwen3.8 (local)**.

`--max-model-len 16384` is the context window the sidecar uses for auto-compact. If you raise it in vLLM, raise the map in `sidecar/index.js` to match.

Triage/embeddings still use host Ollama (`qwen2.5:7b` + `nomic-embed-text`) if that stack is running. The 3090 is dedicated to vLLM.

### Graceful Degradation

| Component Down | Impact |
|----------------|--------|
| **PostgreSQL** | Fatal on startup. Required for persistent memory. |
| **Ollama** | Non-fatal. Triage disabled, no embeddings, no auto-learning, no semantic search. Claude handles everything directly. |
| **Sidecar** | Gateway starts in degraded mode with warning. Claude interactions fail until sidecar recovers. Heartbeat monitors and alerts. |

### Startup Sequence

1. Load config from YAML + env vars
2. Open SQLite database
3. Open PostgreSQL connection (fatal if fails)
4. Create Ollama client + health check (non-fatal warning if fails)
5. Wait for sidecar (up to 60 seconds, continues in degraded mode if timeout)
6. Initialize all managers (rate limiter, skills, memory, people, profiler, journal, context builder, triager, orchestrator)
7. Sync bootstrap memory files to PostgreSQL (inserts new files, updates changed content)
8. Create Telegram bot with all options
9. Start cron scheduler + watchers
10. Start heartbeat monitor
11. Start dashboard server
12. Start bot polling loop
13. Send startup notification to Telegram

### Shutdown Sequence

1. Send shutdown notification to Telegram
2. Cancel main context (signals bot and compaction to stop)
3. Stop cron scheduler + watchers + heartbeat
4. Shutdown dashboard server (5-second graceful timeout)
5. Wait for bot and compaction goroutines
6. Wait for background goroutines (RecordExchange, fact extraction, profile update)
7. Close sidecar WebSocket client
8. Close rate limiter
9. Close PostgreSQL (deferred)
10. Close SQLite (deferred)

---

## 19. Troubleshooting

### Common Issues

| Problem | Cause | Fix |
|---------|-------|-----|
| Bot doesn't respond | User ID not in allowlist | Add your Telegram ID to `ALLOWED_USER_IDS` |
| "Rate limited" on every message | RPM/ITPM too low or cron consuming quota | Check `/status`, increase limits or reduce cron frequency |
| "Session cleared" after idle | SDK session expired (30min idle) | Normal behavior. Gateway replays last 30 messages automatically |
| No daily journal generated | Ollama not running or no conversations that day | Check Ollama health: `ollama list` on host |
| People not auto-detected | Ollama LLM not loaded or triage disabled | Check `triage.enabled: true` and Ollama health |
| Semantic search returns nothing | Not enough conversations (need 100+ for ivfflat) | Accumulate more conversations or lower `min_similarity` |
| Dashboard shows "Unauthorized" | Not on localhost and no DASHBOARD_TOKEN | Set `DASHBOARD_TOKEN` in `.env` |
| Sidecar timeout errors | Claude API slow or long-running task | Check Claude status; normal for complex tasks (up to 30 min) |
| Queue keeps growing | More requests than rate limit allows | Reduce cron frequency, or increase RPM |
| Context lost between sessions | PostgreSQL permissions or RecordExchange failing | See "PostgreSQL Permissions" below |
| WebSocket i/o timeout after idle | Connection died during extended inactivity | Gateway auto-reconnects; if persistent, restart sidecar |
| Claude doesn't know about tools/skills | Bootstrap memory not loaded or stale session | Check logs for "Bootstrap sync", then send `/reset` |
| Go code changes not taking effect | Container not rebuilt | Run `docker compose up -d --build` |
| DeepSeek 401 `Your api key: ****xxxx is invalid` | Host-shell `DEEPSEEK_API_KEY` or cached Claude creds overriding `.env` | Unset host `DEEPSEEK_API_KEY`, confirm `.env`, rebuild sidecar (see below) |
| `/framework qwen` unknown / connection refused | vLLM not running, wrong port, or `VLLM_MODEL` ≠ `--served-model-name` | `curl :8005/v1/models`, set `VLLM_URL`/`VLLM_MODEL` in `.env`, recreate gateway+sidecar |
| Qwen 400 context length | Claude Code cannot fit in vLLM 16k | Sidecar talks to `/v1/messages` directly (chat, no Claude Code tools). Rebuild sidecar. Raise `--max-model-len` to 32768 for agent mode. |
| Triage not working / model not found | Wrong Ollama model name in config | Verify model with `ollama list`; check `OLLAMA_LLM_MODEL` in `.env` |

### DeepSeek 401 — wrong API key suffix

DeepSeek's error shows the last 4 characters of the key it actually received. If that suffix does not match `DEEPSEEK_API_KEY` in `claude-gateway/.env`, the sidecar was not using the file.

Two things caused this:

1. **Docker Compose interpolation.** `DEEPSEEK_API_KEY=${DEEPSEEK_API_KEY}` in `docker-compose.yml` is filled from the **host shell first**, then `.env`. An exported host variable (or a container created before you rotated the key, then `docker compose restart`) keeps sending the old key.
2. **Claude Code credential cache.** The sidecar copies host `~/.claude` into the container. Claude Code can reuse a cached API key / OAuth token instead of `ANTHROPIC_AUTH_TOKEN`.

The sidecar now reads `DEEPSEEK_API_KEY` from `/workspace/projects/claude-gateway/.env` on every request and runs DeepSeek in an isolated `CLAUDE_CONFIG_DIR`. After deploying this:

```bash
unset DEEPSEEK_API_KEY   # on the host, if it is exported
cd /workspace/projects/claude-gateway
docker compose down && docker compose build sidecar && docker compose up -d
docker compose logs -f sidecar   # look for: [providers] deepseek ready ... last4=...
```

The logged `last4` must match the end of the key in `.env`. `docker compose restart` is not enough — the sidecar JS is baked into the image.

### PostgreSQL Permissions

If you see `permission denied for table conversations` (error 42501) in logs or via `/dbcheck`, the `gateway` role lacks table permissions. This happens when `init.sql` was run as the `gateway` user instead of `postgres`:

```bash
# Fix immediately on host:
sudo -u postgres psql -d gateway -c "
  GRANT USAGE ON SCHEMA public TO gateway;
  GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO gateway;
  GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO gateway;
  ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO gateway;
  ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO gateway;
"

# Or re-run init.sql as superuser (idempotent, includes GRANTs):
sudo -u postgres psql -d gateway -f db/init.sql
```

### WebSocket Reconnection

The gateway maintains a persistent WebSocket connection to the sidecar. If the connection drops (network hiccup, sidecar restart, extended idle):

- **Automatic reconnect:** The gateway detects write failures and triggers reconnection
- **Retry with backoff:** Failed writes wait up to 5 seconds for reconnection, then retry once
- **Ping keepalive:** A ping is sent every 30 seconds with a 10-second write deadline
- **Manual recovery:** If problems persist, restart the sidecar: `make restart-sidecar`

### Health Checks

```bash
# Is gateway running?
curl http://localhost:8080/api/status

# Is sidecar running?
curl http://localhost:3001/health

# Is Ollama running? (on host)
curl http://localhost:11434/api/tags

# Is PostgreSQL accessible?
psql -U gateway -d gateway -c "SELECT COUNT(*) FROM conversations;"

# Check rate limit status
curl http://localhost:8080/api/rate-limits

# Check conversations are being saved (from Telegram)
/dbcheck
```

### Viewing Logs

```bash
make logs              # All services
make logs-gateway      # Gateway only
make logs-sidecar      # Sidecar only

# Search for errors
docker compose logs gateway 2>&1 | grep -i error
docker compose logs sidecar 2>&1 | grep -i error

# Check triage is working
docker compose logs gateway 2>&1 | grep -i triage

# Check context builder
docker compose logs gateway 2>&1 | grep -i "context builder"

# Check bootstrap sync
docker compose logs gateway 2>&1 | grep -i "bootstrap sync"
```

### Database Queries

```bash
# Conversation count
psql -U gateway -d gateway -c "SELECT COUNT(*) FROM conversations;"

# Recent conversations
psql -U gateway -d gateway -c "SELECT role, LEFT(content, 80), created_at FROM conversations ORDER BY created_at DESC LIMIT 10;"

# Active memories
psql -U gateway -d gateway -c "SELECT category, key, LEFT(content, 60), importance, source FROM active_memory LIMIT 20;"

# Auto-learned facts
psql -U gateway -d gateway -c "SELECT key, LEFT(content, 60), importance FROM memory_entries WHERE source = 'auto' AND is_active ORDER BY importance DESC;"

# Bootstrap entries (synced from disk)
psql -U gateway -d gateway -c "SELECT key, LEFT(content, 60), importance FROM memory_entries WHERE category = 'bootstrap' AND is_active ORDER BY key;"

# People
psql -U gateway -d gateway -c "SELECT id, full_name, role, company FROM people WHERE is_active ORDER BY full_name;"

# Recent journal entries
psql -U gateway -d gateway -c "SELECT date, LEFT(summary, 100) FROM daily_journal ORDER BY date DESC LIMIT 7;"

# User profile
psql -U gateway -d gateway -c "SELECT aspect, confidence, LEFT(content, 80) FROM user_profile ORDER BY confidence DESC;"
```

---

**Version:** Claude Gateway v2
**Last Updated:** 2026-02-25
