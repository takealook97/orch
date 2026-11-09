package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGCStaleRuns(t *testing.T) {
	for _, name := range []string{"legacy", "same-backend", "live-working", "live-prompted", "live-spawned", "live-pane", "recent", "running-task", "queued-task", "disk-task", "recent-task", "disabled", "worktree", "without-orphans"} {
		t.Run(name, func(t *testing.T) {
			now := pruneEnv(t)
			t.Setenv("ORCH_MUX", "app")
			t.Setenv("HC_TERMINAL_ID", "current-head")
			old := now.Add(-10 * 24 * time.Hour)
			r := pruneRun(t, "stale-run", old)
			r.Status, r.Backend, r.HeadPane = "active", "herdr", "old-head"
			switch name {
			case "same-backend":
				r.Backend = "herdr"
				t.Setenv("ORCH_MUX", "herdr")
				t.Setenv("HC_TERMINAL_ID", "")
				t.Setenv("HERDR_PANE_ID", "current-head")
			case "live-working", "live-prompted", "live-spawned":
				r.Workers = []*Worker{{Name: "live", Status: strings.TrimPrefix(name, "live-")}}
			case "live-pane":
				r.Workers = []*Worker{{Name: "live", Status: "done", Pane: "still-open"}}
			case "recent":
				old = now
			case "running-task", "queued-task":
				r.Tasks = []*Task{{ID: "task-1", Status: strings.TrimSuffix(name, "-task")}}
			case "disk-task", "recent-task":
				task := &Task{ID: "task-1", Run: r.ID, Status: "running", Dir: taskPath(r.ID, "task-1")}
				if name == "recent-task" {
					task.Status = "done"
				}
				if err := os.MkdirAll(task.Dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := writeTask(task); err != nil {
					t.Fatal(err)
				}
				at := old
				if name == "recent-task" {
					at = now
				}
				pruneMtime(t, filepath.Join(task.Dir, "task.json"), at)
			case "disabled":
				t.Setenv("ORCH_RETENTION_DAYS", "0")
			case "worktree":
				dir := t.TempDir()
				putFile(t, dir, "sentinel", "preserve")
				r.Workers = []*Worker{{Name: "done", Status: "closed", Worktree: dir, Branch: "stale-branch", WorktreeAuto: true}}
			}
			pruneSaveRun(t, r, old)
			// Foreign-backend closure is metadata-only. On our backend a live
			// Head must first rule out the legacy orphan-cleanup path.
			oldHerdr, oldApp, oldHC, oldRPC, oldAlive := herdrRun, appRun, hcRun, hcRPC, paneAliveFn
			t.Cleanup(func() { herdrRun, appRun, hcRun, hcRPC, paneAliveFn = oldHerdr, oldApp, oldHC, oldRPC, oldAlive })
			unexpected := func(args ...string) ([]byte, error) {
				t.Errorf("unexpected pane/mux call: %v", args)
				return nil, fmt.Errorf("pane calls forbidden")
			}
			herdrRun, appRun, hcRun = unexpected, unexpected, unexpected
			if name == "same-backend" {
				herdrRun = func(args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "pane" && args[1] == "list" {
						return []byte(`{"result":{"panes":[{"pane_id":"current-head"},{"pane_id":"old-head"}]}}`), nil
					}
					return unexpected(args...)
				}
			}
			hcRPC = func(method string, params any) (json.RawMessage, error) {
				t.Errorf("unexpected mux RPC: %s", method)
				return nil, fmt.Errorf("pane calls forbidden")
			}
			paneAliveFn = func(id string) (bool, error) {
				t.Errorf("unexpected pane liveness: %s", id)
				return false, fmt.Errorf("pane calls forbidden")
			}
			report, err := gc(name != "without-orphans")
			if err != nil {
				t.Fatal(err)
			}
			saved, err := loadRun(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			closeWant := contains1([]string{"legacy", "same-backend", "worktree"}, name)
			if (saved.Status == "closed") != closeWant {
				t.Fatalf("status=%s closeWant=%v report=%v", saved.Status, closeWant, report)
			}
			joined := strings.Join(report, "\n")
			if closeWant {
				want := "closed stale run " + r.ID + " (backend " + r.Backend + ", head old-head, idle 10d)"
				if strings.Count(joined, want) != 1 {
					t.Fatalf("missing unique closure %q: %s", want, joined)
				}
			} else if strings.Contains(joined, "closed stale run") {
				t.Fatal(joined)
			}
			if name == "worktree" {
				w := saved.Workers[0]
				if w.Worktree != r.Workers[0].Worktree {
					t.Fatalf("worktree metadata changed: %+v", w)
				}
				pruneExists(t, filepath.Join(w.Worktree, "sentinel"), true)
				if !strings.Contains(joined, "worktree remains: "+w.Worktree) {
					t.Fatal(joined)
				}
			}
		})
	}
}
