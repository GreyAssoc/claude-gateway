import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import vm from "node:vm";

const src = fs.readFileSync(new URL("./index.js", import.meta.url), "utf8");
const start = src.indexOf("// --- vllm-fit-test-start ---");
const end = src.indexOf("// --- vllm-fit-test-end ---");
if (start < 0 || end < start) throw new Error("vllm fit test region missing");

function loadFit() {
  const ctx = vm.createContext({});
  vm.runInContext(src.slice(start, end), ctx);
  return ctx;
}

test("context-limit error is parsed", () => {
  const f = loadFit();
  const err = "This model's maximum context length is 65536 tokens. However, you requested 90000 tokens (80000 in the messages, 10000 in the completion).";
  assert.equal(f.parseVllmContextLimit(err), 65536);
  assert.equal(f.parseVllmContextLimit("something else"), 0);
});

test("tool call arguments count toward the estimate", () => {
  const f = loadFit();
  const withCalls = [{ role: "assistant", content: "ok", tool_calls: [{ function: { name: "Bash", arguments: "x".repeat(2000) } }] }];
  const plain = [{ role: "assistant", content: "ok" }];
  assert.ok(f.vllmInputTokens(withCalls, 0) > f.vllmInputTokens(plain, 0) + 500);
});

test("last prompt_tokens forces pressure when the char estimate is low", () => {
  const f = loadFit();
  const messages = [{ role: "system", content: "short" }, { role: "user", content: "hi" }];
  const low = f.vllmPressureRatio(messages, 1024, 65536, 0, 0);
  const high = f.vllmPressureRatio(messages, 1024, 65536, 60000, 0);
  assert.ok(low.ratio < 0.7);
  assert.ok(high.ratio > 0.9);
});

test("shrink keeps the latest user turn and drops the old tool pair", () => {
  const f = loadFit();
  const messages = [
    { role: "system", content: "sys" },
    { role: "user", content: "old question " + "a".repeat(3000) },
    { role: "assistant", content: "", tool_calls: [{ id: "1", function: { name: "Read", arguments: "{}" } }] },
    { role: "user", content: "<tool_response>\n" + "b".repeat(8000) + "\n</tool_response>" },
    { role: "user", content: "latest question that must survive" },
  ];
  // Tiny window so the old turns cannot fit. toolsTok 0 so the constant isn't the story.
  const fit = f.shrinkVllmToFitLimit(messages, 256, 1500, 0);
  const texts = messages.map((m) => String(m.content || ""));
  assert.equal(messages[0].role, "system");
  assert.ok(texts.some((t) => t.includes("latest question")));
  assert.ok(!texts.some((t) => t.includes("old question")));
  assert.ok(!texts.some((t) => t.startsWith("<tool_response>")));
  assert.equal(fit.overflow, false);
});

test("a single huge prompt is truncated instead of left over the limit", () => {
  const f = loadFit();
  const messages = [
    { role: "system", content: "sys" },
    { role: "user", content: "P".repeat(20000) },
  ];
  const fit = f.shrinkVllmToFitLimit(messages, 256, 2000, 0);
  assert.ok(messages[1].content.includes("[truncated]"));
  assert.ok(messages[1].content.length < 20000);
  assert.equal(fit.overflow, false);
  assert.equal(fit.changed, true);
});
