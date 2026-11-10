package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestStopHook(t *testing.T) {
	old := spawnHeadMeasure
	t.Cleanup(func() { spawnHeadMeasure = old })
	for _, tc := range []struct {
		name, role, auto, payload string
		want                      bool
	}{
		{"valid", "", "", `{"session_id":"session-1","transcript_path":"/tmp/session.jsonl"}`, true},
		{"worker", "worker", "", `{"session_id":"session-1","transcript_path":"/tmp/session.jsonl"}`, false},
		{"disabled", "", "0", `{"session_id":"session-1","transcript_path":"/tmp/session.jsonl"}`, false},
		{"malformed", "", "", `{`, false},
		{"missing-session", "", "", `{"transcript_path":"/tmp/session.jsonl"}`, false},
		{"empty-session", "", "", `{"session_id":"","transcript_path":"/tmp/session.jsonl"}`, false},
		{"missing-path", "", "", `{"session_id":"session-1"}`, false},
		{"relative", "", "", `{"session_id":"session-1","transcript_path":"session.jsonl"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ORCH_HOME", t.TempDir())
			t.Setenv("ORCH_ROLE", tc.role)
			t.Setenv("ORCH_AUTO_MEASURE", tc.auto)
			calls := 0
			spawnHeadMeasure = func(exe string, args []string, log *os.File) error {
				calls++
				wantExe, _ := os.Executable()
				if exe != wantExe || !reflect.DeepEqual(args, []string{"__measure-head", "--session", "session-1", "--session-file", "/tmp/session.jsonl"}) {
					t.Fatalf("%s %v", exe, args)
				}
				if filepath.Base(log.Name()) != "measure.log" {
					t.Fatal(log.Name())
				}
				return errors.New("spawn failure must be silent")
			}
			var out, errOut bytes.Buffer
			if code := runHook("claude-stop", strings.NewReader(tc.payload), &out, &errOut); code != 0 || out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("%d %q %q", code, out.String(), errOut.String())
			}
			want := 0
			if tc.want {
				want = 1
			}
			if calls != want {
				t.Fatalf("calls=%d want=%d", calls, want)
			}
		})
	}
}

func TestStopHookLogSize(t *testing.T) {
	old := spawnHeadMeasure
	t.Cleanup(func() { spawnHeadMeasure = old })
	for _, tc := range []struct {
		name     string
		size     int
		wantSize int64
	}{
		{"over-cap", maxMeasureLogBytes + 1, 0},
		{"below-cap", maxMeasureLogBytes - 1, maxMeasureLogBytes - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("ORCH_HOME", home)
			t.Setenv("ORCH_ROLE", "")
			t.Setenv("ORCH_AUTO_MEASURE", "")
			content := bytes.Repeat([]byte("x"), tc.size)
			if err := os.WriteFile(filepath.Join(home, "measure.log"), content, 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			spawnHeadMeasure = func(_ string, _ []string, log *os.File) error {
				calls++
				info, err := log.Stat()
				if err != nil || info.Size() != tc.wantSize {
					t.Fatalf("log stat=%v error=%v, want size %d", info, err, tc.wantSize)
				}
				return nil
			}
			stopHook([]byte(`{"session_id":"session-1","transcript_path":"/tmp/session.jsonl"}`))
			if calls != 1 {
				t.Fatalf("calls=%d, want 1", calls)
			}
			got, err := os.ReadFile(filepath.Join(home, "measure.log"))
			if err != nil || !bytes.Equal(got, content[:int(tc.wantSize)]) {
				t.Fatalf("log contents changed unexpectedly: size=%d error=%v", len(got), err)
			}
		})
	}
}

func TestMeasureHeadCollection(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	file := filepath.Join(t.TempDir(), "session.jsonl")
	data := claudeUsage("a", 2, 9, "end_turn") + claudeUsage("b", 1, 10, "tool_use")
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--session", "session-1", "--session-file", file}
	for _, want := range []int{2, 2, 3} {
		if want == 3 {
			f, e := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.WriteString(claudeUsage("c", 4, 8, "end_turn"))
			f.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
		var stderr bytes.Buffer
		if code := measureHeadCommand(args, &stderr); code != 0 {
			t.Fatalf("%d %s", code, stderr.String())
		}
		rows, e := loadMeasurements()
		if e != nil || len(rows) != want {
			t.Fatalf("%d %v", len(rows), e)
		}
		for _, m := range rows {
			if m.Subject != "head" || m.Entity != "head" || m.Period != "session-1" || m.Phase != "session" {
				t.Fatal(m)
			}
		}
	}
	rows, _ := loadMeasurements()
	bad := rows[0]
	bad.Phase = "bogus"
	if bad.validate() == nil {
		t.Fatal("bogus phase accepted")
	}
	var report bytes.Buffer
	if err := measurementCommand([]string{"report"}, strings.NewReader(""), &report); err != nil || !strings.Contains(report.String(), "head|head|session|claude-session-v1") || !strings.Contains(report.String(), `"n_total":3`) {
		t.Fatalf("%v %s", err, report.String())
	}
}

func TestMeasureHeadDispatchDetachedIdentity(t *testing.T) {
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "detached-head-test")
	file := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(file, []byte(claudeUsage("dispatch", 2, 9, "end_turn")), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run([]string{"__measure-head", "--session", "session-1", "--session-file", file}, strings.NewReader(""), &out, &stderr)
	if code != 0 || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("%d %s %s", code, out.String(), stderr.String())
	}
	rows, err := loadMeasurements()
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d %v", len(rows), err)
	}
}

func TestMeasureHeadFailureLogs(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	for _, tc := range []struct {
		name, data string
		missing    bool
	}{
		{"missing", "", true},
		{"malformed", usageSecret + "\n", false},
		{"identity", strings.ReplaceAll(claudeUsage("a", 2, 9, "end_turn"), "session-1", "other-session"), false},
		{"metadata", strings.ReplaceAll(claudeUsage("a", 2, 9, "end_turn"), "claude-test", "bad "+usageSecret), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "session.jsonl")
			if !tc.missing {
				if e := os.WriteFile(file, []byte(tc.data), 0600); e != nil {
					t.Fatal(e)
				}
			}
			var stderr bytes.Buffer
			if code := measureHeadCommand([]string{"--session", "session-1", "--session-file", file}, &stderr); code != 1 {
				t.Fatal(code)
			}
			s := stderr.String()
			if tc.name == "metadata" && !strings.Contains(s, "invalid provider metadata: invalid model identity") {
				t.Fatal("validation cause missing from log")
			}
			if strings.Count(s, "\n") != 1 || !regexp.MustCompile(`^\S+ measure-head session=\S+ error=.+\n$`).MatchString(s) || strings.Contains(s, usageSecret) {
				t.Fatal(s)
			}
		})
	}
}

func TestClaudeStopInstallUninstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	repo := t.TempDir()
	if e := os.MkdirAll(filepath.Join(repo, "skill"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(repo, "skill", "SKILL.md"), []byte("skill"), 0600); e != nil {
		t.Fatal(e)
	}
	foreign := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "'/foreign/orch' hook claude-stop"}}}
	os.MkdirAll(filepath.Dir(claudeSettings()), 0700)
	if e := writeJSONFile(claudeSettings(), map[string]any{"hooks": map[string]any{"Stop": []any{foreign}}}); e != nil {
		t.Fatal(e)
	}
	var data []byte
	for i := 0; i < 2; i++ {
		if _, e := install(repo, false); e != nil {
			t.Fatal(e)
		}
		s, e := readJSONFile(claudeSettings())
		if e != nil {
			t.Fatal(e)
		}
		groups := s["hooks"].(map[string]any)["Stop"].([]any)
		if len(groups) != 2 {
			t.Fatalf("%v", groups)
		}
		if !reflect.DeepEqual(groups[0], foreign) {
			t.Fatal("foreign hook changed")
		}
		if i == 0 {
			data, _ = os.ReadFile(claudeSettings())
		} else {
			after, _ := os.ReadFile(claudeSettings())
			if !bytes.Equal(data, after) {
				t.Fatal("install not idempotent")
			}
		}
	}
	if _, e := uninstall(repo, true); e != nil {
		t.Fatal(e)
	}
	s, e := readJSONFile(claudeSettings())
	if e != nil {
		t.Fatal(e)
	}
	groups := s["hooks"].(map[string]any)["Stop"].([]any)
	if len(groups) != 1 || !reflect.DeepEqual(groups[0], foreign) {
		t.Fatal(groups)
	}
}
