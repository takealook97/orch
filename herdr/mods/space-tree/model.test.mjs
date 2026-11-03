import test from "node:test";
import assert from "node:assert/strict";
import { buildTree, paneName, paneSub, foldDefault, pruneFolded, orchIndexes, aliasKey, machineId } from "./model.js";

const pane = (id, tab = "tab", extra = {}) => ({ pane: id, terminal: `t-${id}`, tab, program: { name: "Codex" }, status: "working", ...extra });
const sp = (id, tabs = [], extra = {}) => ({ id, name: id, tabs, ...extra });
const tab = id => ({ id, name: id });
const orch = { runs: [{ workspace: "repo", tab: "tab", mode: "full", head_terminal: "head",
  workers: [{ terminal: "worker", name: "impl", label: "구현", model: "gpt-6-luna", effort: "high", status: "done" },
    { terminal: "review", name: "review", label: "리뷰", status: "prompted" }] }],
  terminals: [{ address: "argv", model: "gpt-6.1-sol", effort: "low" }] };
const index = orchIndexes(orch);

test("pane naming precedence: alias, worker, Head, main, sub and fallback", () => {
  const w = pane("p", "tab", { terminal: "worker" });
  assert.equal(paneName(w, 0, index, { [aliasKey("pane", "worker")]: "내 이름" }), "내 이름");
  assert.equal(paneName(w, 0, index), "구현 · impl");
  assert.equal(paneName(pane("p", "tab", { terminal: "head" }), 0, index), "Head");
  assert.equal(paneName(pane("p"), 0, index), "main · Codex");
  assert.equal(paneName(pane("p"), 1, index), "sub 1 · Codex");
  assert.equal(paneName(pane("p"), 4, index), "sub 4 · Codex");
  assert.equal(paneName({ terminal: null, mod: "files" }), "main · files");
  assert.equal(paneName({}), "main · Terminal");
  assert.equal(paneName(w, 0, index, { [aliasKey("pane", "worker")]: "" }), "구현 · impl");
});

test("line two uses worker model, argv model, then program plus live status", () => {
  assert.equal(paneSub(pane("p", "tab", { terminal: "worker" }), index), "gpt-6-luna high · working");
  assert.equal(paneSub(pane("p", "tab", { terminal: "argv", status: "blocked" }), index), "gpt-6.1-sol low · blocked");
  assert.equal(paneSub(pane("p", "tab", { terminal: "head" }), index), "Codex · working");
  assert.equal(paneSub({ status: null }), "Terminal · idle");
});

test("machines local first, remote alphabetic; preserve shipped space and tab/pane order", () => {
  const rows = buildTree({ machines: [{ name: "z/host" }, { name: "a" }], spaces: [
    sp("b", [tab("tb"), tab("ta")]), sp("a"), sp("z/host/s", [], { machine: "z/host" })],
    panes: [pane("2", "tb"), pane("1", "tb")], activeSpaceId: "b" });
  assert.deepEqual(rows.filter(r => r.kind === "machine").map(r => r.id), [null, "a", "z/host"]);
  assert.deepEqual(rows.filter(r => r.kind === "space").map(r => r.id), ["b", "a", "z/host/s"]);
  assert.deepEqual(rows.filter(r => r.kind === "tab").map(r => r.id), ["tb", "ta"]);
  assert.deepEqual(rows.filter(r => r.kind === "pane").map(r => r.id), ["2", "1"]);
  assert.deepEqual(rows.filter(r => r.kind === "pane").map(r => r.depth), [3, 3]);
});

