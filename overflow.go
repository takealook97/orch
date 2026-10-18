package main

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
)

// maxWorkers is a per-run limit, separate from the goal's admission budget.
func maxWorkers() int {
	n, err := strconv.Atoi(envOrch("MAX_WORKERS"))
	if err != nil || n <= 0 {
		return 12
	}
	return n
}

func workerCapacity(r *Run) error {
	if n := len(r.liveWorkers()); n >= maxWorkers() {
		return fmt.Errorf("worker capacity is full (%d/%d workers); queue until a worker closes", n, maxWorkers())
	}
	return nil
}

func bottomPane(l Layout) string {
	panes := slices.Clone(l.Panes)
	sort.Slice(panes, func(i, j int) bool { return panes[i].Rect.Y < panes[j].Rect.Y })
	if len(panes) == 0 {
		return ""
	}
	return panes[len(panes)-1].ID
}

func overflowPanes(r *Run, tab string) []string {
	var ids []string
	for _, w := range r.liveWorkers() {
		if w.Tab == tab {
			ids = append(ids, w.Pane)
		}
	}
	return ids
}

// refreshOverflow marks only confirmed missing overflow panes as lost. A failed
// inventory leaves ownership intact; layout reads can still skip unavailable tabs.
// Call under the run lock and the Head lock.
func refreshOverflow(r *Run) {
	if len(r.OverflowTabs) == 0 {
		return
	}
	panes, err := listPanes("")
	if err != nil {
		r.event("overflow inventory warning: %v", err)
		return
	}
	for _, w := range r.liveWorkers() {
		if !slices.Contains(r.OverflowTabs, w.Tab) {
			continue
		}
		exists := slices.ContainsFunc(panes, func(p PaneInfo) bool { return p.ID == w.Pane })
		if !exists {
			r.event("%s: pane %s missing; marked lost", w.Name, w.Pane)
			w.Status, w.Pane = "lost", ""
		}
	}
}

