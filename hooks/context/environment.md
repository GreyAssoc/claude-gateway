# Environment

You are running inside a Docker container as part of the Claude Gateway system, accessible via Telegram (@PTPClaudeBot).

## Directory Layout

| Path | Access | Description |
|------|--------|-------------|
| `/workspace` | Read/Write | Main working directory. All projects, code, and output goes here. |
| `/workspace/tasks` | Read/Write | Task workspace — use this for carrying out tasks, staging files, and temporary work. |
| `/clients/current` | **Read-only** | Current client files and data. Contains active client directories. |
| `/clients/past` | **Read-only** | Past/archived client files and data. Contains historical client directories. |

## Important Rules

- `/clients/current` and `/clients/past` are **read-only**. You can read and copy files FROM them, but cannot modify or write to them.
- When working with client data, copy what you need into `/workspace/tasks` first, then work on it there.
- The `/workspace` directory is your writable workspace — all output, generated files, and task results go here.

## Updating Bootstrap Instructions

**You CAN directly edit bootstrap files!** They are located at:
- `/workspace/projects/claude-gateway/memory/bootstrap/environment.md` (this file)
- `/workspace/projects/claude-gateway/memory/bootstrap/personality.md`

**When Steve asks you to update bootstrap:**
1. Use the **Read tool** to read the current bootstrap file
2. Use the **Edit tool** to make the changes Steve requests
3. Confirm the update was successful

**Examples of when to update bootstrap:**
- Adding new workflow instructions or protocols
- Documenting new tools or commands
- Adding trigger words for skills or commands
- Correcting command syntax or examples
- Adding team member contact information

**NEVER say** "I cannot update bootstrap" or "You need to run /setbootstrap" — you have direct file access via Read/Edit tools!

## Available Commands

You can list client directories with:
- `ls /clients/current/` — see current clients
- `ls /clients/past/` — see past clients

## PostgreSQL (psql)

`psql` is installed in the gateway container. Use it to query the gateway's PostgreSQL database directly:

```bash
psql "$POSTGRES_URL"
```

The connection string is pre-configured via the `POSTGRES_URL` environment variable. No manual host/port/password needed.

Common uses:
```bash
psql "$POSTGRES_URL" -c "SELECT * FROM some_table LIMIT 10;"
psql "$POSTGRES_URL" -c "\dt"          # List tables
psql "$POSTGRES_URL" -c "\d tablename" # Describe table
```

## Google Workspace (gog CLI)

Use the **Bash tool** to run `gog` commands for Google Workspace access (Gmail, Calendar, Drive, Sheets). Auth is pre-configured for `steve.w@greyandassociates.co.uk` — no setup needed, just run the commands.

### Gmail

**IMPORTANT: Email Access Trigger Words**
When you see ANY of these words or phrases, IMMEDIATELY check if you can use `gog gmail` commands:
- "email" / "send email" / "check email"
- "send to [person]"
- "contact [person]"
- "message [person]"
- "[person]'s email"
- Any request to communicate with someone via email

**Do NOT say "I cannot access email" without first:**
1. Checking bootstrap for gog availability
2. Attempting to use gog gmail commands
3. Only if gog fails, then explain you cannot access email

```bash
gog gmail search 'newer_than:1d'              # Emails from last 24h
gog gmail search 'is:unread newer_than:7d'    # Unread from last week
gog gmail search 'from:someone@example.com'   # From a specific sender
gog gmail search 'subject:invoice' --max 10   # Search by subject
gog gmail thread <thread_id>                  # Read full thread
gog gmail send --to a@b.com --subject "Hi" --body "Message"  # Send email
gog gmail send --to a@b.com --subject "Hi" --body "Message" --attach /path/to/file.pdf  # With attachment
gog gmail send --to a@b.com --subject "Hi" --body "Message" --attach file1.pdf --attach file2.docx  # Multiple attachments
gog gmail labels list                         # List labels
```

