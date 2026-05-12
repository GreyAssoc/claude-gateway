import { query } from "@anthropic-ai/claude-agent-sdk";
import { execFile } from "node:child_process";
import { createServer } from "node:http";
import express from "express";
import { WebSocketServer } from "ws";

const app = express();
app.use(express.json({ limit: "10mb" }));

const PORT = process.env.SIDECAR_PORT || 3001;
const DEFAULT_CWD = process.env.WORK_DIR || "/home/user/project";

// --- Compaction config ---------------------------------------------------
//
// When the running token total for a session crosses COMPACT_THRESHOLD of
// the model's context window, the active model writes a summary of the
// conversation and the next /ask starts a fresh SDK session seeded with
// that summary as its first user message. This is the same auto-compact
// behaviour Claude Code applies to its own sessions.
const COMPACT_THRESHOLD = parseFloat(process.env.COMPACT_THRESHOLD || "0.80");

// Per-model context windows. Defaults are Anthropic's published numbers;
// override per-model with the COG_CONTEXT_WINDOW_<MODEL> pattern if needed.
// Unknown models fall back to DEFAULT_CONTEXT_WINDOW.
const DEFAULT_CONTEXT_WINDOW = 200_000;
const CONTEXT_WINDOWS = {
  // Claude
  "claude-sonnet-4-20250514": 200_000,
  "claude-sonnet-4-6": 200_000,
  "claude-haiku-4-5-20251001": 200_000,
  "claude-haiku-4-5": 200_000,
  "claude-opus-4-7": 200_000,
  "claude-opus-4-7[1m]": 1_000_000,
  "claude-opus-4-6": 200_000,
  // DeepSeek (Anthropic-compat endpoint)
  "deepseek-v4-pro": 128_000,
  "deepseek-chat": 128_000,
};

function contextWindowFor(modelName) {
  if (!modelName) return DEFAULT_CONTEXT_WINDOW;
  if (CONTEXT_WINDOWS[modelName]) return CONTEXT_WINDOWS[modelName];
  // Strip [1m] / [200k] suffixes for a lookup fallback.
  const base = modelName.replace(/\[[^\]]*\]$/, "");
  return CONTEXT_WINDOWS[base] || DEFAULT_CONTEXT_WINDOW;
}

const SUMMARY_PROMPT = process.env.COMPACT_SUMMARY_PROMPT || (
  "Summarise our entire conversation so far so it can seed a fresh " +
  "session without losing continuity. Use markdown bullets. Preserve: " +
  "every decision made, every file path or command actually executed, " +
  "open questions still in flight, and any context the user explicitly " +
  "asked to remember. Drop conversational filler and tool-call mechanics. " +
  "Keep the summary under 600 tokens. Output ONLY the summary — no " +
  "preamble like \"Here is the summary\"."
);

// In-memory session store. Each entry tracks the SDK's resume id, the
// last-turn token total (for threshold checks), and any pending seed
// summary that must be prepended to the next /ask prompt.
//
//   sessionId -> {
//     sdkSessionId,    // resume token from the SDK; null after compaction
//     lastActive,
//     lastTurnTotal,   // input + cache_read + cache_creation from latest result
//     contextWindow,   // window for the last-used model
//     pendingSeed,     // string to prepend to the next prompt, or null
//     provider,        // last-used provider name (e.g. "claude", "deepseek")
//     model,           // last-used model id
//   }
const sessions = new Map();

const SESSION_TTL_MS = 30 * 60 * 1000;
const MAX_SESSIONS = 1000;

// --- Shared helpers ---

const summarizeInput = (input) => {
  if (!input || typeof input !== "object") return "";
  const vals = Object.values(input);
  return input.file_path
    || input.command
    || input.pattern
    || input.path
    || input.query
    || input.url
    || (vals.length > 0 && typeof vals[0] === "string" ? vals[0].slice(0, 200) : "");
};

