import { claude } from "@anthropic-ai/claude-code";
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
  const { session_id, prompt, cwd, max_turns } = req.body;

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

    const result = await claude({
      prompt,
      messages: session.messages,
      options,
    });

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

    res.status(500).json({
      error: err.message,
      session_id: sessionId,
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
