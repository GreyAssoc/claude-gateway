import { query } from "@anthropic-ai/claude-agent-sdk";
import { execFile, spawn, spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { createRequire } from "node:module";
import dns from "node:dns/promises";
import http, { createServer } from "node:http";
import https from "node:https";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { URL } from "node:url";
import express from "express";
import { WebSocketServer } from "ws";

const app = express();
app.use(express.json({ limit: "10mb" }));

const PORT = process.env.SIDECAR_PORT || 3001;
const DEFAULT_CWD = process.env.WORK_DIR || "/home/user/project";
const require = createRequire(import.meta.url);

// The Agent SDK's "native binary exists but failed to launch / libc" error
// is any spawn errno (ENOENT, EACCES, ENOTDIR, …). A non-executable file,
// a musl binary with no /lib/ld-musl-*, and a missing cwd all look the same.
// Probe real binaries and only hand the SDK one that execs.
let claudeExecutableTask;

function claudeExecutableCandidates() {
  const arch = process.arch === "arm64" ? "arm64" : "x64";
  const names = [
    `@anthropic-ai/claude-agent-sdk-linux-${arch}/claude`,
    `@anthropic-ai/claude-agent-sdk-linux-${arch}-musl/claude`,
  ];
  const out = [];
  const add = (p) => {
    if (!p || out.includes(p)) return;
    out.push(p);
  };
  add(process.env.CLAUDE_CODE_EXECUTABLE);
  for (const name of names) {
    try {
      add(require.resolve(name));
    } catch {
      // Optional platform package not installed.
    }
  }
  // Image-owned binaries. /root/.claude is overwritten at startup from the
  // host mount, so it is last — a host build often cannot exec in Debian.
  add("/usr/local/lib/claude-code/claude");
  add("/usr/local/bin/claude");
  add("/root/.claude/local/bin/claude");
  return out;
}

function prepareWorkDir(workDir) {
  const dir = workDir && String(workDir).trim() ? String(workDir).trim() : DEFAULT_CWD;
  let st;
  try {
    st = fs.statSync(dir);
  } catch (err) {
    if (err && err.code === "ENOENT" && dir.startsWith("/workspace/") && fs.existsSync("/workspace")) {
      fs.mkdirSync(dir, { recursive: true });
      console.warn(`[cwd] created missing working directory ${dir}`);
      return dir;
    }
    const missing = new Error(
      `Working directory does not exist: ${dir}. Claude Code reports a missing directory as a native-binary libc mismatch.`
    );
    missing.code = "ENOENT";
    throw missing;
  }
  if (!st.isDirectory()) {
    throw new Error(`Working directory is not a directory: ${dir}`);
  }
  return dir;
}

function probeClaudeBinary(bin, cwd) {
  return new Promise((resolve) => {
    if (!bin || !fs.existsSync(bin)) {
      resolve({ ok: false, reason: "missing" });
      return;
    }
    try {
      fs.accessSync(bin, fs.constants.X_OK);
    } catch {
      try {
        fs.chmodSync(bin, 0o755);
      } catch {
        // Read-only layer. Probe still reports the real errno.
      }
    }
    let out = "";
    let errText = "";
    let settled = false;
    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(result);
    };
    const child = spawn(bin, ["--version"], {
      cwd: cwd || "/tmp",
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      finish({ ok: false, reason: "timeout" });
    }, 8000);
    child.stdout.on("data", (d) => { out += d; });
    child.stderr.on("data", (d) => { errText += d; });
    child.on("error", (e) => {
      finish({ ok: false, reason: e.code || "error", detail: e.message });
    });
    child.on("exit", (code) => {
      if (code === 0) {
        finish({ ok: true, version: (out || errText).trim().split("\n")[0] });
        return;
      }
      finish({
        ok: false,
        reason: `exit ${code}`,
        detail: (errText || out).trim().slice(0, 240),
      });
    });
  });
}

function resolveClaudeExecutable() {
  if (!claudeExecutableTask) {
    claudeExecutableTask = (async () => {
      const failures = [];
      for (const bin of claudeExecutableCandidates()) {
        const result = await probeClaudeBinary(bin, "/tmp");
        if (result.ok) {
          console.log(`[claude-bin] using ${bin} (${result.version || "ok"})`);
          return { path: bin, failures };
        }
        const why = `${bin}: ${result.reason}${result.detail ? " " + result.detail : ""}`;
        failures.push(why);
        console.warn(`[claude-bin] skip ${why}`);
      }
      console.error(`[claude-bin] no Claude Code binary launched. ${failures.join(" | ")}`);
      return { path: "", failures };
    })();
  }
  return claudeExecutableTask;
}

// Spawn with the real cwd. A missing directory is ENOENT and the SDK blames libc.
const claudeLaunchOk = new Set();
async function assertClaudeLaunch(bin, cwd) {
  const key = `${bin}\0${cwd}`;
  if (claudeLaunchOk.has(key)) return;
  const here = await probeClaudeBinary(bin, cwd);
  if (here.ok) {
    claudeLaunchOk.add(key);
    return;
  }
  const tmp = cwd === "/tmp" ? here : await probeClaudeBinary(bin, "/tmp");
  if (tmp.ok) {
    throw new Error(
      `Claude Code cannot start with working directory ${cwd} (${here.reason || "launch failed"}). ` +
      "The binary itself runs. Claude Code reports this as a libc mismatch."
    );
  }
  throw new Error(
    `Claude Code binary failed to launch (${here.reason || "error"}): ${bin}` +
    (here.detail ? ` ${here.detail}` : "")
  );
}

