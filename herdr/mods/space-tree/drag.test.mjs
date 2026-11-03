import test from "node:test";
import assert from "node:assert/strict";
import { canMoveTab, moveTabPlan, orchIndexes } from "./model.js";
import { executeMove, beginTabDrag, hoverTabTarget, clearDrag, dropTab } from "./drag.js";

const tab = { id: "tb1", name: "Custom", named: true };
const ps = [{ tab: "other", terminal: "ignore" },
  { tab: "tb1", terminal: "t2" }, { tab: "tb1", terminal: "t1" }];
const input = { tab, panes: ps, fromSpace: "sp1", toSpace: "sp2" };

test("move validation refuses each protected, invalid, or empty source", () => {
  const cases = [
    [{ orchIndex: orchIndexes({ runs: [{ tab: "tb1", workers: [] }] }) }, /active orch run/],
    [{ orchIndex: orchIndexes({ runs: [{ tab: "different", workers: [{ terminal: "t1" }] }] }) }, /worker terminals/],
    [{ panes: [...ps, { tab: "tb1", terminal: null }] }, /mod panes/],
    [{ toSpace: "sp1" }, /already/],
    [{ toSpace: "host/sp2" }, /between machines/],
    [{ fromSpace: "host/sp1", toSpace: "sp2" }, /between machines/],
    [{ fromSpace: "a/host/sp1", toSpace: "b/host/sp2" }, /between machines/],
    [{ panes: [] }, /no terminal/],
    [{ tab: undefined }, /no longer/],
    [{ fromSpace: undefined }, /no longer/],
    [{ toSpace: undefined }, /no longer/],
  ];
  for (const [override, reason] of cases) {
    const result = canMoveTab({ ...input, ...override });
    assert.equal(result.ok, false); assert.match(result.reason, reason);
  }
});

test("ordinary local and same remote machine tabs are movable", () => {
  assert.deepEqual(canMoveTab(input), { ok: true });
  assert.deepEqual(canMoveTab({ ...input, fromSpace: { id: "remote/host/sp1" },
    toSpace: { id: "remote/host/sp2" } }), { ok: true });
  assert.deepEqual(canMoveTab({ ...input, panes: [...ps, { tab: "other", terminal: null }],
    orchIndex: orchIndexes({ runs: [{ tab: "other", workers: [{ terminal: "ignore" }] }] }) }), { ok: true });
});

test("plan parks panes in their original order, selects target, then keeps tab and right", () => {
  assert.deepEqual(moveTabPlan(tab, ps, "sp2"), [
    { command: "terminal.park", args: { terminal: "t2" } },
    { command: "terminal.park", args: { terminal: "t1" } },
    { command: "space.select", args: { id: "sp2" } },
    { command: "terminal.keep", args: { terminal: "t2", place: "tab" } },
    { command: "terminal.keep", args: { terminal: "t1", place: "right" } },
  ]);
  assert.equal(moveTabPlan(tab.id, ps.slice(0, 2), "sp2").at(-1).args.place, "tab");
});

function harness({ named = true, stuck = false, changeSelection = false } = {}) {
  let panes = ps.map(p => ({ ...p })), spaces = [
    { id: "sp1", active: true, tabs: [tab] }, { id: "sp2", active: false, tabs: [] },
  ];
  const calls = [], queue = [], warnings = [];
  let done = 0;
  const api = {
    panes: () => panes, spaces: () => spaces,
    after: fn => queue.push(fn), warn: message => warnings.push(message), done: () => done++,
    run(command, args) {
      calls.push({ command, args });
      if (stuck) return;
      // Delay host effects longer than one polling tick.
      queue.push(() => queue.push(() => {
        if (command === "terminal.park") panes = panes.filter(p => p.terminal !== args.terminal);
        if (command === "space.select") spaces.forEach(s => s.active = s.id === args.id);
        if (command === "terminal.keep") {
          if (args.place === "tab") spaces[1].tabs.push({ id: "new-tab", active: !changeSelection });
          panes.push({ terminal: args.terminal, tab: "new-tab" });
        }
      }));
    },
  };
  return { api, calls, queue, warnings, spaces, get done() { return done; },
    start: () => executeMove({ ...tab, named }, ps, "sp2", api),
    flush() { for (let i = 0; queue.length && i < 1000; i++) queue.shift()(); assert.equal(queue.length, 0); },
  };
}

