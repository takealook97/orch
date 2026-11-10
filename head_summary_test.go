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

func TestHeadBashCategory(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"orch ask --key x --text 'private'", "delegations"},
		{"FOO=bar /tmp/orch do --scope '*.go'", "delegations"},
		{"cd /tmp && env orch spawn --no-focus", "delegations"},
		{"'orch' prompt m1", "bash_other"},
		{"orch test -- go test ./...", "delegations"},
		{"orch dag run file.yaml", "delegations"},
		{"orch merge x", "delegations"},
		{"orch wait && orch status", "waits"},
		{"orch status && orch wait && orch ask x", "delegations"},
		{"orch dag validate file.yaml", "orch_other"},
		{"orch models", "orch_other"},
		{"rg 'orch ask' .", "delegations"},
		{"cat file | jq .", "inspect_bash"},
		{"git status", "inspect_bash"},
		{"python3 -c 'print(1)'", "bash_other"},
		{"echo 'orch spawn --text data'", "delegations"},
		{"echo 'orch wait' && rg x .", "waits"},
		{"echo $(orch ask x)", "delegations"},
		{"$PROGRAM ask x", "bash_other"},
		{"orch $ACTION x", "orch_other"},
		{"orch dag $ACTION", "orch_other"},
		{"python3 - <<'PY'\nprint('orch ask')\nPY", "delegations"},
		{"", "bash_other"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if got := headBashCategory(tc.command); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func headFixture(id, model, stop, timestamp string, tools ...map[string]any) string {
	content := []any{map[string]any{"type": "text", "text": usageSecret}}
	for _, tool := range tools {
		content = append(content, tool)
	}
	return usageJSON(map[string]any{"type": "assistant", "sessionId": "session-1", "timestamp": timestamp, "message": map[string]any{
		"id": id, "model": model, "stop_reason": stop, "content": content,
		"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 20, "cache_creation_input_tokens": 30, "output_tokens": 4},
	}})
}
func headTool(name, cmd string) map[string]any {
	// A stable id per (name, command) lets repeated records dedupe like real ones.
	return map[string]any{"type": "tool_use", "id": name + ":" + cmd, "name": name, "input": map[string]any{"command": cmd, "file_path": "/private/" + usageSecret}}
}
func headUser(id string, content any) string {
	return usageJSON(map[string]any{"type": "user", "uuid": id, "sessionId": "session-1", "timestamp": "2026-11-10T01:00:00Z", "message": map[string]any{"content": content}})
}

func TestCollectHeadSessionCounts(t *testing.T) {
	first, last := "2026-11-10T01:00:00Z", "2026-11-10T02:00:00Z"
	final := headFixture("a", "model-a", "tool_use", last,
		headTool("Bash", "orch do --text '"+usageSecret+"'"), headTool("Bash", "orch wait"),
		headTool("Bash", "orch status"), headTool("Bash", "rg '"+usageSecret+"' ."),
		headTool("Bash", "echo '"+usageSecret+"'"), headTool("Read", ""), headTool("Grep", ""), headTool("Glob", ""),
		headTool("Write", ""), headTool("Edit", ""), headTool("NotebookEdit", ""), headTool("OtherTool", ""))
	human := headUser("u1", usageSecret)
	data := human + human + headUser("u2", []any{map[string]any{"type": "text", "text": "<task-notification>" + usageSecret + "</task-notification>"}}) +
		headUser("u3", []any{map[string]any{"type": "tool_result", "content": usageSecret}}) +
		headFixture("a", "model-a", "", first, headTool("Write", "")) + final + final +
		headFixture("b", "model-b", "end_turn", last) + headFixture("c", "<synthetic>", "stop_sequence", last) +
		`{"text":"unfinished ` + usageSecret
	h, err := collectHeadSession("session-1", strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != 1 || h.Session != "session-1" || h.Requests != 2 || h.Model != "model-a" || h.HumanTurns != 1 || h.NotificationTurns != 1 || h.Delegations != 1 || h.Waits != 1 || h.OrchOther != 1 || h.InspectBash != 1 || h.BashOther != 1 || h.Reads != 3 || h.Edits != 3 {
		t.Fatalf("unexpected counts: %+v", h)
	}
	if h.MaxContext == nil || *h.MaxContext != 60 || h.InputSum == nil || *h.InputSum != 120 || h.OutputSum == nil || *h.OutputSum != 8 || h.FirstTS.Format(time.RFC3339) != first || h.LastTS.Format(time.RFC3339) != last {
		t.Fatalf("unexpected tokens/times: %+v", h)
	}
	b, _ := json.Marshal(h)
	for _, text := range []string{usageSecret, "command", "file_path", "content", "<synthetic>"} {
		if bytes.Contains(b, []byte(text)) {
			t.Fatalf("persisted non-count data: %s", text)
		}
	}
}

func TestCollectHeadSessionMissingAndInvalid(t *testing.T) {
	good := headFixture("a", "model-a", "end_turn", "2026-11-10T01:00:00Z")
	for _, tc := range []struct {
		name, data, session string
		wantErr             bool
	}{
		{"empty", "", "session-1", false},
		{"no-session", strings.Replace(good, `"sessionId":"session-1"`, `"sessionId":""`, 1), "session-1", false},
		{"bad-session-arg", good, usageSecret + " space", true},
		{"mismatch", strings.ReplaceAll(good, "session-1", "other-session"), "session-1", true},
		{"bad-json", "{\"body\":\"" + usageSecret + "\", BROKEN}\n", "session-1", true},
		{"bad-time", strings.ReplaceAll(good, "2026-11-10T01:00:00Z", usageSecret), "session-1", true},
		{"bad-model", strings.ReplaceAll(good, "model-a", "bad "+usageSecret), "session-1", true},
		{"negative-usage", strings.ReplaceAll(good, `"input_tokens":10`, `"input_tokens":-1`), "session-1", true},
		{"unknown-version", "{\"schema_version\":99}\n", "session-1", true},
		{"missing-usage", strings.ReplaceAll(good, `"usage":{`, `"unused":{`), "session-1", false},
		{"unknown-cache", strings.ReplaceAll(good, `"cache_read_input_tokens":20`, `"cache_read_input_tokens":null`), "session-1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := collectHeadSession(tc.session, strings.NewReader(tc.data))
			if (err != nil) != tc.wantErr || err != nil && strings.Contains(err.Error(), usageSecret) {
				t.Fatalf("unexpected/leaking error: %v", err)
			}
			if !tc.wantErr && (tc.name == "missing-usage" || tc.name == "unknown-cache") && (h.InputSum != nil || h.MaxContext != nil) {
				t.Fatal("missing input invented")
			}
		})
	}
	large := strings.ReplaceAll(good, `"input_tokens":10`, `"input_tokens":9223372036854775700`)
	_, err := collectHeadSession("session-1", strings.NewReader(large+strings.ReplaceAll(large, `"id":"a"`, `"id":"b"`)))
	if err == nil || err.Error() != "head usage sum overflow" {
		t.Fatalf("sum overflow accepted: %v", err)
	}
}

func TestHeadSessionPersistenceSelectsRow(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	for _, requests := range []int{2, 10, 1} {
		if err := recordHeadSession(HeadSession{Version: 1, Session: "session-1", Requests: requests}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(headSessionPath())
	if err != nil || bytes.Count(b, []byte("\n")) != 3 {
		t.Fatalf("append failed: %v", err)
	}
	rows, skipped, err := readHeadSessions(bytes.NewReader(b))
	if err != nil || skipped != 0 || len(rows) != 1 || rows["session-1"].Requests != 10 {
		t.Fatalf("equal last_ts must keep the most requests: %v %+v", err, rows)
	}
	info, _ := os.Stat(headSessionPath())
	if info.Mode().Perm() != 0600 {
		t.Fatal("summary file permissions")
	}
	for _, bad := range []string{"{\n", strings.TrimSuffix(string(b), "\n") + "\n{", strings.ReplaceAll(string(b), `"version":1`, `"version":2`)} {
		rows, skipped, err := readHeadSessions(strings.NewReader(bad))
		if err != nil || skipped == 0 || len(rows) > 1 {
			t.Fatalf("malformed line not skipped: %v %d %+v", err, skipped, rows)
		}
	}
}

func TestHeadSessionConcurrentAppend(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recordHeadSession(HeadSession{Version: 1, Session: "s", Requests: 1}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(headSessionPath())
	if err != nil || bytes.Count(b, []byte("\n")) != 10 {
		t.Fatalf("lost append: %v", err)
	}
	if _, skipped, err := readHeadSessions(bytes.NewReader(b)); err != nil || skipped != 0 {
		t.Fatal(err, skipped)
	}
}

func TestMeasureHeadIndependentStores(t *testing.T) {
	for _, broken := range []string{"usage", "summary"} {
		t.Run(broken, func(t *testing.T) {
			t.Setenv("ORCH_HOME", t.TempDir())
			file := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(file, []byte(headFixture("a", "model-a", "end_turn", "2026-11-10T01:00:00Z")), 0600); err != nil {
				t.Fatal(err)
			}
			bad, good := measurementPath(), headSessionPath()
			if broken == "summary" {
				bad, good = good, bad
			}
			if err := os.MkdirAll(filepath.Dir(bad), 0700); err != nil {
				t.Fatal(err)
			}
			if broken == "summary" {
				// The summary log tolerates malformed lines, so break it with a directory.
				if err := os.Mkdir(bad, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(bad, []byte("{\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			if code := measureHeadCommand([]string{"--session", "session-1", "--session-file", file}, &log); code != 1 || log.Len() == 0 || strings.Contains(log.String(), usageSecret) {
				t.Fatalf("failure not logged: %d %s", code, log.String())
			}
			b, err := os.ReadFile(good)
			if err != nil || bytes.Count(b, []byte("\n")) != 1 {
				t.Fatalf("other store blocked: %v", err)
			}
			if broken == "usage" {
				if b, _ = os.ReadFile(bad); string(b) != "{\n" {
					t.Fatal("corrupt log overwritten")
				}
			}
		})
	}
}

func TestHeadSummaryValidation(t *testing.T) {
	for _, h := range []HeadSession{
		{Version: 2, Session: "s"}, {Version: 1, Session: "bad space"},
		{Version: 1, Session: "s", Model: "bad space"}, {Version: 1, Session: "s", Requests: -1},
		{Version: 1, Session: "s", MaxContext: ptrInt64(-1)},
		{Version: 1, Session: "s", FirstTS: time.Now()},
	} {
		if h.validate() == nil {
			t.Fatalf("invalid summary accepted: %+v", h)
		}
	}
}
func ptrInt64(v int64) *int64 { return &v }

func TestCollectHeadSessionFinalDedupeAndDominantModel(t *testing.T) {
	at := "2026-11-10T01:00:00Z"
	data := headFixture("a", "model-z", "tool_use", at, headTool("Write", "")) +
		headFixture("a", "model-z", "end_turn", at, headTool("Read", "")) +
		headFixture("b", "model-a", "end_turn", at) + headFixture("c", "model-z", "end_turn", at)
	h, err := collectHeadSession("session-1", strings.NewReader(data))
	if err != nil || h.Requests != 3 || h.Model != "model-z" || h.Reads != 1 || h.Edits != 1 || h.InputSum == nil || *h.InputSum != 180 {
		t.Fatalf("final dedupe or dominant model failed: %+v %v", h, err)
	}
}

func TestCollectHeadSessionExcludesSidechainAndInjectedTurns(t *testing.T) {
	at := "2026-11-10T01:00:00Z"
	data := headFixture("head", "model-a", "end_turn", at) +
		strings.Replace(headFixture("worker", "model-b", "end_turn", at, headTool("Write", "")), `"type":"assistant"`, `"isSidechain":true,"type":"assistant"`, 1) +
		strings.Replace(headUser("meta", usageSecret), `"type":"user"`, `"isMeta":true,"type":"user"`, 1) +
		strings.Replace(headUser("sidechain", usageSecret), `"type":"user"`, `"isSidechain":true,"type":"user"`, 1) +
		headUser("command", "<command-name>help</command-name>") +
		headUser("local", "<local-command-stdout>output</local-command-stdout>") +
		headUser("hook", "<system-reminder>hook</system-reminder>") +
		headUser("empty", "") + headUser("null", nil) + headUser("human", usageSecret)
	h, err := collectHeadSession("session-1", strings.NewReader(data))
	if err != nil || h.Requests != 1 || h.Model != "model-a" || h.HumanTurns != 1 || h.Edits != 0 {
		t.Fatalf("sidechain or injected content counted as Head: %+v %v", h, err)
	}
}

// Opt-in integration smoke: only an explicitly supplied local Claude transcript
// is read, all writes go to t.TempDir, and output contains aggregate counts only.
func TestHeadExistingSessionSmoke(t *testing.T) {
	file := os.Getenv("ORCH_USAGE_SMOKE_FILE")
	if file == "" || os.Getenv("ORCH_USAGE_SMOKE_PROVIDER") != "claude" {
		t.Skip("explicit local Claude session binding required")
	}
	session := os.Getenv("ORCH_USAGE_SMOKE_SESSION")
	t.Setenv("ORCH_HOME", t.TempDir())
	args := []string{"--session", session, "--session-file", file}
	for i := 0; i < 2; i++ {
		var log bytes.Buffer
		if measureHeadCommand(args, &log) != 0 {
			t.Fatalf("collection failed: %s", log.String())
		}
	}
	usage, err := loadMeasurements()
	if err != nil || len(usage) == 0 {
		t.Fatalf("usage evidence missing: %v", err)
	}
	before, err := os.ReadFile(headSessionPath())
	if err != nil || bytes.Count(before, []byte("\n")) != 2 {
		t.Fatalf("one summary per invocation not appended: %v", err)
	}
	rows, _, err := readHeadSessions(bytes.NewReader(before))
	if err != nil || len(rows) != 1 || rows[session].Requests == 0 {
		t.Fatalf("summary missing: %v", err)
	}
	report, err := collectStats(time.Time{})
	if err != nil || report.Head.Sessions != 1 || report.Head.Requests != rows[session].Requests {
		t.Fatalf("Head stats missing: %v", err)
	}
	after, _ := os.ReadFile(headSessionPath())
	if !bytes.Equal(before, after) {
		t.Fatal("stats modified summary evidence")
	}
	h := report.Head
	t.Logf("usage_records=%d summary_appends=2 retained_sessions=%d requests=%d delegations=%d inspect_bash=%d", len(usage), h.Sessions, h.Requests, h.Delegations, h.InspectBash)
	if h.MaxContext != nil && h.RequestsPerDelegation != nil {
		t.Logf("max_context=%d requests_per_delegation=%.6f", *h.MaxContext, *h.RequestsPerDelegation)
	}
}
