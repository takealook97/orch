import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { aliasKey, renameAction } from "./model.js";

// Exercise the actual retained input lifecycle with host effects recorded in memory.
const source = readFileSync(new URL("./rename.js", import.meta.url), "utf8")
  .replace(/^import .*;$/gm, "").replace(/export function /g, "function ");
function harness(overlay = null, aliases = {}) {
  const calls = [], handlers = {}, values = { ...aliases };
  let value = "";
  const input = {
    on: (event, fn) => { handlers[event] = fn; },
    set_value: text => { value = text; }, value: () => value,
    set_selected_range() {}, focus() {},
  };
  const view = { handle: { focus: () => calls.push(["focus"]) } };
  const api = new Function("InputState", "run", "setShared", "shared", "props", "closeOverlay", "aliasKey", "renameAction",
    `${source}\nreturn { initRename, startRename, finishRename };`)(
    { new: () => input }, (...args) => calls.push(["run", ...args]),
    (key, next) => { calls.push(["setShared", key, next]); values[key] = next; },
    key => values[key], () => ({ overlay }), id => calls.push(["close", id]), aliasKey, renameAction);
  const cx = { notify() {} };
  api.initRename(view);
  return { api, view, cx, input, handlers, calls, values };
}

test("opening and blurring an automatic name performs no write", () => {
  for (const kind of ["space", "tab", "machine", "pane"]) {
    const h = harness();
    h.api.startRename(h.view, kind, "id", "Auto name", h.cx);
    h.handlers.blur({}, h.cx);
    assert.deepEqual(h.calls, [["focus"]]);
    assert.equal(h.view.editing, null);
  }
});

test("popup commits alias, registers it for pruning, and closes exactly once", () => {
  for (const kind of ["machine", "pane"]) {
    const h = harness("popup");
    h.api.startRename(h.view, kind, "id", "Shown", h.cx);
    h.input.set_value(" New name ");
    h.handlers.submit({}, h.cx);
    h.handlers.blur({}, h.cx);
    assert.deepEqual(h.calls, [
      ["setShared", aliasKey(kind, "id"), "New name"],
      ["setShared", `space-tree.alias.${kind}.keys`, ["id"]],
      ["close", "popup"],
    ]);
  }
});

test("popup cancellation closes without saving edited text", () => {
  const h = harness("popup");
  h.api.startRename(h.view, "pane", "id", "Head", h.cx);
  h.input.set_value("discard");
  h.api.finishRename(h.view, h.cx, true);
  h.handlers.blur({}, h.cx);
  assert.deepEqual(h.calls, [["close", "popup"]]);
});

test("existing alias is prefilled and clearing it also removes its registry entry", () => {
  const h = harness("popup", { [aliasKey("pane", "id")]: "Custom", "space-tree.alias.pane.keys": ["id", "other"] });
  h.api.startRename(h.view, "pane", "id", "Head", h.cx);
  assert.equal(h.input.value(), "Custom");
  h.input.set_value("");
  h.handlers.submit({}, h.cx);
  assert.equal(h.values[aliasKey("pane", "id")], null);
  assert.deepEqual(h.values["space-tree.alias.pane.keys"], ["other"]);
});