test("asynchronous move waits for each effect and restores a custom name on the new tab", () => {
  const h = harness(); h.start();
  assert.deepEqual(h.calls, [moveTabPlan(tab, ps, "sp2")[0]]);
  h.flush();
  assert.deepEqual(h.calls, [...moveTabPlan(tab, ps, "sp2"),
    { command: "tab.rename", args: { id: "new-tab", name: "Custom" } }]);
  assert.deepEqual(h.warnings, []); assert.equal(h.done, 1);
});

test("automatic tab names are not renamed after moving", () => {
  const h = harness({ named: false }); h.start(); h.flush();
  assert.deepEqual(h.calls, moveTabPlan(tab, ps, "sp2")); assert.equal(h.done, 1);
});

test("failed park times out without selecting target or keeping terminals", () => {
  const h = harness({ stuck: true }); h.start(); h.flush();
  assert.equal(h.calls.length, 1); assert.match(h.warnings[0], /timed out/); assert.equal(h.done, 1);
});

test("vanished target stops the move and releases the busy state", () => {
  const h = harness(); h.start(); h.spaces.pop(); h.flush();
  assert.equal(h.calls.length, 1); assert.match(h.warnings[0], /disappeared/); assert.equal(h.done, 1);
});

test("a selection change before keep stops without placing terminals elsewhere", () => {
  const h = harness({ changeSelection: true }); h.start(); h.flush();
  assert.equal(h.calls.filter(c => c.command === "terminal.keep").length, 1);
  assert.match(h.warnings[0], /Selection changed/); assert.equal(h.done, 1);
});

test("drag candidates clear on cancel; editing and busy moves cannot start another drag", () => {
  const view = {}, cx = { notify() {} }, row = { id: "tb1", space: "sp1", key: "tab:tb1" };
  beginTabDrag(view, row, cx); assert.deepEqual(view.tabDrag, { id: "tb1", space: "sp1" });
  hoverTabTarget(view, { key: "space:sp2" }, cx); assert.equal(view.tabOver, "space:sp2");
  clearDrag(view, cx); assert.equal(view.tabDrag, null); assert.equal(view.tabOver, null);
  for (const flag of ["editing", "movingTab"]) {
    view[flag] = true; beginTabDrag(view, row, cx); assert.equal(view.tabDrag, null); view[flag] = false;
  }
});

test("drop on another tab resolves its space and rechecks newly protected orch data", () => {
  const view = { tabDrag: { id: "tb1", space: "sp1" }, orch: { runs: [{ tab: "tb1" }] } };
  const warnings = [], cx = { notify() {} };
  const api = { spaces: () => [{ id: "sp1", tabs: [tab] }, { id: "sp2", tabs: [] }],
    panes: () => ps, warn: message => warnings.push(message) };
  assert.equal(dropTab(view, { kind: "tab", id: "other", space: "sp2" }, cx, api), true);
  assert.equal(view.tabDrag, null); assert.match(warnings[0], /active orch run/);
  assert.equal(dropTab(view, { kind: "space", id: "sp2" }, cx, api), false);
});


test("dropping on a target tab moves into its space and clears the busy state", () => {
  const h = harness(), view = { tabDrag: { id: "tb1", space: "sp1" }, orch: {} };
  const cx = { notify() {}, timer: { after: (_ms, fn) => h.queue.push(() => fn(cx)) } };
  assert.equal(dropTab(view, { kind: "tab", id: "target-tab", space: "sp2" }, cx, h.api), true);
  assert.equal(view.movingTab, true); assert.equal(view.tabDrag, null);
  h.flush();
  assert.equal(view.movingTab, false);
  assert.deepEqual(h.calls.at(-1), { command: "tab.rename", args: { id: "new-tab", name: "Custom" } });
});

test("a command exception stops the sequence and clears the busy state", () => {
  const h = harness(); h.api.run = () => { throw new Error("host unavailable"); };
  h.start(); h.flush();
  assert.equal(h.done, 1); assert.match(h.warnings[0], /failed/);
});
