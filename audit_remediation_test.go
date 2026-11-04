package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArchAuditDoFinalScope(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	fakeCodex(t, filepath.Join(dir, "owned.txt"))
	var out, errs bytes.Buffer
	code := taskCommand("do", []string{"--model", "codex-low-h", "--own", "owned.txt", "--cwd", dir, "--test", "printf generated > outside.txt", "--json", "edit"}, &out, &errs)
	var got Task
	if e := json.Unmarshal(out.Bytes(), &got); e != nil {
		t.Fatal(e, out.String(), errs.String())
	}
	if code == 0 || got.Status != "failed" || len(got.ScopeViolations) != 1 {
		t.Fatal(code, out.String(), errs.String())
	}
	if _, e := os.Stat(filepath.Join(dir, "outside.txt")); e != nil {
		t.Fatal(e)
	}
}
func TestArchAuditWriterGroup(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	r, e := newRun("audit", "light", "", "", true)
	if e != nil {
		t.Fatal(e)
	}
	x := &Task{ID: "task-1", Kind: "do", Status: "running", Cwd: dir, Pid: 99999999, Pgid: syscall.Getpgrp()}
	r.Tasks = append(r.Tasks, x)
	saveFixture(t, r)
	if why := taskDead(x); why != "" {
		t.Fatal(why)
	}
	if refs := liveWritersIn(dir, nil); len(refs) != 1 {
		t.Fatal(refs)
	}
}
func TestArchAuditCacheCollision(t *testing.T) {
	taskEnv(t)
	t.Setenv("ORCH_ASK_CACHE_TTL", "168")
	dir := taskRepo(t)
	a := askCacheKey("How many spaces in 'a b'?", "m", 100, dir)
	b := askCacheKey("How many spaces in 'a  b'?", "m", 100, dir)
	if a == "" || a == b {
		t.Fatal(a, b)
	}
}
func TestArchAuditDagNull(t *testing.T) {
	taskEnv(t)
	r, e := newRun("audit", "light", "", "", true)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(dagStatePath(r), []byte(`{"nodes":null}`), 0600)
	if _, e := loadDagState(r, Dag{Nodes: []DagNode{{Name: "x"}}}); e == nil {
		t.Fatal("null nodes accepted")
	}
}
func TestArchAuditCorruptGoal(t *testing.T) {
	g := frozenTestGoal(t)
	os.WriteFile(filepath.Join(g.dir(), "goal.json"), []byte(`{`), 0600)
	if a, e := activeGoal(headID()); e == nil || a != nil {
		t.Fatal(a, e)
	}
	release, e := admit(headID(), admission{Kind: "spawn", Name: "x"})
	if e == nil {
		release()
		t.Fatal("corrupt goal admitted work")
	}
}
func TestArchAuditCloseFailure(t *testing.T) {
	r, _, _ := worktreeFixture(t)
	r.Workers = []*Worker{{Name: "w", Pane: "p-live", Status: "prompted"}}
	saveFixture(t, r)
	old := herdrRun
	defer func() { herdrRun = old }()
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pane" && args[1] == "list" {
			return []byte(`{"result":{"panes":[{"pane_id":"p-live"}]}}`), nil
		}
		return nil, fmt.Errorf("simulated mux error")
	}
	if _, e := closeRun(r.ID, true); e == nil {
		t.Fatal("expected close error")
	}
	fresh, e := loadRun(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Status != "active" || fresh.worker("w").Pane == "" {
		t.Fatal(fresh)
	}
	stopped, e := newReaper(r.ID).once(io.Discard)
	if e == nil || stopped {
		t.Fatal(stopped, e)
	}
	herdrRun = func(args ...string) ([]byte, error) { return []byte(`{"result":{"panes":[]}}`), nil }
	if stopped, e := newReaper(r.ID).once(io.Discard); e != nil || !stopped {
		t.Fatal("cleanup retry failed", stopped, e)
	}
	if got := mustLoad(t, r.ID); got.Status != "closed" || got.EndRequested || got.worker("w").Pane != "" {
		t.Fatal(got)
	}

}
func TestArchAuditDagScopeGate(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	t.Setenv("HERDR_PANE_ID", "audit-head")
	t.Setenv("ORCH_MUX", "herdr")
	r, e := createRun("audit", "full", "", "")
	if e != nil {
		t.Fatal(e)
	}
	base := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	body := strings.Replace(contractTemplate(r, "impl", "produce", "codex-high-m"), "<hash or none>", base, 1)
	os.WriteFile(r.contractPath("impl"), []byte(body), 0600)
	os.WriteFile(r.resultPath("impl"), []byte("# Result: impl\n- status: done\n## Reusable facts\n- Invalid fact — outside.txt\n"), 0600)
	os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("outside"), 0600)
	r.Workers = []*Worker{{Name: "impl", Role: "produce", Key: "codex-high-m", Family: familyOpenAI, Cwd: dir, Contract: r.contractPath("impl"), Result: r.resultPath("impl"), Status: "done"}}
	saveFixture(t, r)
	y := fmt.Sprintf("nodes:\n  - {name: impl, model: codex-high-m, role: produce}\n  - {name: after, run: 'test -e outside.txt', cwd: %q, role: verify-mech, needs: [impl]}\n", dir)
	os.WriteFile(filepath.Join(r.dir(), "dag.yaml"), []byte(y), 0600)
	st := &dagState{Nodes: map[string]*dagNodeState{"impl": {Status: "running", Worker: "impl"}, "after": {Status: "pending"}}}
	saveDagState(r, st)
	var out bytes.Buffer
	final, e := dagRun(r.ID, 5*time.Second, &out)
	if e != nil {
		t.Fatal(e, out.String())
	}
	if final.Nodes["impl"].Status != "failed" || final.Nodes["after"].Status != "skipped" {
		t.Fatal(out.String())
	}
	report, status, e := collectWorker(r.ID, "impl")
	if e != nil || status != "failed" || !strings.Contains(report, "scope violation") {
		t.Fatal(status, e, report)
	}
	knowledge, _ := os.ReadFile(filepath.Join(r.dir(), "knowledge.md"))
	if strings.Contains(string(knowledge), "Invalid fact") {
		t.Fatal("invalid result merged into knowledge")
	}

}