function evictOldestSession() {
  if (sessions.size <= MAX_SESSIONS) return;
  let oldest = null;
  let oldestTime = Infinity;
  for (const [id, s] of sessions) {
    if (s.lastActive < oldestTime) {
      oldest = id;
      oldestTime = s.lastActive;
    }
  }
  if (oldest) sessions.delete(oldest);
}

// Clean ANSI codes and box-drawing chars from CLI stderr output
function cleanStderr(data) {
  return data.toString()
    .replace(/\x1b\[[0-9;]*[a-zA-Z]/g, "")
    .replace(/\x1b\][^\x07]*\x07/g, "")
    .replace(/[\x00-\x08\x0b\x0c\x0e-\x1f]/g, "")
    .replace(/[╭╰╮╯│─┌┐└┘├┤┬┴┼]/g, "")
    .replace(/[✻●○◆◇▶▷►▻⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]/g, "")
    .trim();
}

function is429(msg) {
  return msg && (
    msg.includes("429") ||
    msg.includes("rate_limit") ||
    msg.includes("Rate limit")
  );
}

// --- Provider routing ---
// Maps a provider name to the Anthropic-compatible endpoint exposed by that
// vendor. The Claude Agent SDK reads ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN
// at subprocess spawn time, so we override them per request via options.env.
const PROVIDERS = {
  deepseek: {
    baseURL: "https://api.deepseek.com/anthropic",
    apiKeyEnv: "DEEPSEEK_API_KEY",
    defaultModel: "deepseek-v4-pro",
  },
};

// buildProviderOverlay returns { env, model } for a provider, or null if no
// provider was requested. Throws if the provider is unknown or the API key
// env var is missing — those are caller errors that should surface to the user.
function buildProviderOverlay(providerName, requestedModel) {
  if (!providerName) return null;
  const p = PROVIDERS[providerName];
  if (!p) {
    throw new Error(`Unknown provider: ${providerName}`);
  }
  const apiKey = process.env[p.apiKeyEnv];
  if (!apiKey) {
    throw new Error(`Missing ${p.apiKeyEnv} env var for provider ${providerName}`);
  }
  return {
    env: {
      ANTHROPIC_BASE_URL: p.baseURL,
      ANTHROPIC_AUTH_TOKEN: apiKey,
    },
    model: requestedModel || p.defaultModel,
  };
}

// --- Abort tracking ---
// Maps sessionId -> { aborted: false } for in-flight requests.
// The abort flag is checked inside the for-await loop to break early.
const activeRequests = new Map();

// --- WebSocket Handlers ---

