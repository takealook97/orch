package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func worktreeFixture(t *testing.T) (*Run, string, string) {
	t.Helper()
	taskEnv(t)
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "wt-head")
	t.Setenv("HC_TERMINAL_ID", "")
	stubPaneAlive(t, func(string) (bool, error) { return true, nil })
	root := filepath.Join(t.TempDir(), "repo")
	base := scopeRepo(t, root)
	root = canonicalRoot(root)
	r, err := createRun("worktree test", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return r, root, base
}

func stubPaneAlive(t *testing.T, fn func(string) (bool, error)) {
	t.Helper()
	old := paneAliveFn
	paneAliveFn = fn
	t.Cleanup(func() { paneAliveFn = old })
}

func saveFixture(t *testing.T, r *Run) {
	t.Helper()
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
}

func putFile(t *testing.T, dir, path, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixtureWorktree(t *testing.T, r *Run, root, name string) *Worker {
	t.Helper()
	state, err := prepareWorktree(r.ID, name, root, filepath.Join(filepath.Dir(root), name), false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Name: name, Role: "produce", Status: "done", Cwd: state.Dir, Worktree: state.Dir, Branch: state.Branch, WorktreeBase: root, WorktreeAuto: true}
	r.Workers = append(r.Workers, w)
	saveFixture(t, r)
	return w
}

func fixtureCommit(t *testing.T, r *Run, w *Worker, path, text string) {
	t.Helper()
	putFile(t, w.Worktree, path, text)
	hash, err := commitWorktree(w.Worktree, r.ID, w.Name, "fixture change\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	w.Commit, w.Committed = hash, true
	saveFixture(t, r)
}

func TestAutoWorktreeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		disabled, explicit, collision bool
	}{
		{name: "automatic"}, {name: "collision", collision: true}, {name: "disabled", disabled: true}, {name: "explicit", explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, root, _ := worktreeFixture(t)
			r.Workers = []*Worker{{Name: "first", Role: "produce", Pane: "live", Status: "prompted", Cwd: root}}
			saveFixture(t, r)
			expected := filepath.Join(filepath.Dir(root), "repo-wt-"+r.ID[len(r.ID)-6:]+"-second")
			if tc.collision {
				if err := os.Mkdir(expected, 0700); err != nil {
					t.Fatal(err)
				}
				expected += "-2"
			}
			explicit := ""
			if tc.explicit {
				explicit = filepath.Join(filepath.Dir(root), "chosen")
				expected = explicit
			}
			putFile(t, root, "dirty.txt", "head-only")
			var out bytes.Buffer
			state, err := prepareWorktree(r.ID, "second", root, explicit, tc.disabled, &out)
			if err != nil {
				t.Fatal(err)
			}
			if tc.disabled {
				if state.Dir != "" {
					t.Fatalf("disabled: %+v", state)
				}
				return
			}
			if state.Dir != expected || state.Base != root || state.Auto != !tc.explicit {
				t.Fatalf("state=%+v expected=%s", state, expected)
			}
			branch := "orch/" + r.ID + "/second"
			if tc.explicit {
				branch = "orch/" + r.ID + "-second"
			}
			if state.Branch != branch {
				t.Fatalf("branch=%s", state.Branch)
			}
			if _, err := os.Stat(filepath.Join(state.Dir, "dirty.txt")); !os.IsNotExist(err) {
				t.Fatal("copied dirty source changes")
			}
			if !tc.explicit && (!strings.Contains(out.String(), r.ID+"/first") || !strings.Contains(out.String(), "uncommitted changes")) {
				t.Fatalf("missing diagnostics: %s", out.String())
			}
			if got := strings.TrimSpace(git(t, state.Dir, "branch", "--show-current")); got != state.Branch {
				t.Fatal(got)
			}
			// It is a linked worktree of the same repository, and an explicit respawn reuses it.
			if !strings.Contains(git(t, root, "worktree", "list", "--porcelain"), state.Dir) {
				t.Fatal("worktree not registered for pretrust")
			}
			t.Chdir(root)
			if !trustable(state.Dir) {
				t.Fatal("Head's linked worktree is not pretrusted")
			}
			reused, err := prepareWorktree(r.ID, "second", root, state.Dir, false, io.Discard)
			// An existing path is a plain cwd: nothing records a branch for the executor to manage.
			if err != nil || reused.Dir != state.Dir || reused.Branch != "" {
				t.Fatalf("reuse=%+v err=%v", reused, err)
			}
		})
	}
}

