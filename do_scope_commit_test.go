package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDoCommittedScope(t *testing.T) {
	for _, tc := range []struct {
		name, file, status string
		code               int
	}{
		{"owned", "owned.txt", "done", 0},
		{"outside", "outside.txt", "failed", 1},
		{"deleted", "owned.txt", "done", 0},
		{"staged-deleted", "owned.txt", "done", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			taskEnv(t)
			dir := taskRepo(t)
			start := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
			// An unchanged dirty file must stay outside this task's changes and patch.
			putFile(t, dir, "preexisting.txt", "leave alone\n")
			edit := "printf 'worker change\\n' > " + tc.file
			if tc.name == "deleted" || tc.name == "staged-deleted" {
				edit = "rm -- " + tc.file
			}
			commit := "git commit -qm 'worker commit'\n"
			if tc.name == "staged-deleted" {
				commit = ""
			}
			fakeCLI(t, "claude", "set -eu\n"+edit+"\ngit add -- "+tc.file+"\n"+commit+"echo '{\"result\":\"SUMMARY: committed\"}'\n")
			var out, stderr bytes.Buffer
			code := run([]string{"do", "--model", "claude-medium-m", "--no-worktree", "--own", "owned.txt", "--cwd", dir, "--json", "commit a change"}, strings.NewReader(""), &out, &stderr)
			var task Task
			if err := json.Unmarshal(out.Bytes(), &task); err != nil {
				t.Fatalf("decode: %v; output=%s stderr=%s", err, out.String(), stderr.String())
			}
			if code != tc.code || task.Status != tc.status {
				t.Errorf("code=%d status=%s; want %d %s; stderr=%s", code, task.Status, tc.code, tc.status, stderr.String())
			}
			if !slices.Equal(task.Changed, []string{tc.file}) {
				t.Errorf("changed=%v, want [%s]", task.Changed, tc.file)
			}
			if tc.code != 0 && !slices.Equal(task.ScopeViolations, []string{tc.file}) {
				t.Errorf("scope violations=%v", task.ScopeViolations)
			}
			if tc.code == 0 && len(task.ScopeViolations) != 0 {
				t.Errorf("scope violations=%v", task.ScopeViolations)
			}
			patch, err := os.ReadFile(task.DiffPath)
			if err != nil || len(patch) == 0 || !strings.Contains(string(patch), tc.file) || strings.Contains(string(patch), "preexisting.txt") {
				t.Errorf("patch=%q err=%v", patch, err)
			}
			wantStat := "1 files, +1 -0"
			if tc.file == "owned.txt" {
				wantStat = "1 files, +1 -1"
			}
			if tc.name == "deleted" || tc.name == "staged-deleted" {
				wantStat = "1 files, +0 -1"
			}
			if task.Diffstat != wantStat {
				t.Errorf("diffstat=%q", task.Diffstat)
			}
			saved, err := os.ReadFile(filepath.Join(task.Dir, "task.json"))
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(saved, &record); err != nil {
				t.Fatal(err)
			}
			if record["start_head"] != start {
				t.Errorf("start_head=%v want %s", record["start_head"], start)
			}
		})
	}
}

func runCommittedScopeViolation(t *testing.T, dir, script, file string) *Task {
	t.Helper()
	fakeCLI(t, "claude", "set -eu\n"+script+"\necho '{\"result\":\"SUMMARY: committed\"}'\n")
	var out, stderr bytes.Buffer
	code := run([]string{"do", "--model", "claude-medium-m", "--no-worktree", "--own", "owned.txt", "--cwd", dir, "--json", "commit a change"}, strings.NewReader(""), &out, &stderr)
	var task Task
	if err := json.Unmarshal(out.Bytes(), &task); err != nil {
		t.Fatalf("decode: %v; output=%s stderr=%s", err, out.String(), stderr.String())
	}
	if code != 1 || task.Status != "failed" || !slices.Contains(task.ScopeViolations, file) || !slices.Contains(task.Changed, file) {
		t.Errorf("code=%d status=%s changed=%v violations=%v stderr=%s", code, task.Status, task.Changed, task.ScopeViolations, stderr.String())
	}
	patch, err := os.ReadFile(task.DiffPath)
	if err != nil || !strings.Contains(string(patch), file) {
		t.Errorf("patch=%q err=%v", patch, err)
	}
	if task.Diffstat == "0 files, +0 -0" {
		t.Errorf("empty diffstat: %s", task.Diffstat)
	}
	return &task
}

func TestDoCommittedScopePreexistingDirty(t *testing.T) {
	for _, amend := range []bool{false, true} {
		name := "commit"
		if amend {
			name = "amend"
		}
		t.Run(name, func(t *testing.T) {
			taskEnv(t)
			dir := taskRepo(t)
			putFile(t, dir, "preexisting.txt", "user dirty work\n")
			commit := "git commit -qm 'worker commit'"
			if amend {
				commit = "git commit --amend -qm 'worker amend'"
			}
			runCommittedScopeViolation(t, dir, "git add -- preexisting.txt\n"+commit, "preexisting.txt")
		})
	}
}

