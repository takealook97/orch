package main

import (
	"strings"
	"testing"
	"time"
)

// A wait that already requested a screen read must not rewrite a worker that a
// competing close finished in the meantime.
func TestWaitKeepsClosedWorkerClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fallback bool
	}{
		{"quota", false},
		{"blocked", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			waitAfterCompetingClose(t, tc.fallback)
		})
	}
}

func waitAfterCompetingClose(t *testing.T, fallback bool) {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "")
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, err := createRun("closed-test", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	const name, pane = "a", "w1:p2"
	if err := withRun(r.ID, func(saved *Run) error {
		w := &Worker{Name: name, Status: "prompted", Pane: pane, Result: r.resultPath(name)}
		if fallback {
			w.Contract = r.contractPath(name)
			w.Tried = make([]string, maxFallbacks)
		}
		saved.Workers = append(saved.Workers, w)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHerd{panes: []string{"w1:p1", pane}, agents: map[string]string{name: "working"}}
	previous := herdrRun
	t.Cleanup(func() { herdrRun = previous })
	var closedAt time.Time
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) < 2 || args[0] != "agent" || args[1] != "read" {
			return fake.run(args...)
		}
		// The close finishes after wait took its snapshot but before the screen reply arrives.
		if err := closeWorker(r.ID, name); err != nil {
			t.Fatalf("competing close: %v", err)
		}
		saved, err := loadRun(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		w := saved.worker(name)
		if w.Status != "closed" || w.Pane != "" || w.Closed.IsZero() || fake.live(pane) {
			t.Fatalf("close did not finish: status=%q pane=%q closed=%t", w.Status, w.Pane, !w.Closed.IsZero())
		}
		closedAt = w.Closed
		return []byte(`{"result":{"text":"Error: You've hit your usage limit. Try again at 5pm"}}`), nil
	}
	outcomes, err := waitWorkers(r.ID, []string{name}, time.Second, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "quota" {
		t.Fatalf("wait outcomes=%+v", outcomes)
	}
	if fallback && !strings.Contains(outcomes[0].Detail, "reached the fallback limit") {
		t.Fatalf("wait did not reach the fallback limit: %+v", outcomes)
	}
	saved, err := loadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	w := saved.worker(name)
	if w.Status != "closed" || w.Pane != "" || !w.Closed.Equal(closedAt) {
		t.Fatalf("after wait: status=%q pane=%q original close time=%t", w.Status, w.Pane, w.Closed.Equal(closedAt))
	}
}
