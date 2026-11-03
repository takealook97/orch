package main

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

const movedLS = `{"spaces":[` +
	`{"id":"s2","tabs":[{"id":"t2","terminals":[{"terminal":"th","pane":"ph"},{"terminal":"tw1","pane":"pw1"},{"terminal":"tw3","pane":"pw3"}]}]},` +
	`{"id":"s3","tabs":[{"id":"o2","terminals":[{"terminal":"tw2","pane":"pw2"}]}]}]}`

func headMoveRun(t *testing.T) *Run {
	t.Helper()
	now := time.Now()
	r := &Run{ID: "r1", Backend: "app", HeadPane: "th", Workspace: "s", Tab: "tab", Status: "active",
		OverflowTabs: []string{"oldov"}, OverflowNames: map[string]string{"oldov": "orch r1 1"},
		Workers: []*Worker{
			{Name: "w1", Terminal: "tw1", Pane: "pw1", Tab: "tab", Status: "working", Started: now},
			{Name: "w2", Terminal: "tw2", Pane: "old2", Tab: "oldov", Status: "working", Started: now.Add(time.Second)},
			{Name: "w3", Terminal: "tw3", Pane: "pw3", Tab: "tab", Status: "working", Started: now.Add(2 * time.Second)},
			{Name: "gone", Terminal: "tgone", Pane: "pgone", Tab: "oldov", Status: "working", Started: now.Add(3 * time.Second)},
		}}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRefreshAppLocationsHeadMoved(t *testing.T) {
	appTest(t)
	headMoveRun(t)
	appRun = func(...string) ([]byte, error) { return []byte(movedLS), nil }
	if err := refreshAppLocations("th"); err != nil {
		t.Fatal(err)
	}
	r, _ := loadRun("r1")
	if r.Workspace != "s2" || r.Tab != "t2" {
		t.Fatalf("head location %s/%s", r.Workspace, r.Tab)
	}
	tabs := map[string]string{}
	for _, w := range r.Workers {
		tabs[w.Name] = w.Tab
	}
	want := map[string]string{"w1": "t2", "w2": "o2", "w3": "t2", "gone": "oldov"}
	for name, tab := range want {
		if tabs[name] != tab {
			t.Errorf("worker %s tab %q want %q", name, tabs[name], tab)
		}
	}
	if r.worker("w2").Pane != "pw2" {
		t.Errorf("pane not refreshed: %s", r.worker("w2").Pane)
	}
	if !slices.Equal(r.OverflowTabs, []string{"oldov", "o2"}) {
		t.Errorf("overflow %v", r.OverflowTabs)
	}
	if r.OverflowNames["o2"] != "orch r1 1" || r.OverflowNames["oldov"] != "" {
		t.Errorf("names %v", r.OverflowNames)
	}
	last := r.Events[len(r.Events)-1].What
	if last != "app Head moved: space s/tab tab -> space s2/tab t2" {
		t.Errorf("event %q", last)
	}
}

func TestAppSelectTabAfterHeadMove(t *testing.T) {
	for _, tt := range []struct {
		name, rows, wantTab string
	}{
		{"head tab has room", "3", "t2"},
		{"overflow tab in another space", "2", "o2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			appTest(t)
			t.Setenv("ORCH_MAX_ROWS", tt.rows)
			t.Setenv("ORCH_MIN_ROW_HEIGHT", "1")
			stale := headMoveRun(t)
			appRun = func(...string) ([]byte, error) { return []byte(movedLS), nil }
			hcRun = func(args ...string) ([]byte, error) {
				if args[0] == "run" {
					if !slices.Contains(args, "herdr.space=s2") || !slices.Contains(args, "herdr.tab="+tt.wantTab) {
						t.Errorf("labels %v", args)
					}
					return []byte(`{"address":"tnew","state":"live"}`), nil
				}
				return nil, errHCNotFound
			}
			hcRPC = func(method string, _ any) (json.RawMessage, error) {
				blocks := func(ids ...string) string {
					var parts []string
					for _, id := range ids {
						parts = append(parts, `{"type":"block","id":"p`+id+`","content":{"terminal":"`+id+`"}}`)
					}
					return strings.Join(parts, ",")
				}
				return json.RawMessage(`{"version":1,"value":{"spaces":[` +
					`{"id":"s2","tabs":[{"id":"t2","root":{"type":"row","id":"t2","children":[` + blocks("th", "tw1", "tw3") + `]}}]},` +
					`{"id":"s3","tabs":[{"id":"o2","root":{"type":"row","id":"o2","children":[` + blocks("tw2") + `]}}]}]}}`), nil
			}
			tab, err := appSelectTab(stale, Layout{Area: Rect{Width: 120, Height: 100}})
			if err != nil {
				t.Fatal(err)
			}
			if tab != tt.wantTab || stale.Workspace != "s2" || stale.Tab != "t2" {
				t.Fatalf("tab=%s run=%s/%s", tab, stale.Workspace, stale.Tab)
			}
			if _, _, err = appLaunch(stale, tab, "/cwd", "new", nil, []string{"codex"}); err != nil && !strings.Contains(err.Error(), "pane lookup") {
				t.Fatal(err)
			}
		})
	}
}

func TestRefreshAppLocationsErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		ls   func(...string) ([]byte, error)
	}{
		{"lookup error", func(...string) ([]byte, error) { return nil, errors.New("herdr-app down") }},
		{"head absent", func(...string) ([]byte, error) { return []byte(`{"spaces":[]}`), nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			appTest(t)
			headMoveRun(t)
			appRun = tt.ls
			if err := refreshAppLocations("th"); err == nil {
				t.Fatal("expected error")
			}
			r, _ := loadRun("r1")
			if r.Workspace != "s" || r.Tab != "tab" {
				t.Fatalf("run changed on error: %s/%s", r.Workspace, r.Tab)
			}
		})
	}
}

func TestRefreshAppLocationsUnchangedWritesNothing(t *testing.T) {
	appTest(t)
	r := headMoveRun(t)
	r.Workspace, r.Tab, r.OverflowTabs, r.OverflowNames = "s2", "t2", []string{"o2"}, map[string]string{"o2": "n"}
	r.worker("w1").Tab, r.worker("w3").Tab, r.worker("w2").Tab, r.worker("w2").Pane = "t2", "t2", "o2", "pw2"
	r.Workers = r.Workers[:3]
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	path := r.dir() + "/run.json"
	before, _ := os.Stat(path)
	appRun = func(...string) ([]byte, error) { return []byte(movedLS), nil }
	time.Sleep(10 * time.Millisecond)
	if err := refreshAppLocations("th"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("unchanged location rewrote run.json")
	}
}