func TestLiveWritersAcrossHeadsAndBackends(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	r.Backend, r.HeadPane = "app", "other-head"
	r.Workers = []*Worker{
		{Name: "live", Role: "integrate", Pane: "p", Status: "done", Cwd: root},
		{Name: "closed", Role: "produce", Pane: "p", Status: "closed", Cwd: root},
		{Name: "review", Role: "verify-critical", Pane: "p", Status: "prompted", Cwd: root},
	}
	r.Tasks = []*Task{
		{ID: "task-1", Kind: "do", Status: "running", Pid: os.Getpid(), Cwd: root},
		{ID: "task-2", Kind: "do", Status: "pending", Pid: 0, Cwd: root},
		{ID: "task-3", Kind: "ask", Status: "running", Pid: os.Getpid(), Cwd: root},
	}
	saveFixture(t, r)
	refs := liveWritersIn(root, nil)
	if len(refs) != 3 {
		t.Fatalf("refs=%+v", refs)
	}
	if refs := liveWritersIn(root, func(ref writerRef) bool { return ref.Run == r.ID && ref.Name == "live" }); len(refs) != 2 {
		t.Fatalf("exclude=%+v", refs)
	}
	var out bytes.Buffer
	warnOtherHeads(&Run{HeadPane: "my-head"}, root, &out)
	if !strings.Contains(out.String(), "Head other-head run "+r.ID) || !strings.Contains(out.String(), "live,task-1") {
		t.Fatalf("warning=%s", out.String())
	}
	// A symlink spelling identifies the same checkout.
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if len(liveWritersIn(link, nil)) != 3 {
		t.Fatal("symlink checkout not matched")
	}
}

func TestCollectWorktreeCommitAndScope(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			r, root, base := worktreeFixture(t)
			w := fixtureWorktree(t, r, root, "impl")
			w.Status = "closed"
			w.Contract, w.Result = r.contractPath(w.Name), r.resultPath(w.Name)
			// The original checkout is named explicitly. Scope must still inspect the worker.
			body := "- base commit: " + base + "\n- worktree: " + root + "\n- Owned files (glob): " + filepath.Join(root, "owned.txt") + "\n"
			if err := os.WriteFile(w.Contract, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(w.Result, []byte("# Result\n- summary: implement owned file\n- status: done\n"), 0600); err != nil {
				t.Fatal(err)
			}
			putFile(t, w.Worktree, "owned.txt", "worker change")
			putFile(t, root, "other-writer.txt", "head dirty")
			if outside {
				putFile(t, w.Worktree, "outside.txt", "bad")
			}
			saveFixture(t, r)
			out, status, err := collectWorker(r.ID, w.Name)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ := loadRun(r.ID)
			got := fresh.worker(w.Name)
			if outside {
				if status != "failed" || got.Committed || !strings.Contains(out, "worktree left uncommitted") {
					t.Fatalf("status=%s worker=%+v output=%s", status, got, out)
				}
				if strings.TrimSpace(git(t, w.Worktree, "rev-parse", "HEAD")) != base {
					t.Fatal("scope violation was committed")
				}
			} else {
				if status != "done" || !got.Committed || got.Commit == base {
					t.Fatalf("status=%s worker=%+v output=%s", status, got, out)
				}
				if dirty := git(t, w.Worktree, "status", "--porcelain"); dirty != "" {
					t.Fatalf("dirty=%s", dirty)
				}
				if strings.Contains(out, "other-writer.txt") {
					t.Fatalf("checked another writer: %s", out)
				}
				if _, _, err := collectWorker(r.ID, w.Name); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMergeWorktreeLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		keep, failTest, conflict, dirty bool
	}{
		{name: "success"}, {name: "keep", keep: true}, {name: "failed-test", failTest: true}, {name: "conflict", conflict: true}, {name: "dirty-overlap", dirty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, root, base := worktreeFixture(t)
			w := fixtureWorktree(t, r, root, "impl")
			fixtureCommit(t, r, w, "owned.txt", "worker")
			if tc.conflict {
				putFile(t, root, "owned.txt", "source")
				git(t, root, "add", "-A")
				git(t, root, "commit", "-qm", "conflict source")
			}
			if tc.dirty {
				putFile(t, root, "owned.txt", "uncommitted source")
			}
			test := ""
			if tc.failTest {
				test = "exit 7"
				t.Setenv("ORCH_RUN", r.ID)
			}
			var out, errout bytes.Buffer
			err := mergeWorktrees(r.ID, []string{w.Name}, test, tc.keep, false, &out, &errout)
			failed := tc.failTest || tc.conflict || tc.dirty
			if (err != nil) != failed {
				t.Fatalf("err=%v output=%s stderr=%s", err, out.String(), errout.String())
			}
			fresh, _ := loadRun(r.ID)
			got := fresh.worker(w.Name)
			if failed || tc.keep {
				if _, err := os.Stat(w.Worktree); err != nil {
					t.Fatal("worktree removed", err)
				}
				git(t, root, "show-ref", "--verify", "refs/heads/"+w.Branch)
				if failed && got.Merged {
					t.Fatal("failed integration marked merged")
				}
			} else {
				if _, err := os.Stat(w.Worktree); !os.IsNotExist(err) {
					t.Fatal("worktree remains", err)
				}
				if _, err := gitOutput(root, "show-ref", "--verify", "refs/heads/"+w.Branch); err == nil {
					t.Fatal("branch remains")
				}
				if !got.Merged || got.Worktree != "" {
					t.Fatalf("state=%+v", got)
				}
			}
			if tc.conflict {
				if _, err := gitOutput(root, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
					t.Fatal("merge not aborted")
				}
				if strings.TrimSpace(git(t, root, "status", "--porcelain")) != "" {
					t.Fatal("conflict artifacts remain")
				}
			}
			if tc.dirty && strings.TrimSpace(git(t, root, "rev-parse", "HEAD")) != base {
				t.Fatal("dirty merge mutated HEAD")
			}
			if tc.failTest {
				// A retry after fixing the test can finish cleanup without losing its test task record.
				if err := mergeWorktrees(r.ID, []string{w.Name}, "exit 0", false, false, &out, &errout); err != nil {
					t.Fatal(err)
				}
				fresh, _ = loadRun(r.ID)
				if len(fresh.Tasks) != 2 {
					t.Fatalf("test task records lost: %d", len(fresh.Tasks))
				}
			}
		})
	}
}

