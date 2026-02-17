import { claude } from "@anthropic-ai/claude-code";
import { execFile } from "node:child_process";
import express from "express";

const app = express();
app.use(express.json({ limit: "10mb" }));

const PORT = process.env.SIDECAR_PORT || 3001;
const DEFAULT_CWD = process.env.WORK_DIR || "/home/user/project";

// In-memory session store: sessionId -> messages[]
const sessions = new Map();

// Session TTL: clear sessions older than 30 minutes of inactivity
const SESSION_TTL_MS = 30 * 60 * 1000;

// --- Routes ---

// Health check
app.get("/health", (_req, res) => {
  res.json({ status: "ok", sessions: sessions.size, uptime: process.uptime() });
});

// Main ask endpoint
app.post("/ask", async (req, res) => {
  const { session_id, prompt, cwd, max_turns, system_prompt, allowed_tools } = req.body;

  if (!prompt) {
    return res.status(400).json({ error: "prompt is required" });
  }

  const sessionId = session_id || "default";
  const workDir = cwd || DEFAULT_CWD;

  // Get or create session
  let session = sessions.get(sessionId);
  if (!session) {
    session = { messages: [], lastActive: Date.now() };
    sessions.set(sessionId, session);
  }

  try {
    const options = { cwd: workDir };
    if (max_turns && max_turns > 0) {
      options.maxTurns = max_turns;
    }
    if (allowed_tools) {
      options.allowedTools = allowed_tools;
    }

    // Build the call parameters
    const callParams = {
      prompt,
      messages: session.messages,
      options,
    };

    // Inject system prompt with cache_control for prompt caching.
    // The system prompt contains static content (bootstrap + skills + memory)
    // that rarely changes, so caching it saves significant tokens.
    if (system_prompt) {
      callParams.systemPrompt = system_prompt;
    }

    const result = await claude(callParams);

    // Update session
    session.messages = result.messages;
    session.lastActive = Date.now();

    // Extract usage from the result
    const usage = extractUsage(result);

    res.json({
      text: result.stdout || "",
      session_id: sessionId,
      usage,
    });
  } catch (err) {
    console.error(`Error for session ${sessionId}:`, err.message);

    // Check for rate limit errors
    const is429 = err.message && (
      err.message.includes("429") ||
      err.message.includes("rate_limit") ||
      err.message.includes("Rate limit")
    );

    res.status(is429 ? 429 : 500).json({
      error: err.message,
      session_id: sessionId,
      is_rate_limited: is429,
    });
  }
});

// Isolated agent execution — spawns a separate CLI process per agent.
// Each agent gets its own process with zero shared context/state.
// Used by the orchestrator for independent code review agents.
app.post("/run-agent", async (req, res) => {
  const { prompt, system_prompt, cwd, allowed_tools, timeout_ms } = req.body;

  if (!prompt) {
    return res.status(400).json({ error: "prompt is required" });
  }

  const workDir = cwd || DEFAULT_CWD;
  const timeoutMs = timeout_ms || 600000; // 10 minutes default

  // Build the full prompt: system prompt (DNA) + user prompt (task)
  let fullPrompt = prompt;
  if (system_prompt) {
    fullPrompt = system_prompt + "\n\n---\n\n" + prompt;
  }

  // Build CLI arguments
  const args = ["-p", fullPrompt, "--output-format", "text"];
  if (allowed_tools && allowed_tools.length > 0) {
    args.push("--allowedTools", allowed_tools.join(","));
  }

  console.log(`[run-agent] Spawning isolated claude process (cwd: ${workDir}, timeout: ${timeoutMs}ms)`);

  try {
    const output = await new Promise((resolve, reject) => {
      const proc = execFile("claude", args, {
        cwd: workDir,
        timeout: timeoutMs,
        maxBuffer: 10 * 1024 * 1024, // 10MB output buffer
        env: { ...process.env, HOME: "/root", PWD: workDir },
      }, (err, stdout, stderr) => {
        if (err) {
          if (err.killed) {
            reject(new Error(`Process killed (timeout after ${timeoutMs}ms)`));
          } else {
            // Even on error, there may be useful output
            reject(new Error(err.message + (stderr ? "\nstderr: " + stderr : "")));
          }
          return;
        }
        resolve(stdout);
      });
    });

    console.log(`[run-agent] Process completed (${output.length} bytes output)`);

    res.json({
      text: output,
      is_rate_limited: false,
    });
  } catch (err) {
    console.error(`[run-agent] Error:`, err.message);

    const is429 = err.message && (
      err.message.includes("429") ||
      err.message.includes("rate_limit") ||
      err.message.includes("Rate limit")
    );

    res.status(is429 ? 429 : 500).json({
      error: err.message,
      is_rate_limited: is429,
    });
  }
});

// Reset a session
app.post("/reset", (req, res) => {
  const { session_id } = req.body;
  const sessionId = session_id || "default";

  sessions.delete(sessionId);
  console.log(`Session ${sessionId} cleared`);

  res.json({ status: "ok", session_id: sessionId });
});

// List active sessions (for debugging)
app.get("/sessions", (_req, res) => {
  const list = [];
  for (const [id, session] of sessions) {
    list.push({
      id,
      messages: session.messages.length,
      lastActive: new Date(session.lastActive).toISOString(),
    });
  }
  res.json(list);
});

// --- Helpers ---

function extractUsage(result) {
  // The SDK returns usage info in the result or in individual messages.
  // Try to find cumulative usage.
  let inputTokens = 0;
  let outputTokens = 0;
  let cacheCreation = 0;
  let cacheRead = 0;

  if (result.usage) {
    return {
      input_tokens: result.usage.input_tokens || 0,
      output_tokens: result.usage.output_tokens || 0,
      cache_creation_input_tokens: result.usage.cache_creation_input_tokens || 0,
      cache_read_input_tokens: result.usage.cache_read_input_tokens || 0,
    };
  }

  // Walk through messages looking for usage data
  if (result.messages) {
    for (const msg of result.messages) {
      if (msg.usage) {
        inputTokens += msg.usage.input_tokens || 0;
        outputTokens += msg.usage.output_tokens || 0;
        cacheCreation += msg.usage.cache_creation_input_tokens || 0;
        cacheRead += msg.usage.cache_read_input_tokens || 0;
      }
    }
  }

  return {
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    cache_creation_input_tokens: cacheCreation,
    cache_read_input_tokens: cacheRead,
  };
}

// --- Session cleanup ---

setInterval(() => {
  const now = Date.now();
  for (const [id, session] of sessions) {
    if (now - session.lastActive > SESSION_TTL_MS) {
      sessions.delete(id);
      console.log(`Session ${id} expired (inactive)`);
    }
  }
}, 60 * 1000); // check every minute

// --- Start ---

app.listen(PORT, "0.0.0.0", () => {
  console.log(`Sidecar listening on port ${PORT}`);
  console.log(`Default working directory: ${DEFAULT_CWD}`);
});
