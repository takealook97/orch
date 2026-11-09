package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func compactReportTask(t *testing.T, kind string) *Task {
	t.Helper()
	t.Setenv("ORCH_MUX", "none")
	return &Task{ID: "task-1", Run: "run-1", Kind: kind, Key: "codex-low-h", Status: "done", DurationMS: 1250, Dir: "/task", Answer: "answer line\nsecond line"}
}

func TestCompactReportDoSuccess(t *testing.T) {
	task := compactReportTask(t, "do")
	task.Test = &TestResult{ExitCode: 0}
	task.Commit = "abcdef012345"
	task.Changed = []string{"a", "b"}
	task.Diffstat = "2 files, +3 -4"
	want := "done task-1 do key=codex-low-h 1.2s test=0 commit=abcdef0 files=2 +3 -4\nanswer line\nsecond line\ndir: /task"
	if got := taskReport(task, false); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
func TestCompactReportAskSuccess(t *testing.T) {
	task := compactReportTask(t, "ask")
	want := "done task-1 ask key=codex-low-h 1.2s\nanswer line\nsecond line\ndir: /task"
	if got := taskReport(task, false); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
func TestCompactReportTestSuccess(t *testing.T) {
	task := compactReportTask(t, "test")
	task.Key = ""
	task.Test = &TestResult{ExitCode: 0}
	want := "done task-1 test 1.2s test=0\ndir: /task"
	if got := taskReport(task, false); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
func TestCompactReportFailureKeepsFull(t *testing.T) {
	for _, reason := range []string{"status", "exit", "test", "scope", "hint"} {
		t.Run(reason, func(t *testing.T) {
			task := compactReportTask(t, "do")
			switch reason {
			case "status":
				task.Status = "failed"
			case "exit":
				task.ExitCode = 1
			case "test":
				task.Test = &TestResult{ExitCode: 1, Log: "/test.log"}
			case "scope":
				task.ScopeViolations = []string{"outside"}
			case "hint":
				t.Setenv("ORCH_MUX", "herdr")
				task.DurationMS = (6 * time.Minute).Milliseconds()
			}
			want := fmt.Sprintf("task: task-1\nrun: run-1\nstatus: %s\nexit code: %d\nduration: %d ms\nanswer: answer line\nsecond line\n", task.Status, task.ExitCode, task.DurationMS)
			if reason == "scope" {
				want += "scope violations: outside\n"
			}
			if task.Test != nil {
				var b strings.Builder
				writeTest(&b, "test ", task.Test)
				want += b.String()
			}
			want += "dir: /task"
			if reason == "hint" {
				want += "\nhint: " + paneHint(task, true)
			}
			if got := taskReport(task, false); got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		})
	}
}
func TestCompactReportJSONUnchanged(t *testing.T) {
	task := compactReportTask(t, "do")
	task.Changed = []string{"a"}
	task.Diffstat = "1 files, +1 -0"
	want := `{"answer":"answer line\nsecond line","answer_path":"","capacity_retries":0,"changed":["a"],"diff_path":"","diffstat":"1 files, +1 -0","dir":"/task","duration_ms":1250,"exit_code":0,"key":"codex-low-h","run":"run-1","scope_violations":null,"status":"done","task":"task-1","truncated":false}`
	got := taskReport(task, true)
	var decoded any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestCompactReportParsedFailuresKeepFull(t *testing.T) {
	for _, tr := range []*TestResult{
		{ExitCode: 0, Failures: []string{"TestBroken"}},
		{ExitCode: 0, Excerpt: "--- FAIL: TestBroken"},
	} {
		task := compactReportTask(t, "do")
		task.Test = tr
		got := taskReport(task, false)
		if !strings.HasPrefix(got, "task: task-1\n") {
			t.Fatalf("test failure signal hidden: %q", got)
		}
		if len(tr.Failures) > 0 && !strings.Contains(got, "TestBroken") || tr.Excerpt != "" && !strings.Contains(got, tr.Excerpt) {
			t.Fatalf("missing failure details: %q", got)
		}
	}
}