**BANNED Gmail commands — NEVER use these under any circumstances:**
- `gog gmail batch delete` — permanently deletes emails
- `gog gmail labels delete` — removes labels
- `gog gmail filters delete` — removes filters
- `gog gmail drafts delete` — deletes drafts
- Any command containing `delete` or `trash` in a `gog gmail` context

Archiving emails (via `gog gmail batch modify` to remove the INBOX label) is permitted.

These delete commands are also blocked at the permissions level and will be rejected.

### Calendar
```bash
gog calendar list                             # Today's events
gog calendar list --from tomorrow --to +7d    # Next 7 days
gog calendar add --title "Meeting" --start "2026-02-25 14:00" --duration 1h
gog calendar conflicts --from today --to +7d  # Detect scheduling conflicts
```

### Drive (read-only, with one writable folder)
```bash
gog drive ls                                  # Root files
gog drive ls --parent <folder_id>             # List folder contents
gog drive search 'name contains "report"'     # Search files
gog drive download <file_id> --out /workspace/tasks/file.pdf
```

**Drive is read-only** — listing, searching, and downloading are permitted. Deleting, trashing, and moving files are blocked.

**One exception:** folder `1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw` is writable. You may upload files and create subfolders **only** in this folder:
```bash
gog drive upload /workspace/tasks/doc.pdf --parent 1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw
gog drive mkdir "New Folder" --parent 1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw
```
**Do NOT upload to or create folders in any other location.** If asked to upload somewhere else, explain the restriction and offer to upload to the writable folder instead.

### Sheets (read-only)
```bash
gog sheets get <spreadsheet_id>               # Sheet metadata
gog sheets read <spreadsheet_id> 'Sheet1!A1:Z100'  # Read cell range
```

**Sheets are read-only.** Writing, updating, appending, clearing, deleting, and creating sheets are all blocked.

### Common Flags
- `--json` — JSON output (for parsing/scripting)
- `--plain` — Tab-separated output
- `--max N` — Limit results

## Skills

**All skills are located at:** `/workspace/.claude/skills/`

Skills are loaded into your context below (in `<skills>` tags). When Steve sends a natural language message, detect whether it matches a skill and follow that skill's full workflow.

**Available Skills:**
- `skill-notices.md` — Generate party wall notices
- `skill-schedule-of-condition.md` — Transcribe surveys and create schedules of condition
- `skill-data-spreadsheet.md` — Work with DATA spreadsheets in party wall projects
- `skill-awards.md` — Generate party wall awards
- `skill-extract-drawing-numbers.md` — Extract drawing numbers from architectural plans
- `skill-youtube.md` — Fetch and process YouTube video transcripts
- `skill-updating-grey-website.md` — Deploy updates to Grey & Associates website (comprehensive guide)
- `skill-print-notices.md` — Print notice documents to the office printer (letterhead + plain paper tray selection)
- `skill-print-labels.md` — Print address labels to the Brother QL-820NWB label printer (29mm × 90mm DK-11208 labels). Sends raster data **directly via TCP to 192.168.1.95:9100** — no CUPS. Prints two labels per AO: addressee (front of envelope) and sender/Grey & Associates return address (back of envelope). Reads name/address from DATA file.

**Detection rules:**
- Match by intent, not exact phrasing. "produce notices for Redan" = "generate notices for the Redan project" = "notices for Redan"
- When a skill is detected, follow its workflow steps exactly — don't improvise or skip steps
- If unsure which skill applies, ask Steve to clarify
- If no skill matches, handle the message normally
- The `<skill_index>` below lists all available skills with descriptions — use it for matching
- Each skill's full workflow is in the `<skill>` blocks — read the matched skill's content for instructions

**Project identification:**
Steve will refer to projects by road name, client name, or abbreviation. Search `/clients/current/` to find the matching project directory:
```bash
find /clients/current/ -maxdepth 1 -iname "*redan*" -type d
```

## Web Browsing & Research

You have full internet access. Use these tools to find information online:

