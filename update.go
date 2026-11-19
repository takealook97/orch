package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// updateRefreshModels is replaced in tests.
var updateRefreshModels = refreshModels

// runUpdate fast-forwards every configured checkout under one global lock.
// A checkout that is dirty, off its default branch, written by a live orch worker,
// or ahead of/diverged from origin is skipped with a reason.
func runUpdate(ctx context.Context, opts updateOptions) error {
	if err := os.MkdirAll(orchHome(), 0700); err != nil {
		return err
	}
	var logErr error
	logLine := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		if opts.Out != nil {
			fmt.Fprintln(opts.Out, line)
		}
		if err := appendUpdateLog(line); err != nil && logErr == nil {
			logErr = err
		}
	}
	lock, err := diskLock(updateLockPath(), true)
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			logLine("skip: another orch update is running")
			return withLogErr(nil, logErr)
		}
		return err
	}
	defer unlockDisk(lock)
	if !opts.DryRun {
		touchStamp(updateStampPath(), time.Now())
	}
	cfg, err := loadUpdateConfig()
	if err != nil {
		logLine("error: %v", err)
		return withLogErr(err, logErr)
	}
	failed := 0
	orchRepo := ""
	for _, r := range cfg.Repos {
		outcome, err := updateOneRepo(ctx, r, opts.DryRun)
		if err != nil {
			failed++
			logLine("%s: error: %v", r.Name, err)
		} else {
			logLine("%s: %s", r.Name, outcome)
		}
		if r.Name == "orch" && fileExists(filepath.Join(r.Path, ".git")) {
			orchRepo = r.Path
		}
	}
	if opts.Models {
		if orchRepo == "" {
			logLine("models: skip: no orch checkout configured")
		} else {
			out := opts.Out
			if out == nil {
				out = io.Discard
			}
			// The refresh works in its own worktree, so a skipped user checkout does not block it.
			if err := updateRefreshModels(ctx, modelsRefreshOptions{Repo: orchRepo, DryRun: opts.DryRun, Out: out}); err != nil {
				failed++
				logLine("models: error: %v", err)
			} else {
				logLine("models: done")
			}
		}
	}
	var runErr error
	if failed > 0 {
		runErr = fmt.Errorf("%d step(s) failed; see %s", failed, updateLogPath())
	}
	return withLogErr(runErr, logErr)
}

// withLogErr reports a lost log line; the update steps themselves already happened.
func withLogErr(err, logErr error) error {
	if logErr == nil {
		return err
	}
	logErr = fmt.Errorf("update steps finished but writing %s failed: %w", updateLogPath(), logErr)
	if err == nil {
		return logErr
	}
	return errors.Join(err, logErr)
}

func appendUpdateLog(line string) error {
	f, err := os.OpenFile(updateLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), strings.ReplaceAll(line, "\n", " "))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func touchStamp(path string, at time.Time) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return false
	}
	f.Close()
	return os.Chtimes(path, at, at) == nil
}
