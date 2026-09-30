package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDoRepeatedOwn(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"a.txt", "sub/b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "owned files")
	fakeCLI(t, "codex", `printf 'new\n' > a.txt
printf 'new\n' > sub/b.txt
echo '{"type":"item.completed","item":{"type":"agent_message","text":"SUMMARY: ok"}}'
`)
	var out, errout bytes.Buffer
	code := run([]string{"do", "--model", "codex-medium-h", "--own", "a.txt", "--own", "sub/*", "--cwd", dir, "--json", "req"}, strings.NewReader(""), &out, &errout)
	var result Task
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || code != 0 || result.Status != "done" || len(result.ScopeViolations) != 0 || len(result.Changed) != 2 {
		t.Fatalf("code=%d result=%s stderr=%s err=%v", code, out.String(), errout.String(), err)
	}
}

func TestTaskUnknownFlag(t *testing.T) {
	for _, unknown := range []string{"--purpose", "--purpose=docs"} {
		t.Run(unknown, func(t *testing.T) {
			taskEnv(t)
			var out, errout bytes.Buffer
			args := []string{"do", "--model", "codex-medium-h", unknown}
			if unknown == "--purpose" {
				args = append(args, "docs")
			}
			args = append(args, "--own", "x", "req")
			code := run(args, strings.NewReader(""), &out, &errout)
			if code != 2 || !strings.Contains(errout.String(), "unknown flag --purpose for orch do") || !strings.Contains(errout.String(), "--own") || !strings.Contains(errout.String(), "put -- before a prompt that starts with -") {
				t.Fatalf("code=%d stderr=%s", code, errout.String())
			}
		})
	}
}

func TestTaskDashPrompt(t *testing.T) {
	for _, prompt := range []string{"--- FAIL: x", "-prompt", "-"} {
		t.Run(prompt, func(t *testing.T) {
			taskEnv(t)
			fakeCLI(t, "codex", `echo '{"type":"item.completed","item":{"type":"agent_message","text":"SUMMARY: ok"}}'`)
			var out, errout bytes.Buffer
			code := run([]string{"ask", "--model", "codex-medium-h", "--cwd", t.TempDir(), "--json", prompt}, strings.NewReader(""), &out, &errout)
			var result Task
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || code != 0 {
				t.Fatalf("code=%d result=%s stderr=%s err=%v", code, out.String(), errout.String(), err)
			}
			if saved, err := os.ReadFile(filepath.Join(result.Dir, "prompt.md")); err != nil || string(saved) != prompt {
				t.Fatalf("prompt=%q err=%v", saved, err)
			}
		})
	}
}

func TestTaskArgs(t *testing.T) {
	for _, m := range catalog {
		for _, kind := range []string{"ask", "do"} {
			a, err := taskArgs(m, kind, "/repo", "/answer", "prompt")
			if err != nil {
				t.Fatal(err)
			}
			s := strings.Join(a, " ")
			if !strings.Contains(s, "--model") || !strings.Contains(s, "prompt") {
				t.Fatalf("%s %s: %s", m.Key, kind, s)
			}
			switch m.Kind {
			case "codex":
				if kind == "ask" && !strings.Contains(s, "--sandbox read-only") || kind == "do" && !strings.Contains(s, "--dangerously-bypass-approvals-and-sandbox") {
					t.Fatal(s)
				}
			case "claude":
				if kind == "ask" && !strings.Contains(s, "--tools Read,Grep,Glob") || kind == "do" && !strings.Contains(s, "bypassPermissions") {
					t.Fatal(s)
				}
			case "agy":
				if kind == "ask" && !strings.Contains(s, "--mode plan") || kind == "do" && !strings.Contains(s, "--dangerously-skip-permissions") {
					t.Fatal(s)
				}
			}
		}
	}
}

func TestTaskAnswerParsing(t *testing.T) {
	a, u := parseAnswer("claude", []byte(`{"result":"done","usage":{"input_tokens":12,"output_tokens":3,"cached_input_tokens":2}}`))
	if a != "done" || u == nil || u.InputTokens != 12 || u.CachedInputTokens != 2 {
		t.Fatalf("%q %+v", a, u)
	}
	// Real Claude output: cache fields apart from input_tokens, and a per-model modelUsage map.
	a, u = parseAnswer("claude", []byte(`{"result":"ok","usage":{"input_tokens":4,"cache_creation_input_tokens":100,"cache_read_input_tokens":50,"output_tokens":7},"modelUsage":{"claude-sonnet-5":{"inputTokens":4}}}`))
	if a != "ok" || u == nil || u.InputTokens != 154 || u.CachedInputTokens != 50 || u.OutputTokens != 7 {
		t.Fatalf("%q %+v", a, u)
	}
	a, u = parseAnswer("codex", []byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"hello\"}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":4}}"))
	if a != "hello" || u == nil || u.OutputTokens != 4 {
		t.Fatalf("%q %+v", a, u)
	}
	a, u = parseAnswer("agy", []byte("plain answer"))
	if a != "plain answer" || u != nil {
		t.Fatalf("%q %+v", a, u)
	}
	if out, tr := capText("abcdef", 3, "/full"); !tr || !strings.Contains(out, "/full") || !strings.HasPrefix(out, "abc") {
		t.Fatalf("%q %v", out, tr)
	}
}

