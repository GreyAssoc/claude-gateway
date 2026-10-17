import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import vm from "node:vm";

const src = fs.readFileSync(new URL("./index.js", import.meta.url), "utf8");
const start = src.indexOf("// --- ws-close-test-start ---");
const end = src.indexOf("// --- ws-close-test-end ---");
if (start < 0 || end < start) {
  throw new Error("ws-close test region missing from sidecar/index.js");
}

const ctx = vm.createContext({});
vm.runInContext(src.slice(start, end), ctx);
const { requestsToAbortOnClose } = ctx;

// The helper runs inside vm, so its array is a different realm from the
// test's Array. Copy into host strings before comparing.
function abortIds(active, sock) {
  return Array.from(requestsToAbortOnClose(active, sock), (id) => String(id));
}

test("closing one provider socket does not stop DeepSeek sessions", () => {
  const deepseekSock = { name: "deepseek" };
  const claudeSock = { name: "claude" };
  const active = new Map([
    ["deepseek:tg-1-ag-tbt", { owner: deepseekSock, aborted: false }],
    ["deepseek:tg-1-ag-grey-website", { owner: deepseekSock, aborted: false }],
    ["vllm:tg-1-ag-qwen", { owner: claudeSock, localGpu: true }],
  ]);
  assert.deepEqual(abortIds(active, claudeSock), ["vllm:tg-1-ag-qwen"]);
  assert.deepEqual(abortIds(active, deepseekSock), []);
});

test("a socket with no owned gpu work aborts nothing", () => {
  const deepseekSock = {};
  const other = {};
  const active = new Map([
    ["deepseek:tg-9-ag-tbt", { owner: deepseekSock }],
  ]);
  assert.deepEqual(abortIds(active, other), []);
  assert.deepEqual(abortIds(active, deepseekSock), []);
  assert.deepEqual(abortIds(null, other), []);
});
