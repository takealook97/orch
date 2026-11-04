package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalMCPFilePaths(t *testing.T) {
	for _, name := range []string{"mcp__filesystem__write_file", "mcp__filesystem__edit_file"} {
		t.Run(name, func(t *testing.T) {
			frozenTestGoal(t)
			for _, path := range []string{"", "/workspace/outside/a.go", "/workspace/src/a.go"} {
				in := hookJSON(name, map[string]string{"path": path, "content": "example"})
				if got := goalScopeReason(in); (got != "") != (path != "/workspace/src/a.go") {
					t.Errorf("goal path %q: %s", path, got)
				}
			}
			t.Setenv("ORCH_RUN", "run-1")
			for _, path := range []string{"", "/workspace/src/a.go", filepath.Join(runDir("run-1"), "contracts", "x.md")} {
				in := hookJSON(name, map[string]string{"path": path})
				if got := headExecutionReason(in); (got != "") != (path == "" || path == "/workspace/src/a.go") {
					t.Errorf("Head path %q: %s", path, got)
				}
			}
		})
	}
}

func TestFinalMCPCompilePaths(t *testing.T) {
	goalEnv(t)
	transcript := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(transcript, []byte(rolloutGoal("compile first", "active")), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mcp__filesystem__write_file", "mcp__filesystem__edit_file"} {
		for _, path := range []string{"", "/workspace/src/a.go", filepath.Join(orchHome(), "goals", "draft.md")} {
			in := hookJSON(name, map[string]string{"path": path})
			in.TranscriptPath = transcript
			if got := codexGoalCompileReason(in); (got != "") != (path == "" || path == "/workspace/src/a.go") {
				t.Errorf("%s compile path %q: %s", name, path, got)
			}
		}
	}
}

func TestFinalTerminalWriteStillInspected(t *testing.T) {
	for _, name := range []string{"mcp__terminal__write", "mcp__term__write", "terminal.write", "x.edit", "mcp__filesystem__write_file", "mcp__filesystem__edit_file"} {
		in := hookJSON(name, map[string]string{"path": "/workspace/src/a.go", "command": "orch spawn worker"})
		if in.commandText() == "" || decide(in, "worker", true) == "" {
			t.Errorf("command body exempted: %s", name)
		}
	}
}

func TestFinalHeadInspectionPrograms(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	for _, cmd := range []string{
		`awk 'BEGIN{system("make check")}'`, `awk 'BEGIN{system("true")}'`,
		`awk '{print > "f"}' data`, `awk '{printf "%s", $0 >> "f"}' data`, `awk '{print | "cat"}' data`,
		`sed 'w out' file`, `sed -n '1,3w out' file`, `sed 's/x/y/w out' file`,
		`sed 'e true' file`, `sed 's/x/y/e' file`,
		`go mod tidy`, `go mod download`, `go get example.org/pkg`, `go -C . mod tidy`,
		`find . -delete`, `find . -exec true {} \;`, `find . -execdir true {} +`,
		`bash x.sh`, `sh ./x.sh`, `/bin/zsh -f x.sh`,
	} {
		if got := headExecutionReason(hookJSON("Bash", map[string]string{"command": cmd})); got == "" {
			t.Errorf("execution allowed: %s", cmd)
		}
	}
	for _, cmd := range []string{
		`awk '$3>100' data`, `awk '{print $1}' data`, `awk 'BEGIN {printf "make > build"}'`,
		`awk '{print ($3>100)}' data`, `awk '{printf "%s", ($3>100)}' data`,
		`grep 'system("make check")' log`, `grep 'w out' log`, `git log --format='%h > %s'`,
		`sed -n '1,3p' file`, `sed 's/new/old/' file`, `sed '/warning/p' file`,
		`sed 's/a;w out/b/' file`,
		`find . -name '*.go'`, `grep foo 2>/dev/null`, `orch test 'awk "BEGIN{system(\"true\")}"'`,
	} {
		if got := headExecutionReason(hookJSON("Bash", map[string]string{"command": cmd})); got != "" {
			t.Errorf("inspection denied: %s: %s", cmd, got)
		}
	}
}

func TestFinalFakeCLISentinels(t *testing.T) {
	// Safe stand-ins represent real PATH entries: this test never calls providers.
	trailing := t.TempDir()
	for _, name := range []string{"codex", "claude", "agy", "gemini", "cr"} {
		if err := os.WriteFile(filepath.Join(trailing, name), []byte("#!/bin/sh\necho unintended-fallback\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", trailing+string(os.PathListSeparator)+os.Getenv("PATH"))
	fakeCLI(t, "wrong-provider-name", "echo fixture\n")
	for _, name := range []string{"codex", "claude", "agy", "gemini", "cr"} {
		out, err := exec.Command(name).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "unmocked provider") {
			t.Errorf("%s escaped sentinel: %v %s", name, err, out)
		}
	}
	fakeCLI(t, "codex", "echo codex-fixture\n")
	fakeCLI(t, "agy", "echo agy-fixture\n")
	for _, name := range []string{"codex", "agy"} {
		out, err := exec.Command(name).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != name+"-fixture" {
			t.Errorf("explicit mock lost: %s %v %s", name, err, out)
		}
	}
	if out, err := exec.Command("sh", "-c", "printf system-tooling").CombinedOutput(); err != nil || string(out) != "system-tooling" {
		t.Fatalf("system tooling unavailable: %v %s", err, out)
	}
}