test("shipped ordered worktrees nest under repo; folded parent hides its subtree", () => {
  const spaces = [{ w: sp("repo", [tab("tab")]), i: 2, g: { name: "repo" } },
    { w: sp("wt", [tab("wt-tab")]), i: 0, tree: "└─" }, { w: sp("other"), i: 1 }];
  const input = { spaces, activeSpaceId: "repo", folded: { "space:wt": false }, panes: [pane("p", "wt-tab")] };
  const rows = buildTree(input);
  assert.deepEqual(rows.map(r => r.id), [null, "repo", "tab", "wt", "wt-tab", "p", "other"]);
  const child = rows.find(r => r.id === "wt");
  assert.equal(child.depth, 2); assert.equal(child.data.parentId, "repo"); assert.equal(child.guide, "└");
  assert.equal(rows.find(r => r.id === "repo").data.order, 2);
  assert.deepEqual(buildTree({ ...input, folded: { "space:repo": true } }).map(r => r.id), [null, "repo", "other"]);
});

test("defaults open machines, active space and tabs; explicit fold values win", () => {
  assert.equal(foldDefault("machine", "local", "s"), false);
  assert.equal(foldDefault("space", "s", "s"), false);
  assert.equal(foldDefault("space", "other", "s"), true);
  assert.equal(foldDefault("tab", "t", "s"), false);
  const input = { spaces: [sp("s", [tab("t")]), sp("other", [tab("o")])], activeSpaceId: "s" };
  assert.deepEqual(buildTree(input).filter(r => r.kind === "tab").map(r => r.id), ["t"]);
  assert.deepEqual(buildTree({ ...input, folded: { "space:s": true, "space:other": false } }).filter(r => r.kind === "tab").map(r => r.id), ["o"]);
  assert.equal(buildTree({ ...input, folded: { [machineId(null)]: true } }).length, 1);
});

test("prune removes dead keys and preserves explicit open/closed states without mutation", () => {
  const folded = { "space:gone": true, "tab:live": false, "machine:r/host": true };
  assert.deepEqual(pruneFolded(folded, ["tab:live", "machine:r/host"]), { "tab:live": false, "machine:r/host": true });
  assert.equal(Object.keys(folded).length, 3);
});

test("orch space and tab badges count done/total and Head retains pane focus", () => {
  const rows = buildTree({ spaces: [sp("repo", [tab("tab")])], activeSpaceId: "repo", orch,
    panes: [pane("head-pane", "tab", { terminal: "head", focused: true })] });
  assert.equal(rows.find(r => r.kind === "space").badge, "orch");
  assert.equal(rows.find(r => r.kind === "tab").badge, "orch full · 1/2");
  const head = rows.find(r => r.kind === "pane");
  assert.equal(head.name, "Head"); assert.equal(head.active, true);
  assert.equal(head.guide, "└");
  assert.equal(orchIndexes({ runs: [{ tab: "zero", workers: [] }] }).tabs.zero.total, 0);
});

test("remote identifiers containing slashes match whole addresses and aliases", () => {
  const address = "remote/host/t1", machine = "remote/host";
  const rows = buildTree({ machines: [{ name: machine }], activeSpaceId: "remote/host/sp", spaces: [
    sp("remote/host/sp", [tab("remote/host/tb")], { machine })], panes: [pane("remote/host/p", "remote/host/tb", { terminal: address })],
    aliases: { [aliasKey("machine", machine)]: "서버", [aliasKey("pane", address)]: "원격 작업" },
    orch: { runs: [{ head_terminal: address }], terminals: [{ address, model: "remote-model" }] } });
  assert.equal(rows.find(r => r.kind === "machine" && r.id === machine).name, "서버");
  const row = rows.find(r => r.kind === "pane");
  assert.equal(row.name, "원격 작업"); assert.equal(row.id, "remote/host/p");
  assert.equal(row.sub, "remote-model · working");
});

test("local computer fold key cannot collide with a remote named local", () => {
  assert.notEqual(machineId(null), machineId("local"));
  const rows = buildTree({ machines: [{ name: "local" }], spaces: [sp("remote", [], { machine: "local" })],
    folded: { [machineId(null)]: true } });
  assert.equal(rows.find(r => r.id === "remote").kind, "space");
});
