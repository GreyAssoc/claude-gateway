# Claude Gateway v2

## Your AI Chief of Staff — Always On, Always Learning, Always Yours

Claude Gateway is a self-hosted personal AI gateway that turns Claude into a persistent, context-aware assistant accessible through Telegram. It remembers every conversation, learns your preferences, monitors your inbox, automates your daily routine, and reviews your code — all running on your own infrastructure with zero data leaving your control.

---

## The Problem

AI assistants today are stateless. Every conversation starts from scratch. They don't know your projects, your contacts, your communication style, or what you discussed yesterday. You spend more time re-explaining context than getting work done.

Meanwhile, the tools you rely on — email, calendar, documents, code review — live in separate silos. Switching between them breaks your flow. Delegating across them is impossible.

**Claude Gateway solves both problems in one system.**

---

## What It Does

### Persistent Memory That Actually Works

Claude Gateway maintains a 4-layer memory system backed by PostgreSQL with vector embeddings. It doesn't just store — it understands.

- **Conversations are preserved across sessions.** Claude remembers what you discussed last week, last month, or three months ago. When sessions expire, the gateway automatically replays context so you never experience amnesia.
- **Facts are auto-extracted.** Mention a server IP, a client's phone number, or a project deadline, and the system quietly saves it. Next time you ask, it's already there — no `/remember` command needed.
- **Semantic search finds meaning, not keywords.** Ask "what was that authentication discussion?" and vector similarity search surfaces the right conversation even if you never used the word "authentication".
- **Daily journals summarise your work.** Every midnight, an AI-generated summary captures what you discussed, what was decided, and what's pending. Three months from now, you can search "what happened with the party wall project in January" and get a coherent answer.

### Your Contacts, Automatically Injected

Add people once. Mention them naturally. Claude fills in the rest.

```
You: "Draft an email to Sarah about the invoice"

Claude already knows:
  Sarah Thompson — Accountant at XYZ Firm
  Email: sarah@xyz.com (Work)
  Mobile: 07700900000
  Notes: Handles billing, prefers formal tone
```

The triager detects names in your message and the context builder injects their full profile — role, company, contact details, notes — before Claude even starts composing.

### Emails Written In Your Voice

Profile documents let you teach Claude exactly how you communicate. Upload a writing style analysis and the gateway:

1. **Condenses it** into a short profile aspect (always in context)
2. **Injects the full document** when you ask Claude to draft an email

The result: emails that read like you wrote them. Correct greeting, correct sign-off, correct level of formality for the recipient. Not generic AI output — your voice, your patterns, your conventions.

### Scheduled Automation That Runs Itself

| What | When | What You Get |
|------|------|-------------|
| Morning Briefing | 7 AM weekdays | Urgent emails + today's calendar + top 3 priorities |
| End of Day | 6 PM weekdays | Summary of emails, events, and action items for tomorrow |
| Weekly Review | Monday 9 AM | Google Drive changes and project updates |

Add new jobs from Telegram at any time — no restart, no config file editing:

```
/cronadd ticket_check "*/30 9-17 * * 1-5" Check for new support tickets and summarise
```

### Instant Cancellation

Changed your mind? Sent the wrong instruction? Type `/stop` or just "stop" — the gateway cancels the in-progress Claude session immediately and you can start over. No waiting for a 30-minute timeout. No wasted tokens on the wrong task.

### Real-Time Gmail & Calendar Monitoring

- **Gmail**: Checked every 2 minutes. New emails trigger a notification with sender and subject.
- **Calendar**: Checked every 5 minutes. Events starting within 15 minutes get an alert — once per event, no spam.

Both respect your rate limits. If the API budget is tight, checks are silently skipped until the next window.

### Google Workspace — Fully Integrated, Safety-First

Claude can directly access your Gmail, Calendar, Google Drive, and Sheets through authenticated CLI tools. Not through plugins or third-party connectors — through your own OAuth credentials on your own infrastructure.

- Search, read, and send emails (delete/trash blocked at permissions level)
- Check calendar conflicts before scheduling
- Download files from Drive (read-only; one designated upload folder)
- Read spreadsheet data (read-only)