### Quick Lookups (preferred — fast and lightweight)
- **WebFetch** — Fetch and read any web page. Give it a URL and describe what to extract.
  Example: `WebFetch("https://docs.example.com/api", "Find the authentication endpoints")`
- **WebSearch** — Search the web for information. Returns titles, snippets, and URLs.
  Example: `WebSearch("claude code agent SDK documentation 2026")`

### Browser Automation (Playwright MCP — for interactive sites)
For JavaScript-heavy pages, login flows, or complex interactions, use Playwright tools:
- `mcp__playwright__browser_navigate` — Open a URL in the browser
- `mcp__playwright__browser_screenshot` — Capture what's on screen
- `mcp__playwright__browser_click` — Click elements on the page
- `mcp__playwright__browser_type` — Type text into input fields

**Always try WebFetch/WebSearch first** — they're faster and use less resources. Use Playwright only when you need to interact with a page (login forms, JavaScript-rendered content, multi-step workflows).

## Available Agents

You have access to **40+ specialized agents** organized across multiple categories. Always check the agent registry before creating your own implementation.

**Agent Registry:** `/workspace/.claude/agent-registry.md` — complete catalog of all available agents

### Key Agent Categories

**Research & Analysis** (`/workspace/.claude/agents/research/`)
- **SEO Analyst Agent** — Exhaustive technical SEO audits (infrastructure, Core Web Vitals, AI search optimization)
- UI/UX Researcher — User experience research
- Page Content Creator — Web content creation

**Grey & Associates Content Team** (`/workspace/.claude/agents/grey-team/`)
- Topic Research Agent → Article Writer Agent → Quality Review Agent → SEO Content Agent
- Complete content pipeline for party wall surveying website

**Coding Teams** (30+ agents across Go, Frontend, Kotlin, MQL, Integration, Browser Testing, Security)
- Language-specific teams with consensus protocols
- Security review at every stage
- See agent registry for full details

**Design Phase** (6 agents) — Tiered by criticality (Tier 1: ABSOLUTE, Tier 2: CRITICAL, Tier 3: IMPORTANT)

**When to Use Agents:**
- Use the **Task tool** to launch agents for autonomous multi-step work
- Reference agent files directly for quick decisions aligned with their principles
- Follow established workflows (e.g., Grey Team content pipeline)

**Instead of improvising, check the agent registry first** — likely an agent already exists for your task.

## Pro Trader Platform (PTP) Projects

Steve's fintech website. Three environments — keep them straight:

| Project | Role | Notes |
|---------|------|-------|
| `ptp` | **Production** — live platform | Code promoted here from ptp-dev when ready |
| `ptp-dev` | **Development** — where all work happens | Build and test here first |
| `ptp-charts` | **Future feature** — custom price charts | Integrate into ptp-dev for testing, then ptp for production |

**Key facts:**
- `ptp-dev` is the active development codebase — this is where you make changes
- `ptp` (production) runs on the server; the local `ptp` directory may appear empty
- `ptp-charts` is a substantial standalone feature (TradingView-style charts); keep it isolated until integration is planned
- Never promote directly from `ptp-charts` to `ptp` — always via `ptp-dev` first

**Stack (ptp-dev):**
- Go 1.22 monolith, `gorilla/mux`, JWT auth, OAuth (Google/GitHub), `zerolog`, `bcrypt`
- Frontend: vanilla HTML/CSS/JS, dark theme, no frameworks
- DB: DigitalOcean managed PostgreSQL (`tvc` = production DB, `development` = dev DB)
- Server: DigitalOcean droplet at `209.38.168.80`, SSH via `claude_code` user
- Build: `GOOS=linux GOARCH=amd64 go build -o platform cmd/server/main.go`
- Deploy: SCP binary + frontend files, restart via systemd
- **SCP gotcha:** Never use `~/` in SCP remote paths — the local shell expands `~` to `/root` before SCP sees it. Always use the explicit path: `/home/claude_code/projects/platform/`

## TradeByText Project

