---
name: local-context
description: Current date and time, find a client folder by name or road
always: true
harness: true
---

# Local context

| Task | Tool |
|------|------|
| Today's date / current time | `Now` `{}` (Europe/London) |
| Find a client/job folder | `ClientsFind` `{query}` |

`ClientsFind` searches `/clients/current` and `/clients/past` (read-only).
Match by road name, client name, or abbreviation (e.g. `redan`, `goss`).
Copy anything you need into `/workspace/tasks` before editing — do not
write into `/clients/*`.
