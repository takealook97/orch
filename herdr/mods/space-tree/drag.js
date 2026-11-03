import { canMoveTab, moveTabPlan, orchIndexes } from "./model.js";

export function clearDrag(view, cx) {
  view.tabDrag = null; view.tabOver = null;
  view.pressed = null; view.over = null;
  cx.notify();
}
const orchPending = view => view.orchStale || !view.orchLoaded;
const orchWarning = "orch status not loaded yet; try again in a moment";
function validate(view, source, row, api) {
  if (orchPending(view)) return { ok: false, reason: orchWarning };
  const spaces = api.spaces(), from = spaces.find(s => s.id === source.space);
  return canMoveTab({ tab: from?.tabs.find(t => t.id === source.id), panes: api.panes(),
    orchIndex: orchIndexes(view.orch), fromSpace: from,
    toSpace: row ? spaces.find(s => s.id === (row.kind === "space" ? row.id : row.space)) : from,
    sourceOnly: !row });
}
export function beginTabDrag(view, row, cx, api) {
  if (view.editing || view.movingTab) return;
  clearDrag(view, cx);
  const check = validate(view, row, null, api);
  if (!check.ok) { api.warn(check.reason); return; }
  view.tabDrag = { id: row.id, space: row.space };
}
export function hoverTabTarget(view, row, cx, api) {
  if (!view.tabDrag) return;
  const key = validate(view, view.tabDrag, row, api).ok ? row.key : null;
  if (view.tabOver !== key) { view.tabOver = key; cx.notify(); }
}
export function dropTab(view, row, cx, api) {
  const source = view.tabDrag;
  if (!source) return false;
  clearDrag(view, cx);
  // Re-read the layout and validate the latest completed orch poll.
  const spaces = api.spaces(), from = spaces.find(s => s.id === source.space);
  const tab = from?.tabs.find(t => t.id === source.id);
  const target = spaces.find(s => s.id === (row.kind === "space" ? row.id : row.space));
  const ps = api.panes();
  const check = validate(view, source, row, api);
  if (!check.ok) api.warn(check.reason);
  else {
    view.movingTab = true;
    executeMove(tab, ps, target.id, {
      ...api,
      after: fn => cx.timer.after(50, next => { cx = next; fn(); }),
      done: () => { view.movingTab = false; cx.notify(); },
    });
  }
  return true;
}

// park/keep are asynchronous. Observe the layout before proceeding, so a slow
// remote core cannot keep a terminal before its park finishes or split the wrong tab.
export function executeMove(tab, originalPanes, targetId, api) {
  const plan = moveTabPlan(tab, originalPanes, targetId);
  const first = originalPanes.find(p => p.tab === tab.id)?.terminal;
  let at = 0, polls = 0, pending = null, newTabId = null;
  const parked = new Set(), kept = new Set();
  const initialSpace = api.spaces().find(s => s.active)?.id;
  let selectedTarget = false;
  const target = () => api.spaces().find(s => s.id === targetId);
  const destination = () => {
    const ids = new Set((target()?.tabs ?? []).map(t => t.id));
    return api.panes().find(p => p.terminal === first && ids.has(p.tab))?.tab;
  };
  const fail = message => {
    // Include a command that applied before throwing or before its next poll.
    if (pending?.command === "terminal.park" &&
        !api.panes().some(p => p.terminal === pending.args.terminal)) parked.add(pending.args.terminal);
    if (pending?.command === "terminal.keep" &&
        api.panes().some(p => p.terminal === pending.args.terminal)) kept.add(pending.args.terminal);
    const restore = [...parked].filter(t => !kept.has(t));
    if (!restore.length) { api.warn(message); api.done(); return; }
    const selected = api.spaces().find(s => s.active);
    let waiting = restore, ticks = 0;
    const keep = terminals => terminals.forEach(terminal => {
      try { api.run("terminal.keep", { terminal, place: terminal === restore[0] ? "tab" : "right" }); }
      catch { /* Retry without abandoning the other parked terminals. */ }
    });
    keep(waiting);
    const recover = () => {
      waiting = waiting.filter(t => !api.panes().some(p => p.terminal === t));
      if (!waiting.length) {
        api.warn(`Move stopped; ${restore.length} terminal(s) restored in ${selected?.name ?? selected?.id ?? "selected space"}`);
        api.done(); return;
      }
      if (++ticks >= 200) { ticks = 0; keep(waiting); }
      api.after(recover);
    };
    api.after(recover);
  };
  const step = () => {
    if (!target()) return fail("The target space disappeared; moved terminals remain running.");
    const selected = api.spaces().find(s => s.active)?.id;
    if ((selectedTarget && selected !== targetId) ||
        (!selectedTarget && pending?.command !== "space.select" && selected !== initialSpace)) {
      return fail("Selection changed during the move.");
    }
    if (pending) {
      const ps = api.panes(), terminal = pending.args.terminal;
      const ready = pending.command === "terminal.park" ? !ps.some(p => p.terminal === terminal)
        : pending.command === "space.select" ? target().active
        : ps.some(p => p.terminal === terminal && p.tab === (newTabId || destination()));
      if (!ready) {
        if (++polls >= 200) return fail("Tab move timed out. Terminals remain running; check parked terminals before retrying.");
        return api.after(step);
      }
      if (pending.command === "space.select") selectedTarget = true;
      if (pending.command === "terminal.keep") { kept.add(terminal); newTabId ||= destination(); }
      pending = null;
    }
    if (at === plan.length) {
      if (tab.named && newTabId) {
        try { api.run("tab.rename", { id: newTabId, name: tab.name }); }
        catch { return fail("The tab moved, but its custom name could not be restored."); }
      }
      api.done(); return;
    }
    const next = plan[at++];
    if (next.command === "terminal.keep") {
      // The user can change selection during a move: stop before keeping into
      // another space/tab rather than silently moving terminals elsewhere.
      if (!target().active || (newTabId && destination() !== newTabId)) {
        return fail("Selection changed during the move. Remaining terminals are parked and still running.");
      }
    }
    pending = next; polls = 0;
    try {
      api.run(next.command, next.args);
      if (next.command === "terminal.park") parked.add(next.args.terminal);
    }
    catch { return fail("Tab move failed. Terminals remain running; check parked terminals before retrying."); }
    api.after(step);
  };
  step();
}