async function handleAsk(msg, send) {
  const { session_id, prompt, cwd, max_turns, system_prompt, allowed_tools, model, provider } = msg;

  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  const sessionId = session_id || "default";
  const workDir = cwd || DEFAULT_CWD;
  const session = sessions.get(sessionId);

  // If a previous compaction queued a summary, prepend it to this prompt.
  // The seed becomes the first user message of the fresh SDK session,
  // priming the model with everything the old session would have known.
  let effectivePrompt = prompt;
  let consumedSeed = null;
  if (session && session.pendingSeed) {
    consumedSeed = session.pendingSeed;
    effectivePrompt =
      "[Previous conversation summary — for continuity only; the user's actual request follows below]\n\n" +
      consumedSeed +
      "\n\n---\n\n" +
      prompt;
    session.pendingSeed = null;
    console.log(`[ws-ask ${sessionId}] consuming compaction seed (${consumedSeed.length} chars) into fresh session`);
  }

  const keepalive = setInterval(() => {
    send({ type: "heartbeat" });
  }, 15000);

  let resultText = "";
  let sdkSessionId = null;
  let usage = {};
  let apiModel = "";
  const abortState = { aborted: false };
  activeRequests.set(sessionId, abortState);

  try {
    const options = {
      cwd: workDir,
      permissionMode: "acceptEdits",
      additionalDirectories: ["/clients/current", "/clients/past"],
    };

    if (max_turns && max_turns > 0) options.maxTurns = max_turns;
    options.allowedTools = allowed_tools || [
      "Read", "Write", "Edit", "Bash", "Glob", "Grep",
      "WebFetch", "WebSearch",
    ];
    if (system_prompt) options.systemPrompt = system_prompt;

    // Provider overlay (e.g. provider="deepseek") routes the request to a
    // different Anthropic-compatible endpoint. When unset, default Claude
    // behavior is preserved exactly.
    let overlay;
    try {
      overlay = buildProviderOverlay(provider, model);
    } catch (e) {
      send({ type: "error", error: e.message });
      clearInterval(keepalive);
      activeRequests.delete(sessionId);
      return;
    }
    if (overlay) {
      options.env = { ...process.env, ...overlay.env };
      options.model = overlay.model;
    } else if (model) {
      options.model = model;
    }

    if (session && session.sdkSessionId) options.resume = session.sdkSessionId;

    options.stderr = (data) => {
      process.stderr.write(`[claude-sdk] ${data}`);
      const clean = cleanStderr(data);
      if (clean.length > 2) {
        send({ type: "progress", text: clean.slice(0, 300) });
      }
    };

    console.log(`[ws-ask ${sessionId}] query (cwd: ${workDir}, model: ${options.model || "default"}, provider: ${provider || "claude"}, resume: ${session?.sdkSessionId || "none"}, seeded: ${consumedSeed ? "yes" : "no"})`);

    const conversation = query({ prompt: effectivePrompt, options });

    for await (const message of conversation) {
      if (abortState.aborted) {
        console.log(`[ws-ask ${sessionId}] aborted by user`);
        break;
      }
      const type = message.type;
      const sub = message.subtype ? `:${message.subtype}` : "";
      console.log(`[ws-ask ${sessionId}] event: ${type}${sub}`);

      if (type === "system" && message.subtype === "init") {
        sdkSessionId = message.session_id;
        send({ type: "progress", text: "Session initialized" });
        continue;
      }

      if (type === "assistant") {
        // Capture actual API model from response metadata
        const msgModel = message.message?.model || message.model;
        if (msgModel) {
          apiModel = msgModel;
          console.log(`[ws-ask ${sessionId}]   api-model: ${apiModel}`);
        }
        const content = message.content
          || (message.message && message.message.content)
          || [];
        if (Array.isArray(content)) {
          for (const block of content) {
            if (block.type === "tool_use") {
              const toolName = block.name || "unknown";
              const inputSummary = summarizeInput(block.input);
              console.log(`[ws-ask ${sessionId}]   tool: ${toolName} → ${inputSummary.slice(0, 80)}`);
              send({ type: "progress", tool: toolName, input: inputSummary });
            } else if (block.type === "text" && block.text) {
              send({ type: "progress", text: block.text });
            }
          }
        }
        continue;
      }

      if (type === "result") {
        resultText = message.subtype === "success"
          ? (message.result || "")
          : ((message.errors || []).join("\n") || message.error || `Error: ${message.subtype}`);
        usage = message.usage || {};
        sdkSessionId = sdkSessionId || message.session_id;
        continue;
      }

      // Forward unknown event types
      const preview = JSON.stringify(message).slice(0, 200);
      console.log(`[ws-ask ${sessionId}]   other: ${preview}`);
      send({ type: "progress", text: `[${type}${sub}]` });
    }

    clearInterval(keepalive);
    activeRequests.delete(sessionId);

    if (abortState.aborted) {
      send({
        type: "result",
        text: "(Stopped by user)",
        session_id: sessionId,
        usage: {
          input_tokens: usage.input_tokens || 0,
          output_tokens: usage.output_tokens || 0,
          cache_creation_input_tokens: usage.cache_creation_input_tokens || 0,
          cache_read_input_tokens: usage.cache_read_input_tokens || 0,
        },
      });
      return;
    }

    // Effective context size for THIS turn = uncached input + cache replay +
    // cache creation. For Anthropic the API request shipped the whole
    // conversation; the sum of these is the model's view of the active
    // window, which is what threshold checks should compare against.
    const lastTurnTotal =
      (usage.input_tokens || 0) +
      (usage.cache_read_input_tokens || 0) +
      (usage.cache_creation_input_tokens || 0);
    const ctxWindow = contextWindowFor(apiModel || options.model);
    const usageRatio = ctxWindow > 0 ? lastTurnTotal / ctxWindow : 0;

    // Snapshot the surviving sdk session id so we can record it on the
    // compaction row before clearing it below.
    const survivingSdkSessionId = sdkSessionId;

    // Persist updated session state. We keep the entry alive even if we're
    // about to compact, so the pendingSeed survives until the next /ask.
    if (sdkSessionId) {
      sessions.set(sessionId, {
        sdkSessionId,
        lastActive: Date.now(),
        lastTurnTotal,
        contextWindow: ctxWindow,
        pendingSeed: session?.pendingSeed || null,
        provider: provider || "claude",
        model: apiModel || options.model || null,
      });
    }
    evictOldestSession();

    // --- Auto-compaction trigger ---------------------------------------
    let compaction = null;
    if (survivingSdkSessionId && usageRatio >= COMPACT_THRESHOLD) {
      console.log(`[ws-ask ${sessionId}] threshold crossed (${(usageRatio * 100).toFixed(1)}% of ${ctxWindow}) — compacting`);
      compaction = await runCompaction({
        sessionId,
        sdkSessionIdToSummarise: survivingSdkSessionId,
        workDir,
        provider,
        model,
        reason: `threshold_${Math.round(COMPACT_THRESHOLD * 100)}pct`,
        triggeredBy: "auto",
        usageRatio,
        contextWindow: ctxWindow,
        tokensAtTrigger: lastTurnTotal,
      });
    }

    console.log(`[ws-ask ${sessionId}] done, result=${resultText.length} chars, ctx=${(usageRatio * 100).toFixed(1)}%${compaction ? ", COMPACTED" : ""}`);

    send({
      type: "result",
      text: resultText || "(no output)",
      session_id: sessionId,
      api_model: apiModel || undefined,
      usage: {
        input_tokens: usage.input_tokens || 0,
        output_tokens: usage.output_tokens || 0,
        cache_creation_input_tokens: usage.cache_creation_input_tokens || 0,
        cache_read_input_tokens: usage.cache_read_input_tokens || 0,
      },
      context: {
        tokens_used: lastTurnTotal,
        context_window: ctxWindow,
        usage_ratio_pct: Math.round(usageRatio * 100),
      },
      ...(compaction ? { compaction } : {}),
    });
  } catch (err) {
    clearInterval(keepalive);
    activeRequests.delete(sessionId);

    // If aborted, send a clean result instead of the error
    if (abortState.aborted) {
      console.log(`[ws-ask ${sessionId}] aborted by user (caught in error handler)`);
      send({
        type: "result",
        text: "(Stopped by user)",
        session_id: sessionId,
        usage: {
          input_tokens: usage.input_tokens || 0,
          output_tokens: usage.output_tokens || 0,
          cache_creation_input_tokens: usage.cache_creation_input_tokens || 0,
          cache_read_input_tokens: usage.cache_read_input_tokens || 0,
        },
      });
      return;
    }

    // The SDK throws "exit code 1" when the CLI process terminates, even after
    // a successful result:success event. If we already captured a result, send
    // it instead of the error.
    if (resultText) {
      console.log(`[ws-ask ${sessionId}] process exited with error after result, sending result (${resultText.length} chars)`);

      if (sdkSessionId) {
        sessions.set(sessionId, { sdkSessionId, lastActive: Date.now() });
      }
      evictOldestSession();

      send({
        type: "result",
        text: resultText,
        session_id: sessionId,
        usage: {
          input_tokens: usage.input_tokens || 0,
          output_tokens: usage.output_tokens || 0,
          cache_creation_input_tokens: usage.cache_creation_input_tokens || 0,
          cache_read_input_tokens: usage.cache_read_input_tokens || 0,
        },
      });
      return;
    }

    console.error(`[ws-ask ${sessionId}] error:`, err.message);
    if (err.stderr) console.error(`  stderr: ${err.stderr}`);
    if (err.stack) console.error(`  stack: ${err.stack}`);

    if (session && session.sdkSessionId) {
      sessions.delete(sessionId);
      console.log(`Cleared stale session ${sessionId} after failure`);
    }

    send({ type: "error", error: err.message, is_rate_limited: is429(err.message) });
  }
}