func TestTestFailureExtraction(t *testing.T) {
	for _, tc := range []struct{ line, want string }{{"--- FAIL: TestThing (0.1s)", "TestThing"}, {"FAILED tests/a.py::test_thing - error", "tests/a.py::test_thing"}, {"  ✕ renders correctly", "renders correctly"}, {"FAIL pkg/name", "pkg/name"}} {
		p := filepath.Join(t.TempDir(), "test.log")
		r, err := executeTest("printf '%s\\n' '"+tc.line+"'; exit 1", t.TempDir(), 4, 1000, p)
		if err != nil || r.ExitCode != 1 || !slices.Contains(r.Failures, tc.want) || !strings.Contains(r.Excerpt, tc.line) {
			t.Fatalf("%+v %v", r, err)
		}
	}
}

func fakeCodex(t *testing.T, target string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
set -eu
test "$ORCH_ROLE" = worker
test -n "$ORCH_RUN"
test -n "$ORCH_WORKER"
test -n "$ORCH_MODEL"
printf 'edited\n' > "$ORCH_FAKE_TARGET"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"SUMMARY: edited file"}}' '{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":2}}'
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORCH_FAKE_TARGET", target)
}

func taskRepo(t *testing.T) string {
	t.Helper()
	dir := gitInit(t)
	if err := os.WriteFile(filepath.Join(dir, "owned.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	return dir
}
func TestDoAndScope(t *testing.T) {
	for _, tc := range []struct{ file, status string }{{"owned.txt", "done"}, {"unowned.txt", "failed"}} {
		t.Run(tc.file, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("ORCH_HOME", filepath.Join(root, "orch"))
			t.Setenv("ORCH_ROLE", "")
			t.Setenv("ORCH_RUN", "")
			t.Setenv("HERDR_PANE_ID", "")
			dir := taskRepo(t)
			fakeCodex(t, filepath.Join(dir, tc.file))
			var out, errout bytes.Buffer
			code := run([]string{"do", "--model", "codex-medium-h", "--own", "owned.txt", "--cwd", dir, "--json", "change file"}, strings.NewReader(""), &out, &errout)
			var v map[string]any
			if e := json.Unmarshal(out.Bytes(), &v); e != nil {
				t.Fatalf("json: %v %s %s", e, out.String(), errout.String())
			}
			if v["status"] != tc.status || code == 0 != (tc.status == "done") {
				t.Fatalf("code=%d result=%s err=%s", code, out.String(), errout.String())
			}
			if tc.status == "failed" && len(v["scope_violations"].([]any)) == 0 {
				t.Fatal(out.String())
			}
			if v["diff_path"] == "" || v["run"] == "" {
				t.Fatal(out.String())
			}
			r, e := loadRun(v["run"].(string))
			if e != nil || r.Status != "closed" {
				t.Fatalf("ad-hoc run %+v %v", r, e)
			}
		})
	}
}
func TestDoFinalTestFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORCH_HOME", filepath.Join(root, "orch"))
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "")
	dir := taskRepo(t)
	fakeCodex(t, filepath.Join(dir, "owned.txt"))
	var out, errout bytes.Buffer
	code := run([]string{"do", "--model", "codex-medium-h", "--own", "owned.txt", "--cwd", dir, "--test", "exit 1", "--json", "change"}, strings.NewReader(""), &out, &errout)
	if code != 1 || !strings.Contains(out.String(), `"status":"failed"`) || !strings.Contains(out.String(), `"test"`) {
		t.Fatalf("%d %s %s", code, out.String(), errout.String())
	}
}
func TestTaskWaitAndWorkerRefusal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORCH_HOME", filepath.Join(root, "orch"))
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "")
	dir := taskRepo(t)
	fakeCodex(t, filepath.Join(dir, "owned.txt"))
	task, err := newTask("do", "codex-medium-h", "change", dir)
	if err != nil {
		t.Fatal(err)
	}
	task.Own = []string{"owned.txt"}
	task.TimeoutMS = 10000
	files, _ := changedFiles(dir, "")
	task.Baseline = snapshot(dir, files)
	if err = saveTask(task); err != nil {
		t.Fatal(err)
	}
	go func() { _ = runTask(task) }()
	outs, err := waitWorkers(task.Run, []string{task.ID}, 5*time.Second, false)
	if err != nil || len(outs) != 1 || outs[0].Status != "done" {
		t.Fatalf("%+v %v", outs, err)
	}
	t.Setenv("ORCH_ROLE", "worker")
	var out, errout bytes.Buffer
	code := run([]string{"ask", "--model", "codex-medium-h", "hello"}, strings.NewReader(""), &out, &errout)
	if code == 0 || !strings.Contains(errout.String(), "Head only") {
		t.Fatalf("%d %s", code, errout.String())
	}
}

func TestAskTruncationAndTestCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ORCH_HOME", filepath.Join(root, "orch"))
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "")
	bin := t.TempDir()
	script := `#!/bin/sh
test "$ORCH_ROLE" = worker || exit 20
printf '%s\n' '{"result":"long answer","usage":{"input_tokens":9,"output_tokens":3}}'
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, errout bytes.Buffer
	code := run([]string{"ask", "--model", "claude-medium-m", "--cwd", root, "--max-output", "4", "--json", "question"}, strings.NewReader(""), &out, &errout)
	if code != 0 {
		t.Fatalf("%d %s", code, errout.String())
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["truncated"] != true || !strings.Contains(v["answer"].(string), "full:") || v["usage"].(map[string]any)["input_tokens"] != float64(9) {
		t.Fatal(out.String())
	}
	if b, err := os.ReadFile(v["answer_path"].(string)); err != nil || string(b) != "long answer" {
		t.Fatalf("%q %v", b, err)
	}
	out.Reset()
	errout.Reset()
	code = run([]string{"test", "--cwd", root, "--max-lines", "2", "--json", "printf '--- FAIL: TestNope\\n'; exit 1"}, strings.NewReader(""), &out, &errout)
	if code != 1 {
		t.Fatalf("%d %s", code, out.String())
	}
	var tr map[string]any
	if err := json.Unmarshal(out.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if tr["exit_code"] != float64(1) || tr["test"].(map[string]any)["log"] == "" {
		t.Fatal(out.String())
	}
}

// taskEnv isolates orch state for a task test with no Head pane.
func taskEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ORCH_HOME", filepath.Join(t.TempDir(), "orch"))
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "")
}

// fakeCLI puts an executable script named name first on PATH.
func fakeCLI(t *testing.T, name, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

func TestInterruptedDoKillsChildGroup(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	pids := filepath.Join(t.TempDir(), "grandchild")
	t.Setenv("ORCH_FAKE_PIDS", pids)
	fakeCLI(t, "codex", `sleep 60 &
