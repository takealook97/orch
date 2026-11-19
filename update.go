package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// updateRepo is one checkout that `orch update` fast-forwards from origin.
type updateRepo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Install string `json:"install"`
}

type updateConfig struct {
	Repos []updateRepo `json:"repos"`
}

type updateOptions struct {
	DryRun bool
	Models bool
	Out    io.Writer // progress lines; nil means quiet
}

func updateConfigPath() string { return filepath.Join(orchHome(), "update.json") }
func updateLockPath() string   { return filepath.Join(orchHome(), "update.lock") }
func updateLogPath() string    { return filepath.Join(orchHome(), "update.log") }
func updateStampPath() string  { return filepath.Join(orchHome(), "update.stamp") }

func defaultUpdateConfig() updateConfig {
	projects := filepath.Join(home(), "Projects")
	return updateConfig{Repos: []updateRepo{
		{Name: "orch", Path: filepath.Join(projects, "orch"), Install: "make build link && ./bin/orch install --repo ."},
		{Name: "vat", Path: filepath.Join(projects, "vat"), Install: "make install"},
		{Name: "herdr-cockpit", Path: filepath.Join(projects, "herdr-cockpit"), Install: "./scripts/install.sh"},
	}}
}

// loadUpdateConfig reads ~/.orch/update.json; a missing file means the defaults.
func loadUpdateConfig() (updateConfig, error) {
	data, err := os.ReadFile(updateConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return defaultUpdateConfig(), nil
	}
	if err != nil {
		return updateConfig{}, err
	}
	var cfg updateConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return updateConfig{}, fmt.Errorf("%s: %w", updateConfigPath(), err)
	}
	for i, r := range cfg.Repos {
		if r.Path == "" {
			return updateConfig{}, fmt.Errorf("%s: repos[%d] has no path", updateConfigPath(), i)
		}
		if strings.HasPrefix(r.Path, "~/") {
			r.Path = filepath.Join(home(), r.Path[2:])
		}
		if r.Name == "" {
			r.Name = filepath.Base(r.Path)
		}
		cfg.Repos[i] = r
	}
	return cfg, nil
}

func updateCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("orch update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dry := fs.Bool("dry-run", false, "report what would change without fetching results into checkouts")
	quiet := fs.Bool("quiet", false, "write only to ~/.orch/update.log")
	models := fs.Bool("models", false, "refresh model ids after the orch checkout is updated")
	if pos, err := parse(fs, args); err != nil || len(pos) != 0 {
		fmt.Fprintln(stderr, "usage: orch update [--dry-run] [--quiet] [--models]")
		return 2
	}
	opts := updateOptions{DryRun: *dry, Models: *models, Out: stdout}
	if *quiet {
		opts.Out = nil
	}
	if err := runUpdate(context.Background(), opts); err != nil {
		fmt.Fprintln(stderr, "orch update:", err)
		return 1
	}
	return 0
}

// runUpdate fast-forwards every configured checkout under one global lock.
// A checkout that is dirty, off its default branch, written by a live orch worker,
// or ahead of/diverged from origin is skipped with a reason.
func runUpdate(ctx context.Context, opts updateOptions) error {
	if err := os.MkdirAll(orchHome(), 0700); err != nil {
		return err
	}
	logLine := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		if opts.Out != nil {
			fmt.Fprintln(opts.Out, line)
		}
		appendUpdateLog(line)
	}
	lock, err := diskLock(updateLockPath(), true)
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			logLine("skip: another orch update is running")
			return nil
		}
		return err
	}
	defer unlockDisk(lock)
	touchStamp(updateStampPath(), time.Now())
	cfg, err := loadUpdateConfig()
	if err != nil {
		logLine("error: %v", err)
		return err
	}
	failed := 0
	orchReady := ""
	for _, r := range cfg.Repos {
		outcome, err := updateOneRepo(ctx, r, opts.DryRun)
		if err != nil {
			failed++
			logLine("%s: error: %v", r.Name, err)
			continue
		}
		logLine("%s: %s", r.Name, outcome)
		if r.Name == "orch" && !strings.HasPrefix(outcome, "skip") {
			orchReady = r.Path
		}
	}
	if opts.Models {
		if orchReady == "" {
			logLine("models: skip: orch checkout was not updated cleanly")
		} else {
			out := opts.Out
			if out == nil {
				out = io.Discard
			}
			if err := refreshModels(ctx, modelsRefreshOptions{Repo: orchReady, DryRun: opts.DryRun, Out: out}); err != nil {
				failed++
				logLine("models: error: %v", err)
			} else {
				logLine("models: done")
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d step(s) failed; see %s", failed, updateLogPath())
	}
	return nil
}

func appendUpdateLog(line string) {
	f, err := os.OpenFile(updateLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), strings.ReplaceAll(line, "\n", " "))
}

