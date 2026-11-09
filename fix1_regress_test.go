package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Crashed Head: active run, Head pane gone, worker pane still open.
func TestGCOrphanDeadHeadActive(t *testing.T) {
	now := pruneEnv(t)
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "current-head")
	t.Setenv("HC_TERMINAL_ID", "")
	r := pruneRun(t, "crashed-run", now)
	r.Status, r.Backend, r.HeadPane = "active", "herdr", "dead-head"
	r.Workers = []*Worker{{Name: "w", Status: "working", Pane: "wpane"}}
	pruneSaveRun(t, r, now)
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	closed := false
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pane" && args[1] == "close" {
			closed = true
			return []byte("{}"), nil
		}
		if len(args) >= 2 && args[0] == "pane" && args[1] == "list" {
			if closed {
				return []byte(`{"result":{"panes":[{"pane_id":"current-head"}]}}`), nil
			}
			return []byte(`{"result":{"panes":[{"pane_id":"current-head"},{"pane_id":"wpane"}]}}`), nil
		}
		return []byte("{}"), nil
	}
	report, err := gc(true)
	saved, _ := loadRun(r.ID)
	t.Logf("err=%v status=%s closed=%v report=%v", err, saved.Status, closed, report)
	if !closed || saved.Status != "closed" {
		t.Fatalf("orphan active run of dead Head not cleaned")
	}
}

// Explicit contract base older than HEAD at prompt time: files committed between base and
// prompt (e.g. Head merges) are not the worker's changes.
func TestPaneExplicitBaseAdvancedBeforePrompt(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	putFile(t, root, "outside.txt", "head merge\n")
	git(t, root, "add", "outside.txt")
	git(t, root, "commit", "-qm", "head merged other work before prompt")
	w := fixtureWorktree(t, r, root, "impl")
	w = paneScopePrompt(t, r, w, base)
	putFile(t, w.Cwd, "owned.txt", "worker\n")
	w.Pane, w.Status = "", "closed"
	r, _ = loadRun(r.ID)
	r.Workers = []*Worker{w}
	saveFixture(t, r)
	_ = os.WriteFile(w.Result, []byte("- status: done\n"), 0600)
	out, status, err := collectWorker(r.ID, w.Name)
	t.Logf("status=%s err=%v out=%s", status, err, out)
	if status != "done" {
		t.Fatalf("false positive scope violation")
	}
}

