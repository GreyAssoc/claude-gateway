---
name: google-workspace
description: Check Gmail inbox, read email threads, send mail, list calendar, search Drive, read Sheets
always: true
harness: true
---

# Google Workspace

You CAN access the user's Gmail, Calendar, Drive, and Sheets. Do not say
you cannot check email. Call a tool first.

Auth is pre-configured for `steve.w@greyandassociates.co.uk`. If a tool
returns "not authenticated", tell the user to run `/googleauth` in Telegram.

## Prefer named tools (Qwen / OpenAI-tool backends)

| Task | Tool |
|------|------|
| Search mail | `GmailSearch` `{query, max?}` |
| Read a thread | `GmailThread` `{id}` |
| Send mail | `GmailSend` `{to, subject, body}` — only when the user asked to send |
| Today's / upcoming events | `CalendarList` `{days?}` |
| Add an event | `CalendarAdd` `{title, start, duration}` |
| Find a Drive file | `DriveSearch` `{query}` / `DriveLs` `{parent?}` |
| Download a Drive file | `DriveDownload` `{file_id, out}` |
| Upload (one folder only) | `DriveUpload` `{path, parent}` parent must be `1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw` |
| Read a sheet | `SheetsRead` `{spreadsheet_id, range}` |

## Claude / Grok / DeepSeek — same job via Bash `gog`

```bash
gog gmail search 'is:unread newer_than:12h' --max 20
gog gmail search 'is:starred newer_than:7d'
gog gmail search 'from:someone@example.com'
gog gmail thread <thread_or_message_id>
gog gmail send --to a@b.com --subject "Hi" --body "Message"
gog calendar list
gog calendar list --from tomorrow --to +7d
gog calendar add --title "Meeting" --start "2026-02-25 14:00" --duration 1h
gog drive search 'name contains "report"'
gog drive download <file_id> --out /workspace/tasks/file.pdf
gog sheets read <spreadsheet_id> --range 'Sheet1!A1:Z100'
```

## Trigger words — use the tools immediately

email, gmail, inbox, unread, starred, send to, calendar, agenda, drive, spreadsheet, sheets

## Banned (never do these)

delete, trash, sheets write/update/append/clear/create, drive delete/trash/move.
Archiving (remove INBOX label) is allowed via `gog gmail batch modify`.

## Morning briefing pattern

1. `GmailSearch` / `gog gmail search` with `is:unread newer_than:12h`
2. `CalendarList` / `gog calendar list` for today
3. Bullet the top 3 priorities. Keep it short for Telegram.