// Read through a surviving owned pane rather than relying on the oldest worker.
func overflowLayout(owned []string) (Layout, error) {
	var last error
	for _, pane := range owned {
		l, err := getLayout(pane)
		if err == nil {
			return l, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("overflow tab has no live owned panes")
	}
	return Layout{}, last
}

// validateOverflow protects panes added by the user, just as I6 does on the Head tab.
func validateOverflow(l Layout, owned []string) error {
	if len(l.Panes) == 0 {
		return fmt.Errorf("overflow tab has no panes")
	}
	for _, p := range l.Panes {
		if !slices.Contains(owned, p.ID) {
			return fmt.Errorf("I6: overflow pane %s was not created by orch", p.ID)
		}
		if p.Rect.X != l.Area.X || p.Rect.Width != l.Area.Width {
			return fmt.Errorf("overflow tab must contain only vertically stacked rows")
		}
	}
	return nil
}

// placeWorker is called under the Head lock. Never moves an existing pane or takes focus.
func placeWorker(r *Run, headLayout Layout, cwd string, env []string) (pane, tab string, err error) {
	if err = workerCapacity(r); err != nil {
		return
	}
	if headLayout.Zoomed {
		return "", "", fmt.Errorf("Head layout is zoomed; unzoom before spawning")
	}
	if err = checkLayout(headLayout, r.HeadPane); err != nil {
		return
	}
	if len(headLayout.Panes)-1 < rowCap(headLayout) {
		target, direction, ratio := bottomPane(headLayout), "down", 0.0
		if len(headLayout.Panes) == 1 {
			target, direction, ratio = r.HeadPane, "right", headRatio
		} else {
			h, _ := findPane(headLayout, r.HeadPane)
			right := rightColumn(headLayout, h)
			target = right[len(right)-1].ID
		}
		pane, err = splitPane(target, direction, ratio, cwd, env)
		return pane, r.Tab, err
	}
	for _, id := range r.OverflowTabs {
		owned := overflowPanes(r, id)
		if len(owned) == 0 {
			continue
		}
		l, e := overflowLayout(owned)
		if e != nil {
			continue
		}
		if e = validateOverflow(l, owned); e != nil {
			continue
		}
		if len(l.Panes) < rowCap(l) {
			pane, err = splitPane(bottomPane(l), "down", 0, cwd, env)
			return pane, id, err
		}
	}
	// Reserve the sequence before creating anything, including failed creations.
	var label string
	if err = withRun(r.ID, func(current *Run) error {
		current.OverflowSeq = max(current.OverflowSeq, len(current.OverflowTabs)) + 1
		r.OverflowSeq = current.OverflowSeq
		label = fmt.Sprintf("orch-%s-%d", r.ID, current.OverflowSeq)
		return nil
	}); err != nil {
		return
	}
	args := []string{"tab", "create", "--cwd", cwd, "--label", label}
	if r.Workspace != "" {
		args = append(args, "--workspace", r.Workspace)
	}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	var res struct {
		Tab struct {
			ID string `json:"tab_id"`
		} `json:"tab"`
		Pane struct {
			ID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	// tab create leaves focus unchanged unless --focus is explicitly passed.
	if err = herdrJSON(&res, args...); err != nil {
		cleanupCreatedOverflow(label)
		return
	}
	if res.Tab.ID == "" || res.Pane.ID == "" {
		cleanupCreatedOverflow(label)
		return "", "", fmt.Errorf("Herdr tab create response has no tab_id or root pane_id")
	}
	r.OverflowTabs = append(r.OverflowTabs, res.Tab.ID)
	return res.Pane.ID, res.Tab.ID, nil
}

// Recover a successful create whose response could not be decoded. The reserved
// label is unique to this run and is never reused, so only that new tab matches.
func cleanupCreatedOverflow(label string) {
	var res struct {
		Tabs []struct {
			ID    string `json:"tab_id"`
			Label string `json:"label"`
		} `json:"tabs"`
	}
	if err := herdrJSON(&res, "tab", "list"); err != nil {
		return
	}
	for _, tab := range res.Tabs {
		if tab.Label == label {
			_, _ = herdrRun("tab", "close", tab.ID)
		}
	}
}

// cleanupOverflowTabs is called under the Head lock. Retain failed tab IDs for retry.
func cleanupOverflowTabs(r *Run) error {
	if len(r.OverflowTabs) == 0 {
		return nil
	}
	panes, err := listPanes("")
	if err != nil {
		return err
	}
	var first error
	for _, tab := range slices.Clone(r.OverflowTabs) {
		if len(overflowPanes(r, tab)) != 0 {
			continue
		}
		occupied := false
		for _, p := range panes {
			if p.Tab == tab {
				occupied = true
			}
		}
		if occupied {
			if first == nil {
				first = fmt.Errorf("I6: overflow tab %s still contains unowned panes; kept", tab)
			}
			continue
		}
		// Closing the last pane may already have removed the tab. Confirm either outcome.
		_, closeErr := herdrRun("tab", "close", tab)
		var res struct {
			Tabs []struct {
				ID string `json:"tab_id"`
			} `json:"tabs"`
		}
		if err := herdrJSON(&res, "tab", "list"); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		exists := false
		for _, t := range res.Tabs {
			if t.ID == tab {
				exists = true
			}
		}
		if exists {
			if first == nil {
				first = fmt.Errorf("overflow tab %s did not close (%v)", tab, closeErr)
			}
			continue
		}
		r.OverflowTabs = slices.DeleteFunc(r.OverflowTabs, func(id string) bool { return id == tab })
		r.event("overflow tab %s removal confirmed", tab)
	}
	return first
}

// normalizeOwnedLayouts keeps the Head geometry and gives overflow tabs equal full-width rows.
func normalizeOwnedLayouts(head string) error {
	if err := normalizeLayout(head); err != nil {
		return err
	}
	runs, err := allRuns()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.HeadPane != head || r.Status != "active" {
			continue
		}
		if err := withRun(r.ID, func(current *Run) error {
			refreshOverflow(current)
			for _, tab := range current.OverflowTabs {
				owned := overflowPanes(current, tab)
				if len(owned) == 0 {
					continue
				}
				if err := normalizeOverflow(owned); err != nil {
					current.event("overflow layout warning tab=%s: %v", tab, err)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func normalizeOverflow(owned []string) error {
	for round := 0; round < 3; round++ {
		l, err := overflowLayout(owned)
		if err != nil {
			return err
		}
		if err := validateOverflow(l, owned); err != nil {
			return err
		}
		lo, hi := l.Area.Height, 0
		for _, p := range l.Panes {
			lo = min(lo, p.Rect.Height)
			hi = max(hi, p.Rect.Height)
		}
		if hi-lo <= 1 {
			return nil
		}
		if round == 2 {
			break
		}
		// Reuse the vertical-split ratio planner with a virtual Head immediately to the left.
		virtual := LayoutPane{ID: "__orch_virtual_head", Rect: Rect{X: l.Area.X - 1, Y: l.Area.Y, Width: 1, Height: l.Area.Height}}
		l.Panes = append(l.Panes, virtual)
		for _, op := range planResize(l, virtual.ID) {
			if _, err := herdrRun("pane", "resize", "--pane", op.Pane, "--direction", op.Direction, "--amount", fmt.Sprintf("%.4f", op.Amount)); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("could not restore equal rows in overflow tab")
}
