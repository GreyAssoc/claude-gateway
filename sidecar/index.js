import { query } from "@anthropic-ai/claude-agent-sdk";
import { execFile } from "node:child_process";
import { createServer } from "node:http";
import express from "express";
import { WebSocketServer } from "ws";

const app = express();
app.use(express.json({ limit: "10mb" }));

const PORT = process.env.SIDECAR_PORT || 3001;
const DEFAULT_CWD = process.env.WORK_DIR || "/home/user/project";

// In-memory session store: sessionId -> { sdkSessionId, lastActive }
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

// --- Abort tracking ---
// Maps sessionId -> { aborted: false } for in-flight requests.
// The abort flag is checked inside the for-await loop to break early.
const activeRequests = new Map();

// --- WebSocket Handlers ---

async function handleAsk(msg, send) {
  const { session_id, prompt, cwd, max_turns, system_prompt, allowed_tools, model } = msg;

  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  const sessionId = session_id || "default";
  const workDir = cwd || DEFAULT_CWD;
  const session = sessions.get(sessionId);

  const keepalive = setInterval(() => {
    send({ type: "heartbeat" });
  }, 15000);

  let resultText = "";
  let sdkSessionId = null;
  let usage = {};
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
    if (model) options.model = model;
    if (session && session.sdkSessionId) options.resume = session.sdkSessionId;

    options.stderr = (data) => {
      process.stderr.write(`[claude-sdk] ${data}`);
      const clean = cleanStderr(data);
      if (clean.length > 2) {
        send({ type: "progress", text: clean.slice(0, 300) });
      }
    };

    console.log(`[ws-ask ${sessionId}] query (cwd: ${workDir}, model: ${model || "default"}, resume: ${session?.sdkSessionId || "none"})`);

    const conversation = query({ prompt, options });

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

    if (sdkSessionId) {
      sessions.set(sessionId, { sdkSessionId, lastActive: Date.now() });
    }
    evictOldestSession();

    console.log(`[ws-ask ${sessionId}] done, result=${resultText.length} chars`);

    send({
      type: "result",
      text: resultText || "(no output)",
      session_id: sessionId,
      usage: {
        input_tokens: usage.input_tokens || 0,
        output_tokens: usage.output_tokens || 0,
        cache_creation_input_tokens: usage.cache_creation_input_tokens || 0,
        cache_read_input_tokens: usage.cache_read_input_tokens || 0,
      },
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
  const { prompt, system_prompt, cwd, allowed_tools, timeout_ms, model } = msg;

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

  const args = ["-p", fullPrompt, "--output-format", "text"];
  if (model) args.push("--model", model);
  if (allowed_tools && allowed_tools.length > 0) {
    args.push("--allowedTools", allowed_tools.join(","));
  }

  console.log(`[ws-agent] Spawning isolated process (cwd: ${workDir}, timeout: ${timeoutMs}ms)`);

  try {
    const output = await new Promise((resolve, reject) => {
      execFile("claude", args, {
        cwd: workDir,
        timeout: timeoutMs,
        maxBuffer: 10 * 1024 * 1024,
        env: { ...process.env, HOME: "/root", PWD: workDir },
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
