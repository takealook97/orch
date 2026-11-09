package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise prompt persistence and collection, with every agent operation stubbed.
func paneScopePrompt(t *testing.T, r *Run, w *Worker, base string) *Worker {
	t.Helper()
	body := strings.Replace(contractTemplate(r, w.Name, "produce", "codex-high-m"), "<hash or none>", base, 1)
	body = strings.Replace(body, "- Owned files (glob): none", "- Owned files (glob): owned.txt", 1)
	if err := os.WriteFile(r.contractPath(w.Name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	w.Pane, w.Status, w.Result = "worker-pane", "spawned", r.resultPath(w.Name)
	r.Workers = []*Worker{w}
	saveFixture(t, r)
	old, times := herdrRun, timings
	t.Cleanup(func() { herdrRun, timings = old, times })
	timings.TrustSettlePrompt, timings.KeyGap = 0, 0
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "agent" && args[1] == "send-keys" {
			return nil, os.WriteFile(w.Result, []byte("- status: done\n"), 0600)
		}
		return []byte(`{"result":{"agent":{"agent_status":"idle"}}}`), nil
	}
	if err := prompt(r.ID, w.Name, "", false, 0); err != nil {
		t.Fatal(err)
	}
	got, err := loadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.worker(w.Name)
}

func TestPaneCommittedScope(t *testing.T) {
	for _, name := range []string{"outside", "explicit-outside", "dirty-outside", "revert-worktree", "discard-dirty", "owned", "dirty-owned", "worktree-base", "empty-repo"} {
		t.Run(name, func(t *testing.T) {
			r, root, _ := worktreeFixture(t)
			if name == "empty-repo" {
				root = gitInit(t)
			}
			file := "outside.txt"
			if name == "owned" || name == "dirty-owned" || name == "worktree-base" {
				file = "owned.txt"
			}
			if name == "discard-dirty" {
				putFile(t, root, file, "base\n")
				git(t, root, "add", file)
				git(t, root, "commit", "-qm", "base outside")
			}
			if strings.HasPrefix(name, "dirty-") || name == "discard-dirty" {
				putFile(t, root, file, "user dirty\n")
			}
			// Baseline dirt left unchanged must never become a worker change.
			putFile(t, root, "untouched.txt", "leave alone\n")
			w := &Worker{Name: "impl", Cwd: root}
			if name == "worktree-base" {
				w = fixtureWorktree(t, r, root, "impl")
				w.WorktreeStart = strings.TrimSpace(git(t, w.Cwd, "rev-parse", "HEAD"))
			}
			base := "none"
			if name == "explicit-outside" || name == "revert-worktree" {
				base = strings.TrimSpace(git(t, w.Cwd, "rev-parse", "HEAD"))
			}
			w = paneScopePrompt(t, r, w, base)
			if name == "discard-dirty" {
				git(t, w.Cwd, "checkout", "--", file)
			} else {
				if !strings.HasPrefix(name, "dirty-") {
					putFile(t, w.Cwd, file, "worker\n")
				}
				git(t, w.Cwd, "add", file)
				git(t, w.Cwd, "commit", "-qm", "worker commit")
				if name == "revert-worktree" {
					if err := os.Remove(filepath.Join(w.Cwd, file)); err != nil {
						t.Fatal(err)
					}
				}
			}
			w.Pane, w.Status = "", "closed"
			r, _ = loadRun(r.ID)
			r.Workers = []*Worker{w}
			saveFixture(t, r)
			if err := os.WriteFile(w.Result, []byte("- status: done\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out, status, err := collectWorker(r.ID, w.Name)
			want := "failed"
			if file == "owned.txt" {
				want = "done"
			}
			if err != nil || status != want || !strings.Contains(out, "changed files: "+file) || strings.Contains(out, "untouched.txt") {
				t.Fatalf("status=%s want=%s err=%v output=%s", status, want, err, out)
			}
			if want == "failed" && !strings.Contains(out, "scope violation") {
				t.Fatal(out)
			}
			saved, _ := loadRun(r.ID)
			if saved.worker(w.Name).Status != want {
				t.Fatalf("persisted worker: %+v", saved.worker(w.Name))
			}
		})
	}
}