// --- Compaction config ---------------------------------------------------
//
// When the running token total for a session crosses COMPACT_THRESHOLD of
// the model's context window, the active model writes a summary of the
// conversation and the next /ask starts a fresh SDK session seeded with
// that summary as its first user message. This is the same auto-compact
// behaviour Claude Code applies to its own sessions.
const COMPACT_THRESHOLD = parseFloat(process.env.COMPACT_THRESHOLD || "0.80");
// Qwen/vLLM: compact when input fills this fraction of (context − reserved output).
// 0.70 leaves room for a bad chars/token estimate and for max_tokens. 0.95
// compacted only after vLLM had already rejected the request.
const VLLM_COMPACT_THRESHOLD = parseFloat(process.env.VLLM_COMPACT_THRESHOLD || "0.70");

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
  "deepseek-flash": 1_000_000,
  "deepseek-v4-flash": 1_000_000,
  "deepseek-chat": 128_000,
  // Qwen3.8 via local vLLM (serve --max-model-len 131072)
  "qwen3.8": 131_072,
  "qwen3.8-27b": 131_072,
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
  if (modelName.startsWith("qwen3.8")) return vllmContextLimit();
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
    // Subagents and SDK tool processes must stay on the requested model
    // (V4 Pro or Flash). Without this they fall back to another model.
    setModelEnvVars: true,
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
  // Local vLLM OpenAI image. /v1/* requires Bearer if the serve has
  // --api-key / VLLM_API_KEY (vLLM 0.27 does on this host). Dummy is
  // only a fallback for unauthenticated serves.
  vllm: {
    baseURL: "http://host.docker.internal:18020",
    baseURLEnv: "VLLM_URL",
    apiKeyEnv: "VLLM_API_KEY",
    dummyAuth: "dummy",
    defaultModel: "qwen3.8-27b",
    defaultModelEnv: "VLLM_MODEL",
    setModelEnvVars: true,
    setApiKey: true,
    // Claude Code defaults max_completion_tokens to 32000 for unknown
    // model IDs. Cap output; keep context in sync with vLLM --max-model-len.
    maxOutputTokensEnv: "VLLM_MAX_OUTPUT_TOKENS",
    maxOutputTokensDefault: "16384",
    maxContextTokensEnv: "VLLM_CONTEXT_WINDOW",
    maxContextTokensDefault: "65536",
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

// .env may use BRAVE_API_KEY (docs) or BRAVE_API (what Steve added).
function braveApiKey() {
  return (envFileOrProc("BRAVE_API_KEY", "") || envFileOrProc("BRAVE_API", "") || "").trim();
}

function resolveProviderApiKey(p) {
  const file = parseEnvFile(GATEWAY_ENV_FILE);
  let fromFile = "";
  let fromProc = "";
  if (p.apiKeyEnv) {
    fromFile = (file[p.apiKeyEnv] || (p.apiKeyEnvAlt && file[p.apiKeyEnvAlt]) || "").trim();
    fromProc = (process.env[p.apiKeyEnv] || (p.apiKeyEnvAlt && process.env[p.apiKeyEnvAlt]) || "").trim();
  }
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
  // Unauthenticated vLLM/Ollama still need a Bearer token for some clients;
  // the server ignores it unless --api-key is set.
  if (p.dummyAuth) {
    return { apiKey: p.dummyAuth, source: "dummy" };
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
  return withToolPath(env);
}

function logProviderKeysAtBoot() {
  for (const [name, p] of Object.entries(PROVIDERS)) {
    const { apiKey, source } = resolveProviderApiKey(p);
    const model = envFileOrProc(p.defaultModelEnv, p.defaultModel);
    const base = envFileOrProc(p.baseURLEnv, p.baseURL);
    if (apiKey) {
      console.log(`[providers] ${name} ready source=${source} last4=${keyLast4(apiKey)} len=${apiKey.length} model=${model} base=${base}`);
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

function withToolPath(base, cwd) {
  const env = { ...(base || {}) };
  const extra = [
    "/usr/local/bin", // wrappers (go → local toolchain, no download)
    "/usr/local/go/bin",
    "/root/go/bin",
    "/root/.claude/local/bin",
    "/root/.grok/bin",
  ];
  const current = String(env.PATH || process.env.PATH || "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin");
  const parts = current.split(":").filter(Boolean);
  for (let i = extra.length - 1; i >= 0; i--) {
    const d = extra[i];
    const idx = parts.indexOf(d);
    if (idx >= 0) parts.splice(idx, 1);
    parts.unshift(d);
  }
  env.PATH = parts.join(":");
  env.GOPATH = env.GOPATH || "/root/go";
  env.GOROOT = env.GOROOT || "/usr/local/go";
  // Never inherit auto — that downloads a newer patch from go.dev and hangs.
  env.GOTOOLCHAIN = "local";
  env.GOSUMDB = "off";
  env.GOTELEMETRY = "off";
  env.GIT_TERMINAL_PROMPT = "0";
  env.CLAUDE_CODE_DISABLE_SANDBOX = "1";
  env.GOCACHE = env.GOCACHE || process.env.GOCACHE || "/root/.cache/go-build";
  env.GOMODCACHE = env.GOMODCACHE || process.env.GOMODCACHE || "/root/go/pkg/mod";
  env.GOFLAGS = vllmGoFlags(cwd || env.PWD);
  try {
    const root = cwd || env.PWD;
    if (root && fs.existsSync(path.join(root, "go.mod")) && fs.existsSync(path.join(root, "vendor"))) {
      env.GOPROXY = "off";
    }
  } catch (_) { /* ignore */ }
  return env;
}

function grokSubscriptionEnv() {
  const env = { ...process.env, HOME: "/root", GROK_HOME };
  delete env.XAI_API_KEY;
  delete env.GROK_API_KEY;
  return withToolPath(env);
}

// stampChildModelEnv records the model of this ask on every process the
// agent starts. The grey-website orchestrator reads AGENT_PROVIDER and
// AGENT_MODEL, so quality control, repair, and any other child stay on
// the model selected for the parent. An empty provider clears a value
// inherited from the sidecar process itself.
function stampChildModelEnv(env, provider, model) {
  if (!env || typeof env !== "object") return env;
  const p = String(provider || "").trim();
  const m = String(model || "").trim();
  if (!p) {
    delete env.AGENT_PROVIDER;
    delete env.AGENT_MODEL;
    return env;
  }
  env.AGENT_PROVIDER = p;
  if (m) env.AGENT_MODEL = m;
  else delete env.AGENT_MODEL;
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

// The CLI's own text says `grok login --device-code`. That signs in whichever
// machine runs the command. The sidecar keeps its session in its own
// /root/.grok volume, so the Telegram command is the one that writes it.
function grokSignInError(message) {
  const text = String(message || "");
  if (/not signed in|grok login --device-code|Headless mode requires a grok\.com session/i.test(text)) {
    return "SuperGrok is not signed in inside the sidecar. Send /grokauth in Telegram and approve the device login. A grok login on another machine does not sign this sidecar in.";
  }
  return text;
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

// Visible answer chunks (final Telegram message). Thoughts are separate.
function grokVisibleText(ev) {
  if (!ev || typeof ev !== "object") return "";
  if (ev.type !== "text") return "";
  if (typeof ev.data === "string") return ev.data;
  if (typeof ev.text === "string") return ev.text;
  return "";
}

function grokThoughtText(ev) {
  if (!ev || typeof ev !== "object") return "";
  if (ev.type !== "thought" && ev.type !== "thinking") return "";
  if (typeof ev.data === "string") return ev.data;
  if (typeof ev.text === "string") return ev.text;
  if (typeof ev.thought === "string") return ev.thought;
  return "";
}

function sendThinkingProgress(send, text) {
  // Do not trim: Grok streams leading spaces as part of each token.
  // Do not prefix 💭 here — callers add it once per thinking burst.
  if (text == null || text === "") return;
  send({ type: "progress", text: String(text) });
}

function stripThinkTags(s) {
  if (!s) return s;
  return String(s).replace(/<think>\s*[\s\S]*?<\/think>/gi, "").trim();
}

// DeepSeek / Claude SDK: thinking lives in assistant content blocks
// (type=thinking) and sometimes on system events.
function sdkThinkingText(message) {
  if (!message || typeof message !== "object") return "";
  const content = message.content || message.message?.content;
  if (Array.isArray(content)) {
    const parts = [];
    for (const b of content) {
      if (!b || typeof b !== "object") continue;
      if (b.type === "thinking" || b.type === "reasoning") {
        const t = b.thinking || b.reasoning || b.text;
        if (typeof t === "string" && t.trim()) parts.push(t);
      }
    }
    if (parts.length) return parts.join("");
  }
  if (message.type === "system") {
    if (message.subtype === "init" || message.subtype === "compact_boundary") return "";
    const t = message.thinking || message.reasoning
      || (typeof message.content === "string" ? message.content : "")
      || (typeof message.message === "string" ? message.message : "");
    if (t && !isSdkNoise(t)) return t;
  }
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
// Maps sessionId -> { aborted, child, httpReq } for in-flight requests.
// Abort must destroy the vLLM HTTP socket; otherwise the 3090 keeps generating
// after Telegram already thinks the agent finished.
const activeRequests = new Map();

function killSpawned(child) {
  if (!child || !child.pid) return;
  const pid = child.pid;
  // Immediate SIGKILL — hung `go env` / GOPROXY DNS does not die on SIGTERM,
  // and a 2s grace left the module-cache lock held for the next tool call.
  try { process.kill(-pid, "SIGKILL"); } catch (_) {
    try { child.kill("SIGKILL"); } catch (_) {}
  }
}

function markAborted(abortState) {
  if (!abortState) return;
  // Drops the vLLM HTTP call and any GPU ticket still waiting. A bare
  // req.destroy() after streaming has started does not emit 'error' or
  // 'end', so this also rejects via abortState.cancel.
  abortInFlight(abortState);
  if (abortState.child) {
    killSpawned(abortState.child);
    abortState.child = null;
  }
}

// --- Direct vLLM (Anthropic /v1/messages) --------------------------------
// Claude Code's own system prompt + hooks still blow a local Qwen window.
// This path sends only the user turn plus a short system prompt, and keeps
// a rolling history sized for vLLM --max-model-len 65536.

const VLLM_SYSTEM =
  "You are a coding agent on this machine. " +
  "If WORKING DIRECTORY is named in the prompt, that is the project — you are already there. " +
  "Do not glob /workspace, do not ls /workspace/projects, do not search for the project. " +
  "Otherwise workspace root is /workspace (projects live in /workspace/projects/<name>). " +
  "Prefer small diffs. Do not dump whole files into chat. " +
  "Thinking is shown live then discarded — it is NOT the user-facing answer. " +
  "CHAT vs WORK: if the user is greeting, planning, or saying let's work on a project " +
  "without naming a file, bug, test, diff, or deploy — answer in plain text and STOP. " +
  "Do not run git, glob, or a repo tour. Ask what they want next. " +
  "Only CALL tools (Glob, Grep, Read, Bash, Edit, Write) when the request requires " +
  "inspecting or changing something concrete. After tools, write a complete visible answer. " +
  "Never end a turn with empty visible text. " +
  "Go: no output and exit 0 means SUCCESS. First compile can take ~60s with no logs. " +
  "Do not run go env/go version/GOPROXY unless go printed an error. " +
  "GPU queued means another agent is using the model — wait, do not start a toolchain investigation. " +
  "Google Workspace is available: GmailSearch/GmailThread/GmailSend, " +
  "CalendarList/CalendarAdd, DriveSearch/DriveLs/DriveDownload/DriveUpload, " +
  "SheetsRead. The internet is available: WebSearch (find pages) then WebFetch " +
  "(read a URL). Now returns today's date in Europe/London. ClientsFind searches " +
  "/clients/current and /clients/past. When asked about email, the web, a URL, " +
  "or a client job, CALL those tools. Do not say you cannot access email or the internet.";

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
    description: "Run a shell command in the working directory (cwd). 5 minute timeout. Output truncated. Prefer WebFetch/WebSearch over curl for public web pages.",
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
    description: "Find files under a path matching a glob (e.g. **/*.go). Default path is the working directory. If WORKING DIRECTORY is set, do not pass /workspace.",
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
  {
    name: "GmailSearch",
    description: "Search the user's Gmail. query is Gmail search syntax (e.g. is:unread newer_than:12h, in:sent from:me). max defaults to 10, cap 25. Page long ranges with newer_than/older_than — never pull 12 months in one call. Never say you cannot access email — call this.",
    input_schema: {
      type: "object",
      properties: {
        query: { type: "string" },
        max: { type: "integer" },
        label: { type: "string" },
      },
      required: ["query"],
    },
  },
  {
    name: "GmailThread",
    description: "Read a full Gmail thread by thread or message id from GmailSearch.",
    input_schema: {
      type: "object",
      properties: {
        id: { type: "string" },
      },
      required: ["id"],
    },
  },
  {
    name: "GmailSend",
    description: "Send an email from the user's account. Only when the user explicitly asked to send.",
    input_schema: {
      type: "object",
      properties: {
        to: { type: "string" },
        subject: { type: "string" },
        body: { type: "string" },
        attach: { type: "string", description: "Optional local file path" },
      },
      required: ["to", "subject", "body"],
    },
  },
  {
    name: "CalendarList",
    description: "List calendar events. Default is today. days=7 for the next week.",
    input_schema: {
      type: "object",
      properties: {
        from: { type: "string" },
        to: { type: "string" },
        days: { type: "integer" },
        max: { type: "integer" },
        query: { type: "string" },
      },
    },
  },
  {
    name: "CalendarAdd",
    description: "Create a calendar event. start like '2026-02-25 14:00', duration like '1h'.",
    input_schema: {
      type: "object",
      properties: {
        title: { type: "string" },
        start: { type: "string" },
        duration: { type: "string" },
        description: { type: "string" },
      },
      required: ["title", "start"],
    },
  },
  {
    name: "DriveSearch",
    description: "Search Google Drive. query is Drive search syntax, e.g. name contains \"report\".",
    input_schema: {
      type: "object",
      properties: {
        query: { type: "string" },
        max: { type: "integer" },
      },
      required: ["query"],
    },
  },
  {
    name: "DriveLs",
    description: "List files in a Drive folder. Omit parent for root.",
    input_schema: {
      type: "object",
      properties: {
        parent: { type: "string" },
        max: { type: "integer" },
      },
    },
  },
  {
    name: "DriveDownload",
    description: "Download a Drive file to a local path under /workspace or /tmp.",
    input_schema: {
      type: "object",
      properties: {
        file_id: { type: "string" },
        out: { type: "string" },
      },
      required: ["file_id", "out"],
    },
  },
  {
    name: "DriveUpload",
    description: "Upload a local file to the one writable Drive folder.",
    input_schema: {
      type: "object",
      properties: {
        path: { type: "string" },
        parent: { type: "string" },
        name: { type: "string" },
      },
      required: ["path", "parent"],
    },
  },
  {
    name: "SheetsRead",
    description: "Read a Google Sheets range. Sheets are read-only.",
    input_schema: {
      type: "object",
      properties: {
        spreadsheet_id: { type: "string" },
        range: { type: "string" },
      },
      required: ["spreadsheet_id", "range"],
    },
  },
  {
    name: "WebSearch",
    description: "Search the public internet. Returns titles, URLs, and snippets. Use this instead of saying you cannot access the web. Then WebFetch promising URLs.",
    input_schema: {
      type: "object",
      properties: {
        query: { type: "string" },
        max: { type: "integer", description: "Max results, default 8, cap 12" },
      },
      required: ["query"],
    },
  },
  {
    name: "WebFetch",
    description: "Fetch a public http(s) URL and return extracted text. Use after WebSearch or when the user gives a link. Private/localhost URLs are blocked.",
    input_schema: {
      type: "object",
      properties: {
        url: { type: "string" },
        extract: { type: "string", description: "Optional hint of what to focus on (returned as a header; full text still included)" },
      },
      required: ["url"],
    },
  },
  {
    name: "Now",
    description: "Current date and time in Europe/London and UTC. Use before 'today', 'this morning', or scheduling.",
    input_schema: { type: "object", properties: {} },
  },
  {
    name: "ClientsFind",
    description: "Find a Grey & Associates client/job folder under /clients/current and /clients/past by name, road, or abbreviation.",
    input_schema: {
      type: "object",
      properties: {
        query: { type: "string" },
      },
      required: ["query"],
    },
  },
  {
    name: "RunReviewTeam",
    description: "Run a named review team as isolated agents using this local model (not the claude CLI). Teams: go-dev-team, go-sec-team, fe-dev-team, fe-sec-team, design, integration. Use this instead of spawning `claude`.",
    input_schema: {
      type: "object",
      properties: {
        team: { type: "string" },
        task: { type: "string", description: "What to review (paths, diff, feature)" },
      },
      required: ["team", "task"],
    },
  },
];

const HARNESS_SKILLS_DIR = process.env.HARNESS_SKILLS_DIR || "/app/harness/skills";
const DRIVE_WRITABLE_FOLDER = "1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw";

function parseYAMLBool(v) {
  const s = String(v || "").trim().replace(/^['"]|['"]$/g, "").toLowerCase();
  return s === "true" || s === "yes" || s === "1";
}

function parseSkillMD(content, filename) {
  const skill = {
    name: String(filename || "").replace(/^skill-/i, "").replace(/\.md$/i, ""),
    description: "",
    always: false,
    harness: false,
    content: content,
  };
  const trimmed = String(content || "").trim();
  if (!trimmed.startsWith("---")) return skill;
  const rest = trimmed.slice(3);
  const endIdx = rest.indexOf("---");
  if (endIdx <= 0) return skill;
  const front = rest.slice(0, endIdx);
  skill.content = rest.slice(endIdx + 3).trim();
  for (const raw of front.split("\n")) {
    const line = raw.trim();
    const colon = line.indexOf(":");
    if (colon <= 0) continue;
    const key = line.slice(0, colon).trim().toLowerCase();
    const val = line.slice(colon + 1).trim().replace(/^['"]|['"]$/g, "");
    if (key === "name") skill.name = val;
    else if (key === "description") skill.description = val;
    else if (key === "always") skill.always = parseYAMLBool(val);
    else if (key === "harness") skill.harness = parseYAMLBool(val);
  }
  return skill;
}

function loadHarnessSkills() {
  let entries;
  try {
    entries = fs.readdirSync(HARNESS_SKILLS_DIR);
  } catch {
    return [];
  }
  const skills = [];
  for (const name of entries) {
    if (!/skill/i.test(name) || !name.toLowerCase().endsWith(".md")) continue;
    try {
      const raw = fs.readFileSync(path.join(HARNESS_SKILLS_DIR, name), "utf8");
      skills.push(parseSkillMD(raw, name));
    } catch (err) {
      console.warn(`[harness] skip ${name}: ${err.message}`);
    }
  }
  return skills;
}

function formatHarnessSkillsForVllm() {
  const skills = loadHarnessSkills();
  if (!skills.length) return "";
  const parts = [];
  const indexLines = skills.map((s) => `- ${s.name}: ${s.description || s.name}`);
  parts.push(
    "<harness_skills>\nUse these tools/workflows. Match by intent.\n" +
    indexLines.join("\n") +
    "\n</harness_skills>"
  );
  for (const s of skills) {
    if (!s.always || !s.content) continue;
    parts.push(`<skill name="${s.name}">\n${s.content}\n</skill>`);
  }
  return parts.join("\n\n");
}

function vllmPinnedCwd(cwd) {
  const p = String(cwd || "").replace(/\/+$/, "");
  if (p.startsWith("/workspace/projects/") && p.length > "/workspace/projects/".length) {
    return p;
  }
  return "";
}

function vllmCwdBanner(workDir) {
  const pinned = vllmPinnedCwd(workDir);
  if (pinned) {
    return (
      `WORKING DIRECTORY is ${pinned}. You are already there. ` +
      `Do not glob /workspace, do not ls /workspace/projects, do not search for this project, ` +
      `do not list sibling repos. Read, Glob, Grep, and Bash start from this directory.`
    );
  }
  return "";
}

function vllmClampSearchRoot(cwd, requested) {
  const pinned = vllmPinnedCwd(cwd);
  const root = vllmResolvePath(cwd, requested || cwd || "/workspace");
  if (!pinned) return root;
  const norm = String(root || "").replace(/\/+$/, "");
  if (norm === "/workspace" || norm === "/workspace/projects") {
    return pinned;
  }
  return root;
}

function vllmSystemForRequest(extra, workDir) {
  const parts = [];
  const banner = vllmCwdBanner(workDir);
  if (banner) parts.push(banner);
  parts.push(VLLM_SYSTEM);
  const harness = formatHarnessSkillsForVllm();
  if (harness) parts.push(harness);
  const extraTrim = String(extra || "").trim();
  if (extraTrim && extraTrim !== VLLM_SYSTEM) parts.push(extraTrim);
  return parts.join("\n\n");
}

function gogAuthHint(out) {
  const s = String(out || "");
  if (/not authenticated|token.*(missing|expired|invalid)|oauth/i.test(s)) {
    return s + "\nTell the user to run /googleauth in Telegram and paste the code.";
  }
  return s;
}

function gogBannedReason(argsOrCmd) {
  // Tokenise so a search query containing the word "delete" is not blocked.
  const tokens = Array.isArray(argsOrCmd)
    ? argsOrCmd.map((a) => String(a).toLowerCase())
    : (String(argsOrCmd || "").toLowerCase().match(/[a-z0-9_-]+/g) || []);
  let i = tokens.indexOf("gog");
  if (i < 0) i = -1; // named tools pass argv without the binary
  const svc = tokens[i + 1] || "";
  const verb = tokens[i + 2] || "";
  const third = tokens[i + 3] || "";
  if (verb === "delete" || verb === "trash" || third === "delete" || third === "trash") {
    return "delete/trash is blocked";
  }
  if (svc === "sheets" && ["write", "update", "append", "clear", "create"].includes(verb)) {
    return "sheets are read-only";
  }
  if (svc === "drive" && ["delete", "trash", "move"].includes(verb)) {
    return "drive delete/trash/move is blocked";
  }
  return "";
}

async function runGog(args, abortState) {
  const banned = gogBannedReason(args);
  if (banned) return `error: ${banned}`;
  // Must be async. spawnSync blocked the Node event loop so WebSocket pongs
  // stopped and the gateway dropped the session ("socket hang up") mid-Gmail.
  const result = await spawnAsync("gog", args, {
    timeout: 90000,
    maxBuffer: 1024 * 1024,
    env: stampChildModelEnv(
      { ...process.env, HOME: "/root" },
      abortState && abortState.agentProvider,
      abortState && abortState.agentModel,
    ),
  }, abortState);
  const out = ((result.stdout || "") + (result.stderr || "")).trim() || "(empty)";
  if (result.status === 1 && /ENOENT|not found/i.test(out)) {
    return "error: gog binary not found in sidecar — rebuild the sidecar image";
  }
  const body = result.status === 0 ? out : `gog exit ${result.status}\n${out}`;
  return gogAuthHint(vllmTruncate(body, 8000));
}

const WEB_UA =
  "Mozilla/5.0 (compatible; ClaudeGateway/1.0; +https://github.com/you/claude-gateway)";
const WEB_FETCH_TIMEOUT_MS = 20000;
const WEB_MAX_BYTES = 512 * 1024;
const WEB_MAX_REDIRECTS = 5;

function isPrivateIP(ip) {
  const ver = net.isIP(ip);
  if (ver === 4) {
    const p = ip.split(".").map((n) => parseInt(n, 10));
    if (p.length !== 4 || p.some((n) => Number.isNaN(n))) return true;
    if (p[0] === 0 || p[0] === 10 || p[0] === 127) return true;
    if (p[0] === 169 && p[1] === 254) return true;
    if (p[0] === 172 && p[1] >= 16 && p[1] <= 31) return true;
    if (p[0] === 192 && p[1] === 168) return true;
    if (p[0] === 100 && p[1] >= 64 && p[1] <= 127) return true;
    if (p[0] >= 224) return true;
    return false;
  }
  if (ver === 6) {
    const n = ip.toLowerCase();
    if (n === "::1" || n === "::") return true;
    if (n.startsWith("fe80:") || n.startsWith("fc") || n.startsWith("fd")) return true;
    if (n.startsWith("::ffff:")) return isPrivateIP(n.slice(7));
    return false;
  }
  return true;
}

function hostnameBlocked(host) {
  const h = String(host || "").toLowerCase().replace(/\.$/, "");
  if (!h) return true;
  if (h === "localhost" || h.endsWith(".localhost") || h.endsWith(".local") || h.endsWith(".internal")) return true;
  if (h === "host.docker.internal" || h === "sidecar" || h === "gateway" || h === "ollama") return true;
  if (h === "metadata.google.internal") return true;
  return false;
}

async function assertPublicHttpUrl(raw) {
  let u;
  try {
    u = new URL(String(raw || "").trim());
  } catch {
    throw new Error("invalid URL");
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") {
    throw new Error("only http/https URLs are allowed");
  }
  if (u.username || u.password) {
    throw new Error("URLs with credentials are blocked");
  }
  if (hostnameBlocked(u.hostname)) {
    throw new Error(`blocked host ${u.hostname}`);
  }
  if (net.isIP(u.hostname) && isPrivateIP(u.hostname)) {
    throw new Error(`blocked private IP ${u.hostname}`);
  }
  if (!net.isIP(u.hostname)) {
    let answers;
    try {
      answers = await dns.lookup(u.hostname, { all: true });
    } catch (err) {
      throw new Error(`DNS failed for ${u.hostname}: ${err.message}`);
    }
    if (!answers.length) throw new Error(`DNS returned no addresses for ${u.hostname}`);
    for (const a of answers) {
      if (isPrivateIP(a.address)) {
        throw new Error(`blocked private IP ${a.address} for ${u.hostname}`);
      }
    }
  }
  return u;
}

function decodeEntities(s) {
  return String(s || "")
    .replace(/&nbsp;/gi, " ")
    .replace(/&amp;/gi, "&")
    .replace(/&lt;/gi, "<")
    .replace(/&gt;/gi, ">")
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'")
    .replace(/&#x([0-9a-f]+);/gi, (_, h) => {
      try { return String.fromCodePoint(parseInt(h, 16)); } catch { return ""; }
    })
    .replace(/&#(\d+);/g, (_, n) => {
      try { return String.fromCodePoint(parseInt(n, 10)); } catch { return ""; }
    });
}

function htmlToText(html) {
  let s = String(html || "");
  s = s.replace(/<script[\s\S]*?<\/script>/gi, " ");
  s = s.replace(/<style[\s\S]*?<\/style>/gi, " ");
  s = s.replace(/<noscript[\s\S]*?<\/noscript>/gi, " ");
  s = s.replace(/<!--[\s\S]*?-->/g, " ");
  s = s.replace(/<(br|p|div|h[1-6]|li|tr|section|article|header|footer)[^>]*>/gi, "\n");
  s = s.replace(/<\/(p|div|h[1-6]|li|tr|section|article)[^>]*>/gi, "\n");
  s = s.replace(/<[^>]+>/g, " ");
  s = decodeEntities(s);
  s = s.replace(/\r/g, "");
  s = s.replace(/[ \t]+\n/g, "\n").replace(/\n{3,}/g, "\n\n");
  s = s.replace(/[ \t]{2,}/g, " ");
  return s.trim();
}

function unwrapDdgHref(href) {
  try {
    const u = new URL(href, "https://duckduckgo.com");
    const uddg = u.searchParams.get("uddg");
    if (uddg) return uddg;
    return u.href;
  } catch {
    return href;
  }
}

async function httpGetText(urlObj, remainingRedirects) {
  const resp = await fetch(urlObj.href, {
    method: "GET",
    redirect: "manual",
    headers: {
      "user-agent": WEB_UA,
      accept: "text/html,application/xhtml+xml,application/xml,text/plain,application/json;q=0.9,*/*;q=0.1",
    },
    signal: AbortSignal.timeout(WEB_FETCH_TIMEOUT_MS),
  });
  if ([301, 302, 303, 307, 308].includes(resp.status)) {
    const loc = resp.headers.get("location");
    if (!loc) throw new Error(`redirect ${resp.status} with no Location`);
    if (remainingRedirects <= 0) throw new Error("too many redirects");
    const next = await assertPublicHttpUrl(new URL(loc, urlObj.href).href);
    return httpGetText(next, remainingRedirects - 1);
  }
  const ctype = (resp.headers.get("content-type") || "").toLowerCase();
  if (ctype.startsWith("image/") || ctype.includes("octet-stream") || ctype.includes("pdf")) {
    throw new Error(`non-text content-type ${ctype || "unknown"}`);
  }
  if (!resp.ok) {
    const errBody = await resp.text().catch(() => "");
    throw new Error(`HTTP ${resp.status} ${resp.statusText} ${errBody.slice(0, 200)}`);
  }
  const buf = Buffer.from(await resp.arrayBuffer());
  const sliced = buf.length > WEB_MAX_BYTES ? buf.subarray(0, WEB_MAX_BYTES) : buf;
  const text = sliced.toString("utf8");
  return { ctype, text, finalUrl: urlObj.href, truncated: buf.length > WEB_MAX_BYTES };
}

async function webFetchUrl(rawUrl, extract) {
  const url = await assertPublicHttpUrl(rawUrl);
  const got = await httpGetText(url, WEB_MAX_REDIRECTS);
  let body = got.text;
  if (got.ctype.includes("html") || /<html/i.test(body.slice(0, 2000))) {
    body = htmlToText(body);
  }
  const header = [
    `url: ${got.finalUrl}`,
    got.truncated ? "note: truncated large body" : "",
    extract ? `extract hint: ${extract}` : "",
  ].filter(Boolean).join("\n");
  return vllmTruncate(`${header}\n\n${body}`, 12000);
}

function parseDdgResults(html, max) {
  const results = [];
  const seen = new Set();
  const re = /<a[^>]+href=["']([^"']+)["'][^>]*>([\s\S]*?)<\/a>/gi;
  let m;
  while ((m = re.exec(html)) && results.length < max) {
    let href = decodeEntities(m[1]);
    if (href.startsWith("//")) href = "https:" + href;
    href = unwrapDdgHref(href);
    if (!/^https?:\/\//i.test(href)) continue;
    try {
      const host = new URL(href).hostname;
      if (/duckduckgo\.com$/i.test(host) || hostnameBlocked(host)) continue;
    } catch {
      continue;
    }
    const title = htmlToText(m[2]).replace(/\s+/g, " ").trim();
    if (!title || title.length < 2) continue;
    if (seen.has(href)) continue;
    seen.add(href);
    results.push({ title, url: href });
  }
  return results;
}

async function braveSearch(query, max, apiKey) {
  const u = new URL("https://api.search.brave.com/res/v1/web/search");
  u.searchParams.set("q", query);
  u.searchParams.set("count", String(max));
  const resp = await fetch(u.href, {
    headers: {
      accept: "application/json",
      "x-subscription-token": apiKey,
      "user-agent": WEB_UA,
    },
    signal: AbortSignal.timeout(WEB_FETCH_TIMEOUT_MS),
  });
  const json = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw new Error(`Brave HTTP ${resp.status}: ${json?.message || json?.error || ""}`.trim());
  }
  const rows = Array.isArray(json?.web?.results) ? json.web.results : [];
  return rows.slice(0, max).map((r) => ({
    title: r.title || "",
    url: r.url || "",
    snippet: r.description || "",
  })).filter((r) => r.url);
}

async function duckDuckGoSearch(query, max) {
  const u = new URL("https://html.duckduckgo.com/html/");
  const resp = await fetch(u.href, {
    method: "POST",
    headers: {
      "user-agent": WEB_UA,
      "content-type": "application/x-www-form-urlencoded",
      accept: "text/html",
    },
    body: new URLSearchParams({ q: query, kl: "uk-en" }).toString(),
    signal: AbortSignal.timeout(WEB_FETCH_TIMEOUT_MS),
    redirect: "follow",
  });
  const html = await resp.text();
  if (!resp.ok) {
    throw new Error(`DuckDuckGo HTTP ${resp.status}`);
  }
  const rows = parseDdgResults(html, max);
  if (!rows.length) {
    throw new Error("DuckDuckGo returned no parseable results");
  }
  return rows;
}

function formatSearchResults(rows) {
  if (!rows.length) return "No search results.";
  return rows.map((r, i) => {
    const snip = r.snippet ? `\n   ${htmlToText(r.snippet).replace(/\s+/g, " ").trim()}` : "";
    return `${i + 1}. ${r.title}\n   ${r.url}${snip}`;
  }).join("\n");
}

async function webSearchQuery(query, maxN) {
  const q = String(query || "").trim();
  if (!q) return "error: query required";
  const max = Math.min(12, Math.max(1, parseInt(maxN, 10) || 8));
  const braveKey = braveApiKey();
  // If a Brave key is configured, use it exclusively so a 401 is visible
  // instead of silently falling back to DuckDuckGo.
  if (braveKey) {
    try {
      const rows = await braveSearch(q, max, braveKey);
      if (!rows.length) return "source: brave\n\nNo search results.";
      return vllmTruncate(`source: brave\n\n${formatSearchResults(rows)}`, 6000);
    } catch (err) {
      return `error: Brave search failed: ${err.message}`;
    }
  }
  try {
    const rows = await duckDuckGoSearch(q, max);
    if (!rows.length) return "source: duckduckgo\n\nNo search results.";
    return vllmTruncate(`source: duckduckgo\n\n${formatSearchResults(rows)}`, 6000);
  } catch (err) {
    return `error: web search failed (${err.message}). Set BRAVE_API or BRAVE_API_KEY in claude-gateway/.env.`;
  }
}

function nowStamp() {
  const d = new Date();
  const london = d.toLocaleString("en-GB", {
    timeZone: "Europe/London",
    weekday: "long",
    year: "numeric",
    month: "long",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    timeZoneName: "short",
  });
  return `UTC: ${d.toISOString()}\nEurope/London: ${london}`;
}

function clientsFind(query) {
  const q = String(query || "").trim().toLowerCase();
  if (!q) return "error: query required";
  const hits = [];
  for (const root of ["/clients/current", "/clients/past"]) {
    let names;
    try {
      names = fs.readdirSync(root, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const d of names) {
      if (!d.isDirectory() && !d.isSymbolicLink()) continue;
      if (d.name.toLowerCase().includes(q)) hits.push(`${root}/${d.name}`);
    }
  }
  return hits.slice(0, 40).join("\n") || "(no matching client folders)";
}

const VLLM_PATH_PREFIXES = ["/workspace", "/clients/current", "/clients/past", "/tmp"];

function vllmResolvePath(cwd, p) {
  const abs = path.resolve(cwd || "/workspace", p || "");
  if (VLLM_PATH_PREFIXES.some((pre) => abs === pre || abs.startsWith(pre + "/"))) {
    return abs;
  }
  throw new Error(`path not allowed: ${abs}`);
}

function vllmGoFlags(cwd) {
  let flags = String(process.env.GOFLAGS || "");
  if (!/(^|\s)-buildvcs=false(\s|$)/.test(flags)) {
    flags = `${flags} -buildvcs=false`.trim();
  }
  const root = cwd || "/workspace";
  try {
    if (fs.existsSync(path.join(root, "go.mod")) && fs.existsSync(path.join(root, "vendor"))) {
      if (!/(^|\s)-mod=/.test(flags)) flags = `${flags} -mod=vendor`.trim();
    }
  } catch (_) { /* ignore */ }
  return flags;
}

function vllmToolEnv(cwd, abortState) {
  const root = cwd || "/workspace";
  const env = withToolPath({
    ...process.env,
    HOME: "/root",
    PWD: root,
  }, root);
  return stampChildModelEnv(
    env,
    abortState && abortState.agentProvider,
    abortState && abortState.agentModel,
  );
}

// spawnSync blocks the Node event loop, so WebSocket pongs stop and the
// gateway drops the connection after 90s ("websocket connection lost").
// Phase 5 `go test` routinely exceeds that. Keep the loop free.
function spawnAsync(command, args, opts, abortState) {
  return new Promise((resolve) => {
    const timeoutMs = opts.timeout || 120000;
    const maxBuf = opts.maxBuffer || 1024 * 1024;
    const startedAt = Date.now();
    const child = spawn(command, args, {
      cwd: opts.cwd,
      env: opts.env,
      stdio: ["ignore", "pipe", "pipe"],
      detached: true,
    });
    if (abortState) abortState.child = child;
    let stdout = "";
    let stderr = "";
    let settled = false;
    const ping = opts.send
      ? setInterval(() => {
          if (settled) return;
          const secs = Math.round((Date.now() - startedAt) / 1000);
          const label = String(opts.progressLabel || "").slice(0, 90);
          opts.send({
            type: "progress",
            tool: "Bash",
            input: label ? `still running ${secs}s: ${label}` : `still running ${secs}s`,
          });
        }, 15000)
      : null;
    const finish = (status, extraErr) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (ping) clearInterval(ping);
      if (abortState && abortState.child === child) abortState.child = null;
      if (extraErr) stderr = (stderr + "\n" + extraErr).trim();
      resolve({ status, stdout, stderr, elapsedMs: Date.now() - startedAt });
    };
    const timer = setTimeout(() => {
      killSpawned(child);
      finish(124, `timed out after ${timeoutMs}ms`);
    }, timeoutMs);
    const take = (chunk, which) => {
      if (which === "out") stdout += chunk;
      else stderr += chunk;
      if (stdout.length + stderr.length > maxBuf) {
        killSpawned(child);
      }
    };
    child.stdout.on("data", (d) => take(d.toString(), "out"));
    child.stderr.on("data", (d) => take(d.toString(), "err"));
    child.on("error", (err) => finish(1, err.message));
    child.on("close", (code) => finish(code == null ? 1 : code));
    if (abortState && abortState.aborted) {
      killSpawned(child);
    }
  });
}

function vllmTruncate(s, n) {
  const t = String(s ?? "");
  if (t.length <= n) return t;
  return t.slice(0, n) + `\n… truncated ${t.length - n} chars`;
}

function isReportsWritePath(fp) {
  return /(^|\/)reports\//.test(String(fp || "").replace(/\\/g, "/"));
}

function reviewWriteOnly(allowedTools) {
  return Array.isArray(allowedTools) &&
    allowedTools.includes("Write") &&
    !allowedTools.includes("Bash") &&
    !allowedTools.includes("Edit");
}

async function vllmExecTool(name, input, cwd, abortState, send, allowedTools) {
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
      if (reviewWriteOnly(allowedTools) && !isReportsWritePath(fp)) {
        return `error: review agents may only Write under a reports/ directory (got ${fp})`;
      }
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
      const cmd = String(i.command ?? "");
      if (/\bgog\b/i.test(cmd)) {
        const banned = gogBannedReason(cmd);
        if (banned) return `error: ${banned}`;
        if (/drive\s+upload/i.test(cmd) && !cmd.includes(DRIVE_WRITABLE_FOLDER)) {
          return `error: Drive uploads only allowed to folder ${DRIVE_WRITABLE_FOLDER}`;
        }
      }
      if (/\b(curl|wget)\b/i.test(cmd) && /(169\.254\.|metadata\.google|host\.docker\.internal|\/\/127\.|\/\/localhost\b)/i.test(cmd)) {
        return "error: curl/wget to private/metadata addresses is blocked; use WebFetch for public URLs";
      }
      let timeout = 300000;
      if (/\bgo\s+(env|version)\b/.test(cmd)) timeout = 15000;
      else if (/\bgo\s+(build|test|vet)\b/.test(cmd)) timeout = 180000;
      const result = await spawnAsync("bash", ["-c", cmd], {
        cwd: cwd || "/workspace",
        timeout,
        maxBuffer: 1024 * 1024,
        env: vllmToolEnv(cwd, abortState),
        send,
        progressLabel: cmd.replace(/\s+/g, " ").slice(0, 90),
      }, abortState);
      let out = (result.stdout || "") + (result.stderr || "");
      const code = result.status;
      const secs = Math.max(1, Math.round((result.elapsedMs || 0) / 1000));
      if (/\bgo\s+/.test(cmd) && code === 0) {
        const body = out.replace(/^exit 0\s*/i, "").trim();
        if (!body) {
          out += `\n# go exited 0 in ${secs}s with no output. That is SUCCESS, not a hang. Do not run go env/go version/GOPROXY/vendor.\n`;
        }
      }
      if (code === 124 && /\bgo\s+/.test(cmd)) {
        out += "\n# killed on timeout. Do not diagnose GOPROXY. Next build: go build -o /tmp/out ./cmd/server\n";
      }
      return vllmTruncate(`exit ${code} (${secs}s)\n${out}`, 8000);
    }
    if (name === "Glob") {
      const root = vllmClampSearchRoot(cwd, i.path);
      const pat = String(i.pattern || "*");
      const result = await spawnAsync("bash", ["-c", `find ${JSON.stringify(root)} -type f | head -n 200`], {
        cwd: cwd || "/workspace",
        timeout: 15000,
        maxBuffer: 512 * 1024,
        env: vllmToolEnv(cwd, abortState),
      }, abortState);
      const files = (result.stdout || "").split("\n").filter(Boolean);
      const re = globToRegExp(pat);
      const matched = files.filter((f) => re.test(f)).slice(0, 80);
      return matched.join("\n") || "(no matches)";
    }
    if (name === "Grep") {
      const root = vllmClampSearchRoot(cwd, i.path);
      const pat = String(i.pattern || "");
      const glob = i.glob ? ` --glob ${JSON.stringify(i.glob)}` : "";
      const result = await spawnAsync(
        "bash",
        ["-c", `rg -n --no-heading -m 50${glob} ${JSON.stringify(pat)} ${JSON.stringify(root)} || true`],
        {
          cwd: cwd || "/workspace",
          timeout: 20000,
          maxBuffer: 512 * 1024,
          env: vllmToolEnv(cwd, abortState),
        },
        abortState
      );
      return vllmTruncate(result.stdout || "(no matches)", 6000);
    }
    if (name === "GmailSearch") {
      const query = String(i.query || "newer_than:1d");
      const args = ["gmail", "search", query];
      const max = parseInt(i.max, 10);
      if (max > 0) args.push("--max", String(Math.min(max, 25)));
      if (i.label) args.push("--label", String(i.label));
      return await runGog(args, abortState);
    }
    if (name === "GmailThread") {
      const id = String(i.id || "").trim();
      if (!id) return "error: id required";
      return await runGog(["gmail", "thread", id], abortState);
    }
    if (name === "GmailSend") {
      const args = ["gmail", "send", "--to", String(i.to || ""), "--subject", String(i.subject || ""), "--body", String(i.body || "")];
      if (i.attach) args.push("--attach", String(i.attach));
      return await runGog(args, abortState);
    }
    if (name === "CalendarList") {
      const args = ["calendar", "list"];
      if (i.from) args.push("--from", String(i.from));
      if (i.to) args.push("--to", String(i.to));
      const days = parseInt(i.days, 10);
      if (days > 0) args.push("--days", String(days));
      const max = parseInt(i.max, 10);
      if (max > 0) args.push("--max", String(max));
      if (i.query) args.push("--query", String(i.query));
      return await runGog(args, abortState);
    }
    if (name === "CalendarAdd") {
      const args = ["calendar", "add", "--title", String(i.title || ""), "--start", String(i.start || "")];
      if (i.duration) args.push("--duration", String(i.duration));
      if (i.description) args.push("--description", String(i.description));
      return await runGog(args, abortState);
    }
    if (name === "DriveSearch") {
      const args = ["drive", "search", String(i.query || "")];
      const max = parseInt(i.max, 10);
      if (max > 0) args.push("--max", String(max));
      return await runGog(args, abortState);
    }
    if (name === "DriveLs") {
      const args = ["drive", "ls"];
      if (i.parent) args.push("--parent", String(i.parent));
      const max = parseInt(i.max, 10);
      if (max > 0) args.push("--max", String(max));
      return await runGog(args, abortState);
    }
    if (name === "DriveDownload") {
      const out = vllmResolvePath(cwd, i.out);
      return await runGog(["drive", "download", String(i.file_id || ""), "--out", out], abortState);
    }
    if (name === "DriveUpload") {
      const parent = String(i.parent || "");
      if (parent !== DRIVE_WRITABLE_FOLDER) {
        return `error: Drive uploads only allowed to folder ${DRIVE_WRITABLE_FOLDER}`;
      }
      const fp = vllmResolvePath(cwd, i.path);
      const args = ["drive", "upload", fp, "--parent", parent];
      if (i.name) args.push("--name", String(i.name));
      return await runGog(args, abortState);
    }
    if (name === "SheetsRead") {
      const args = ["sheets", "read", String(i.spreadsheet_id || ""), "--range", String(i.range || "")];
      return await runGog(args, abortState);
    }
    if (name === "WebSearch") {
      return await webSearchQuery(i.query, i.max);
    }
    if (name === "WebFetch") {
      return await webFetchUrl(i.url, i.extract);
    }
    if (name === "Now") {
      return nowStamp();
    }
    if (name === "ClientsFind") {
      return clientsFind(i.query);
    }
    if (name === "RunReviewTeam") {
      return await runReviewTeamIsolated(String(i.team || ""), String(i.task || ""), cwd, send, abortState);
    }
    return `unknown tool: ${name}`;
  } catch (err) {
    return `error: ${err.message}`;
  }
}

const REVIEW_TEAMS = {
  "go-dev-team": [
    { name: "GoPurist", file: "coding/go-dev-team/go-purist.md" },
    { name: "GoPragmatist", file: "coding/go-dev-team/go-pragmatist.md" },
    { name: "GoPessimist", file: "coding/go-dev-team/go-pessimist.md" },
  ],
  "go-sec-team": [
    { name: "GoSecurity", file: "coding/go-sec-team/go-security-agent.md" },
    { name: "GoHacker", file: "coding/go-sec-team/go-hacker-agent.md" },
  ],
  "fe-dev-team": [
    { name: "FEPurist", file: "coding/frontend-team/fe-purist.md" },
    { name: "FEPragmatist", file: "coding/frontend-team/fe-pragmatist.md" },
    { name: "FEPessimist", file: "coding/frontend-team/fe-pessimist.md" },
  ],
  "fe-sec-team": [
    { name: "FESecurity", file: "coding/frontend-team/fe-security-agent.md" },
    { name: "FEHacker", file: "coding/frontend-team/fe-hacker-agent.md" },
  ],
  design: [
    { name: "Security", file: "design/security-agent.md" },
    { name: "Compliance", file: "design/compliance-agent.md" },
    { name: "DataIntegrity", file: "design/data-integrity-agent.md" },
    { name: "Performance", file: "design/performance-agent.md" },
    { name: "ErrorHandling", file: "design/error-handling-agent.md" },
  ],
  integration: [
    { name: "APIGuardian", file: "coding/integration-team/api-guardian-agent.md" },
    { name: "CrossLangSecurity", file: "coding/security-review/cross-language-security.md" },
  ],
};

function discoverReviewTeamsFromDisk() {
  const roots = [
    "/app/teams",
    "/workspace/.claude/teams",
    "/app/agents",
    "/workspace/.claude/agents",
  ];
  const skip = new Set(["readme.md", "consensus-protocol.md"]);
  const claimed = new Set();
  for (const members of Object.values(REVIEW_TEAMS)) {
    for (const m of members) claimed.add(m.file);
  }
  function mdFiles(dir) {
    let ents;
    try { ents = fs.readdirSync(dir, { withFileTypes: true }); } catch { return []; }
    return ents.filter((e) => {
      if (!e.isFile()) return false;
      const n = e.name.toLowerCase();
      return n.endsWith(".md") && !skip.has(n) && !n.startsWith("skill-");
    }).map((e) => path.join(dir, e.name));
  }
  function hasTeamSubdirs(dir) {
    let ents;
    try { ents = fs.readdirSync(dir, { withFileTypes: true }); } catch { return false; }
    return ents.some((e) => e.isDirectory() && mdFiles(path.join(dir, e.name)).length > 0);
  }
  function prettyName(file) {
    let base = path.basename(file, path.extname(file)).replace(/-agent$/i, "");
    return base.split(/[-_]/).map((p) => p ? p[0].toUpperCase() + p.slice(1).toLowerCase() : "").join("");
  }
  function addMember(team, fileRel, abs) {
    if (claimed.has(fileRel)) return;
    claimed.add(fileRel);
    if (!REVIEW_TEAMS[team]) REVIEW_TEAMS[team] = [];
    if (REVIEW_TEAMS[team].some((m) => m.file === fileRel)) return;
    REVIEW_TEAMS[team].push({ name: prettyName(abs), file: fileRel });
  }
  function walk(root, dir) {
    let ents;
    try { ents = fs.readdirSync(dir, { withFileTypes: true }); } catch { return; }
    const files = mdFiles(dir);
    const kids = hasTeamSubdirs(dir);
    if (files.length && dir !== root) {
      const rels = files.map((f) => path.relative(root, f).split(path.sep).join("/"));
      if (kids) {
        for (let i = 0; i < files.length; i++) {
          const base = path.basename(rels[i], path.extname(rels[i])).replace(/-agent$/i, "").toLowerCase();
          addMember(base, rels[i], files[i]);
        }
      } else {
        const team = path.basename(dir);
        const alias = { "frontend-team": "fe-dev-team", "kotlin-team": "mobile-review" }[team] || team;
        for (let i = 0; i < files.length; i++) addMember(alias, rels[i], files[i]);
      }
    }
    for (const e of ents) {
      if (e.isDirectory()) walk(root, path.join(dir, e.name));
    }
  }
  for (const root of roots) walk(root, root);
}

discoverReviewTeamsFromDisk();

function normalizeReviewTeam(name) {
  const n = String(name || "").trim().toLowerCase().replace(/\s+/g, "-");
  const aliases = {
    "go-dev": "go-dev-team",
    "go-sec": "go-sec-team",
    "gosec": "go-sec-team",
    "fe-dev": "fe-dev-team",
    "fe-sec": "fe-sec-team",
  };
  return aliases[n] || n;
}

function readAgentDNA(rel) {
  const roots = [
    "/app/teams",
    "/workspace/.claude/teams",
    "/app/agents",
    "/workspace/.claude/agents",
  ];
  for (const root of roots) {
    const p = path.join(root, rel);
    try {
      return fs.readFileSync(p, "utf8");
    } catch {
      /* try next */
    }
  }
  throw new Error(`agent DNA not found: ${rel}`);
}

async function runReviewTeamIsolated(teamName, task, cwd, parentSend, abortState) {
  const team = normalizeReviewTeam(teamName);
  const members = REVIEW_TEAMS[team];
  if (!members) {
    return `unknown team ${teamName}. Available: ${Object.keys(REVIEW_TEAMS).join(", ")}`;
  }
  if (!String(task || "").trim()) {
    return "error: task is required";
  }
  const parts = [`# ${team}\nTask: ${task}\n`];
  for (const m of members) {
    if (abortState && abortState.aborted) {
      parts.push(`## ${m.name}\n(aborted)`);
      break;
    }
    if (typeof parentSend === "function") {
      parentSend({ type: "progress", tool: "RunReviewTeam", input: `${team}/${m.name}` });
    }
    let dna;
    try {
      dna = readAgentDNA(m.file);
    } catch (err) {
      parts.push(`## ${m.name}\nERROR: ${err.message}`);
      continue;
    }
    let text = "";
    await handleVllmAsk({
      session_id: `orch-${randomUUID()}`,
      isolated: true,
      prompt: task,
      system_prompt: dna,
      cwd: cwd || "/workspace",
      max_turns: 12,
      allowed_tools: ["Read", "Glob", "Grep", "Write"],
      // Same served model as the agent that started the team.
      model: abortState && abortState.agentModel,
    }, (obj) => {
      if (!obj || typeof obj !== "object") return;
      if (obj.type === "result") text = obj.text || "";
      else if (obj.type === "error") text = `ERROR: ${obj.error}`;
      else if (obj.type === "progress" && obj.tool && typeof parentSend === "function") {
        parentSend({ type: "progress", tool: obj.tool, input: `${m.name}: ${obj.input || ""}` });
      }
    }, abortState);
    parts.push(`## ${m.name}\n${text || "(no output)"}`);
  }
  return vllmTruncate(parts.join("\n\n"), 24000);
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

// Qwen3.8 / vLLM can put chain-of-thought in reasoning_content, reasoning,
// or <think>...</think> inside content. Telegram should see it; history
// should only keep the visible answer so the next turn is not polluted.
function splitVllmMessage(msgOut) {
  const raw = msgOut && typeof msgOut === "object" ? msgOut : {};
  let thinking = "";
  if (typeof raw.reasoning_content === "string") thinking = raw.reasoning_content;
  else if (typeof raw.reasoning === "string") thinking = raw.reasoning;

  let content = typeof raw.content === "string" ? raw.content : "";
  const closed = content.match(/<think>\s*([\s\S]*?)\s*<\/think>/i);
  if (closed) {
    if (!thinking.trim()) thinking = closed[1];
    content = content.replace(/<think>\s*[\s\S]*?<\/think>/gi, "").trim();
  } else {
    const open = content.match(/<think>\s*([\s\S]*)/i);
    if (open) {
      if (!thinking.trim()) thinking = open[1];
      content = "";
    }
  }
  return { thinking: thinking.trim(), content: content.trim() };
}

function parseToolArguments(args) {
  if (args && typeof args === "object" && !Array.isArray(args)) return args;
  if (typeof args === "string" && args.trim()) {
    try {
      const parsed = JSON.parse(args);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed;
    } catch { /* keep empty */ }
  }
  return {};
}

// OpenAI/vLLM ChatCompletionMessageFunctionToolCallParam.arguments is a STRING.
function stringifyToolArguments(args) {
  if (typeof args === "string") return args;
  if (args && typeof args === "object") {
    try { return JSON.stringify(args); } catch { return "{}"; }
  }
  return "{}";
}

function isToolResponseUser(m) {
  if (!m || m.role !== "user") return false;
  const c = typeof m.content === "string" ? m.content.trim() : "";
  return c.startsWith("<tool_response>") && c.endsWith("</tool_response>");
}

function isRealUserQuery(m) {
  return Boolean(m && m.role === "user" && !isToolResponseUser(m) && String(m.content || "").trim());
}

// Qwen often emits a plan, a "now I will…" stub, or gets cut off mid-sentence
// (finish_reason=length because thinking ate max_tokens). That is not done.
function looksLikeUnfinishedAnswer(text, thinking, finishReason) {
  if (String(finishReason || "") === "length") return true;
  const t = String(text || "").trim();
  const th = String(thinking || "").trim();
  if (!t) return Boolean(th);
  if (t.length < 1000 && /[:…]$/.test(t)) return true;
  if (t.length < 220 && !/[.!?]["']?$/.test(t)) return true;
  // "I found X. I'll fix this by doing Y." — a plan, not a finished edit.
  if (t.length < 2000 && /\bi(?:'ll| will) (fix|update|rewrite|change|replace|patch|edit|add|wire|refactor|use|restructure)\b/i.test(t)) {
    return true;
  }
  if (t.length < 2000 && /\b(let me|i am going to|i'm going to) (fix|update|rewrite|change|replace|patch|edit|add|wire)\b/i.test(t)) {
    return true;
  }
  if (th.length > 800 && t.length < 800 && !/\b(edited|wrote|committed|tests pass|done)\b/i.test(t)) {
    return true;
  }
  if (t.length > 900) return false;
  if (/\b(done|completed|here is|here's the|summary:|conclusion:)\b/i.test(t) && t.length > 200) {
    return false;
  }
  return /^(let me|let's|i(?:'ll| will| am going to|'m going to)|i need to|i should|okay[,.]? |ok[,.]? |alright[,.]? |first[,:]|now i|now [a-z]|before (i|we)|to (start|begin)|next[,:]|then (i|'ll|we)|give me a (moment|second)|hold on)/i.test(t);
}

// "I'll fix this by…" / "Let me rewrite…" — a plan, not a finished answer.
function looksLikeChatOnly(prompt) {
  const t = String(prompt || "").trim();
  if (!t) return true;
  if (t.length <= 80 && /^(hi|hello|hey|thanks|cheers|ok|okay|yes|no|yo|sup)\b[.!?]*$/i.test(t)) {
    return true;
  }
  const work = /\b(fix|implement|edit|patch|commit|push|deploy|diff|debug|refactor|write file|read file|show me the code|what's uncommitted|git status|go test|go build)\b/i.test(t);
  if (work) return false;
  if (t.length < 500 && /\b(work on|working on|continue|pick up|get back|start on|look at (the )?project|what do you think|can we talk|catch me up|where (are|were) we)\b/i.test(t)) {
    return true;
  }
  return false;
}

function looksLikePlanStub(text) {
  const t = String(text || "").trim();
  if (!t) return false;
  if (t.length < 2000 && /\bi(?:'ll| will) (fix|update|rewrite|change|replace|patch|edit|add|wire|refactor|use|restructure)\b/i.test(t)) {
    return true;
  }
  if (t.length < 2000 && /\b(let me|i am going to|i'm going to) (fix|update|rewrite|change|replace|patch|edit|add|wire)\b/i.test(t)) {
    return true;
  }
  return /^(let me|let's|i(?:'ll| will| am going to|'m going to)|i need to|i should|okay[,.]? |ok[,.]? |alright[,.]? |first[,:]|now i|now [a-z]|before (i|we)|to (start|begin)|next[,:]|then (i|'ll|we)|give me a (moment|second)|hold on)/i.test(t);
}

function thinkingExcerpt(thinking, maxChars) {
  const th = String(thinking || "").trim();
  if (!th) return "";
  const cap = maxChars || 1600;
  if (th.length <= cap) return th;
  return th.slice(-cap).replace(/^[^\n]*\n/, "").trim();
}

// Last-resort user-facing text when Qwen produced only thinking / tool_calls.
// Never return empty — Telegram shows that as "(no output)".
function composeVllmFallback({ lastSolid, lastVisible, toolsUsed, thinking }) {
  const solid = String(lastSolid || "").trim();
  if (solid) return solid;
  const vis = String(lastVisible || "").trim();
  if (vis) return vis;
  const excerpt = thinkingExcerpt(thinking, 1600);
  if (excerpt) return excerpt;
  const tools = Array.isArray(toolsUsed) ? toolsUsed.filter(Boolean) : [];
  if (tools.length) {
    const names = [];
    for (const n of tools) {
      if (names[names.length - 1] !== n) names.push(n);
    }
    return `Work ran (${names.slice(-12).join(", ")}) but produced no user-facing summary. Send continue.`;
  }
  return "The model returned no user-facing text. Send continue.";
}

function reseatVllmMessages(openaiMessages, session, prompt, systemContent) {
  session.messages = sanitizeQwenMessages(
    openaiMessages.filter((m) => m.role !== "system"),
    prompt,
  );
  session.messages = trimVllmHistory(session.messages, vllmHistoryCharBudget());
  openaiMessages.length = 0;
  openaiMessages.push({ role: "system", content: systemContent }, ...session.messages);
}

// Qwen 3.8's chat template walks messages backwards and raises
// "No user query found in messages" unless it sees a role=user turn whose
// content is NOT a <tool_response> wrap. OpenAI role=tool history plus
// naive front-trimming can leave only tool wraps. arguments stay JSON
// strings on the wire (vLLM pydantic); the template is applied server-side.
function sanitizeQwenMessages(messages, currentUserPrompt) {
  const out = [];
  for (const m of messages || []) {
    if (!m || !m.role || m.role === "system") continue;
    if (m.role === "tool") {
      const body = typeof m.content === "string" ? m.content : JSON.stringify(m.content ?? "");
      out.push({ role: "user", content: `<tool_response>\n${body}\n</tool_response>` });
      continue;
    }
    if (m.role === "assistant") {
      const asst = { role: "assistant", content: typeof m.content === "string" ? m.content : "" };
      if (Array.isArray(m.tool_calls) && m.tool_calls.length) {
        asst.tool_calls = m.tool_calls.map((tc) => {
          const fn = tc.function || tc;
          return {
            type: "function",
            id: tc.id,
            function: {
              name: fn.name || "unknown",
              arguments: stringifyToolArguments(fn.arguments),
            },
          };
        });
      }
      out.push(asst);
      continue;
    }
    if (m.role === "user") {
      out.push({ role: "user", content: typeof m.content === "string" ? m.content : String(m.content ?? "") });
    }
  }
  const prompt = String(currentUserPrompt || "").trim();
  if (prompt) {
    const lastReal = [...out].reverse().find(isRealUserQuery);
    const last = lastReal ? String(lastReal.content || "") : "";
    // A shrink pass marks the kept user turn with [truncated]. Putting the
    // original prompt back would restore the overflow for the next cron run.
    const head = last.split("\n…[truncated]…\n")[0];
    const truncatedKeep = last.includes("[truncated]") && head.length >= 40 && prompt.startsWith(head);
    if (!lastReal || (last !== prompt && !truncatedKeep)) {
      out.push({ role: "user", content: prompt });
    }
  }
  return out;
}

// Learned from a vLLM 400 ("maximum context length is N"). Config can be
// larger than the serve (131072 in env, 65536 on the GPU). Estimates and
// cron retries must use the smaller number or every later run 400s.
let vllmObservedContext = 0;

function noteVllmContextLimit(errText) {
  const m = String(errText || "").match(/maximum context length is (\d+)/i);
  if (!m) return 0;
  const n = parseInt(m[1], 10);
  if (!n || n < 2048) return 0;
  if (!vllmObservedContext || n < vllmObservedContext) {
    vllmObservedContext = n;
    console.warn(`[vllm] server context window is ${n}; clamping estimates`);
  }
  return n;
}

function vllmContextLimit() {
  const configured = parseInt(envFileOrProc("VLLM_CONTEXT_WINDOW", "131072"), 10) || 131072;
  if (vllmObservedContext > 0) return Math.min(configured, vllmObservedContext);
  return configured;
}

function vllmMaxOutput() {
  return parseInt(envFileOrProc("VLLM_MAX_OUTPUT_TOKENS", "16384"), 10) || 16384;
}

// --- vllm-fit-test-start ---
// Code and tool JSON are often ~2 chars/token. Dividing by 3 under-counted,
// so compact never fired and vLLM returned "maximum context length".
const VLLM_CHARS_PER_TOKEN = 2;
const VLLM_TOOLS_TOKENS = 12000;

function estimateTokensFromChars(n) {
  return Math.ceil(Math.max(0, n) / VLLM_CHARS_PER_TOKEN);
}

function vllmTextLen(c) {
  if (typeof c === "string") return c.length;
  if (c == null) return 0;
  try { return JSON.stringify(c).length; } catch { return 0; }
}

function vllmMessageChars(m) {
  if (!m) return 0;
  let n = vllmTextLen(m.content);
  if (m.tool_calls) n += vllmTextLen(m.tool_calls);
  if (typeof m.reasoning_content === "string") n += m.reasoning_content.length;
  return n;
}

function vllmFitIsToolResponse(m) {
  if (!m || m.role !== "user") return false;
  const c = typeof m.content === "string" ? m.content.trim() : "";
  return c.startsWith("<tool_response>") && c.endsWith("</tool_response>");
}

function vllmFitIsRealUser(m) {
  return Boolean(m && m.role === "user" && !vllmFitIsToolResponse(m) && String(m.content || "").trim());
}

function parseVllmContextLimit(errText) {
  const m = String(errText || "").match(/maximum context length is (\d+)/i);
  if (!m) return 0;
  const n = parseInt(m[1], 10);
  return n >= 2048 ? n : 0;
}

function vllmInputTokens(messages, toolsTok) {
  let chars = 0;
  for (const m of messages || []) chars += vllmMessageChars(m);
  return estimateTokensFromChars(chars) + (toolsTok == null ? VLLM_TOOLS_TOKENS : toolsTok);
}

// Last server-reported prompt_tokens is exact. The char estimate is a guess.
// Pressure is the worse of the two so a low estimate cannot skip compact.
function vllmPressureRatio(messages, maxOut, ctxLimit, lastPromptTokens, toolsTok) {
  const ctx = ctxLimit || 131072;
  const inputTok = vllmInputTokens(messages, toolsTok);
  const usable = Math.max(1024, ctx - maxOut - 512);
  let ratio = inputTok / usable;
  if (lastPromptTokens > 0) ratio = Math.max(ratio, lastPromptTokens / usable);
  return { ratio, inputTok, usable, ctx };
}

function capVllmMessage(m, maxChars) {
  if (!m || typeof m.content !== "string" || m.content.length <= maxChars) return false;
  const mark = "\n…[truncated]…\n";
  const room = Math.max(200, maxChars - mark.length);
  const head = Math.max(80, Math.floor(room * 0.25));
  const tail = Math.max(80, room - head);
  m.content = m.content.slice(0, head) + mark + m.content.slice(-tail);
  return true;
}

// Drop oldest turns and truncate huge tool dumps until input + maxOut fits.
// Keeps the system message and the latest real user text. Mutates messages.
function shrinkVllmToFitLimit(messages, maxOut, ctxLimit, toolsTok) {
  const ctx = ctxLimit || 131072;
  const tools = toolsTok == null ? VLLM_TOOLS_TOKENS : toolsTok;
  const safety = 1024;
  const inputTok = () => vllmInputTokens(messages, tools);
  const over = () => inputTok() + maxOut + safety > ctx;
  let changed = false;
  const lastUser = () => {
    for (let i = messages.length - 1; i >= 0; i--) {
      if (vllmFitIsRealUser(messages[i])) return i;
    }
    return -1;
  };
  const charBudget = Math.max(2000, (ctx - maxOut - tools - safety) * VLLM_CHARS_PER_TOKEN);
  const perMsg = Math.max(1500, Math.floor(charBudget / 4));
  if (over()) {
    for (const m of messages) {
      if (capVllmMessage(m, perMsg)) changed = true;
    }
  }
  let guard = 0;
  while (messages.length > 2 && over() && guard++ < 400) {
    let dropAt = messages[0] && messages[0].role === "system" ? 1 : 0;
    const keep = lastUser();
    if (dropAt === keep || dropAt >= messages.length) {
      const m = keep >= 0 ? messages[keep] : null;
      if (m && capVllmMessage(m, Math.max(800, Math.floor(String(m.content || "").length * 0.6)))) {
        changed = true;
        continue;
      }
      break;
    }
    const dropped = messages[dropAt];
    messages.splice(dropAt, 1);
    changed = true;
    if (dropped && dropped.role === "assistant" && Array.isArray(dropped.tool_calls) && dropped.tool_calls.length) {
      while (dropAt < messages.length && vllmFitIsToolResponse(messages[dropAt]) && dropAt !== lastUser()) {
        messages.splice(dropAt, 1);
      }
    }
  }
  guard = 0;
  while (over() && guard++ < 8) {
    let cut = false;
    for (const m of messages) {
      if (typeof m.content === "string" && m.content.length > 600 && capVllmMessage(m, Math.floor(m.content.length * 0.6))) {
        cut = true;
        changed = true;
      }
    }
    if (!cut) break;
  }
  let out = maxOut;
  const room = ctx - inputTok() - safety;
  if (room < out) out = Math.max(256, room);
  return { maxOut: out, inputTok: inputTok(), ctx, changed, overflow: inputTok() + out + safety > ctx };
}
// --- vllm-fit-test-end ---

function vllmHistoryCharBudget() {
  const tok = vllmContextLimit();
  const out = vllmMaxOutput();
  // Stay under the window after tools + output. The old budget was
  // (window − output) × 3 chars and could itself exceed the serve.
  // Below the compact threshold once tool tokens are added back, so a
  // freshly trimmed cron session does not compact on the very next call.
  const histTok = Math.max(4000, Math.floor((tok - out - VLLM_TOOLS_TOKENS - 1024) * 0.55));
  return histTok * VLLM_CHARS_PER_TOKEN;
}

function estimateVllmInputTokens(messages) {
  return vllmInputTokens(messages, VLLM_TOOLS_TOKENS);
}

function vllmFillRatio(messages, maxOut, lastPromptTokens) {
  return vllmPressureRatio(messages, maxOut, vllmContextLimit(), lastPromptTokens, VLLM_TOOLS_TOKENS);
}

function shrinkVllmToFit(messages, maxOut) {
  return shrinkVllmToFitLimit(messages, maxOut, vllmContextLimit(), VLLM_TOOLS_TOKENS);
}

async function summariseVllmMessages(messages, baseURL, auth, modelName, abortState) {
  // The summary request itself must fit. Thirty full tool dumps do not,
  // and a thinking-only reply used to look like an empty summary so
  // compact was skipped and the next call (and the next cron run) 400'd.
  const conv = (messages || []).filter((m) => m.role !== "system");
  const pieces = [];
  let budget = 12000;
  for (let i = conv.length - 1; i >= 0 && budget > 200; i--) {
    let text = typeof conv[i].content === "string" ? conv[i].content : "";
    if (conv[i].tool_calls) {
      try { text += "\n" + JSON.stringify(conv[i].tool_calls).slice(0, 400); } catch (_) { /* ignore */ }
    }
    text = text.trim();
    if (!text) continue;
    const take = text.slice(0, Math.min(1800, budget));
    pieces.push({
      role: conv[i].role === "assistant" ? "assistant" : "user",
      content: take,
    });
    budget -= take.length;
  }
  pieces.reverse();
  const json = await vllmChat(baseURL, auth, {
    model: modelName,
    messages: [
      { role: "system", content: SUMMARY_PROMPT },
      ...pieces,
      { role: "user", content: "Write the summary now. Output only the summary." },
    ],
    max_tokens: 1024,
    chat_template_kwargs: { enable_thinking: false, reasoning_effort: "low" },
  }, abortState);
  const msgOut = json?.choices?.[0]?.message || {};
  const { thinking, content } = splitVllmMessage(msgOut);
  let summary = (content || thinking || "").trim();
  if (summary.length > 6000) summary = summary.slice(0, 6000);
  return summary;
}

async function compactVllmSession(opts) {
  const {
    sessionId, messages, systemContent, currentPrompt,
    baseURL, auth, modelName, usageRatio, contextWindow, tokensAtTrigger,
  } = opts;
  try {
    const summary = await summariseVllmMessages(messages, baseURL, auth, modelName, opts.abortState);
    if (!summary) {
      console.warn(`[compact-vllm ${sessionId}] empty summary; skip`);
      return { ok: false, error: "empty summary", triggered: true, triggered_by: "auto" };
    }
    const seed = [
      { role: "user", content: "[Session summary — continue from here]\n\n" + summary },
      { role: "assistant", content: "Understood. I will continue from that summary." },
    ];
    if (currentPrompt && currentPrompt.trim()) {
      seed.push({ role: "user", content: currentPrompt });
    }
    console.log(`[compact-vllm ${sessionId}] summary ${summary.length} chars, ratio=${(usageRatio * 100).toFixed(1)}%`);
    return {
      ok: true,
      triggered: true,
      triggered_by: opts.triggeredBy || "auto",
      reason: opts.reason || `threshold_${Math.round(VLLM_COMPACT_THRESHOLD * 100)}pct`,
      summary,
      seed,
      usage_ratio_pct: Math.round((usageRatio || 0) * 100),
      context_window: contextWindow || vllmContextLimit(),
      tokens_at_trigger: tokensAtTrigger || 0,
      provider: "vllm",
      model: modelName,
    };
  } catch (err) {
    console.warn(`[compact-vllm ${sessionId}] failed: ${err.message}`);
    return { ok: false, error: err.message, triggered: true, triggered_by: opts.triggeredBy || "auto" };
  }
}

function fitVllmMessages(messages, maxOut) {
  return shrinkVllmToFit(messages, maxOut);
}

function trimVllmHistory(messages, maxChars) {
  let total = 0;
  for (const m of messages) total += vllmMessageChars(m);
  while (messages.length > 2 && total > maxChars) {
    let lastUser = -1;
    for (let i = messages.length - 1; i >= 0; i--) {
      if (isRealUserQuery(messages[i])) {
        lastUser = i;
        break;
      }
    }
    let dropAt = 0;
    if (dropAt === lastUser) dropAt++;
    if (dropAt >= messages.length) break;
    const dropped = messages.splice(dropAt, 1)[0];
    total -= vllmMessageChars(dropped);
  }
  // The latest user turn is kept even when it alone exceeds the budget
  // (enriched prompt, cron DNA). Truncate it instead of sending a 400.
  while (total > maxChars) {
    let cut = false;
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i];
      if (typeof m.content === "string" && m.content.length > 600 && capVllmMessage(m, Math.floor(m.content.length * 0.6))) {
        cut = true;
        break;
      }
    }
    if (!cut) break;
    total = 0;
    for (const m of messages) total += vllmMessageChars(m);
  }
  return messages;
}

function vllmOpenAITools(allowed) {
  const allow = Array.isArray(allowed) && allowed.length
    ? new Set(allowed)
    : null;
  return VLLM_TOOLS.filter((t) => !allow || allow.has(t.name)).map((t) => ({
    type: "function",
    function: {
      name: t.name,
      description: t.description,
      parameters: t.input_schema,
    },
  }));
}

function fetchErrorDetail(err) {
  const parts = [];
  for (let e = err, n = 0; e && n < 4; e = e.cause, n++) {
    if (e.name === "TimeoutError" || e.code === "ABORT_ERR") {
      parts.push("vLLM request timed out");
    }
    if (e.code && !parts.includes(e.code)) parts.push(e.code);
    if (e.message && !parts.includes(e.message)) parts.push(e.message);
  }
  return parts.join(": ") || "fetch failed";
}

function isTransientFetch(err) {
  const s = fetchErrorDetail(err).toLowerCase();
  if (/maximum context length|input_tokens/.test(s)) return false;
  if (/headers timeout|und_err_headers_timeout/.test(s)) return false;
  return /fetch failed|econnreset|econnrefused|etimedout|und_err|socket|timed out|aborted|network/.test(s);
}

function watchVllmResponse(res, fail, isSettled) {
  // 'error' with no listener crashes the process. Mid-stream destroy()
  // emits 'aborted' + 'close' (complete=false) and neither 'end' nor
  // req 'error' — without these, the GPU ticket never leaves the queue.
  // A finished body emits 'end' first; isSettled stops that close from
  // turning a good response into an abort.
  const done = () => (typeof isSettled === "function" ? isSettled() : false);
  res.on("error", (err) => {
    if (!done()) fail(err || new Error("aborted"));
  });
  res.on("aborted", () => {
    if (!done()) fail(new Error("aborted"));
  });
  res.on("close", () => {
    if (vllmResponseEvent("close", Boolean(res.complete), done()) === "reject") {
      fail(new Error("aborted"));
    }
  });
}

function watchVllmRequest(req, abortState, fail) {
  req.on("error", (err) => fail(err || new Error("aborted")));
  if (!abortState) return () => {};
  abortState.cancel = fail;
  abortState.httpReq = req;
  return () => {
    if (abortState.cancel === fail) abortState.cancel = null;
    if (abortState.httpReq === req) abortState.httpReq = null;
  };
}

function postJson(urlStr, { headers, body, timeoutMs, abortState }) {
  return new Promise((resolve, reject) => {
    let u;
    try {
      u = new URL(urlStr);
    } catch (err) {
      reject(err);
      return;
    }
    if (abortState && abortState.aborted) {
      reject(new Error("aborted"));
      return;
    }
    const lib = u.protocol === "https:" ? https : http;
    const payload = Buffer.from(body);
    let settled = false;
    let disarm = () => {};
    const settle = (err, value) => {
      if (settled) return;
      settled = true;
      disarm();
      if (err) reject(err);
      else resolve(value);
    };
    const fail = (err) => settle(err || new Error("aborted"));
    const req = lib.request({
      hostname: u.hostname,
      port: u.port || (u.protocol === "https:" ? 443 : 80),
      path: `${u.pathname}${u.search}`,
      method: "POST",
      family: 4,
      headers: {
        ...headers,
        "content-length": payload.length,
      },
      timeout: timeoutMs,
    }, (res) => {
      watchVllmResponse(res, fail, () => settled);
      const chunks = [];
      res.on("data", (c) => chunks.push(c));
      res.on("end", () => {
        if (vllmResponseEvent("end", res.complete, settled) !== "resolve") return;
        const raw = Buffer.concat(chunks).toString("utf8");
        let json = {};
        try { json = raw ? JSON.parse(raw) : {}; } catch { json = { message: raw.slice(0, 500) }; }
        settle(null, { status: res.statusCode || 0, json });
      });
    });
    disarm = watchVllmRequest(req, abortState, fail);
    req.on("timeout", () => {
      try { req.destroy(new Error("vLLM request timed out")); } catch (_) {}
      fail(new Error("vLLM request timed out"));
    });
    if (abortState && abortState.aborted) {
      try { req.destroy(new Error("aborted")); } catch (_) {}
      fail(new Error("aborted"));
      return;
    }
    req.write(payload);
    req.end();
  });
}

// --- gpu-queue-test-start ---
// One 27B sequence at a time on the 3090. A second @agent hitting vLLM
// while another is generating caused "socket hang up" (OOM / dropped HTTP).
// Not a promise chain: /stop must pull a waiting agent out immediately.
// A chain could only skip that agent by resolving its slot, which let the
// next request start in parallel with the one still on the GPU.
var gpuTickets = [];
var gpuBusy = false;
// After /stop or a deadline, a stream that never settles must not stay at
// the head of the queue. 2s is long enough for req.destroy() to land and
// short enough that the next agent is not stuck behind a dead one.
var gpuAbortReleaseMs = 2000;

// Mid-stream req.destroy() emits response 'close' (complete=false) and
// not 'end'. A finished body emits 'end' then 'close' with complete=true.
function vllmResponseEvent(kind, complete, settled) {
  if (settled) return "ignore";
  if (kind === "end") return "resolve";
  if (kind === "close" && complete) return "ignore";
  if (kind === "close" || kind === "aborted" || kind === "error") return "reject";
  return "ignore";
}

function gpuAgentName(abortState) {
  const sid = String((abortState && abortState.sessionId) || "");
  // Provider prefixes stack (vllm:cron-…). Only a real Telegram main chat
  // is @default. Bare cron jobs used to match nothing and were labeled
  // @default, so /progress showed a queue behind an idle @default while
  // morning_briefing / end_of_day held the GPU.
  const bare = sid.replace(/^(?:[a-z0-9]+:)+/i, "");
  const cronAgent = bare.match(/^cron-.+-ag-([a-z0-9]+)$/i);
  if (cronAgent) return cronAgent[1];
  const cronJob = bare.match(/^cron-(.+)$/i);
  if (cronJob) return "cron-" + cronJob[1];
  const agent = bare.match(/(?:^|-)ag-([a-z0-9]+)$/i);
  if (agent) return agent[1];
  if (/orch-/i.test(sid)) return "orchestrator";
  if (bare === "" || bare === "default" || /^tg-\d+$/i.test(bare)) return "default";
  return bare.slice(0, 48);
}

// i=0 && !running used to fall through to "queued 1 of 1 behind @self"
// because names[0] is this ticket. First in line is starting, not queued.
function gpuQueueLine(i, n, names, running) {
  const pos = i + 1;
  const mine = names[i] || "agent";
  const tag = (x) => "@" + x;
  if (i === 0) {
    const verb = running ? "generating" : "starting";
    if (n <= 1) return `GPU: ${verb} (@${mine})`;
    return `GPU: ${verb} (@${mine}) — 1 of ${n}; waiting: ${names.slice(1).map(tag).join(", ")}`;
  }
  const ahead = names.slice(0, i).map(tag).join(", ") || "another agent";
  return `GPU: queued ${pos} of ${n} (@${mine}, behind ${ahead})`;
}

function broadcastGpuQueue() {
  const n = gpuTickets.length;
  const names = gpuTickets.map((t) => gpuAgentName(t.abortState));
  gpuTickets.forEach((t, i) => {
    const send = t.abortState && t.abortState.send;
    if (typeof send !== "function" || (t.abortState && t.abortState.aborted)) return;
    const input = gpuQueueLine(i, n, names, Boolean(t.running));
    try {
      send({ type: "progress", tool: "model", input });
    } catch (_) { /* ignore */ }
  });
}

function dropAbortedGpuTickets() {
  let changed = false;
  for (let i = gpuTickets.length - 1; i >= 0; i--) {
    const t = gpuTickets[i];
    if (t.running) continue;
    if (t.abortState && t.abortState.aborted) {
      gpuTickets.splice(i, 1);
      t.fail(new Error("aborted"));
      changed = true;
    }
  }
  if (changed) {
    broadcastGpuQueue();
    if (!gpuBusy) pumpGpu();
  }
}

// Reject the in-flight vLLM call and forget queued turns for this agent.
// The running ticket stays until its HTTP promise settles, so the next
// agent cannot overlap it on the 3090. Waiting tickets are removed now —
// otherwise /stop leaves them in front of whoever starts next.
function abortInFlight(abortState) {
  if (!abortState) return;
  abortState.aborted = true;
  const cancel = abortState.cancel;
  const req = abortState.httpReq;
  abortState.cancel = null;
  abortState.httpReq = null;
  if (req && typeof req.destroy === "function") {
    try { req.destroy(new Error("aborted")); } catch (_) {}
  }
  if (typeof cancel === "function") {
    try { cancel(new Error("aborted")); } catch (_) {}
  }
  const name = gpuAgentName(abortState);
  console.log(`[gpu-abort] @${name} tickets=${gpuTickets.length} busy=${gpuBusy}`);
  dropAbortedGpuTickets();
  for (const t of gpuTickets) {
    if (t.abortState === abortState && t.running) scheduleAbortedGpuRelease(t);
  }
}

// cancel() rejects the HTTP promise when the socket is still ours. A prefill
// that never emits a byte, or a destroy() that never fires 'close', leaves
// the ticket running and gpuBusy true. Release it so the next agent can use
// the 3090. The old request is destroyed again here; vLLM stops when the
// client socket goes away.
function scheduleAbortedGpuRelease(ticket) {
  if (!ticket || ticket.forceTimer) return;
  ticket.forceTimer = setTimeout(() => {
    ticket.forceTimer = null;
    const idx = gpuTickets.indexOf(ticket);
    if (idx < 0) return;
    const req = ticket.abortState && ticket.abortState.httpReq;
    if (req && typeof req.destroy === "function") {
      try { req.destroy(new Error("aborted")); } catch (_) { /* already dead */ }
    }
    gpuTickets.splice(idx, 1);
    ticket.running = false;
    if (!gpuTickets.some((t) => t.running)) gpuBusy = false;
    ticket.fail(new Error("aborted"));
    console.warn(`[gpu] released hung ticket @${gpuAgentName(ticket.abortState)}`);
    broadcastGpuQueue();
    pumpGpu();
  }, gpuAbortReleaseMs);
}

function pumpGpu() {
  if (gpuBusy) return;
  while (gpuTickets.length) {
    const ticket = gpuTickets[0];
    if (ticket.abortState && ticket.abortState.aborted) {
      gpuTickets.shift();
      ticket.fail(new Error("aborted"));
      continue;
    }
    gpuBusy = true;
    ticket.running = true;
    broadcastGpuQueue();
    Promise.resolve()
      .then(() => {
        if (ticket.abortState && ticket.abortState.aborted) {
          throw new Error("aborted");
        }
        return ticket.fn();
      })
      .then((v) => ticket.succeed(v), (e) => ticket.fail(e))
      .finally(() => {
        if (ticket.forceTimer) {
          clearTimeout(ticket.forceTimer);
          ticket.forceTimer = null;
        }
        const idx = gpuTickets.indexOf(ticket);
        if (idx >= 0) gpuTickets.splice(idx, 1);
        ticket.running = false;
        // A force-released ticket's finally runs after the next job has
        // already taken the GPU. Do not clear that job's busy flag.
        if (!gpuTickets.some((t) => t.running)) gpuBusy = false;
        broadcastGpuQueue();
        pumpGpu();
      });
    return;
  }
  broadcastGpuQueue();
}

function enqueueVllm(fn, abortState) {
  return new Promise((resolve, reject) => {
    let settled = false;
    const ticket = {
      abortState,
      fn,
      running: false,
      succeed(v) {
        if (settled) return;
        settled = true;
        resolve(v);
      },
      fail(e) {
        if (settled) return;
        settled = true;
        reject(e instanceof Error ? e : new Error(String(e || "aborted")));
      },
    };
    if (abortState && abortState.aborted) {
      ticket.fail(new Error("aborted"));
      return;
    }
    gpuTickets.push(ticket);
    broadcastGpuQueue();
    pumpGpu();
  });
}

function resetGpuQueueForTests() {
  for (const t of gpuTickets) {
    if (t.forceTimer) clearTimeout(t.forceTimer);
  }
  gpuTickets.length = 0;
  gpuBusy = false;
}
// --- gpu-queue-test-end ---

async function vllmChat(baseURL, auth, body, abortState) {
  return enqueueVllm(() => vllmChatUnlocked(baseURL, auth, body, abortState), abortState);
}

async function vllmChatUnlocked(baseURL, auth, body, abortState) {
  // 27B + thinking + a large follow-up routinely exceeds 3 minutes.
  // Use node:http so we are not bound by undici's 300s headersTimeout
  // (and so the sidecar boots on Node 20 without node:undici).
  const timeoutMs = parseInt(process.env.VLLM_FETCH_TIMEOUT_MS || "600000", 10) || 600000;
  const url = `${baseURL}/v1/chat/completions`;
  let lastErr;
  for (let attempt = 1; attempt <= 2; attempt++) {
    if (abortState && abortState.aborted) {
      throw new Error("aborted");
    }
    try {
      const resp = await postJson(url, {
        timeoutMs,
        abortState,
        headers: {
          "content-type": "application/json",
          authorization: `Bearer ${auth}`,
        },
        body: JSON.stringify(body),
      });
      const json = resp.json || {};
      if (resp.status < 200 || resp.status >= 300) {
        if (resp.status === 401) {
          throw new Error(
            "HTTP 401 from vLLM — the serve on :18020 requires an API key. " +
            "Set VLLM_API_KEY in claude-gateway/.env to the same value as vLLM's --api-key / VLLM_API_KEY, then retry (sidecar re-reads .env per request)."
          );
        }
        const errMsg = (typeof json?.error === "string" ? json.error : json?.error?.message) || json?.message || `HTTP ${resp.status}`;
        throw new Error(errMsg);
      }
      return json;
    } catch (err) {
      lastErr = err;
      if (abortState && abortState.aborted) {
        throw new Error("aborted");
      }
      const detail = fetchErrorDetail(err);
      console.error(`[vllm] ${url} attempt ${attempt}/2 failed: ${detail}`);
      if (attempt === 1 && isTransientFetch(err) && !String(detail).includes("401")) {
        await new Promise((r) => setTimeout(r, 1500));
        continue;
      }
      throw new Error(detail);
    }
  }
  throw lastErr;
}

function vllmChatStream(baseURL, auth, body, abortState, onDelta) {
  return enqueueVllm(() => vllmChatStreamUnlocked(baseURL, auth, body, abortState, onDelta), abortState);
}

function vllmChatStreamUnlocked(baseURL, auth, body, abortState, onDelta) {
  const timeoutMs = parseInt(process.env.VLLM_FETCH_TIMEOUT_MS || "600000", 10) || 600000;
  const urlStr = `${baseURL}/v1/chat/completions`;
  return new Promise((resolve, reject) => {
    if (abortState && abortState.aborted) {
      reject(new Error("aborted"));
      return;
    }
    let u;
    try {
      u = new URL(urlStr);
    } catch (err) {
      reject(err);
      return;
    }
    const payload = Buffer.from(JSON.stringify({
      ...body,
      stream: true,
    }));
    const lib = u.protocol === "https:" ? https : http;
    let settled = false;
    let disarm = () => {};
    const settle = (err, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(wallTimer);
      disarm();
      if (err) reject(err);
      else resolve(value);
    };
    const fail = (err) => settle(err || new Error("aborted"));
    // Node's req timeout is inactivity. A thinking stream resets it on every
    // token, so one completion used to hold the 3090 until the process died.
    const wallMs = parseInt(process.env.VLLM_STREAM_WALL_MS || "600000", 10) || 600000;
    let wallTimer = null;
    console.log(`[vllm-stream] POST ${urlStr} bytes=${payload.length}`);
    const req = lib.request({
      hostname: u.hostname,
      port: u.port || (u.protocol === "https:" ? 443 : 80),
      path: `${u.pathname}${u.search}`,
      method: "POST",
      family: 4,
      headers: {
        "content-type": "application/json",
        authorization: `Bearer ${auth}`,
        "content-length": payload.length,
      },
      timeout: timeoutMs,
    }, (res) => {
      watchVllmResponse(res, fail, () => settled);
      if ((res.statusCode || 0) < 200 || res.statusCode >= 300) {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          if (vllmResponseEvent("end", res.complete, settled) !== "resolve") return;
          const raw = Buffer.concat(chunks).toString("utf8");
          let json = {};
          try { json = raw ? JSON.parse(raw) : {}; } catch { json = { message: raw.slice(0, 500) }; }
          if (res.statusCode === 401) {
            fail(new Error(
              "HTTP 401 from vLLM — the serve on :18020 requires an API key. " +
              "Set VLLM_API_KEY in claude-gateway/.env to the same value as vLLM's --api-key."
            ));
            return;
          }
          const errMsg = (typeof json?.error === "string" ? json.error : json?.error?.message) || json?.message || `HTTP ${res.statusCode}`;
          fail(new Error(errMsg));
        });
        return;
      }
      let buf = "";
      let content = "";
      let thinking = "";
      const toolAcc = [];
      let finish = "";
      let usage = {};
      let apiModel = body.model || "";
      const takeDelta = (d) => {
        if (!d || typeof d !== "object") return;
        const reason = d.reasoning_content || d.reasoning || d.thinking;
        if (typeof reason === "string" && reason) {
          thinking += reason;
          if (typeof onDelta === "function") onDelta({ thinking: reason });
        }
        if (typeof d.content === "string" && d.content) {
          content += d.content;
          if (typeof onDelta === "function") onDelta({ content: d.content });
        }
        if (Array.isArray(d.tool_calls)) {
          for (const tc of d.tool_calls) {
            const idx = Number.isInteger(tc.index) ? tc.index : toolAcc.length;
            if (!toolAcc[idx]) toolAcc[idx] = { id: "", type: "function", function: { name: "", arguments: "" } };
            if (tc.id) toolAcc[idx].id = tc.id;
            const fn = tc.function || {};
            if (fn.name) toolAcc[idx].function.name += fn.name;
            if (fn.arguments) toolAcc[idx].function.arguments += fn.arguments;
          }
        }
      };
      let firstByte = false;
      res.on("data", (chunk) => {
        if (abortState && abortState.aborted) {
          try { req.destroy(new Error("aborted")); } catch (_) { /* closing */ }
          fail(new Error("aborted"));
          return;
        }
        if (!firstByte) {
          firstByte = true;
          console.log(`[vllm-stream] first byte after headers status=${res.statusCode}`);
          if (typeof onDelta === "function") onDelta({ firstByte: true });
        }
        buf += chunk.toString("utf8");
        const lines = buf.split(/\r?\n/);
        buf = lines.pop() || "";
        for (const line of lines) {
          const t = line.trim();
          if (!t.startsWith("data:")) continue;
          const data = t.slice(5).trim();
          if (!data || data === "[DONE]") continue;
          let ev;
          try { ev = JSON.parse(data); } catch { continue; }
          if (ev.model) apiModel = ev.model;
          if (ev.usage) usage = ev.usage;
          const choice = ev.choices && ev.choices[0];
          if (!choice) continue;
          if (choice.finish_reason) finish = choice.finish_reason;
          takeDelta(choice.delta || {});
        }
      });
      res.on("end", () => {
        if (vllmResponseEvent("end", res.complete, settled) !== "resolve") return;
        const tool_calls = toolAcc.filter(Boolean);
        settle(null, {
          model: apiModel,
          usage,
          choices: [{
            finish_reason: finish || (tool_calls.length ? "tool_calls" : "stop"),
            message: {
              role: "assistant",
              content,
              reasoning_content: thinking,
              ...(tool_calls.length ? { tool_calls } : {}),
            },
          }],
        });
      });
    });
    disarm = watchVllmRequest(req, abortState, fail);
    wallTimer = setTimeout(() => {
      try { req.destroy(new Error("vLLM stream wall clock")); } catch (_) { /* already closed */ }
      fail(new Error("vLLM stream exceeded wall clock"));
    }, wallMs);
    req.on("timeout", () => {
      try { req.destroy(new Error("vLLM request timed out")); } catch (_) {}
      fail(new Error("vLLM request timed out"));
    });
    if (abortState && abortState.aborted) {
      try { req.destroy(new Error("aborted")); } catch (_) {}
      fail(new Error("aborted"));
      return;
    }
    req.write(payload);
    req.end();
  });
}

async function handleVllmAsk(msg, send, sharedAbort, ws) {
  const { session_id, prompt, cwd, max_turns, model, system_prompt, allowed_tools } = msg;
  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  const sessionId = session_id || "default";
  // Unscoped cron sessions are "cron-<job>", not the Telegram @default chat.
  const cronAsk = /(^|:)cron-/.test(String(sessionId));
  const isolated = /(?:^|:)orch-/.test(String(sessionId)) || Boolean(msg.isolated);
  const allowedTools = Array.isArray(allowed_tools) && allowed_tools.length ? allowed_tools : null;
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
  const maxOut = parseInt(overlay.env.CLAUDE_CODE_MAX_OUTPUT_TOKENS || "16384", 10) || 16384;
  const auth = overlay.env.ANTHROPIC_AUTH_TOKEN || "dummy";
  // Coding tasks (Phase 5, tests, multi-file edits) burn 20 tool rounds easily.
  // Hitting the cap with a "Let me rewrite:" stub looks like the bot died.
  let maxTurns = (max_turns && max_turns > 0) ? max_turns : 50;
  const chatOnly = !isolated && looksLikeChatOnly(prompt);
  if (chatOnly) {
    maxTurns = Math.min(maxTurns, 2);
  }
  const reasoningEffort = (envFileOrProc("VLLM_REASONING_EFFORT", "medium") || "medium").toLowerCase();

  const session = isolated
    ? { messages: [], lastActive: Date.now() }
    : (sessions.get(sessionId) || { messages: [], lastActive: Date.now() });
  if (!Array.isArray(session.messages)) session.messages = [];
  session.messages = sanitizeQwenMessages(session.messages, prompt);
  session.messages = trimVllmHistory(session.messages, chatOnly ? 8000 : vllmHistoryCharBudget());

  const prior = activeRequests.get(sessionId);
  if (prior) markAborted(prior);
  const abortState = sharedAbort || { aborted: false };
  // Local GPU work is tied to the socket that started it. DeepSeek is not.
  abortState.localGpu = true;
  // Bash and other tools this ask starts must keep this model.
  abortState.agentProvider = "vllm";
  abortState.agentModel = modelName;
  if (ws) abortState.owner = ws;
  abortState.send = send;
  abortState.sessionId = sessionId;
  send({ type: "progress", tool: "model", input: "starting" });
  const keepalive = setInterval(() => send({ type: "heartbeat" }), 15000);
  activeRequests.set(sessionId, abortState);
  // Gateway cron cancels its waiter at 10 minutes and a Telegram turn at
  // 120. The abort frame is best-effort. Without this timer the GPU call
  // kept streaming and the queue still listed the job.
  const askedTimeout = parseInt(msg.timeout_ms, 10) || 0;
  const askBudget = askedTimeout > 0
    ? askedTimeout
    : (cronAsk ? 10 * 60 * 1000 : 120 * 60 * 1000);
  const timeoutTimer = setTimeout(() => {
    console.log(`[vllm-ask ${sessionId}] aborting after ${askBudget}ms`);
    markAborted(abortState);
  }, askBudget);

  console.log(
    `[vllm-ask ${sessionId}] agent ${baseURL}/v1/chat/completions model=${modelName} ` +
    `max_out=${maxOut} effort=${reasoningEffort} cwd=${workDir} history=${session.messages.length} ` +
    `roles=${["system", ...session.messages.map((m) => m.role)].join(",")}`
  );

  let inTok = 0;
  let outTok = 0;
  let finalText = "";
  let lastSolid = "";
  let lastVisible = "";
  let allThinking = [];
  let apiModel = modelName;
  let preambleNudges = 0;
  let summaryNudges = 0;
  let forceTools = false;
  let compaction = null;
  const toolsThisAsk = [];
  let goToolchainProbes = 0;
  const systemContent = isolated
    ? [VLLM_SYSTEM, "You are an isolated review agent. Use only the tools you have. Save a written findings report under reports/ with the Write tool, then return a clear verdict: APPROVE, REJECT, or NEEDS_CHANGES, with findings.", system_prompt].filter(Boolean).join("\n\n")
    : (chatOnly
        ? [
            vllmCwdBanner(workDir),
            "Reply in a few sentences. Do not call tools. Do not inspect git or the repo unless asked.",
            String(system_prompt || "").slice(0, 3500),
          ].filter(Boolean).join("\n\n")
        : vllmSystemForRequest(system_prompt, workDir));
  const openaiMessages = [
    { role: "system", content: systemContent },
    ...session.messages,
  ];

  try {
    for (let turn = 0; turn < maxTurns; turn++) {
      if (abortState.aborted) break;
      const requireTools = forceTools;
      forceTools = false;
      if (!chatOnly && openaiMessages.length > 2) {
        const fill = vllmFillRatio(openaiMessages, maxOut, session.lastPromptTokens);
        if (fill.ratio >= VLLM_COMPACT_THRESHOLD) {
          send({ type: "progress", tool: "compact", input: `${Math.round(fill.ratio * 100)}% of usable context` });
          const c = await compactVllmSession({
            sessionId,
            messages: openaiMessages,
            systemContent,
            currentPrompt: prompt,
            baseURL,
            auth,
            modelName,
            usageRatio: fill.ratio,
            contextWindow: fill.ctx,
            tokensAtTrigger: fill.inputTok,
            triggeredBy: "auto",
            reason: `threshold_${Math.round(VLLM_COMPACT_THRESHOLD * 100)}pct`,
            abortState,
          });
          if (c.ok && Array.isArray(c.seed)) {
            compaction = c;
            session.lastPromptTokens = 0;
            session.messages = c.seed;
            openaiMessages.length = 0;
            openaiMessages.push({ role: "system", content: systemContent }, ...c.seed);
          } else {
            console.warn(`[compact-vllm ${sessionId}] not applied (${c.error || "no seed"}); trimming instead`);
          }
        }
      }
      // Always shrink to the real window before the call. Compact can fail
      // (empty summary, summary request 400) and must not leave the cron
      // session over the limit for the next run.
      let fitted = shrinkVllmToFit(openaiMessages, maxOut);
      if (fitted.changed) {
        session.lastPromptTokens = 0;
        session.messages = openaiMessages.filter((m) => m && m.role !== "system");
        console.warn(`[vllm-ask ${sessionId}] trimmed to ~${fitted.inputTok} tokens (ctx ${fitted.ctx}, msgs ${openaiMessages.length})`);
      } else if (fitted.maxOut < maxOut) {
        console.log(`[vllm-ask ${sessionId}] fit max_out ${maxOut}→${fitted.maxOut} est_in=${fitted.inputTok} msgs=${openaiMessages.length}`);
      }
      const call = (choice) => {
        let thinkBuf = "";
        let textBuf = "";
        let flushTimer = null;
        const flush = () => {
          flushTimer = null;
          if (thinkBuf) {
            send({ type: "progress", text: "💭 " + thinkBuf });
            thinkBuf = "";
          }
          if (textBuf) {
            send({ type: "progress", text: textBuf });
            textBuf = "";
          }
        };
        const onDelta = (d) => {
          if (d.firstByte) {
            send({ type: "progress", tool: "model", input: "first token" });
            return;
          }
          if (d.thinking) thinkBuf += d.thinking;
          if (d.content) textBuf += d.content;
          if (thinkBuf.length + textBuf.length > 24) flush();
          else if (!flushTimer) flushTimer = setTimeout(flush, 30);
        };
        const reqBody = {
          model: modelName,
          messages: openaiMessages,
          max_tokens: chatOnly ? Math.min(fitted.maxOut, 768) : fitted.maxOut,
        };
        if (!chatOnly) {
          reqBody.tools = vllmOpenAITools(allowedTools);
          reqBody.tool_choice = choice;
          reqBody.chat_template_kwargs = { enable_thinking: true, reasoning_effort: reasoningEffort };
        }
        return vllmChatStream(baseURL, auth, reqBody, abortState, onDelta).finally(() => {
          if (flushTimer) clearTimeout(flushTimer);
          flush();
        });
      };
      let json;
      let toolChoice = requireTools ? "required" : "auto";
      for (let attempt = 1; attempt <= 3 && !json; attempt++) {
        try {
          json = await call(toolChoice);
        } catch (err) {
          const msgErr = String(err.message || "");
          if (abortState.aborted || /aborted/i.test(msgErr)) break;
          const overflow = /maximum context length|input_tokens/i.test(msgErr);
          if (overflow && attempt < 3) {
            noteVllmContextLimit(msgErr);
            console.warn(`[vllm-ask ${sessionId}] context overflow attempt ${attempt}; window=${vllmContextLimit()} msgs=${openaiMessages.length}`);
            send({ type: "progress", tool: "compact", input: "context full — trimming history" });
            if (attempt === 1 && openaiMessages.length > 3) {
              const c = await compactVllmSession({
                sessionId,
                messages: openaiMessages,
                systemContent,
                currentPrompt: prompt,
                baseURL,
                auth,
                modelName,
                usageRatio: 1,
                contextWindow: vllmContextLimit(),
                tokensAtTrigger: fitted.inputTok,
                triggeredBy: "auto",
                reason: "context_overflow",
                abortState,
              });
              if (c.ok && Array.isArray(c.seed)) {
                compaction = c;
                openaiMessages.length = 0;
                openaiMessages.push({ role: "system", content: systemContent }, ...c.seed);
              }
            }
            // Server disagreed with the estimate. Cut bodies even when the
            // char count still looks safe, then fit to the learned window.
            if (!parseVllmContextLimit(msgErr)) {
              for (const m of openaiMessages) {
                if (typeof m.content === "string" && m.content.length > 800) {
                  capVllmMessage(m, Math.floor(m.content.length / 2));
                }
              }
            }
            fitted = shrinkVllmToFit(openaiMessages, Math.min(fitted.maxOut || maxOut, 4096));
            session.lastPromptTokens = 0;
            session.messages = openaiMessages.filter((m) => m && m.role !== "system");
            toolChoice = "auto";
            continue;
          }
          if (toolChoice === "required" && !overflow) {
            console.warn(`[vllm-ask ${sessionId}] tool_choice=required failed (${msgErr}); retry auto`);
            toolChoice = "auto";
            continue;
          }
          throw err;
        }
      }
      if (!json || abortState.aborted) break;
      inTok += json.usage?.prompt_tokens || 0;
      outTok += json.usage?.completion_tokens || 0;
      if (json.usage && json.usage.prompt_tokens) {
        session.lastPromptTokens = json.usage.prompt_tokens;
      }
      if (json.model) apiModel = json.model;

      const choice = json.choices && json.choices[0];
      const msgOut = choice?.message || {};
      const toolCalls = Array.isArray(msgOut.tool_calls) ? msgOut.tool_calls : [];
      const { thinking, content: text } = splitVllmMessage(msgOut);
      if (thinking) {
        allThinking.push(thinking);
        send({ type: "progress", text: "💭 " + thinking });
      }
      if (text) {
        send({ type: "progress", text });
      }

      openaiMessages.push({
        role: "assistant",
        content: text || "",
        ...(toolCalls.length ? {
          tool_calls: toolCalls.map((tc) => {
            const fn = tc.function || tc;
            return {
              type: "function",
              id: tc.id,
              function: {
                name: fn.name || "unknown",
                arguments: stringifyToolArguments(fn.arguments || tc.arguments),
              },
            };
          }),
        } : {}),
      });
      session.messages = openaiMessages.filter((m) => m.role !== "system");

      const finish = String(choice?.finish_reason || "");
      const unfinished = looksLikeUnfinishedAnswer(text, thinking, finish);
      const visible = String(text || "").trim();
      if (visible) lastVisible = visible;
      if (visible && !unfinished && visible.length > 60) {
        lastSolid = visible;
        finalText = visible;
      }

      if (toolCalls.length === 0) {
        if (chatOnly && visible) {
          finalText = visible;
          break;
        }
        if (isolated) {
          // Review DNA is treated as "unfinished" by the coding-task heuristics
          // (long thinking + a short verdict). Extending to 60 turns is what
          // left the 3090 pegged after the agent looked done.
          const wroteReport = toolsThisAsk.includes("Write");
          if (visible && wroteReport) {
            finalText = visible;
            break;
          }
          if (visible && !wroteReport && summaryNudges < 1 && allowedTools && allowedTools.includes("Write")) {
            summaryNudges++;
            openaiMessages.push({
              role: "user",
              content: "Your review text is done. REQUIRED: Write the findings report under reports/ (create the directory via Write) then stop. Do not edit source files.",
            });
            reseatVllmMessages(openaiMessages, session, prompt, systemContent);
            continue;
          }
          if (visible) {
            finalText = visible;
            break;
          }
          if (summaryNudges < 1) {
            summaryNudges++;
            openaiMessages.push({
              role: "user",
              content: "Write your final review and verdict now as plain text. If you still need a file, call Read/Glob/Grep. To save the required findings report, Write under reports/ only.",
            });
            reseatVllmMessages(openaiMessages, session, prompt, systemContent);
            continue;
          }
          finalText = lastVisible || lastSolid || "";
          break;
        }
        const planOnly = !chatOnly && unfinished && looksLikePlanStub(visible) && toolsThisAsk.length === 0;
        // A plan with no tools yet still needs Edit/Write/Bash.
        if (planOnly && preambleNudges < 8) {
          if (turn >= maxTurns - 1) {
            maxTurns = Math.min(turn + 6, 60);
          }
          preambleNudges++;
          forceTools = true;
          const stub = visible.slice(-400);
          console.log(`[vllm-ask ${sessionId}] unfinished (finish=${finish || "stop"} nudges=${preambleNudges} chars=${visible.length} turn=${turn}/${maxTurns}) — continuing`);
          send({ type: "progress", tool: "continue", input: finish === "length" ? "output truncated" : "incomplete reply" });
          openaiMessages.push({
            role: "user",
            content: "That was not the final answer (cut off or only a next-step stub). Continue FROM THERE — call Edit/Write/Bash and finish the work. Do not restart the plan." +
              (stub ? `\nLast visible line:\n${stub}` : ""),
          });
          reseatVllmMessages(openaiMessages, session, prompt, systemContent);
          continue;
        }
        // Empty content, thinking-only, or unfinished after tools: ask for a
        // written answer. Forcing more tools here is how Telegram got "(no output)".
        if ((!visible || unfinished) && summaryNudges < 2) {
          if (turn >= maxTurns - 1) {
            maxTurns = Math.min(turn + 4, 60);
          }
          summaryNudges++;
          forceTools = false;
          console.log(`[vllm-ask ${sessionId}] empty/unfinished summary nudge ${summaryNudges} (finish=${finish || "stop"} chars=${visible.length} tools=${toolsThisAsk.length})`);
          send({ type: "progress", tool: "continue", input: "write the user-facing answer" });
          openaiMessages.push({
            role: "user",
            content: "Stop. Write the user-facing answer NOW as plain text (what you changed, what is left, any errors). " +
              "Do not only think. Call a tool only if something is still broken. Do not restart the plan.",
          });
          reseatVllmMessages(openaiMessages, session, prompt, systemContent);
          continue;
        }
        if (!unfinished && visible) finalText = visible;
        else if (lastSolid) finalText = lastSolid;
        else if (lastVisible) finalText = lastVisible;
        break;
      }

      // Tools still running — don't treat a "Let me edit:" line as the answer.
      // Leave one extra model turn after tools so it can write a real close.
      // Isolated review agents must not grow past the requested cap.
      // Cron must stop at its turn cap. Extending it let a briefing run
      // dozens of extra GPU calls after the gateway had already given up.
      if (!isolated && !cronAsk && turn >= maxTurns - 1) {
        maxTurns = Math.min(maxTurns + 3, 60);
      }

      for (const tc of toolCalls) {
        if (abortState.aborted) break;
        const toolName = tc.function?.name || tc.name || "unknown";
        const toolInput = parseToolArguments(tc.function?.arguments || tc.arguments);
        toolsThisAsk.push(toolName);
        if (toolName === "Bash") {
          const c = String(toolInput.command || "");
          if (/\bgo\s+(env|version)\b|GOPROXY|GOMODCACHE|ls vendor|vendor\/modules/.test(c)) {
            goToolchainProbes++;
          }
        }
        send({ type: "progress", tool: toolName, input: summarizeInput(toolInput) });
        console.log(`[vllm-ask ${sessionId}] tool ${toolName}`);
        if (allowedTools && !allowedTools.includes(toolName)) {
          openaiMessages.push({
            role: "user",
            content: `<tool_response>\nerror: tool ${toolName} is not allowed for this agent\n</tool_response>`,
          });
          continue;
        }
        const out = await vllmExecTool(toolName, toolInput, workDir, abortState, send, allowedTools);
        openaiMessages.push({
          role: "user",
          content: `<tool_response>\n${vllmTruncate(out, 8000)}\n</tool_response>`,
        });
      }
      if (goToolchainProbes >= 2) {
        openaiMessages.push({
          role: "user",
          content: "STOP diagnosing the Go toolchain. Vendor + GOTOOLCHAIN=local are already set. Silent go build is success. Continue the user's actual task. Do not run go env/go version again.",
        });
        goToolchainProbes = 0;
      }
      reseatVllmMessages(openaiMessages, session, prompt, systemContent);
    }

    session.lastActive = Date.now();
    session.ok = true;
    session.provider = "vllm";
    session.model = apiModel;
    session.contextWindow = vllmContextLimit();
    session.lastTurnTotal = inTok;
    if (!isolated) {
      sessions.set(sessionId, session);
      evictOldestSession();
    } else {
      sessions.delete(sessionId);
    }

    if (abortState.aborted) {
      send({ type: "result", text: finalText || lastVisible || "(Stopped by user)", session_id: sessionId });
      return;
    }

    const thinkingText = allThinking.join("\n\n").trim();
    let answer = (finalText || lastSolid || lastVisible || "").trim();
    if (!answer) {
      answer = composeVllmFallback({
        lastSolid,
        lastVisible,
        toolsUsed: toolsThisAsk,
        thinking: thinkingText,
      });
    } else if (!isolated && looksLikeUnfinishedAnswer(answer, "", "stop")) {
      answer = (lastSolid || answer) + "\n\n(Cut off mid-edit — send \"continue\".)";
    }
    console.log(`[vllm-ask ${sessionId}] done in=${inTok} out=${outTok} chars=${answer.length} thinking=${thinkingText.length}`);
    send({
      type: "result",
      text: answer,
      thinking: thinkingText || undefined,
      session_id: sessionId,
      api_model: apiModel,
      usage: {
        input_tokens: inTok,
        output_tokens: outTok,
        cache_creation_input_tokens: 0,
        cache_read_input_tokens: 0,
      },
      context: {
        // Last prompt, not the sum of every tool round. The sum walks past
        // the window and the next cron run looks like the context is full.
        tokens_used: session.lastPromptTokens || 0,
        context_window: vllmContextLimit(),
        usage_ratio_pct: Math.round(((session.lastPromptTokens || 0) / Math.max(1, vllmContextLimit() - maxOut)) * 100),
      },
      ...(compaction && compaction.ok ? { compaction } : {}),
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
    if (timeoutTimer) clearTimeout(timeoutTimer);
    activeRequests.delete(sessionId);
  }
}

// --- gateway-tool-approval-test-start ---
// Claude Code never auto-approves writes under a directory named `.claude`
// (protected path), even in acceptEdits and even when that directory is the
// cwd. The SDK then waits for canUseTool. With no callback the tool dies as
// "you haven't granted it yet", so @agent-creator cannot land files in
// /workspace/.claude/agents. Settings files stay blocked.
function gatewayNormPath(cwd, p) {
  if (!p || typeof p !== "string") return "";
  let s = p.replace(/\\/g, "/");
  if (!s.startsWith("/")) {
    const base = String(cwd || "/workspace").replace(/\/+$/, "");
    s = `${base}/${s.replace(/^\.\//, "")}`;
  }
  const parts = [];
  for (const seg of s.split("/")) {
    if (!seg || seg === ".") continue;
    if (seg === "..") parts.pop();
    else parts.push(seg);
  }
  return `/${parts.join("/")}`;
}

function isBlockedClaudeConfig(abs) {
  const n = String(abs || "").replace(/\\/g, "/");
  if (!n) return false;
  if (n === "/root/.claude" || n.startsWith("/root/.claude/")) return true;
  if (/\/\.claude\/settings(?:\.(?:local|managed))?\.json$/.test(n)) return true;
  if (n.endsWith("/.claude.json")) return true;
  return false;
}

function bashClaudeTargets(command, cwd) {
  const out = [];
  const re = /(?:^|[\s"'`(=:])((?:\/[\w.+@-]+)+(?:\/\.claude(?:\/[^\s"'`;|&$<>]*)?)|\/root\/\.claude[^\s"'`;|&$<>]*|(?:\.\.?\/)?\.claude(?:\/[^\s"'`;|&$<>]*)?)/g;
  const s = String(command || "");
  let m;
  while ((m = re.exec(s))) {
    const abs = gatewayNormPath(cwd, m[1]);
    if (abs) out.push(abs);
  }
  return out;
}

function approveGatewayTool(toolName, input, cwd) {
  const allow = () => ({ behavior: "allow", updatedInput: input });
  const deny = (message) => ({ behavior: "deny", message });
  if (toolName === "AskUserQuestion") {
    return deny("No keyboard is attached. Pick an approach and continue, or ask in your reply.");
  }
  const src = input && typeof input === "object" ? input : {};
  const paths = [];
  for (const key of ["file_path", "notebook_path", "path"]) {
    const abs = gatewayNormPath(cwd, src[key]);
    if (abs) paths.push(abs);
  }
  if (toolName === "Bash") paths.push(...bashClaudeTargets(src.command, cwd));
  for (const p of paths) {
    if (isBlockedClaudeConfig(p)) {
      return deny(`refusing to change Claude settings at ${p}. Files under /workspace/.claude/agents (and skills, teams, commands) are allowed.`);
    }
  }
  return allow();
}
// --- gateway-tool-approval-test-end ---

// --- sdk-max-turns-test-start ---
// Claude Code stops the process at maxTurns and the gateway used to forward
// "Reached maximum number of turns (40)" as the agent's final answer.
var SDK_MAX_TURN_CONTINUATIONS = 15;

function isSdkMaxTurns(subtype, text) {
  if (subtype === "error_max_turns") return true;
  return /reached maximum number of turns\s*\(\d+\)/i.test(String(text || ""));
}

function shouldContinueAfterMaxTurns(state) {
  if (!state || state.isolated) return false;
  if (!state.hitMaxTurns) return false;
  if (!state.sdkSessionId) return false;
  if (state.aborted) return false;
  const cap = state.maxContinuations == null ? SDK_MAX_TURN_CONTINUATIONS : state.maxContinuations;
  if ((state.continuations || 0) >= cap) return false;
  // A resume that immediately maxes out again without doing any work is stuck.
  if ((state.continuations || 0) > 0 && !state.toolsThisSlice) return false;
  return true;
}
// --- sdk-max-turns-test-end ---

function addSdkUsage(dst, src) {
  const out = dst || {};
  const inc = src || {};
  for (const k of ["input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"]) {
    out[k] = (out[k] || 0) + (inc[k] || 0);
  }
  return out;
}

// One Claude Code query. A max-turns stop is a result event, sometimes
// followed by the process exiting 1. Capture the result either way.
async function consumeSdkQuery(prompt, options, bag) {
  const { send, abortState, sessionId } = bag;
  let resultText = "";
  let subtype = "";
  let usage = {};
  let apiModel = bag.apiModel || "";
  let sdkSessionId = bag.sdkSessionId || "";
  let toolsThisSlice = 0;
  const conversation = query({ prompt, options });
  try {
    for await (const message of conversation) {
      if (abortState.aborted) {
        console.log(`[ws-ask ${sessionId}] aborted by user`);
        break;
      }
      const type = message.type;
      const sub = message.subtype ? `:${message.subtype}` : "";
      console.log(`[ws-ask ${sessionId}] event: ${type}${sub}`);

      if (type === "system" && message.subtype === "init") {
        sdkSessionId = message.session_id || sdkSessionId;
        continue;
      }

      if (type === "system") {
        const thought = sdkThinkingText(message);
        if (thought) sendThinkingProgress(send, thought.startsWith("💭") ? thought : "💭 " + thought);
        continue;
      }

      if (type === "assistant") {
        const msgModel = message.message?.model || message.model;
        if (msgModel) {
          apiModel = msgModel;
          console.log(`[ws-ask ${sessionId}]   api-model: ${apiModel}`);
        }
        const thought = sdkThinkingText(message);
        if (thought) sendThinkingProgress(send, thought.startsWith("💭") ? thought : "💭 " + thought);
        const content = message.content
          || (message.message && message.message.content)
          || [];
        if (Array.isArray(content)) {
          for (const block of content) {
            if (block.type === "tool_use") {
              toolsThisSlice++;
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
        subtype = message.subtype || "";
        resultText = subtype === "success"
          ? stripThinkTags(message.result || "")
          : ((message.errors || []).join("\n") || message.error || `Error: ${subtype}`);
        usage = message.usage || {};
        sdkSessionId = message.session_id || sdkSessionId;
        continue;
      }

      const preview = JSON.stringify(message).slice(0, 200);
      console.log(`[ws-ask ${sessionId}]   other: ${preview}`);
    }
  } catch (err) {
    if (abortState.aborted) {
      return { aborted: true, resultText, subtype, usage, apiModel, sdkSessionId, toolsThisSlice };
    }
    if (!resultText) throw err;
    console.log(`[ws-ask ${sessionId}] query ended after result (${subtype || "no subtype"}): ${err.message}`);
  }
  return {
    aborted: Boolean(abortState.aborted),
    resultText,
    subtype,
    usage,
    apiModel,
    sdkSessionId,
    toolsThisSlice,
  };
}

// --- WebSocket Handlers ---

async function handleAsk(msg, send, ws) {
  const { session_id, prompt, cwd, max_turns, system_prompt, allowed_tools, model, provider } = msg;

  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  if (provider === "grok") {
    await handleGrokAsk(msg, send, ws);
    return;
  }

  // Claude Code's own system prompt + hooks + CLAUDE.md cannot fit in
  // vLLM window. Talk to /v1/chat/completions directly.
  if (provider === "vllm") {
    await handleVllmAsk(msg, send, undefined, ws);
    return;
  }

  const sessionId = session_id || "default";
  let workDir;
  try {
    workDir = prepareWorkDir(cwd || DEFAULT_CWD);
  } catch (err) {
    send({ type: "error", error: err.message });
    return;
  }
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
  // owner is the gateway socket that started this turn. A different
  // provider's socket closing must not set aborted — that is what turned
  // a reconnect into "(Stopped by user)" on every DeepSeek agent at once.
  const abortState = { aborted: false, owner: ws };
  activeRequests.set(sessionId, abortState);

  try {
    const options = {
      cwd: workDir,
      permissionMode: "acceptEdits",
      // Protected-path prompts (.claude/agents, .git, …) have no human to
      // click Allow. Approve them here, except Claude's own settings files.
      canUseTool: async (toolName, input) => {
        const decision = approveGatewayTool(toolName, input, workDir);
        if (decision.behavior === "deny") {
          console.warn(`[ws-ask ${sessionId}] denied ${toolName}: ${decision.message}`);
        } else {
          console.log(`[ws-ask ${sessionId}] approved prompted ${toolName}`);
        }
        return decision;
      },
    };

    if (max_turns && max_turns > 0) options.maxTurns = max_turns;
    const localGpu = provider === "vllm" || provider === "ollama";
    const isolated = /(?:^|:)orch-/.test(String(sessionId)) || Boolean(msg.isolated);
    // Telegram (every @agent and @default) must work like Qwen @default:
    // no Task/subagents, no SessionStart workspace dump, no go-compile hook
    // that hangs `go vet` after every Edit. Isolated orchestrator reviews
    // keep the full Claude Code tool set.
    const telegramTools = allowed_tools && allowed_tools.length
      ? allowed_tools
      : ["Read", "Write", "Edit", "Bash", "Glob", "Grep", "WebFetch", "WebSearch"];
    const noSubagents = [
      "Task", "TaskOutput", "TaskStop", "Agent", "TodoWrite",
      "Skill", "NotebookEdit", "Sleep", "BashOutput", "KillShell",
      "ExitPlanMode", "EnterPlanMode",
    ];
    if (localGpu) {
      options.allowedTools = allowed_tools || [
        "Read", "Write", "Edit", "Bash", "Glob", "Grep",
      ];
      options.disallowedTools = noSubagents;
      options.settingSources = [];
      options.systemPrompt =
        "You are a coding assistant running locally (Qwen3.8-27B). " +
        "The context window is 65536 tokens. Prefer small diffs. " +
        "Do not dump whole files. Thinking is discarded; put the full analysis in the final answer.";
    } else if (!isolated) {
      options.additionalDirectories = ["/clients/current", "/clients/past"];
      options.allowedTools = telegramTools;
      options.disallowedTools = noSubagents;
      options.settingSources = [];
      options.systemPrompt = [
        "Work like the main Telegram assistant: edit the files, small diffs, then answer. ",
        "Never use Task, Agent, or background compiles. Never diagnose GOPROXY, go env, go version, or vendor. ",
        "Do not run go build/vet unless Steve asked or the compiler error is the task. ",
        "Cwd is already the project if WORKING DIRECTORY is set — do not cd or search /workspace.",
        system_prompt ? "\n\n" + system_prompt : "",
      ].join("");
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
    options.env = withToolPath({
      ...(options.env || process.env),
      HOME: "/root",
      PWD: workDir,
      CLAUDE_CODE_DISABLE_SANDBOX: "1",
    }, workDir);
    if (!isolated) {
      options.env.CLAUDE_GATEWAY_SKIP_GO_HOOK = "1";
    }
    // Bash this ask starts (the content pipeline, quality control, repair)
    // reads these and must not pick a different model.
    stampChildModelEnv(options.env, provider, options.model || model);
    // Claude Code's default sandbox isolates GOCACHE and still uses
    // GOTOOLCHAIN=auto against go.dev — `go test` then "hangs" at startup.
    options.sandbox = { enabled: false };

    // vLLM: only resume after a successful turn. Failed 400s left an SDK
    // session whose prompt only grows. Other providers resume unless we
    // marked the last turn as failed.
    const canResume = session?.sdkSessionId && (
      localGpu ? session.ok === true : session.ok !== false
    );
    let resumeId = canResume ? session.sdkSessionId : undefined;

    options.stderr = (data) => {
      process.stderr.write(`[claude-sdk] ${data}`);
      const clean = cleanStderr(data);
      if (!isSdkNoise(clean)) {
        send({ type: "progress", text: clean.slice(0, 300) });
      }
    };

    const claudeBin = await resolveClaudeExecutable();
    if (!claudeBin.path) {
      throw new Error(
        "Claude Code native binary failed to launch. " +
        (claudeBin.failures.join("; ") || "no candidate binary")
      );
    }
    await assertClaudeLaunch(claudeBin.path, workDir);
    options.pathToClaudeCodeExecutable = claudeBin.path;

    // DeepSeek/Claude Code stops at maxTurns (40 from Telegram) and that
    // used to be the whole reply. Resume the same session and keep working.
    const sliceTurns = (max_turns && max_turns > 0) ? max_turns : 40;
    options.maxTurns = sliceTurns;
    let promptForQuery = effectivePrompt;
    let continuations = 0;
    let totalUsage = {};
    let lastSliceUsage = {};
    while (true) {
      if (resumeId) options.resume = resumeId;
      else delete options.resume;
      console.log(`[ws-ask ${sessionId}] query (cwd: ${workDir}, model: ${options.model || "default"}, provider: ${provider || "claude"}, resume: ${resumeId || "none"}, seeded: ${consumedSeed && continuations === 0 ? "yes" : "no"}, slice: ${continuations})`);
      const slice = await consumeSdkQuery(promptForQuery, options, {
        send, abortState, sessionId, apiModel, sdkSessionId,
      });
      if (slice.apiModel) apiModel = slice.apiModel;
      if (slice.sdkSessionId) sdkSessionId = slice.sdkSessionId;
      resultText = slice.resultText || "";
      lastSliceUsage = slice.usage || {};
      totalUsage = addSdkUsage(totalUsage, lastSliceUsage);
      if (slice.aborted || abortState.aborted) break;
      const hitMax = isSdkMaxTurns(slice.subtype, resultText);
      if (!shouldContinueAfterMaxTurns({
        isolated,
        hitMaxTurns: hitMax,
        sdkSessionId,
        aborted: abortState.aborted,
        continuations,
        maxContinuations: SDK_MAX_TURN_CONTINUATIONS,
        toolsThisSlice: slice.toolsThisSlice,
      })) {
        if (hitMax && !isolated) {
          console.warn(`[ws-ask ${sessionId}] turn budget exhausted after ${continuations} continuation(s)`);
          resultText = "Still working when the turn budget ran out. Send continue and I will pick up from here.";
        }
        break;
      }
      continuations++;
      resumeId = sdkSessionId;
      promptForQuery = "Continue the task from the exact point you stopped. The turn limit is not a reason to stop or summarise. Do not recap. Do not restart. Finish the remaining work, then write the user-facing answer.";
      const note = `turn limit — continuing (${continuations}/${SDK_MAX_TURN_CONTINUATIONS})`;
      console.log(`[ws-ask ${sessionId}] ${note}`);
      send({ type: "progress", tool: "continue", input: note });
    }
    usage = totalUsage;
    // Context pressure is the last slice, not the sum of every resume.
    const usageForContext = lastSliceUsage;

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
      (usageForContext.input_tokens || 0) +
      (usageForContext.cache_read_input_tokens || 0) +
      (usageForContext.cache_creation_input_tokens || 0);
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

    if (err && typeof err.message === "string" && err.message.includes("failed to launch")) {
      let exists = false;
      try { exists = fs.statSync(workDir).isDirectory(); } catch { exists = false; }
      if (!exists) {
        err.message = `Working directory does not exist: ${workDir}. Claude Code reports that as a native-binary libc mismatch.`;
      }
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

async function handleGrokAsk(msg, send, ws) {
  const { session_id, prompt, cwd, system_prompt, model, allowed_tools } = msg;
  const sessionId = session_id || "default";
  const isolated = /(?:^|:)orch-/.test(String(sessionId)) || Boolean(msg.isolated);
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
  if (isolated && Array.isArray(allowed_tools) && allowed_tools.length) {
    effectivePrompt =
      `You are an isolated review agent. Use only: ${allowed_tools.join(", ")}. ` +
      `Do not edit files. Return APPROVE, REJECT, or NEEDS_CHANGES with findings.\n\n` +
      effectivePrompt;
  }
  if (system_prompt) {
    effectivePrompt = system_prompt + "\n\n---\n\n" + effectivePrompt;
  }

  const keepalive = setInterval(() => {
    send({ type: "heartbeat" });
  }, 15000);

  const abortState = { aborted: false, child: null, owner: ws };
  activeRequests.set(sessionId, abortState);

  if (isolated) {
    resetGrokSession(sessionId);
  }
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
        env: stampChildModelEnv(grokSubscriptionEnv(), "grok", effectiveModel),
        stdio: ["ignore", "pipe", "pipe"],
      });
      abortState.child = child;

      let assembled = "";
      let stderrRaw = "";
      let lineBuf = "";
      let thoughtOpen = false;

      const handleLine = (line) => {
        if (!line) return;
        let ev;
        try {
          ev = JSON.parse(line);
        } catch {
          return;
        }
        const thought = grokThoughtText(ev);
        if (thought) {
          if (!thoughtOpen) {
            sendThinkingProgress(send, "💭 " + String(thought).replace(/^\s+/, ""));
            thoughtOpen = true;
          } else {
            sendThinkingProgress(send, thought);
          }
          return;
        }
        const chunk = grokVisibleText(ev);
        if (chunk) {
          assembled += chunk;
          send({ type: "progress", text: thoughtOpen ? "\n" + chunk : chunk });
          thoughtOpen = false;
          return;
        }
        const tool = grokToolLabel(ev);
        if (tool) {
          thoughtOpen = false;
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
    send({ type: "error", error: grokSignInError(err.message), is_rate_limited: is429(err.message) });
  } finally {
    clearInterval(keepalive);
    activeRequests.delete(sessionId);
    try { fs.unlinkSync(promptFile); } catch (_) {}
  }
}

async function handleRunAgent(msg, send, ws) {
  const { prompt, system_prompt, cwd, allowed_tools, timeout_ms, model, provider } = msg;

  if (!prompt) {
    send({ type: "error", error: "prompt is required" });
    return;
  }

  if (provider === "grok") {
    await handleGrokAsk(msg, send, ws);
    return;
  }
  if (provider === "vllm") {
    const isolated = {
      ...msg,
      session_id: msg.session_id || `orch-${randomUUID()}`,
      isolated: true,
      max_turns: msg.max_turns || 12,
    };
    sessions.delete(isolated.session_id);
    await handleVllmAsk(isolated, send, undefined, ws);
    sessions.delete(isolated.session_id);
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

  const childEnv = withToolPath(
    overlay ? applyProviderEnv(overlay) : { ...process.env, HOME: "/root" },
    workDir,
  );
  childEnv.PWD = workDir;
  stampChildModelEnv(childEnv, provider, effectiveModel);

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
  const claudeBin = await resolveClaudeExecutable();
  if (claudeBin.path) queryOptions.pathToClaudeCodeExecutable = claudeBin.path;

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

  if (sess && (sess.provider === "vllm" || (Array.isArray(sess.messages) && sess.messages.length && !sess.sdkSessionId))) {
    let overlay;
    try {
      overlay = buildProviderOverlay("vllm", msg.model || sess.model);
    } catch (e) {
      send({ type: "compact-result", session_id: sessionId, ok: false, error: e.message });
      return;
    }
    const baseURL = overlay.env.ANTHROPIC_BASE_URL.replace(/\/$/, "");
    const result = await compactVllmSession({
      sessionId,
      messages: [{ role: "system", content: VLLM_SYSTEM }, ...sess.messages],
      currentPrompt: "",
      baseURL,
      auth: overlay.env.ANTHROPIC_AUTH_TOKEN || "dummy",
      modelName: overlay.model,
      usageRatio: sess.contextWindow ? (sess.lastTurnTotal || 0) / sess.contextWindow : 0,
      contextWindow: sess.contextWindow || vllmContextLimit(),
      tokensAtTrigger: sess.lastTurnTotal || 0,
      triggeredBy: "manual",
      reason: "manual",
    });
    if (result.ok && Array.isArray(result.seed)) {
      sess.messages = result.seed;
      sess.lastTurnTotal = 0;
      sessions.set(sessionId, sess);
    }
    send({ type: "compact-result", session_id: sessionId, ok: !!result.ok, compaction: result });
    return;
  }

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

// --- ws-close-test-start ---
// One gateway process opens a socket per provider (claude, deepseek, grok,
// qwen). Closing one must not stop the others. DeepSeek/Claude/Grok jobs
// are not socket-tied: a reconnect is not a user /stop. Only a local GPU
// turn owned by the closed socket is aborted, otherwise the 3090 keeps
// generating for a client that has gone.
function requestsToAbortOnClose(activeRequests, closedWs) {
  const out = [];
  if (!activeRequests || !closedWs) return out;
  for (const [sid, st] of activeRequests) {
    if (!st || st.owner !== closedWs) continue;
    if (st.localGpu) out.push(sid);
  }
  return out;
}
// --- ws-close-test-end ---

const httpServer = createServer(app);
const wss = new WebSocketServer({ server: httpServer, path: "/ws" });

wss.on("connection", (ws) => {
  console.log("Gateway connected via WebSocket");
  ws.isAlive = true;

  ws.on("pong", () => { ws.isAlive = true; ws.missedPongs = 0; });

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
          case "ask": await handleAsk(msg, send, ws); break;
          case "run-agent": await handleRunAgent(msg, send, ws); break;
          case "reset": handleReset(msg, send); break;
          case "check-session": handleCheckSession(msg, send); break;
          case "compact": await handleCompact(msg, send); break;
          case "abort": {
            const sid = msg.session_id || "default";
            const target = activeRequests.get(sid);
            if (target) {
              markAborted(target);
              console.log(`[ws-abort] Abort flag set for session ${sid}`);
            } else {
              console.log(`[ws-abort] No active request for session ${sid}`);
            }
            // Isolated review agents use orch-* (sidecar) or vllm:orch-*
            // (gateway). Only cascade from the main chat session (tg-123),
            // not from a named agent thread (tg-123-ag-seoagent).
            if (/(^|:)tg-\d+$/.test(sid)) {
              for (const [id, st] of activeRequests) {
                if (id === sid) continue;
                if (/(^|:)orch-/.test(id)) {
                  markAborted(st);
                  console.log(`[ws-abort] also aborted isolated ${id}`);
                }
              }
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

  ws.on("close", () => {
    const victims = requestsToAbortOnClose(activeRequests, ws);
    console.log(`Gateway socket closed; stopping ${victims.length} local-gpu request(s). DeepSeek/Claude/Grok sessions keep running.`);
    for (const sid of victims) {
      markAborted(activeRequests.get(sid));
      console.log(`[ws-close] aborted in-flight ${sid}`);
    }
  });
  ws.on("error", (err) => { console.error("WebSocket error:", err.message); });
});

// Ping all clients every 30s. One missed pong used to terminate the socket,
// and the close handler then aborted every in-flight agent. Three misses
// (about 90s) is a dead socket. Live DeepSeek turns survive a single stall.
const pingInterval = setInterval(() => {
  wss.clients.forEach((ws) => {
    if (!ws.isAlive) {
      ws.missedPongs = (ws.missedPongs || 0) + 1;
      if (ws.missedPongs >= 3) return ws.terminate();
    } else {
      ws.missedPongs = 0;
    }
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
  resolveClaudeExecutable().catch((err) => {
    console.error(`[claude-bin] probe failed: ${err.message}`);
  });
  logProviderKeysAtBoot();
  const hs = loadHarnessSkills();
  console.log(`[harness] ${hs.length} skill(s) from ${HARNESS_SKILLS_DIR} always=${hs.filter((s) => s.always).map((s) => s.name).join(",") || "none"}`);
  const bk = braveApiKey();
  console.log(
    bk
      ? `[harness] web search=brave source=${GATEWAY_ENV_FILE} last4=${keyLast4(bk)} len=${bk.length}`
      : `[harness] web search=duckduckgo (no BRAVE_API_KEY or BRAVE_API in ${GATEWAY_ENV_FILE})`
  );
});

// timeout is inactivity on the HTTP socket. A non-zero value cuts a
// WebSocket out from under a long DeepSeek turn (the bot allows 120 min).
httpServer.timeout = 0;
httpServer.requestTimeout = 0;
httpServer.headersTimeout = 120000;
httpServer.keepAliveTimeout = 30000;
