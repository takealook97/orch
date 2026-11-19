package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// N1: a local branch named origin/main must not stand in for the fetched remote branch.
func TestRefreshIgnoresLocalBranchNamedOriginMain(t *testing.T) {
	repo, remote := refreshRepo(t)
	stubRefresh(t, nil)
	published := gitT(t, remote, "rev-parse", "main")
	gitT(t, repo, "switch", "-qc", "topic")
	if err := os.WriteFile(filepath.Join(repo, "local-only.txt"), []byte("unpublished"), 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", "local-only.txt")
	gitT(t, repo, "commit", "-qm", "topic work")
	gitT(t, repo, "branch", "origin/main", "HEAD")
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if parent := gitT(t, remote, "rev-parse", "main^"); parent != published {
		t.Fatalf("published on top of %s, want origin's %s\n%s", parent, published, &out)
	}
	if files := gitT(t, remote, "ls-tree", "--name-only", "main"); strings.Contains(files, "local-only.txt") {
		t.Fatalf("unpublished local file reached origin: %s", files)
	}
}

// N2: a worktree add that fails after Git registered the worktree is removed again.
func TestRefreshWorktreeAddFailureCleansUp(t *testing.T) {
	repo, _ := refreshRepo(t)
	stubRefresh(t, nil)
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 42\n"), 0700); err != nil {
		t.Fatal(err)
	}
	err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo})
	if err == nil || !strings.Contains(err.Error(), "exit status 42") {
		t.Fatalf("err = %v", err)
	}
	assertNoRefreshLeftovers(t, repo)
}

// N3: a failed worktree removal is returned, not only printed.
func TestRefreshReturnsWorktreeRemoveError(t *testing.T) {
	repo, _ := refreshRepo(t)
	stubRefresh(t, nil)
	inner := refreshHooks.run
	refreshHooks.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 1 && args[0] == "worktree" && args[1] == "remove" {
			return "", errors.New("simulated worktree remove error")
		}
		return inner(ctx, dir, name, args...)
	}
	err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo})
	if err == nil || !strings.Contains(err.Error(), "simulated worktree remove error") {
		t.Fatalf("cleanup error lost: %v", err)
	}
	// Remove what the injected failure left so the temp directories can be deleted.
	for _, line := range strings.Split(gitT(t, repo, "worktree", "list", "--porcelain"), "\n") {
		if wt, ok := strings.CutPrefix(line, "worktree "); ok && wt != gitT(t, repo, "rev-parse", "--show-toplevel") {
			gitT(t, repo, "worktree", "remove", "--force", wt)
		}
	}
}

// N4: the read-only checkout gate does not rewrite the user's index stat cache.
func TestRefreshSyncLeavesIndexUntouched(t *testing.T) {
	repo, _ := refreshRepo(t)
	stubRefresh(t, nil)
	gitT(t, repo, "switch", "-qc", "topic")
	later := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo, "models.yaml"), later, later); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) || !strings.Contains(out.String(), "not default") {
		t.Fatalf("index changed=%v err=%v out=%s", !bytes.Equal(before, after), err, &out)
	}
}
