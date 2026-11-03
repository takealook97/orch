package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type appDocument struct {
	Value   map[string]any `json:"value"`
	Version int            `json:"version"`
}

func appDocGet() (appDocument, error) {
	var d appDocument
	raw, err := hcRPC("doc.get", map[string]any{"name": "herdr-app"})
	if err == nil {
		err = json.Unmarshal(raw, &d)
	}
	return d, err
}
func docTab(d map[string]any, space, tab string) (map[string]any, error) {
	spaces, _ := d["spaces"].([]any)
	for _, ss := range spaces {
		s, _ := ss.(map[string]any)
		if s["id"] != space {
			continue
		}
		tabs, _ := s["tabs"].([]any)
		for _, tt := range tabs {
			t, _ := tt.(map[string]any)
			if t["id"] == tab {
				return t, nil
			}
		}
	}
	return nil, fmt.Errorf("app tab %s in space %s not found", tab, space)
}
func appLeaves(node any) ([]map[string]any, error) {
	n, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("I6: invalid app tree node")
	}
	typ, _ := n["type"].(string)
	if typ == "block" {
		return []map[string]any{n}, nil
	}
	if typ != "row" && typ != "column" {
		return nil, fmt.Errorf("I6: unknown app node type %q", typ)
	}
	children, ok := n["children"].([]any)
	if !ok {
		return nil, fmt.Errorf("I6: missing app tree children")
	}
	var out []map[string]any
	for _, child := range children {
		leaves, err := appLeaves(child)
		if err != nil {
			return nil, err
		}
		out = append(out, leaves...)
	}
	return out, nil
}
func leafTerminal(n map[string]any) string {
	c, _ := n["content"].(map[string]any)
	if _, mod := c["mod"]; mod {
		return ""
	}
	s, _ := c["terminal"].(string)
	return s
}
func weighted(n map[string]any, w float64) map[string]any {
	out := map[string]any{}
	for k, v := range n {
		out[k] = v
	}
	out["size"] = map[string]any{"weight": w}
	return out
}

// canonicalAppRoot rewrites a tab tree to Head | column of workers. Blocks of known non-live workers
// (closed by this Head; the app removes their panes a moment later) are dropped, not foreign.
func canonicalAppRoot(root any, tab, head string, order []string, known map[string]appIdentity) (map[string]any, error) {
	all, err := appLeaves(root)
	if err != nil {
		return nil, err
	}
	byTerminal := map[string]map[string]any{}
	allowed := map[string]bool{}
	for _, id := range order {
		allowed[id] = true
	}
	if head != "" {
		allowed[head] = true
	}
	var leaves []map[string]any
	for _, leaf := range all {
		id := leafTerminal(leaf)
		if owner, ok := known[id]; id != "" && ok && !allowed[id] {
			if _, err := appCheckIdentity(id, owner); err != nil {
				return nil, err
			}
			continue
		}
		leaves = append(leaves, leaf)
	}
	for _, leaf := range leaves {
		id := leafTerminal(leaf)
		if id == "" || !allowed[id] {
			return nil, fmt.Errorf("I6: app block %v is not Head or a live owned worker", leaf["id"])
		}
		if byTerminal[id] != nil {
			return nil, fmt.Errorf("I6: duplicate terminal %s", id)
		}
		byTerminal[id] = leaf
	}
	children := []any{}
	if head != "" {
		h := byTerminal[head]
		if h == nil {
			return nil, fmt.Errorf("Head terminal %s absent from app tab", head)
		}
		if len(leaves) == 1 { // With no split, remove stale split weights from the Head.
			copy := map[string]any{}
			for k, v := range h {
				copy[k] = v
			}
			delete(copy, "size")
			children = append(children, copy)
		} else {
			children = append(children, weighted(h, 1))
		}
	}
	workers := []any{}
	for _, id := range order {
		if n := byTerminal[id]; n != nil {
			workers = append(workers, weighted(n, 1))
			delete(byTerminal, id)
		}
	}
	if len(workers) > 0 {
		weight := 1.0
		if head != "" {
			weight = 2
		}
		children = append(children, map[string]any{"type": "column", "id": "orchcol-" + tab, "size": map[string]any{"weight": weight}, "children": workers})
	}
	return map[string]any{"type": "row", "id": tab, "children": children}, nil
}
func normalizeAppTab(space, tab, head, name string, order []string, known map[string]appIdentity) error {
	for attempt := 0; attempt < 3; attempt++ {
		d, err := appDocGet()
		if err != nil {
			return err
		}
		t, err := docTab(d.Value, space, tab)
		if err != nil {
			return err
		}
		root, err := canonicalAppRoot(t["root"], tab, head, order, known)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(root, t["root"]) && (name == "" || t["name"] == name) {
			return nil
		}
		t["root"] = root
		if name != "" {
			t["name"] = name
		}
		_, err = hcRPC("doc.set", map[string]any{"name": "herdr-app", "value": d.Value, "version": d.Version})
		if err != nil {
			if strings.Contains(err.Error(), "conflict") {
				continue
			}
			return err
		}
		verify, err := appDocGet()
		if err != nil {
			return err
		}
		vt, err := docTab(verify.Value, space, tab)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(vt["root"], root) && (name == "" || vt["name"] == name) {
			return nil
		}
		return fmt.Errorf("app layout verification failed for tab %s", tab)
	}
	return fmt.Errorf("app layout conflict after three attempts")
}

