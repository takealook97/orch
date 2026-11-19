package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A pass is valid only for the same command and cwd on an unchanged clean tree.
// Environment changes are deliberately caller-controlled through --fresh.
type testCacheEntry struct {
	Command string    `json:"command"`
	Tree    string    `json:"tree"`
	Cwd     string    `json:"cwd"`
	Passed  time.Time `json:"passed"`
	Dir     string    `json:"task_dir"`
	Log     string    `json:"log"`
	TaskID  string    `json:"task"`
	Run     string    `json:"run"`
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
	tree, err := cacheGit(abs, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", entry
	}
	status, err := cacheGit(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return "", entry
	}
	entry.Tree = tree
	data := root + "\x00" + tree + "\x00" + abs + "\x00" + line
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
		entry.Command != expected.Command || entry.Tree != expected.Tree || entry.Cwd != expected.Cwd {
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