async function handleRunAgent(msg, send) {
  const { prompt, system_prompt, cwd, allowed_tools, timeout_ms, model, provider } = msg;

  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  const workDir = cwd || DEFAULT_CWD;
  const timeoutMs = timeout_ms || 3600000;

  let fullPrompt = prompt;
  if (system_prompt) {
    fullPrompt = system_prompt + "\n\n---\n\n" + prompt;
  }

  let overlay;
  try {
    overlay = buildProviderOverlay(provider, model);
  } catch (e) {
    send({ type: "error", error: e.message });
    return;
  }

  const effectiveModel = overlay ? overlay.model : model;
  const args = ["-p", fullPrompt, "--output-format", "text"];
  if (effectiveModel) args.push("--model", effectiveModel);
  if (allowed_tools && allowed_tools.length > 0) {
    args.push("--allowedTools", allowed_tools.join(","));
  }

  const childEnv = { ...process.env, HOME: "/root", PWD: workDir };
  if (overlay) Object.assign(childEnv, overlay.env);

  console.log(`[ws-agent] Spawning isolated process (cwd: ${workDir}, model: ${effectiveModel || "default"}, provider: ${provider || "claude"}, timeout: ${timeoutMs}ms)`);

  try {
    const output = await new Promise((resolve, reject) => {
      execFile("claude", args, {
        cwd: workDir,
        timeout: timeoutMs,
        maxBuffer: 10 * 1024 * 1024,
        env: childEnv,
      }, (err, stdout, stderr) => {
        if (err) {
          reject(err.killed
            ? new Error(`Process killed (timeout after ${timeoutMs}ms)`)
            : new Error(err.message + (stderr ? "\nstderr: " + stderr : "")));
          return;
        }
        resolve(stdout);
      });
    });

    console.log(`[ws-agent] Done (${output.length} bytes)`);
    send({ type: "result", text: output, is_rate_limited: false });
  } catch (err) {
    console.error(`[ws-agent] Error:`, err.message);
    send({ type: "error", error: err.message, is_rate_limited: is429(err.message) });
  }
}

