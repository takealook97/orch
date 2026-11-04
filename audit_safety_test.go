package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAuditAgyBodiesAndPaths(t *testing.T) {
	frozenTestGoal(t)
	for _, tool := range []string{"write_file", "edit_file"} {
		in := hookJSON(tool, map[string]any{"path": "/workspace/outside/a.go", "content": "orch status; herdr pane split"})
		if got := decide(in, "worker", true); got != "" {
			t.Fatalf("%s edit body denied: %s", tool, got)
		}
		if got := decide(in, "", true); got == "" {
			t.Fatalf("%s Head edit escaped goal scope", tool)
		}
		in = hookJSON(tool, map[string]any{"path": "/workspace/src/a.go", "content": "orch status"})
		if got := decide(in, "", true); got != "" {
			t.Fatal(got)
		}
	}
	if decide(hookJSON("run_command", map[string]any{"CommandLine": "orch status"}), "worker", true) == "" {
		t.Fatal("real worker command allowed")
	}
}

func TestAuditDagCorruptionAndAtomicFailure(t *testing.T) {
	taskEnv(t)
	r, e := newRun("audit", "light", "", "", true)
	if e != nil {
		t.Fatal(e)
	}
	d := Dag{Nodes: []DagNode{{Name: "x"}}}
	for _, body := range []string{"{", `{"nodes":null}`, `{"nodes":{"x":null}}`, `{"nodes":{"x":{"status":"invalid"}}}`} {
		os.WriteFile(dagStatePath(r), []byte(body), 0600)
		if _, e := loadDagState(r, d); e == nil {
			t.Fatalf("accepted %s", body)
		}
		b, _ := os.ReadFile(dagStatePath(r))
		if string(b) != body {
			t.Fatal("corrupt evidence replaced")
		}
	}
	os.Remove(dagStatePath(r))
	os.Mkdir(dagStatePath(r), 0700)
	if e := saveDagState(r, &dagState{Nodes: map[string]*dagNodeState{}}); e == nil {
		t.Fatal("DAG persistence error swallowed")
	}
}

