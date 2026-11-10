package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func headSplit(id string, block map[string]any) string {
	return usageJSON(map[string]any{"type": "assistant", "sessionId": "session-1", "timestamp": "2026-11-10T01:00:00Z", "message": map[string]any{
		"id": id, "model": "model-a", "stop_reason": "tool_use", "content": []any{block},
		"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "output_tokens": 1},
	}})
}
func headToolID(id, name string, input any) map[string]any {
	b := map[string]any{"type": "tool_use", "name": name, "input": input}
	if id != "" {
		b["id"] = id
	}
	return b
}

// Claude Code writes one record per content block; every record of a message is final.
func TestCollectHeadSessionMergesSplitRecords(t *testing.T) {
	ask := headSplit("m1", headToolID("t1", "Bash", map[string]any{"command": "orch ask x"}))
	data := headSplit("m1", map[string]any{"type": "thinking", "thinking": usageSecret}) + ask +
		headSplit("m1", headToolID("t2", "Bash", map[string]any{"command": "orch do x"})) + ask +
		headSplit("m2", headToolID("t3", "Read", map[string]any{"file_path": usageSecret})) +
		headSplit("m2", map[string]any{"type": "text", "text": usageSecret}) +
		headSplit("m3", headToolID("", "Glob", map[string]any{})) + headSplit("m3", headToolID("", "Glob", map[string]any{}))
	h, err := collectHeadSession("session-1", strings.NewReader(data))
	if err != nil || h.Requests != 3 || h.Delegations != 2 || h.Reads != 3 || h.InputSum == nil || *h.InputSum != 30 || h.OutputSum == nil || *h.OutputSum != 3 {
		t.Fatalf("split records not merged: %+v %v", h, err)
	}
}

func TestCollectHeadSessionToolInputShapes(t *testing.T) {
	data := headSplit("m1", headToolID("t1", "Bash", map[string]any{"command": []string{"ls"}})) +
		headSplit("m2", headToolID("t2", "Bash", "ls")) +
		headSplit("m3", headToolID("t3", "Bash", map[string]any{"command": 5})) +
		headSplit("m4", headToolID("t4", "Bash", nil)) +
		headSplit("m5", headToolID("t5", "Edit", []any{1, 2})) +
		headSplit("m6", headToolID("t6", "Other", map[string]any{"command": map[string]any{"x": 1}})) +
		headSplit("m7", headToolID("t7", "Bash", map[string]any{"command": "rg x ."}))
	h, err := collectHeadSession("session-1", strings.NewReader(data))
	if err != nil || h.Requests != 7 || h.BashOther != 4 || h.InspectBash != 1 || h.Edits != 1 {
		t.Fatalf("tool input shape: %+v %v", h, err)
	}
}

func TestHeadBashCategoryWordBoundary(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"orch doctor", "orch_other"},
		{"orch do", "delegations"},
		{"orch do\tx", "delegations"},
		{"orch doctor && orch do x", "delegations"},
		{"(orch ask)", "delegations"},
		{"orch test;x", "delegations"},
		{"orch merge|x", "delegations"},
		{"orch spawn&", "delegations"},
		{"orch asking x", "orch_other"},
		{"orch testify", "orch_other"},
		{"orch prompts", "orch_other"},
		{"orch waiting", "orch_other"},
		{"orch dag running", "orch_other"},
		{"orch dag run", "delegations"},
		{"orch wait", "waits"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if got := headBashCategory(tc.command); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func headRow(session string, last time.Time, requests, delegations int) HeadSession {
	return HeadSession{Version: 1, Session: session, FirstTS: last, LastTS: last, Requests: requests, Delegations: delegations}
}

func TestHeadSessionRowSelection(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name       string
		rows       []HeadSession
		requests   int
		delegation int
	}{
		{"newer-last-ts-wins-over-later-append", []HeadSession{headRow("s", now, 5, 1), headRow("s", now.Add(-time.Minute), 50, 9)}, 5, 1},
		{"more-requests-on-tie", []HeadSession{headRow("s", now, 7, 1), headRow("s", now, 3, 9)}, 7, 1},
		{"last-append-on-full-tie", []HeadSession{headRow("s", now, 7, 1), headRow("s", now, 7, 2)}, 7, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ORCH_HOME", t.TempDir())
			for _, h := range tc.rows {
				if err := recordHeadSession(h); err != nil {
					t.Fatal(err)
				}
			}
			stats, err := collectHeadStats(time.Time{})
			if err != nil || stats.Sessions != 1 || stats.Requests != tc.requests || stats.Delegations != tc.delegation {
				t.Fatalf("%+v %v", stats, err)
			}
		})
	}
}

