package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAskCacheKeyInvalidation(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	dir := taskRepo(t)
	key := askCacheKey(" a  question ", "model", 20, dir)
	if key == "" || key == askCacheKey("a question", "model", 20, dir) {
		t.Fatal("raw question collision")
	}
	for _, k := range []string{askCacheKey("other", "model", 20, dir), askCacheKey("a question", "other", 20, dir), askCacheKey("a question", "model", 21, dir)} {
		if k == key {
			t.Fatal("key collision")
		}
	}
	os.WriteFile(filepath.Join(dir, "owned.txt"), []byte("dirty"), 0600)
	if key == askCacheKey("a question", "model", 20, dir) {
		t.Fatal("dirty diff")
	}
	exec.Command("git", "-C", dir, "commit", "-am", "changed").Run()
	if key == askCacheKey("a question", "model", 20, dir) {
		t.Fatal("commit")
	}
	if askCacheKey("a", "m", 1, t.TempDir()) != "" {
		t.Fatal("non-git")
	}
}

func TestAskCacheStoreTTLAndFailure(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	dir := t.TempDir()
	answer := filepath.Join(dir, "answer.md")
	os.WriteFile(answer, []byte("hello"), 0600)
	task := &Task{Dir: dir, Status: "failed", AnswerPath: answer}
	storeAskCache("key", task)
	if lookupAskCache("key") != nil {
		t.Fatal("failed cached")
	}
	task.Status = "done"
	if err := writeTask(task); err != nil {
		t.Fatal(err)
	}
	storeAskCache("key", task)
	if lookupAskCache("key") == nil {
		t.Fatal("miss")
	}
	entry := askCacheEntry{Dir: dir, AnswerPath: answer, Created: time.Now().Add(-8 * 24 * time.Hour)}
	writeAskCacheEntry("key", entry)
	if lookupAskCache("key") != nil {
		t.Fatal("expired")
	}
	t.Setenv("ORCH_ASK_CACHE_TTL", "0")
	storeAskCache("disabled", task)
	if lookupAskCache("disabled") != nil {
		t.Fatal("disabled")
	}
}

func TestAskCacheHitFreshAndBackground(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HC_TERMINAL_ID", "")
	t.Setenv("ORCH_ASK_CACHE_TTL", "168")
	repo := taskRepo(t)
	bin := t.TempDir()
	count := filepath.Join(t.TempDir(), "count")
	t.Setenv("ASK_COUNT", count)
	script := `#!/bin/sh
printf 'run\n' >> "$ASK_COUNT"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"cached full answer"}}'
`
	os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	call := func(extra ...string) string {
		t.Helper()
		args := []string{"ask", "--cwd", repo, "--model", "codex-low-h", "--json"}
		args = append(args, extra...)
		args = append(args, "same question")
		var out, errs bytes.Buffer
		if code := taskCommand("ask", args[1:], &out, &errs); code != 0 {
			t.Fatalf("%d %s", code, errs.String())
		}
		return out.String()
	}
	if strings.Contains(call(), `"cached"`) {
		t.Fatal("first hit")
	}
	if !strings.Contains(call("--background"), `"cached"`) {
		t.Fatal("background miss")
	}
	b, _ := os.ReadFile(count)
	if strings.Count(string(b), "run") != 1 {
		t.Fatal("CLI ran on cache hit")
	}
	if strings.Contains(call("--fresh"), `"cached"`) {
		t.Fatal("fresh hit")
	}
	if !strings.Contains(call(), `"cached"`) {
		t.Fatal("fresh did not store")
	}
	b, _ = os.ReadFile(count)
	if strings.Count(string(b), "run") != 2 {
		t.Fatal("fresh did not execute")
	}
	entries, _ := filepath.Glob(filepath.Join(runsDir(), "*", "tasks", "*", "task.json"))
	if len(entries) != 2 {
		t.Fatalf("hit created task: %v", entries)
	}
}

func TestAskCacheMissingTaskMetadata(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	dir := t.TempDir()
	p := filepath.Join(dir, "answer.md")
	os.WriteFile(p, []byte("answer"), 0600)
	writeAskCacheEntry("orphan", askCacheEntry{Dir: dir, AnswerPath: p, Created: time.Now()})
	if lookupAskCache("orphan") != nil {
		t.Fatal("orphan cache cannot reproduce task report")
	}
}

func TestAskCacheTrailingWhitespaceDiff(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	repo := taskRepo(t)
	p := filepath.Join(repo, "owned.txt")
	os.WriteFile(p, []byte("changed \n"), 0600)
	first := askCacheKey("q", "m", 1, repo)
	os.WriteFile(p, []byte("changed  \n"), 0600)
	if first == askCacheKey("q", "m", 1, repo) {
		t.Fatal("trailing whitespace edits must invalidate")
	}
}

func TestAskCacheResolvedModelAndCwd(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HC_TERMINAL_ID", "")
	cleanModelEnv(t)
	repo := taskRepo(t)
	for _, d := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(repo, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	count := filepath.Join(t.TempDir(), "count")
	t.Setenv("ASK_COUNT", count)
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf 'x\\n' >> \"$ASK_COUNT\"\necho '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"answer\"}}'\n"), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	call := func(dir string, extra ...string) string {
		t.Helper()
		args := append([]string{"--cwd", dir, "--model", "codex-low-h"}, extra...)
		args = append(args, "q")
		var out, errout bytes.Buffer
		if c := taskCommand("ask", args, &out, &errout); c != 0 {
			t.Fatalf("%d %s", c, errout.String())
		}
		return out.String()
	}
	a, b := filepath.Join(repo, "a"), filepath.Join(repo, "b")
	call(a)
	hit := call(a, "--background")
	if !strings.Contains(hit, "wait: orch wait ") || !strings.Contains(hit, " --run ") || !strings.Contains(hit, "cached:") {
		t.Fatal(hit)
	}
	call(b)
	t.Setenv("CODEX_LOW_H", "future-model")
	call(a)
	overlayFile(t, "keys: [{key: codex-low-h, effort: low}]\n")
	call(a)
	data, _ := os.ReadFile(count)
	if strings.Count(string(data), "x\n") != 4 {
		t.Fatalf("calls: %s", data)
	}
}