func TestAuditDagSchedulerExclusion(t *testing.T) {
	r := dagRunFixture(t, "nodes:\n  - {name: gate, run: 'true', role: verify-mech}\n")
	unlock, e := exclusiveFile(filepath.Join(r.dir(), ".dag-scheduler.lock"), false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := dagRun(r.ID, time.Second, io.Discard); e == nil {
		t.Fatal("second scheduler admitted")
	}
	unlock()
	if st, e := dagRun(r.ID, time.Second, io.Discard); e != nil || st.Nodes["gate"].Status != "done" {
		t.Fatal(st, e)
	}
}

func TestAuditGoalApplicability(t *testing.T) {
	g := frozenTestGoal(t)
	os.WriteFile(filepath.Join(g.dir(), "goal.json"), []byte("{}"), 0600)
	if _, e := activeGoal(headID()); e == nil || !strings.Contains(e.Error(), "restore") {
		t.Fatal(e)
	}
	t.Setenv("HERDR_PANE_ID", "unrelated-head")
	if g, e := activeGoal(headID()); e != nil || g != nil {
		t.Fatal(g, e)
	}
	t.Setenv("HERDR_PANE_ID", "goal-head")
	os.Remove(filepath.Join(g.dir(), "goal.json"))
	if g, e := activeGoal(headID()); e != nil || g != nil {
		t.Fatal("incomplete new goal treated as corrupt", g, e)
	}
}

func TestAuditFinalSaveFailureDoesNotCache(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	fakeCLI(t, "codex", `rm -f "$ORCH_HOME/runs/$ORCH_RUN/.lock"
mkdir "$ORCH_HOME/runs/$ORCH_RUN/.lock"
echo '{"type":"item.completed","item":{"type":"agent_message","text":"SUMMARY: ok"}}'`)
	var out, errout bytes.Buffer
	if code := taskCommand("ask", []string{"--model", "codex-low-h", "--cwd", dir, "--json", "audit save"}, &out, &errout); code == 0 {
		t.Fatal("final persistence failure returned success", out.String())
	}
	entries, _ := os.ReadDir(filepath.Join(orchHome(), "cache", "ask"))
	if len(entries) > 0 {
		t.Fatal("non-durable success cached")
	}
	if !strings.Contains(errout.String(), "persist final task") {
		t.Fatal(errout.String())
	}
}

func TestAuditControlTimeoutKillsDescendants(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "child")
	script := filepath.Join(dir, "hanging")
	body := "#!/bin/sh\nsleep 60 &\necho $! > \"$AUDIT_CHILD_PID\"\nwait\n"
	os.WriteFile(script, []byte(body), 0700)
	t.Setenv("AUDIT_CHILD_PID", pidfile)
	start := time.Now()
	_, e := boundedControl(script, nil, os.Environ(), time.Second)
	if e == nil || !strings.Contains(e.Error(), "deadline") || time.Since(start) > 3*time.Second {
		t.Fatal(e, time.Since(start))
	}
	b, e := os.ReadFile(pidfile)
	if e != nil {
		t.Fatalf("child pid missing; command error=%v; read=%v", e, e)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(time.Second)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pidAlive(pid) {
		state, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if !strings.Contains(string(state), "Z") {
			t.Fatalf("child survived: pid=%d state=%s", pid, state)
		}
	}
}

func TestAuditAccountTimeoutRedactsValues(t *testing.T) {
	taskEnv(t)
	old := controlTimeout
	controlTimeout = 100 * time.Millisecond
	defer func() { controlTimeout = old }()
	t.Setenv("ORCH_ACCOUNT_CMD", "printf 'TOKEN=private-value\\n' >&2; sleep 60")
	_, e := hookAccount("codex", nil)
	if e == nil || strings.Contains(e.Error(), "private-value") {
		t.Fatal(e)
	}
}

func TestAuditPromptUncertainKeepsBaseline(t *testing.T) {
	r, dir, _ := worktreeFixture(t)
	w := &Worker{Name: "w", Pane: "p", Cwd: dir, Result: r.resultPath("w")}
	r.Workers = []*Worker{w}
	saveFixture(t, r)
	os.WriteFile(r.contractPath("w"), []byte(contractTemplate(r, "w", "produce", "codex-low-h")), 0600)
	os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("before"), 0600)
	old, times := herdrRun, timings
	defer func() { herdrRun = old; timings = times }()
	timings.TrustSettlePrompt = 0
	sends := 0
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pane" && args[1] == "send-text" {
			sends++
			return nil, errors.New("transport lost after possible send")
		}
		return []byte(`{"result":{}}`), nil
	}
	if e := prompt(r.ID, "w", "", false, 0); e == nil {
		t.Fatal("send failure lost")
	}
	first := mustLoad(t, r.ID).worker("w").Baseline["existing.txt"]
	os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("after"), 0600)
	if e := prompt(r.ID, "w", "", false, 0); e == nil || !strings.Contains(e.Error(), "uncertain") {
		t.Fatal(e)
	}
	if sends != 1 || first == "" || mustLoad(t, r.ID).worker("w").Baseline["existing.txt"] != first {
		t.Fatal("uncertain retry resent or changed baseline", sends)
	}
}

