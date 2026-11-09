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
	} {
		t.Run(tc.name, func(t *testing.T) {
			taskEnv(t)
			dir := taskRepo(t)
			start := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
			// An unchanged dirty file must stay outside this task's changes and patch.
			putFile(t, dir, "preexisting.txt", "leave alone\n")
			edit := "printf 'worker change\\n' > " + tc.file
			if tc.name == "deleted" {
				edit = "rm -- " + tc.file
			}
			fakeCLI(t, "claude", "set -eu\n"+edit+"\ngit add -- "+tc.file+"\ngit commit -qm 'worker commit'\necho '{\"result\":\"SUMMARY: committed\"}'\n")
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
			if tc.name == "deleted" {
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
