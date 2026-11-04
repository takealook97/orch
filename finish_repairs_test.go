package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFinishHeadQuotedDelegation(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	for _, cmd := range []string{
		"orch do --own '*.go' 'Fix it; go test ./...; make check; echo x > x'",
		"orch do --own '*.go' \"Fix it; go test ./...\"",
		"orch test 'go test ./... && make check'",
	} {
		if r := headExecutionReason(hookJSON("Bash", map[string]any{"command": cmd})); r != "" {
			t.Errorf("safe delegation denied: %s: %s", cmd, r)
		}
	}
	for _, cmd := range []string{
		"orch do 'fix it'; go test ./...", "orch do 'fix it' && make check", "orch do 'fix it' > out",
		"orch do \"$(go test ./...)\"", "orch do \"`make check`\"", "orch do 'fix it' | python3 x.py",
		"orch do 'fix it'\ngo test ./...",
	} {
		if r := headExecutionReason(hookJSON("Bash", map[string]any{"command": cmd})); r == "" {
			t.Errorf("execution allowed: %s", cmd)
		}
	}
}

func TestFinishLiteralCompletion(t *testing.T) {
	for _, name := range []string{"*.txt", "[id].txt", ":(glob)*.txt"} {
		t.Run(name, func(t *testing.T) {
			dir := taskRepo(t)
			os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("baseline"), 0600)
			git(t, dir, "add", "unrelated.txt")
			os.WriteFile(filepath.Join(dir, name), []byte("fresh"), 0600)
			hash, e := commitCompletion(dir, "r", "w", "ok", []string{name})
			if e != nil {
				t.Fatal(e)
			}
			files := git(t, dir, "show", "--pretty=format:", "--name-only", hash)
			if strings.Contains(files, "unrelated.txt") || !strings.Contains(files, name) {
				t.Fatalf("wrong commit paths: %s", files)
			}
			if !strings.Contains(git(t, dir, "diff", "--cached", "--name-only"), "unrelated.txt") {
				t.Fatal("index lost")
			}
		})
	}
}

func TestFinishEditToolPrecision(t *testing.T) {
	for _, name := range []string{"mcp__term__write", "x.edit"} {
		if decide(hookJSON(name, map[string]any{"command": "orch spawn worker"}), "worker", true) == "" {
			t.Errorf("unknown edit suffix bypass: %s", name)
		}
	}
	frozenTestGoal(t)
	if r := goalScopeReason(hookJSON("write_file", map[string]any{"content": "x"})); r == "" {
		t.Fatal("missing edit path allowed")
	}
	if r := goalScopeReason(hookJSON("write_file", map[string]any{"absolute_path": "/workspace/outside/a.go", "content": "x"})); r == "" {
		t.Fatal("absolute edit path escaped")
	}
}

func TestFinishProviderFailure(t *testing.T) {
	for _, body := range []string{`{"response":"","denied_actions":[{"tool":"shell"}]}`, `{"response":""}`, `{"response":"ok","denied_actions":["shell"]}`} {
		t.Run(body, func(t *testing.T) {
			taskEnv(t)
			dir := taskRepo(t)
			fakeCLI(t, "agy", "cat <<'JSON'\n"+body+"\nJSON\n")
			var out, errout bytes.Buffer
			code := taskCommand("ask", []string{"--model", "gemini-medium-m", "--cwd", dir, "--json", "question"}, &out, &errout)
			var result Task
			json.Unmarshal(out.Bytes(), &result)
			if code == 0 || result.Status != "failed" {
				t.Fatalf("invalid provider success: code=%d out=%s err=%s", code, out.String(), errout.String())
			}
			entries, _ := os.ReadDir(filepath.Join(orchHome(), "cache", "ask"))
			if len(entries) > 0 {
				t.Fatal("invalid result cached")
			}
		})
	}
}

func TestFinishWaitStateRace(t *testing.T) {
	r, f := reapFixture(t)
	old := herdrRun
	defer func() { herdrRun = old }()
	gets := 0
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "agent" && args[1] == "get" && args[2] == "a" {
			gets++
			if gets == 2 {
				return []byte(`{"result":{"agent_status":"working"}}`), nil
			}
		}
		return f.run(args...)
	}
	outcomes, e := waitWorkers(r.ID, []string{"a"}, 2*time.Second, false)
	if e != nil || len(outcomes) != 1 || outcomes[0].Status != "done" {
		t.Fatalf("dropped worker: %+v %v", outcomes, e)
	}
}