**Two-layer permission enforcement** protects your data: technical deny rules in the settings block dangerous operations at the tool level, while bootstrap instructions teach Claude why they're blocked. Archiving is allowed; deletion is not. Drive is read-only except for one writable folder. Sheets are fully read-only.

### Web Browsing

Claude has full internet access for research and information gathering:

- **WebSearch** for quick lookups and current information
- **WebFetch** for reading specific URLs and documentation
- **Playwright browser automation** for JavaScript-heavy pages and interactive workflows

No proxies, no wrappers — direct access to the web from inside the container.

### Multi-Agent Code Review

One command triggers a team of specialised AI reviewers, each with a distinct perspective:

| Agent | Perspective |
|-------|-------------|
| **Purist** | Is this idiomatic? Would the language creator approve? |
| **Pragmatist** | Does it work? Is it simple? Would a junior dev understand it? |
| **Pessimist** | How will this break? What's the worst case? |
| **Security** | How can this be exploited? (VETO power — cannot be overruled) |
| **Hacker** | Can I find an actual exploit? |

Available for Go, Python, Frontend, Kotlin, and cross-language integration reviews. Each language has both a dev team (quality) and a sec team (security). Security agents have absolute veto power — if they find a vulnerability, the review fails regardless of what other agents say. `/autoreview` auto-detects the primary language and selects the right team.

```
/review_go Review the authentication middleware for session handling vulnerabilities
```

The `/autofix` command goes further: review, fix, build, verify, repeat — automated loop until clean or max iterations reached.

### Hot-Swappable Skills

Skills are markdown files that teach Claude domain-specific workflows. Drop a file in the `skills/` directory and it's live immediately — no restart needed.

Current skills include YouTube research, legal document generation (notices and awards), audio survey transcription, spreadsheet analysis, and PDF drawing register extraction. Skills can chain together — the awards skill automatically invokes drawing extraction to populate documents. Creating your own takes minutes: write the instructions in markdown, add YAML frontmatter, save.

### Local LLM Triage

A local 7B model (Ollama) pre-screens every message. Simple greetings and status checks are handled locally — zero Claude API tokens consumed. Complex requests pass through to Claude with enriched context.

The same local model handles:
- Fact extraction from conversations
- User profile observation updates
- Daily journal generation
- Semantic embedding for all conversations

If the local model goes down, the system gracefully degrades — Claude handles everything directly. No crash, no data loss, just slightly higher token usage.

---

## Architecture

```
Telegram
    |
Go Gateway
├── Triage (local 7B)       — Classify & route messages
├── Context Builder          — Assemble memory, people, facts
├── Rate Limiter             — Track RPM / input / output tokens
├── Skills Engine            — Hot-load domain workflows
├── Memory System            — 4-layer persistent memory
├── People Manager           — Contact database with fuzzy search
├── Journal & Profile        — Daily summaries + auto-learning
├── Cron Scheduler           — Automated daily routines
├── Gmail & Calendar Watch   — Real-time notifications
├── Code Review Orchestrator — Multi-agent consensus
└── Web Dashboard            — Health monitoring API
    |
Node.js Sidecar
├── Claude Agent SDK         — Multi-turn with tool use
├── Google Workspace         — Gmail, Calendar, Drive, Sheets
└── Web Browsing             — WebSearch, WebFetch, Playwright
    |
PostgreSQL + pgvector        — Conversations, memory, people, journal
Ollama (host)                — Embeddings + local LLM triage
```

**Two Docker containers** (gateway + sidecar) plus host-based PostgreSQL and Ollama. Deploys in minutes with `docker compose up -d --build`.

---

## Key Differentiators