func TestGCWorktreesMergedAndCleanOnly(t *testing.T) {
	for _, tc := range []struct {
		name                string
		merged, dirty, auto bool
	}{
		{"merged-clean", true, false, true}, {"merged-dirty", true, true, true}, {"unmerged", false, false, true}, {"explicit", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, root, _ := worktreeFixture(t)
			w := fixtureWorktree(t, r, root, "impl")
			fixtureCommit(t, r, w, "new.txt", "worker")
			w.WorktreeAuto = tc.auto
			r.Status = "closed"
			saveFixture(t, r)
			if tc.merged {
				git(t, root, "merge", "--no-ff", "--no-edit", w.Branch)
			}
			if tc.dirty {
				putFile(t, w.Worktree, "untracked.txt", "keep me")
			}
			report := cleanupRunWorktrees(r, true)
			_, err := os.Stat(w.Cwd)
			removed := os.IsNotExist(err)
			if removed != (tc.merged && !tc.dirty && tc.auto) {
				t.Fatalf("removed=%v report=%v", removed, report)
			}
		})
	}
}

func TestAdhocPruneKeepsWorktreeRecords(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	fixtureWorktree(t, r, root, "impl")
	r.Adhoc = true
	r.Status = "closed"
	r.Created = time.Now().Add(-365 * 24 * time.Hour)
	saveFixture(t, r)
	if report := pruneAdhoc([]*Run{r}, time.Hour); len(report) != 0 {
		t.Fatal(report)
	}
	if _, err := loadRun(r.ID); err != nil {
		t.Fatal("worktree owner pruned", err)
	}
}

func TestDagWorktreeNone(t *testing.T) {
	dir := t.TempDir()
	n := DagNode{Cwd: dir, Worktree: "none"}
	if n.dir() != canonicalRoot(dir) || nodeWorktree(n.Worktree) != "" {
		t.Fatalf("none interpreted as path: %s", n.dir())
	}
}