func TestFinishClosedRunStaysClosed(t *testing.T) {
	r, _ := reapFixture(t)
	withRun(r.ID, func(r *Run) error { r.Status = "closed"; r.Workers = nil; return nil })
	old := herdrRun
	defer func() { herdrRun = old }()
	herdrRun = func(args ...string) ([]byte, error) { t.Error("closed run performed cleanup", args); return nil, nil }
	if _, e := closeRun(r.ID, true); e != nil {
		t.Fatal(e)
	}
	if r := mustLoad(t, r.ID); r.Status != "closed" || r.EndRequested {
		t.Fatal("reopened", r.Status, r.EndRequested)
	}
}

func TestFinishReaperStateRace(t *testing.T) {
	r, f := reapFixture(t)
	withRun(r.ID, func(r *Run) error { r.Workers = r.Workers[:1]; return nil })
	old := herdrRun
	defer func() { herdrRun = old }()
	gets := 0
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "agent" && args[1] == "get" {
			gets++
			if gets >= 2 {
				return []byte(`{"result":{"agent_status":"blocked"}}`), nil
			}
		}
		return f.run(args...)
	}
	rp := newReaper(r.ID)
	rp.notWorking["a"] = 1
	var log strings.Builder
	if _, e := rp.once(&log); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(log.String(), "pane closed") || mustLoad(t, r.ID).worker("a").Reaped || !f.live("w1:p2") {
		t.Fatalf("false reap: %s", log.String())
	}
}

func TestFinishDagInterruptHelper(t *testing.T) {
	if os.Getenv("FINISH_DAG_HELPER") != "1" {
		return
	}
	_, e := dagRun(os.Getenv("FINISH_DAG_RUN"), time.Minute, io.Discard)
	if e == nil || !strings.Contains(e.Error(), "again to resume") {
		t.Fatal(e)
	}
}