| Feature | Claude Gateway | ChatGPT / Gemini / Generic Wrappers |
|---------|---------------|--------------------------------------|
| **Memory persistence** | PostgreSQL with vector search — survives restarts, sessions, months | Session-based; lost on timeout |
| **Auto-learning** | Extracts facts, learns preferences, builds profile over time | Manual setup or none |
| **Contact awareness** | Fuzzy + semantic people search, auto-injected on mention | No contact integration |
| **Email in your voice** | Profile documents with condensed aspects + full doc injection | Generic tone |
| **Instant cancellation** | `/stop` cancels in-progress work immediately, no wasted tokens | Wait for timeout or refresh page |
| **Code review** | Multi-agent consensus with VETO security agents | Single-pass or none |
| **Workspace integration** | Gmail, Calendar, Drive, Sheets with two-layer permission controls | Plugin marketplace (privacy concerns) |
| **Web browsing** | WebSearch, WebFetch, and Playwright browser automation | Limited or plugin-based |
| **Local LLM triage** | 7B model reduces API costs, runs on your hardware | All tokens go to provider |
| **Self-hosted** | Your infrastructure, your data, your control | Cloud-only, data shared |
| **Data safety** | Deny rules block destructive operations; read-only defaults | Trust the AI not to delete things |
| **Rate limit management** | Three-dimension tracking with queuing and safety margins | Hard failures on limit |
| **Hot-reload skills** | Drop a markdown file, instantly available; skills can chain together | Requires deployment |
| **Cross-platform install** | Interactive wizard for Linux, macOS, Windows 11 (WSL2) | Manual config or cloud-only |

---

## Who It's For

- **Technical professionals** who live in Telegram and want Claude integrated into their workflow
- **Small business owners** who need an AI assistant that knows their clients, their email style, and their schedule
- **Software teams** who want automated multi-perspective code review without switching tools
- **Anyone** tired of re-explaining context to AI every single conversation

---

## What's Running Today

Claude Gateway v2 is in daily production use. It:

- Processes messages through a 5-stage pipeline (triage → context → Claude → post-process → respond)
- Maintains persistent memory across all sessions with automatic recovery
- Lets you cancel any in-progress request instantly with `/stop`
- Monitors Gmail every 2 minutes and calendar every 5 minutes
- Runs morning briefings, end-of-day summaries, and weekly reviews on schedule
- Auto-extracts facts, learns communication preferences, and generates daily journals
- Reviews Go, Python, and frontend code through multi-agent consensus teams
- Manages contacts with fuzzy search and automatic context injection
- Browses the web for research, documentation, and current information
- Enforces data safety with two-layer permission controls on Google Workspace
- Serves a monitoring dashboard with health, rate limits, and session data
- Handles graceful degradation when any component goes down
- Installs on Linux, macOS, or Windows 11 (WSL2) via interactive wizard

All on a single machine, using a standard Claude subscription. No additional API costs for the orchestrator — it uses the same subscription.

---

## The Numbers

| Metric | Value |
|--------|-------|
| Startup time | < 10 seconds |
| Context assembly | < 100ms per message |
| Local triage | 1-3 seconds (saves Claude tokens) |
| Embedding latency | ~50ms per call |
| Request cancellation | Instant via `/stop` |
| Memory categories | 5 (bootstrap, self, preference, profile_doc, fact) |
| Auto-fact extraction | Up to 3 per conversation |
| Max auto-facts | 200 (lowest-importance pruned) |
| Conversation replay | Last 30 messages on session recovery |
| Semantic search | Top 5 similar past conversations |
| Rate limit safety | 90% of plan limits (configurable) |
| Queue capacity | 100 requests |
| Code review teams | 10 teams, 20+ agents |
| Skills | 6 built-in, hot-reloadable, chainable |
| Platforms | Linux, macOS, Windows 11 (WSL2) |

---

## Getting Started

```bash
git clone <repo>
cd claude-gateway
bash install.sh   # Interactive wizard — configures everything
```

The installer walks you through bot personality, Telegram credentials, Google Workspace setup, PostgreSQL, workspace paths, and more. It generates all config files, initialises the database, builds the containers, and starts the services.

Or do it manually:

```bash
make setup        # Create .env from template
make build        # Build containers
make up           # Start everything
```

Either way: pull two Ollama models, send `/start` in Telegram, and you're live.

---

*Claude Gateway v2 — the AI assistant that remembers, learns, and works while you sleep.*
