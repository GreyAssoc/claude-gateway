import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import test from "node:test";
import vm from "node:vm";

const src = fs.readFileSync(new URL("./index.js", import.meta.url), "utf8");
const start = src.indexOf("// --- gpu-queue-test-start ---");
const end = src.indexOf("// --- gpu-queue-test-end ---");
if (start < 0 || end < start) {
  throw new Error("gpu queue test region missing from sidecar/index.js");
}

function loadQueue() {
  const ctx = vm.createContext({ console, setTimeout, clearTimeout });
  vm.runInContext(src.slice(start, end), ctx);
  return ctx;
}

function agent(name, lines) {
  return {
    sessionId: `vllm:tg-1-ag-${name}`,
    send(ev) {
      if (lines && ev && ev.input) lines.push(ev.input);
    },
  };
}

test("socket close mid-stream is an abort, a finished body is not", () => {
  const q = loadQueue();
  assert.equal(q.vllmResponseEvent("end", true, false), "resolve");
  assert.equal(q.vllmResponseEvent("close", true, false), "ignore");
  assert.equal(q.vllmResponseEvent("close", false, false), "reject");
  assert.equal(q.vllmResponseEvent("aborted", false, false), "reject");
  assert.equal(q.vllmResponseEvent("close", false, true), "ignore");
});

// Node does not emit req 'error' or res 'end' when destroy() runs after
// response headers. The handlers in index.js key off this.
test("mid-stream destroy matches the abort decision", async () => {
  const q = loadQueue();
  const server = http.createServer((req, res) => {
    res.writeHead(200, { "content-type": "text/event-stream" });
    const t = setInterval(() => res.write("data: x\n\n"), 15);
    res.on("close", () => clearInterval(t));
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address();
  try {
    const decision = await new Promise((resolve, reject) => {
      const req = http.request({ hostname: "127.0.0.1", port, path: "/", method: "POST" }, (res) => {
        let settled = false;
        const actions = [];
        const note = (kind) => {
          const action = q.vllmResponseEvent(kind, Boolean(res.complete), settled);
          actions.push(kind + ":" + action);
          if (action === "resolve" || action === "reject") settled = true;
        };
        res.on("end", () => note("end"));
        res.on("aborted", () => note("aborted"));
        res.on("close", () => note("close"));
        res.on("error", () => note("error"));
        req.on("error", () => actions.push("req-error"));
        setTimeout(() => {
          try { req.destroy(); } catch (err) { reject(err); }
          setTimeout(() => resolve(actions), 40);
        }, 40);
      });
      req.on("error", () => {});
      req.end();
    });
    assert.ok(decision.includes("aborted:reject") || decision.includes("close:reject"), decision.join(","));
    assert.ok(!decision.includes("end:resolve"), decision.join(","));
  } finally {
    server.close();
  }
});

test("stopped agent does not stay in front of the next one", async () => {
  const q = loadQueue();
  q.resetGpuQueueForTests();
  const greyLines = [];
  const nextLines = [];
  const grey = agent("greydocs", greyLines);
  let rejectGrey;
  const stopped = q.enqueueVllm(() => new Promise((resolve, reject) => {
    grey.cancel = (err) => reject(err);
    rejectGrey = reject;
    void resolve;
  }), grey);
  const started = q.enqueueVllm(() => Promise.resolve("ran"), agent("agentcreator", nextLines));

  await new Promise((r) => setImmediate(r));
  assert.ok(nextLines.some((l) => l.includes("behind @greydocs")), nextLines.join(" | "));

  q.abortInFlight(grey);
  // cancel() is what /stop uses. Also reject if the socket event wins the race.
  if (rejectGrey) {
    try { rejectGrey(new Error("aborted")); } catch (_) { /* already rejected */ }
  }

  const result = await Promise.race([
    started,
    new Promise((_, reject) => setTimeout(() => reject(new Error("next agent still queued behind stopped agent")), 500)),
  ]);
  assert.equal(result, "ran");
  await assert.rejects(stopped, /aborted/);
  assert.match(nextLines.at(-1), /GPU: generating \(@agentcreator\)/);
  assert.equal(q.gpuTickets.length, 0);
});

test("a stopped waiter is removed so a newer job is not queued behind it", async () => {
  const q = loadQueue();
  q.resetGpuQueueForTests();
  const holder = agent("greydocs", []);
  let releaseHold;
  const holding = q.enqueueVllm(() => new Promise((resolve) => { releaseHold = resolve; }), holder);
  const waitingLines = [];
  const waiterState = agent("agentcreator", waitingLines);
  const waiting = q.enqueueVllm(() => Promise.resolve("should-not-run"), waiterState);
  const freshLines = [];
  const fresh = q.enqueueVllm(() => Promise.resolve("fresh"), agent("agentcreator", freshLines));

  await new Promise((r) => setImmediate(r));
  q.abortInFlight(waiterState);

  const behind = freshLines.at(-1) || "";
  assert.match(behind, /behind @greydocs/);
  assert.doesNotMatch(behind, /behind @greydocs, @agentcreator/);

  releaseHold("done");
  assert.equal(await holding, "done");
  await assert.rejects(waiting, /aborted/);
  assert.equal(await fresh, "fresh");
  assert.match(freshLines.at(-1), /GPU: generating \(@agentcreator\)/);
});

test("cron jobs are not labeled @default", () => {
  const q = loadQueue();
  const name = (sessionId) => q.gpuAgentName({ sessionId });
  assert.equal(name("vllm:cron-end_of_day"), "cron-end_of_day");
  assert.equal(name("vllm:cron-morning_briefing"), "cron-morning_briefing");
  assert.equal(name("vllm:cron-home_solar_battery-ag-homesolar"), "homesolar");
  assert.equal(name("vllm:tg-42"), "default");
  assert.equal(name("vllm:tg-42-ag-homesolar"), "homesolar");
  assert.notEqual(name("vllm:cron-end_of_day"), "default");
});

test("a hung generation does not stay in front after abort", async () => {
  const q = loadQueue();
  q.resetGpuQueueForTests();
  q.gpuAbortReleaseMs = 30;
  const hungState = agent("greydocs", []);
  const hanging = q.enqueueVllm(() => new Promise(() => {}), hungState);
  await new Promise((r) => setImmediate(r));
  q.abortInFlight(hungState);
  const lines = [];
  const next = q.enqueueVllm(() => Promise.resolve("next"), agent("homesolar", lines));
  const result = await Promise.race([
    next,
    new Promise((_, reject) => setTimeout(() => reject(new Error("still behind hung job")), 500)),
  ]);
  assert.equal(result, "next");
  await assert.rejects(hanging, /aborted/);
  assert.equal(q.gpuTickets.length, 0);
  assert.equal(q.gpuBusy, false);
});

test("only one vLLM call runs at a time", async () => {
  const q = loadQueue();
  q.resetGpuQueueForTests();
  let active = 0;
  let max = 0;
  const job = () => q.enqueueVllm(async () => {
    active++;
    max = Math.max(max, active);
    await new Promise((r) => setTimeout(r, 20));
    active--;
    return "ok";
  }, agent("greywebsite", []));
  const all = await Promise.all([job(), job(), job()]);
  assert.deepEqual(all, ["ok", "ok", "ok"]);
  assert.equal(max, 1);
  assert.equal(q.gpuTickets.length, 0);
  assert.equal(q.gpuBusy, false);
});
