import { div } from "gpui-kit";
import { InputState, Input } from "gpui-base";
import { run, setShared, shared } from "herdr";
import { aliasKey } from "./model.js";

// One retained input belongs to the View, never to a render pass.
export function initRename(view) {
  view.input = InputState.new({ value: "" });
  view.input.on("change", (_e, cx) => cx.notify());
  view.input.on("submit", (_e, cx) => finishRename(view, cx));
  view.input.on("blur", (_e, cx) => finishRename(view, cx));
}
export function startRename(view, kind, id, current, cx) {
  if (view.editing) finishRename(view, cx);
  view.editing = { kind, id };
  view.input.set_value(current);
  view.input.set_selected_range({ start: 0, end: current.length });
  view.input.focus();
  cx.notify();
}
export function finishRename(view, cx, cancel = false) {
  const edit = view.editing;
  if (!edit) return;
  view.editing = null; // Clear first: focusing the tree may deliver blur.
  if (!cancel) {
    const name = view.input.value().trim();
    if (edit.kind === "space" || edit.kind === "tab") run(`${edit.kind}.rename`, { id: edit.id, name });
    else {
      setShared(aliasKey(edit.kind, edit.id), name || null);
      if (edit.kind === "pane") {
        const known = shared("space-tree.alias.pane.keys") ?? [];
        setShared("space-tree.alias.pane.keys", [...new Set([...known, edit.id])]);
      }
    }
  }
  view.handle.focus();
  cx.notify();
}
export function renameId(row) {
  return row.kind === "pane" ? row.terminal : row.kind === "machine" ? row.id ?? "local" : row.id;
}
export function nameElement(view, row, t) {
  const id = renameId(row);
  if (view.editing?.kind === row.kind && view.editing?.id === id) {
    return Input.new(view.input).flex_1().min_w(0).h(24).px(3).text_size(12.5)
      .bg(t.background).text_color(t.foreground).border_1().border_color(t.accent)
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
      .on_key_down((e, cx) => {
        if (e.key === "escape") { finishRename(view, cx, true); cx.stop_propagation(); }
      });
  }
  return div().id(`name-${row.key}`).flex_1().min_w(0).truncate().child(row.name)
    .on_mouse_down("left", (e, cx) => {
      if (e.click_count === 2 && id != null) {
        cx.stop_propagation(); view.highlight = row.key;
        startRename(view, row.kind, id, row.name, cx);
      }
    });
}