// This helper is a separate OS process: flock serializes delivery across CLI invocations.
func TestAuditPromptProcess(t *testing.T) {
	if os.Getenv("AUDIT_PROMPT_HELPER") != "1" {
		return
	}
	herdrRun = func(args ...string) ([]byte, error) {
		return boundedControl("herdr", args, os.Environ(), controlTimeout)
	}
	timings.TrustSettlePrompt = 0
	timings.KeyGap = 0
	timings.Delivery = time.Second
	if e := prompt(os.Getenv("AUDIT_PROMPT_RUN"), "w", "", false, 0); e != nil {
		t.Fatal(e)
	}
}
func TestAuditPromptAcrossProcesses(t *testing.T) {
	r, _, _ := worktreeFixture(t)
	r.Workers = []*Worker{{Name: "w", Pane: "p", Result: r.resultPath("w")}}
	saveFixture(t, r)
	os.WriteFile(r.contractPath("w"), []byte(contractTemplate(r, "w", "recon", "codex-low-h")), 0600)
	os.WriteFile(r.resultPath("w"), []byte("status: done\n"), 0600)
	dir := t.TempDir()
	count := filepath.Join(dir, "sends")
	os.WriteFile(filepath.Join(dir, "herdr"), []byte("#!/bin/sh\nif [ \"$1 $2\" = 'pane send-text' ]; then echo sent >> \"$AUDIT_SENDS\"; fi\nprintf '%s' '{\"result\":{}}'\n"), 0700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AUDIT_SENDS", count)
	t.Setenv("AUDIT_PROMPT_HELPER", "1")
	t.Setenv("AUDIT_PROMPT_RUN", r.ID)
	exe, _ := os.Executable()
	a := exec.Command(exe, "-test.run=^TestAuditPromptProcess$")
	b := exec.Command(exe, "-test.run=^TestAuditPromptProcess$")
	var ao, bo bytes.Buffer
	a.Stdout = &ao
	a.Stderr = &ao
	b.Stdout = &bo
	b.Stderr = &bo
	if e := a.Start(); e != nil {
		t.Fatal(e)
	}
	if e := b.Start(); e != nil {
		t.Fatal(e)
	}
	ae, be := a.Wait(), b.Wait()
	if ae != nil || be != nil {
		t.Fatal(ae, be, ao.String(), bo.String())
	}
	data, _ := os.ReadFile(count)
	if strings.Count(string(data), "sent") != 1 {
		t.Fatal(string(data))
	}
}

func TestAuditCommitPreservesUnrelatedIndex(t *testing.T) {
	r, root, _ := worktreeFixture(t)
	w := fixtureWorktree(t, r, root, "w")
	os.WriteFile(filepath.Join(w.Worktree, "unrelated.txt"), []byte("baseline"), 0600)
	git(t, w.Worktree, "add", "unrelated.txt")
	os.WriteFile(filepath.Join(w.Worktree, "fresh.txt"), []byte("fresh"), 0600)
	hash, e := commitCompletion(w.Worktree, r.ID, w.Name, "accepted", []string{"fresh.txt"})
	if e != nil {
		t.Fatal(e)
	}
	files := git(t, w.Worktree, "show", "--pretty=format:", "--name-only", hash)
	if strings.Contains(files, "unrelated") || !strings.Contains(files, "fresh.txt") {
		t.Fatal(files)
	}
	staged := git(t, w.Worktree, "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "unrelated.txt") {
		t.Fatal("preexisting index lost", staged)
	}
	again, e := commitCompletion(w.Worktree, r.ID, w.Name, "accepted", []string{"fresh.txt"})
	if e != nil || hash != again {
		t.Fatal("commit retry not idempotent", hash, again, e)
	}
}

func TestAuditDAGCommitBeforeDependency(t *testing.T) {
	r, root, base := worktreeFixture(t)
	w := fixtureWorktree(t, r, root, "impl")
	w.Role = "produce"
	w.Key = "codex-high-m"
	w.Status = "done"
	w.Contract = r.contractPath("impl")
	w.Result = r.resultPath("impl")
	body := strings.Replace(contractTemplate(r, "impl", "produce", w.Key), "<hash or none>", base, 1)
	body = strings.Replace(body, "- Owned files (glob):", "- Owned files (glob): owned.txt\n- Previous owned example:", 1)
	os.WriteFile(w.Contract, []byte(body), 0600)
	os.WriteFile(w.Result, []byte("status: done\n"), 0600)
	os.WriteFile(filepath.Join(w.Worktree, "owned.txt"), []byte("accepted"), 0600)
	saveFixture(t, r)
	y := fmt.Sprintf("nodes:\n  - {name: impl, model: codex-high-m, role: produce}\n  - {name: after, run: 'git log -1 --format=%%s | grep %s/impl', cwd: %q, role: verify-mech, needs: [impl]}\n", r.ID, w.Worktree)
	os.WriteFile(filepath.Join(r.dir(), "dag.yaml"), []byte(y), 0600)
	saveDagState(r, &dagState{Nodes: map[string]*dagNodeState{"impl": {Status: "running", Worker: "impl"}, "after": {Status: "pending"}}})
	st, e := dagRun(r.ID, 5*time.Second, io.Discard)
	if e != nil || st.Nodes["after"].Status != "done" || !mustLoad(t, r.ID).worker("impl").Committed {
		t.Fatal(st, e)
	}
}

func TestAuditDoTestScopeOnFailureAndIgnoredArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		ignored       bool
		want          string
	}{
		{"failed test", "printf generated > outside.txt; false", false, "failed"},
		{"ignored artifact", "printf generated > outside.txt", true, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			taskEnv(t)
			dir := taskRepo(t)
			if tc.ignored {
				os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("outside.txt\n"), 0600)
				git(t, dir, "add", ".gitignore")
				git(t, dir, "commit", "-qm", "ignore artifact")
			}
			os.WriteFile(filepath.Join(dir, "preexisting.txt"), []byte("preserve"), 0600)
			fakeCodex(t, filepath.Join(dir, "owned.txt"))
			var out, errout bytes.Buffer
			code := taskCommand("do", []string{"--model", "codex-low-h", "--own", "owned.txt", "--cwd", dir, "--test", tc.command, "--json", "edit"}, &out, &errout)
			var got Task
			if e := json.Unmarshal(out.Bytes(), &got); e != nil {
				t.Fatal(e, out.String(), errout.String())
			}
			if got.Status != tc.want || (code == 0) != (tc.want == "done") {
				t.Fatal(code, out.String(), errout.String())
			}
			if tc.ignored && len(got.ScopeViolations) != 0 {
				t.Fatal(got.ScopeViolations)
			}
			if !tc.ignored && (len(got.ScopeViolations) != 1 || got.ScopeViolations[0] != "outside.txt") {
				t.Fatal(got.ScopeViolations)
			}
			b, _ := os.ReadFile(filepath.Join(dir, "preexisting.txt"))
			if string(b) != "preserve" {
				t.Fatal("baseline changed")
			}
			diff, _ := os.ReadFile(got.DiffPath)
			if !tc.ignored && !bytes.Contains(diff, []byte("outside.txt")) {
				t.Fatal("final diff missing test changes")
			}
		})
	}
}

