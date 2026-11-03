import { div, svg, image } from "gpui-kit";
import { h_flex } from "gpui-base";
import { run, shared } from "herdr";
import { nameElement } from "./rename.js";
import { tabIconColor, machineConnected, rowDisplay } from "./model.js";

const asset = name => `mods/space-tree/${name}.svg`;
const tone = (t, status) => t[status] ?? t.dim;
const dot = (row, t) => div().size(6).rounded(3).flex_none().bg(tone(t, row.status));
const glyph = (name, t) => svg(asset(name)).size(13).flex_none().text_color(t.dim);

function icon(row, t) {
  if (row.kind === "machine") return glyph(row.id ? "server" : "monitor", t);
  if (row.kind === "space") return row.data.parentId ? glyph("branch", t) : null;
  if (row.kind === "tab") return svg(asset("tab")).size(13).flex_none().text_color(t[tabIconColor(row.status, row.active)]);
  const program = row.data.program;
  if (program?.icon) return program.tint ? svg(program.icon).size(14).flex_none().text_color(t.muted) : image(program.icon).size(14).flex_none();
  return div().w(14).text_color(t.dim).child("›_");
}
export function treeRow(view, row, t) {
  const stop = (_e, cx) => cx.stop_propagation();
  const name = nameElement(view, row, t);
  const main = h_flex().w_full().min_w(0).items_center().gap(row.kind === "pane" ? 6 : 5)
    .child(div().w(row.kind === "space" && !row.data.parentId ? 3 : 9).h(17).self_start().mt(-4).flex_none()
      .when(Boolean(row.guide), el => el.border_l(1).border_b(1).rounded_bl(6).border_color(t.border)))
    .child(row.foldable ? div().id(`fold-${row.key}`).size(12).flex_none()
      .on_mouse_down("left", stop).on_mouse_up("left", stop)
      .on_click((_e, cx) => { cx.stop_propagation(); view.toggle(row, cx); })
      .child(svg(asset(row.open ? "chevron-down" : "chevron-right")).size(12).text_color(t.dim)) : div().w(12).flex_none())
    .when(row.kind !== "space" || !!row.data.parentId, el => el.child(icon(row, t)))
    .child(row.kind === "pane" ? h_flex().flex_none().min_w(0).max_w_3_5().text_size(12).text_color(t.foreground).child(name) : row.kind === "space" ? div().flex_1().min_w(0).text_size(12.5).font_semibold().text_color(t.foreground).child(name) : name)
    .when(row.kind === "pane", el => el.child(div().flex_1().min_w(0).truncate().text_size(11).text_color(t.dim).child(row.sub)));
  const display = rowDisplay(row);
  if (display.count) main.child(div().text_size(11).text_color(t.dim).child(display.count));
  if (row.kind === "machine") {
    const machine = view.machines.find(m => m.name === row.id) ?? view.reconnecting[row.id]?.machine;
    const repair = view.reconnecting[row.id];
    const rebuilt = view.rebuilt(machine) || !!repair;
    const connected = machineConnected(row, machine);
    if (rebuilt) main.child(div().id(`connect-${row.key}`).text_size(11).text_color(t.working)
      .tooltip(repair?.error || "Connect to rebuilt machine")
      .on_mouse_down("left", stop).on_click((_e, cx) => { cx.stop_propagation(); view.reconnect(machine, cx); })
      .child(repair?.busy ? "Connecting…" : "Connect"));
    else if (!connected) main.child(div().text_size(11).text_color(t.dim).child((machine?.state ?? row.data.layout ?? "asleep").replace(/_/g, " ")));
    if (rowDisplay(row, connected, view.hoveredMachine === row.key).add) main.child(div().id(`add-${row.key}`).px(3).text_color(t.dim).hover(s => s.text_color(t.foreground))
      .on_mouse_down("left", stop).on_click((_e, cx) => { cx.stop_propagation(); run("space.new", { machine: row.id ?? "" }); }).child("+"));
    if (!connected) main.child(div().size(6).flex_none().rounded(3).bg(t.dim));
  } else if (display.dot) main.child(dot(row, t));
  const el = h_flex().id(row.key).relative().h(row.kind === "space" && !row.data.parentId ? 26 : 24)
    .flex_none().pl(2 + row.depth * 12).pr(6).items_center().rounded(5).text_size(12)
    .text_color(row.kind === "machine" ? t.muted : row.orch && view.orchStale ? t.dim : t.foreground)
    .bg(row.active ? row.kind === "pane" ? t.raised : t.selected : row.key === view.highlight ? t.raised : t.chrome)
    .hover(s => s.bg(t.hover))
    .on_click((_e, cx) => { if (Date.now() >= (view.suppressClickUntil ?? 0)) view.select(row, cx); })
    .on_mouse_down("right", (_e, cx) => { cx.stop_propagation(); view.contextMenu(row); })
    .when(row.sectionColorKey != null, e => e.child(div().absolute().left(2 + row.sectionDepth * 12).top(0).bottom(0).w(2)
      .bg(t[(shared("space.colors") ?? {})[row.sectionColorKey]] ?? t.accent)))
    .when(row.kind === "pane" && row.active, e => e.child(div().absolute().left(0).top(0).bottom(0).w(2).bg(t.accent)))
    .when(row.guide === "├", e => e.child(div().absolute().top(0).bottom(0)
      .left(2 + row.depth * 12).border_l(1).border_color(t.border)))
    .child(main)
    .when(view.tabDrag && view.tabOver === row.key, e => e.child(div().absolute().inset_0()
      .rounded(5).border_1().border_color(t.accent)));
  if (row.kind === "machine") el.on_hover((hovered, cx) => {
    const next = hovered ? row.key : view.hoveredMachine === row.key ? null : view.hoveredMachine;
    if (next !== view.hoveredMachine) { view.hoveredMachine = next; cx.notify(); }
  });
  if (row.kind === "tab") {
    el.when(view.tabDrag?.id === row.id && view.tabOver && view.tabOver !== row.key, e => e.opacity(0.5))
      .on_mouse_down("left", (_e, cx) => view.beginTabDrag(row, cx))
      .on_mouse_move((_e, cx) => view.hoverTabTarget(row, cx))
      .on_mouse_up("left", (_e, cx) => { if (view.tabDrag?.id !== row.id) view.dropTab(row, cx); });
  }
  if (row.kind === "tab" || row.kind === "space") {
    el.on_hover((hovered, cx) => {
      if (!hovered && view.tabOver === row.key) { view.tabOver = null; cx.notify(); }
    });
  }
  if (row.kind === "space") {
    const i = row.data.order;
    el.on_mouse_down("left", () => { if (!view.movingTab) { view.tabDrag = null; view.pressed = i; view.over = i; } })
      .on_mouse_move((_e, cx) => view.dragOver(row, cx))
      .on_mouse_up("left", (_e, cx) => view.dropSpace(row, cx))
      .when(view.pressed === i && view.over !== i, e => e.opacity(0.5))
      .when(view.pressed !== null && view.over === i && view.pressed !== i, e => e.child(div().absolute().left(0).right(0).h(2).bg(t.accent)
        .when(view.pressed < i, line => line.bottom(0)).when(view.pressed > i, line => line.top(0))));
  }
  return el;
}
