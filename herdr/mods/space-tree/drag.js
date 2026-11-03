import { canMoveTab, moveTabPlan, orchIndexes } from "./model.js";

export function clearDrag(view, cx) {
  view.tabDrag = null; view.tabOver = null;
  view.pressed = null; view.over = null;
  cx.notify();
}
export function beginTabDrag(view, row, cx) {
  if (view.editing || view.movingTab) return;
  clearDrag(view, cx);
  view.tabDrag = { id: row.id, space: row.space };
}
export function hoverTabTarget(view, row, cx) {
  if (!view.tabDrag) return;
  if (view.tabOver !== row.key) { view.tabOver = row.key; cx.notify(); }
}
export function dropTab(view, row, cx, api) {
  const source = view.tabDrag;
  if (!source) return false;
  clearDrag(view, cx);
  // Re-read all data: an orch run may have started since the press.
  const spaces = api.spaces(), from = spaces.find(s => s.id === source.space);
  const tab = from?.tabs.find(t => t.id === source.id);
  const target = spaces.find(s => s.id === (row.kind === "space" ? row.id : row.space));
  const ps = api.panes();
  const check = canMoveTab({ tab, panes: ps, orchIndex: orchIndexes(view.orch), fromSpace: from, toSpace: target });
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
  const target = () => api.spaces().find(s => s.id === targetId);
  const destination = () => {
    const ids = new Set((target()?.tabs ?? []).map(t => t.id));
    return api.panes().find(p => p.terminal === first && ids.has(p.tab))?.tab;
  };
  const fail = message => { api.warn(message); api.done(); };
  const step = () => {
    if (!target()) return fail("The target space disappeared; moved terminals remain running.");
    if (pending) {
      const ps = api.panes(), terminal = pending.args.terminal;
      const ready = pending.command === "terminal.park" ? !ps.some(p => p.terminal === terminal)
        : pending.command === "space.select" ? target().active
        : ps.some(p => p.terminal === terminal && p.tab === (newTabId || destination()));
      if (!ready) {
        if (++polls >= 100) return fail("Tab move timed out. Terminals remain running; check parked terminals before retrying.");
        return api.after(step);
      }
      if (pending.command === "terminal.keep") newTabId ||= destination();
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
      if (!target().active || (newTabId && !target().tabs.some(t => t.id === newTabId && t.active))) {
        return fail("Selection changed during the move. Remaining terminals are parked and still running.");
      }
    }
    pending = next; polls = 0;
    try { api.run(next.command, next.args); }
    catch { return fail("Tab move failed. Terminals remain running; check parked terminals before retrying."); }
    api.after(step);
  };
  step();
}
