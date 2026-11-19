package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testCacheFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_ROLE", "worker")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HC_TERMINAL_ID", "")
	repo := taskRepo(t)
	counter := filepath.Join(t.TempDir(), "count")
	// Keep the counter outside the repo so the command leaves the tree clean.
	t.Setenv("TEST_CACHE_COUNT", counter)
	return repo, `printf 'run\n' >> "$TEST_CACHE_COUNT"`
}

func testCacheCall(t *testing.T, cwd, line string, flags ...string) (int, string) {
	t.Helper()
	args := append([]string{"--cwd", cwd}, flags...)
	args = append(args, line)
	var out, errs bytes.Buffer
	code := taskCommand("test", args, &out, &errs)
	if errs.Len() != 0 {
		t.Fatalf("unexpected stderr (%d): %s", code, errs.String())
	}
	return code, out.String()
}

func testCacheCount(t *testing.T, want int) {
	t.Helper()
	// The counter lives outside the tested repository.
	p := os.Getenv("TEST_CACHE_COUNT")
	b, err := os.ReadFile(p)
	if err != nil || strings.Count(string(b), "run\n") != want {
		t.Fatalf("counter = %q, err=%v; want %d", b, err, want)
	}
}

func testCacheGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
}

func TestTestCacheHitFreshAndIdentity(t *testing.T) {
	repo, line := testCacheFixture(t)
	// Exercise the regular task-report path as well as the no-active-run fallback.
	r := &Run{ID: "test-cache-run"}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCH_RUN", r.ID)
	if code, out := testCacheCall(t, repo, line); code != 0 || strings.Contains(out, "cached") {
		t.Fatalf("first: %d %s", code, out)
	}
	key, expected := testCacheKey(line, repo)
	entry := lookupTestCache(key, expected)
	if entry == nil || entry.TaskID != "task-1" || entry.Passed.IsZero() {
		t.Fatalf("missing receipt: %#v", entry)
	}
	if code, out := testCacheCall(t, repo, line); code != 0 ||
		!strings.HasPrefix(out, "done task-1 test 0.0s test=0 cached (tree ") ||
		!strings.Contains(out, "passed ") || !strings.Contains(out, "from "+entry.Dir+")\ndir: "+entry.Dir) {
		t.Fatalf("hit: %d %s", code, out)
	}
	testCacheCount(t, 1)
	if code, out := testCacheCall(t, repo, line, "--fresh"); code != 0 || strings.Contains(out, "cached") {
		t.Fatalf("fresh: %d %s", code, out)
	}
	_, out := testCacheCall(t, repo, line, "--json")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil || v["cached"] == nil || v["status"] != "done" || v["exit_code"] != float64(0) || v["task"] != "task-2" {
		t.Fatalf("JSON hit: %s (%v)", out, err)
	}
	testCacheCount(t, 2)
	// HEAD identity is intentionally excluded: an empty commit has the same tree.
	testCacheGit(t, repo, "commit", "--allow-empty", "-qm", "same tree")
	if _, out := testCacheCall(t, repo, line); !strings.Contains(out, "cached") {
		t.Fatalf("same tree missed: %s", out)
	}
	if err := os.Mkdir(filepath.Join(repo, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if code, out := testCacheCall(t, filepath.Join(repo, "sub"), line); code != 0 || strings.Contains(out, "cached") {
		t.Fatalf("cwd collision: %d %s", code, out)
	}
	if code, out := testCacheCall(t, repo, line+"; true"); code != 0 || strings.Contains(out, "cached") {
		t.Fatalf("command collision: %d %s", code, out)
	}
	testCacheCount(t, 4)
	if err := os.WriteFile(filepath.Join(repo, "owned.txt"), []byte("new tree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testCacheGit(t, repo, "commit", "-am", "changed tree")
	if code, out := testCacheCall(t, repo, line); code != 0 || strings.Contains(out, "cached") {
		t.Fatalf("tree collision: %d %s", code, out)
	}
	testCacheCount(t, 5)
}

func TestTestCacheDirtyAndFailure(t *testing.T) {
	for _, mode := range []string{"tracked", "staged", "untracked", "failure", "non-git"} {
		t.Run(mode, func(t *testing.T) {
			repo, line := testCacheFixture(t)
			if mode == "non-git" {
				repo = t.TempDir()
			} else if mode == "failure" {
				line += "; exit 7"
			} else {
				// Seed a pass so dirty trees must reject an existing receipt.
				testCacheCall(t, repo, line)
				name := "owned.txt"
				if mode == "untracked" {
					name = "new.txt"
				}
				if err := os.WriteFile(filepath.Join(repo, name), []byte("dirty\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "staged" {
					testCacheGit(t, repo, "add", name)
				}
			}
			for i := 0; i < 2; i++ {
				code, out := testCacheCall(t, repo, line)
				wantCode := 0
				if mode == "failure" {
					wantCode = 1
				}
				if code != wantCode || strings.Contains(out, "cached") {
					t.Fatalf("unexpected reuse: %d %s", code, out)
				}
			}
			wantCount := 3
			if mode == "failure" || mode == "non-git" {
				wantCount = 2
			}
			testCacheCount(t, wantCount)
			if mode == "failure" {
				key, _ := testCacheKey(line, repo)
				if _, err := os.Stat(testCachePath(key)); !os.IsNotExist(err) {
					t.Fatalf("failure cached: %v", err)
				}
			}
		})
	}
}

func TestTestCacheRejectsChangesDuringCommand(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "dirty", true: "new-tree"}[commit], func(t *testing.T) {
			repo, _ := testCacheFixture(t)
			line := "printf changed > owned.txt"
			if commit {
				line += "; git add owned.txt && git commit -qm changed"
			}
			key, expected := testCacheKey(line, repo)
			if code, out := testCacheCall(t, repo, line); code != 0 {
				t.Fatalf("%d %s", code, out)
			}
			if lookupTestCache(key, expected) != nil {
				t.Fatal("command that changed the tree was cached")
			}
		})
	}
}

func TestTestCacheHelp(t *testing.T) {
	var out, errs bytes.Buffer
	if code := taskCommand("test", []string{"--help"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "--fresh") || !strings.Contains(out.String(), "environment change") || errs.Len() != 0 {
		t.Fatalf("help: %d %s %s", code, out.String(), errs.String())
	}
}

func TestTestCacheNestedRepositories(t *testing.T) {
	for _, mode := range []string{"ignored", "ignored-container", "gitfile", "submodule"} {
		t.Run(mode, func(t *testing.T) {
			repo, line := testCacheFixture(t)
			if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("nested/\n"), 0600); err != nil {
				t.Fatal(err)
			}
			testCacheGit(t, repo, "add", ".gitignore")
			testCacheGit(t, repo, "commit", "-qm", "ignore nested repositories")
			// Seed a receipt to prove detection blocks reading an existing pass.
			testCacheCall(t, repo, line)
			if mode == "submodule" {
				sha, err := cacheGit(repo, "rev-parse", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				testCacheGit(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+sha+",nested")
				testCacheGit(t, repo, "commit", "-qm", "add gitlink")
			} else {
				nested := filepath.Join(repo, "nested")
				if mode == "ignored-container" || mode == "gitfile" {
					nested = filepath.Join(nested, "deep", "child")
				}
				if err := os.MkdirAll(nested, 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "gitfile" {
					testCacheGit(t, repo, "worktree", "add", "--detach", nested)
				} else {
					testCacheGit(t, nested, "init", "-q")
				}
			}
			for i := 0; i < 2; i++ {
				code, out := testCacheCall(t, repo, line)
				if code != 0 || strings.Contains(out, "cached") || !strings.Contains(out, "cache: disabled (nested repositories)") {
					t.Fatalf("nested reuse: %d %s", code, out)
				}
			}
			testCacheCount(t, 3)
			key, entry := testCacheKey(line, repo)
			if key != "" || entry.Disabled != "disabled (nested repositories)" {
				t.Fatalf("nested key: %q %#v", key, entry)
			}
			var v map[string]any
			_, out := testCacheCall(t, repo, line, "--json")
			if err := json.Unmarshal([]byte(out), &v); err != nil || v["cache"] != entry.Disabled {
				t.Fatalf("JSON: %s (%v)", out, err)
			}
			// No new receipts may be written while nested repositories exist.
			receipts, err := os.ReadDir(filepath.Join(orchHome(), "test-cache"))
			if err != nil || len(receipts) != 1 {
				t.Fatalf("receipts: %v %v", receipts, err)
			}
		})
	}
}

func TestTestCacheEnvironment(t *testing.T) {
	repo, line := testCacheFixture(t)
	t.Setenv("FOO", "0")
	testCacheCall(t, repo, line)
	if _, out := testCacheCall(t, repo, line); !strings.Contains(out, "cached") {
		t.Fatalf("same env missed: %s", out)
	}
	t.Setenv("FOO", "1")
	if code, out := testCacheCall(t, repo, line); code != 0 || strings.Contains(out, "cached") {
		t.Fatalf("changed env reused: %d %s", code, out)
	}
	if _, out := testCacheCall(t, repo, line); !strings.Contains(out, "cached") {
		t.Fatalf("same changed env missed: %s", out)
	}
	testCacheCount(t, 2)
	// Reproduce the original false pass, including its exit status.
	line += `; test "$FOO" = 0`
	t.Setenv("FOO", "0")
	testCacheCall(t, repo, line)
	t.Setenv("FOO", "1")
	if code, out := testCacheCall(t, repo, line); code != 1 || strings.Contains(out, "cached") {
		t.Fatalf("false pass: %d %s", code, out)
	}
}

func TestTestCacheVolatileEnvironment(t *testing.T) {
	repo, line := testCacheFixture(t)
	testCacheCall(t, repo, line)
	for _, key := range []string{"PWD", "OLDPWD", "SHLVL", "_", "TERM_SESSION_ID", "TMUX", "TMUX_PANE", "WINDOWID", "SSH_AUTH_SOCK", "SSH_CLIENT", "SSH_CONNECTION", "SSH_TTY", "ORCH_RUN", "ORCH_WORKER"} {
		t.Setenv(key, "different-volatile-value")
		if code, out := testCacheCall(t, repo, line); code != 0 || !strings.Contains(out, "cached") {
			t.Fatalf("%s missed: %d %s", key, code, out)
		}
	}
	testCacheCount(t, 1)
	t.Setenv("ORCH_RUN", "")
	for _, key := range []string{"ORCH_MODEL", "ORCH_SOME_FUTURE_OPTION"} {
		t.Setenv(key, "different-setting")
		if _, out := testCacheCall(t, repo, line); strings.Contains(out, "cached") {
			t.Fatalf("%s wrongly excluded: %s", key, out)
		}
	}
	if testCacheEnvironment([]string{"B=2", "A=1"}) != testCacheEnvironment([]string{"A=1", "B=2"}) {
		t.Fatal("environment order affected digest")
	}
}
