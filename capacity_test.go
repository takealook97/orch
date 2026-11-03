package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const capacityScreen = "■ Selected model is at capacity. Please try a different model."

func TestCapacityHeuristics(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"codex", capacityScreen, true}, {"case", "SERVER IS OVERLOADED", true},
		{"anthropic", `{"error":{"type":"overloaded_error"}}`, true},
		{"blank lines", capacityScreen + strings.Repeat("\n", 30), true},
		{"old", capacityScreen + strings.Repeat("\nnew line", 12), false},
		{"edge", capacityScreen + strings.Repeat("\nnew line", 11), true},
		{"quota", "usage limit reached", false}, {"resume prompt", capacityResumeText("a"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := capacityText(tc.text); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
	for n, want := range []time.Duration{20, 40, 80, 160, 300, 300, 300} {
		if got := capacityBackoff(n); got != want*time.Second {
			t.Fatalf("%d: %s", n, got)
		}
	}
	for _, tc := range []struct {
		value string
		want  int
	}{{"", 8}, {"bad", 8}, {"-1", 8}, {"0", 0}, {"3", 3}} {
		t.Setenv("ORCH_CAPACITY_RETRIES", tc.value)
		if capacityMaxRetries() != tc.want {
			t.Fatal(tc)
		}
	}
}

func capacityFixture(t *testing.T) (*Run, *Worker, *atomic.Int32) {
	t.Helper()
	t.Setenv("ORCH_MUX", "herdr")
	r, f := reapFixture(t)
	err := withRun(r.ID, func(x *Run) error { w := x.worker("b"); w.Kind = "codex"; x.Workers = []*Worker{w}; return nil })
	if err != nil {
		t.Fatal(err)
	}
	r, _ = loadRun(r.ID)
	sends := new(atomic.Int32)
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "agent" && args[1] == "read" {
			return []byte(capacityScreen), nil
		}
		if len(args) > 1 && args[0] == "pane" && args[1] == "send-text" {
			sends.Add(1)
		}
		return f.run(args...)
	}
	old := timings
	timings.KeyGap = 0
	timings.WaitPoll = time.Millisecond
	timings.ScreenRead = 0
	timings.IdleNoResult = time.Millisecond
	t.Cleanup(func() { timings = old })
	return r, r.worker("b"), sends
}

func TestCapacityReservationAndConcurrency(t *testing.T) {
	r, w, sends := capacityFixture(t)
	act, ex, err := resumeOnCapacity(r.ID, w, capacityScreen)
	if err != nil || act || ex {
		t.Fatalf("first detection: %v %v %v", act, ex, err)
	}
	got, _ := loadRun(r.ID)
	if got.worker("b").LastResume.IsZero() || got.worker("b").CapacityRetries != 0 {
		t.Fatal("detection did not start backoff")
	}
	act, _, _ = resumeOnCapacity(r.ID, w, capacityScreen)
	if act {
		t.Fatal("sent inside initial backoff")
	}
	_ = withRun(r.ID, func(x *Run) error { x.worker("b").LastResume = time.Now().Add(-time.Hour); return nil })
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := resumeOnCapacity(r.ID, w, capacityScreen)
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	got, _ = loadRun(r.ID)
	if sends.Load() != 1 || got.worker("b").CapacityRetries != 1 {
		t.Fatalf("sends=%d worker=%+v", sends.Load(), got.worker("b"))
	}
	if !strings.Contains(got.Events[len(got.Events)-1].What, "capacity: resume 1/8 after 20s") {
		t.Fatal(got.Events)
	}
}

func TestWaitCapacitySurvivesIdleAndExhausts(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			r, w, sends := capacityFixture(t)
			t.Setenv("ORCH_CAPACITY_RETRIES", "2")
			old := capacityDelay
			capacityDelay = func(int) time.Duration { return 8 * time.Millisecond }
			t.Cleanup(func() { capacityDelay = old })
			out, err := waitWorkers(r.ID, []string{w.Name}, time.Second, fallback)
			if err != nil || len(out) != 1 || out[0].Status != "capacity" {
				t.Fatalf("%+v %v", out, err)
			}
			if fallback && !strings.Contains(out[0].Detail, "without a contract") {
				t.Fatalf("fallback was not attempted: %+v", out)
			}
			got, _ := loadRun(r.ID)
			if sends.Load() != 2 || got.worker(w.Name).CapacityRetries != 2 || got.worker(w.Name).Status != "capacity" {
				t.Fatalf("sends %d worker %+v", sends.Load(), got.worker(w.Name))
			}
		})
	}
}