func TestFinishDagInterruptedResume(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("FINISH_DAG_MARKER", marker)
	r := dagRunFixture(t, "nodes:\n  - {name: gate, run: 'if test -f \"$FINISH_DAG_MARKER\"; then exit 0; fi; touch \"$FINISH_DAG_MARKER\"; sleep 30', role: verify-mech}\n")
	t.Setenv("FINISH_DAG_HELPER", "1")
	t.Setenv("FINISH_DAG_RUN", r.ID)
	exe, _ := os.Executable()
	c := exec.Command(exe, "-test.run=^TestFinishDagInterruptHelper$")
	var output bytes.Buffer
	c.Stdout = &output
	c.Stderr = &output
	if e := c.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { c.Process.Kill() }()
	deadline := time.Now().Add(10 * time.Second)
	for !fileExists(marker) {
		if time.Now().After(deadline) {
			t.Fatal("inline task did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := c.Process.Signal(syscall.SIGINT); e != nil {
		t.Fatal(e)
	}
	if e := c.Wait(); e != nil {
		t.Fatal(e, output.String())
	}
	d, e := loadDag(r)
	if e != nil {
		t.Fatal(e)
	}
	st, e := loadDagState(r, d)
	if e != nil || st.Nodes["gate"].Status != "pending" {
		t.Fatal(st, e)
	}
	st, e = dagRun(r.ID, 5*time.Second, io.Discard)
	if e != nil || st.Nodes["gate"].Status != "done" {
		t.Fatal(st, e)
	}
}

func TestFinishUnknownTaskPreserved(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	w := fixtureWorktree(t, r, root, "w")
	r.Workers = nil
	r.Tasks = []*Task{{ID: "missing", Status: "running", Branch: w.Branch, Worktree: w.Worktree, WorktreeBase: root, WorktreeAuto: true}}
	saveFixture(t, r)
	notes, e := cleanupRunWorktreesChecked(r, false)
	if e == nil || !strings.Contains(e.Error(), "restore missing/corrupt task state") || !fileExists(w.Worktree) {
		t.Fatal(notes, e)
	}
	if live, unknown := taskWriterLive(r.ID, r.Tasks[0]); !live || !unknown {
		t.Fatal("lost unknown writer protection")
	}
}

func TestFinishProviderMixedLog(t *testing.T) {
	raw := []byte("permission warning\n" + `{"response":"","denied_actions":[{"action":"read_file"}]}`)
	answer, _ := parseAnswer("agy", raw)
	if answer != "" || !strings.Contains(providerFailure(raw, answer), "denied") {
		t.Fatal(answer)
	}
}

func TestFinishHeadDagPlan(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	for _, name := range []string{"dag.yaml", "implementation.go", "nested/dag.yaml"} {
		input := hookJSON("apply_patch", map[string]any{"patch": "*** Update File: " + filepath.Join(runDir("run-1"), name) + "\n+x"})
		got := headExecutionReason(input)
		if (got == "") != (name == "dag.yaml") {
			t.Errorf("%s: %s", name, got)
		}
	}
}

func TestFinishNonGitDo(t *testing.T) {
	taskEnv(t)
	dir := t.TempDir()
	fakeCLI(t, "codex", "echo 'must not launch' >&2; exit 99")
	var out, errout bytes.Buffer
	code := taskCommand("do", []string{"--model", "codex-low-h", "--cwd", dir, "--own", "plan.md", "fix"}, &out, &errout)
	if code == 0 || !strings.Contains(errout.String(), "requires a Git repository") {
		t.Fatalf("code %d: %s %s", code, out.String(), errout.String())
	}
}

func TestFinishUninstallReleasesManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	for i := 0; i < 2; i++ {
		repo := t.TempDir()
		os.MkdirAll(filepath.Join(repo, "skill"), 0700)
		os.WriteFile(filepath.Join(repo, "skill/SKILL.md"), []byte("skill"), 0600)
		if _, e := install(repo, false); e != nil {
			t.Fatal(e)
		}
		if _, e := uninstall(repo, true); e != nil {
			t.Fatal(e)
		}
		if fileExists(ownershipPath()) {
			t.Fatal("ownership manifest retained")
		}
	}
}

func TestFinishLegacyForeignUninstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	repo := t.TempDir()
	bin := filepath.Join(home(), ".local/bin/orch")
	os.MkdirAll(filepath.Dir(bin), 0700)
	os.Symlink("/foreign/bin/orch", bin)
	p := constitutionFiles()[0]
	os.MkdirAll(filepath.Dir(p), 0700)
	original := spliceBlock("user", constitutionBlock)
	os.WriteFile(p, []byte(original), 0600)
	os.MkdirAll(filepath.Dir(claudeSettings()), 0700)
	b, _ := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{"command": hookCommand(bin, "claude")}}}}}})
	os.WriteFile(claudeSettings(), b, 0600)
	if _, e := uninstall(repo, true); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(p)
	if string(got) != original {
		t.Fatal("foreign constitution removed")
	}
	got, _ = os.ReadFile(claudeSettings())
	if !bytes.Equal(got, b) {
		t.Fatal("foreign shared hooks removed")
	}
}

func TestFinishUninstallPreservesNumbers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	repo := t.TempDir()
	os.MkdirAll(filepath.Dir(claudeSettings()), 0700)
	command, _ := json.Marshal(hookCommand(filepath.Join(repo, "bin/orch"), "claude"))
	body := `{"number":9007199254740993,"text":"<>&","hooks":{"PreToolUse":[{"hooks":[{"command":` + string(command) + `}]}]}}`
	os.WriteFile(claudeSettings(), []byte(body), 0600)
	if _, e := uninstall(repo, true); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(claudeSettings())
	if !bytes.Contains(got, []byte("9007199254740993")) || !bytes.Contains(got, []byte("<>&")) {
		t.Fatal(string(got))
	}
}

func TestFinishMeasurementFailureIsWarning(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	os.MkdirAll(filepath.Dir(measurementPath()), 0700)
	os.WriteFile(measurementPath(), []byte("{broken\n"), 0600)
	fakeCLI(t, "codex", `echo '{"type":"item.completed","item":{"type":"agent_message","text":"SUMMARY: ok"}}'`)
	var out, errout bytes.Buffer
	if code := taskCommand("ask", []string{"--model", "codex-low-h", "--cwd", dir, "--json", "research"}, &out, &errout); code != 0 {
		t.Fatalf("measurement changed success: %s %s", out.String(), errout.String())
	}
	out.Reset()
	errout.Reset()
	r, e := newRun("test", "light", "", "", true)
	if e != nil {
		t.Fatal(e)
	}
	if code := testCommandForRun(r.ID, "true", dir, 40, 1000, true, &out, &errout); code != 0 {
		t.Fatalf("test measurement changed success: %s %s", out.String(), errout.String())
	}
	if !strings.Contains(errout.String(), "warning") {
		t.Fatal("measurement warning missing")
	}
}

