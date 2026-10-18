package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// overflowHerd models tabs explicitly and fails any attempt to take focus.
type overflowHerd struct {
	t               *testing.T
	tabs            map[string][]string
	agents          map[string]string
	next            int
	created, closed []string
	splits          [][]string
	labels          map[string]string
	keepEmpty       bool
}

func overflowFixture(t *testing.T) (*Run, *overflowHerd) {
	t.Helper()
	startupTest(t)
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("ORCH_MAX_WORKERS", "")
	t.Setenv("ORCH_MIN_ROW_HEIGHT", "8")
	t.Setenv("ORCH_MAX_ROWS", "6")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "head")
	t.Setenv("HERDR_TAB_ID", "head-tab")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("ORCH_ACCOUNT_CMD", "")
	r, err := createRun("overflow", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	f := &overflowHerd{t: t, tabs: map[string][]string{"head-tab": {"head"}}, agents: map[string]string{}, labels: map[string]string{}}
	herdrRun = f.run
	return r, f
}

func (f *overflowHerd) layout(tab string) Layout {
	ids := f.tabs[tab]
	l := Layout{Area: Rect{Width: 90, Height: 24}}
	workers := ids
	if tab == "head-tab" {
		width := 90
		if len(ids) > 1 {
			width = 30
		}
		l.Panes = append(l.Panes, LayoutPane{ID: "head", Rect: Rect{Width: width, Height: 24}})
		workers = ids[1:]
	}
	for i, id := range workers {
		x, width := 0, 90
		if tab == "head-tab" {
			x, width = 30, 60
		}
		start, end := i*24/len(workers), (i+1)*24/len(workers)
		l.Panes = append(l.Panes, LayoutPane{ID: id, Rect: Rect{X: x, Y: start, Width: width, Height: end - start}})
	}
	return l
}

func (f *overflowHerd) run(args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "--focus" || a == "focus" || a == "zoom" {
			f.t.Fatalf("focus-changing request: %v", args)
		}
	}
	result := map[string]any{}
	value := func(flag string) string {
		i := slices.Index(args, flag)
		if i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	tabOf := func(id string) string {
		for tab, ids := range f.tabs {
			if slices.Contains(ids, id) {
				return tab
			}
		}
		return ""
	}
	switch strings.Join(args[:2], " ") {
	case "pane layout":
		tab := tabOf(value("--pane"))
		if tab == "" {
			return nil, fmt.Errorf("pane missing")
		}
		result["layout"] = f.layout(tab)
	case "pane split":
		if !slices.Contains(args, "--no-focus") {
			f.t.Fatalf("split focuses: %v", args)
		}
		target := value("--pane")
		tab := tabOf(target)
		if tab == "" {
			f.t.Fatalf("split missing target: %v", args)
		}
		if target == "head" && value("--direction") != "right" {
			f.t.Fatal("Head split down")
		}
		f.next++
		id := fmt.Sprintf("pane-%d", f.next)
		f.tabs[tab] = append(f.tabs[tab], id)
		f.splits = append(f.splits, slices.Clone(args))
		result["pane"] = map[string]string{"pane_id": id}
	case "tab create":
		if value("--workspace") != "workspace" || value("--cwd") == "" || !slices.Contains(args, "ORCH_ROLE=worker") {
			f.t.Fatalf("missing context: %v", args)
		}
		f.next++
		tab, id := fmt.Sprintf("overflow-%d", f.next), fmt.Sprintf("pane-%d", f.next)
		f.tabs[tab] = []string{id}
		f.created = append(f.created, tab)
		f.labels[tab] = value("--label")
		result["tab"] = map[string]string{"tab_id": tab}
		result["root_pane"] = map[string]string{"pane_id": id}
	case "pane list":
		rows := []PaneInfo{}
		for tab, ids := range f.tabs {
			for _, id := range ids {
				rows = append(rows, PaneInfo{ID: id, Tab: tab})
			}
		}
		result["panes"] = rows
	case "pane close":
		tab := tabOf(args[2])
		if tab != "" {
			f.tabs[tab] = slices.DeleteFunc(f.tabs[tab], func(id string) bool { return id == args[2] })
			if len(f.tabs[tab]) == 0 && !f.keepEmpty {
				delete(f.tabs, tab)
				f.closed = append(f.closed, tab)
			}
		}
	case "tab close":
		if args[2] == "head-tab" {
			f.t.Fatal("closed Head tab")
		}
		if _, exists := f.tabs[args[2]]; exists {
			delete(f.tabs, args[2])
			f.closed = append(f.closed, args[2])
		}
	case "tab list":
		rows := []map[string]string{}
		for tab := range f.tabs {
			rows = append(rows, map[string]string{"tab_id": tab, "label": f.labels[tab]})
		}
		result["tabs"] = rows
	case "agent start":
		f.agents[args[2]] = value("--pane")
	case "agent get":
		result["agent"] = map[string]string{"pane_id": f.agents[args[2]], "agent_status": "idle"}
	case "pane read":
		result["text"] = "ready"
	default:
		f.t.Fatalf("unexpected call: %v", args)
	}
	b, err := json.Marshal(map[string]any{"result": result})
	return b, err
}

