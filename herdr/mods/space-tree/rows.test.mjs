import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dropTab } from "./drag.js";
import { rowDisplay, machineConnected, tabIconColor } from "./model.js";

const rows = readFileSync(new URL("./rows.js", import.meta.url), "utf8");

test("compact rows use one-line pane details and aligned indentation", () => {
  assert.match(rows, /\.h\(row\.kind === "space" && !row\.data\.parentId \? 26 : 24\)/);
  assert.match(rows, /\.pl\(2 \+ row\.depth \* 12\)/);
  assert.match(rows, /\.left\(2 \+ row\.depth \* 12\)/);
  assert.match(rows, /\.gap\(row\.kind === "pane" \? 6 : 5\)/);
  assert.match(rows, /h_flex\(\)\.flex_none\(\)\.min_w\(0\)\.max_w_3_5\(\)\.text_size\(12\)\.text_color\(t\.foreground\)\.child\(name\)/);
  assert.match(rows, /div\(\)\.flex_1\(\)\.min_w\(0\)\.truncate\(\)\.text_size\(11\)\.text_color\(t\.dim\)\.child\(row\.sub\)/);
  assert.doesNotMatch(rows, /v_flex/);
});

// Render the actual row handlers with a minimal retained-element host.
function element() {
  const node = { handlers: {} };
  const proxy = new Proxy(node, { get(target, key) {
    if (key in target) return target[key];
    return (...args) => {
      if (key === "when" && args[0]) args[1](proxy);
      if (key === "on_click") target.handlers.click = args[0];
      if (key === "on_mouse_up") target.handlers.mouseUp = args[1];
      return proxy;
    };
  } });
  return proxy;
}

const treeRow = new Function("div", "svg", "image", "h_flex", "run", "shared", "nameElement",
  "tabIconColor", "machineConnected", "rowDisplay",
  `${rows.replace(/^import .*;$/gm, "").replace("export function treeRow", "function treeRow")}\nreturn treeRow;`)(
  element, element, element, element, () => {}, () => ({}), () => "Name",
  tabIconColor, machineConnected, rowDisplay);

test("drop mouse-up on a space or tab row does not also select that row", () => {
  for (const kind of ["space", "tab"]) {
    const calls = [], tab = { id: "a", index: 1 }, sibling = { id: "b", index: 2 };
    const spaces = [{ id: "source", active: true, tabs: [tab, sibling] }, { id: "target", tabs: [] }];
    const api = { spaces: () => spaces, panes: () => [{ tab: "a", terminal: "t" }],
      run: (command, args) => calls.push({ command, args }), warn: assert.fail };
    const row = kind === "space"
      ? { kind, id: "target", key: "space:target", name: "Target", data: { order: 1 } }
      : { kind, id: "b", key: "tab:b", space: "source", data: sibling };
    const view = { orchLoaded: true, tabDrag: { id: "a", space: "source" },
      select: row => api.run(`${row.kind}.select`, { id: row.id }),
      dropTab: (row, cx) => dropTab(view, row, cx, api),
      dropSpace: (row, cx) => dropTab(view, row, cx, api) };
    const cx = { notify() {}, timer: { after() {} } };
    const el = treeRow(view, row, {});
    el.handlers.mouseUp({}, cx);
    // Even after the operation clears its busy flag, the generated click is suppressed.
    view.movingTab = false;
    el.handlers.click({}, cx);
    assert.deepEqual(calls, kind === "space"
      ? [{ command: "terminal.park", args: { terminal: "t" } }]
      : [{ command: "tab.move", args: { id: "a", index: 2 } }]);
    view.suppressClickUntil = Date.now() - 1;
    el.handlers.click({}, cx);
    assert.deepEqual(calls.at(-1), { command: `${kind}.select`, args: { id: row.id } });
  }
});