func TestGCKeepsClosedRunMtime(t *testing.T) {
	now := pruneEnv(t)
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "current-head")
	t.Setenv("HC_TERMINAL_ID", "")
	old := now.Add(-10 * 24 * time.Hour)
	r := pruneRun(t, "old-closed", old)
	r.Adhoc = true
	pruneSaveRun(t, r, old)
	oldRun := herdrRun
	t.Cleanup(func() { herdrRun = oldRun })
	herdrRun = func(args ...string) ([]byte, error) { return []byte(`{"result":{"panes":[]}}`), nil }
	report, err := gc(false)
	info, statErr := os.Stat(filepath.Join(r.dir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if os.IsNotExist(statErr) {
		return
	}
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.ModTime().After(old.Add(time.Hour)) {
		t.Fatalf("gc refreshed old run: %v; report=%v", info.ModTime(), report)
	}
}

func finishFix1Pane(t *testing.T, r *Run, w *Worker, want string) {
	t.Helper()
	w.Pane, w.Status = "", "closed"
	r.Workers = []*Worker{w}
	saveFixture(t, r)
	if err := os.WriteFile(w.Result, []byte("- status: done\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, status, err := collectWorker(r.ID, w.Name)
	if err != nil || status != want {
		t.Fatalf("status=%s want=%s err=%v output=%s", status, want, err, out)
	}
}
func TestPaneSharedCheckoutHeadCommit(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	w := paneScopePrompt(t, r, &Worker{Name: "impl", Cwd: root}, "none")
	putFile(t, root, "outside.txt", "Head change\n")
	git(t, root, "add", "outside.txt")
	git(t, root, "commit", "-qm", "Head commit during task")
	putFile(t, root, "owned.txt", "worker change\n")
	finishFix1Pane(t, r, w, "done")
}
func TestPaneWorktreeCommitOutOfScope(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	w := paneScopePrompt(t, r, fixtureWorktree(t, r, root, "impl"), "none")
	putFile(t, w.Cwd, "outside.txt", "worker change\n")
	git(t, w.Cwd, "add", "outside.txt")
	git(t, w.Cwd, "commit", "-qm", "worker outside commit")
	finishFix1Pane(t, r, w, "failed")
}
func TestUnbornHeadWithOtherRefs(t *testing.T) {
	for _, mode := range []string{"do", "pane"} {
		t.Run(mode, func(t *testing.T) {
			r, root, _ := worktreeFixture(t)
			git(t, root, "checkout", "--orphan", "unborn")
			git(t, root, "rm", "-r", "--cached", ".")
			if mode == "do" {
				taskEnv(t)
				runCommittedScopeViolation(t, root, "printf 'outside\\n' > outside.txt\ngit add outside.txt\ngit commit -qm outside", "outside.txt")
				return
			}
			// Recorded orch worktree metadata enables committed attribution; the orphan
			// checkout retains refs, exercising HEAD state rather than repository emptiness.
			w := &Worker{Name: "impl", Cwd: root, Worktree: root, Branch: "unborn"}
			w = paneScopePrompt(t, r, w, "none")
			putFile(t, root, "outside.txt", "outside\n")
			git(t, root, "add", "outside.txt")
			git(t, root, "commit", "-qm", "outside")
			finishFix1Pane(t, r, w, "failed")
		})
	}
}
func TestOverflowCleanupErrorStillReturned(t *testing.T) {
	r, f := overflowFixture(t)
	tab := "fix1-close-error"
	overflowCloseWarnings.Delete(tab)
	t.Cleanup(func() { overflowCloseWarnings.Delete(tab) })
	r.OverflowTabs = []string{tab}
	f.tabs[tab] = nil
	saveFixture(t, r)
	original := herdrRun
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "tab" && args[1] == "close" {
			return nil, fmt.Errorf("close failed")
		}
		return original(args...)
	}
	// Warning entry point must dedupe output without hiding the cleanup error.
	oldStderr := os.Stderr
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = wr
	t.Cleanup(func() { os.Stderr = oldStderr; rd.Close(); wr.Close() })
	for i := 0; i < 2; i++ {
		if err := cleanRunOverflowWarning(r.ID); err != nil {
			t.Fatal(err)
		}
		if err := cleanRunOverflow(r.ID); err == nil {
			t.Errorf("attempt %d hid cleanup error", i)
		}
	}
	wr.Close()
	os.Stderr = oldStderr
	var output bytes.Buffer
	output.ReadFrom(rd)
	if strings.Count(output.String(), "orch: warning: overflow cleanup:") != 1 {
		t.Fatalf("warnings: %s", output.String())
	}
}
func TestReviewContractMissingWorktreeFallsBack(t *testing.T) {
	r, repo := reviewFixture(t)
	r.Workers[0].Worktree = filepath.Join(t.TempDir(), "removed-worktree")
	body, err := reviewContract(r, "missing-worktree", "verify-critical", "codex-high-l", reviewContractOptions{Reviews: "author", Diff: "HEAD~1..HEAD"})
	if err != nil || !strings.Contains(body, "Repository: "+repo) {
		t.Fatalf("err=%v body=%s", err, body)
	}
}

// Pane cleanup persists state, so disk retention must run before it refreshes
// an old closed run owned by this Head.
func TestGCCompactsOldOwnedClosedRun(t *testing.T) {
	now := pruneEnv(t)
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "current-head")
	t.Setenv("HC_TERMINAL_ID", "")
	old := now.Add(-10 * 24 * time.Hour)
	r := pruneRun(t, "old-owned", old)
	r.Backend, r.HeadPane = "herdr", "current-head"
	pruneSaveRun(t, r, old)
	original := herdrRun
	t.Cleanup(func() { herdrRun = original })
	herdrRun = func(args ...string) ([]byte, error) {
		return []byte(`{"result":{"panes":[{"pane_id":"current-head"}]}}`), nil
	}
	report, err := gc(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir(), "evidence", "blob.bin")); !os.IsNotExist(err) {
		t.Fatalf("old evidence not compacted: %v report=%v", err, report)
	}
}