func TestArchAuditClosePersistFailure(t *testing.T) {
	r, _, _ := worktreeFixture(t)
	r.Workers = []*Worker{{Name: "w", Pane: "p-live", Status: "prompted"}}
	saveFixture(t, r)
	old := herdrRun
	defer func() { herdrRun = old }()
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pane" && args[1] == "list" {
			return []byte(`{"result":{"panes":[{"pane_id":"p-live"}]}}`), nil
		}
		if len(args) > 1 && args[0] == "pane" && args[1] == "close" {
			if e := os.Remove(filepath.Join(r.dir(), ".lock")); e != nil {
				return nil, e
			}
			if e := os.Mkdir(filepath.Join(r.dir(), ".lock"), 0700); e != nil {
				return nil, e
			}
		}
		return []byte(`{"result":{"panes":[]}}`), nil
	}
	if e := closeWorker(r.ID, "w"); e == nil {
		t.Fatal("persistence failure reported success")
	}
	fresh, e := loadRun(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.worker("w").Pane != "p-live" {
		t.Fatal(fresh)
	}
}
func TestArchAuditConcurrentPrompt(t *testing.T) {
	r, _, _ := worktreeFixture(t)
	w := &Worker{Name: "w", Pane: "p-live", Status: "spawned", Result: r.resultPath("w")}
	r.Workers = []*Worker{w}
	saveFixture(t, r)
	os.WriteFile(r.contractPath("w"), []byte(contractTemplate(r, "w", "recon", "codex-low-h")), 0600)
	os.WriteFile(w.Result, []byte("status: done\n"), 0600)
	old := herdrRun
	oldTimes := timings
	defer func() { herdrRun = old; timings = oldTimes }()
	timings.TrustSettlePrompt = 0
	timings.KeyGap = 0
	timings.Delivery = time.Second
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "agent" && args[1] == "get" {
			return []byte(`{"result":{"agent":{"agent_status":"idle"}}}`), nil
		}
		if len(args) > 1 && args[0] == "pane" && args[1] == "send-text" {
			entered <- struct{}{}
			<-release
		}
		return []byte(`{"result":{}}`), nil
	}
	for i := 0; i < 2; i++ {
		go func() { errs <- prompt(r.ID, "w", "", false, 0) }()
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery")
	}
	both := false
	select {
	case <-entered:
		both = true
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	if both {
		t.Fatal("duplicate concurrent delivery")
	}
}