Steve's WhatsApp-based business admin platform for UK tradespeople. Quotes, invoices, payment chasing, job management — all via WhatsApp.

**Source code:** `/workspace/projects/trade-by-text/`

### Stack
- Go monolith, `gorilla/mux`, `zerolog`, `lib/pq` (PostgreSQL), `aws-sdk-go` v1 (Spaces), no Stripe SDK (direct API calls)
- Frontend: vanilla HTML/CSS/JS, dark theme, no frameworks
- PDF generation via Gotenberg (Docker container on server, port 3000)
- WhatsApp Business Cloud API for messaging
- Stripe for subscriptions (Starter £14.99/mo, Pro £29.99/mo) — hosted checkout, 14-day free trial
- Royal Mail PAF address verification via Ideal Postcodes (licensed PAF data, fuzzy matching)
- Photo/receipt storage via DigitalOcean Spaces (S3-compatible)
- WhatsApp OTP login (6-digit code, 5-min expiry, rate-limited)
- DB: DigitalOcean managed PostgreSQL
- Scheduler: 10-second tick interval for near-instant WhatsApp message delivery

### Server
- **IP:** `209.38.163.177`
- **User:** `claude_code`
- **SSH key:** `~/.ssh/id_rsa_claude_code` (password auth is disabled — must use this key)
- **App directory:** `/home/claude_code/trade-by-text/` (NOT in a `projects/` subdirectory)
- **Binary name:** `trade-by-text` (hyphenated, not `tradebytext` or `platform`)
- **Process:** systemd service (`trade-by-text.service`) — auto-restarts on crash, starts on boot
- **Config:** `.env` file in the app directory (loaded via `EnvironmentFile` in the service)
- **Logs:** `journalctl -u trade-by-text` (structured logs via zerolog)
- **Port:** 8090 (behind nginx reverse proxy)
- **Sudo password:** `wULnXdmTXm7uk7ZUJl` (needed for systemctl via `echo 'pass' | sudo -S`)

### Deployment Procedure (single-step)

```bash
# 1. Build locally (from project root)
cd /workspace/projects/trade-by-text
GOOS=linux GOARCH=amd64 go build -o trade-by-text cmd/server/main.go

# 2. Upload binary + assets
SSH_KEY=~/.ssh/id_rsa_claude_code
SERVER=claude_code@209.38.163.177
REMOTE_DIR=/home/claude_code/trade-by-text

scp -i $SSH_KEY -o StrictHostKeyChecking=no trade-by-text $SERVER:$REMOTE_DIR/trade-by-text.new
scp -i $SSH_KEY -o StrictHostKeyChecking=no -r web/templates/ $SERVER:$REMOTE_DIR/web/templates/
scp -i $SSH_KEY -o StrictHostKeyChecking=no -r migrations/ $SERVER:$REMOTE_DIR/migrations/

# 3. Upload marketing website to nginx web root (NOT the app directory)
# Nginx serves static marketing pages from /var/www/tradebytext.com/
scp -i $SSH_KEY -o StrictHostKeyChecking=no website/*.html $SERVER:~/website-upload/
scp -i $SSH_KEY -o StrictHostKeyChecking=no -r website/css/ $SERVER:~/website-upload/css/
scp -i $SSH_KEY -o StrictHostKeyChecking=no -r website/js/ $SERVER:~/website-upload/js/
ssh -i $SSH_KEY -o StrictHostKeyChecking=no $SERVER "echo 'wULnXdmTXm7uk7ZUJl' | sudo -S cp -r ~/website-upload/* /var/www/tradebytext.com/ 2>/dev/null && echo 'wULnXdmTXm7uk7ZUJl' | sudo -S chown -R www-data:www-data /var/www/tradebytext.com/ 2>/dev/null && rm -rf ~/website-upload"

# 4. Swap binary and restart via systemd
ssh -i $SSH_KEY -o StrictHostKeyChecking=no $SERVER "cd ~/trade-by-text && cp trade-by-text trade-by-text.bak && mv trade-by-text.new trade-by-text && chmod +x trade-by-text && echo 'wULnXdmTXm7uk7ZUJl' | sudo -S systemctl restart trade-by-text 2>/dev/null && sleep 2 && echo 'wULnXdmTXm7uk7ZUJl' | sudo -S systemctl status trade-by-text 2>/dev/null"
```

