# Autonomous agent class for saas-builder

Date: 2026-10-17
Status: agreed, not built

This is the build note for the next change to claude-gateway. Nothing in this
file has been implemented. `@saas-builder` still uses the standard agent path.

## Baseline

Local `main` is commit `fde9bb6` ("Count each conversation on its own context
window and keep the selected model on child processes."). The push to
`origin/main` failed: the GitHub token in the build environment was rejected.
`origin/main` was still one commit behind when this note was written.

Do that push before starting this work if the remote is still behind.

## Why

The saas-builder agent (`/workspace/.claude/agents/saas-builder/`) is a
four-role pipeline. State lives in the product repo (`pipeline/STATE.md`).
The actor reads one role prompt, runs `next.sh`, does that role's work, writes
a handoff, and stops. Review happens in a fresh context. Human gates are real.
Qwen cannot hold this. DeepSeek and Grok can, if the harness sends the real
instructions instead of a truncated template.

The harness currently cannot do that:

- Every named agent gets `namedAgentSystemPrompt`, which keeps 900 characters
  of DNA (`maxNamedDNAChars` in `internal/bot/agentchat.go`).
- The saas-builder folder walks to about 143,000 characters of markdown.
  `ARCHITECTURE.md` is first, so the 900 characters are the blank product
  template. The operating rules in `saas-builder.md` are cut off.
- The standard wrapper says "prefer 1–3 tool calls" and not to run tests
  unless Steve asked or a compile just failed. Review must re-run the tests.
- There is no framework pin, so the agent inherits Qwen. On Qwen, phrases such
  as "where are we", "continue", and "work on …" are chat-only: two turns and
  no tools, so `next.sh` never runs.
- `/pipeline_resume` reaches the `pipeline-resume` skill, but a bare command
  is rejected because the handler demands extra text.
- One Telegram session is kept until `/reset`. Nothing starts the fresh
  context the review role requires.

`next.sh` is already executable and read-only. Bash on DeepSeek and Grok can
run it. Subagents are already off for these Telegram sessions, which matches
the agent's own rule.

## Two classes

Add `class` to an agent's frontmatter.

- **standard** — the default when `class` is omitted. Every existing agent
  stays standard. Their prompt, turn cap, Qwen chat-only detection, and tool
  rules stay as they are. Do not edit their files to add the field.
- **autonomous** — opt-in. Assign it only to saas-builder:

  ```yaml
  ---
  name: saas-builder
  class: autonomous
  ---
  ```

The branch in the gateway is "if this agent's class is autonomous". The
standard functions stay the ones every other agent calls.

## What autonomous means

One long role-step, then a clean stop. It does not mean the model keeps going
until the product is finished.

saas-builder already forbids that. One role per session. Update
`pipeline/STATE.md` and write the handoff before stopping. If the next item is
a human gate, stop and present the decision. Do not commit, push, or deploy
unless Steve asked. Do not review code in the same context that wrote it.

The harness change is a longer, properly instructed role-step. The pipeline as
a whole stays supervised at the gates.

## What the autonomous path does

Only for an agent whose class is autonomous:

1. **Prompt.** Send `saas-builder.md` in full. Do not concatenate the folder.
   Role prompts, templates, and `examples/shooting-club-ARCHITECTURE.md` stay
   on disk. The model reads the one role file `next.sh` names. Loading the
   whole folder puts Intake, Coding, and Review in one context.
2. **No standard wrapper.** Do not apply the 900-character cut, the "1–3 tool
   calls" line, or the ban on running tests.
3. **Model.** Refuse Qwen and say so. DeepSeek and Grok are both large enough
   for one role when the whole folder is not pasted in. Do not pin one of them
   in YAML. Steve chooses with `/models`, the same way other agents work.
   DeepSeek Pro is 128k in the rate limiter, Flash is 1M, Grok is 2M. One role
   prompt is 12–25k characters plus the ledger and the artefacts that role
   needs. Pro can hold that if the model reads only the architecture section
   tags the spec names, which the agent already requires.
4. **Turns.** Raise the turn cap for this class only, and keep a hard stop.
   A coding task of a few hundred lines does not fit in 40 turns. It must not
   run without a limit.
5. **`/pipeline_resume`.** A bare `/pipeline_resume` runs the resume. It must
   not demand extra text. The hyphen and underscore forms already match.
6. **Fresh role.** When `next.sh` names a different role from the one this
   session already played, start a fresh sidecar session before that role
   begins. `/reset` stays available either way.
7. **Same runner.** Same sidecar. Same Read, Write, Edit, and Bash. Child
   processes still inherit the model selected for this agent (`fde9bb6`).
   Do not add a second executor.
8. **Qwen chat-only detection does not apply** to this class. "Where are we?"
   must be allowed to run `next.sh` and read `STATE.md`.

## What does not change

- `namedAgentSystemPrompt` for standard agents.
- The 900-character DNA cut for standard agents.
- Qwen as the gateway default.
- Per-agent `/models` for standard agents, including Qwen.
- Cron, rate limits, and the grey-website content pipeline.
- The saas-builder role prompts, templates, and `next.sh`, except the one
  frontmatter line `class: autonomous` on `saas-builder.md`.

## Done when

- A standard agent prompt is unchanged (test the existing
  `namedAgentSystemPrompt` cases).
- `@saas-builder` on Qwen is refused with a short message to pick DeepSeek or
  Grok.
- `@saas-builder` on DeepSeek or Grok receives the full `saas-builder.md` text
  and not the architecture template.
- A bare `/pipeline_resume` starts the resume skill.
- A role change starts a new session.
- A human gate still stops the turn. The harness does not loop.