func TestFinishHeadGuardCommands(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	for _, cmd := range []string{"echo x >&out.txt", "go -C . test ./...", "git -c k=v commit -am x", "git --git-dir=.git commit -am x", "git stash", "git tag new", "git rm x", "git mv x y", "git revert HEAD", "git am x", "git branch -D x", "git config x y", "ln -s x y", "chmod +x x", "dd if=x of=y", "rsync x y", "npx x", "uv run x", "gh pr create"} {
		if headExecutionReason(hookJSON("Bash", map[string]any{"command": cmd})) == "" {
			t.Errorf("execution allowed: %s", cmd)
		}
	}
	for _, cmd := range []string{"grep foo 2>/dev/null", "awk '$3>100' data", "git log --format='%h > %s'"} {
		if reason := headExecutionReason(hookJSON("Bash", map[string]any{"command": cmd})); reason != "" {
			t.Errorf("inspection denied: %s", cmd)
		}
	}
}

func TestFinishHeadDraftPlan(t *testing.T) {
	g := frozenTestGoal(t)
	g.Status = "draft"
	if e := saveGoal(g); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ORCH_RUN", "run-1")
	if reason := headExecutionReason(hookJSON("Write", map[string]any{"path": filepath.Join(g.dir(), "goal.md")})); reason != "" {
		t.Fatal(reason)
	}
	for _, p := range []string{filepath.Join(g.dir(), "goal.json"), filepath.Join(goalsDir(), "other", "goal.md")} {
		if headExecutionReason(hookJSON("Write", map[string]any{"path": p})) == "" {
			t.Fatal("invalid planning path allowed", p)
		}
	}
	g.Status = "active"
	saveGoal(g)
	if headExecutionReason(hookJSON("Write", map[string]any{"path": filepath.Join(g.dir(), "goal.md")})) == "" {
		t.Fatal("frozen goal editing allowed")
	}
}

func TestFinishKnowledgeFailureRetry(t *testing.T) {
	r, f := reapFixture(t)
	path := filepath.Join(r.dir(), "knowledge.md")
	os.Remove(path)
	os.Mkdir(path, 0700)
	os.WriteFile(r.resultPath("a"), []byte("status: done\n## Reusable facts\n- Retry fact — source\n"), 0600)
	if _, _, e := collectWorker(r.ID, "a"); e == nil {
		t.Fatal("knowledge error hidden")
	}
	if f.live("w1:p2") {
		t.Fatal("knowledge failure kept pane open")
	}
	os.Remove(path)
	for i := 0; i < 2; i++ {
		if _, st, e := collectWorker(r.ID, "a"); e != nil || st != "done" {
			t.Fatal(st, e)
		}
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "- Retry fact") != 1 {
		t.Fatal(string(b))
	}
}

func TestFinishLegacyDeniedCache(t *testing.T) {
	taskEnv(t)
	dir := t.TempDir()
	answerPath := filepath.Join(dir, "answer.md")
	raw := []byte(`{"response":"","denied_actions":["read_file"]}`)
	os.WriteFile(answerPath, raw, 0600)
	os.WriteFile(filepath.Join(dir, "output.log"), raw, 0600)
	task := Task{Status: "done", ExitCode: 0, Dir: dir}
	b, _ := json.Marshal(task)
	os.WriteFile(filepath.Join(dir, "task.json"), b, 0600)
	storeAskCache("denied", &task)
	if fileExists(askCachePath("denied")) {
		t.Fatal("denied legacy task cached")
	}
	writeAskCacheEntry("legacy", askCacheEntry{dir, answerPath, time.Now()})
	if lookupAskCache("legacy") != nil {
		t.Fatal("legacy denied cache reused")
	}
	os.WriteFile(answerPath, nil, 0600)
	writeAskCacheEntry("empty", askCacheEntry{dir, answerPath, time.Now()})
	if lookupAskCache("empty") != nil {
		t.Fatal("empty cache reused")
	}
}
