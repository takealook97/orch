package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const usageSecret = "PRIVATE_PROMPT_SENTINEL_DO_NOT_STORE"

func usageTestBinding(t *testing.T, provider string) usageBinding {
	t.Helper()
	return usageBinding{Provider: provider, File: filepath.Join(t.TempDir(), "session.jsonl"), Session: "session-1", Subject: "head", Entity: "declared-head", Period: "sample", Phase: "execution"}
}
func usageJSON(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
func claudeUsage(id string, in, out any, stop string) string {
	return usageJSON(map[string]any{"type": "assistant", "sessionId": "session-1", "requestId": "req-" + id, "prompt": usageSecret, "message": map[string]any{"id": id, "model": "claude-test", "stop_reason": stop, "content": []any{map[string]string{"text": usageSecret}}, "usage": map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 4}}})
}
func codexMeta() string {
	return usageJSON(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "session-1", "instructions": usageSecret}})
}
func codexModel(model string) string {
	return usageJSON(map[string]any{"type": "turn_context", "payload": map[string]any{"model": model, "prompt": usageSecret}})
}
func codexUsage(in, out any) string {
	return usageJSON(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count", "content": usageSecret, "info": map[string]any{"total_token_usage": map[string]any{"input_tokens": in, "output_tokens": out}, "last_token_usage": map[string]int{"input_tokens": 999999, "output_tokens": 999999}}}})
}
func requireTokens(t *testing.T, m Measurement, in, out int64) {
	t.Helper()
	if m.Input == nil || m.Output == nil || *m.Input != in || *m.Output != out {
		t.Fatalf("wrong counters: %+v", m)
	}
}
func TestCollectClaudeFinalDedupAndGrowth(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	b := usageTestBinding(t, "claude")
	data := claudeUsage("a", 2, 1, "") + claudeUsage("a", 2, 9, "tool_use") + claudeUsage("b", 1, 10, "")
	r, e := collectUsage(b, strings.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Records) != 1 {
		t.Fatal(r)
	}
	requireTokens(t, r.Records[0], 9, 9)
	if e = recordMeasurementBatch(r.Records); e != nil {
		t.Fatal(e)
	}
	if e = recordMeasurementBatch(r.Records); e != nil {
		t.Fatal(e)
	}
	data += claudeUsage("b", 1, 20, "end_turn")
	r, e = collectUsage(b, strings.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	if e = recordMeasurementBatch(r.Records); e != nil {
		t.Fatal(e)
	}
	rows, e := loadMeasurements()
	if e != nil || len(rows) != 2 {
		t.Fatal(len(rows), e)
	}
	requireTokens(t, rows[1], 8, 20)
	raw, _ := os.ReadFile(measurementPath())
	if bytes.Contains(raw, []byte(usageSecret)) {
		t.Fatal("prompt leaked")
	}
	// A different declared binding cannot count the same event again.
	for _, change := range []string{"entity", "phase", "period"} {
		bad := append([]Measurement(nil), r.Records...)
		for i := range bad {
			switch change {
			case "entity":
				bad[i].Entity = "other"
			case "phase":
				bad[i].Phase = "review"
			case "period":
				bad[i].Period = "other"
			}
		}
		if recordMeasurementBatch(bad) == nil {
			t.Fatalf("accepted conflicting %s", change)
		}
	}
}
func TestCollectCodexCumulativeReplayGrowthModels(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	b := usageTestBinding(t, "codex")
	data := codexMeta() + codexModel("model-a") + codexUsage(20, 1) + codexUsage(20, 1) + codexUsage(48, 3) + codexModel("model-b") + codexUsage(60, 5)
	r, e := collectUsage(b, strings.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Records) != 3 {
		t.Fatal(r)
	}
	requireTokens(t, r.Records[0], 20, 1)
	requireTokens(t, r.Records[1], 28, 2)
	requireTokens(t, r.Records[2], 12, 2)
	if r.Records[0].Model != "model-a" || r.Records[2].Model != "model-b" {
		t.Fatal("mixed model attribution")
	}
	for i := 0; i < 2; i++ {
		if e = recordMeasurementBatch(r.Records); e != nil {
			t.Fatal(e)
		}
	}
	data += codexUsage(70, 7)
	r, e = collectUsage(b, strings.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	if e = recordMeasurementBatch(r.Records); e != nil {
		t.Fatal(e)
	}
	rows, e := loadMeasurements()
	if e != nil || len(rows) != 4 {
		t.Fatal(len(rows), e)
	}
	var in, out int64
	for _, m := range rows {
		in += *m.Input
		out += *m.Output
	}
	if in != 70 || out != 7 {
		t.Fatal(in, out)
	}
	// Same provider evidence moved to another path retains identity.
	b.File = "/different/session.jsonl"
	moved, e := collectUsage(b, strings.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	if e = recordMeasurementBatch(moved.Records); e != nil {
		t.Fatal(e)
	}
}
func TestCollectMissingZeroTruncatedUnknown(t *testing.T) {
	b := usageTestBinding(t, "codex")
	for _, tc := range []struct {
		name, data string
		count      int
		coverage   string
		unclosed   bool
	}{
		{"unknown", usageJSON(map[string]any{"type": "unknown", "body": usageSecret}), 0, "unavailable", false},
		{"empty", "", 0, "unavailable", false},
		{"missing", codexMeta() + codexUsage(nil, nil), 1, "unavailable", false},
		{"zero", codexMeta() + codexUsage(0, 0), 1, "partial", false},
		{"truncated", codexMeta() + codexUsage(5, 2) + `{"body":"` + usageSecret, 1, "partial", true},
		{"unclosed valid", codexMeta() + strings.TrimSuffix(codexUsage(5, 2), "\n"), 0, "unavailable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, e := collectUsage(b, strings.NewReader(tc.data))
			if e != nil || r.Events != tc.count || r.Coverage != tc.coverage || r.Unclosed != tc.unclosed {
				t.Fatalf("%+v %v", r, e)
			}
			if tc.name == "zero" {
				requireTokens(t, r.Records[0], 0, 0)
			}
		})
	}
	r, e := collectUsage(b, strings.NewReader(codexMeta()+codexUsage(nil, 2)+codexUsage(10, 4)+codexUsage(15, 6)))
	if e != nil {
		t.Fatal(e)
	}
	if r.Records[0].Input != nil || r.Records[1].Input != nil {
		t.Fatal("missing input became zero")
	}
	requireTokens(t, r.Records[2], 5, 2)
	cb := usageTestBinding(t, "claude")
	r, e = collectUsage(cb, strings.NewReader(claudeUsage("a", nil, 0, "end_turn")))
	if e != nil {
		t.Fatal(e)
	}
	if r.Records[0].Input != nil || r.Records[0].Output == nil || *r.Records[0].Output != 0 {
		t.Fatal("missing/zero lost")
	}
}
func TestCollectRejectsBeforeWritingAndNoLeaks(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	b := usageTestBinding(t, "codex")
	good := codexMeta() + codexUsage(10, 2)
	if e := os.WriteFile(b.File, []byte(good), 0600); e != nil {
		t.Fatal(e)
	}
	args := []string{"--provider", b.Provider, "--session-file", b.File, "--session", b.Session, "--subject", b.Subject, "--entity", b.Entity, "--period", b.Period, "--phase", b.Phase}
	var output bytes.Buffer
	if e := collectUsageCommand(args, &output); e != nil {
		t.Fatal(e)
	}
	original, _ := os.ReadFile(measurementPath())
	bads := []string{
		good + codexUsage(12, 3) + `{"body":"` + usageSecret + `", BROKEN}` + "\n",
		good + codexUsage(2, 1), good + codexUsage(-1, 2), good + codexUsage("secret-"+usageSecret, 2),
		strings.Replace(good, "session-1", "different", 1),
		good + usageJSON(map[string]any{"schema_version": 99, "prompt": usageSecret}),
	}
	for _, data := range bads {
		os.WriteFile(b.File, []byte(data), 0600)
		output.Reset()
		e := collectUsageCommand(args, &output)
		if e == nil {
			t.Fatal("invalid input accepted")
		}
		raw, _ := os.ReadFile(measurementPath())
		if !bytes.Equal(raw, original) {
			t.Fatal("partial batch committed")
		}
		if strings.Contains(output.String()+e.Error()+string(raw), usageSecret) {
			t.Fatal("secret leaked")
		}
	}
	// Late duplicate conflict also preserves the entire previous store.
	r, e := collectUsage(b, strings.NewReader(good))
	if e != nil {
		t.Fatal(e)
	}
	conflict := r.Records[0]
	v := int64(999)
	conflict.Output = &v
	fresh := r.Records[0]
	fresh.ID = "new-valid-event"
	if recordMeasurementBatch([]Measurement{fresh, conflict}) == nil {
		t.Fatal("conflict accepted")
	}
	raw, _ := os.ReadFile(measurementPath())
	if !bytes.Equal(raw, original) {
		t.Fatal("half batch written")
	}
	// Export validates and serializes only the whitelist; it does not write.
	os.WriteFile(b.File, []byte(good+codexUsage(15, 3)), 0600)
	output.Reset()
	if e = collectUsageCommand(append(args, "--export"), &output); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(output.String(), usageSecret) {
		t.Fatal("export leaked")
	}
	raw, _ = os.ReadFile(measurementPath())
	if !bytes.Equal(raw, original) {
		t.Fatal("export changed store")
	}
}
func TestCollectClaudeIdentityAndFinalConflicts(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	b := usageTestBinding(t, "claude")
	for _, data := range []string{
		claudeUsage("a", 1, 2, "end_turn") + strings.Replace(claudeUsage("b", 1, 2, "end_turn"), "req-b", "req-a", 1),
		claudeUsage("a", 1, 2, "end_turn") + strings.Replace(claudeUsage("a", 1, 2, "end_turn"), "req-a", "req-b", 1),
	} {
		if _, e := collectUsage(b, strings.NewReader(data)); e == nil {
			t.Fatal("identity conflict accepted")
		}
	}
	r, e := collectUsage(b, strings.NewReader(claudeUsage("a", 1, 2, "end_turn")))
	if e != nil {
		t.Fatal(e)
	}
	if e = recordMeasurementBatch(r.Records); e != nil {
		t.Fatal(e)
	}
	r, e = collectUsage(b, strings.NewReader(claudeUsage("a", 1, 3, "end_turn")))
	if e != nil {
		t.Fatal(e)
	}
	if recordMeasurementBatch(r.Records) == nil {
		t.Fatal("changed finalized event accepted")
	}
}

// Opt-in smoke: the operator supplies an explicit existing provider session.
// Only aggregate counters are printed; session/path/record bodies stay local.
func TestCollectExistingSessionSmoke(t *testing.T) {
	file := os.Getenv("ORCH_USAGE_SMOKE_FILE")
	if file == "" {
		t.Skip("explicit local session binding required")
	}
	b := usageBinding{Provider: os.Getenv("ORCH_USAGE_SMOKE_PROVIDER"), File: file, Session: os.Getenv("ORCH_USAGE_SMOKE_SESSION"), Subject: "head", Entity: "operator-declared-sample", Period: "usage-smoke", Phase: "execution"}
	t.Setenv("ORCH_HOME", t.TempDir())
	f, e := os.Open(file)
	if e != nil {
		t.Fatal("cannot open smoke source")
	}
	defer f.Close()
	r, e := collectUsage(b, f)
	if e != nil {
		t.Fatal(e)
	}
	if r.Events == 0 {
		t.Fatal("no observed events")
	}
	for i := 0; i < 2; i++ {
		if e = recordMeasurementBatch(r.Records); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := loadMeasurements()
	if e != nil || len(rows) != r.Events {
		t.Fatal("replay mismatch", e)
	}
	var in, out int64
	missing := 0
	for _, m := range rows {
		if m.Input == nil || m.Output == nil {
			missing++
		} else {
			in += *m.Input
			out += *m.Output
		}
	}
	t.Logf("provider=%s lines=%d events=%d missing=%d inclusive_input=%d output=%d coverage=%s replay=unchanged binding=operator-declared", b.Provider, r.Lines, r.Events, missing, in, out, r.Coverage)
}

func TestCollectClaudeSyntheticPlaceholder(t *testing.T) {
	b := usageTestBinding(t, "claude")
	placeholder := strings.Replace(claudeUsage("placeholder", 0, 0, "stop_sequence"), "claude-test", "<synthetic>", 1)
	data := claudeUsage("a", 2, 9, "end_turn") + placeholder + claudeUsage("b", 3, 10, "end_turn")
	r, err := collectUsage(b, strings.NewReader(data))
	if err != nil || len(r.Records) != 2 {
		t.Fatalf("synthetic placeholder aborted provider evidence: records=%d error=%v", len(r.Records), err)
	}
	for _, m := range r.Records {
		if m.Model != "claude-test" {
			t.Fatal("synthetic placeholder became provider evidence")
		}
	}
	// Unknown invalid model metadata still fails with a fixed-string cause.
	bad := strings.Replace(placeholder, "<synthetic>", "invalid "+usageSecret, 1)
	_, err = collectUsage(b, strings.NewReader(bad))
	if err == nil || err.Error() != "invalid provider metadata: invalid model identity" {
		t.Fatalf("invalid metadata cause lost: %v", err)
	}
	// A placeholder cannot hide a session binding mismatch.
	_, err = collectUsage(b, strings.NewReader(strings.ReplaceAll(placeholder, "session-1", "other-session")))
	if err == nil || err.Error() != "provider session binding mismatch" {
		t.Fatalf("placeholder bypassed session validation: %v", err)
	}
}