func TestDoWorktreeExecution(t *testing.T) {
	for _, tc := range []struct {
		name                                      string
		disabled, explicit, outside, workerCommit bool
	}{
		{name: "automatic"}, {name: "explicit", explicit: true}, {name: "disabled", disabled: true},
		{name: "scope-violation", outside: true}, {name: "committed-scope-violation", outside: true, workerCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, root, _ := worktreeFixture(t)
			cleanModelEnv(t)
			r.Workers = []*Worker{{Name: "first", Role: "produce", Cwd: root, Pane: "p", Status: "prompted"}}
			saveFixture(t, r)
			t.Setenv("ORCH_RUN", r.ID)
			file := "owned.txt"
			if tc.outside {
				file = "outside.txt"
			}
			script := "echo worker > " + file + "\n"
			if tc.workerCommit {
				script += "git add -A\ngit commit -qm 'worker commit'\n"
			}
			script += "echo '{\"result\":\"SUMMARY: updated\"}'\n"
			fakeCLI(t, "claude", script)
			args := []string{"--model", "claude-medium-m", "--own", "owned.txt", "--cwd", root, "--json"}
			if tc.disabled {
				args = append(args, "--no-worktree")
			}
			if tc.explicit {
				args = append(args, "--worktree", filepath.Join(filepath.Dir(root), "explicit"))
			}
			args = append(args, "update owned file")
			var out, errout bytes.Buffer
			code := taskCommand("do", args, &out, &errout)
			var report map[string]any
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("%v out=%s err=%s", err, out.String(), errout.String())
			}
			if (code != 0) != tc.outside {
				t.Fatalf("code=%d out=%s err=%s", code, out.String(), errout.String())
			}
			fresh, _ := loadRun(r.ID)
			task := fresh.Tasks[0]
			if task.Committed != (!tc.disabled && !tc.outside) {
				t.Fatalf("committed=%v state=%+v", task.Committed, task)
			}
			if tc.disabled {
				if task.Worktree != "" {
					t.Fatal("disabled isolation")
				}
				return
			}
			if task.Branch == "" || task.Worktree == "" {
				t.Fatalf("missing worktree metadata: %+v", task)
			}
			if !tc.outside {
				if !strings.Contains(git(t, task.Worktree, "log", "-1", "--format=%s"), "orch: "+r.ID+"/"+task.ID) {
					t.Fatal("missing collect commit")
				}
				if err := mergeWorktrees(r.ID, []string{task.ID}, "", false, true, &out, &errout); err != nil {
					t.Fatal(err)
				}
				fresh, _ = loadRun(r.ID)
				if !fresh.Tasks[0].Merged || fresh.Tasks[0].Worktree != "" {
					t.Fatalf("task cleanup not saved: %+v", fresh.Tasks[0])
				}
				disk, err := readTask(r.ID, task.ID)
				if err != nil || !disk.Merged || disk.Worktree != "" {
					t.Fatalf("task.json mismatch: %+v %v", disk, err)
				}
			} else if !tc.workerCommit && strings.TrimSpace(git(t, task.Worktree, "log", "-1", "--format=%s")) != "base" {
				t.Fatal("scope violation committed")
			}
		})
	}
}

func TestFallbackWorktreeMetadata(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	w := fixtureWorktree(t, r, root, "impl")
	w.WorktreeStart = strings.TrimSpace(git(t, w.Worktree, "rev-parse", "HEAD"))
	saveFixture(t, r)
	state, err := spawnWorktree(r, spawnOpts{Name: "impl-f1", Fallback: w.Name, Cwd: w.Cwd, Worktree: w.Worktree}, io.Discard)
	if err != nil || state.Dir != w.Worktree || state.Branch != w.Branch || state.Base != root || !state.Auto || state.Start != w.WorktreeStart {
		t.Fatalf("fallback=%+v err=%v", state, err)
	}
	if count := strings.Count(git(t, root, "worktree", "list", "--porcelain"), "worktree "); count != 2 {
		t.Fatalf("fallback created a worktree: %d", count)
	}
}

