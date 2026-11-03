import { div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { props, theme, closeOverlay, openOverlay } from "herdr";
import { startRename, nameElement, finishRename } from "./rename.js";

// Alias controls supplement the shipped pane menu without changing that mod.
export function menuView(view) {
  const p = props(), t = theme();
  const row = { kind: p.kind, id: p.kind === "machine" && p.id === "local" ? null : p.id,
    terminal: p.target?.terminal, key: "menu-name", name: p.current };
  const button = (id, label, action) => h_flex().id(id).h(30).px(8).items_center().rounded(5)
    .hover(s => s.bg(t.hover)).on_click(action).child(label);
  return v_flex().id("tree-alias-menu").track_focus(view.handle).p(5).gap(3)
    .bg(t.popover).text_color(t.foreground).text_size(12)
    .on_key_down((e, cx) => {
      if (e.key === "escape") {
        if (view.editing) finishRename(view, cx, true);
        else closeOverlay(p.overlay);
        cx.stop_propagation();
      }
    })
    .child(view.editing ? nameElement(view, row, t) : button("rename-alias", "이름 바꾸기", (_e, cx) => {
      if (p.id != null) startRename(view, p.kind, p.id, p.current, cx);
    }))
    .when(p.kind === "pane", el => el.child(button("pane-actions", "Pane actions…", () => {
      closeOverlay(p.overlay);
      openOverlay({ mod: "menu", kind: "popup", width: 200, props: { tab: "pane", target: p.target } });
    })))
    .when(p.kind === "pane" && p.id == null, el => el.child(div().text_color(t.dim).text_size(11).child("Terminal aliases only")));
}