echo $! > "$ORCH_FAKE_PIDS.tmp" && mv "$ORCH_FAKE_PIDS.tmp" "$ORCH_FAKE_PIDS"
wait
`)
	type result struct {
		code int
		out  string
	}
	done := make(chan result, 1)
	go func() {
		var out, errout bytes.Buffer
		code := run([]string{"do", "--model", "codex-medium-h", "--own", "owned.txt", "--cwd", dir, "--json", "change"}, strings.NewReader(""), &out, &errout)
		done <- result{code, out.String() + errout.String()}
	}()
	var grandchild int
	for i := 0; i < 100 && grandchild == 0; i++ {
		if b, err := os.ReadFile(pids); err == nil {
			grandchild, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("fake CLI did not start")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted do did not return")
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(res.out, "\n", 2)[0]), &v); err != nil {
		t.Fatalf("%v %s", err, res.out)
	}
	if res.code != 1 || v["status"] != "failed" || v["note"] != "interrupted" {
		t.Fatalf("code=%d %s", res.code, res.out)
	}
	for i := 0; i < 50 && pidAlive(grandchild); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if pidAlive(grandchild) {
		t.Fatalf("grandchild %d survived the interrupt", grandchild)
	}
	saved, err := readTask(v["run"].(string), v["task"].(string))
	if err != nil || saved.Status != "failed" || saved.Note != "interrupted" {
		t.Fatalf("%+v %v", saved, err)
	}
}

func TestWaitDetectsDeadRunner(t *testing.T) {
	taskEnv(t)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		pid  int
		beat time.Time
	}{{"pid gone", dead.Process.Pid, time.Now()}, {"stale heartbeat", 0, time.Now().Add(-2 * time.Minute)}} {
		t.Run(tc.name, func(t *testing.T) {
			task, err := newTask("ask", "claude-medium-m", "q", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			task.Pid, task.Heartbeat = tc.pid, tc.beat
			if err := saveTask(task); err != nil {
				t.Fatal(err)
			}
			outs, err := waitWorkers(task.Run, []string{task.ID}, 10*time.Second, false)
			if err != nil || len(outs) != 1 || outs[0].Status != "failed" || !strings.Contains(outs[0].Detail, "runner died") {
				t.Fatalf("%+v %v", outs, err)
			}
			if saved, _ := readTask(task.Run, task.ID); saved.Status != "failed" {
				t.Fatalf("not saved: %+v", saved)
			}
		})
	}
}

func TestTaskChildRefusesFinishedOrClaimedTask(t *testing.T) {
	taskEnv(t)
	fakeCLI(t, "claude", `echo '{"result":"rerun"}'`)
	task, err := newTask("ask", "claude-medium-m", "q", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	answer := filepath.Join(task.Dir, "answer.md")
	os.WriteFile(answer, []byte("original"), 0600)
	task.Status = "done"
	_ = saveTask(task)
	var out, errout bytes.Buffer
	if code := run([]string{"__task", "--run", task.Run, task.ID}, strings.NewReader(""), &out, &errout); code == 0 {
		t.Fatalf("done task was re-run: %s", out.String())
	}
	if b, _ := os.ReadFile(answer); string(b) != "original" {
		t.Fatalf("answer overwritten: %q", b)
	}
	// A running task that another live process runs is not started twice.
	task.Status, task.Pid = "running", os.Getppid()
	_ = saveTask(task)
	errout.Reset()
	if code := run([]string{"__task", "--run", task.Run, task.ID}, strings.NewReader(""), &out, &errout); code == 0 || !strings.Contains(errout.String(), "already run") {
		t.Fatalf("%d %s", code, errout.String())
	}
}

func TestDiffPatchCoversOnlyTaskChanges(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("unrelated\n"), 0600) // dirty before the task
	fakeCLI(t, "codex", `printf 'new\n' > owned.txt