func TestWorktreeSubdirectoryOwnership(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(root, "sub"), "file.txt", "base")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "subdirectory")
	r.Workers = []*Worker{{Name: "first", Role: "produce", Pane: "p", Status: "prompted", Cwd: root}}
	saveFixture(t, r)
	state, err := prepareWorktree(r.ID, "second", filepath.Join(root, "sub"), "", false, io.Discard)
	if err != nil || state.Cwd != filepath.Join(state.Dir, "sub") {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	task := &Task{Cwd: filepath.Join(root, "sub")}
	applyTaskWorktree(task, state)
	if task.Cwd != state.Cwd {
		t.Fatal(task.Cwd)
	}
	if got := rootGlobs(task.Cwd, []string{"file.txt"}); len(got) != 1 || got[0] != "sub/file.txt" {
		t.Fatal(got)
	}
}

func TestBackgroundWriterPIDPublication(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	task, err := newTaskIn(r.ID, "do", "", "fixture", root)
	if err != nil {
		t.Fatal(err)
	}
	task.Pid = 0
	if err := saveTask(task); err != nil {
		t.Fatal(err)
	}
	if len(liveWritersIn(root, nil)) != 1 {
		t.Fatal("fresh unclaimed task lost writer protection")
	}
	if err := recordTaskRunner(r.ID, task.ID, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if refs := liveWritersIn(root, nil); len(refs) != 1 || refs[0].Name != task.ID {
		t.Fatalf("runner not published: %+v", refs)
	}
	claimed, err := claimTask(r.ID, task.ID)
	if err != nil || claimed.Pid != os.Getpid() {
		t.Fatalf("claim failed: %+v %v", claimed, err)
	}
}

func TestConcurrentWriterAdmission(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	type result struct {
		state worktreeState
		err   error
	}
	results := make(chan result, 2)
	for _, name := range []string{"first", "second"} {
		go func(name string) {
			unlock, err := lockHead("writers")
			if err != nil {
				results <- result{err: err}
				return
			}
			defer unlock()
			state, err := prepareWorktree(r.ID, name, root, "", false, io.Discard)
			if err == nil {
				cwd := root
				if state.Dir != "" {
					cwd = state.Dir
				}
				err = withRun(r.ID, func(current *Run) error {
					current.Workers = append(current.Workers, &Worker{Name: name, Role: "produce", Pane: name, Status: "prompted", Cwd: cwd, Worktree: state.Dir, Branch: state.Branch, WorktreeBase: state.Base, WorktreeAuto: state.Auto})
					return nil
				})
			}
			results <- result{state, err}
		}(name)
	}
	autos := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.state.Auto {
			autos++
		}
	}
	if autos != 1 {
		t.Fatalf("expected exactly one isolated writer; got %d", autos)
	}
	fresh, _ := loadRun(r.ID)
	if len(fresh.Workers) != 2 || fresh.Workers[0].Cwd == fresh.Workers[1].Cwd {
		t.Fatalf("writers share a checkout: %+v", fresh.Workers)
	}
}

func TestMergeConflictLeavesLaterWorktreesUntouched(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	first := fixtureWorktree(t, r, root, "first")
	fixtureCommit(t, r, first, "owned.txt", "worker")
	second := fixtureWorktree(t, r, root, "second")
	fixtureCommit(t, r, second, "second.txt", "later")
	putFile(t, root, "owned.txt", "source")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "source")
	var out bytes.Buffer
	if err := mergeWorktrees(r.ID, nil, "", false, false, &out, io.Discard); err == nil {
		t.Fatal("conflict accepted")
	}
	if _, err := os.Stat(second.Worktree); err != nil {
		t.Fatal("later worktree removed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "second.txt")); !os.IsNotExist(err) {
		t.Fatal("later branch merged after conflict")
	}
}

func TestOtherHeadWarningAtRunCreation(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	r.HeadPane = "other-head"
	r.Workers = []*Worker{{Name: "other", Role: "produce", Pane: "p", Status: "prompted", Cwd: root}}
	saveFixture(t, r)
	t.Chdir(root)
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = old; f.Close() })
	created, err := createRun("new head", "light", "", "")
	os.Stderr = old
	if err != nil {
		t.Fatal(err)
	}
	warning, err := os.ReadFile(f.Name())
	if err != nil || !strings.Contains(string(warning), "Head other-head run "+r.ID) {
		t.Fatalf("warning=%s err=%v", warning, err)
	}
	var out bytes.Buffer
	state, err := spawnWorktree(created, spawnOpts{Name: "new", Cwd: root}, &out)
	if err != nil || !state.Auto || !strings.Contains(out.String(), "Head other-head run "+r.ID) {
		t.Fatalf("first writer=%+v warning=%s err=%v", state, out.String(), err)
	}
}

