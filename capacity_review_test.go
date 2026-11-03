package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTaskExitZeroAnswerMentioningCapacityIsNotRetried(t *testing.T) {
	taskEnv(t)
	t.Setenv("ORCH_CAPACITY_RETRIES", "2")
	old := capacityDelay
	capacityDelay = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { capacityDelay = old })
	fakeCLI(t, "codex", `echo '{"thread_id":"session-one"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"the model is at capacity, so I will wait"}}'
exit 0`)
	task, err := newTask("ask", "codex-low-h", "explain", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	task.TimeoutMS = 1000
	task.MaxOutput = 2000
	if err = runTask(task); err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" || task.ExitCode != 0 || task.CapacityRetries != 0 || !strings.Contains(task.Answer, "at capacity") {
		t.Fatalf("%+v", task)
	}
}

func TestTaskExitZeroWithErrorEventStillResumes(t *testing.T) {
	if !jsonErrorCapacity([]byte(`{"type":"error","message":"Selected model is at capacity"}`)) ||
		!jsonErrorCapacity([]byte(`{"type":"turn.failed","error":{"message":"model is at capacity"}}`)) ||
		jsonErrorCapacity([]byte(`{"type":"item.completed","item":{"text":"model is at capacity"}}`)) {
		t.Fatal("jsonErrorCapacity mismatch")
	}
}

func TestInlineFallbackExcludesKindThatRan(t *testing.T) {
	// codex-low-h ran after a fallback from another kind; the cr wrapper
	// reported codex spent, so codex must be skipped and the earlier skips kept.
	tried := []string{"gemini-medium-m"}
	skip := withoutKind("explore", "codex", tried)
	if !slices.Contains(skip, "gemini-medium-m") {
		t.Fatalf("skip dropped earlier tries: %v", skip)
	}
	kind := inlineRanKind("codex-low-h", "claude")
	if kind != "codex" {
		t.Fatalf("ran kind = %s", kind)
	}
	if inlineRanKind("missing-key", "claude") != "claude" {
		t.Fatal("unknown key must fall back to the node kind")
	}
}

func TestTailLinesOldErrorAboveLongToolOutput(t *testing.T) {
	text := capacityScreen + "\n• Ran go test\n" + strings.Repeat("    ok line\n", 50) + "• Working (3s)\n"
	if capacityText(text) {
		t.Fatal("old capacity line resurfaced")
	}
	inside := "• Ran go test\n" + strings.Repeat("    ok line\n", 20) + "    " + capacityScreen + "\n    more\n• Working"
	if capacityText(inside) {
		t.Fatal("capacity text inside tool output counted")
	}
	if !capacityText(strings.Repeat("    ok\n", 20) + capacityScreen) {
		t.Fatal("real capacity line at the bottom was dropped")
	}
}

func TestCapacityBackoffFromCurrentIncident(t *testing.T) {
	r, w, sends := capacityFixture(t)
	t.Setenv("ORCH_CAPACITY_RETRIES", "3")
	_, _, _ = resumeOnCapacity(r.ID, w, capacityScreen)
	_ = withRun(r.ID, func(x *Run) error { x.worker("b").CapacitySeen = time.Now().Add(-time.Hour); return nil })
	if act, _, _ := resumeOnCapacity(r.ID, w, capacityScreen); !act || sends.Load() != 1 {
		t.Fatal("first resume not sent")
	}
	got, _ := loadRun(r.ID)
	if !got.worker("b").CapacitySeen.IsZero() || got.worker("b").CapacityRetries != 1 {
		t.Fatalf("seen not cleared: %+v", got.worker("b"))
	}
	// A new sighting starts a new wait, even with an old retry count.
	if act, _, _ := resumeOnCapacity(r.ID, w, capacityScreen); act || sends.Load() != 1 {
		t.Fatal("resumed without waiting for the new incident")
	}
	// Recovery resets the consecutive-failure count.
	if err := capacityRecovered(r.ID, w.Name); err != nil {
		t.Fatal(err)
	}
	got, _ = loadRun(r.ID)
	if got.worker("b").CapacityRetries != 0 || !got.worker("b").CapacitySeen.IsZero() {
		t.Fatalf("not reset: %+v", got.worker("b"))
	}
}

func TestCapacityZeroRetriesExhaustsOnFirstSighting(t *testing.T) {
	r, w, sends := capacityFixture(t)
	t.Setenv("ORCH_CAPACITY_RETRIES", "0")
	act, ex, err := resumeOnCapacity(r.ID, w, capacityScreen)
	if err != nil || act || !ex || sends.Load() != 0 {
		t.Fatalf("%v %v %v", act, ex, err)
	}
}

func TestFallbackCarriesSkippedKindsIntoTried(t *testing.T) {
	r, f := overflowFixture(t)
	fakeCR(t)
	cwd := t.TempDir()
	contract := r.contractPath("original")
	if err := os.WriteFile(contract, []byte("# Contract: original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := withRun(r.ID, func(r *Run) error {
		r.Workers = append(r.Workers, &Worker{Name: "original", Key: "codex-high-m", Kind: "codex", Family: familyOpenAI, Role: roleProduce, Purpose: "impl", Account: "cr", Cwd: cwd, Contract: contract, Tried: []string{"codex-high-m"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	herdrRun = func(args ...string) ([]byte, error) {
		if strings.Join(args[:2], " ") == "pane run" {
			return nil, errors.New("fixture launch stop")
		}
		return f.run(args...)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	_, _ = doFallbackFrom(r.ID, "original", "capacity", true)
	saved, _ := loadRun(r.ID)
	rep := saved.worker("original-f1")
	if rep == nil {
		t.Fatal("no replacement")
	}
	for _, key := range withoutKind("impl", "codex", []string{"codex-high-m"}) {
		if !slices.Contains(rep.Tried, key) {
			t.Fatalf("Tried %v lacks %s", rep.Tried, key)
		}
	}
}
