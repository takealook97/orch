package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// deadPid returns the pid of a process that has already exited and been reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// liveGroup starts a sleeping child in its own process group and returns its group id.
func liveGroup(t *testing.T) int {
	t.Helper()
	c := exec.Command("sleep", "30")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL); _ = c.Wait() })
	return c.Process.Pid
}

// Reusing a worker name moves the old result aside so the new worker cannot inherit it.
func TestRetireNameMovesPreviousResult(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	started := time.Date(2026, 9, 26, 18, 4, 5, 0, time.Local)
	os.WriteFile(r.resultPath("a"), []byte("# Result: a\n- status: done\n"), 0600)
	_ = withRun(r.ID, func(x *Run) error {
		x.Workers = append(x.Workers, &Worker{Name: "a", Status: "closed", Result: r.resultPath("a"), Started: started})
		return nil
	})
	if err := withRun(r.ID, func(x *Run) error { return retireName(x, "a") }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.resultPath("a")); !os.IsNotExist(err) {
		t.Fatalf("old result still at the new worker's path: %v", err)
	}
	got, _ := loadRun(r.ID)
	old := got.worker("a~180405")
	if old == nil || old.Result != r.resultPath("a~180405") {
		t.Fatalf("old = %+v", old)
	}
	if resultStatus(old.Result) != "done" {
		t.Fatal("moved result lost its content")
	}
	if got.worker("a") != nil {
		t.Fatal("name not freed")
	}
}

// A status line written while the agent still works does not close its pane.
func TestWaitAndCollectLeaveWorkingAgentOpen(t *testing.T) {
	r, f := reapFixture(t)
	outs, err := waitWorkers(r.ID, []string{"c"}, 50*time.Millisecond, false)
	if err != nil || len(outs) != 1 || outs[0].Status != "timeout" {
		t.Fatalf("outs=%+v err=%v", outs, err)
	}
	if !f.live("w1:p4") {
		t.Fatal("wait closed a pane whose agent is still working")
	}
	out, err := collect(r.ID, "c")
	if err != nil || !strings.Contains(out, "agent still working; pane left open") {
		t.Fatalf("collect: %q %v", out, err)
	}
	if !f.live("w1:p4") {
		t.Fatal("collect closed a pane whose agent is still working")
	}
	// Once the agent stops, wait closes it.
	f.mu.Lock()
	f.agents["c"] = "idle"
	f.mu.Unlock()
	outs, err = waitWorkers(r.ID, []string{"c"}, time.Second, false)
	if err != nil || len(outs) != 1 || outs[0].Status != "done" || f.live("w1:p4") {
		t.Fatalf("outs=%+v err=%v", outs, err)
	}
}

func TestTaskDeadNeedsRunnerAndGroupGone(t *testing.T) {
	stale := time.Now().Add(-time.Hour)
	if why := taskDead(&Task{Pid: os.Getpid(), Heartbeat: stale}); why != "" {
		t.Fatalf("live runner with a stale heartbeat declared dead: %s", why)
	}
	dead := deadPid(t)
	if why := taskDead(&Task{Pid: dead, Pgid: liveGroup(t), Heartbeat: stale}); why != "" {
		t.Fatalf("live child group declared dead: %s", why)
	}
	if why := taskDead(&Task{Pid: dead, Pgid: deadPid(t)}); why == "" {
		t.Fatal("task with runner and group gone not declared dead")
	}
	if why := taskDead(&Task{Heartbeat: stale}); why == "" {
		t.Fatal("unclaimed task with a stale heartbeat not declared dead")
	}
}

func TestTaskRecordsChildGroupAndClaimRefusesLiveGroup(t *testing.T) {
	taskEnv(t)
	fakeCLI(t, "claude", `echo '{"result":"ok"}'`)
	task, err := newTask("ask", "claude-medium-m", "q", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := runTask(task); err != nil {
		t.Fatal(err)
	}
	saved, _ := readTask(task.Run, task.ID)
	if saved.Pgid <= 0 {
		t.Fatalf("child process group not recorded: %+v", saved)
	}
	// The runner died but its CLI group survived: the task must not start a second time.
	saved.Status, saved.Pid, saved.Pgid = "running", deadPid(t), liveGroup(t)
	_ = saveTask(saved)
	var out, errout bytes.Buffer
	if code := run([]string{"__task", "--run", task.Run, task.ID}, strings.NewReader(""), &out, &errout); code == 0 || !strings.Contains(errout.String(), "live child process group") {
		t.Fatalf("%d %s", code, errout.String())
	}
}

func TestFileHashCountsModeChange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.sh")
	os.WriteFile(p, []byte("echo\n"), 0644)
	before := fileHash(dir, "f.sh")
	os.Chmod(p, 0755)
	if fileHash(dir, "f.sh") == before {
		t.Fatal("chmod not seen as a change")
	}
}

func TestExcerptKeepsFirstFailureUnderSmallCap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.log")
	r, err := executeTest(`i=0; while [ $i -lt 10 ]; do echo ok$i; i=$((i+1)); done; echo '--- FAIL: TestX'; exit 1`, t.TempDir(), 4, 5000, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Excerpt, "--- FAIL: TestX") {
		t.Fatalf("excerpt lost the first failure: %q", r.Excerpt)
	}
}

func TestCollectExitsOneOnFailedStatus(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	os.WriteFile(r.resultPath("a"), []byte("# Result: a\n- status: failed\n"), 0600)
	_ = withRun(r.ID, func(x *Run) error {
		x.Workers = append(x.Workers, &Worker{Name: "a", Status: "closed", Result: r.resultPath("a")})
		return nil
	})
	var out, errout bytes.Buffer
	if code := run([]string{"collect", "--run", r.ID, "a"}, strings.NewReader(""), &out, &errout); code != 1 {
		t.Fatalf("code %d: %s %s", code, out.String(), errout.String())
	}
}

// A reaper that observed an old pane must not close the new worker that reused the name.
func TestCloseSkipsReusedName(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	_ = withRun(r.ID, func(x *Run) error {
		x.Workers = append(x.Workers, &Worker{Name: "a~120000", Status: "done"}, &Worker{Name: "a", Pane: "w1:p9", Status: "prompted"})
		return nil
	})
	if err := closeWorkerAs(r.ID, "a", "w1:p2", true); err != nil {
		t.Fatal(err)
	}
	r2, _ := loadRun(r.ID)
	if w := r2.worker("a"); w.Pane != "w1:p9" || w.Reaped || w.Status != "prompted" {
		t.Fatalf("new worker was touched: %+v", w)
	}
}
