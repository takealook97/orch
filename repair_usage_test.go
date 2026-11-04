package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRepairClaudeFinalAndSessionValidation(t *testing.T) {
	b := usageTestBinding(t, "claude")
	good := claudeUsage("a", 2, 3, "end_turn")
	for _, data := range []string{
		good + claudeUsage("a", 2, 4, "end_turn"),
		good + strings.Replace(claudeUsage("b", 1, 1, "end_turn"), `"sessionId":"session-1",`, "", 1),
		strings.Replace(good, "session-1", "other", 1),
	} {
		if _, err := collectUsage(b, strings.NewReader(data)); err == nil {
			t.Fatal("accepted conflicting final or invalid session")
		}
	}
	r, err := collectUsage(b, strings.NewReader(good+good+claudeUsage("a", 99, 99, "")))
	if err != nil || len(r.Records) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	requireTokens(t, r.Records[0], 9, 3)
}

func TestRepairClaudeCopiedObservation(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	b := usageTestBinding(t, "claude")
	data := claudeUsage("a", 2, 3, "end_turn")
	first, err := collectUsage(b, strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err = recordMeasurementBatch(first.Records); err != nil {
		t.Fatal(err)
	}
	b.Session = "session-2"
	copied := strings.ReplaceAll(data, "session-1", b.Session)
	second, err := collectUsage(b, strings.NewReader(copied))
	if err != nil {
		t.Fatal(err)
	}
	if first.Records[0].ID != second.Records[0].ID {
		t.Fatal("session-scoped Claude identity")
	}
	duplicates, err := recordMeasurementBatchWithDuplicates(second.Records)
	if err != nil || duplicates != 1 {
		t.Fatalf("duplicate provenance outcome: %d %v", duplicates, err)
	}
	if err = os.WriteFile(b.File, []byte(copied), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--provider", b.Provider, "--session-file", b.File, "--session", b.Session, "--subject", b.Subject, "--entity", b.Entity, "--period", b.Period, "--phase", b.Phase}
	var summary bytes.Buffer
	if err = collectUsageCommand(args, &summary); err != nil {
		t.Fatal(err)
	}
	var imported usageCollection
	if err = json.Unmarshal(summary.Bytes(), &imported); err != nil || imported.Duplicates != 1 || imported.DuplicateProvenance != "existing-observation-retained" {
		t.Fatalf("duplicate provenance not explicit: %s %v", summary.String(), err)
	}
	rows, err := loadMeasurements()
	if err != nil || len(rows) != 1 || rows[0].Provenance.Session != "session-1" {
		t.Fatalf("duplicate counted or original provenance lost: %+v %v", rows, err)
	}
	original, _ := os.ReadFile(measurementPath())
	for _, change := range []string{"counter", "binding", "request", "message"} {
		badData := copied
		badBinding := b
		switch change {
		case "counter":
			badData = strings.Replace(copied, `"output_tokens":3`, `"output_tokens":4`, 1)
		case "binding":
			badBinding.Entity = "other"
		case "request":
			badData = strings.Replace(copied, "req-a", "req-other", 1)
		case "message":
			badData = strings.Replace(copied, `"id":"a"`, `"id":"other"`, 1)
		}
		bad, e := collectUsage(badBinding, strings.NewReader(badData))
		if e != nil {
			t.Fatal(e)
		}
		fresh, e := collectUsage(b, strings.NewReader(strings.ReplaceAll(claudeUsage("fresh", 1, 1, "end_turn"), "session-1", b.Session)))
		if e != nil {
			t.Fatal(e)
		}
		if recordMeasurementBatch(append(fresh.Records, bad.Records...)) == nil {
			t.Fatalf("accepted %s conflict", change)
		}
		now, _ := os.ReadFile(measurementPath())
		if !bytes.Equal(original, now) {
			t.Fatal("partial batch written")
		}
	}
}

func TestRepairCodexDimensionGaps(t *testing.T) {
	for _, tc := range []struct {
		name, data      string
		inputs, outputs []*int64
	}{
		{"input gap", codexUsage(10, 1) + codexUsage(nil, 2) + codexModel("after") + codexUsage(50, 6) + codexUsage(60, 8), []*int64{tokenPtr(10), nil, nil, tokenPtr(10)}, []*int64{tokenPtr(1), tokenPtr(1), tokenPtr(4), tokenPtr(2)}},
		{"both gap", codexUsage(10, 1) + codexUsage(nil, nil) + codexModel("after") + codexUsage(50, 6) + codexUsage(60, 8), []*int64{tokenPtr(10), nil, nil, tokenPtr(10)}, []*int64{tokenPtr(1), nil, nil, tokenPtr(2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ORCH_HOME", t.TempDir())
			r, err := collectUsage(usageTestBinding(t, "codex"), strings.NewReader(codexMeta()+codexModel("before")+tc.data))
			if err != nil || len(r.Records) != len(tc.inputs) {
				t.Fatalf("%+v %v", r, err)
			}
			for i, m := range r.Records {
				if !sameCounter(m.Input, tc.inputs[i]) || !sameCounter(m.Output, tc.outputs[i]) {
					t.Fatalf("gap interval %d: %+v", i, m)
				}
			}
			if err = recordMeasurementBatch(r.Records); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err = measurementCommand([]string{"report"}, nil, &out); err != nil {
				t.Fatal(err)
			}
			var report struct {
				Groups map[string]struct {
					Input         int64 `json:"observed_input_subtotal"`
					Output        int64 `json:"observed_output_subtotal"`
					WithInput     int   `json:"n_with_input"`
					MissingInput  int   `json:"n_missing_input"`
					WithOutput    int   `json:"n_with_output"`
					MissingOutput int   `json:"n_missing_output"`
				}
			}
			if err = json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			var in, output int64
			var wi, mi, wo, mo int
			for _, g := range report.Groups {
				in += g.Input
				output += g.Output
				wi += g.WithInput
				mi += g.MissingInput
				wo += g.WithOutput
				mo += g.MissingOutput
			}
			expectedOut, expectedWO := int64(8), 4
			if tc.name == "both gap" {
				expectedOut = 3
				expectedWO = 2
			}
			if in != 20 || output != expectedOut || wi != 2 || mi != 2 || wo != expectedWO || mo != 4-expectedWO {
				t.Fatalf("wrong dimension subtotals/coverage: %s", out.String())
			}
		})
	}
}
func tokenPtr(n int64) *int64 { return &n }

func TestRepairClaudeLegacyIdentityFailsClosed(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	b := usageTestBinding(t, "claude")
	r, err := collectUsage(b, strings.NewReader(claudeUsage("a", 2, 3, "end_turn")))
	if err != nil {
		t.Fatal(err)
	}
	legacy := r.Records[0]
	legacy.ID = "usage-" + digestID("claude:"+b.Session+":a")
	provenance := *legacy.Provenance
	provenance.Message = ""
	provenance.Request = ""
	legacy.Provenance = &provenance
	if err = recordMeasurement(legacy); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(measurementPath())
	b.Session = "session-2"
	copied, err := collectUsage(b, strings.NewReader(strings.ReplaceAll(claudeUsage("a", 2, 3, "end_turn"), "session-1", b.Session)))
	if err != nil {
		t.Fatal(err)
	}
	if err = recordMeasurementBatch(copied.Records); err == nil || !strings.Contains(err.Error(), "legacy session-scoped") {
		t.Fatalf("legacy duplication not rejected: %v", err)
	}
	after, _ := os.ReadFile(measurementPath())
	if !bytes.Equal(before, after) {
		t.Fatal("legacy store changed")
	}
}
