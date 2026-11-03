package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustLoad(t *testing.T, id string) *Run {
	t.Helper()
	r, err := loadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// An explicit --worktree that already exists is the user's own checkout: the executor must not
// record a branch, commit its unrelated files, merge its branch or remove anything.
func TestExistingExplicitWorktreeStaysPlainCwd(t *testing.T) {
	r, root, base := worktreeFixture(t)
	userWT := filepath.Join(filepath.Dir(root), "user-feature")
	git(t, root, "worktree", "add", "-q", "-b", "feature", userWT, "HEAD")
	putFile(t, userWT, "notes.txt", "user's private uncommitted notes")

	state, err := prepareWorktree(r.ID, "impl", root, userWT, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if state.Dir != userWT || state.Branch != "" || state.Auto || state.Start != "" {
		t.Fatalf("existing worktree adopted: %+v", state)
	}
	w := &Worker{Name: "impl", Role: "produce", Status: "closed", Cwd: state.Dir, Worktree: state.Dir,
		Branch: state.Branch, WorktreeBase: state.Base, WorktreeStart: state.Start, WorktreeAuto: state.Auto}
	w.Contract, w.Result = r.contractPath(w.Name), r.resultPath(w.Name)
	body := "- base commit: " + base + "\n- Owned files (glob): owned.txt\n"
	if err := os.WriteFile(w.Contract, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.Result, []byte("# Result\n- summary: x\n- status: done\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rootWT, _ := workerScopeRepository(w, body)
	files, _ := changedFiles(rootWT, workerScopeBase(w, body))
	w.Baseline, w.BaselineRoot = snapshot(rootWT, files), rootWT
	r.Workers = append(r.Workers, w)
	saveFixture(t, r)

	putFile(t, userWT, "owned.txt", "worker change")
	head := strings.TrimSpace(git(t, userWT, "rev-parse", "HEAD"))
	out, _, err := collectWorker(r.ID, "impl")
	if err != nil {
		t.Fatalf("collect: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(git(t, userWT, "rev-parse", "HEAD")); got != head {
		t.Fatalf("committed on the user's branch: %s\n%s", got, out)
	}
	if !strings.Contains(git(t, userWT, "status", "--porcelain"), "notes.txt") {
		t.Fatal("user's uncommitted notes.txt vanished")
	}
	if e := worktreeEntries(mustLoad(t, r.ID)); len(e) != 0 {
		t.Fatalf("plain cwd became a merge/cleanup candidate: %+v", e)
	}
	var o bytes.Buffer
	if err := mergeWorktrees(r.ID, nil, "", false, false, &o, io.Discard); err == nil {
		t.Fatalf("merge picked up the user's worktree: %s", o.String())
	}
	cleanupRunWorktrees(mustLoad(t, r.ID), false)
	cleanupRunWorktrees(mustLoad(t, r.ID), true)
	if _, err := os.Stat(userWT); err != nil {
		t.Fatalf("user's worktree removed: %v", err)
	}
	if _, err := gitOutput(root, "show-ref", "--verify", "refs/heads/feature"); err != nil {
		t.Fatal("user's branch deleted")
	}
}

// A path that does not exist yet is created by the executor, so it owns it.
func TestNewExplicitWorktreeIsOwned(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	dir := filepath.Join(filepath.Dir(root), "fresh")
	state, err := prepareWorktree(r.ID, "impl", root, dir, false, io.Discard)
	if err != nil || state.Branch == "" || state.Start == "" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	r.Workers = append(r.Workers, &Worker{Name: "impl", Worktree: state.Dir, Branch: state.Branch, WorktreeBase: root})
	if len(worktreeEntries(r)) != 1 {
		t.Fatal("created worktree is not managed")
	}
}

// A merge must not move HEAD under a writer that still works in the source checkout.
func TestMergeRefusesLiveWriterInSourceCheckout(t *testing.T) {
	r, root, base := worktreeFixture(t)
	a := &Worker{Name: "a", Role: "produce", Status: "prompted", Pane: "live", Cwd: root}
	a.Contract, a.Result = r.contractPath("a"), r.resultPath("a")
	body := "- base commit: " + base + "\n- Owned files (glob): owned.txt\n"
	if err := os.WriteFile(a.Contract, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	r.Workers = append(r.Workers, a)
	saveFixture(t, r)
	b := fixtureWorktree(t, r, root, "b")
	fixtureCommit(t, r, b, "b.txt", "b work")

	var o bytes.Buffer
	err := mergeWorktrees(r.ID, []string{"b"}, "", false, false, &o, io.Discard)
	if err == nil || !strings.Contains(err.Error(), r.ID+"/a") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("merge under live writer: err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "b.txt")); statErr == nil {
		t.Fatal("refused merge still changed the checkout")
	}
	if err := mergeWorktrees(r.ID, []string{"b"}, "", false, true, &o, io.Discard); err != nil {
		t.Fatalf("--force merge: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "b.txt")); statErr != nil {
		t.Fatal("forced merge did not land b.txt")
	}
}

func TestFallbackKeepsOldWorkerPlacement(t *testing.T) {
	t.Run("shared checkout stays shared", func(t *testing.T) {
		r, root, _ := worktreeFixture(t)
		r.Workers = []*Worker{
			{Name: "other", Role: "produce", Pane: "live", Status: "prompted", Cwd: root},
			{Name: "old", Role: "produce", Pane: "live", Status: "failed", Cwd: root},
		}
		saveFixture(t, r)
		state, err := spawnWorktree(r, spawnOpts{Name: "old-2", Cwd: root, Fallback: "old"}, io.Discard)
		if err != nil || state != (worktreeState{}) {
			t.Fatalf("fallback was given a new placement: %+v err=%v", state, err)
		}
		auto := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-wt-"+r.ID[len(r.ID)-6:]+"-old-2")
		if _, err := os.Stat(auto); err == nil {
			t.Fatal("auto worktree created for fallback")
		}
	})
	t.Run("worktree is inherited", func(t *testing.T) {
		r, root, _ := worktreeFixture(t)
		old := fixtureWorktree(t, r, root, "old")
		old.Pane, old.Status, old.WorktreeStart = "live", "failed", "start-sha"
		saveFixture(t, r)
		state, err := spawnWorktree(r, spawnOpts{Name: "old-2", Cwd: old.Cwd, Worktree: old.Worktree, Fallback: "old"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if state.Dir != old.Worktree || state.Branch != old.Branch || !state.Auto || state.Base != root || state.Start != "start-sha" || state.Cwd != old.Cwd {
			t.Fatalf("state=%+v old=%+v", state, old)
		}
	})
}

func TestStalePaneWritersAreNotLive(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	r.HeadPane = "other-head"
	r.Workers = []*Worker{
		{Name: "alive", Role: "produce", Pane: "p-alive", Status: "prompted", Cwd: root},
		{Name: "dead-pane", Role: "produce", Pane: "p-dead", Status: "prompted", Cwd: root},
		{Name: "lookup-fails", Role: "integrate", Pane: "p-err", Status: "prompted", Cwd: root},
		{Name: "alive-2", Role: "produce", Pane: "p-alive", Status: "prompted", Cwd: root},
	}
	saveFixture(t, r)
	calls := map[string]int{}
	stubPaneAlive(t, func(id string) (bool, error) {
		calls[id]++
		switch id {
		case "p-dead":
			return false, nil
		case "p-err":
			return false, fmt.Errorf("herdr unreachable")
		}
		return true, nil
	})
	names := func() string {
		var n []string
		for _, ref := range liveWritersIn(root, nil) {
			n = append(n, ref.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names(); got != "alive,lookup-fails,alive-2" {
		t.Fatalf("live writers=%s", got)
	}
	if calls["p-alive"] != 1 || calls["other-head"] != 1 {
		t.Fatalf("lookups are not cached per call: %v", calls)
	}
	// Once the run's Head is gone, none of its pane writers count.
	stubPaneAlive(t, func(id string) (bool, error) { return id != "other-head", nil })
	if got := names(); got != "" {
		t.Fatalf("writers of a vanished Head still live: %s", got)
	}
}

func TestOtherBackendPaneWriterIsLiveButUnverified(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	r.Backend, r.HeadPane = "app", "other-head"
	r.Workers = []*Worker{{Name: "w", Role: "produce", Pane: "p", Status: "prompted", Cwd: root}}
	saveFixture(t, r)
	stubPaneAlive(t, func(id string) (bool, error) {
		t.Errorf("looked up %s on a foreign backend", id)
		return false, nil
	})
	refs := liveWritersIn(root, nil)
	if len(refs) != 1 || !refs[0].Unverified {
		t.Fatalf("refs=%+v", refs)
	}
	var out bytes.Buffer
	warnOtherHeads(&Run{HeadPane: "my-head"}, root, &out)
	if !strings.Contains(out.String(), "unverified") {
		t.Fatalf("warning=%s", out.String())
	}
}