func TestReaperCapacityAndQuotaPriority(t *testing.T) {
	r, w, sends := capacityFixture(t)
	_ = withRun(r.ID, func(x *Run) error { x.worker(w.Name).LastResume = time.Now().Add(-time.Hour); return nil })
	rp := newReaper(r.ID)
	var log strings.Builder
	if _, err := rp.once(&log); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 {
		t.Fatalf("reaper sends %d, log %s", sends.Load(), log.String())
	}
	_, _, _ = resumeOnCapacity(r.ID, w, capacityScreen)
	if sends.Load() != 1 {
		t.Fatal("wait and reaper both sent")
	}
	t.Setenv("ORCH_CAPACITY_RETRIES", "1")
	_ = withRun(r.ID, func(x *Run) error { x.worker(w.Name).LastResume = time.Now().Add(-time.Hour); return nil })
	if _, err := rp.once(&log); err != nil {
		t.Fatal(err)
	}
	got, _ := loadRun(r.ID)
	if len(got.Workers) != 1 || got.worker(w.Name).Pane == "" || sends.Load() != 1 {
		t.Fatal("reaper fell back or closed exhausted worker")
	}
	act, ex, err := resumeOnCapacity(r.ID, w, capacityScreen+"\nquota exceeded")
	if act || ex || err != nil {
		t.Fatalf("quota must win: %v %v %v", act, ex, err)
	}
	base := herdrRun
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "agent" && args[1] == "read" {
			return []byte(capacityScreen + "\nquota exceeded"), nil
		}
		return base(args...)
	}
	out, err := waitWorkers(r.ID, []string{w.Name}, time.Second, false)
	if err != nil || len(out) != 1 || out[0].Status != "quota" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestLatestSessionID(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"type":"thread.started","thread_id":"thread"}`, "thread"},
		{"noise\n{\"thread_id\":\"old\"}\n{\"session_id\":\"new\"}", "new"},
		{`{"nested":{"session_id":"nested"}}`, "nested"},
		{"{\n\"session_id\":\"pretty\"\n}", "pretty"},
		{`{"thread_id":12}`, ""}, {capacityScreen, ""},
	} {
		if got := latestSessionID([]byte(tc.raw)); got != tc.want {
			t.Fatalf("%q: %q != %q", tc.raw, got, tc.want)
		}
	}
}

func TestResumeArgs(t *testing.T) {
	for _, m := range catalog {
		for _, kind := range []string{"ask", "do"} {
			a, _ := adapterFor(m.Kind)
			args, err := a.resume(m, kind, "/repo", "/answer", "session-123", "continue")
			if m.Kind == "agy" {
				if err == nil {
					t.Fatal("agy resumed")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			s := strings.Join(args, " ")
			if !strings.Contains(s, "--model") || !strings.HasSuffix(s, "session-123 continue") {
				t.Fatal(s)
			}
			if m.Kind == "codex" {
				if !strings.Contains(s, "resume") || !strings.Contains(s, "--json -o /answer") || !strings.Contains(s, "-C /repo") {
					t.Fatal(s)
				}
				if kind == "ask" && !strings.Contains(s, "--sandbox read-only") || kind == "do" && !strings.Contains(s, codexBypassFlag) {
					t.Fatal(s)
				}
			} else {
				if !strings.Contains(s, "--resume session-123") || !strings.Contains(s, "--output-format json") {
					t.Fatal(s)
				}
				if kind == "ask" && !strings.Contains(s, "--permission-mode dontAsk") || kind == "do" && !strings.Contains(s, "--permission-mode bypassPermissions") {
					t.Fatal(s)
				}
			}
		}
	}
}

func TestTaskCapacityResume(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		for _, mode := range []string{"success", "cap", "no-id", "quota"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				taskEnv(t)
				t.Setenv("ORCH_CAPACITY_RETRIES", "2")
				old := capacityDelay
				capacityDelay = func(int) time.Duration { return time.Millisecond }
				t.Cleanup(func() { capacityDelay = old })
				dir := t.TempDir()
				count := filepath.Join(dir, "count")
				argsLog := filepath.Join(dir, "args")
				script := fmt.Sprintf(`n=0