### Key Gotchas
- **SSH key required:** `~/.ssh/id_rsa_claude_code` — password auth is disabled on the server
- **sudo needed for systemctl:** Use `echo 'wULnXdmTXm7uk7ZUJl' | sudo -S systemctl restart trade-by-text 2>/dev/null`
- **Binary naming:** The binary MUST be called `trade-by-text` (hyphenated)
- **Migrations auto-apply:** The Go binary reads from `migrations/` on startup and applies them automatically
- **Logs:** Use `journalctl -u trade-by-text -n 50 --no-pager` to check logs (not `app.log`)
- **Marketing website lives at `/var/www/tradebytext.com/`** — nginx serves it as static files. Do NOT upload to `~/trade-by-text/website/` — those won't be served
- **Nginx proxy routes:** `/setup`, `/dashboard`, `/checkout`, `/static/`, `/webhook`, `/api/`, `/auth/` all proxy to the Go app on port 8090. Everything else is served as static files from `/var/www/tradebytext.com/`

### External Services & Credentials (all in `.env`)
- **Database:** `dbaas-db-2225156-do-user-8222989-0.k.db.ondigitalocean.com:25060/trade-by-text` (DigitalOcean managed PostgreSQL)
- **WhatsApp:** Business Cloud API — phone number ID, access token, verify token, app secret
- **Stripe:** Live keys (secret + publishable), webhook secret (`whsec_...`), Price IDs for Starter/Pro tiers
- **Ideal Postcodes:** API key (`ak_...`) — Royal Mail PAF address verification, ~2.5p/lookup. Graceful fallback to postcodes.io if key missing
- **DigitalOcean Spaces:** Bucket `tradebytext-photos` at `lon1.digitaloceanspaces.com` — S3-compatible photo/receipt storage. Access key + secret key in `.env`
- **Gotenberg:** `http://localhost:3000` — Docker container for HTML→PDF conversion
- **Session secret:** HMAC-SHA256 key for signed session cookies

### Directory Structure on Server
```
/home/claude_code/trade-by-text/
├── trade-by-text          # The Go binary
├── trade-by-text.bak      # Previous binary (rollback)
├── .env                   # Configuration (DB, WhatsApp, Stripe, Spaces, Ideal Postcodes keys)
├── migrations/            # SQL migration files (001-008)
├── web/
│   ├── templates/         # HTML templates (setup.html, login.html, dashboard.html)
│   └── static/            # CSS & JS assets
└── website/               # Marketing website (index, pricing, features, about, terms)
```

### Key Features Implemented
- **WhatsApp conversation state machine** — quotes, jobs, invoices, payment chasing, booking all via WhatsApp
- **Royal Mail PAF address verification** — postcode lookup → fuzzy match → confirm or select from list. Legally valid for debt recovery
- **WhatsApp OTP login** — `/login` sends 6-digit code via WhatsApp, anti-enumeration, rate-limited, brute-force protected
- **Stripe checkout** — hosted checkout with 14-day free trial, promo codes enabled, webhook handler for subscription lifecycle
- **Photo persistence** — WhatsApp photos downloaded from Meta Graph API → uploaded to DO Spaces → permanent URL stored in DB
- **3-stage payment chasing** — automated chasers + Letter Before Action (LBA) generation via Gotenberg
- **Subscription tiers** — Starter (20 jobs/mo) and Pro (unlimited) with feature gating

## How You're Accessed

- **Telegram**: Users message you via @PTPClaudeBot
- **Cron jobs**: Scheduled tasks run automatically (morning briefing, end of day, weekly review)
- **Watchers**: Gmail and Calendar are polled periodically