func TestOverflowTwelveWorkersAndCleanup(t *testing.T) {
	r, f := overflowFixture(t)
	for i := 0; i < 12; i++ {
		w, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: fmt.Sprintf("worker-%d", i), Role: roleProduce})
		if err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
		if w.Tab == "" {
			t.Fatal("worker tab not recorded")
		}
	}
	r = got0(r)
	if len(r.liveWorkers()) != 12 || len(f.tabs["head-tab"]) != 4 || len(f.created) != 3 || len(r.OverflowTabs) != 3 {
		t.Fatalf("workers=%d head=%v tabs=%v", len(r.liveWorkers()), f.tabs["head-tab"], r.OverflowTabs)
	}
	for _, tab := range r.OverflowTabs {
		if len(f.tabs[tab]) != 3 {
			t.Fatalf("rows in %s: %v", tab, f.tabs[tab])
		}
	}
	if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "thirteenth", Role: roleProduce}); err == nil || !strings.Contains(err.Error(), "is full") {
		t.Fatalf("cap: %v", err)
	}
	if err := normalizeOwnedLayouts(r.HeadPane); err != nil {
		t.Fatal(err)
	}
	if _, err := closeRun(r.ID, true); err != nil {
		t.Fatal(err)
	}
	if len(f.closed) != 3 || len(got0(r).OverflowTabs) != 0 || len(f.tabs) != 1 || len(f.tabs["head-tab"]) != 1 {
		t.Fatalf("cleanup: closed=%v tabs=%v state=%v", f.closed, f.tabs, got0(r).OverflowTabs)
	}
}

func TestWorkerCapConfig(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{{"", 12}, {"0", 12}, {"-1", 12}, {"invalid", 12}, {"10", 10}, {"24", 24}} {
		t.Setenv("ORCH_MAX_WORKERS", tc.value)
		if got := maxWorkers(); got != tc.want {
			t.Errorf("%q: %d", tc.value, got)
		}
	}
	r := &Run{Workers: []*Worker{{Pane: "a", Status: "spawned"}, {Pane: "b", Status: "closed"}, {Pane: "c", Status: "lost"}, {Status: "done"}}}
	t.Setenv("ORCH_MAX_WORKERS", "1")
	if err := workerCapacity(r); err == nil || !strings.Contains(err.Error(), "is full") {
		t.Fatalf("configured cap: %v", err)
	}
	r.Workers[0].Pane = ""
	if err := workerCapacity(r); err != nil {
		t.Fatal(err)
	}
}

func TestOverflowLastWorkerClosesTabAndProtectsUserPane(t *testing.T) {
	for _, userPane := range []bool{false, true} {
		t.Run(fmt.Sprint(userPane), func(t *testing.T) {
			r, f := overflowFixture(t)
			for i := 0; i < 5; i++ {
				if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: fmt.Sprintf("worker-%d", i), Role: roleProduce}); err != nil {
					t.Fatal(err)
				}
			}
			r = got0(r)
			tab := r.Workers[3].Tab
			if err := closeWorker(r.ID, "worker-3"); err != nil {
				t.Fatal(err)
			}
			if len(f.closed) != 0 {
				t.Fatal("tab closed with live worker")
			}
			if userPane {
				f.tabs[tab] = append(f.tabs[tab], "user-pane")
			}
			err := closeWorker(r.ID, "worker-4")
			if userPane {
				if err != nil || len(f.closed) != 0 || !slices.Contains(f.tabs[tab], "user-pane") {
					t.Fatalf("user protection: %v %v", err, f.tabs)
				}
			} else if err != nil || !slices.Contains(f.closed, tab) {
				t.Fatalf("last worker: %v %v", err, f.closed)
			}
		})
	}
}

