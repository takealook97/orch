package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runDoReport(t *testing.T, script string, extra ...string) (int, string) {
	t.Helper()
	taskEnv(t)
	dir := taskRepo(t)
	fakeCLI(t, "claude", "set -eu\n"+script+"\necho '{\"result\":\"SUMMARY: edited\"}'\n")
	args := append([]string{"do", "--model", "claude-medium-m", "--no-worktree", "--own", "*.txt,*.dat", "--cwd", dir}, extra...)
	var out, stderr bytes.Buffer
	code := run(append(args, "edit files"), strings.NewReader(""), &out, &stderr)
	return code, out.String() + stderr.String()
}

func TestDoReportIncludesSmallNetDiff(t *testing.T) {
	script := "printf 'worker change\\n' > owned.txt\nprintf 'brand new\\n' > new.txt\nprintf 'a\\000b' > blob.dat"
	code, out := runDoReport(t, script)
	if code != 0 || !strings.HasPrefix(out, "done task-") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	for _, want := range []string{"+worker change", "-", "b/new.txt", "+brand new", "Binary files", "blob.dat"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in report:\n%s", want, out)
		}
	}
	if strings.Contains(out, "GIT binary patch") {
		t.Fatalf("binary content included:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "dir: ") {
		t.Fatalf("dir line must stay last:\n%s", out)
	}
}

func TestDoReportDiffThreshold(t *testing.T) {
	big := "seq 1 70 > owned.txt"
	small := "printf 'one\\ntwo\\n' > owned.txt"
	for _, tc := range []struct {
		name, script string
		args         []string
		wantDiff     bool
	}{
		{"default-large", big, nil, false},
		{"disabled", small, []string{"--diff-lines", "0"}, false},
		{"below-threshold", small, []string{"--diff-lines", "2"}, false},
		{"raised", big, []string{"--diff-lines", "100"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runDoReport(t, tc.script, tc.args...)
			if code != 0 || !strings.HasPrefix(out, "done task-") {
				t.Fatalf("code=%d out=%s", code, out)
			}
			if got := strings.Contains(out, "@@"); got != tc.wantDiff {
				t.Fatalf("diff included=%v want %v:\n%s", got, tc.wantDiff, out)
			}
		})
	}
}

func TestDoReportDiffJSONAndFailure(t *testing.T) {
	code, out := runDoReport(t, "printf 'x\\n' > owned.txt", "--json")
	var v map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || !strings.Contains(v["diff"].(string), "+x") {
		t.Fatalf("json diff missing: %d %s", code, out)
	}
	code, out = runDoReport(t, "printf 'x\\n' > owned.txt\nprintf 'y\\n' > outside.md")
	if code != 1 || strings.Contains(out, "@@") || !strings.Contains(out, "scope violations") {
		t.Fatalf("failure report changed: %d %s", code, out)
	}
	code, _ = runDoReport(t, "true", "--diff-lines", "-1")
	if code != 2 {
		t.Fatalf("negative threshold accepted: %d", code)
	}
}
