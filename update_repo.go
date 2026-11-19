package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// updateState records, per checkout, the last commit whose install command succeeded.
// A checkout whose HEAD differs is installed again, so a failed install is retried.
type updateState struct {
	Repos map[string]updateRepoState `json:"repos"`
}

type updateRepoState struct {
	InstalledCommit string `json:"installed_commit"`
}

func updateStatePath() string { return filepath.Join(orchHome(), "update.state.json") }

func updateStateKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return path
}

// loadUpdateState reads the install record. A missing file, JSON null or a value of the
// wrong shape means nothing is known to be installed; invalid JSON such as a truncated
// file is an error and the file is left for the user to inspect.
func loadUpdateState() (updateState, error) {
	empty := updateState{Repos: map[string]updateRepoState{}}
	data, err := os.ReadFile(updateStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	var st updateState
	if err := json.Unmarshal(data, &st); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return empty, nil
		}
		return empty, fmt.Errorf("%s: %w", updateStatePath(), err)
	}
	if st.Repos == nil {
		st.Repos = map[string]updateRepoState{}
	}
	return st, nil
}

// recordInstalled stores commit for one checkout, keeping the other entries.
func recordInstalled(path, commit string) error {
	st, err := loadUpdateState()
	if err != nil {
		return err
	}
	repos := make(map[string]updateRepoState, len(st.Repos)+1)
	for k, v := range st.Repos {
		repos[k] = v
	}
	repos[updateStateKey(path)] = updateRepoState{InstalledCommit: commit}
	data, err := json.MarshalIndent(updateState{Repos: repos}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(orchHome(), 0700); err != nil {
		return err
	}
	return atomicStateWrite(updateStatePath(), append(data, '\n'))
}

// updateLiveWriters is replaced in tests.
var updateLiveWriters = func(root string) int { return len(liveWritersIn(root, nil)) }

// remoteDefaultBranch asks origin for its current HEAD branch. It writes no refs,
// so update, refresh and their dry runs all use it.
func remoteDefaultBranch(ctx context.Context, dir string) (string, error) {
	out, err := updateGit(ctx, dir, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "ref: refs/heads/")
		if !ok {
			continue
		}
		if branch, _, ok := strings.Cut(rest, "\t"); ok && branch != "" {
			return branch, nil
		}
	}
	return "", fmt.Errorf("origin does not report a HEAD branch")
}

// repoGate is the part of the skip check that is repeated right before the merge.
func repoGate(ctx context.Context, dir, def string) (skip string, head string, err error) {
	git := func(args ...string) (string, error) { return updateGit(ctx, dir, args...) }
	status, err := git("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", "", err
	}
	if status != "" {
		return "skip: working tree is dirty", "", nil
	}
	branch, _ := git("symbolic-ref", "--quiet", "--short", "HEAD")
	if branch != def {
		return fmt.Sprintf("skip: on branch %q, not default %q", branch, def), "", nil
	}
	if updateLiveWriters(dir) > 0 {
		return "skip: live orch writer in checkout", "", nil
	}
	head, err = git("rev-parse", "HEAD")
	return "", head, err
}

