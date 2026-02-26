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

## Available Commands

You can list client directories with:
- `ls /clients/current/` — see current clients
- `ls /clients/past/` — see past clients

## Google Workspace (gog CLI)

Use the **Bash tool** to run `gog` commands for Google Workspace access (Gmail, Calendar, Drive, Sheets). Auth is pre-configured for `steve.w@greyandassociates.co.uk` — no setup needed, just run the commands.

### Gmail
```bash
gog gmail search 'newer_than:1d'              # Emails from last 24h
gog gmail search 'is:unread newer_than:7d'    # Unread from last week
gog gmail search 'from:someone@example.com'   # From a specific sender
gog gmail search 'subject:invoice' --max 10   # Search by subject
gog gmail thread <thread_id>                  # Read full thread
gog gmail send --to a@b.com --subject "Hi" --body "Message"  # Send email
gog gmail labels list                         # List labels
```

### Calendar
```bash
gog calendar list                             # Today's events
gog calendar list --from tomorrow --to +7d    # Next 7 days
gog calendar add --title "Meeting" --start "2026-02-25 14:00" --duration 1h
gog calendar conflicts --from today --to +7d  # Detect scheduling conflicts
```

### Drive
```bash
gog drive ls                                  # Root files
gog drive ls --parent <folder_id>             # List folder contents
gog drive search 'name contains "report"'     # Search files
gog drive download <file_id> --out /workspace/tasks/file.pdf
gog drive upload /workspace/tasks/doc.pdf --parent <folder_id>
```

### Sheets
```bash
gog sheets get <spreadsheet_id>               # Sheet metadata
gog sheets read <spreadsheet_id> 'Sheet1!A1:Z100'  # Read cell range
gog sheets write <spreadsheet_id> 'Sheet1!A1' --values '[[1,2],[3,4]]'
```

### Common Flags
- `--json` — JSON output (for parsing/scripting)
- `--plain` — Tab-separated output
- `--max N` — Limit results

## Skill Detection

Skills are loaded into your context below (in `<skills>` tags). When Steve sends a natural language message, detect whether it matches a skill and follow that skill's full workflow.

**Detection rules:**
- Match by intent, not exact phrasing. "produce notices for Redan" = "generate notices for the Redan project" = "notices for Redan"
- When a skill is detected, follow its workflow steps exactly — don't improvise or skip steps
- If unsure which skill applies, ask Steve to clarify
- If no skill matches, handle the message normally

**Keyword patterns:**

| Keywords | Skill |
|----------|-------|
| notices, serve notices, generate notices, produce notices | **notices** |
| schedule of condition, SoC, transcribe survey, process audio, process recordings | **schedule-of-condition** |
| DATA spreadsheet, DATA file, read the DATA, check the DATA | **data-spreadsheet** |
| youtube, transcript, video transcript, watch video | **youtube** |

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

## How You're Accessed

- **Telegram**: Users message you via @PTPClaudeBot
- **Cron jobs**: Scheduled tasks run automatically (morning briefing, end of day, weekly review)
- **Watchers**: Gmail and Calendar are polled periodically
