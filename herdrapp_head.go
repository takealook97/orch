package main

import (
	"fmt"
	"slices"
)

type appLocation struct{ space, tab, pane string }

// appTerminalLocations maps every terminal and pane id in the inventory to where it lives now.
func appTerminalLocations(ls appInventory) map[string]appLocation {
	out := map[string]appLocation{}
	for _, s := range ls.Spaces {
		for _, t := range s.Tabs {
			for _, p := range t.Terminals {
				loc := appLocation{s.ID, t.ID, p.Pane}
				out[p.Terminal] = loc
				if p.Pane != "" {
					out[p.Pane] = loc
				}
			}
		}
	}
	return out
}

func appTabIDs(ls appInventory) map[string]bool {
	out := map[string]bool{}
	for _, s := range ls.Spaces {
		for _, t := range s.Tabs {
			out[t.ID] = true
		}
	}
	return out
}

// applyAppLocations syncs one run with the live inventory and reports whether anything changed.
// A worker whose terminal is absent is left alone for the lost/close logic.
func applyAppLocations(r *Run, ls appInventory, locs map[string]appLocation, headLoc appLocation) bool {
	changed := false
	if r.Workspace != headLoc.space || r.Tab != headLoc.tab {
		r.event("app Head moved: space %s/tab %s -> space %s/tab %s", r.Workspace, r.Tab, headLoc.space, headLoc.tab)
		r.Workspace, r.Tab = headLoc.space, headLoc.tab
		changed = true
	}
	remapped := map[string]string{}
	var workerTabs []string
	for _, w := range r.liveWorkers() {
		if loc, ok := locs[w.Terminal]; ok && w.Terminal != "" {
			if w.Tab != loc.tab {
				if w.Tab != "" {
					remapped[w.Tab] = loc.tab
				}
				w.Tab, changed = loc.tab, true
			}
			if loc.pane != "" && w.Pane != loc.pane {
				w.Pane, changed = loc.pane, true
			}
		}
		if w.Tab != "" && w.Tab != r.Tab && !slices.Contains(workerTabs, w.Tab) {
			workerTabs = append(workerTabs, w.Tab)
		}
	}
	// Overflow tabs are those holding live workers; a still-existing tab without workers
	// stays listed so cleanup can close it.
	existing := appTabIDs(ls)
	var overflow []string
	for _, id := range r.OverflowTabs {
		if id != r.Tab && (slices.Contains(workerTabs, id) || existing[id]) && !slices.Contains(overflow, id) {
			overflow = append(overflow, id)
		}
	}
	for _, id := range workerTabs {
		if !slices.Contains(overflow, id) {
			overflow = append(overflow, id)
		}
	}
	if !slices.Equal(overflow, r.OverflowTabs) && (len(overflow) > 0 || len(r.OverflowTabs) > 0) {
		r.OverflowTabs, changed = overflow, true
	}
	for old, now := range remapped {
		name, ok := r.OverflowNames[old]
		if !ok {
			continue
		}
		if _, has := r.OverflowNames[now]; !has {
			r.OverflowNames[now] = name
		}
		delete(r.OverflowNames, old)
		changed = true
	}
	return changed
}

// refreshAppLocations re-reads where the Head and its workers live. Herdr.app gives a space
// and tab new ids when the Head terminal is dragged elsewhere, which leaves run.json pointing at
// ids that no longer exist; an unknown id would make Herdr create a new space or tab.
func refreshAppLocations(head string) error {
	ls, err := appList()
	if err != nil {
		return fmt.Errorf("app Head location: %w", err)
	}
	locs := appTerminalLocations(ls)
	headLoc, ok := locs[head]
	if !ok || headLoc.tab == "" {
		return fmt.Errorf("app Head terminal %s not found in herdr-app", head)
	}
	setAppHeadLocation(head, headLoc.space, headLoc.tab)
	runs, err := allRuns()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.HeadPane != head || runBackend(r) != "app" || (r.Status != "active" && len(r.liveWorkers()) == 0) {
			continue
		}
		if !applyAppLocations(r, ls, locs, appLocation{space: headLoc.space, tab: headLoc.tab}) {
			continue
		}
		if err = withRun(r.ID, func(current *Run) error {
			applyAppLocations(current, ls, locs, appLocation{space: headLoc.space, tab: headLoc.tab})
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
