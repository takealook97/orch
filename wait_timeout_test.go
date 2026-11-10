package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestWaitTimeoutStatesAgentAndRerun(t *testing.T) {
	r, f := reapFixture(t)
	f.mu.Lock()
	f.agents["b"] = "working"
	f.mu.Unlock()
	task := &Task{ID: "task-1", Run: r.ID, Status: "running", Pid: os.Getpid(), Dir: taskPath(r.ID, "task-1")}
	if err := os.MkdirAll(task.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeTask(task); err != nil {
		t.Fatal(err)
	}
	outs, err := waitWorkers(r.ID, []string{"c", "b", "task-1"}, 50*time.Millisecond, false)
	if err != nil || len(outs) != 3 {
		t.Fatalf("outs=%+v err=%v", outs, err)
	}
	want := map[string]string{"c": "working; rerun: orch wait c", "b": "working; rerun: orch wait b", "task-1": "working; rerun: orch wait task-1"}
	for _, o := range outs {
		if o.Status != "timeout" || o.Detail != want[o.Name] || strings.Contains(o.Detail, "read") {
			t.Fatalf("timeout line %+v", o)
		}
	}
}

func TestWorkerAgentStateMapping(t *testing.T) {
	_, f := reapFixture(t)
	for status, want := range map[string]string{"working": "working", "idle": "idle", "done": "idle", "blocked": "blocked", "weird": "unknown"} {
		f.mu.Lock()
		f.agents["a"] = status
		f.mu.Unlock()
		if got := workerAgentState(&Worker{Name: "a", Pane: "w1:p2"}); got != want {
			t.Fatalf("%s: got %s want %s", status, got, want)
		}
	}
	if got := workerAgentState(&Worker{Name: "missing", Pane: "w1:p9"}); got != "unknown" {
		t.Fatalf("missing agent: %s", got)
	}
}