mkdir -p sub
printf 'line1\nline2' > sub/new.txt
: > sub/empty.txt
echo '{"type":"item.completed","item":{"type":"agent_message","text":"SUMMARY: ok"}}'
`)
	var out, errout bytes.Buffer
	code := run([]string{"do", "--model", "codex-medium-h", "--own", "owned.txt,sub/*", "--cwd", dir, "--json", "change"}, strings.NewReader(""), &out, &errout)
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || code != 0 {
		t.Fatalf("%d %v %s %s", code, err, out.String(), errout.String())
	}
	patch, _ := os.ReadFile(v["diff_path"].(string))
	if strings.Contains(string(patch), "dirty.txt") {
		t.Fatalf("patch includes a change from before the task:\n%s", patch)
	}
	if v["diffstat"] != "3 files, +3 -1" {
		t.Fatalf("diffstat %v\n%s", v["diffstat"], patch)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", "-q", dir, clone).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	c := exec.Command("git", "-C", clone, "apply", "--check", v["diff_path"].(string))
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("patch does not apply: %v %s\n%s", err, out, patch)
	}
}

func TestScopeFromSubdirectory(t *testing.T) {
	for _, tc := range []struct{ target, status string }{{"sub/a.txt", "done"}, {"top.txt", "failed"}} {
		t.Run(tc.target, func(t *testing.T) {
			taskEnv(t)
			dir := taskRepo(t)
			os.MkdirAll(filepath.Join(dir, "sub"), 0700)
			fakeCodex(t, filepath.Join(dir, tc.target))
			var out, errout bytes.Buffer
			run([]string{"do", "--model", "codex-medium-h", "--own", "*.txt", "--cwd", filepath.Join(dir, "sub"), "--json", "change"}, strings.NewReader(""), &out, &errout)
			var v map[string]any
			if err := json.Unmarshal(out.Bytes(), &v); err != nil {
				t.Fatalf("%v %s %s", err, out.String(), errout.String())
			}
			changed, _ := v["changed"].([]any)
			if v["status"] != tc.status || len(changed) != 1 || changed[0] != tc.target {
				t.Fatal(out.String())
			}
		})
	}
}

func TestChangedFilesCountsBothSidesOfRename(t *testing.T) {
	dir := taskRepo(t)
	git(t, dir, "mv", "owned.txt", "moved.txt")
	files, err := changedFiles(dir, "")
	if err != nil || !slices.Contains(files, "owned.txt") || !slices.Contains(files, "moved.txt") {
		t.Fatalf("%v %v", files, err)
	}
}

func TestExcerptKeepsCompileErrorAndMarksTruncation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.log")
	r, err := executeTest(`printf '# m\nkeep.go:1:1: expected package, found b\nFAIL\tm [setup failed]\n'; exit 1`, t.TempDir(), 40, 5000, p)
	if err != nil || !strings.Contains(r.Excerpt, "expected package") || strings.Contains(r.Excerpt, "truncated") {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = executeTest(`i=0; while [ $i -lt 30 ]; do echo "--- FAIL: T$i"; i=$((i+1)); done; exit 1`, t.TempDir(), 5, 5000, p)
	if err != nil || !strings.Contains(r.Excerpt, "truncated; full: "+p) || len(r.Failures) != 21 || !strings.Contains(r.Failures[20], "truncated") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestTestTimeoutStatus(t *testing.T) {
	taskEnv(t)
	var out, errout bytes.Buffer
	code := run([]string{"test", "--timeout", "200", "--json", "sleep 5"}, strings.NewReader(""), &out, &errout)
	if code != 1 || !strings.Contains(out.String(), `"status":"timeout"`) {
		t.Fatalf("%d %s %s", code, out.String(), errout.String())
	}
}

func TestLeadingDashPrompt(t *testing.T) {
	taskEnv(t)
	fakeCLI(t, "claude", `echo '{"result":"ok"}'`)
	for _, args := range [][]string{
		{"ask", "--model", "claude-medium-m", "--json", "-1 or 1?"},
		{"ask", "--model", "claude-medium-m", "--json", "--", "--- a divider"},
	} {
		var out, errout bytes.Buffer
		if code := run(args, strings.NewReader(""), &out, &errout); code != 0 {
			t.Fatalf("%v: %d %s", args, code, errout.String())
		}
		var v map[string]any
		_ = json.Unmarshal(out.Bytes(), &v)
		prompt, _ := os.ReadFile(filepath.Join(v["dir"].(string), "prompt.md"))
		if string(prompt) != args[len(args)-1] {
			t.Fatalf("prompt %q", prompt)
		}
	}
	var out, errout bytes.Buffer
	if code := run([]string{"test", "--json", "-x"}, strings.NewReader(""), &out, &errout); code != 1 || strings.Contains(errout.String(), "flag provided") {
		t.Fatalf("%d %s", code, errout.String())
	}
}

func TestCollectSummaryIsRuneSafe(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	os.WriteFile(r.resultPath("a"), []byte("# Result: a\n- summary: "+strings.Repeat("ü", 400)+"\n- status: done\n"), 0600)
	_ = withRun(r.ID, func(x *Run) error {
		x.Workers = append(x.Workers, &Worker{Name: "a", Status: "prompted", Result: r.resultPath("a")})
		return nil
	})
	out, err := collect(r.ID, "a")
	if err != nil || !utf8.ValidString(out) || !strings.Contains(out, "ü…") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestAdhocRunNeverActiveAndHidden(t *testing.T) {
	taskEnv(t)
	fakeCLI(t, "claude", `echo '{"result":"ok"}'`)
	var out, errout bytes.Buffer
	if code := run([]string{"ask", "--model", "claude-medium-m", "--json", "q"}, strings.NewReader(""), &out, &errout); code != 0 {
		t.Fatalf("%d %s", code, errout.String())
	}
	var v map[string]any
	_ = json.Unmarshal(out.Bytes(), &v)
	id := v["run"].(string)
	b, _ := os.ReadFile(filepath.Join(runDir(id), "run.json"))
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["status"] != "closed" || raw["adhoc"] != true {
		t.Fatalf("%s", b)
	}
	if got, err := resolveRun(""); err == nil {
		t.Fatalf("ad-hoc run resolved as active: %s", got)
	}
	out.Reset()
	run([]string{"run", "list"}, strings.NewReader(""), &out, &errout)
	if strings.Contains(out.String(), id) {
		t.Fatal(out.String())
	}
	out.Reset()
	run([]string{"run", "list", "--all"}, strings.NewReader(""), &out, &errout)
	if !strings.Contains(out.String(), id) {
		t.Fatal(out.String())
	}
	runs, _ := allRuns()
	if rep := pruneAdhoc(runs, time.Hour); len(rep) != 0 {
		t.Fatalf("young run pruned: %v", rep)
	}
	_ = withRun(id, func(r *Run) error { r.Created = time.Now().Add(-8 * 24 * time.Hour); return nil })
	runs, _ = allRuns()
	if rep := pruneAdhoc(runs, adhocRetention); len(rep) != 1 || fileExists(runDir(id)) {
		t.Fatalf("old ad-hoc run kept: %v", rep)
	}
}
