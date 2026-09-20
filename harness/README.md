# Harness skills

Framework-agnostic skills owned by the gateway, not by Claude Code.

Drop a `skill-*.md` file in `harness/skills/` and **every** connected
model sees it: Claude, Grok, DeepSeek, OpenCode, and local Qwen (vLLM).

Claude-only workflows (Task tool, Claude agents, SessionStart hooks) stay
in `/workspace/.claude/skills/`. Anything a local model should also be
able to do belongs here.

## How they are delivered

| Layer | What | Who gets it |
|-------|------|-------------|
| Sidecar tools | First-class function tools (`GmailSearch`, `WebSearch`, `WebFetch`, `Now`, `ClientsFind`, …) | OpenAI-tool backends (Qwen/vLLM, future Ollama chat) |
| Sidecar system prompt | Always-on harness skill bodies + skill index | Qwen/vLLM (Claude Code is skipped on this path) |
| Gateway skills manager | Same files, hot-reloaded; index + matched full content | Claude, Grok, DeepSeek, OpenCode, and Qwen (matched extras) |
| Bash `gog` | Same OAuth token; documented in bootstrap | Claude / Grok / DeepSeek (Claude Code Bash tool) |

Auth is shared: `/googleauth` writes `/root/.config/gog/token.json` on the
`gog-tokens` volume. Sidecar and gateway both read it.

## Adding a skill

```markdown
---
name: my-skill
description: Short trigger text. Include the words a user will actually say.
always: false
harness: true
---

# My Skill

Write instructions that do **not** assume Claude Code.
Prefer named tools (`GmailSearch`) when they exist; fall back to
`gog` via Bash for Claude/Grok.
```

- `harness: true` — prefer this skill over Claude-only skills with the
  same trigger when the window is small (Qwen).
- `always: true` — inject the full body into Qwen's system prompt on
  every turn. Keep those files short. Use for capabilities the model
  must not "forget" (Gmail, web, clock).

On-demand library: naming a workspace skill or agent in the message
(e.g. "produce notices", "use the go purist", "elon musk") injects
that one file into **Qwen, DeepSeek, and Grok**. The rest of
`/.claude/agents` stays as a name index, not a dump.
- Filename must contain `skill` and end in `.md` (the loader's filter).

No sidecar rebuild is needed for markdown-only changes (sidecar re-reads
the directory per request; the gateway watches via fsnotify). New **tools**
require a sidecar rebuild.
