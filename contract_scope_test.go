package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func scopeRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.name", "test")
	git(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "owned.txt"), []byte(dir), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	return strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
}

func TestScopeRepositoryWorkspace(t *testing.T) {
	for _, rootGit := range []bool{false, true} {
		t.Run(fmt.Sprint(rootGit), func(t *testing.T) {
			cwd := t.TempDir()
			if rootGit {
				scopeRepo(t, cwd)
			}
			child := filepath.Join(cwd, "b2b")
			base := scopeRepo(t, child)
			body := "- base commit: " + base
			root, err := scopeRepository(cwd, body)
			if err != nil || root != repoRoot(child) {
				t.Fatalf("root=%s err=%v", root, err)
			}
			os.WriteFile(filepath.Join(child, "owned.txt"), []byte("changed"), 0600)
			os.WriteFile(filepath.Join(child, "extra.txt"), []byte("untracked"), 0600)
			changed, err := changedFiles(root, base)
			if err != nil || !slices.Contains(changed, "owned.txt") || !slices.Contains(changed, "extra.txt") {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			for _, glob := range []string{"b2b/owned.txt", filepath.Join(child, "owned.txt")} {
				bad := repositoryScopeViolations(cwd, root, changed, []string{glob})
				if len(bad) != 1 || bad[0] != "extra.txt" {
					t.Fatalf("glob %s: %v", glob, bad)
				}
			}
			if bad := repositoryScopeViolations(cwd, root, changed, []string{"**"}); len(bad) > 0 {
				t.Fatalf("workspace wildcard: %v", bad)
			}
		})
	}
}

func TestScopeRepositoryPathsAndDepth(t *testing.T) {
	cwd := t.TempDir()
	child := filepath.Join(cwd, "repos", "b2b")
	base := scopeRepo(t, child)
	if root, err := scopeRepository(cwd, "- base commit: "+base); err != nil || root != repoRoot(child) {
		t.Fatalf("depth two: %s %v", root, err)
	}
	deep := filepath.Join(cwd, "a", "b", "deep")
	deepBase := scopeRepo(t, deep)
	if _, err := scopeRepository(cwd, "- base commit: "+deepBase); err == nil {
		t.Fatal("searched beyond depth two")
	}
	if root, err := scopeRepository(cwd, "- base commit: "+deepBase+"\n- worktree: "+deep); err != nil || root != repoRoot(deep) {
		t.Fatalf("explicit path: %s %v", root, err)
	}
	spaced := filepath.Join(t.TempDir(), "work tree")
	spacedBase := scopeRepo(t, spaced)
	if root, err := scopeRepository(cwd, "- base commit: "+spacedBase+"\n- worktree: `"+spaced+"`"); err != nil || root != repoRoot(spaced) {
		t.Fatalf("quoted explicit path: %s %v", root, err)
	}
	// A unique contract path wins before cwd discovery.
	priority := filepath.Join(t.TempDir(), "priority")
	git(t, child, "worktree", "add", "--detach", priority, base)
	if root, err := scopeRepository(child, "- base commit: "+base+"\n- worktree: "+priority); err != nil || root != repoRoot(priority) {
		t.Fatalf("explicit path priority: %s %v", root, err)
	}
	if _, err := scopeRepository(cwd, "- base commit: "+strings.Repeat("f", 40)); err == nil {
		t.Fatal("missing commit accepted")
	}
	// An explicit worktree takes precedence over a child sharing its commit.
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, child, "worktree", "add", "--detach", wt, base)
	if root, err := scopeRepository(cwd, "- base commit: "+base+"\n- worktree: "+wt); err != nil || root != repoRoot(wt) {
		t.Fatalf("explicit linked worktree: %s %v", root, err)
	}
	// Discovery must also accept .git files in depth-two linked worktrees.
	discovery := t.TempDir()
	linked := filepath.Join(discovery, "repos", "wt")
	os.MkdirAll(filepath.Dir(linked), 0700)
	git(t, child, "worktree", "add", "--detach", linked, base)
	if root, err := scopeRepository(discovery, "- base commit: "+base); err != nil || root != repoRoot(linked) {
		t.Fatalf("discovered linked worktree: %s %v", root, err)
	}
}

func TestScopeRepositoryRejectsAmbiguousSiblingRepositories(t *testing.T) {
	cwd := t.TempDir()
	first := filepath.Join(cwd, "repo-a")
	base := scopeRepo(t, first)
	second := filepath.Join(cwd, "repo-b")
	if out, err := exec.Command("git", "clone", "-q", first, second).CombinedOutput(); err != nil {
		t.Fatalf("clone sibling fixture: %v %s", err, out)
	}
	if got := strings.TrimSpace(git(t, second, "rev-parse", "HEAD")); got != base {
		t.Fatalf("fixture commits differ: first=%s second=%s", base, got)
	}
	root, err := scopeRepository(cwd, "- base commit: "+base)
	if err == nil || root != "" {
		t.Fatalf("ambiguous repositories accepted: root=%s err=%v", root, err)
	}
	for _, want := range []string{repoRoot(first), repoRoot(second), "계약 Baseline 에 작업 저장소 절대 경로를 적어라"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguity error %q missing %q", err, want)
		}
	}
	// A single explicit absolute path resolves the otherwise ambiguous pair.
	root, err = scopeRepository(cwd, "- base commit: "+base+"\n- worktree: `"+second+"`")
	if err != nil || root != repoRoot(second) {
		t.Fatalf("explicit absolute path did not resolve ambiguity: root=%s err=%v", root, err)
	}
}