// appOwnedTabs maps each tab to this Head's live worker terminals in spawn order, and
// returns all terminals of its workers, regardless of status.
func appOwnedTabs(head string) (map[string][]string, map[string]appIdentity, error) {
	runs, err := allRuns()
	if err != nil {
		return nil, nil, err
	}
	var workers []*Worker
	known := map[string]appIdentity{}
	for _, r := range runs {
		if r.HeadPane != head || runBackend(r) != "app" {
			continue
		}
		live := r.liveWorkers()
		workers = append(workers, live...)
		for _, w := range r.Workers {
			if w.Terminal != "" {
				known[w.Terminal] = appIdentity{r.ID, w.Name}
			}
		}
	}
	sort.SliceStable(workers, func(i, j int) bool { return workers[i].Started.Before(workers[j].Started) })
	tabs := map[string][]string{}
	for _, w := range workers {
		if w.Terminal != "" {
			tabs[w.Tab] = append(tabs[w.Tab], w.Terminal)
		}
	}
	return tabs, known, nil
}
func normalizeAppLayouts(head string) error {
	space, tab := headSpace(), headTab()
	if space == "" || tab == "" {
		return fmt.Errorf("cannot resolve app Head location")
	}
	tabs, known, err := appOwnedTabs(head)
	if err != nil {
		return err
	}
	runs, err := allRuns()
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, r := range runs {
		if r.HeadPane == head && runBackend(r) == "app" {
			for id, name := range r.OverflowNames {
				names[id] = name
			}
		}
	}
	if err = normalizeAppTab(space, tab, head, "", tabs[tab], known); err != nil {
		return err
	}
	for id, workers := range tabs {
		if id == tab {
			continue
		}
		if err = normalizeAppTab(space, id, "", names[id], workers, known); err != nil {
			return err
		}
	}
	return nil
}
func appGetLayout(id string) (Layout, error) {
	address := appAddress(id)
	space, tab, _, err := appPane(address)
	if err != nil {
		return Layout{}, err
	}
	d, err := appDocGet()
	if err != nil {
		return Layout{}, err
	}
	t, err := docTab(d.Value, space, tab)
	if err != nil {
		return Layout{}, err
	}
	leaves, err := appLeaves(t["root"])
	if err != nil {
		return Layout{}, err
	}
	head, err := hcGet(headID())
	if err != nil {
		return Layout{}, err
	}
	height, width := head.Rows, head.Cols
	if width < 3 {
		width = 120
	}
	l := Layout{Area: Rect{Width: width, Height: height}}
	worker := 0
	hasHead := false
	for _, leaf := range leaves {
		if leafTerminal(leaf) == headID() {
			hasHead = true
		}
	}
	for _, leaf := range leaves {
		pid, _ := leaf["id"].(string)
		rect := Rect{Width: width, Height: height}
		if leafTerminal(leaf) == headID() {
			pid = headID()
			if len(leaves) > 1 {
				rect.Width = width / 3
			}
		} else {
			n := len(leaves)
			if hasHead {
				n--
				rect.X = width / 3
				rect.Width = width - rect.X
			}
			if n > 0 {
				rect.Height = height / n
				rect.Y = worker * rect.Height
			}
			worker++
		}
		l.Panes = append(l.Panes, LayoutPane{ID: pid, Rect: rect})
	}
	return l, nil
}