func updateOneRepo(ctx context.Context, r updateRepo, dry bool) (string, error) {
	if info, err := os.Stat(filepath.Join(r.Path, ".git")); err != nil || info == nil {
		return "skip: not a git checkout: " + r.Path, nil
	}
	git := func(args ...string) (string, error) { return updateGit(ctx, r.Path, args...) }
	def, err := remoteDefaultBranch(ctx, r.Path)
	if err != nil {
		return "", err
	}
	skip, head, err := repoGate(ctx, r.Path, def)
	if err != nil || skip != "" {
		return skip, err
	}
	if dry {
		return dryRunOutcome(ctx, r, def, head)
	}
	if _, err := git("fetch", "--quiet", "origin", "+refs/heads/"+def+":refs/remotes/origin/"+def); err != nil {
		return "", err
	}
	// The full ref name cannot resolve to a local branch called origin/<def>; the OID
	// pins the merge to the commit compared here.
	remote, err := git("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+def+"^{commit}")
	if err != nil {
		return "", err
	}
	counts, err := git("rev-list", "--left-right", "--count", "HEAD..."+remote)
	if err != nil {
		return "", err
	}
	ahead, behind, err := parseAheadBehind(counts)
	if err != nil {
		return "", err
	}
	switch {
	case ahead > 0 && behind > 0:
		return fmt.Sprintf("skip: diverged from origin/%s (ahead %d, behind %d)", def, ahead, behind), nil
	case ahead > 0:
		return fmt.Sprintf("skip: ahead of origin/%s by %d", def, ahead), nil
	case behind == 0:
		return installPending(ctx, r, head, "up to date")
	}
	// The checks above ran before a network call; repeat them right before merging.
	skip, again, err := repoGate(ctx, r.Path, def)
	if err != nil || skip != "" {
		return skip, err
	}
	if again != head {
		return "skip: HEAD moved during update", nil
	}
	if _, err := git("merge", "--ff-only", "--no-overwrite-ignore", "--quiet", remote); err != nil {
		return "", err
	}
	after, err := git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return installPending(ctx, r, after, fmt.Sprintf("updated %s..%s", short(head), short(after)))
}

// installPending runs the install command unless head is known to be installed.
// A checkout without a record counts as not installed; only a successful install is recorded.
func installPending(ctx context.Context, r updateRepo, head, outcome string) (string, error) {
	if strings.TrimSpace(r.Install) == "" {
		return outcome, nil
	}
	st, err := loadUpdateState()
	if err != nil {
		return "", err
	}
	if st.Repos[updateStateKey(r.Path)].InstalledCommit == head {
		return outcome, nil
	}
	if err := runUpdateInstall(ctx, r); err != nil {
		return "", fmt.Errorf("%s but install at %s failed (retried next run): %w", outcome, short(head), err)
	}
	if err := recordInstalled(r.Path, head); err != nil {
		return "", fmt.Errorf("%s and installed %s, but recording it failed: %w", outcome, short(head), err)
	}
	return outcome + ", installed " + short(head), nil
}

// dryRunOutcome compares with origin through ls-remote only: no refs or FETCH_HEAD are written.
func dryRunOutcome(ctx context.Context, r updateRepo, def, head string) (string, error) {
	out, err := updateGit(ctx, r.Path, "ls-remote", "origin", "refs/heads/"+def)
	if err != nil {
		return "", err
	}
	remote, _, _ := strings.Cut(out, "\t")
	if remote == "" {
		return "", fmt.Errorf("origin has no branch %s", def)
	}
	pending := ""
	if strings.TrimSpace(r.Install) != "" {
		if st, err := loadUpdateState(); err == nil && st.Repos[updateStateKey(r.Path)].InstalledCommit != head {
			pending = "; install pending at " + short(head)
		}
	}
	if remote == head {
		return "up to date" + pending, nil
	}
	if _, err := updateGit(ctx, r.Path, "cat-file", "-e", remote+"^{commit}"); err != nil {
		return fmt.Sprintf("would fetch origin/%s (%s) and fast-forward if HEAD is behind it%s", def, short(remote), pending), nil
	}
	counts, err := updateGit(ctx, r.Path, "rev-list", "--left-right", "--count", "HEAD..."+remote)
	if err != nil {
		return "", err
	}
	ahead, behind, err := parseAheadBehind(counts)
	if err != nil {
		return "", err
	}
	switch {
	case ahead > 0:
		return fmt.Sprintf("skip: not a fast-forward of origin/%s (ahead %d, behind %d)", def, ahead, behind), nil
	default:
		return fmt.Sprintf("would fast-forward %d commit(s) and run install", behind), nil
	}
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func parseAheadBehind(s string) (int, int, error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", s)
	}
	a, e1 := strconv.Atoi(f[0])
	b, e2 := strconv.Atoi(f[1])
	if e1 != nil || e2 != nil {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", s)
	}
	return a, b, nil
}

func updateGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, updateGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// GIT_OPTIONAL_LOCKS=0 keeps the read-only gate (status, rev-parse, rev-list) from
	// rewriting the user's index stat cache; commands that must write still lock.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if err != nil {
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, probeExcerpt(text))
	}
	return text, nil
}

// runUpdateInstall runs the repository's install command through the shell.
// ORCH_AUTO_UPDATE=0 keeps an orch binary started by the install from re-triggering.
func runUpdateInstall(ctx context.Context, r updateRepo) error {
	ctx, cancel := context.WithTimeout(ctx, updateInstallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", r.Install)
	cmd.Dir = r.Path
	cmd.Env = append(os.Environ(), "ORCH_AUTO_UPDATE=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		text := out.String()
		if len(text) > 400 {
			text = text[len(text)-400:]
		}
		return fmt.Errorf("%q: %w: %s", r.Install, err, probeExcerpt(text))
	}
	return nil
}
