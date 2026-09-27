import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import vm from "node:vm";

const src = fs.readFileSync(new URL("./index.js", import.meta.url), "utf8");
const start = src.indexOf("// --- sdk-max-turns-test-start ---");
const end = src.indexOf("// --- sdk-max-turns-test-end ---");
if (start < 0 || end < start) throw new Error("max-turns test region missing");

function load() {
  const ctx = vm.createContext({});
  vm.runInContext(src.slice(start, end), ctx);
  return ctx;
}

test("detects the Claude Code turn-limit result", () => {
  const m = load();
  assert.equal(m.isSdkMaxTurns("error_max_turns", ""), true);
  assert.equal(m.isSdkMaxTurns("", "Reached maximum number of turns (40)"), true);
  assert.equal(m.isSdkMaxTurns("success", "Wrote the adapter."), false);
});

test("a deepseek chat resumes after the turn limit", () => {
  const m = load();
  assert.equal(m.shouldContinueAfterMaxTurns({
    isolated: false,
    hitMaxTurns: true,
    sdkSessionId: "sess-1",
    aborted: false,
    continuations: 0,
    toolsThisSlice: 12,
  }), true);
});

test("isolated reviews and stuck resumes do not continue", () => {
  const m = load();
  assert.equal(m.shouldContinueAfterMaxTurns({
    isolated: true,
    hitMaxTurns: true,
    sdkSessionId: "sess-1",
    continuations: 0,
    toolsThisSlice: 4,
  }), false);
  assert.equal(m.shouldContinueAfterMaxTurns({
    isolated: false,
    hitMaxTurns: true,
    sdkSessionId: "sess-1",
    continuations: 1,
    toolsThisSlice: 0,
  }), false);
  assert.equal(m.shouldContinueAfterMaxTurns({
    isolated: false,
    hitMaxTurns: true,
    sdkSessionId: "",
    continuations: 0,
    toolsThisSlice: 3,
  }), false);
});

test("continuation stops at the budget", () => {
  const m = load();
  assert.equal(m.shouldContinueAfterMaxTurns({
    isolated: false,
    hitMaxTurns: true,
    sdkSessionId: "sess-1",
    continuations: m.SDK_MAX_TURN_CONTINUATIONS,
    toolsThisSlice: 8,
  }), false);
  assert.equal(m.shouldContinueAfterMaxTurns({
    isolated: false,
    hitMaxTurns: true,
    sdkSessionId: "sess-1",
    continuations: m.SDK_MAX_TURN_CONTINUATIONS - 1,
    toolsThisSlice: 8,
  }), true);
});