// runCompaction performs one full compaction cycle:
//   1. Resume the about-to-be-superseded SDK session with a summarise prompt.
//   2. Collect the summary text from the active model (whichever provider is
//      currently routed — Claude, DeepSeek, etc.).
//   3. Clear the in-memory sdk session id (so the next /ask starts fresh)
//      and stash the summary as the pendingSeed for that fresh session.
//
// Returns the compaction metadata to include in the surrounding /ask
// response, or an object describing the failure (compaction is
// best-effort; a failed compaction must not corrupt the live session).
async function runCompaction(opts) {
  const {
    sessionId,
    sdkSessionIdToSummarise,
    workDir,
    provider,
    model,
    reason,
    triggeredBy,
    usageRatio,
    contextWindow,
    tokensAtTrigger,
  } = opts;

  const queryOptions = {
    cwd: workDir,
    permissionMode: "acceptEdits",
    resume: sdkSessionIdToSummarise,
    maxTurns: 1,
    allowedTools: [], // summarisation needs no tools
    systemPrompt:
      "You are a session-summarisation pass. You will be asked to summarise " +
      "the conversation so it can seed a fresh session. Output the summary " +
      "directly; do not call any tools.",
  };

  let overlay;
  try {
    overlay = buildProviderOverlay(provider, model);
  } catch (e) {
    console.warn(`[compact ${sessionId}] provider overlay failed: ${e.message}`);
    return {
      triggered: false,
      ok: false,
      reason,
      triggered_by: triggeredBy,
      error: e.message,
    };
  }
  if (overlay) {
    queryOptions.env = { ...process.env, ...overlay.env };
    queryOptions.model = overlay.model;
  } else if (model) {
    queryOptions.model = model;
  }

  let summaryText = "";
  try {
    const conv = query({ prompt: SUMMARY_PROMPT, options: queryOptions });
    for await (const message of conv) {
      if (message.type === "result") {
        summaryText = message.subtype === "success"
          ? (message.result || "")
          : "";
        break;
      }
    }
  } catch (err) {
    console.warn(`[compact ${sessionId}] summary call failed: ${err.message}`);
    // Mark the session entry: do NOT clear sdkSessionId — keep the old
    // resume id active so the user's next message still works. Skip the
    // compaction this turn; threshold will fire again next turn.
    return {
      triggered: false,
      ok: false,
      reason,
      triggered_by: triggeredBy,
      error: err.message,
    };
  }

  summaryText = (summaryText || "").trim();
  if (!summaryText) {
    console.warn(`[compact ${sessionId}] summary came back empty; skipping`);
    return {
      triggered: false,
      ok: false,
      reason,
      triggered_by: triggeredBy,
      error: "empty summary",
    };
  }

  // Compaction succeeded: drop the old SDK session id and queue the seed
  // for the next /ask. Preserve every other field on the session entry.
  const prev = sessions.get(sessionId) || {};
  sessions.set(sessionId, {
    sdkSessionId: null,
    lastActive: Date.now(),
    lastTurnTotal: 0,
    contextWindow: prev.contextWindow || contextWindow,
    pendingSeed: summaryText,
    provider: prev.provider || provider || "claude",
    model: prev.model || model || null,
  });

  console.log(`[compact ${sessionId}] success — summary ${summaryText.length} chars; old sdk=${sdkSessionIdToSummarise}`);

  return {
    triggered: true,
    ok: true,
    reason,
    triggered_by: triggeredBy,
    summary: summaryText,
    old_sdk_session_id: sdkSessionIdToSummarise,
    usage_ratio_pct: Math.round(usageRatio * 100),
    context_window: contextWindow,
    tokens_at_trigger: tokensAtTrigger,
    provider: provider || "claude",
    model: model || null,
  };
}

