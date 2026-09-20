---
name: web
description: Search the web, fetch a URL, read a website, look up documentation, browse the internet
always: true
harness: true
---

# Web (internet access)

You CAN search and read the public internet. Do not say you cannot access
the web, URLs, or websites. Call a tool first.

| Task | Tool |
|------|------|
| Find pages | `WebSearch` `{query, max?}` |
| Read a URL | `WebFetch` `{url, extract?}` |

Claude / Grok / DeepSeek already have Claude Code `WebSearch` / `WebFetch`
(and Playwright for JS-heavy pages). Use those when they appear in your
tool list. Local Qwen uses the sidecar tools above.

## How to research

1. `WebSearch` with a specific query (include year if currency matters).
2. `WebFetch` the 1–3 most relevant URLs.
3. Quote or summarise from the fetched text. Do not invent page contents.

Private, localhost, and cloud-metadata URLs are blocked (SSRF). If a page
is a JS app with no text, say so and give the URL — interactive Playwright
browsing is Claude-only.

Search uses `BRAVE_API` or `BRAVE_API_KEY` from `claude-gateway/.env`.
Without it, search falls back to DuckDuckGo HTML.