func TestMergeCleanupKeepsDirtyWorktree(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	w := fixtureWorktree(t, r, root, "impl")
	fixtureCommit(t, r, w, "new.txt", "committed")
	putFile(t, w.Worktree, "untracked.txt", "keep")
	var out bytes.Buffer
	if err := mergeWorktrees(r.ID, nil, "", false, false, &out, io.Discard); err == nil {
		t.Fatal("dirty worktree removed")
	}
	if _, err := os.Stat(filepath.Join(w.Worktree, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "show-ref", "--verify", "refs/heads/"+w.Branch)
	fresh, _ := loadRun(r.ID)
	if !fresh.worker(w.Name).Merged || fresh.worker(w.Name).Worktree == "" {
		t.Fatal("cleanup failure not recorded")
	}
}

func TestMergeCLIAndWorkerGuard(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	w := fixtureWorktree(t, r, root, "impl")
	fixtureCommit(t, r, w, "new.txt", "committed")
	// Exercise parsing without a live mux or caller ancestry lookup.
	t.Setenv("HERDR_PANE_ID", "")
	r.HeadPane = ""
	saveFixture(t, r)
	var out, errout bytes.Buffer
	if code := run([]string{"merge", w.Name, "--run", r.ID, "--test", "exit 0"}, strings.NewReader(""), &out, &errout); code != 0 {
		t.Fatalf("code=%d output=%s stderr=%s", code, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), "merged: "+w.Branch) || !strings.Contains(out.String(), "removed: "+w.Worktree) {
		t.Fatalf("output=%s", out.String())
	}
	fresh, _ := loadRun(r.ID)
	if len(fresh.Tasks) != 1 || fresh.Tasks[0].Kind != "test" {
		t.Fatalf("test did not use requested run: %+v", fresh.Tasks)
	}
	t.Setenv("ORCH_ROLE", "worker")
	errout.Reset()
	out.Reset()
	if code := run([]string{"merge", "--run", r.ID}, strings.NewReader(""), &out, &errout); code == 0 || !strings.Contains(errout.String(), "Head only") {
		t.Fatalf("worker accepted: code=%d err=%s", code, errout.String())
	}
}

func TestWorktreeOwnedGlobSymlink(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	w := fixtureWorktree(t, r, root, "impl")
	alias := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	body := "- Owned files (glob): " + filepath.Join(alias, "*.txt")
	if bad := repositoryScopeViolations(w.Cwd, w.Worktree, []string{"owned.txt"}, workerOwnedGlobs(w, body)); len(bad) > 0 {
		t.Fatalf("symlink source ownership was not mapped into worktree: %v", bad)
	}
}

func TestDoWorktreeFinalTestCannotCommitOutsideScope(t *testing.T) {
	r, root, base := worktreeFixture(t)
	cleanModelEnv(t)
	r.Workers = []*Worker{{Name: "first", Role: "produce", Cwd: root, Pane: "p", Status: "prompted"}}
	saveFixture(t, r)
	t.Setenv("ORCH_RUN", r.ID)
	fakeCLI(t, "claude", "echo worker > owned.txt\necho '{\"result\":\"SUMMARY: updated\"}'\n")
	var out, errout bytes.Buffer
	args := []string{"--model", "claude-medium-m", "--cwd", root, "--own", "owned.txt", "--test", "echo generated > outside.txt", "change owned file"}
	if code := taskCommand("do", args, &out, &errout); code == 0 {
		t.Fatalf("test output escaped scope: %s", out.String())
	}
	fresh, _ := loadRun(r.ID)
	task := fresh.Tasks[0]
	if task.Committed || len(task.ScopeViolations) != 1 || task.ScopeViolations[0] != "outside.txt" {
		t.Fatalf("state=%+v", task)
	}
	if hash := strings.TrimSpace(git(t, task.Worktree, "rev-parse", "HEAD")); hash != base {
		t.Fatal("out-of-scope test output committed")
	}
}

func TestTestsNeverAddWorktreesToTheRealCheckout(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if worktreeRootAllowed(canonicalRoot(wd)) {
		t.Fatalf("test guard allows worktrees in %s", wd)
	}
	if !worktreeRootAllowed(canonicalRoot(t.TempDir())) {
		t.Fatal("test guard blocks temp repositories")
	}
}
