package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeGone(t *testing.T) {
	for _, gcMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "run-end", true: "gc"}[gcMode], func(t *testing.T) {
			for _, kind := range []string{"worker", "task"} {
				t.Run(kind, func(t *testing.T) {
					r, root, _ := worktreeFixture(t)
					existing := fixtureWorktree(t, r, root, "existing")
					fixtureCommit(t, r, existing, "existing.txt", "keep")
					gone := filepath.Join(t.TempDir(), "deleted")
					if err := os.Mkdir(gone, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(gone); err != nil {
						t.Fatal(err)
					}
					git(t, root, "branch", "retained")
					if kind == "worker" {
						r.Workers = append(r.Workers, &Worker{Name: "gone", Status: "closed", Worktree: gone, Branch: "retained", WorktreeBase: root, WorktreeAuto: true})
					} else {
						taskDir := taskPath(r.ID, "task-1")
						if err := os.MkdirAll(taskDir, 0700); err != nil {
							t.Fatal(err)
						}
						r.Tasks = append(r.Tasks, &Task{ID: "task-1", Run: r.ID, Dir: taskDir, Status: "done", Worktree: gone, Branch: "retained", WorktreeBase: root, WorktreeAuto: true})
					}
					if kind == "task" {
						if err := writeTask(r.Tasks[0]); err != nil {
							t.Fatal(err)
						}
					}
					r.Status, r.Adhoc = "closed", true
					saveFixture(t, r)
					for i := 0; i < 2; i++ {
						var report []string
						var err error
						if gcMode {
							report, err = gc(false)
						} else {
							report, err = cleanupRecordedWorktrees(mustLoad(t, r.ID), false)
						}
						if err != nil {
							t.Fatal(err)
						}
						joined := strings.Join(report, "\n")
						if strings.Contains(joined, gone) || !strings.Contains(joined, "worktree remains: "+existing.Worktree) {
							t.Fatalf("call %d: %s", i, joined)
						}
						fresh := mustLoad(t, r.ID)
						path := ""
						if kind == "worker" {
							path = fresh.worker("gone").Worktree
						} else {
							path = fresh.Tasks[0].Worktree
							diskTask, err := readTask(r.ID, "task-1")
							if err != nil {
								t.Fatal(err)
							}
							if diskTask.Worktree != "" || diskTask.Branch != "retained" {
								t.Fatalf("task file: %+v", diskTask)
							}
						}
						if path != "" {
							t.Fatalf("missing path still recorded: %s", path)
						}
						git(t, root, "show-ref", "--verify", "refs/heads/retained")
					}
				})
			}
		})
	}
}