func TestAuditPromptBlockedBeforeSendIsRetryable(t *testing.T) {
	r, dir, _ := worktreeFixture(t)
	r.Workers = []*Worker{{Name: "w", Pane: "p", Cwd: dir, Result: r.resultPath("w")}}
	saveFixture(t, r)
	os.WriteFile(r.contractPath("w"), []byte(contractTemplate(r, "w", "produce", "codex-low-h")), 0600)
	os.WriteFile(filepath.Join(dir, "baseline.txt"), []byte("before"), 0600)
	old, times := herdrRun, timings
	defer func() { herdrRun = old; timings = times }()
	timings.TrustSettlePrompt = 0
	timings.KeyGap = 0
	timings.Delivery = time.Second
	blocked := true
	sends := 0
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "agent" && args[1] == "get" && blocked {
			return []byte(`{"result":{"agent_status":"blocked"}}`), nil
		}
		if len(args) > 1 && args[0] == "pane" && args[1] == "send-text" {
			sends++
		}
		return []byte(`{"result":{}}`), nil
	}
	if e := prompt(r.ID, "w", "", false, 0); e == nil {
		t.Fatal("approval block ignored")
	}
	before := mustLoad(t, r.ID).worker("w").Baseline["baseline.txt"]
	if mustLoad(t, r.ID).worker("w").ContractHash != "" {
		t.Fatal("definitely unsent prompt retained contract hash")
	}
	body, _ := os.ReadFile(r.contractPath("w"))
	os.WriteFile(r.contractPath("w"), append(body, []byte("\nRetry clarification.\n")...), 0600)
	os.WriteFile(filepath.Join(dir, "baseline.txt"), []byte("after"), 0600)
	os.WriteFile(r.resultPath("w"), []byte("status: done\n"), 0600)
	blocked = false
	if e := prompt(r.ID, "w", "", false, 0); e != nil {
		t.Fatal(e)
	}
	if sends != 1 || before == "" || mustLoad(t, r.ID).worker("w").Baseline["baseline.txt"] != before {
		t.Fatal("retry changed baseline or duplicated send")
	}
}

func TestAuditDAGProcess(t *testing.T) {
	if os.Getenv("AUDIT_DAG_HELPER") != "1" {
		return
	}
	_, err := dagRun(os.Getenv("AUDIT_DAG_RUN"), 5*time.Second, io.Discard)
	if err != nil && !strings.Contains(err.Error(), "already in use") {
		t.Fatal(err)
	}
}

