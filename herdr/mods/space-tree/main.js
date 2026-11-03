const ORCH = "/Users/deepeet/.local/bin/orch";
import { div, svg } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { spaces, machines, panes, terminals, run, exec, shared, setShared, theme, openOverlay, nextCommand, props } from "herdr";
import Plumbing from "./plumbing.js";
import { buildTree, aliasKey, machineId, rowId, pruneFolded } from "./model.js";
import { treeRow } from "./rows.js";
import { initRename, startRename, finishRename, renameId } from "./rename.js";
import { menuView } from "./tree-menu.js";

export default class SpaceTree extends Plumbing {
  init(p, cx) {
    this.handle = cx.focus_handle();
    initRename(this);
    if (props()?.treeMenu) { this.handle.focus(); return; }
    super.init(p, cx);
    try { this.treeFolded = JSON.parse(localStorage.getItem("tree.folded") ?? "{}"); } catch { this.treeFolded = {}; }
    this.orch = {};
    this.orchStale = false;
    this.highlight = null;
    this.refreshOrch(cx);
    cx.timer.every(3000, cx => this.refreshOrch(cx));
    cx.timer.every(60000, cx => this.prune(cx));
    cx.spawn(async cx => {
      for (;;) {
        if (await nextCommand() === "focus") this.handle.focus();
        cx.notify();
      }
    });
  }

  refreshOrch(cx) {
    if (this.polling) return;
    this.polling = true;
    cx.spawn(async cx => {
      try {
        const r = await exec(ORCH, ["hud", "panes"]);
        if (r.code !== 0) throw new Error("hud unavailable");
        const data = JSON.parse(r.stdout);
        if (!Array.isArray(data.runs) || !Array.isArray(data.terminals)) throw new Error("bad hud data");
        this.orch = data; this.orchStale = false;
      } catch { this.orchStale = true; }
      this.polling = false;
      cx.notify();
    });
  }

  inputs() {
    const sp = this.ordered(), ps = panes(), aliases = {};
    const ms = [...machines()];
    for (const m of this.machines) if (!ms.some(k => k.name === m.name)) ms.push({ ...m, layout: "unavailable" });
    for (const m of [null, ...ms.map(m => m.name)]) {
      const key = aliasKey("machine", m ?? "local"); aliases[key] = shared(key);
    }
    for (const p of ps) if (p.terminal) {
      const key = aliasKey("pane", p.terminal); aliases[key] = shared(key);
    }
    return { machines: ms, spaces: sp, panes: ps, orch: this.orch, aliases,
      activeSpaceId: spaces().find(s => s.active)?.id };
  }

  liveKeys(input) {
    return [machineId(null), ...input.machines.map(m => machineId(m.name)),
      ...input.spaces.flatMap(e => [rowId("space", e.w.id), ...(e.w.tabs ?? []).map(t => rowId("tab", t.id))]),
      ...input.panes.map(p => rowId("pane", p.pane))];
  }
  saveFold(cx) {
    localStorage.setItem("tree.folded", JSON.stringify(this.treeFolded));
    cx.notify();
  }
  toggle(row, cx, open = !row.open) {
    if (!row.foldable) return;
    this.treeFolded[row.key] = !open;
    this.highlight = row.key;
    this.handle.focus(); this.saveFold(cx);
  }
  foldAll(closed, cx) {
    for (const key of this.liveKeys(this.inputs())) if (!key.startsWith("pane:")) this.treeFolded[key] = closed;
    this.handle.focus(); this.saveFold(cx);
  }

  prune(cx) {
    const input = this.inputs();
    // Offline machines may return their spaces; only prune when layouts are ready.
    if (input.machines.every(m => m.layout === "ready")) {
      this.treeFolded = pruneFolded(this.treeFolded, this.liveKeys(input));
      this.saveFold(cx);
    }
    const known = shared("space-tree.alias.pane.keys") ?? [];
    const live = new Set(terminals().map(t => t.address));
    const kept = [];
    for (const address of known) {
      const remote = input.machines.filter(m => m.name && address.startsWith(`${m.name}/`))
        .sort((a, b) => b.name.length - a.name.length)[0];
      const unavailable = remote ? remote.layout !== "ready" : address.includes("/");
      if (live.has(address) || unavailable) kept.push(address);
      else setShared(aliasKey("pane", address), null);
    }
    if (kept.length !== known.length) setShared("space-tree.alias.pane.keys", kept);
    const storedMachines = shared("space-tree.alias.machine.keys") ?? [];
    // Enroll aliases created before the machine-key registry was introduced.
    const machineKeys = [...new Set([...storedMachines, ...input.machines
      .map(m => m.name ?? "local").filter(name => shared(aliasKey("machine", name)))])];
    const liveMachines = new Set(["local", ...machines().map(m => m.name)]);
    const keptMachines = machineKeys.filter(name => {
      if (liveMachines.has(name)) return true;
      setShared(aliasKey("machine", name), null);
      return false;
    });
    if (JSON.stringify(keptMachines) !== JSON.stringify(storedMachines)) setShared("space-tree.alias.machine.keys", keptMachines);
  }

