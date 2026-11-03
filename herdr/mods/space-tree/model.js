export const tabIconColor = (status, active) => active ? "foreground" : status === "working" || status === "blocked" ? status : "muted";

// Pure tree projection. Git ordering/linked parent information comes from spaces.
export const machineId = name => `machine:${name ?? ""}`;
export const rowId = (kind, id) => `${kind}:${id}`;
export const aliasKey = (kind, id) => `space-tree.alias.${kind}.${id}`;

export function orchIndexes(orch = {}) {
  const workers = {}, heads = {}, tabs = {}, workspaces = new Set();
  for (const run of orch.runs ?? []) {
    if (run.workspace) workspaces.add(run.workspace);
    if (run.head_terminal) heads[run.head_terminal] = run;
    for (const w of run.workers ?? []) if (w.terminal) workers[w.terminal] = w;
    if (run.tab) {
      const prev = tabs[run.tab] ?? { done: 0, total: 0, mode: run.mode ?? "" };
      prev.total += (run.workers ?? []).length;
      prev.done += (run.workers ?? []).filter(w => w.status === "done").length;
      tabs[run.tab] = prev;
    }
  }
  return { workers, heads, tabs, workspaces,
    terminals: Object.fromEntries((orch.terminals ?? []).map(t => [t.address, t])) };
}

const programName = pane => pane.program?.name || pane.app || pane.mod || "Terminal";
export function paneName(pane, indexInTab = 0, orchIndex = {}, aliases = {}) {
  const alias = aliases[aliasKey("pane", pane.terminal)];
  if (alias) return alias;
  const worker = orchIndex.workers?.[pane.terminal];
  if (worker) return [worker.label, worker.name].filter(Boolean).join(" · ").toLowerCase();
  // Every agent session that is not an orch worker is a Head, with or without an active run.
  if (orchIndex.heads?.[pane.terminal] || orchIndex.terminals?.[pane.terminal]) return "head";
  return programName(pane).toLowerCase();
}

export function paneSub(pane, orchIndex = {}, indexInTab = 0) {
  const worker = orchIndex.workers?.[pane.terminal];
  const info = worker?.model ? worker : orchIndex.terminals?.[pane.terminal];
  const model = info?.model ? [info.model, info.effort].filter(Boolean).join("-").replace(/\s+/g, "-") : "";
  return [model || (indexInTab === 0 ? "main" : `sub ${indexInTab}`), pane.status || "idle"].join(" · ").toLowerCase();
}

// Remote ids keep the complete machine prefix (machine names may contain '/').
const idOf = value => typeof value === "string" ? value : value?.id;
const machinePrefix = value => {
  const id = idOf(value) ?? "";
  return id.slice(0, Math.max(0, id.lastIndexOf("/")));
};
export function canMoveTab({ tab, panes = [], orchIndex = {}, fromSpace, toSpace, sourceOnly = false }) {
  const id = idOf(tab), from = idOf(fromSpace), to = idOf(toSpace);
  const refuse = reason => ({ ok: false, reason });
  if (!id || !from || !to) return refuse("The tab or space is no longer available.");
  if (orchIndex.tabs?.[id]) return refuse("Tabs hosting an active orch run cannot be moved.");
  const ps = panes.filter(p => p.tab === id);
  if (ps.some(p => orchIndex.workers?.[p.terminal])) return refuse("Tabs containing orch worker terminals cannot be moved.");
  if (ps.some(p => !p.terminal)) return refuse("Tabs containing mod panes cannot be moved.");
  if (!ps.length) return refuse("The tab has no terminal panes to move.");
  if (sourceOnly) return { ok: true };
  if (from === to) return refuse("The tab is already in this space.");
  if (machinePrefix(from) !== machinePrefix(to)) return refuse("Tabs cannot be moved between machines.");
  return { ok: true };
}
export function moveTabPlan(tab, panes, toSpaceId) {
  const terminals = panes.filter(p => p.tab === idOf(tab)).map(p => p.terminal);
  return [
    ...terminals.map(terminal => ({ command: "terminal.park", args: { terminal } })),
    { command: "space.select", args: { id: toSpaceId } },
    ...terminals.map((terminal, i) => ({ command: "terminal.keep", args: { terminal, place: i ? "right" : "tab" } })),
  ];
}