// handleCompact is the manual-trigger path (Telegram /compact). Same
// summarisation logic but ignores the threshold.
async function handleCompact(msg, send) {
  const sessionId = msg.session_id || "default";
  const sess = sessions.get(sessionId);

  if (!sess || !sess.sdkSessionId) {
    send({ type: "compact-result", session_id: sessionId, ok: false, error: "no active session to compact" });
    return;
  }

  const result = await runCompaction({
    sessionId,
    sdkSessionIdToSummarise: sess.sdkSessionId,
    workDir: msg.cwd || DEFAULT_CWD,
    provider: msg.provider || sess.provider,
    model: msg.model || sess.model,
    reason: "manual",
    triggeredBy: "manual",
    usageRatio: sess.contextWindow ? (sess.lastTurnTotal || 0) / sess.contextWindow : 0,
    contextWindow: sess.contextWindow || DEFAULT_CONTEXT_WINDOW,
    tokensAtTrigger: sess.lastTurnTotal || 0,
  });

  send({
    type: "compact-result",
    session_id: sessionId,
    ok: !!result.ok,
    compaction: result,
  });
}

function handleReset(msg, send) {
  const sessionId = msg.session_id || "default";
  sessions.delete(sessionId);
  console.log(`Session ${sessionId} cleared`);
  send({ type: "ack", session_id: sessionId });
}

