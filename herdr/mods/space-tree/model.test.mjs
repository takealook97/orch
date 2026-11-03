import test from "node:test";
import assert from "node:assert/strict";
import { tabIconColor, buildTree, paneName, paneSub, foldDefault, pruneFolded, orchIndexes, aliasKey, machineId, renameAction } from "./model.js";

const pane = (id, tab = "tab", extra = {}) => ({ pane: id, terminal: `t-${id}`, tab, program: { name: "Codex" }, status: "working", ...extra });
const sp = (id, tabs = [], extra = {}) => ({ id, name: id, tabs, ...extra });
const tab = id => ({ id, name: id });
const orch = { runs: [{ workspace: "repo", tab: "tab", mode: "full", head_terminal: "head",
  workers: [{ terminal: "worker", name: "impl", label: "Impl", model: "gpt-6-luna", effort: "high", status: "done" },
    { terminal: "review", name: "review", label: "Review", status: "prompted" }] }],
  terminals: [{ address: "argv", model: "gpt-6.1-sol", effort: "low" }] };
const index = orchIndexes(orch);

test("unchanged rename is a no-op for automatic/custom names and aliases", () => {
  for (const kind of ["space", "tab", "machine", "pane"]) {
    for (const named of [false, true]) {
      assert.equal(renameAction(kind, "Shown name", "  Shown name  ", named), null);
      assert.equal(renameAction(kind, "", "  ", named), null);
    }
  }
});

test("rename restores automatic names, clears aliases, or applies trimmed text", () => {
  for (const kind of ["space", "tab"]) {
    for (const named of [false, true]) {
      assert.deepEqual(renameAction(kind, "Shown", "  ", named), { command: `${kind}.rename`, args: { name: "" } });
      assert.deepEqual(renameAction(kind, "Shown", " New ", named), { command: `${kind}.rename`, args: { name: "New" } });
    }
  }
  for (const kind of ["machine", "pane"]) {
    assert.deepEqual(renameAction(kind, "Shown", "", true), { alias: kind, value: null });
    assert.deepEqual(renameAction(kind, "Shown", " New ", false), { alias: kind, value: "New" });
  }
});

test("inactive folded repo still lists child worktree and active worktree tabs", () => {
  const spaces = [sp("repo", [tab("repo-tab")]), sp("wt", [tab("wt-tab")], { parentId: "repo", active: true })];
  const rows = buildTree({ spaces });
  assert.deepEqual(rows.map(r => r.id), [null, "repo", "wt", "wt-tab"]);
  assert.equal(rows.find(r => r.id === "repo").open, false);
  assert.equal(rows.find(r => r.id === "wt").open, true);
  assert.equal(rows.find(r => r.id === "wt").active, true);
  assert.deepEqual(buildTree({ spaces, activeSpaceId: "elsewhere" }).map(r => r.id), [null, "repo", "wt"]);
});

test("only shown space's active tab and focused pane are highlighted", () => {
  const rows = buildTree({ spaces: [sp("shown", [{ ...tab("t1"), active: true }]), sp("other", [{ ...tab("t2"), active: true }])],
    panes: [pane("p1", "t1", { focused: true }), pane("p2", "t2", { focused: true })],
    activeSpaceId: "shown", folded: { "space:other": false } });
  assert.deepEqual(rows.filter(r => r.active).map(r => r.id), ["shown", "t1", "p1"]);
});

test("pane subtitle uses position when model unknown and argv when worker model empty", () => {
  assert.equal(paneSub(pane("p"), index, 2), "sub 2 · working");
  assert.equal(paneSub(pane("p", "tab", { terminal: "argv" }), {
    ...index, workers: { argv: { model: "", effort: "high" } }
  }, 2), "gpt-6.1-sol-low · working");
  assert.equal(paneSub(pane("p", "tab", { terminal: "argv" }), {
    terminals: { argv: { model: "", effort: "high" } }
  }, 2), "sub 2 · working");
  const rows = buildTree({ spaces: [sp("s", [tab("tab")])], activeSpaceId: "s", panes: [pane("a"), pane("b")] });
  assert.deepEqual(rows.filter(r => r.kind === "pane").map(r => [r.name, r.sub]), [
    ["codex", "main · working"], ["codex", "sub 1 · working"]]);
});

test("pane naming precedence: alias, worker, Head, main, sub and fallback", () => {
  const w = pane("p", "tab", { terminal: "worker" });
  assert.equal(paneName(w, 0, index, { [aliasKey("pane", "worker")]: "내 이름" }), "내 이름");
  assert.equal(paneName(w, 0, index), "impl · impl");
  assert.equal(paneName(pane("p", "tab", { terminal: "head" }), 0, index), "head");
  assert.equal(paneName(pane("p"), 0, index), "codex");
  assert.equal(paneName(pane("p"), 1, index), "codex");
  assert.equal(paneName(pane("p"), 4, index), "codex");
  assert.equal(paneName({ terminal: null, mod: "files" }), "files");
  assert.equal(paneName({}), "terminal");
  assert.equal(paneName(w, 0, index, { [aliasKey("pane", "worker")]: "" }), "impl · impl");
});

test("line two uses worker model, argv model, then position plus live status", () => {
  assert.equal(paneSub(pane("p", "tab", { terminal: "worker" }), index), "gpt-6-luna-high · working");
  assert.equal(paneSub(pane("p", "tab", { terminal: "argv", status: "blocked" }), index), "gpt-6.1-sol-low · blocked");
  assert.equal(paneSub(pane("p", "tab", { terminal: "head" }), index), "main · working");
  assert.equal(paneSub({ status: null }), "main · idle");
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

test("shipped ordered worktrees nest under repo; folded parent hides only its tabs", () => {
  const spaces = [{ w: sp("repo", [tab("tab")]), i: 2, g: { name: "repo" } },
    { w: sp("wt", [tab("wt-tab")]), i: 0, tree: "└─" }, { w: sp("other"), i: 1 }];
  const input = { spaces, activeSpaceId: "repo", folded: { "space:wt": false }, panes: [pane("p", "wt-tab")] };
  const rows = buildTree(input);
  assert.deepEqual(rows.map(r => r.id), [null, "repo", "tab", "wt", "wt-tab", "p", "other"]);
  const child = rows.find(r => r.id === "wt");
  assert.equal(child.depth, 2); assert.equal(child.data.parentId, "repo"); assert.equal(child.guide, "└");
  assert.equal(rows.find(r => r.id === "repo").data.order, 2);
  assert.deepEqual(buildTree({ ...input, folded: { "space:repo": true } }).map(r => r.id), [null, "repo", "wt", "other"]);
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

test("tab badge counts done/total without a duplicate space badge; Head retains focus", () => {
  const rows = buildTree({ spaces: [sp("repo", [tab("tab")])], activeSpaceId: "repo", orch,
    panes: [pane("head-pane", "tab", { terminal: "head", focused: true })] });
  assert.equal(rows.find(r => r.kind === "space").badge, undefined);
  assert.equal(rows.find(r => r.kind === "tab").badge, "orch full · 1/2");
  const head = rows.find(r => r.kind === "pane");
  assert.equal(head.name, "head"); assert.equal(head.active, true);
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

test("tab icon color follows active state and actionable status", () => {
  for (const status of ["working", "blocked", "idle", undefined]) {
    assert.equal(tabIconColor(status, true), "foreground");
    assert.equal(tabIconColor(status, false), ["working", "blocked"].includes(status) ? status : "muted");
  }
});
