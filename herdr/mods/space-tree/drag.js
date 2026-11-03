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
  if (row?.kind === "tab" && row.space === source.space && row.id !== source.id &&
      !from?.tabs.some(t => t.id === row.id && Number.isInteger(t.index) && t.index > 0)) {
    return { ok: false, reason: "The tab or space is no longer available." };
  }
  return canMoveTab({ tab: from?.tabs.find(t => t.id === source.id), panes: api.panes(),
    orchIndex: orchIndexes(view.orch), fromSpace: from,
    toSpace: row ? spaces.find(s => s.id === (row.kind === "space" ? row.id : row.space)) : from,
    sourceOnly: !row || (row.kind === "tab" && row.space === source.space && row.id !== source.id) });
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
  if (row.kind === "tab" && row.id === source.id) { clearDrag(view, cx); return true; }
  view.suppressClickUntil = Date.now() + 300;
  clearDrag(view, cx);
  // Re-read the layout and validate the latest completed orch poll.
  const spaces = api.spaces(), from = spaces.find(s => s.id === source.space);
  const tab = from?.tabs.find(t => t.id === source.id);
  const target = spaces.find(s => s.id === (row.kind === "space" ? row.id : row.space));
  const ps = api.panes();
  const check = validate(view, source, row, api);
  if (!check.ok) api.warn(check.reason);
  else if (row.kind === "tab" && target.id === from.id) {
    const targetTab = from.tabs.find(t => t.id === row.id);
    if (!targetTab) api.warn("The tab or space is no longer available.");
    else {
      try { api.run("tab.move", { id: tab.id, index: targetTab.index }); }
      catch { api.warn("Tab reorder failed."); }
    }
  } else {
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
  const source = api.spaces().find(s => s.tabs.some(t => t.id === tab.id));
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
    let index = 0, ticks = 0, issued = false, selecting = false;
    const sourceNow = () => api.spaces().find(s => s.id === source?.id);
    const restoredPane = terminal => {
      const ids = new Set((sourceNow()?.tabs ?? []).map(t => t.id));
      return api.panes().find(p => p.terminal === terminal && ids.has(p.tab));
    };
    const selectSource = () => {
      try { api.run("space.select", { id: source.id }); } catch { /* Observe selection even if dispatch throws. */ }
      selecting = true;
    };
    const recover = () => {
      if (!sourceNow()) {
        api.warn("Move stopped; the source space disappeared. Check parked terminals."); api.done(); return;
      }
      if (!selecting) selectSource();
      if (!sourceNow().active) {
        if (++ticks >= 200) {
          api.warn("Move stopped; could not select the source space. Check parked terminals."); api.done(); return;
        }
        return api.after(recover);
      }
      const terminal = restore[index], pane = issued && restoredPane(terminal);
      if (pane) {
        if (index === 0) {
          if (tab.named) {
            try { api.run("tab.rename", { id: pane.tab, name: tab.name }); }
            catch {
              api.warn("Move stopped; the restored tab's custom name could not be restored.");
            }
          }
        }
        index++; issued = false; ticks = 0;
      }
      if (index === restore.length) {
        api.warn(`Move stopped; ${restore.length} terminal(s) restored in ${source.name ?? source.id}`);
        api.done(); return;
      }
      if (!issued || ++ticks >= 200) {
        ticks = 0;
        issued = true;
        try {
          api.run("terminal.keep", { terminal: restore[index], place: index ? "right" : "tab" });
        } catch { /* Retry this terminal before splitting the next one. */ }
      }
      api.after(recover);
    };
    recover();
  };
  const step = () => {
    // Finish observing an in-flight park before recovery; a timed-out park
    // that never removed its pane needs no keep (which would create a stray tab).
    if (pending?.command === "terminal.park") {
      if (api.panes().some(p => p.terminal === pending.args.terminal)) {
        if (++polls >= 200) return fail("Tab move timed out. Terminals remain running; check parked terminals before retrying.");
        return api.after(step);
      }
      parked.add(pending.args.terminal); pending = null;
    }
    if (!target()) return fail("The target space disappeared; moved terminals remain running.");
    const selected = api.spaces().find(s => s.active)?.id;
    if (selected !== initialSpace && selected !== targetId) {
      return fail("Selection changed during the move.");
    }
    if (pending) {
      const ps = api.panes(), terminal = pending.args.terminal;
      const ready = pending.command === "space.select" ? target().active
        : ps.some(p => p.terminal === terminal && p.tab === (newTabId || destination()));
      if (!ready) {
        if (++polls >= 200) return fail("Tab move timed out. Terminals remain running; check parked terminals before retrying.");
        return api.after(step);
      }
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
    }
    catch { return fail("Tab move failed. Terminals remain running; check parked terminals before retrying."); }
    api.after(step);
  };
  step();
}