func TestOverflowReaperAndGC(t *testing.T) {
	for _, reap := range []bool{true, false} {
		t.Run(fmt.Sprint(reap), func(t *testing.T) {
			r, f := overflowFixture(t)
			for i := 0; i < 4; i++ {
				if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: fmt.Sprintf("worker-%d", i), Role: roleProduce}); err != nil {
					t.Fatal(err)
				}
			}
			r = got0(r)
			tab := r.Workers[3].Tab
			if reap {
				if err := os.WriteFile(r.resultPath("worker-3"), []byte("- status: done\n"), 0600); err != nil {
					t.Fatal(err)
				}
				rp := newReaper(r.ID)
				var log strings.Builder
				for i := 0; i < 2; i++ {
					if _, err := rp.once(&log); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err := withRun(r.ID, func(x *Run) error { x.Status = "closed"; return nil }); err != nil {
					t.Fatal(err)
				}
				if _, err := gc(false); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Contains(f.closed, tab) || len(got0(r).OverflowTabs) != 0 {
				t.Fatalf("overflow not cleaned: %v", f.closed)
			}
		})
	}
}

func TestNormalizeOverflowRepairsRowsWithoutFocus(t *testing.T) {
	testNormalizeOverflowUneven(t, []string{"a", "b", "c"})
}

func TestNormalizeOverflowUnevenRowsWithMissingFirstPane(t *testing.T) {
	testNormalizeOverflowUneven(t, []string{"missing", "a", "b", "c"})
}

func testNormalizeOverflowUneven(t *testing.T, owned []string) {
	t.Helper()
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	repaired := false
	herdrRun = func(args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "pane layout":
			if slices.Contains(args, "missing") {
				return nil, fmt.Errorf("pane missing")
			}
			l := Layout{Area: Rect{Width: 90, Height: 24}, Panes: []LayoutPane{
				{ID: "a", Rect: Rect{Width: 90, Height: 12}},
				{ID: "b", Rect: Rect{Y: 12, Width: 90, Height: 6}},
				{ID: "c", Rect: Rect{Y: 18, Width: 90, Height: 6}},
			}, Splits: []LayoutSplit{{Direction: "down", Ratio: .5, Rect: Rect{Width: 90, Height: 24}}, {Direction: "down", Ratio: .5, Rect: Rect{Y: 12, Width: 90, Height: 12}}}}
			if repaired {
				for i := range l.Panes {
					l.Panes[i].Rect.Y = i * 8
					l.Panes[i].Rect.Height = 8
				}
			}
			b, e := json.Marshal(map[string]any{"result": map[string]any{"layout": l}})
			return b, e
		case "pane resize":
			if !slices.Contains(args, "b") || !slices.Contains(args, "up") || !slices.Contains(args, "0.1667") {
				t.Fatalf("wrong resize: %v", args)
			}
			repaired = true
			return []byte(`{"result":{}}`), nil
		default:
			t.Fatalf("unexpected/focus-changing request: %v", args)
			return nil, nil
		}
	}
	if err := normalizeOverflow(owned); err != nil {
		t.Fatal(err)
	}
	if !repaired {
		t.Fatal("unequal rows were not repaired")
	}
}

