import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import vm from "node:vm";

const src = fs.readFileSync(new URL("./index.js", import.meta.url), "utf8");
const start = src.indexOf("// --- gateway-tool-approval-test-start ---");
const end = src.indexOf("// --- gateway-tool-approval-test-end ---");
if (start < 0 || end < start) throw new Error("approval test region missing");

function load() {
  const ctx = vm.createContext({});
  vm.runInContext(src.slice(start, end), ctx);
  return ctx;
}

const cwd = "/workspace/.claude/agents/paddleboarding";

test("write inside the agent folder is approved", () => {
  const a = load();
  const d = a.approveGatewayTool("Write", {
    file_path: "lib/adapters/gfs_wave.js",
    content: "x",
  }, cwd);
  assert.equal(d.behavior, "allow");
});

test("absolute write under .claude/agents is approved", () => {
  const a = load();
  const d = a.approveGatewayTool("Edit", {
    file_path: "/workspace/.claude/agents/paddleboarding/lib/normalize.js",
    old_string: "a",
    new_string: "b",
  }, "/workspace");
  assert.equal(d.behavior, "allow");
});

test("touch via bash in the agent folder is approved", () => {
  const a = load();
  const d = a.approveGatewayTool("Bash", {
    command: "touch lib/adapters/gfs_wave.js",
  }, cwd);
  assert.equal(d.behavior, "allow");
});

test("settings.json under .claude stays blocked", () => {
  const a = load();
  const d = a.approveGatewayTool("Write", {
    file_path: "/workspace/.claude/settings.json",
    content: "{}",
  }, "/workspace");
  assert.equal(d.behavior, "deny");
  const local = a.approveGatewayTool("Bash", {
    command: "echo x > /workspace/.claude/settings.local.json",
  }, "/workspace");
  assert.equal(local.behavior, "deny");
});

test("root claude config stays blocked", () => {
  const a = load();
  const d = a.approveGatewayTool("Write", {
    file_path: "/root/.claude/settings.json",
    content: "{}",
  }, cwd);
  assert.equal(d.behavior, "deny");
});