func TestAuditDAGAcrossProcessesLaunchesOnce(t *testing.T) {
	r := dagRunFixture(t, "nodes:\n  - {name: gate, run: 'echo launch >> \"$AUDIT_DAG_COUNT\"; sleep 0.2', role: verify-mech}\n")
	count := filepath.Join(t.TempDir(), "count")
	t.Setenv("AUDIT_DAG_HELPER", "1")
	t.Setenv("AUDIT_DAG_RUN", r.ID)
	t.Setenv("AUDIT_DAG_COUNT", count)
	exe, _ := os.Executable()
	a := exec.Command(exe, "-test.run=^TestAuditDAGProcess$")
	b := exec.Command(exe, "-test.run=^TestAuditDAGProcess$")
	var ao, bo bytes.Buffer
	a.Stdout = &ao
	a.Stderr = &ao
	b.Stdout = &bo
	b.Stderr = &bo
	if e := a.Start(); e != nil {
		t.Fatal(e)
	}
	if e := b.Start(); e != nil {
		t.Fatal(e)
	}
	ae, be := a.Wait(), b.Wait()
	if ae != nil || be != nil {
		t.Fatal(ae, be, ao.String(), bo.String())
	}
	data, _ := os.ReadFile(count)
	if strings.Count(string(data), "launch") != 1 {
		t.Fatal("duplicate or missing launch", string(data))
	}
}

func TestAuditAtomicStateReaders(t *testing.T) {
	taskEnv(t)
	r, e := newRun("atomic", "light", "", "", true)
	if e != nil {
		t.Fatal(e)
	}
	a := &dagState{Nodes: map[string]*dagNodeState{"x": {Status: "pending"}}}
	b := &dagState{Nodes: map[string]*dagNodeState{"x": {Status: "done"}}}
	if e := saveDagState(r, a); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 30; i++ {
			state := a
			if i%2 == 0 {
				state = b
			}
			if e := saveDagState(r, state); e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	d := Dag{Nodes: []DagNode{{Name: "x"}}}
	for {
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
			return
		default:
			st, e := loadDagState(r, d)
			if e != nil {
				t.Fatal("partial state observed", e)
			}
			if st.Nodes["x"].Status != "pending" && st.Nodes["x"].Status != "done" {
				t.Fatal(st)
			}
		}
	}
}

func TestAuditControlWrappersAndWaitDeadlines(t *testing.T) {
	dir := t.TempDir()
	script := []byte("#!/bin/sh\nsleep 60\n")
	for _, name := range []string{"herdr", "hc", "herdr-app"} {
		if e := os.WriteFile(filepath.Join(dir, name), script, 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := controlTimeout
	controlTimeout = time.Second
	defer func() { controlTimeout = old }()
	for _, call := range []func() ([]byte, error){func() ([]byte, error) { return runHerdrControl("pane", "list") }, func() ([]byte, error) { return muxCommand("hc", "ls") }, func() ([]byte, error) { return muxCommand("herdr-app", "list") }} {
		start := time.Now()
		if _, e := call(); e == nil || !strings.Contains(e.Error(), "deadline") || time.Since(start) > 3*time.Second {
			t.Fatal(e, time.Since(start))
		}
	}
	if controlDeadline("herdr", []string{"agent", "wait", "w", "--timeout", "30000"}) != 31*time.Second {
		t.Fatal("Herdr wait budget lost")
	}
	if controlDeadline("hc", []string{"wait", "w", "--timeout", "30"}) != 31*time.Second {
		t.Fatal("hc wait budget lost")
	}
}

func TestAuditWriterUsesFreshChildGroupRecord(t *testing.T) {
	r, dir, _ := worktreeFixture(t)
	task, e := newTaskIn(r.ID, "do", "", "writer", dir)
	if e != nil {
		t.Fatal(e)
	}
	task.Pid = 99999999
	task.Pgid = 0
	if e := saveTask(task); e != nil {
		t.Fatal(e)
	}
	// Reproduce the normal heartbeat path: task.json changes without rewriting run.json.
	if e := editTask(task, func(x *Task) { x.Pgid = syscall.Getpgrp() }); e != nil {
		t.Fatal(e)
	}
	if refs := liveWritersIn(dir, nil); len(refs) != 1 {
		t.Fatal("fresh child group was ignored", refs)
	}
}

func TestAuditDAGUncertainLaunchIsNotRepeated(t *testing.T) {
	r := dagRunFixture(t, "nodes:\n  - {name: gate, run: 'true', role: verify-mech}\n")
	for _, status := range []string{"starting", "running"} {
		if e := saveDagState(r, &dagState{Nodes: map[string]*dagNodeState{"gate": {Status: status, Worker: "run"}}}); e != nil {
			t.Fatal(e)
		}
		if _, e := dagRun(r.ID, time.Second, io.Discard); e == nil || !strings.Contains(e.Error(), "uncertain") {
			t.Fatal(e)
		}
	}
}
