package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatsAggregation(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	dir := runDir("stats")
	os.MkdirAll(dir, 0700)
	write := func(path string, v any) {
		t.Helper()
		os.MkdirAll(filepath.Dir(path), 0700)
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	now := time.Now()
	write(filepath.Join(dir, "run.json"), &Run{ID: "stats", Created: now, Workers: []*Worker{{Name: "a", Key: "claude-high-m", Purpose: "review", Status: "done", Started: now.Add(-time.Minute), Closed: now, FallbackOf: "b"}}})
	for i, record := range []map[string]any{
		{"kind": "do", "key": "codex-high-l", "status": "done", "exit_code": 0, "duration_ms": 2000, "capacity_retries": 2},
		{"kind": "do", "key": "codex-high-l", "status": "failed", "exit_code": 1, "duration_ms": 4000, "scope_violations": []string{"oops"}},
		{"kind": "test", "status": "failed", "exit_code": 1, "duration_ms": 1000},
		{"kind": "ask", "key": "codex-high-l", "status": "done", "duration_ms": 1000, "started": now.Add(-time.Hour * 200)},
	} {
		write(filepath.Join(dir, "tasks", string(rune('a'+i)), "task.json"), record)
	}
	os.MkdirAll(filepath.Join(dir, "tasks/bad"), 0700)
	os.WriteFile(filepath.Join(dir, "tasks/bad/task.json"), []byte("{"), 0600)
	var out bytes.Buffer
	if code := statsCommand([]string{"--json"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	var report statsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || len(report.Rows) != 3 {
		t.Fatalf("%s", out.String())
	}
	for _, r := range report.Rows {
		switch r.Kind {
		case "do":
			if r.N != 2 || r.Done != 1 || r.Failed != 1 || r.ScopeViolations == nil || *r.ScopeViolations != 1 || r.Retries == nil || *r.Retries != 2 || r.MeanSeconds == nil || *r.MeanSeconds != 3 {
				t.Fatalf("%+v", r)
			}
		case "pane":
			if r.Fallbacks == nil || *r.Fallbacks != 1 {
				t.Fatalf("%+v", r)
			}
		case "test":
			if r.TestFailures == nil || *r.TestFailures != 1 {
				t.Fatalf("%+v", r)
			}
		}
	}
	out.Reset()
	if statsCommand(nil, &out) != 0 || !strings.Contains(out.String(), "-") || !strings.Contains(out.String(), "skipped=1") {
		t.Fatal(out.String())
	}
}

func TestStatsArguments(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	for _, args := range [][]string{{"--since", "bad"}, {"--since", "-1h"}, {"unexpected"}} {
		var out bytes.Buffer
		if statsCommand(args, &out) == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestStatsUnknownAndReadOnly(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_ROLE", "worker")
	dir := runDir("unknown")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(&Run{Created: time.Now(), Workers: []*Worker{{Name: "unknown", Status: "spawned", Started: time.Now()}}})
	p := filepath.Join(dir, "run.json")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	var out, errs bytes.Buffer
	if code := run([]string{"stats", "--json"}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	var report statsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 1 || report.Rows[0].MeanSeconds != nil || report.Rows[0].TestFailures != nil || report.Rows[0].ScopeViolations != nil {
		t.Fatal(out.String())
	}
	after, _ := os.Stat(p)
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, b) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("stats modified run")
	}
	entries, err := os.ReadDir(orchHome())
	if err != nil || len(entries) != 1 || entries[0].Name() != "runs" {
		t.Fatalf("unexpected writes: %v %v", entries, err)
	}
}

func TestStatsGroupingAndWindow(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	now := time.Now()
	for _, fixture := range []struct {
		id           string
		created      time.Time
		key, purpose string
		n            int
	}{
		{"one", now, "codex-high-l", "lookup", 2},
		{"two", now, "codex-medium-m", "lookup", 1},
		{"three", now, "codex-high-l", "other", 1},
		{"old", now.Add(-200 * time.Hour), "old-key", "", 1},
	} {
		dir := runDir(fixture.id)
		os.MkdirAll(dir, 0700)
		b, _ := json.Marshal(&Run{Created: fixture.created})
		os.WriteFile(filepath.Join(dir, "run.json"), b, 0600)
		for i := 0; i < fixture.n; i++ {
			td := filepath.Join(dir, "tasks", string(rune('a'+i)))
			os.MkdirAll(td, 0700)
			b, _ = json.Marshal(map[string]any{"kind": "ask", "key": fixture.key, "purpose": fixture.purpose, "status": "done"})
			os.WriteFile(filepath.Join(td, "task.json"), b, 0600)
		}
	}
	report, err := collectStats(now.Add(-168 * time.Hour))
	if err != nil || report.Skipped != 0 || len(report.Rows) != 3 || report.Rows[0].N != 2 || report.Rows[0].Purpose != "lookup" {
		t.Fatalf("%+v %v", report, err)
	}
	for _, r := range report.Rows {
		if r.Key == "old-key" || r.MeanSeconds != nil || r.Fallbacks != nil {
			t.Fatalf("%+v", r)
		}
	}
}

func TestStatsCorruptRunSkippedOnce(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	dir := runDir("corrupt")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, fixture := range []struct {
		name string
		at   time.Time
	}{
		{"recent-a", now}, {"recent-b", now}, {"old", now.Add(-200 * time.Hour)},
	} {
		td := filepath.Join(dir, "tasks", fixture.name)
		if err := os.MkdirAll(td, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(td, "task.json")
		if err := os.WriteFile(path, []byte(`{"kind":"ask","status":"done"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, fixture.at, fixture.at); err != nil {
			t.Fatal(err)
		}
	}
	report, err := collectStats(now.Add(-168 * time.Hour))
	if err != nil || report.Skipped != 1 || len(report.Rows) != 1 || report.Rows[0].N != 2 || report.Rows[0].Done != 2 {
		t.Fatalf("report=%+v rows=%+v err=%v", report, report.Rows, err)
	}
}

func TestStatsHeadAggregation(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	now := time.Now().UTC()
	for _, h := range []HeadSession{
		{Version: 1, Session: "replaced", LastTS: now.Add(-300 * time.Hour), Requests: 999, Delegations: 999, MaxContext: ptrInt64(999)},
		{Version: 1, Session: "replaced", LastTS: now.Add(-200 * time.Hour), Requests: 999},
		{Version: 1, Session: "a", LastTS: now, Requests: 10, Delegations: 2, InspectBash: 3, MaxContext: ptrInt64(100)},
		{Version: 1, Session: "b", LastTS: now, Requests: 20, Delegations: 3, InspectBash: 4, MaxContext: ptrInt64(200)},
		{Version: 1, Session: "direct", LastTS: now, Requests: 100, MaxContext: ptrInt64(300)},
		{Version: 1, Session: "missing", LastTS: now, Requests: 1},
		{Version: 1, Session: "no-time", Requests: 999},
	} {
		if err := recordHeadSession(h); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(headSessionPath())
	info, _ := os.Stat(headSessionPath())
	var out bytes.Buffer
	if statsCommand([]string{"--json"}, &out) != 0 {
		t.Fatal(out.String())
	}
	var report statsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	h := report.Head
	if h.Sessions != 4 || h.Requests != 131 || h.Delegations != 5 || h.InspectBash != 7 || h.MedianMaxContext == nil || *h.MedianMaxContext != 200 || h.MaxContext == nil || *h.MaxContext != 300 || h.RequestsPerDelegation == nil || *h.RequestsPerDelegation != 6 {
		t.Fatalf("unexpected head stats: %s", out.String())
	}
	if len(report.Rows) != 0 || report.Skipped != 0 {
		t.Fatal("Head-only statistics require a runs directory")
	}
	out.Reset()
	if statsCommand(nil, &out) != 0 || !strings.Contains(out.String(), "Head\n") || !strings.Contains(out.String(), "requests_per_delegation") || !strings.Contains(out.String(), "median_max_context") {
		t.Fatal(out.String())
	}
	after, _ := os.ReadFile(headSessionPath())
	afterInfo, _ := os.Stat(headSessionPath())
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("stats changed Head evidence")
	}
	entries, err := os.ReadDir(orchHome())
	if err != nil || len(entries) != 1 || entries[0].Name() != "measurements" {
		t.Fatalf("unexpected writes: %v %v", entries, err)
	}
}

func TestStatsHeadMedianAndWindow(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		values []int64
		want   *float64
	}{
		{"empty", nil, nil},
		{"observed-zero", []int64{0}, func() *float64 { v := 0.0; return &v }()},
		{"even", []int64{11, 20}, func() *float64 { v := 15.5; return &v }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ORCH_HOME", t.TempDir())
			for i, n := range tc.values {
				if err := recordHeadSession(HeadSession{Version: 1, Session: string(rune('a' + i)), LastTS: now, MaxContext: ptrInt64(n)}); err != nil {
					t.Fatal(err)
				}
			}
			stats, err := collectHeadStats(now)
			if err != nil || stats.Sessions != len(tc.values) || stats.RequestsPerDelegation != nil || (stats.MedianMaxContext == nil) != (tc.want == nil) {
				t.Fatalf("%+v %v", stats, err)
			}
			if tc.want != nil && *stats.MedianMaxContext != *tc.want {
				t.Fatal(*stats.MedianMaxContext)
			}
			stats, err = collectHeadStats(now.Add(time.Nanosecond))
			if err != nil || stats.Sessions != 0 || stats.MaxContext != nil {
				t.Fatalf("cutoff not based on last_ts: %+v %v", stats, err)
			}
		})
	}
}

func TestStatsHeadMissingReadOnlyAndMalformed(t *testing.T) {
	t.Setenv("ORCH_HOME", filepath.Join(t.TempDir(), "absent"))
	var out bytes.Buffer
	if statsCommand([]string{"--json"}, &out) != 0 || !strings.Contains(out.String(), `"head":{"sessions":0`) || !strings.Contains(out.String(), `"requests_per_delegation":null`) {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(orchHome()); !os.IsNotExist(err) {
		t.Fatal("stats created ORCH_HOME")
	}
	if err := os.MkdirAll(filepath.Dir(headSessionPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headSessionPath(), []byte("{\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if statsCommand(nil, &out) != 0 || strings.Contains(out.String(), "unavailable") {
		t.Fatal(out.String())
	}
	b, _ := os.ReadFile(headSessionPath())
	if string(b) != "{\n" {
		t.Fatal("stats rewrote the summary log")
	}
}
