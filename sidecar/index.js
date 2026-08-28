import { query } from "@anthropic-ai/claude-agent-sdk";
import { execFile, spawn, spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { createServer } from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
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
  // Qwen3.8 via local vLLM (compose --max-model-len 16384)
  "qwen3.8": 16_384,
  "qwen3.8-27b": 16_384,
  // xAI Grok (Anthropic-compat at https://api.x.ai)
  "grok-4.6": 2_000_000,
  "grok-4.5": 2_000_000,
};

function contextWindowFor(modelName) {
  if (!modelName) return DEFAULT_CONTEXT_WINDOW;
  if (CONTEXT_WINDOWS[modelName]) return CONTEXT_WINDOWS[modelName];
  // Strip [1m] / [200k] suffixes for a lookup fallback.
  const base = modelName.replace(/\[[^\]]*\]$/, "");
  if (CONTEXT_WINDOWS[base]) return CONTEXT_WINDOWS[base];
  if (modelName.startsWith("qwen3.8")) return 16_384;
  return DEFAULT_CONTEXT_WINDOW;
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

// SDK / provider internals that must not appear in Telegram.
function isSdkNoise(text) {
  if (!text) return true;
  const t = text.trim();
  if (t.length < 2) return true;
  if (t === "Session initialized") return true;
  if (/thinking_tokens/i.test(t)) return true;
  if (/unrecognized_model/i.test(t)) return true;
  if (/^\[system:/.test(t)) return true;
  if (/^\[claude-code:/.test(t)) return true;
  return false;
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
  // xAI Anthropic SDK compat: base_url is https://api.x.ai (SDK appends /v1/messages).
  // Accept XAI_API_KEY (official) or GROK_API_KEY (alias used in this project's .env).
  grok: {
    baseURL: "https://api.x.ai",
    apiKeyEnv: "XAI_API_KEY",
    apiKeyEnvAlt: "GROK_API_KEY",
    defaultModel: "grok-4.5",
  },
  // Local Ollama (host GPU). Anthropic Messages API at the Ollama root
  // (NOT /v1 — that path is OpenAI-compat). Auth token is required by
  // Claude Code but ignored by Ollama.
  ollama: {
    baseURL: "http://host.docker.internal:11434",
    baseURLEnv: "OLLAMA_URL",
    dummyAuth: "ollama",
    defaultModel: "qwen3.8",
    defaultModelEnv: "OLLAMA_CHAT_MODEL",
    setModelEnvVars: true,
  },
  // Local vLLM OpenAI image. Also serves Anthropic /v1/messages at the
  // container root (Claude Code appends /v1/messages). Dummy auth is
  // required by Claude Code and ignored by vLLM unless --api-key is set.
  vllm: {
    baseURL: "http://host.docker.internal:8005",
    baseURLEnv: "VLLM_URL",
    dummyAuth: "dummy",
    defaultModel: "qwen3.8-27b",
    defaultModelEnv: "VLLM_MODEL",
    setModelEnvVars: true,
    setApiKey: true,
    // Claude Code defaults max_completion_tokens to 32000 for unknown
    // model IDs. vLLM --max-model-len 16384 rejects that (output cannot
    // exceed the whole window). Cap output; keep context in sync.
    maxOutputTokensEnv: "VLLM_MAX_OUTPUT_TOKENS",
    maxOutputTokensDefault: "2048",
    maxContextTokensEnv: "VLLM_CONTEXT_WINDOW",
    maxContextTokensDefault: "16384",
  },
};

// Keys for third-party providers are read from the project .env at request
// time. Docker Compose interpolates ${DEEPSEEK_API_KEY} from the *host shell
// first*, so a stale exported key silently overrides the file. Claude Code
// also copies host ~/.claude into the container and will reuse a cached
// credential unless we isolate CLAUDE_CONFIG_DIR for the provider subprocess.
const GATEWAY_ENV_FILE = process.env.GATEWAY_ENV_FILE || "/workspace/projects/claude-gateway/.env";
const PROVIDER_CONFIG_ROOT = "/root/.claude-providers";
const AUTH_VARS_TO_STRIP = [
  "ANTHROPIC_API_KEY",
  "ANTHROPIC_AUTH_TOKEN",
  "ANTHROPIC_BASE_URL",
  "CLAUDE_CODE_OAUTH_TOKEN",
  "ANTHROPIC_CUSTOM_HEADERS",
];

function parseEnvFile(filePath) {
  const parsed = Object.create(null);
  let text;
  try {
    text = fs.readFileSync(filePath, "utf8");
  } catch {
    return parsed;
  }
  for (const raw of text.split(/\r?\n/)) {
    let line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    if (line.startsWith("export ")) line = line.slice(7).trim();
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const key = line.slice(0, eq).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) continue;
    let val = line.slice(eq + 1).trim();
    if (
      (val.startsWith('"') && val.endsWith('"') && val.length >= 2) ||
      (val.startsWith("'") && val.endsWith("'") && val.length >= 2)
    ) {
      val = val.slice(1, -1);
    }
    parsed[key] = val;
  }
  return parsed;
}

function keyLast4(key) {
  if (!key) return "none";
  return key.length <= 4 ? key : key.slice(-4);
}

function envFileOrProc(name, fallback) {
  if (!name) return fallback;
  const file = parseEnvFile(GATEWAY_ENV_FILE);
  const fromFile = (file[name] || "").trim();
  if (fromFile) return fromFile;
  const fromProc = (process.env[name] || "").trim();
  if (fromProc) return fromProc;
  return fallback;
}

function resolveProviderApiKey(p) {
  if (p.dummyAuth) {
    return { apiKey: p.dummyAuth, source: "dummy" };
  }
  const file = parseEnvFile(GATEWAY_ENV_FILE);
  const fromFile = (file[p.apiKeyEnv] || (p.apiKeyEnvAlt && file[p.apiKeyEnvAlt]) || "").trim();
  const fromProc = (process.env[p.apiKeyEnv] || (p.apiKeyEnvAlt && process.env[p.apiKeyEnvAlt]) || "").trim();
  if (fromFile) {
    if (fromProc && fromFile !== fromProc) {
      console.warn(
        `[providers] ${p.apiKeyEnv} process.env last4=${keyLast4(fromProc)} ` +
        `differs from ${GATEWAY_ENV_FILE} last4=${keyLast4(fromFile)}; using .env file`
      );
    }
    return { apiKey: fromFile, source: GATEWAY_ENV_FILE };
  }
  if (fromProc) {
    return { apiKey: fromProc, source: "process.env" };
  }
  return { apiKey: "", source: "" };
}

const MINIMAL_CLAUDE_SETTINGS = {
  permissions: {
    allow: ["Bash(*)", "Read(*)", "Write(*)", "Edit(*)", "Glob(*)", "Grep(*)"],
  },
};

function ensureProviderConfigDir(providerName) {
  const dir = path.join(PROVIDER_CONFIG_ROOT, providerName);
  fs.mkdirSync(dir, { recursive: true });
  const dest = path.join(dir, "settings.json");
  // vLLM/Ollama cannot afford SessionStart hooks (environment.md, personality,
  // git status, project registry) or MCP servers — those alone are ~8–12k tokens.
  if (providerName === "vllm" || providerName === "ollama") {
    try {
      fs.writeFileSync(dest, JSON.stringify(MINIMAL_CLAUDE_SETTINGS, null, 2));
    } catch (err) {
      console.warn(`[provider ${providerName}] could not write minimal settings: ${err.message}`);
    }
    return dir;
  }
  const src = "/root/.config/claude-code/settings.json";
  try {
    fs.copyFileSync(src, dest);
  } catch (err) {
    console.warn(`[provider ${providerName}] could not copy settings.json: ${err.message}`);
  }
  return dir;
}

function applyProviderEnv(overlay) {
  const env = { ...process.env, HOME: "/root" };
  for (const name of AUTH_VARS_TO_STRIP) {
    delete env[name];
  }
  if (overlay.configDir) {
    env.CLAUDE_CONFIG_DIR = overlay.configDir;
  }
  Object.assign(env, overlay.env);
  return env;
}

function logProviderKeysAtBoot() {
  for (const [name, p] of Object.entries(PROVIDERS)) {
    const { apiKey, source } = resolveProviderApiKey(p);
    const model = envFileOrProc(p.defaultModelEnv, p.defaultModel);
    const base = envFileOrProc(p.baseURLEnv, p.baseURL);
    if (p.dummyAuth) {
      console.log(`[providers] ${name} ready model=${model} base=${base} auth=dummy`);
      continue;
    }
    if (apiKey) {
      console.log(`[providers] ${name} ready source=${source} last4=${keyLast4(apiKey)} len=${apiKey.length}`);
    } else {
      const needed = p.apiKeyEnvAlt ? `${p.apiKeyEnv} or ${p.apiKeyEnvAlt}` : p.apiKeyEnv;
      console.log(`[providers] ${name} not configured (no ${needed} in ${GATEWAY_ENV_FILE} or process env)`);
    }
  }
}

// buildProviderOverlay returns { env, model, configDir } for a provider, or
// null if no provider was requested. Throws if the provider is unknown or the
// API key is missing — those are caller errors that should surface to the user.
function buildProviderOverlay(providerName, requestedModel) {
  if (!providerName) return null;
  const p = PROVIDERS[providerName];
  if (!p) {
    throw new Error(`Unknown provider: ${providerName}`);
  }
  const { apiKey, source } = resolveProviderApiKey(p);
  if (!apiKey) {
    const needed = p.apiKeyEnvAlt ? `${p.apiKeyEnv} or ${p.apiKeyEnvAlt}` : p.apiKeyEnv;
    throw new Error(`Missing ${needed} in ${GATEWAY_ENV_FILE} (and process env) for provider ${providerName}`);
  }
  const configDir = ensureProviderConfigDir(providerName);
  // Prefer the container env for OLLAMA_URL so compose can force
  // host.docker.internal even if .env still says localhost.
  const baseURL = (
    (p.baseURLEnv && process.env[p.baseURLEnv]) ||
    envFileOrProc(p.baseURLEnv, p.baseURL)
  ).replace(/\/$/, "");
  const model = requestedModel || envFileOrProc(p.defaultModelEnv, p.defaultModel);
  console.log(
    `[provider ${providerName}] key source=${source} last4=${keyLast4(apiKey)} ` +
    `len=${apiKey.length} model=${model} base=${baseURL} configDir=${configDir}`
  );
  const env = {
    ANTHROPIC_BASE_URL: baseURL,
    ANTHROPIC_AUTH_TOKEN: apiKey,
  };
  if (p.setModelEnvVars && model) {
    env.ANTHROPIC_MODEL = model;
    env.ANTHROPIC_DEFAULT_OPUS_MODEL = model;
    env.ANTHROPIC_DEFAULT_SONNET_MODEL = model;
    env.ANTHROPIC_DEFAULT_HAIKU_MODEL = model;
    env.CLAUDE_CODE_SUBAGENT_MODEL = model;
    env.CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS = "1";
  }
  if (p.setApiKey) {
    env.ANTHROPIC_API_KEY = apiKey;
  }
  const maxOut = envFileOrProc(p.maxOutputTokensEnv, p.maxOutputTokensDefault);
  if (maxOut) {
    env.CLAUDE_CODE_MAX_OUTPUT_TOKENS = String(maxOut);
  }
  const maxCtx = envFileOrProc(p.maxContextTokensEnv, p.maxContextTokensDefault);
  if (maxCtx) {
    env.CLAUDE_CODE_MAX_CONTEXT_TOKENS = String(maxCtx);
  }
  if (providerName === "vllm") {
    env.CLAUDE_CODE_ATTRIBUTION_HEADER = "0";
  }
  console.log(
    `[provider ${providerName}] max_out=${env.CLAUDE_CODE_MAX_OUTPUT_TOKENS || "default"} ` +
    `ctx=${env.CLAUDE_CODE_MAX_CONTEXT_TOKENS || "default"}`
  );
  return {
    env,
    model,
    configDir,
  };
}

// --- SuperGrok (Grok Build CLI) ------------------------------------------
// SuperGrok / X Premium+ is a subscription login, not an API key. The CLI
// stores the session in ~/.grok/auth.json after `grok login --device-auth`.
// We strip XAI_API_KEY / GROK_API_KEY from the child env so Grok cannot
// silently fall back to pay-per-token API billing.

const GROK_HOME = process.env.GROK_HOME || "/root/.grok";
const GROK_AUTH = path.join(GROK_HOME, "auth.json");

function grokSubscriptionEnv() {
  const env = { ...process.env, HOME: "/root", GROK_HOME };
  delete env.XAI_API_KEY;
  delete env.GROK_API_KEY;
  return env;
}

function grokBinaryExists() {
  try {
    fs.accessSync("/usr/local/bin/grok", fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function grokIsLoggedIn() {
  try {
    const st = fs.statSync(GROK_AUTH);
    return st.isFile() && st.size > 20;
  } catch {
    return false;
  }
}

function parseDeviceAuth(text) {
  const urlMatch = text.match(/https?:\/\/[^\s"'<>]+/i);
  const codeMatch =
    text.match(/\b(?:code|user[_ ]?code)\s*[:=]\s*([A-Z0-9][A-Z0-9-]{3,})\b/i) ||
    text.match(/\b([A-Z0-9]{4}-[A-Z0-9]{4,})\b/);
  return {
    url: urlMatch ? urlMatch[0].replace(/[.,;]+$/, "") : "",
    code: codeMatch ? codeMatch[1] : "",
  };
}

// Only the model's visible answer belongs on Telegram. Grok Build's
// streaming-json also emits thoughts, tool calls, command menus, usage,
// and file dumps — those must never be forwarded.
function grokVisibleText(ev) {
  if (!ev || typeof ev !== "object") return "";
  if (ev.type !== "text") return "";
  if (typeof ev.data === "string") return ev.data;
  if (typeof ev.text === "string") return ev.text;
  return "";
}

function grokToolLabel(ev) {
  if (!ev || ev.type !== "tool_call") return "";
  return String(ev.title || ev.toolName || ev.kind || "tool").slice(0, 60);
}

const GROK_SESSION_MAP = path.join(GROK_HOME, "gateway-sessions.json");

function loadGrokSessionMap() {
  try {
    return JSON.parse(fs.readFileSync(GROK_SESSION_MAP, "utf8"));
  } catch {
    return {};
  }
}

function saveGrokSessionMap(map) {
  try {
    fs.writeFileSync(GROK_SESSION_MAP, JSON.stringify(map), { mode: 0o600 });
  } catch (err) {
    console.warn("[grok] failed to persist session map:", err.message);
  }
}

// Grok --session-id must be a UUID. Gateway IDs look like "grok:tg-123".
// Hash them deterministically so the same Telegram chat resumes after restart.
function grokSessionUUID(seed) {
  const hex = createHash("sha256").update(String(seed)).digest("hex").slice(0, 32);
  const variant = ((parseInt(hex.slice(16, 18), 16) & 0x3f) | 0x80).toString(16).padStart(2, "0");
  return [
    hex.slice(0, 8),
    hex.slice(8, 12),
    "5" + hex.slice(13, 16),
    variant + hex.slice(18, 20),
    hex.slice(20, 32),
  ].join("-");
}

function grokSessionFlags(sessionId) {
  const map = loadGrokSessionMap();
  let rec = map[sessionId];
  if (!rec || !rec.uuid) {
    rec = { uuid: grokSessionUUID(sessionId), created: false };
    map[sessionId] = rec;
    saveGrokSessionMap(map);
  }
  if (rec.created) {
    return { flags: ["--resume", rec.uuid], rec, map };
  }
  return { flags: ["--session-id", rec.uuid], rec, map };
}

function markGrokSessionCreated(sessionId) {
  const map = loadGrokSessionMap();
  if (!map[sessionId]) return;
  map[sessionId].created = true;
  saveGrokSessionMap(map);
}

function resetGrokSession(sessionId) {
  const map = loadGrokSessionMap();
  map[sessionId] = { uuid: randomUUID(), created: false };
  saveGrokSessionMap(map);
}

let grokLoginState = {
  proc: null,
  url: "",
  code: "",
  done: true,
  ok: false,
  error: "",
};

function startGrokLogin() {
  if (grokLoginState.proc) {
    return { started: true, already: true };
  }
  if (!grokBinaryExists()) {
    return { started: false, error: "Grok Build CLI is not installed. Rebuild the sidecar." };
  }
  grokLoginState = { proc: null, url: "", code: "", done: false, ok: false, error: "", log: "" };
  const child = spawn("grok", ["login", "--device-auth"], {
    env: grokSubscriptionEnv(),
    stdio: ["ignore", "pipe", "pipe"],
  });
  grokLoginState.proc = child;
  const onData = (buf) => {
    grokLoginState.log += buf.toString();
    const parsed = parseDeviceAuth(grokLoginState.log);
    if (parsed.url) grokLoginState.url = parsed.url;
    if (parsed.code) grokLoginState.code = parsed.code;
  };
  child.stdout.on("data", onData);
  child.stderr.on("data", onData);
  child.on("error", (err) => {
    grokLoginState.done = true;
    grokLoginState.ok = false;
    grokLoginState.error = err.message;
    grokLoginState.proc = null;
  });
  child.on("close", (code) => {
    grokLoginState.done = true;
    grokLoginState.ok = code === 0 && grokIsLoggedIn();
    if (!grokLoginState.ok && !grokLoginState.error) {
      grokLoginState.error = grokIsLoggedIn()
        ? ""
        : `login exited ${code}`;
    }
    grokLoginState.proc = null;
  });
  return { started: true, already: false };
}

// --- Abort tracking ---
// Maps sessionId -> { aborted: false } for in-flight requests.
// The abort flag is checked inside the for-await loop to break early.
const activeRequests = new Map();

// --- Direct vLLM (Anthropic /v1/messages) --------------------------------
// Claude Code cannot fit in a 16k context window. This path sends only the
// user turn plus a short system prompt, and keeps a small rolling history.

const VLLM_SYSTEM =
  "You are a coding agent on this machine. Workspace root is /workspace " +
  "(projects live in /workspace/projects/<name>). Use tools to inspect and " +
  "edit files. Be concise. Prefer small diffs. Do not dump whole files into " +
  "chat. After tools finish, give a short summary of what you did.";

const VLLM_TOOLS = [
  {
    name: "Read",
    description: "Read a file. Optional offset/limit are 1-indexed line numbers.",
    input_schema: {
      type: "object",
      properties: {
        file_path: { type: "string" },
        offset: { type: "integer" },
        limit: { type: "integer" },
      },
      required: ["file_path"],
    },
  },
  {
    name: "Write",
    description: "Write contents to a file, creating parent dirs. Overwrites.",
    input_schema: {
      type: "object",
      properties: {
        file_path: { type: "string" },
        content: { type: "string" },
      },
      required: ["file_path", "content"],
    },
  },
  {
    name: "Edit",
    description: "Replace exact text in a file. old_string must match once unless replace_all.",
    input_schema: {
      type: "object",
      properties: {
        file_path: { type: "string" },
        old_string: { type: "string" },
        new_string: { type: "string" },
        replace_all: { type: "boolean" },
      },
      required: ["file_path", "old_string", "new_string"],
    },
  },
  {
    name: "Bash",
    description: "Run a shell command in the workspace. 60s timeout. Output truncated.",
    input_schema: {
      type: "object",
      properties: {
        command: { type: "string" },
      },
      required: ["command"],
    },
  },
  {
    name: "Glob",
    description: "Find files under a path matching a glob (e.g. **/*.go).",
    input_schema: {
      type: "object",
      properties: {
        pattern: { type: "string" },
        path: { type: "string" },
      },
      required: ["pattern"],
    },
  },
  {
    name: "Grep",
    description: "Search file contents with a regex. Returns matching lines.",
    input_schema: {
      type: "object",
      properties: {
        pattern: { type: "string" },
        path: { type: "string" },
        glob: { type: "string" },
      },
      required: ["pattern"],
    },
  },
];

const VLLM_PATH_PREFIXES = ["/workspace", "/clients/current", "/clients/past", "/tmp"];

function vllmResolvePath(cwd, p) {
  const abs = path.resolve(cwd || "/workspace", p || "");
  if (VLLM_PATH_PREFIXES.some((pre) => abs === pre || abs.startsWith(pre + "/"))) {
    return abs;
  }
  throw new Error(`path not allowed: ${abs}`);
}

function vllmTruncate(s, n) {
  const t = String(s ?? "");
  if (t.length <= n) return t;
  return t.slice(0, n) + `\n… truncated ${t.length - n} chars`;
}

function vllmExecTool(name, input, cwd) {
  const i = input && typeof input === "object" ? input : {};
  try {
    if (name === "Read") {
      const fp = vllmResolvePath(cwd, i.file_path);
      const raw = fs.readFileSync(fp, "utf8");
      const lines = raw.split(/\r?\n/);
      const offset = Math.max(1, parseInt(i.offset, 10) || 1);
      const limit = Math.max(1, parseInt(i.limit, 10) || 200);
      const slice = lines.slice(offset - 1, offset - 1 + limit);
      const numbered = slice.map((ln, idx) => `${offset + idx}|${ln}`).join("\n");
      return numbered || "(empty)";
    }
    if (name === "Write") {
      const fp = vllmResolvePath(cwd, i.file_path);
      fs.mkdirSync(path.dirname(fp), { recursive: true });
      fs.writeFileSync(fp, i.content ?? "");
      return `Wrote ${fp} (${Buffer.byteLength(String(i.content ?? ""), "utf8")} bytes)`;
    }
    if (name === "Edit") {
      const fp = vllmResolvePath(cwd, i.file_path);
      const old = String(i.old_string ?? "");
      const neu = String(i.new_string ?? "");
      let cur = fs.readFileSync(fp, "utf8");
      if (i.replace_all) {
        if (!cur.includes(old)) return `old_string not found in ${fp}`;
        cur = cur.split(old).join(neu);
      } else {
        const n = cur.split(old).length - 1;
        if (n === 0) return `old_string not found in ${fp}`;
        if (n > 1) return `old_string matched ${n} times; set replace_all or make it unique`;
        cur = cur.replace(old, neu);
      }
      fs.writeFileSync(fp, cur);
      return `Edited ${fp}`;
    }
    if (name === "Bash") {
      const result = spawnSync("bash", ["-lc", String(i.command ?? "")], {
        cwd: cwd || "/workspace",
        encoding: "utf8",
        timeout: 60000,
        maxBuffer: 1024 * 1024,
        env: { ...process.env, HOME: "/root", PWD: cwd || "/workspace" },
      });
      const out = (result.stdout || "") + (result.stderr || "");
      const code = result.status;
      return vllmTruncate(`exit ${code}\n${out}`, 8000);
    }
    if (name === "Glob") {
      const root = vllmResolvePath(cwd, i.path || cwd || "/workspace");
      const pat = String(i.pattern || "*");
      const result = spawnSync(
        "bash",
        ["-lc", `find ${JSON.stringify(root)} -type f | head -n 200`],
        { encoding: "utf8", timeout: 15000, maxBuffer: 512 * 1024 }
      );
      const files = (result.stdout || "").split("\n").filter(Boolean);
      const re = globToRegExp(pat);
      const matched = files.filter((f) => re.test(f)).slice(0, 80);
      return matched.join("\n") || "(no matches)";
    }
    if (name === "Grep") {
      const root = vllmResolvePath(cwd, i.path || cwd || "/workspace");
      const pat = String(i.pattern || "");
      const glob = i.glob ? ` --glob ${JSON.stringify(i.glob)}` : "";
      const result = spawnSync(
        "bash",
        ["-lc", `rg -n --no-heading -m 50${glob} ${JSON.stringify(pat)} ${JSON.stringify(root)} || true`],
        { encoding: "utf8", timeout: 20000, maxBuffer: 512 * 1024 }
      );
      return vllmTruncate(result.stdout || "(no matches)", 6000);
    }
    return `unknown tool: ${name}`;
  } catch (err) {
    return `error: ${err.message}`;
  }
}

function globToRegExp(glob) {
  const escaped = glob
    .replace(/[.+^${}()|[\]\\]/g, "\\$&")
    .replace(/\*\*/g, "\0")
    .replace(/\*/g, "[^/]*")
    .replace(/\0/g, ".*")
    .replace(/\?/g, "[^/]");
  return new RegExp(escaped + "$");
}

function vllmVisibleText(json) {
  const blocks = Array.isArray(json?.content) ? json.content : [];
  const texts = [];
  for (const b of blocks) {
    if (b && b.type === "text" && b.text) texts.push(b.text);
  }
  if (texts.length) return texts.join("\n");
  for (const b of blocks) {
    if (b && b.type === "thinking" && b.thinking) return b.thinking;
  }
  return "";
}

function vllmContentLen(c) {
  if (typeof c === "string") return c.length;
  try { return JSON.stringify(c).length; } catch { return 0; }
}

function trimVllmHistory(messages, maxChars) {
  let total = 0;
  for (const m of messages) total += vllmContentLen(m.content);
  while (messages.length > 2 && total > maxChars) {
    const dropped = messages.shift();
    total -= vllmContentLen(dropped.content);
  }
  return messages;
}

function vllmOpenAITools() {
  return VLLM_TOOLS.map((t) => ({
    type: "function",
    function: {
      name: t.name,
      description: t.description,
      parameters: t.input_schema,
    },
  }));
}

async function vllmChat(baseURL, auth, body) {
  const resp = await fetch(`${baseURL}/v1/chat/completions`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      authorization: `Bearer ${auth}`,
    },
    signal: AbortSignal.timeout(180000),
    body: JSON.stringify(body),
  });
  const json = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    const errMsg = json?.error?.message || json?.message || `HTTP ${resp.status}`;
    throw new Error(errMsg);
  }
  return json;
}

async function handleVllmAsk(msg, send) {
  const { session_id, prompt, cwd, max_turns, model } = msg;
  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  const sessionId = session_id || "default";
  const workDir = cwd || DEFAULT_CWD;
  let overlay;
  try {
    overlay = buildProviderOverlay("vllm", model);
  } catch (e) {
    send({ type: "error", error: e.message });
    return;
  }

  const baseURL = overlay.env.ANTHROPIC_BASE_URL.replace(/\/$/, "");
  const modelName = overlay.model;
  const maxOut = parseInt(overlay.env.CLAUDE_CODE_MAX_OUTPUT_TOKENS || "2048", 10) || 2048;
  const auth = overlay.env.ANTHROPIC_AUTH_TOKEN || "dummy";
  const maxTurns = (max_turns && max_turns > 0) ? max_turns : 20;

  const session = sessions.get(sessionId) || { messages: [], lastActive: Date.now() };
  if (!Array.isArray(session.messages)) session.messages = [];
  // Drop leftover Anthropic-format turns from the previous /v1/messages path.
  session.messages = session.messages.filter((m) =>
    m && (m.role === "system" || m.role === "user" || m.role === "assistant" || m.role === "tool")
    && (typeof m.content === "string" || m.content == null || Array.isArray(m.tool_calls) || m.role === "tool")
  );
  session.messages.push({ role: "user", content: String(prompt) });
  session.messages = trimVllmHistory(session.messages, 24000);

  const keepalive = setInterval(() => send({ type: "heartbeat" }), 15000);
  const abortState = { aborted: false };
  activeRequests.set(sessionId, abortState);

  console.log(
    `[vllm-ask ${sessionId}] agent ${baseURL}/v1/chat/completions model=${modelName} ` +
    `max_out=${maxOut} cwd=${workDir} history=${session.messages.length}`
  );

  let inTok = 0;
  let outTok = 0;
  let finalText = "";
  let apiModel = modelName;
  const openaiMessages = [
    { role: "system", content: VLLM_SYSTEM },
    ...session.messages,
  ];

  try {
    for (let turn = 0; turn < maxTurns; turn++) {
      if (abortState.aborted) break;
      const json = await vllmChat(baseURL, auth, {
        model: modelName,
        messages: openaiMessages,
        tools: vllmOpenAITools(),
        tool_choice: "auto",
        max_tokens: maxOut,
        chat_template_kwargs: { enable_thinking: false },
      });
      inTok += json.usage?.prompt_tokens || 0;
      outTok += json.usage?.completion_tokens || 0;
      if (json.model) apiModel = json.model;

      const choice = json.choices && json.choices[0];
      const msgOut = choice?.message || {};
      const toolCalls = Array.isArray(msgOut.tool_calls) ? msgOut.tool_calls : [];
      const text = typeof msgOut.content === "string" ? msgOut.content.trim() : "";
      if (text) {
        send({ type: "progress", text });
        finalText = text;
      }

      openaiMessages.push({
        role: "assistant",
        content: text || null,
        ...(toolCalls.length ? { tool_calls: toolCalls } : {}),
      });
      session.messages = openaiMessages.filter((m) => m.role !== "system");

      if (toolCalls.length === 0) {
        break;
      }

      for (const tc of toolCalls) {
        const toolName = tc.function?.name || tc.name || "unknown";
        let toolInput = {};
        try {
          toolInput = JSON.parse(tc.function?.arguments || tc.arguments || "{}");
        } catch {
          toolInput = {};
        }
        send({ type: "progress", tool: toolName, input: summarizeInput(toolInput) });
        console.log(`[vllm-ask ${sessionId}] tool ${toolName}`);
        const out = vllmExecTool(toolName, toolInput, workDir);
        openaiMessages.push({
          role: "tool",
          tool_call_id: tc.id,
          content: vllmTruncate(out, 8000),
        });
      }
      session.messages = openaiMessages.filter((m) => m.role !== "system");
      session.messages = trimVllmHistory(session.messages, 24000);
      openaiMessages.length = 0;
      openaiMessages.push({ role: "system", content: VLLM_SYSTEM }, ...session.messages);
    }

    session.lastActive = Date.now();
    session.ok = true;
    sessions.set(sessionId, session);
    evictOldestSession();

    console.log(`[vllm-ask ${sessionId}] done in=${inTok} out=${outTok} chars=${(finalText || "").length}`);
    send({
      type: "result",
      text: finalText || "(no output)",
      session_id: sessionId,
      api_model: apiModel,
      usage: {
        input_tokens: inTok,
        output_tokens: outTok,
        cache_creation_input_tokens: 0,
        cache_read_input_tokens: 0,
      },
    });
  } catch (err) {
    if (abortState.aborted) {
      send({ type: "result", text: "(Stopped by user)", session_id: sessionId });
    } else {
      console.error(`[vllm-ask ${sessionId}] error:`, err.message);
      send({ type: "error", error: err.message });
    }
  } finally {
    clearInterval(keepalive);
    activeRequests.delete(sessionId);
  }
}

// --- WebSocket Handlers ---

async function handleAsk(msg, send) {
  const { session_id, prompt, cwd, max_turns, system_prompt, allowed_tools, model, provider } = msg;

  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  if (provider === "grok") {
    await handleGrokAsk(msg, send);
    return;
  }

  // Claude Code's own system prompt + hooks + CLAUDE.md cannot fit in
  // vLLM's 16k window. Talk to /v1/messages directly.
  if (provider === "vllm") {
    await handleVllmAsk(msg, send);
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
    };

    if (max_turns && max_turns > 0) options.maxTurns = max_turns;
    const localGpu = provider === "vllm" || provider === "ollama";
    // vLLM's 16k window cannot afford WebFetch/WebSearch schemas, extra
    // CLAUDE.md trees, or SessionStart hook dumps.
    if (localGpu) {
      options.allowedTools = allowed_tools || [
        "Read", "Write", "Edit", "Bash", "Glob", "Grep",
      ];
      options.settingSources = [];
      options.systemPrompt =
        "You are a coding assistant running locally (Qwen3.8-27B). " +
        "The context window is 16384 tokens. Be concise. Prefer small diffs. " +
        "Do not dump whole files. Answer directly, no long chain-of-thought.";
    } else {
      options.additionalDirectories = ["/clients/current", "/clients/past"];
      options.allowedTools = allowed_tools || [
        "Read", "Write", "Edit", "Bash", "Glob", "Grep",
        "WebFetch", "WebSearch",
      ];
      if (system_prompt) options.systemPrompt = system_prompt;
    }

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
      options.env = applyProviderEnv(overlay);
      options.model = overlay.model;
    } else if (model) {
      options.model = model;
    }

    // vLLM: only resume after a successful turn. Failed 400s left an SDK
    // session whose prompt only grows. Other providers resume unless we
    // marked the last turn as failed.
    const canResume = session?.sdkSessionId && (
      localGpu ? session.ok === true : session.ok !== false
    );
    if (canResume) options.resume = session.sdkSessionId;

    options.stderr = (data) => {
      process.stderr.write(`[claude-sdk] ${data}`);
      const clean = cleanStderr(data);
      if (!isSdkNoise(clean)) {
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
        continue;
      }

      // DeepSeek (and other reasoning models) emit thinking as system
      // events. Those are not user-facing — keep them in the sidecar log.
      if (type === "system") {
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

      // Unknown SDK event types — log only, do not leak into Telegram.
      const preview = JSON.stringify(message).slice(0, 200);
      console.log(`[ws-ask ${sessionId}]   other: ${preview}`);
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

    const resultIsError = /API Error:\s*\d+|maximum context length|max_completion_tokens|Authentication Fails/i.test(resultText || "");
    // Persist updated session state. We keep the entry alive even if we're
    // about to compact, so the pendingSeed survives until the next /ask.
    // Do NOT keep a session that 400'd — resume would only enlarge the prompt.
    if (resultIsError) {
      sessions.delete(sessionId);
      console.log(`[ws-ask ${sessionId}] dropping session after error result`);
    } else if (sdkSessionId) {
      sessions.set(sessionId, {
        sdkSessionId,
        lastActive: Date.now(),
        lastTurnTotal,
        contextWindow: ctxWindow,
        pendingSeed: session?.pendingSeed || null,
        provider: provider || "claude",
        model: apiModel || options.model || null,
        ok: true,
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

      if (sdkSessionId && !/API Error:\s*\d+|maximum context length|max_completion_tokens/i.test(resultText)) {
        sessions.set(sessionId, { sdkSessionId, lastActive: Date.now(), ok: true });
      } else {
        sessions.delete(sessionId);
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

async function handleGrokAsk(msg, send) {
  const { session_id, prompt, cwd, system_prompt, model } = msg;
  const sessionId = session_id || "default";
  const workDir = cwd || DEFAULT_CWD;
  const effectiveModel = model && !String(model).startsWith("claude-") ? model : "grok-4.5";

  if (!grokBinaryExists()) {
    send({ type: "error", error: "Grok Build CLI is not installed in the sidecar. Rebuild the sidecar image." });
    return;
  }
  if (!grokIsLoggedIn()) {
    send({ type: "error", error: "SuperGrok is not signed in. Send /grokauth in Telegram and complete the device login." });
    return;
  }

  let effectivePrompt = prompt;
  if (system_prompt) {
    effectivePrompt = system_prompt + "\n\n---\n\n" + prompt;
  }

  const keepalive = setInterval(() => {
    send({ type: "heartbeat" });
  }, 15000);

  const abortState = { aborted: false, child: null };
  activeRequests.set(sessionId, abortState);

  const sess = grokSessionFlags(sessionId);
  // The gateway injects bootstrap + profile + history — far too large for
  // argv (spawn E2BIG). Grok accepts the same payload via --prompt-file.
  const promptFile = path.join(os.tmpdir(), `grok-prompt-${randomUUID()}.txt`);
  try {
    fs.writeFileSync(promptFile, effectivePrompt, { mode: 0o600 });
  } catch (err) {
    send({ type: "error", error: `failed to write grok prompt file: ${err.message}` });
    clearInterval(keepalive);
    activeRequests.delete(sessionId);
    return;
  }

  const args = [
    "--prompt-file", promptFile,
    "--cwd", workDir,
    ...sess.flags,
    "--always-approve",
    "--output-format", "streaming-json",
    "-m", effectiveModel,
  ];

  console.log(`[ws-ask ${sessionId}] grok-cli (cwd: ${workDir}, model: ${effectiveModel}, session: ${sess.flags.join(" ")}, prompt_bytes: ${Buffer.byteLength(effectivePrompt)})`);

  try {
    const text = await new Promise((resolve, reject) => {
      const child = spawn("grok", args, {
        cwd: workDir,
        env: grokSubscriptionEnv(),
        stdio: ["ignore", "pipe", "pipe"],
      });
      abortState.child = child;

      let assembled = "";
      let stderrRaw = "";
      let lineBuf = "";

      const handleLine = (line) => {
        if (!line) return;
        let ev;
        try {
          ev = JSON.parse(line);
        } catch {
          return;
        }
        const chunk = grokVisibleText(ev);
        if (chunk) {
          assembled += chunk;
          return;
        }
        const tool = grokToolLabel(ev);
        if (tool) {
          send({ type: "progress", tool, input: "" });
        }
      };

      child.stdout.on("data", (d) => {
        lineBuf += d.toString();
        const lines = lineBuf.split("\n");
        lineBuf = lines.pop() || "";
        for (const line of lines) handleLine(line);
      });
      child.stderr.on("data", (d) => {
        stderrRaw += d.toString();
      });
      child.on("error", (err) => reject(err));
      child.on("close", (code) => {
        if (lineBuf) handleLine(lineBuf);
        if (abortState.aborted) {
          reject(new Error("aborted"));
          return;
        }
        const answer = assembled.trim();
        if (code !== 0 && !answer) {
          const detail = stderrRaw.trim().split("\n").slice(-8).join("\n");
          reject(new Error(detail || `grok exited ${code}`));
          return;
        }
        resolve(answer);
      });
    });

    markGrokSessionCreated(sessionId);
    sessions.set(sessionId, {
      sdkSessionId: null,
      lastActive: Date.now(),
      lastTurnTotal: 0,
      contextWindow: CONTEXT_WINDOWS[effectiveModel] || DEFAULT_CONTEXT_WINDOW,
      pendingSeed: null,
      provider: "grok",
      model: effectiveModel,
    });

    send({
      type: "result",
      text,
      session_id: sessionId,
      api_model: effectiveModel,
      usage: {
        input_tokens: 0,
        output_tokens: 0,
        cache_creation_input_tokens: 0,
        cache_read_input_tokens: 0,
      },
    });
  } catch (err) {
    console.error(`[ws-ask ${sessionId}] grok-cli error:`, err.message);
    send({ type: "error", error: err.message, is_rate_limited: is429(err.message) });
  } finally {
    clearInterval(keepalive);
    activeRequests.delete(sessionId);
    try { fs.unlinkSync(promptFile); } catch (_) {}
  }
}

async function handleRunAgent(msg, send) {
  const { prompt, system_prompt, cwd, allowed_tools, timeout_ms, model, provider } = msg;

  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  if (provider === "grok") {
    await handleGrokAsk(msg, send);
    return;
  }
  if (provider === "vllm") {
    await handleVllmAsk(msg, send);
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

  const childEnv = overlay ? applyProviderEnv(overlay) : { ...process.env, HOME: "/root" };
  childEnv.PWD = workDir;

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
    queryOptions.env = applyProviderEnv(overlay);
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
  resetGrokSession(sessionId);
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

app.get("/grok/status", (_req, res) => {
  res.json({
    binary: grokBinaryExists(),
    logged_in: grokIsLoggedIn(),
  });
});

app.post("/grok/login", (_req, res) => {
  const result = startGrokLogin();
  if (!result.started) {
    res.status(503).json(result);
    return;
  }
  res.json({
    started: true,
    already: !!result.already,
    url: grokLoginState.url,
    code: grokLoginState.code,
    done: grokLoginState.done,
    ok: grokLoginState.ok,
    logged_in: grokIsLoggedIn(),
  });
});

app.get("/grok/login", (_req, res) => {
  res.json({
    url: grokLoginState.url,
    code: grokLoginState.code,
    done: grokLoginState.done,
    ok: grokLoginState.ok,
    error: grokLoginState.error,
    logged_in: grokIsLoggedIn(),
  });
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
              if (target.child && typeof target.child.kill === "function") {
                try { target.child.kill("SIGTERM"); } catch (_) {}
              }
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
  logProviderKeysAtBoot();
});

httpServer.timeout = 3600000;
httpServer.requestTimeout = 0;
httpServer.headersTimeout = 120000;
httpServer.keepAliveTimeout = 30000;