func TestDoCommittedScopeRevertWorktree(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	task := runCommittedScopeViolation(t, dir, "printf 'outside\\n' > outside.txt\ngit add -- outside.txt\ngit commit -qm 'worker commit'\nrm outside.txt", "outside.txt")
	patch, _ := os.ReadFile(task.DiffPath)
	if !strings.Contains(string(patch), "new file mode") || !strings.Contains(string(patch), "deleted file mode") {
		t.Errorf("patch must show committed addition and working-tree deletion: %s", patch)
	}
	if task.Diffstat != "1 files, +0 -0" {
		t.Errorf("diffstat=%s", task.Diffstat)
	}
}

func TestDoCommittedScopeEmptyRepo(t *testing.T) {
	taskEnv(t)
	dir := gitInit(t)
	task := runCommittedScopeViolation(t, dir, "printf 'outside\\n' > outside.txt\ngit add -- outside.txt\ngit commit -qm 'first commit'", "outside.txt")
	saved, err := os.ReadFile(filepath.Join(task.Dir, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Task
	if err := json.Unmarshal(saved, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.StartEmpty {
		t.Fatalf("start empty=%v", persisted.StartEmpty)
	}
	if len(task.ScopeViolations) != 1 {
		t.Fatalf("initial branch creation must be exempt: %v", task.ScopeViolations)
	}
}

func runScopeScript(t *testing.T, dir, script, own string) (int, Task) {
	t.Helper()
	fakeCLI(t, "claude", "set -eu\n"+script+"\necho '{\"result\":\"SUMMARY: changed\"}'\n")
	var out, stderr bytes.Buffer
	code := run([]string{"do", "--model", "claude-medium-m", "--no-worktree", "--own", own, "--cwd", dir, "--json", "change"}, strings.NewReader(""), &out, &stderr)
	var task Task
	if err := json.Unmarshal(out.Bytes(), &task); err != nil {
		t.Fatalf("decode: %v; output=%s stderr=%s", err, out.String(), stderr.String())
	}
	return code, task
}

func TestDoCommittedScopeNetDiffstat(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	code, task := runScopeScript(t, dir, "printf 'a\\nb\\n' > owned.txt\ngit add owned.txt\ngit commit -qm worker\nprintf 'a\\nc\\n' > owned.txt", "owned.txt")
	if code != 0 || task.Status != "done" || task.Diffstat != "1 files, +2 -1" {
		t.Fatalf("code=%d task=%+v", code, task)
	}
}

func TestDoCommittedScopeDiscardUserDirty(t *testing.T) {
	for _, tc := range []struct {
		name, file, script string
		owned              bool
	}{
		{"outside", "x", "git checkout -- x", false},
		{"owned", "owned.txt", "git checkout -- owned.txt", true},
		{"deleted-untracked", "x", "rm x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			taskEnv(t)
			dir := taskRepo(t)
			if tc.file == "x" && tc.name != "deleted-untracked" {
				putFile(t, dir, "x", "base\n")
				git(t, dir, "add", "x")
				git(t, dir, "commit", "-qm", "add x")
			}
			putFile(t, dir, tc.file, "user dirty\n")
			code, task := runScopeScript(t, dir, tc.script, "owned.txt")
			wantCode := 1
			if tc.owned {
				wantCode = 0
			}
			if code != wantCode || !slices.Contains(task.Changed, tc.file) || slices.Contains(task.ScopeViolations, tc.file) == tc.owned {
				t.Fatalf("code=%d changed=%v violations=%v", code, task.Changed, task.ScopeViolations)
			}
		})
	}
}

func TestDoCommittedScopeStartHeadUnknown(t *testing.T) {
	taskEnv(t)
	dir := taskRepo(t)
	putFile(t, dir, "outside.txt", "existing\n")
	git(t, dir, "add", "outside.txt")
	git(t, dir, "commit", "-qm", "outside already committed")
	task := Task{Cwd: dir, Dir: t.TempDir(), Own: []string{"owned.txt"}}
	if err := captureTaskChanges(&task); err != nil {
		t.Fatal(err)
	}
	if len(task.Changed) != 0 || len(task.ScopeViolations) != 0 {
		t.Fatalf("changed=%v violations=%v", task.Changed, task.ScopeViolations)
	}
	putFile(t, dir, "owned.txt", "new\n")
	if err := captureTaskChanges(&task); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(task.Changed, []string{"owned.txt"}) || task.Diffstat != "1 files, +1 -1" {
		t.Fatalf("changed=%v stat=%s", task.Changed, task.Diffstat)
	}
}