func touchStamp(path string, at time.Time) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return false
	}
	f.Close()
	return os.Chtimes(path, at, at) == nil
}

// updateLiveWriters is replaced in tests.
var updateLiveWriters = func(root string) int { return len(liveWritersIn(root, nil)) }

func updateOneRepo(ctx context.Context, r updateRepo, dry bool) (string, error) {
	if info, err := os.Stat(filepath.Join(r.Path, ".git")); err != nil || info == nil {
		return "skip: not a git checkout: " + r.Path, nil
	}
	git := func(args ...string) (string, error) { return updateGit(ctx, r.Path, args...) }
	status, err := git("status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "skip: working tree is dirty", nil
	}
	branch, _ := git("symbolic-ref", "--quiet", "--short", "HEAD")
	def := updateDefaultBranch(ctx, r.Path)
	if def == "" {
		return "skip: no origin default branch", nil
	}
	if branch != def {
		return fmt.Sprintf("skip: on branch %q, not default %q", branch, def), nil
	}
	if updateLiveWriters(r.Path) > 0 {
		return "skip: live orch writer in checkout", nil
	}
	if _, err := git("fetch", "--quiet", "origin"); err != nil {
		return "", err
	}
	counts, err := git("rev-list", "--left-right", "--count", "HEAD...origin/"+def)
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
		return "up to date", nil
	case dry:
		return fmt.Sprintf("would fast-forward %d commit(s) and run install", behind), nil
	}
	before, _ := git("rev-parse", "--short", "HEAD")
	if _, err := git("merge", "--ff-only", "--quiet", "origin/"+def); err != nil {
		return "", err
	}
	after, _ := git("rev-parse", "--short", "HEAD")
	if strings.TrimSpace(r.Install) != "" {
		if err := runUpdateInstall(ctx, r); err != nil {
			return "", fmt.Errorf("fast-forwarded %s..%s but install failed: %w", before, after, err)
		}
	}
	return fmt.Sprintf("updated %s..%s", before, after), nil
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

func updateDefaultBranch(ctx context.Context, dir string) string {
	if ref, err := updateGit(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimPrefix(ref, "origin/"); b != "" && b != ref {
			return b
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, err := updateGit(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b); err == nil {
			return b
		}
	}
	return ""
}

func updateGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, updateGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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

// autoUpdateSpawn is replaced in tests.
var autoUpdateSpawn = func() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "update", "--quiet")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "ORCH_AUTO_UPDATE=0")
	// Nil stdin/stdout/stderr connect to the null device.
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// maybeAutoUpdate launches a detached `orch update --quiet` when the stamp is stale.
// Cost on the common path (hooks included): environment reads and one stat.
func maybeAutoUpdate(args []string) bool {
	if envOrch("ROLE") == "worker" || envOrch("AUTO_UPDATE") == "0" {
		return false
	}
	if len(args) > 0 && (args[0] == "update" || args[0] == "stats" || strings.HasPrefix(args[0], "__")) {
		return false
	}
	stamp := updateStampPath()
	now := time.Now()
	if info, err := os.Stat(stamp); err == nil && now.Sub(info.ModTime()) < autoUpdateEvery() {
		return false
	}
	if os.MkdirAll(orchHome(), 0700) != nil || !touchStamp(stamp, now) {
		return false
	}
	if autoUpdateSpawn() != nil {
		_ = os.Chtimes(stamp, time.Unix(0, 0), time.Unix(0, 0))
		return false
	}
	return true
}
