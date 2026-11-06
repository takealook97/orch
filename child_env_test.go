package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestChildCLIEnv(t *testing.T) {
	input := []string{"CLAUDE_CODE_CHILD_SESSION=1", "CLAUDECODE=keep", "ORCH_ROLE=worker", "CLAUDE_CODE_CHILD_SESSION=", "CLAUDE_CODE_CHILD_SESSION_EXTRA=keep"}
	original := append([]string(nil), input...)
	if got := childCLIEnv(nil); len(got) != 0 {
		t.Fatal("nil pane overrides expanded into parent environment")
	}
	want := []string{"CLAUDECODE=keep", "ORCH_ROLE=worker", "CLAUDE_CODE_CHILD_SESSION_EXTRA=keep"}
	if got := childCLIEnv(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("mutated parent environment")
	}
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDECODE", "keep")
	for _, env := range [][]string{nil, os.Environ(), input} {
		script := `test "${CLAUDE_CODE_CHILD_SESSION+x}" != x && test "$CLAUDECODE" = keep && printf NO`
		path := filepath.Join(t.TempDir(), "child.log")
		res, err := execLogged(t.TempDir(), "/bin/sh", []string{"-c", script}, env, time.Second, path, nil)
		data, _ := os.ReadFile(path)
		if err != nil || res.Code != 0 || string(data) != "NO" {
			t.Fatalf("execLogged: %+v %v %q", res, err, data)
		}
		data, err = boundedControl("/bin/sh", []string{"-c", script}, env, time.Second)
		if err != nil || string(data) != "NO" {
			t.Fatalf("boundedControl: %v %q", err, data)
		}
	}
	if os.Getenv("CLAUDE_CODE_CHILD_SESSION") != "1" {
		t.Fatal("parent marker changed")
	}
}

func TestChildSessionTaskPaths(t *testing.T) {
	for _, kind := range []string{"ask", "do", "test"} {
		t.Run(kind, func(t *testing.T) {
			taskEnv(t)
			t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
			t.Setenv("CLAUDECODE", "keep")
			dir := taskRepo(t)
			script := `test "${CLAUDE_CODE_CHILD_SESSION+x}" != x || exit 31
 test "$CLAUDECODE" = keep || exit 32
 printf '%s\n' '{"result":"NO"}'`
			fakeCLI(t, "claude", script)
			args := []string{kind, "--cwd", dir, "--json"}
			if kind != "test" {
				args = append(args, "--model", "claude-medium-m")
			}
			if kind == "do" {
				args = append(args, "--own", "owned.txt", "--no-worktree")
			}
			if kind == "test" {
				args = append(args, `test "$CLAUDECODE" = keep && if env | /usr/bin/grep -q '^CLAUDE_CODE_CHILD_SESSION='; then exit 33; else printf '0\n'; fi`)
			} else {
				args = append(args, "fixture")
			}
			var out, errout bytes.Buffer
			code := run(args, strings.NewReader(""), &out, &errout)
			var result Task
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || code != 0 || result.Status != "done" {
				t.Fatalf("code=%d out=%s err=%s decode=%v", code, &out, &errout, err)
			}
			data := []byte(result.Answer)
			if kind == "test" {
				if result.Test == nil {
					t.Fatal("missing test result")
				}
				var err error
				data, err = os.ReadFile(result.Test.Log)
				if err != nil {
					t.Fatal(err)
				}
			}
			want := "NO"
			if kind == "test" {
				want = "0"
			}
			if !strings.Contains(string(data), want) {
				t.Fatalf("log %q want %s", data, want)
			}
			t.Logf("%s fixture: %s", kind, want)
		})
	}
}

func TestChildSessionCRPane(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDECODE", "keep")
	path := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntest \"${CLAUDE_CODE_CHILD_SESSION+x}\" != x && test \"$CLAUDECODE\" = keep && printf NO\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"claude", "codex", "gemini"} {
		command := crPaneCommand(path, kind, t.TempDir(), []string{"CLAUDE_CODE_CHILD_SESSION=override"}, nil)
		out, err := exec.Command("/bin/sh", "-c", command).CombinedOutput()
		if err != nil || string(out) != "NO" {
			t.Fatalf("%s: %v %q", kind, err, out)
		}
	}
}

func TestChildSessionNativePaneHandshake(t *testing.T) {
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDECODE", "keep")
	calls := 0
	herdrRun = func(args ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			if strings.Join(args[:3], " ") != "pane run owned-pane" {
				t.Fatalf("%v", args)
			}
			out, err := exec.Command("/bin/sh", "-c", args[3]+`; test "${CLAUDE_CODE_CHILD_SESSION+x}" != x && test "$CLAUDECODE" = keep`).CombinedOutput()
			if err != nil || string(out) != "orch-child-session-cleared\n" {
				t.Fatalf("%q %v", out, err)
			}
			if strings.Contains(args[3], "orch-child-session-cleared") {
				t.Fatal("input echo can match acknowledgement")
			}
		} else if strings.Join(args[:3], " ") != "pane wait-output owned-pane" {
			t.Fatalf("%v", args)
		}
		return nil, nil
	}
	if err := clearPaneChildSession("owned-pane"); err != nil || calls != 2 {
		t.Fatalf("%v calls=%d", err, calls)
	}
	herdrRun = func(...string) ([]byte, error) { return nil, errors.New("fixture failed") }
	if err := clearPaneChildSession("owned-pane"); err == nil {
		t.Fatal("accepted failed unset")
	}
	if err := startWorkerAgent("test", "owned-pane", "", nil); err == nil || !strings.Contains(err.Error(), "clear inherited") {
		t.Fatalf("start did not fail closed: %v", err)
	}
}

func TestChildSessionPaneAcknowledgementFailure(t *testing.T) {
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	herdrRun = func(args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "pane run":
			return nil, nil
		case "pane wait-output":
			return nil, errors.New("acknowledgement timeout")
		default:
			t.Fatalf("started CLI without unset acknowledgement: %v", args)
			return nil, nil
		}
	}
	if err := startWorkerAgent("test", "owned-pane", "", []string{"agent", "start", "test"}); err == nil || !strings.Contains(err.Error(), "acknowledgement timeout") {
		t.Fatalf("%v", err)
	}
}
