package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHerd is an in-memory Herdr: live panes and per-agent status.
type fakeHerd struct {
	mu     sync.Mutex
	panes  []string
	agents map[string]string // name → agent status; missing means the agent is gone
}

func (f *fakeHerd) run(args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case len(args) >= 2 && args[0] == "pane" && args[1] == "list":
		var rows []string
		for _, p := range f.panes {
			rows = append(rows, fmt.Sprintf(`{"pane_id":%q}`, p))
		}
		return []byte(`{"result":{"panes":[` + strings.Join(rows, ",") + `]}}`), nil
	case len(args) >= 3 && args[0] == "pane" && args[1] == "close":
		f.panes = slices.DeleteFunc(f.panes, func(p string) bool { return p == args[2] })
		return []byte(`{"result":{}}`), nil
	case len(args) >= 3 && args[0] == "agent" && args[1] == "get":
		st, ok := f.agents[args[2]]
		if !ok {
			return nil, errors.New("agent not found")
		}
		return []byte(fmt.Sprintf(`{"result":{"agent":{"agent_status":%q}}}`, st)), nil
	}
	return []byte(`{"result":{}}`), nil
}

func (f *fakeHerd) live(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.panes, p)
}

// reapFixture creates an active run of Head w1:p1 with workers a (done, idle), b (no status
// line, idle), c (done, still working) and d (partial, agent and pane gone).
func reapFixture(t *testing.T) (*Run, *fakeHerd) {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, err := createRun("x", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]string{"a": "- status: done\n", "b": "working on it\n", "c": "- status: done\n", "d": "- summary: half\n- status: partial\n"}
	for n, body := range results {
		if err := os.WriteFile(r.resultPath(n), []byte("# Result: "+n+"\n"+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_ = withRun(r.ID, func(x *Run) error {
		for i, n := range []string{"a", "b", "c", "d"} {
			x.Workers = append(x.Workers, &Worker{Name: n, Pane: fmt.Sprintf("w1:p%d", i+2), Status: "prompted", Result: r.resultPath(n)})
		}
		return nil
	})
	f := &fakeHerd{panes: []string{"w1:p1", "w1:p2", "w1:p3", "w1:p4"}, agents: map[string]string{"a": "idle", "b": "idle", "c": "working"}}
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	herdrRun = f.run
	return r, f
}

func TestReaperClosesOnlyFinishedIdleWorkers(t *testing.T) {
	r, f := reapFixture(t)
	rp := newReaper(r.ID)
	var log strings.Builder
	if done, err := rp.once(&log); done || err != nil {
		t.Fatalf("first pass: done=%v err=%v", done, err)
	}
	if !f.live("w1:p2") {
		t.Fatal("a worker was closed after a single idle poll")
	}
	if done, err := rp.once(&log); done || err != nil {
		t.Fatalf("second pass: done=%v err=%v", done, err)
	}
	if f.live("w1:p2") || got0(r).worker("d").Pane != "" {
		t.Fatalf("finished idle or gone workers not reaped: %v\n%s", f.panes, log.String())
	}
	if !f.live("w1:p3") {
		t.Fatal("worker without a status line was closed")
	}
	if !f.live("w1:p4") {
		t.Fatal("worker still working was closed")
	}
	got, _ := loadRun(r.ID)
	if a := got.worker("a"); a.Pane != "" || !a.Reaped || a.Status != "done" {
		t.Fatalf("a = %+v", a)
	}
	events := ""
	for _, e := range got.Events {
		events += e.What + "\n"
	}
	if !strings.Contains(events, "reap a") || !strings.Contains(events, "reap d") {
		t.Fatal(events)
	}

	// Collect and wait report reaped workers as finished, as if they had closed them.
	out, err := collect(r.ID, "d")
	if err != nil || !strings.Contains(out, "status: partial") || !strings.Contains(out, "closed by the reaper") {
		t.Fatalf("collect: %q %v", out, err)
	}
	outs, err := waitWorkers(r.ID, nil, time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	var a *waitOutcome
	for i := range outs {
		if outs[i].Name == "a" {
			a = &outs[i]
		}
	}
	if a == nil || a.Status != "done" || !strings.Contains(a.Detail, "reaper") {
		t.Fatalf("outs = %+v", outs)
	}
	for _, o := range outs {
		if o.Name == "d" {
			t.Fatalf("worker reported twice: %+v", o)
		}
	}
	got, _ = loadRun(r.ID)
	if got.worker("a").Reaped || got.worker("d").Reaped {
		t.Fatal("reported workers keep the reaper mark")
	}

	// The reaper exits once the run is closed.
	_ = withRun(r.ID, func(x *Run) error { x.Status = "closed"; return nil })
	if done, _ := rp.once(&log); !done {
		t.Fatal("reaper keeps running after the run closed")
	}
}

func got0(r *Run) *Run {
	x, _ := loadRun(r.ID)
	return x
}

// A failed agent lookup while the pane still exists is a transient error, not a gone agent.
func TestReaperKeepsWorkerWhenAgentLookupFailsButPaneLives(t *testing.T) {
	r, f := reapFixture(t)
	delete(f.agents, "a")
	rp := newReaper(r.ID)
	var log strings.Builder
	for i := 0; i < 3; i++ {
		rp.once(&log)
	}
	if !f.live("w1:p2") {
		t.Fatalf("worker closed on agent lookup errors while its pane lives\n%s", log.String())
	}
	if w := got0(r).worker("a"); w.Pane == "" {
		t.Fatalf("a = %+v", w)
	}
}

func TestReaperExitsWhenIdle(t *testing.T) {
	r, _ := reapFixture(t)
	old := timings.ReapIdle
	t.Cleanup(func() { timings.ReapIdle = old })
	timings.ReapIdle = 0
	_ = withRun(r.ID, func(x *Run) error {
		for _, w := range x.Workers {
			w.Pane, w.Status = "", "closed"
		}
		return nil
	})
	rp := newReaper(r.ID)
	var log strings.Builder
	rp.once(&log) // first pass starts the idle clock
	time.Sleep(time.Millisecond)
	if done, err := rp.once(&log); !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
}

func TestReaperSingletonAndOwnership(t *testing.T) {
	r, f := reapFixture(t)
	lock, ok, err := reaperLock(r.ID)
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	if !reaperRunning(r.ID) {
		t.Fatal("held lock not seen")
	}
	var log strings.Builder
	if code := reapCommand(r.ID, &log); code != 0 {
		t.Fatalf("second reaper: %d %s", code, log.String())
	}
	lock.Close()
	if reaperRunning(r.ID) {
		t.Fatal("released lock still seen")
	}
	// A reaper started from another pane never touches this Head's panes.
	t.Setenv("HERDR_PANE_ID", "w1:p9")
	if code := reapCommand(r.ID, &log); code != 1 {
		t.Fatalf("foreign reaper: %d %s", code, log.String())
	}
	if !f.live("w1:p2") {
		t.Fatal("foreign reaper closed a pane")
	}
}

func TestReapIsInternalAndWorkerRefused(t *testing.T) {
	r, _ := reapFixture(t)
	t.Setenv("ORCH_ROLE", "worker")
	var out, errout strings.Builder
	if code := run([]string{"__reap", "--run", r.ID}, strings.NewReader(""), &out, &errout); code == 0 || !strings.Contains(errout.String(), "Head only") {
		t.Fatalf("%d %s", code, errout.String())
	}
}
