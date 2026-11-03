import test from "node:test";
import assert from "node:assert/strict";
import { canMoveTab, moveTabPlan, orchIndexes } from "./model.js";
import { executeMove, beginTabDrag, hoverTabTarget, clearDrag, dropTab } from "./drag.js";

const tab = { id: "tb1", name: "Custom", named: true, index: 1 };
const sibling = { id: "other", name: "Other", index: 2 };
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

function harness({ named = true, stuck = false, inactiveTab = false, third = false,
  fault = () => false } = {}) {
  const original = third ? [...ps, { tab: "tb1", terminal: "t3" }] : ps;
  let panes = original.map(p => ({ ...p })), spaces = [
    { id: "sp1", name: "Source", active: true, tabs: [tab, sibling] },
    { id: "sp2", name: "Target", active: false, tabs: [] },
  ];
  const calls = [], queue = [], warnings = [];
  let done = 0, nextTab = 0;
  const api = {
    panes: () => panes, spaces: () => spaces,
    after: fn => queue.push(fn), warn: message => warnings.push(message), done: () => done++,
    run(command, args) {
      calls.push({ command, args });
      if (fault(command, args, calls)) throw new Error("host unavailable");
      if (stuck && command === "terminal.park") return;
      // Delay host effects longer than one polling tick.
      queue.push(() => queue.push(() => {
        if (command === "terminal.park") panes = panes.filter(p => p.terminal !== args.terminal);
        if (command === "space.select") spaces.forEach(s => s.active = s.id === args.id);
        if (command === "terminal.keep") {
          const selected = spaces.find(s => s.active);
          if (args.place === "tab") selected.tabs.push({ id: ++nextTab === 1 ? "new-tab" : `restored-${nextTab}`, active: !inactiveTab });
          panes = panes.filter(p => p.terminal !== args.terminal);
          panes.push({ terminal: args.terminal, tab: selected.tabs.at(-1).id });
        }
      }));
    },
  };
  return { api, calls, queue, warnings, spaces, get done() { return done; },
    start: () => executeMove({ ...tab, named }, original, "sp2", api),
    flush() { for (let i = 0; queue.length && i < 5000; i++) queue.shift()(); assert.equal(queue.length, 0); },
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

test("a park timeout with the terminal still visible never creates a recovery tab", () => {
  const h = harness({ stuck: true }); h.start();
  for (let i = 0; i < 199; i++) h.queue.shift()();
  assert.equal(h.calls.length, 1); // 200 x 50 ms = ten seconds per step.
  h.flush();
  assert.deepEqual(h.calls, [moveTabPlan(tab, ps, "sp2")[0]]);
  assert.match(h.warnings[0], /timed out/); assert.equal(h.done, 1);
});

test("vanished target restores parked terminals in the source space", () => {
  const h = harness(); h.start(); h.spaces.pop(); h.flush();
  assert.equal(h.calls.filter(c => c.command === "terminal.keep").length, 1);
  assert.match(h.warnings[0], /restored in Source/); assert.equal(h.done, 1);
});

test("a new tab need not be marked active for a multi-pane move to succeed", () => {
  const h = harness({ inactiveTab: true }); h.start(); h.flush();
  assert.deepEqual(h.calls, [...moveTabPlan(tab, ps, "sp2"),
    { command: "tab.rename", args: { id: "new-tab", name: "Custom" } }]);
  assert.deepEqual(h.warnings, []); assert.equal(h.done, 1);
});

test("failure after one of three parks restores every parked terminal", () => {
  const h = harness({ third: true, fault: (cmd, args) => cmd === "terminal.park" && args.terminal === "t1" });
  h.start(); h.flush();
  assert.deepEqual(h.calls.filter(c => c.command === "terminal.keep").map(c => c.args),
    [{ terminal: "t2", place: "tab" }]);
  assert.match(h.warnings[0], /1 terminal\(s\) restored in Source/);
  assert.equal(h.api.panes().filter(p => p.terminal !== "ignore").length, 3);
});

test("a failed second keep restores all remaining parked terminals", () => {
  let failed = false;
  const h = harness({ third: true, fault: (cmd, args) => {
    if (!failed && cmd === "terminal.keep" && args.terminal === "t1") { failed = true; return true; }
  } });
  h.start(); h.flush();
  assert.deepEqual(h.calls.filter(c => c.command === "terminal.keep").slice(2).map(c => c.args),
    [{ terminal: "t1", place: "tab" }, { terminal: "t3", place: "right" }]);
  assert.match(h.warnings[0], /2 terminal\(s\) restored in Source/); assert.equal(h.done, 1);
  assert.equal(h.api.panes().filter(p => p.terminal !== "ignore").length, 3);
});

test("selection of a third space after parking restores into the source with its custom name", () => {
  const h = harness(); h.start();
  while (!h.calls.some(c => c.command === "space.select")) h.queue.shift()();
  h.spaces.push({ id: "sp3", name: "Third", active: true, tabs: [] });
  h.spaces.forEach(s => s.active = s.id === "sp3");
  h.flush();
  const selects = h.calls.filter(c => c.command === "space.select");
  assert.deepEqual(selects.at(-1).args, { id: "sp1" });
  assert.equal(h.spaces[2].tabs.length, 0);
  assert.ok(h.spaces[0].tabs.some(t => t.id === "new-tab"));
  assert.ok(h.calls.some(c => c.command === "tab.rename" && c.args.id === "new-tab" && c.args.name === "Custom"));
  assert.match(h.warnings[0], /2 terminal\(s\) restored in Source/); assert.equal(h.done, 1);
});

test("drag candidates clear on cancel; editing and busy moves cannot start another drag", () => {
  const h = harness(), view = { orchLoaded: true }, cx = { notify() {} };
  const row = { id: "tb1", space: "sp1", key: "tab:tb1" };
  beginTabDrag(view, row, cx, h.api); assert.deepEqual(view.tabDrag, { id: "tb1", space: "sp1" });
  hoverTabTarget(view, { kind: "space", id: "sp2", key: "space:sp2" }, cx, h.api);
  assert.equal(view.tabOver, "space:sp2");
  clearDrag(view, cx); assert.equal(view.tabDrag, null); assert.equal(view.tabOver, null);
  for (const flag of ["editing", "movingTab"]) {
    view[flag] = true; beginTabDrag(view, row, cx, h.api); assert.equal(view.tabDrag, null); view[flag] = false;
  }
});

test("drop on another tab resolves its space and rechecks newly protected orch data", () => {
  const view = { orchLoaded: true, tabDrag: { id: "tb1", space: "sp1" }, orch: { runs: [{ tab: "tb1" }] } };
  const warnings = [], cx = { notify() {} };
  const api = { spaces: () => [{ id: "sp1", tabs: [tab, sibling] }, { id: "sp2", tabs: [] }],
    panes: () => ps, warn: message => warnings.push(message) };
  assert.equal(dropTab(view, { kind: "tab", id: "other", space: "sp2" }, cx, api), true);
  assert.equal(view.tabDrag, null); assert.match(warnings[0], /active orch run/);
  assert.equal(dropTab(view, { kind: "space", id: "sp2" }, cx, api), false);
});


test("dropping on a target tab moves into its space and clears the busy state", () => {
  const h = harness(), view = { orchLoaded: true, tabDrag: { id: "tb1", space: "sp1" }, orch: {} };
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

test("drops fail closed before the first orch poll and with stale orch data", () => {
  for (const flags of [{}, { polling: true }, { orchLoaded: true, orchStale: true }]) {
    const h = harness(), view = { ...flags, orch: {}, tabDrag: { id: "tb1", space: "sp1" } };
    dropTab(view, { kind: "space", id: "sp2" }, { notify() {} }, h.api);
    assert.deepEqual(h.calls, []);
    assert.deepEqual(h.warnings, ["orch status not loaded yet; try again in a moment"]);
  }
});

test("protected sources refuse drag start without a ghost or target highlight", () => {
  const h = harness(), cx = { notify() {} };
  const view = { orchLoaded: true, orch: { runs: [{ tab: "tb1" }] } };
  beginTabDrag(view, { id: "tb1", space: "sp1" }, cx, h.api);
  assert.equal(view.tabDrag, null); assert.equal(view.tabOver, null);
  assert.match(h.warnings[0], /active orch run/);
});

test("only valid destinations highlight, and source movability is rechecked on hover", () => {
  const h = harness(), cx = { notify() {} };
  h.spaces.push({ id: "remote/sp3", tabs: [] });
  const view = { orchLoaded: true, orch: {}, tabDrag: { id: "tb1", space: "sp1" } };
  const valid = { kind: "space", id: "sp2", key: "space:sp2" };
  for (const row of [{ kind: "tab", id: "tb1", space: "sp1" },
    { kind: "space", id: "sp1" }, { kind: "tab", space: "sp1", id: "missing" },
    { kind: "space", id: "remote/sp3" }]) {
    hoverTabTarget(view, valid, cx, h.api); assert.equal(view.tabOver, valid.key);
    hoverTabTarget(view, { ...row, key: "invalid" }, cx, h.api); assert.equal(view.tabOver, null);
  }
  view.orch = { runs: [{ tab: "tb1" }] };
  hoverTabTarget(view, valid, cx, h.api); assert.equal(view.tabOver, null);
});

test("selecting the target while parking is allowed and preserves the custom name", () => {
  const h = harness({ third: true }); h.start();
  h.queue.shift()();
  h.spaces.forEach(s => s.active = s.id === "sp2");
  h.flush();
  assert.equal(h.calls.filter(c => c.command === "terminal.park").length, 3);
  assert.deepEqual(h.warnings, []);
  assert.deepEqual(h.calls.at(-1), { command: "tab.rename", args: { id: "new-tab", name: "Custom" } });
  assert.equal(h.done, 1);
});

test("recovery continues past a failed keep and retries until no terminal is parked", () => {
  let failures = 0;
  const h = harness({ third: true, fault: (cmd, args) => {
    if (cmd === "space.select" && args.id === "sp2") return true;
    if (cmd === "terminal.keep" && args.terminal === "t2" && failures++ === 0) return true;
  } });
  h.start(); h.flush();
  assert.deepEqual(h.calls.filter(c => c.command === "terminal.keep").map(c => c.args.terminal),
    ["t2", "t2", "t1", "t3"]);
  assert.equal(h.api.panes().filter(p => p.terminal !== "ignore").length, 3);
  assert.match(h.warnings[0], /3 terminal\(s\) restored in Source/); assert.equal(h.done, 1);
});

test("moving the only tab is refused before parking can close its source space", () => {
  assert.deepEqual(canMoveTab({ ...input, fromSpace: { id: "sp1", tabs: [tab] } }), {
    ok: false, reason: "This is the only tab in its space; moving it would close the space.",
  });
  const h = harness(), view = { orchLoaded: true }, cx = { notify() {} };
  h.spaces[0].tabs = [tab];
  beginTabDrag(view, { id: tab.id, space: "sp1" }, cx, h.api);
  assert.equal(view.tabDrag, null); assert.deepEqual(h.calls, []);
});

test("same-space tab drop highlights and reorders by the current 1-based index without parking", () => {
  const h = harness(), view = { orchLoaded: true, tabDrag: { id: tab.id, space: "sp1" } };
  const row = { kind: "tab", id: sibling.id, space: "sp1", key: "tab:other" };
  const cx = { notify() {} };
  hoverTabTarget(view, row, cx, h.api); assert.equal(view.tabOver, row.key);
  dropTab(view, row, cx, h.api);
  assert.deepEqual(h.calls, [{ command: "tab.move", args: { id: "tb1", index: 2 } }]);
  assert.equal(view.tabDrag, null);
});

test("dropping a tab on itself is a no-op", () => {
  const h = harness(), view = { orchLoaded: true, tabDrag: { id: tab.id, space: "sp1" } };
  dropTab(view, { kind: "tab", id: tab.id, space: "sp1" }, { notify() {} }, h.api);
  assert.deepEqual(h.calls, []); assert.deepEqual(h.warnings, []);
  assert.equal(view.tabDrag, null);
});

test("recovery observes a keep that applies before its dispatch throws", () => {
  const h = harness({ fault: (cmd, args) => cmd === "space.select" && args.id === "sp2" });
  const run = h.api.run;
  let thrown = false;
  h.api.run = (command, args) => {
    run(command, args);
    if (command === "terminal.keep" && !thrown) { thrown = true; throw new Error("late dispatch error"); }
  };
  h.start(); h.flush();
  assert.equal(h.calls.filter(c => c.command === "terminal.keep").length, 2);
  assert.match(h.warnings[0], /2 terminal\(s\) restored in Source/);
  assert.equal(h.done, 1);
});