// Actions omit the target id so callers can attach their space/tab id or alias key.
export function renameAction(kind, original, value, named = false) {
  const name = value.trim();
  // Original is the prefilled custom name (named), alias, or displayed auto name.
  // An untouched auto name must remain automatic, just like an untouched custom name.
  if (name === original) return null;
  if (kind === "space" || kind === "tab") return { command: `${kind}.rename`, args: { name } };
  return { alias: kind, value: name || null };
}

// true means folded. Explicit stored values always override these defaults.
export function foldDefault(kind, id, activeSpaceId) {
  return kind === "space" && id !== activeSpaceId;
}
export function pruneFolded(folded = {}, liveIds) {
  const live = new Set(liveIds);
  return Object.fromEntries(Object.entries(folded).filter(([id]) => live.has(id)));
}

export function buildTree({ machines = [], spaces = [], panes = [], orch = {}, aliases = {},
  folded = {}, activeSpaceId } = {}) {
  activeSpaceId ??= spaces.map(e => e.w ?? e).find(s => s.active)?.id;
  const index = orchIndexes(orch), result = [];
  // Accept shipped ordered() entries ({w,i,g,tree}) or explicit {parentId}.
  let parent = null;
  const entries = spaces.map((entry, i) => {
    const s = entry.w ?? entry;
    if (!entry.tree) parent = s.id;
    return { ...s, git: entry.g ?? s.git, order: entry.i ?? i,
      parentId: s.parentId ?? (entry.tree ? parent : null), tree: entry.tree };
  });
  const names = [null, ...new Set([...machines.map(m => m.name), ...entries.map(s => s.machine)]
    .filter(name => name != null))].sort((a, b) => a === null ? -1 : b === null ? 1 : a.localeCompare(b));
  const emit = (kind, id, depth, fields) => {
    const key = kind === "machine" ? machineId(id) : rowId(kind, id);
    const row = { kind, id, key, depth, guide: "", foldable: kind !== "pane",
      open: !(folded[key] ?? foldDefault(kind, id, activeSpaceId)), ...fields };
    result.push(row); return row;
  };
  const addSpace = (s, depth, guide = "") => {
    const row = emit("space", s.id, depth, { name: s.name, status: s.status, guide,
      active: s.id === activeSpaceId, data: s });
    const children = entries.filter(c => c.parentId === s.id);
    const tabs = row.open ? s.tabs ?? [] : [];
    tabs.forEach((tab, ti) => {
      const ps = panes.filter(p => p.tab === tab.id), run = index.tabs[tab.id];
      const tr = emit("tab", tab.id, depth + 1, { name: tab.name, data: tab, space: s.id,
        guide: ti === tabs.length - 1 && !children.length ? "└" : "├", count: ps.length,
        status: tab.status, active: !!tab.active && s.id === activeSpaceId,
        badge: run ? `orch ${run.mode} · ${run.done}/${run.total}` : "" });
      if (!tr.open) return;
      ps.forEach((pane, pi) => emit("pane", pane.pane, depth + 2, {
        name: paneName(pane, pi, index, aliases), sub: paneSub(pane, index, pi),
        orch: !!(index.workers[pane.terminal] || index.heads[pane.terminal]),
        data: pane, terminal: pane.terminal, status: pane.status, active: !!pane.focused && s.id === activeSpaceId,
        guide: pi === ps.length - 1 ? "└" : "├" }));
    });
    children.forEach((child, i) => addSpace(child, depth + 1, i === children.length - 1 ? "└" : "├"));
  };
  names.forEach(name => {
    const data = machines.find(m => (m.name ?? null) === name) ?? { name, layout: "ready" };
    const row = emit("machine", name, 0, { name: aliases[aliasKey("machine", name ?? "local")] || name || "This computer", data });
    if (row.open) entries.filter(s => (s.machine ?? null) === name && !s.parentId).forEach(s => addSpace(s, 1));
  });
  return result;
}