func headStatsJSON(t *testing.T) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if code := statsCommand([]string{"--json"}, &out); code != 0 {
		t.Fatalf("stats failed: %s", out.String())
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestHeadSessionLogToleratesMalformedLines(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	now := time.Now().UTC()
	good, _ := json.Marshal(headRow("a", now, 4, 1))
	bad, _ := json.Marshal(HeadSession{Version: 2, Session: "v2", LastTS: now})
	seed := string(good) + "\n{\n" + string(bad) + "\n" + `{"version":1,"sess`
	if err := os.MkdirAll(filepath.Dir(headSessionPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headSessionPath(), []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordHeadSession(headRow("b", now, 6, 2)); err != nil {
		t.Fatalf("append blocked by malformed line: %v", err)
	}
	b, _ := os.ReadFile(headSessionPath())
	if !strings.HasPrefix(string(b), seed+"\n{") || !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("partial tail not closed before append: %q", b)
	}
	head := headStatsJSON(t)["head"].(map[string]any)
	if head["sessions"] != 2.0 || head["requests"] != 10.0 || head["skipped"] != 3.0 {
		t.Fatalf("malformed lines not skipped and counted: %+v", head)
	}
	after, _ := os.ReadFile(headSessionPath())
	if !bytes.Equal(b, after) {
		t.Fatal("stats changed the summary file")
	}
}

func TestStatsHeadUnavailableKeepsTaskRows(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	task := filepath.Join(runDir("r"), "tasks", "a", "task.json")
	if err := os.MkdirAll(filepath.Dir(task), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(task, []byte(`{"kind":"do","status":"done","started":"`+time.Now().UTC().Format(time.RFC3339)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(headSessionPath(), 0700); err != nil {
		t.Fatal(err)
	}
	report := headStatsJSON(t)
	reason, _ := report["head_error"].(string)
	if rows, _ := report["rows"].([]any); len(rows) != 1 || reason == "" || strings.Contains(reason, orchHome()) {
		t.Fatalf("Head failure blocked task rows or leaked a path: %+v", report)
	}
	var out bytes.Buffer
	if statsCommand(nil, &out) != 0 || !strings.Contains(out.String(), "head: unavailable ("+reason+")") || !strings.Contains(out.String(), "do ") {
		t.Fatal(out.String())
	}
}

const headTestCompactBytes = 4 << 20

func writeLargeHeadLog(t *testing.T, now time.Time) {
	t.Helper()
	var buf bytes.Buffer
	for i := 0; buf.Len() <= headTestCompactBytes; i++ {
		row, _ := json.Marshal(headRow(string(rune('a'+i%2)), now.Add(time.Duration(i)*time.Millisecond), i, 1))
		buf.Write(row)
		buf.WriteByte('\n')
		if i == 10 {
			buf.WriteString("{\n")
		}
	}
	if err := os.MkdirAll(filepath.Dir(headSessionPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headSessionPath(), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHeadSessionLogCompaction(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	now := time.Now().UTC().Add(-time.Hour)
	writeLargeHeadLog(t, now)
	before, _ := collectHeadStats(time.Time{})
	if err := recordHeadSession(headRow("c", now, 1, 1)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(headSessionPath())
	if err != nil || info.Size() > 4096 || info.Mode().Perm() != 0600 {
		t.Fatalf("not compacted: %v %v", info.Size(), err)
	}
	b, _ := os.ReadFile(headSessionPath())
	if bytes.Count(b, []byte("\n")) != 3 {
		t.Fatalf("expected one row per session: %s", b)
	}
	after, err := collectHeadStats(time.Time{})
	if err != nil || after.Sessions != 3 || after.Requests != before.Requests+1 {
		t.Fatalf("selected rows changed: %+v %+v %v", before, after, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(headSessionPath()))
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}

func TestHeadSessionConcurrentCompaction(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	now := time.Now().UTC().Add(-time.Hour)
	writeLargeHeadLog(t, now)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := recordHeadSession(headRow("w"+string(rune('0'+i)), now, 1, 1)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	stats, err := collectHeadStats(time.Time{})
	if err != nil || stats.Sessions != 12 {
		t.Fatalf("append lost across compaction: %+v %v", stats, err)
	}
}