func TestOverflowCleanupRetryAndReuse(t *testing.T) {
	r, f := overflowFixture(t)
	for i := 0; i < 5; i++ {
		if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: fmt.Sprintf("worker-%d", i), Role: roleProduce}); err != nil {
			t.Fatal(err)
		}
	}
	r = got0(r)
	tab := r.Workers[3].Tab
	if err := closeWorker(r.ID, "worker-3"); err != nil {
		t.Fatal(err)
	}
	w, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "replacement", Role: roleProduce})
	if err != nil || w.Tab != tab || len(f.created) != 1 {
		t.Fatalf("reuse: worker=%+v err=%v created=%v", w, err, f.created)
	}
	if err := closeWorker(r.ID, "replacement"); err != nil {
		t.Fatal(err)
	}
	f.keepEmpty = true // Exercise a Herdr tab that remains after its last pane closes.
	original := herdrRun
	herdrRun = func(args ...string) ([]byte, error) {
		if strings.Join(args[:2], " ") == "tab close" {
			return nil, fmt.Errorf("temporary tab-close failure")
		}
		return original(args...)
	}
	if err := closeWorker(r.ID, "worker-4"); err != nil {
		t.Fatalf("confirmed pane close failed: %v", err)
	}
	if !slices.Contains(got0(r).OverflowTabs, tab) {
		t.Fatal("failed tab ID was forgotten")
	}
	herdrRun = original
	if err := closeWorker(r.ID, "worker-4"); err != nil {
		t.Fatal(err)
	}
	if len(got0(r).OverflowTabs) != 0 || !slices.Contains(f.closed, tab) {
		t.Fatal("empty-tab retry failed")
	}
}

func TestSpawnConfiguredCapAndZoomedHead(t *testing.T) {
	r, f := overflowFixture(t)
	t.Setenv("ORCH_MAX_WORKERS", "1")
	if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "first", Role: roleProduce}); err != nil {
		t.Fatal(err)
	}
	if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "second", Role: roleProduce}); err == nil || !strings.Contains(err.Error(), "is full") {
		t.Fatalf("configured cap: %v", err)
	}
	t.Setenv("ORCH_MAX_WORKERS", "12")
	l := f.layout("head-tab")
	l.Zoomed = true
	if _, _, err := placeWorker(got0(r), l, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "zoomed") {
		t.Fatalf("zoomed spawn: %v", err)
	}
	if f.next != 1 {
		t.Fatal("zoomed layout created a pane")
	}
}

func TestOverflowWaitAndCollectCleanup(t *testing.T) {
	for _, method := range []string{"wait", "collect"} {
		t.Run(method, func(t *testing.T) {
			r, f := overflowFixture(t)
			for i := 0; i < 4; i++ {
				if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: fmt.Sprintf("worker-%d", i), Role: roleProduce}); err != nil {
					t.Fatal(err)
				}
			}
			r = got0(r)
			tab := r.Workers[3].Tab
			if err := os.WriteFile(r.resultPath("worker-3"), []byte("- status: done\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if method == "wait" {
				outcomes, err := waitWorkers(r.ID, []string{"worker-3"}, 0, false)
				if err != nil || len(outcomes) != 1 || outcomes[0].Status != "done" {
					t.Fatalf("wait: %v %v", outcomes, err)
				}
			} else {
				_, status, err := collectWorker(r.ID, "worker-3")
				if err != nil || status != "done" {
					t.Fatalf("collect: %s %v", status, err)
				}
			}
			if !slices.Contains(f.closed, tab) || len(got0(r).OverflowTabs) != 0 {
				t.Fatalf("%s left overflow tab: %v", method, f.tabs)
			}
		})
	}
}

func spawnOverflowN(t *testing.T, r *Run, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: fmt.Sprintf("w%d", i), Role: roleProduce}); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}
}

// Ported critical-review repro: losing an overflow pane must not block Head or overflow placement.
func TestReproStaleOverflowPaneBlocksSpawn(t *testing.T) {
	for _, mode := range []string{"free-head", "new-tab", "existing-tab", "surviving-pane"} {
		t.Run(mode, func(t *testing.T) {
			r, f := overflowFixture(t)
			n := 4
			if mode == "existing-tab" {
				n = 7
			}
			if mode == "surviving-pane" {
				n = 5
			}
			spawnOverflowN(t, r, n)
			r = got0(r)
			ov := r.OverflowTabs[0]
			if mode == "surviving-pane" {
				f.tabs[ov] = f.tabs[ov][1:]
			} else {
				delete(f.tabs, ov)
			}
			if mode == "free-head" {
				if err := closeWorker(r.ID, "w0"); err != nil {
					t.Fatal(err)
				}
			}
			w, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "next", Role: roleProduce})
			if err != nil {
				t.Fatalf("spawn after missing overflow pane: %v", err)
			}
			if mode == "free-head" && w.Tab != "head-tab" {
				t.Fatalf("placed in %s", w.Tab)
			}
			if mode == "existing-tab" && w.Tab != r.OverflowTabs[1] {
				t.Fatalf("did not reuse healthy tab: %s", w.Tab)
			}
			if mode == "surviving-pane" && w.Tab != ov {
				t.Fatalf("did not read surviving pane: %s", w.Tab)
			}
			if lost := got0(r).worker("w3"); lost.Status != "lost" || lost.Pane != "" {
				t.Fatalf("missing pane not marked lost: %+v", lost)
			}
		})
	}
}