function handleCheckSession(msg, send) {
  const sessionId = msg.session_id || "default";
  const sess = sessions.get(sessionId);
  send({
    type: "session-status",
    session_id: sessionId,
    alive: !!sess,
    sdk_session_id: sess?.sdkSessionId || null,
    last_active: sess ? new Date(sess.lastActive).toISOString() : null,
    idle_seconds: sess ? Math.floor((Date.now() - sess.lastActive) / 1000) : null,
  });
}

// --- HTTP Routes (health + debugging only) ---

app.get("/health", (_req, res) => {
  res.json({ status: "ok", sessions: sessions.size, uptime: process.uptime() });
});

app.get("/sessions", (_req, res) => {
  const list = [];
  for (const [id, session] of sessions) {
    list.push({
      id,
      sdkSessionId: session.sdkSessionId,
      lastActive: new Date(session.lastActive).toISOString(),
    });
  }
  res.json(list);
});

// --- Server Setup ---

const httpServer = createServer(app);
const wss = new WebSocketServer({ server: httpServer, path: "/ws" });

wss.on("connection", (ws) => {
  console.log("Gateway connected via WebSocket");
  ws.isAlive = true;

  ws.on("pong", () => { ws.isAlive = true; });

  ws.on("message", (raw) => {
    let msg;
    try {
      msg = JSON.parse(raw);
    } catch (e) {
      try { ws.send(JSON.stringify({ type: "error", error: "Invalid JSON" })); } catch (_) {}
      return;
    }

    const { id, type } = msg;

    // send() tags every response with the request id for correlation
    const send = (obj) => {
      if (ws.readyState === 1) {
        try { ws.send(JSON.stringify({ id, ...obj })); } catch (_) {}
      }
    };

    // Fire-and-forget — each request runs concurrently for multiplexing
    const handle = async () => {
      try {
        switch (type) {
          case "ask": await handleAsk(msg, send); break;
          case "run-agent": await handleRunAgent(msg, send); break;
          case "reset": handleReset(msg, send); break;
          case "check-session": handleCheckSession(msg, send); break;
          case "compact": await handleCompact(msg, send); break;
          case "abort": {
            const sid = msg.session_id || "default";
            const target = activeRequests.get(sid);
            if (target) {
              target.aborted = true;
              console.log(`[ws-abort] Abort flag set for session ${sid}`);
            } else {
              console.log(`[ws-abort] No active request for session ${sid}`);
            }
            send({ type: "ack" });
            break;
          }
          default: send({ type: "error", error: `Unknown message type: ${type}` });
        }
      } catch (err) {
        console.error(`[ws] Unhandled error for ${type} ${id}:`, err);
        send({ type: "error", error: err.message || "Internal error" });
      }
    };
    handle();
  });

  ws.on("close", () => { console.log("Gateway disconnected"); });
  ws.on("error", (err) => { console.error("WebSocket error:", err.message); });
});

// Ping all clients every 30s to detect dead connections
const pingInterval = setInterval(() => {
  wss.clients.forEach((ws) => {
    if (!ws.isAlive) return ws.terminate();
    ws.isAlive = false;
    ws.ping();
  });
}, 30000);

wss.on("close", () => clearInterval(pingInterval));

// --- Session cleanup ---

setInterval(() => {
  const now = Date.now();
  for (const [id, session] of sessions) {
    if (now - session.lastActive > SESSION_TTL_MS) {
      sessions.delete(id);
      console.log(`Session ${id} expired (inactive)`);
    }
  }
}, 60 * 1000);

// --- Start ---

httpServer.listen(PORT, "0.0.0.0", () => {
  console.log(`Sidecar listening on port ${PORT} (HTTP + WebSocket)`);
  console.log(`  HTTP:      http://0.0.0.0:${PORT}/health`);
  console.log(`  WebSocket: ws://0.0.0.0:${PORT}/ws`);
  console.log(`  Work dir:  ${DEFAULT_CWD}`);
});

httpServer.timeout = 3600000;
httpServer.requestTimeout = 0;
httpServer.headersTimeout = 120000;
httpServer.keepAliveTimeout = 30000;
