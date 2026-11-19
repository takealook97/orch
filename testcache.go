package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A pass is valid only for the same command, cwd and environment on a clean tree
// without nested repositories. Only the environment digest is persisted.
type testCacheEntry struct {
	Environment string    `json:"environment"`
	Disabled    string    `json:"-"`
	Command     string    `json:"command"`
	Tree        string    `json:"tree"`
	Cwd         string    `json:"cwd"`
	Passed      time.Time `json:"passed"`
	Dir         string    `json:"task_dir"`
	Log         string    `json:"log"`
	TaskID      string    `json:"task"`
	Run         string    `json:"run"`
}

func testCacheKey(line, cwd string) (string, testCacheEntry) {
	entry := testCacheEntry{Command: line}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", entry
	}
	entry.Cwd = abs
	root, err := cacheGit(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", entry
	}
	nested, err := testCacheNestedRepositories(root)
	if err != nil {
		return "", entry
	}
	if nested {
		entry.Disabled = "disabled (nested repositories)"
		return "", entry
	}
	tree, err := cacheGit(abs, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", entry
	}
	status, err := cacheGit(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return "", entry
	}
	entry.Tree = tree
	entry.Environment = testCacheEnvironment(os.Environ())
	data := entry.Environment + "\x00" + root + "\x00" + tree + "\x00" + abs + "\x00" + line
	return fmt.Sprintf("%x", sha256.Sum256([]byte(data))), entry
}

func testCachePath(key string) string {
	return filepath.Join(orchHome(), "test-cache", key+".json")
}

func lookupTestCache(key string, expected testCacheEntry) *testCacheEntry {
	if key == "" {
		return nil
	}
	b, err := os.ReadFile(testCachePath(key))
	if err != nil {
		return nil
	}
	var entry testCacheEntry
	if json.Unmarshal(b, &entry) != nil || entry.Passed.IsZero() || entry.Dir == "" ||
		entry.Environment != expected.Environment || entry.Command != expected.Command || entry.Tree != expected.Tree || entry.Cwd != expected.Cwd {
		return nil
	}
	return &entry
}

func storeTestCache(key string, entry testCacheEntry, dir, log, taskID, run string) {
	if key == "" {
		return
	}
	// A test may change tracked or untracked files, or even commit a new tree.
	if after, _ := testCacheKey(entry.Command, entry.Cwd); after != key {
		return
	}
	entry.Passed, entry.Dir, entry.Log, entry.TaskID, entry.Run = time.Now().UTC(), dir, log, taskID, run
	p := testCachePath(key)
	if os.MkdirAll(filepath.Dir(p), 0700) != nil {
		return
	}
	b, _ := json.Marshal(entry)
	f, err := os.CreateTemp(filepath.Dir(p), ".cache-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return
	}
	if f.Close() == nil {
		os.Rename(f.Name(), p)
	}
}

func cachedTestReport(entry *testCacheEntry, asJSON bool) string {
	t := &Task{ID: entry.TaskID, Run: entry.Run, Kind: "test", Status: "done", Dir: entry.Dir,
		Test: &TestResult{Cmd: entry.Command, Log: entry.Log}}
	notice := fmt.Sprintf("cached (tree %s, passed %s, from %s)", entry.Tree[:min(12, len(entry.Tree))], entry.Passed.Format(time.RFC3339Nano), entry.Dir)
	report := taskReport(t, asJSON)
	if asJSON {
		var v map[string]any
		json.Unmarshal([]byte(report), &v)
		v["cached"] = notice
		b, _ := json.Marshal(v)
		return string(b)
	}
	return strings.Replace(report, "\ndir:", " "+notice+"\ndir:", 1)
}

// ORCH_RUN and ORCH_WORKER are injected per run/task in task.go and worker.go.
// ORCH_MODEL, ORCH_HOME and every other ORCH_* setting remain part of the key.
func testCacheEnvironment(env []string) string {
	stable := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "PWD", "OLDPWD", "SHLVL", "_", "TERM_SESSION_ID", "TMUX", "TMUX_PANE", "WINDOWID",
			"SSH_AUTH_SOCK", "SSH_CLIENT", "SSH_CONNECTION", "SSH_TTY", "ORCH_RUN", "ORCH_WORKER":
			continue
		}
		stable = append(stable, item)
	}
	sort.Strings(stable)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(stable, "\x00"))))
}

func testCacheNestedRepositories(root string) (bool, error) {
	// Gitlinks count even when the submodule is not initialized.
	staged, err := cacheGit(root, "ls-files", "--stage", "-z")
	if err != nil {
		return false, err
	}
	for _, record := range strings.Split(staged, "\x00") {
		if strings.HasPrefix(record, "160000 ") {
			return true, nil
		}
	}
	// --directory can collapse a whole ignored container (.worktrees/, for
	// example), so inspect its descendants too. Use raw NUL-delimited output
	// to preserve whitespace and newlines in paths.
	for _, ignored := range []bool{true, false} {
		args := []string{"-C", root, "ls-files", "--others", "--exclude-standard", "--directory", "-z"}
		if ignored {
			args = append(args, "--ignored")
		}
		out, err := exec.Command("git", args...).Output()
		if err != nil {
			return false, err
		}
		for _, path := range strings.Split(string(out), "\x00") {
			if !strings.HasSuffix(path, "/") {
				continue
			}
			found := false
			err := filepath.WalkDir(filepath.Join(root, path), func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.Name() == ".git" {
					found = true // directory, worktree gitfile, or symlink: fail closed.
					return fs.SkipAll
				}
				return nil
			})
			if err != nil || found {
				return found, err
			}
		}
	}
	return false, nil
}

func testCacheReport(report, disabled string, asJSON bool) string {
	report = strings.TrimRight(report, "\n")
	if disabled == "" {
		return report
	}
	if asJSON {
		var v map[string]any
		json.Unmarshal([]byte(report), &v)
		v["cache"] = disabled
		b, _ := json.Marshal(v)
		return string(b)
	}
	return strings.TrimRight(report, "\n") + "\ncache: " + disabled
}
