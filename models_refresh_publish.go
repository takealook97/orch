package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// refreshModels updates models.yaml aliases to the newest CLI-listed ids that answer a live probe.
// It never edits the user's checkout: the change is made, tested, committed and pushed from a
// detached temporary worktree at origin's default branch, then the checkout follows through
// the normal `orch update` path (fast-forward when safe, then reinstall).
func refreshModels(ctx context.Context, opts modelsRefreshOptions) (err error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	repo := opts.Repo
	if repo == "" {
		repo = "."
	}
	def, err := remoteDefaultBranch(ctx, repo)
	if err != nil {
		return err
	}
	if opts.DryRun {
		return refreshDryRun(ctx, repo, def, out)
	}
	if _, err := gitOut(ctx, repo, "fetch", "--quiet", "origin", "+refs/heads/"+def+":refs/remotes/origin/"+def); err != nil {
		return err
	}
	// The full ref name cannot resolve to a local branch called origin/<def>.
	base, err := gitOut(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+def+"^{commit}")
	if err != nil {
		return err
	}
	wt, err := refreshWorktree(ctx, repo, base)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, removeRefreshWorktree(ctx, repo, wt)) }()
	pushed, err := refreshInWorktree(ctx, wt, def, out)
	if err != nil || !pushed {
		return err
	}
	outcome, err := refreshHooks.sync(ctx, repo)
	if err != nil {
		return fmt.Errorf("pushed to origin/%s but updating %s failed (orch update retries): %w", def, repo, err)
	}
	fmt.Fprintf(out, "checkout: %s\n", outcome)
	return nil
}

// refreshDryRun reads origin through ls-remote and the checkout's models.yaml.
// It fetches nothing, makes no live probe and writes no file or ref.
func refreshDryRun(ctx context.Context, repo, def string, out io.Writer) error {
	head, err := updateGit(ctx, repo, "ls-remote", "origin", "refs/heads/"+def)
	if err != nil {
		return err
	}
	sha, _, _ := strings.Cut(head, "\t")
	fmt.Fprintf(out, "dry run: origin/%s is at %s; candidates come from %s/models.yaml; probes are not run\n", def, short(sha), repo)
	_, models, err := readRepoModels(repo)
	if err != nil {
		return err
	}
	if len(discoverChanges(ctx, models, out)) == 0 {
		fmt.Fprintln(out, "no model id candidates")
	}
	return nil
}

// refreshWorktree adds a detached worktree at rev under the temp directory.
// A failed add can still leave a registered, populated worktree, so it is removed too.
func refreshWorktree(ctx context.Context, repo, rev string) (string, error) {
	dir, err := os.MkdirTemp("", "orch-models-refresh-")
	if err != nil {
		return "", err
	}
	if _, err := gitOut(ctx, repo, "worktree", "add", "--quiet", "--detach", dir, rev); err != nil {
		return "", errors.Join(err, removeRefreshWorktree(ctx, repo, dir))
	}
	return dir, nil
}

// removeRefreshWorktree unregisters and deletes dir. Git refuses to remove a directory it
// never registered; that leftover is empty and is deleted directly.
func removeRefreshWorktree(ctx context.Context, repo, dir string) error {
	ctx = context.WithoutCancel(ctx)
	_, removeErr := gitOut(ctx, repo, "worktree", "remove", "--force", dir)
	if removeErr != nil {
		if err := os.Remove(dir); err == nil || errors.Is(err, os.ErrNotExist) {
			removeErr = nil
		}
	}
	_, pruneErr := gitOut(ctx, repo, "worktree", "prune")
	if err := errors.Join(removeErr, pruneErr); err != nil {
		return fmt.Errorf("temporary worktree cleanup failed, remove %s with `git worktree remove --force`: %w", dir, err)
	}
	return nil
}

// refreshInWorktree discovers, probes, applies, tests, commits and pushes inside wt.
// A failure anywhere leaves origin and the user's checkout untouched; the next run starts over.
func refreshInWorktree(ctx context.Context, wt, def string, out io.Writer) (bool, error) {
	data, models, err := readRepoModels(wt)
	if err != nil {
		return false, err
	}
	changes := probeChanges(ctx, discoverChanges(ctx, models, out), out)
	if len(changes) == 0 {
		fmt.Fprintln(out, "no verified model id changes")
		return false, nil
	}
	if err := applyChanges(ctx, wt, data, changes, out); err != nil {
		return false, err
	}
	subject, body := refreshCommitMessage(changes)
	steps := [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", subject, "-m", body},
	}
	for _, s := range steps {
		if _, err := gitOut(ctx, wt, s...); err != nil {
			return false, err
		}
	}
	fmt.Fprintf(out, "committed: %s\n", subject)
	if _, err := gitOut(ctx, wt, "push", "--quiet", "origin", "HEAD:refs/heads/"+def); err != nil {
		return false, fmt.Errorf("push to origin/%s failed; the commit stays only in the discarded worktree: %w", def, err)
	}
	fmt.Fprintf(out, "pushed to origin/%s\n", def)
	return true, nil
}

// refreshSyncCheckout runs the update step for the orch checkout, using its configured
// install command when ~/.orch/update.json lists it.
func refreshSyncCheckout(ctx context.Context, repo string) (string, error) {
	r := updateRepo{Name: "orch", Path: repo, Install: refreshInstallShell}
	if cfg, err := loadUpdateConfig(); err == nil {
		key := updateStateKey(repo)
		for _, c := range cfg.Repos {
			if updateStateKey(c.Path) == key {
				r = c
				break
			}
		}
	}
	if abs, err := filepath.Abs(r.Path); err == nil {
		r.Path = abs
	}
	return updateOneRepo(ctx, r, false)
}