// Ported critical-review repro: a kept user tab must not poison unrelated confirmed closes.
func TestReproUserPaneMakesEveryCloseFail(t *testing.T) {
	r, f := overflowFixture(t)
	spawnOverflowN(t, r, 4)
	r = got0(r)
	ov := r.OverflowTabs[0]
	f.tabs[ov] = append(f.tabs[ov], "user-pane")
	if err := closeWorker(r.ID, "w3"); err != nil {
		t.Fatal(err)
	}
	if err := closeWorker(r.ID, "w0"); err != nil {
		t.Fatalf("unrelated close failed: %v", err)
	}
	if got0(r).worker("w0").Pane != "" || !slices.Contains(f.tabs[ov], "user-pane") {
		t.Fatal("close or user protection failed")
	}
	if _, err := closeRun(r.ID, true); err == nil || !strings.Contains(err.Error(), "I6") {
		t.Fatalf("run end did not report kept tab: %v", err)
	}
}

func TestOverflowMalformedCreateCleanupAndMonotonicLabels(t *testing.T) {
	for _, response := range []string{"invalid json", `{"result":{"tab":{},"root_pane":{}}}`} {
		t.Run(response, func(t *testing.T) {
			r, f := overflowFixture(t)
			spawnOverflowN(t, r, 3)
			original := herdrRun
			herdrRun = func(args ...string) ([]byte, error) {
				b, err := original(args...)
				if args[0] == "tab" && args[1] == "create" && err == nil {
					return []byte(response), nil
				}
				return b, err
			}
			if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "bad", Role: roleProduce}); err == nil {
				t.Fatal("malformed response accepted")
			}
			if len(f.tabs) != 1 || len(f.closed) != 1 {
				t.Fatalf("orphan tab: %v", f.tabs)
			}
			herdrRun = original
			spawnOne := func(name string) *Worker {
				t.Helper()
				w, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: name, Role: roleProduce})
				if err != nil {
					t.Fatal(err)
				}
				return w
			}
			first := spawnOne("first")
			if f.labels[first.Tab] != "orch-"+r.ID+"-2" {
				t.Fatalf("failed create sequence reused: %v", f.labels)
			}
			if err := closeWorker(r.ID, "first"); err != nil {
				t.Fatal(err)
			}
			second := spawnOne("second")
			if f.labels[second.Tab] != "orch-"+r.ID+"-3" || got0(r).OverflowSeq != 3 {
				t.Fatalf("removed tab sequence reused: %v", f.labels)
			}
		})
	}
}

func TestOverflowLayoutFailureDoesNotAbortHeadSpawn(t *testing.T) {
	r, f := overflowFixture(t)
	spawnOverflowN(t, r, 4)
	ov := got0(r).OverflowTabs[0]
	pane := f.tabs[ov][0]
	if err := closeWorker(r.ID, "w0"); err != nil {
		t.Fatal(err)
	}
	original := herdrRun
	herdrRun = func(args ...string) ([]byte, error) {
		if args[0] == "pane" && args[1] == "layout" && slices.Contains(args, pane) {
			return nil, fmt.Errorf("temporary layout failure")
		}
		return original(args...)
	}
	if _, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "next", Role: roleProduce}); err != nil {
		t.Fatal(err)
	}
	if got0(r).worker("w3").Status == "lost" {
		t.Fatal("layout failure marked existing pane lost")
	}
}