func TestCollectWorkspaceScope(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprint(outside), func(t *testing.T) {
			t.Setenv("ORCH_HOME", t.TempDir())
			t.Setenv("ORCH_RUN", "")
			t.Setenv("HERDR_PANE_ID", "w1:p1")
			cwd := t.TempDir()
			scopeRepo(t, cwd)
			child := filepath.Join(cwd, "b2b")
			base := scopeRepo(t, child)
			os.WriteFile(filepath.Join(child, "existing.txt"), []byte("preexisting"), 0600)
			files, _ := changedFiles(child, base)
			baseline := snapshot(child, files)
			os.WriteFile(filepath.Join(child, "owned.txt"), []byte("changed"), 0600)
			if outside {
				os.WriteFile(filepath.Join(child, "extra.txt"), []byte("outside"), 0600)
			}
			r, err := createRun("x", "light", "", "")
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(r.resultPath("a"), []byte("- status: done\n"), 0600)
			os.WriteFile(r.contractPath("a"), []byte("- base commit: "+base+"\n- Owned files (glob): "+filepath.Join(child, "owned.txt")), 0600)
			if err := withRun(r.ID, func(r *Run) error {
				r.Workers = append(r.Workers, &Worker{Name: "a", Cwd: cwd, Status: "closed", Contract: r.contractPath("a"), Result: r.resultPath("a"), Baseline: baseline, BaselineRoot: repoRoot(child)})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			out, status, err := collectWorker(r.ID, "a")
			want := "done"
			if outside {
				want = "failed"
			}
			if err != nil || status != want || !strings.Contains(out, "scope checked in repository: "+repoRoot(child)) || strings.Contains(out, "existing.txt") {
				t.Fatalf("status=%s err=%v output=%s", status, err, out)
			}
		})
	}
}

func TestPromptContractHash(t *testing.T) {
	body := []byte("original contract")
	w := &Worker{Name: "a"}
	if err := checkPromptContract(w, body); err != nil {
		t.Fatal(err)
	}
	w.Prompted = time.Now()
	w.ContractHash = contractHash(body)
	if err := checkPromptContract(w, body); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{w.ContractHash, ""} {
		w.ContractHash = hash
		err := checkPromptContract(w, []byte("revised contract"))
		if err == nil || !strings.Contains(err.Error(), "new name") || !strings.Contains(err.Error(), "close a") {
			t.Fatalf("revised/unknown hash accepted: %v", err)
		}
	}
}

func TestPromptRefusesChangedContractBeforeSending(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, err := createRun("x", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(contractTemplate(r, "a", "produce", "codex-high-m"))
	os.WriteFile(r.contractPath("a"), body, 0600)
	stamp := time.Now().Add(-time.Minute)
	withRun(r.ID, func(r *Run) error {
		r.Workers = append(r.Workers, &Worker{Name: "a", Prompted: stamp, Status: "prompted", ContractHash: contractHash(body), Baseline: map[string]string{"existing.txt": "original"}})
		return nil
	})
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	herdrRun = func(args ...string) ([]byte, error) {
		t.Fatalf("unexpected Herdr call: %v", args)
		return nil, nil
	}
	if err := prompt(r.ID, "a", "", false, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := loadRun(r.ID)
	if !got.worker("a").Prompted.Equal(stamp) || got.worker("a").Baseline["existing.txt"] != "original" {
		t.Fatal("idempotent prompt reset baseline or timestamp")
	}
	os.WriteFile(r.contractPath("a"), append(body, []byte("\nrevised")...), 0600)
	if err := prompt(r.ID, "a", "", false, 0); err == nil {
		t.Fatal("changed contract accepted")
	}
	// --contract must be checked before replacing the original on disk.
	os.WriteFile(r.contractPath("a"), body, 0600)
	source := filepath.Join(t.TempDir(), "revised.md")
	os.WriteFile(source, []byte("revised"), 0600)
	if err := prompt(r.ID, "a", source, false, 0); err == nil {
		t.Fatal("changed external contract accepted")
	}
	current, _ := os.ReadFile(r.contractPath("a"))
	if string(current) != string(body) {
		t.Fatal("rejected contract replaced original")
	}
}

func TestFirstPromptSavesHashAndWorkspaceBaseline(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	cwd := t.TempDir()
	child := filepath.Join(cwd, "b2b")
	base := scopeRepo(t, child)
	if err := os.WriteFile(filepath.Join(child, "dirty.txt"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := createRun("x", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(contractTemplate(r, "a", "produce", "codex-high-m"), "<hash or none>", base, 1)
	if err := os.WriteFile(r.contractPath("a"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := withRun(r.ID, func(r *Run) error {
		r.Workers = append(r.Workers, &Worker{Name: "a", Cwd: cwd, Pane: "w1:p2", Result: r.resultPath("a"), Status: "spawned"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldHerd, oldTimings := herdrRun, timings
	t.Cleanup(func() { herdrRun, timings = oldHerd, oldTimings })
	timings.TrustSettlePrompt = 0
	timings.KeyGap = 0
	// Emulate delivery entirely in memory: no real pane or agent is used.
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "agent" && args[1] == "send-keys" {
			return nil, os.WriteFile(r.resultPath("a"), []byte("- status: done\n"), 0600)
		}
		return []byte(`{"result":{"agent":{"agent_status":"idle"}}}`), nil
	}
	if err := prompt(r.ID, "a", "", false, 0); err != nil {
		t.Fatal(err)
	}
	got, err := loadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	w := got.worker("a")
	if w.ContractHash != contractHash([]byte(body)) || w.Prompted.IsZero() || w.BaselineRoot != repoRoot(child) || w.Baseline["dirty.txt"] != fileHash(repoRoot(child), "dirty.txt") {
		t.Fatalf("first prompt state: %+v", w)
	}
}