[ ! -f %q ] || n=$(cat %q)
n=$((n+1))
echo "$n" > %q
printf '%%s\n' "$@" >> %q
`, count, count, count, argsLog)
				if mode != "no-id" {
					script += "echo '{\"thread_id\":\"session-one\"}'\n"
				}
				if mode == "success" {
					script += "if [ \"$n\" -gt 1 ]; then echo '{\"result\":\"finished\",\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"finished\"}}'; exit 0; fi\n"
				}
				script += "echo '" + capacityScreen + "'\n"
				if mode == "quota" {
					script += "echo 'quota exceeded'\n"
				}
				script += "exit 1\n"
				fakeCLI(t, kind, script)
				key := "codex-low-h"
				if kind == "claude" {
					key = "claude-medium-m"
				}
				task, err := newTask("ask", key, "continue me", dir)
				if err != nil {
					t.Fatal(err)
				}
				task.TimeoutMS = 1000
				task.MaxOutput = 2000
				if err = runTask(task); err != nil {
					t.Fatal(err)
				}
				want := 2
				if mode == "success" {
					want = 1
				}
				if mode == "no-id" || mode == "quota" {
					want = 0
				}
				if task.CapacityRetries != want {
					t.Fatalf("%+v", task)
				}
				if mode == "success" {
					if task.Status != "done" || task.Answer != "finished" {
						t.Fatalf("%+v", task)
					}
				} else if task.Status != "failed" {
					t.Fatalf("%+v", task)
				}
				log, _ := os.ReadFile(filepath.Join(task.Dir, "output.log"))
				if strings.Count(string(log), capacityScreen) != map[bool]int{true: 1, false: want + 1}[mode == "success"] {
					t.Fatalf("log lost attempts: %s", log)
				}
				args, _ := os.ReadFile(argsLog)
				if want > 0 && !strings.Contains(string(args), "session-one") {
					t.Fatalf("did not resume session: %s", args)
				}
				var report map[string]any
				_ = json.Unmarshal([]byte(taskReport(task, true)), &report)
				if report["capacity_retries"] != float64(want) {
					t.Fatal(report)
				}
			})
		}
	}
}

func TestCapacityAppDelivery(t *testing.T) {
	r, w, _ := capacityFixture(t)
	t.Setenv("ORCH_MUX", "app")
	old := hcRun
	t.Cleanup(func() { hcRun = old })
	calls := 0
	hcRun = func(args ...string) ([]byte, error) {
		if args[0] == "send" {
			calls++
			if len(args) != 3 || args[1] != "term-b" || args[2] != capacityResumeText("b") {
				t.Fatalf("bad send: %v", args)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("no status available")
	}
	_ = withRun(r.ID, func(x *Run) error {
		live := x.worker(w.Name)
		live.Terminal = "term-b"
		live.LastResume = time.Now().Add(-time.Hour)
		return nil
	})
	acted, ex, err := resumeOnCapacity(r.ID, w, capacityScreen)
	if !acted || ex || err != nil || calls != 1 {
		t.Fatalf("%v %v %v calls %d", acted, ex, err, calls)
	}
}

func TestCapacityNoResumeAfterResultOrForAgy(t *testing.T) {
	for _, mode := range []string{"result", "agy"} {
		t.Run(mode, func(t *testing.T) {
			r, w, sends := capacityFixture(t)
			_ = withRun(r.ID, func(x *Run) error {
				live := x.worker(w.Name)
				live.LastResume = time.Now().Add(-time.Hour)
				if mode == "agy" {
					live.Kind = "agy"
				}
				return nil
			})
			if mode == "result" {
				if err := os.WriteFile(w.Result, []byte("- status: done\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			acted, ex, err := resumeOnCapacity(r.ID, w, capacityScreen)
			if acted || ex != (mode == "agy") || err != nil || sends.Load() != 0 {
				t.Fatalf("%v %v %v sends %d", acted, ex, err, sends.Load())
			}
		})
	}
}

func TestTaskCapacityHonorsOverallBudget(t *testing.T) {
	taskEnv(t)
	fakeCLI(t, "codex", `echo '{"thread_id":"session"}'
echo 'Selected model is at capacity'
exit 1`)
	task, err := newTask("ask", "codex-low-h", "req", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	task.TimeoutMS = 40
	start := time.Now()
	if err = runTask(task); err != nil {
		t.Fatal(err)
	}
	if task.Status != "timeout" || task.CapacityRetries != 0 || time.Since(start) > time.Second {
		t.Fatalf("%+v elapsed %s", task, time.Since(start))
	}
}

func TestQuotaTextTailAndToolOutput(t *testing.T) {
	regression := "  └ func TestDagInlineAskFallsBackOnQuota(t *testing.T) {\n" +
		"        fakeCLI(t, \"codex\", `echo \"ERROR: quota exceeded\"; exit 1`)\n" +
		"• Working (2m 41s • esc to interrupt)\n"
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"regression excerpt", regression, false},
		{"real quota", "■ You've hit your usage limit.\n› ", true},
		{"scrolled away", "usage limit reached" + strings.Repeat("\nnew line", 12), false},
		{"box line", "│ quota exceeded in a test\nidle", false},
		{"ran block", "• Ran go test\n    quota exceeded\n• Working", false},
		{"after ran block", "• Ran go test\n    ok\nERROR: quota exceeded", true},
		{"cr all accounts", "cr: all codex accounts reached capacity", true},
		{"cr exhausted", "cr: capacity exhausted", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaText(tc.text); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
	if !crExhausted("cr: capacity exhausted") || crExhausted("quota exceeded") {
		t.Fatal("crExhausted mismatch")
	}
}

func TestFallbackSkipsSpentKind(t *testing.T) {
	tried := []string{"codex-high-m"}
	plain, ok := nextFallback("analysis", "codex-high-m", "implement", nil, nil, tried)
	if !ok || plain.Kind != "claude" {
		t.Fatalf("baseline: %v %v", plain, ok)
	}
	chain := chains["extract"]
	skipped := withoutKind("extract", "codex", nil)
	m, ok := nextFallback("extract", chain[0], "implement", nil, nil, skipped)
	if !ok || m.Kind != "claude" {
		t.Fatalf("extract: %v %v", m, ok)
	}
	if _, ok := nextFallback("explore", "codex-low-h", "implement", nil, nil, withoutKind("explore", "codex", nil)); !ok {
		t.Fatal("explore should still reach gemini")
	}
}