  select(row, cx) {
    if (this.editing) return;
    this.highlight = row.key; this.handle.focus();
    if (row.kind === "space" || row.kind === "tab") run(`${row.kind}.select`, { id: row.id });
    if (row.kind === "pane") run("pane.focus", { pane: row.id });
    cx.notify();
  }
  contextMenu(row) {
    if (row.kind === "space") return this.menu({ id: row.id });
    if (row.kind === "tab") return openOverlay({ mod: "menu", kind: "popup", width: 200,
      props: { tab: "tab", target: { id: row.id, space: row.space } } });
    openOverlay({ mod: "space-tree", kind: "popup", width: 200,
      props: { treeMenu: true, kind: row.kind, id: renameId(row), current: row.name,
        target: { pane: row.id, terminal: row.terminal } } });
  }
  dragOver(row, cx) {
    const source = spaces()[this.pressed];
    const over = source && (source.machine ?? null) === (row.data.machine ?? null) ? row.data.order : null;
    if (this.pressed !== null && this.over !== over) { this.over = over; cx.notify(); }
  }
  dropSpace(row, cx) {
    const from = this.pressed, over = this.over, i = row.data.order;
    this.pressed = null; this.over = null;
    if (from !== null && over === i && from !== i) run("space.move", { id: spaces()[from].id, index: i + 1 });
    cx.notify();
  }
  key(e, cx) {
    if (this.editing) {
      if (e.key === "escape") { finishRename(this, cx, true); cx.stop_propagation(); }
      return;
    }
    const rows = this.visible ?? [], at = Math.max(0, rows.findIndex(r => r.key === this.highlight)), row = rows[at];
    if (!row) return;
    if (e.key === "up" || e.key === "down") {
      this.highlight = rows[Math.max(0, Math.min(rows.length - 1, at + (e.key === "up" ? -1 : 1)))].key;
    } else if (e.key === "left" || e.key === "right") this.toggle(row, cx, e.key === "right");
    else if (e.key === "enter") this.select(row, cx);
    else if (e.key === "f2" && renameId(row) != null) startRename(this, row.kind, renameId(row), row.data?.named ? row.data.name : row.name, cx, row.data?.named);
    else return;
    cx.stop_propagation(); cx.notify();
  }

  render() {
    if (props()?.treeMenu) return menuView(this);
    const t = theme(), input = this.inputs();
    this.visible = buildTree({ ...input, folded: this.treeFolded });
    if (!this.visible.some(r => r.key === this.highlight)) this.highlight = this.visible.find(r => r.active)?.key ?? this.visible[0]?.key;
    const colors = { ...(shared("space.colors") ?? {}) }, palette = ["ansi6", "ansi3", "ansi4", "ansi5", "ansi2", "ansi1"];
    let changed = false;
    for (const s of spaces()) if (!colors[s.id] && s.name !== "~") {
      colors[s.id] = palette.reduce((best, c) => Object.values(colors).filter(v => v === c).length < Object.values(colors).filter(v => v === best).length ? c : best);
      changed = true;
    }
    if (changed) setShared("space.colors", colors);
    const button = (id, icon, label, action) => div().id(id).size(24).flex_none().flex().items_center().justify_center()
      .rounded(4).tooltip(label).hover(s => s.bg(t.hover).text_color(t.foreground))
      .on_click(action).child(svg(`mods/space-tree/${icon}.svg`).size(12));
    return v_flex().id("space-tree").track_focus(this.handle).on_key_down((e, cx) => this.key(e, cx))
      .size_full().min_h(0).bg(t.chrome).text_size(12)
      .child(h_flex().h(30).flex_none().pl(12).pr(8).items_center().text_color(t.muted)
        .child(div().flex_1().child("Spaces"))
        .when(this.orchStale, el => el.child(div().text_size(10).text_color(t.dim).tooltip("orch hud unavailable; retaining last data").child("⚠")))
        .child(button("expand-all", "chevron-down", "Expand all", (_e, cx) => this.foldAll(false, cx)))
        .child(button("collapse-all", "chevron-right", "Collapse all", (_e, cx) => this.foldAll(true, cx)))
        .child(button("new-space", "plus", "New space", () => run("space.new"))))
      .child(v_flex().id("tree-list").flex_1().min_h(0).px(4).pb(10).overflow_y_scroll()
        .on_mouse_up("left", (_e, cx) => { this.pressed = null; cx.notify(); })
        .children(this.visible.map(row => treeRow(this, row, t))));
  }
}
