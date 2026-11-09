package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const blockScreen = "ⓘ This content can’t be shown\nWe take extra care with some cybersecurity requests. If you’re doing authorized security work, apply for Daybreak to get broader access.\n› "

// The cyber_policy event as Codex writes it to its session log.
const blockJSON = `{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":null,"error":{"message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request.","codex_error_info":"cyber_policy"}}}`

func TestBlockHeuristics(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"tui curly", blockScreen, true},
		{"tui straight", "This content can't be shown", true},
		{"older wording", "We take extra caution with cybersecurity requests.", true},
		{"daybreak on", "Some cybersecurity requests are still limited, even when Daybreak is on.", true},
		{"json", blockJSON, true},
		{"scrolled away", blockScreen + strings.Repeat("\nnew line", 12), false},
		{"tool output", "• Ran grep\n    This content can't be shown\n• Working", false},
		{"plain work", "• Edited heuristics.go (+12 -3)\n› ", false},
		{"capacity", capacityScreen, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockText(tc.text); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
	// None of orch's own follow-up prompts may trip a detector when echoed on screen.
	for n := 0; n < 6; n++ {
		if s := blockResumeText("w", n); blockText(s) || capacityText(s) || quotaText(s) {
			t.Fatalf("resume %d trips a detector: %s", n, s)
		}
	}
	if s := idleNudgeText("w", "/r.md"); blockText(s) || capacityText(s) || quotaText(s) {
		t.Fatal(s)
	}
	if blockResumeText("w", 0) == blockResumeText("w", 1) {
		t.Fatal("retries should rephrase")
	}
	for n, want := range []time.Duration{15, 30, 60, 120, 120} {
		if got := blockBackoff(n); got != want*time.Second {
			t.Fatalf("%d: %s", n, got)
		}
	}
	t.Setenv("ORCH_BLOCK_RETRIES", "0")
	if blockMaxRetries() != 0 {
		t.Fatal("env override")
	}
	if inc, ok := screenIncident(blockScreen); !ok || inc.name != "content-blocked" {
		t.Fatal(inc.name)
	}
	if _, ok := screenIncident(blockScreen + "\nquota exceeded"); ok {
		t.Fatal("quota must win")
	}
}

// blockFixture is capacityFixture with Codex's cyber_policy screen on the worker.
func blockFixture(t *testing.T, screen string) (*Run, *Worker, *atomic.Int32, *[]string) {
	t.Helper()
	r, w, sends := capacityFixture(t)
	var texts []string
	base := herdrRun
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "agent" && args[1] == "read" {
			return []byte(screen), nil
		}
		if len(args) > 3 && args[0] == "pane" && args[1] == "send-text" {
			texts = append(texts, args[3])
		}
		return base(args...)
	}
	oldWake, oldNotify := wakeHead, notifyUser
	wakeHead = func(string, headAlert) bool { return false }
	notifyUser = func(string, string) {}
	t.Cleanup(func() { wakeHead, notifyUser = oldWake, oldNotify })
	return r, w, sends, &texts
}

func TestBlockResumeRephrasesAndExhausts(t *testing.T) {
	r, w, sends, texts := blockFixture(t, blockScreen)
	t.Setenv("ORCH_BLOCK_RETRIES", "2")
	inc, acted, ex, err := resumeOnIncident(r.ID, w, blockScreen)
	if inc.name != "content-blocked" || acted || ex || err != nil {
		t.Fatalf("first sighting only starts the backoff: %v %v %v", acted, ex, err)
	}
	for i := 0; i < 2; i++ {
		_ = withRun(r.ID, func(x *Run) error { x.worker("b").BlockSeen = time.Now().Add(-time.Hour); return nil })
		if _, acted, _, err = resumeOnIncident(r.ID, w, blockScreen); !acted || err != nil {
			t.Fatalf("retry %d: %v %v", i, acted, err)
		}
	}
	got, _ := loadRun(r.ID)
	if b := got.worker("b"); b.BlockRetries != 2 || b.CapacityRetries != 0 || sends.Load() != 2 {
		t.Fatalf("worker %+v sends %d", b, sends.Load())
	}
	if len(*texts) != 2 || (*texts)[0] == (*texts)[1] || !strings.HasPrefix((*texts)[0], promptMarker("b")) {
		t.Fatalf("texts %q", *texts)
	}
	_ = withRun(r.ID, func(x *Run) error { x.worker("b").BlockSeen = time.Now().Add(-time.Hour); return nil })
	if _, acted, ex, _ = resumeOnIncident(r.ID, w, blockScreen); acted || !ex {
		t.Fatal("retries should be exhausted")
	}
	// Working again ends the incident.
	got, _ = loadRun(r.ID)
	noteRecovered(r.ID, got.worker("b"), "", true)
	got, _ = loadRun(r.ID)
	if b := got.worker("b"); b.BlockRetries != 0 || !b.BlockSeen.IsZero() {
		t.Fatalf("%+v", b)
	}
	if !strings.Contains(workerCapacityStatus(&Worker{Status: "prompted", BlockRetries: 1}), "content-blocked 1/") {
		t.Fatal("status line")
	}
}

func TestWaitBlockExhaustsAndLeavesCodex(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			r, w, sends, _ := blockFixture(t, blockScreen)
			t.Setenv("ORCH_BLOCK_RETRIES", "2")
			old := blockDelay
			blockDelay = func(int) time.Duration { return 8 * time.Millisecond }
			t.Cleanup(func() { blockDelay = old })
			out, err := waitWorkers(r.ID, []string{w.Name}, time.Second, fallback)
			if err != nil || len(out) != 1 || out[0].Status != "content-blocked" {
				t.Fatalf("%+v %v", out, err)
			}
			if fallback && !strings.Contains(out[0].Detail, "without a contract") {
				t.Fatalf("fallback was not attempted: %+v", out)
			}
			if !fallback && !strings.Contains(out[0].Detail, "orch fallback b") {
				t.Fatalf("no next step: %+v", out)
			}
			got, _ := loadRun(r.ID)
			if sends.Load() != 2 || got.worker("b").Status != "content-blocked" {
				t.Fatalf("sends %d worker %+v", sends.Load(), got.worker("b"))
			}
			if !blockedCodex(r.ID, "b") {
				t.Fatal("manual fallback would stay on Codex")
			}
		})
	}
}

func TestReaperNudgesThenAlertsHead(t *testing.T) {
	r, w, sends, texts := blockFixture(t, "• Edited a.go\n› ")
	t.Setenv("ORCH_IDLE_NUDGES", "1")
	_ = withRun(r.ID, func(x *Run) error { x.worker(w.Name).Prompted = time.Now(); return nil })
	rp := newReaper(r.ID)
	var log strings.Builder
	pass := func() {
		t.Helper()
		if _, err := rp.once(&log); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	pass() // first sighting
	if sends.Load() != 0 {
		t.Fatal("nudged on first sighting")
	}
	pass() // idle past IdleNoResult: nudge
	if sends.Load() != 1 || !strings.Contains((*texts)[0], "before writing the result file") {
		t.Fatalf("sends %d texts %q log %s", sends.Load(), *texts, log.String())
	}
	pass()
	pass() // nudges exhausted: alert
	got, _ := loadRun(r.ID)
	if b := got.worker("b"); b.Nudges != 1 || !strings.Contains(b.Alert, "did not resume after a nudge") {
		t.Fatalf("%+v\n%s", b, log.String())
	}
	pass() // the same stall is not reported twice
	alerts := takeAlerts(r.HeadPane)
	if len(alerts) != 1 || alerts[0].Worker != "b" || alerts[0].Run != r.ID {
		t.Fatalf("%+v", alerts)
	}
	if takeAlerts(r.HeadPane) != nil {
		t.Fatal("alerts were not drained")
	}
	// Working again clears the alert, so a later stall is reported anew.
	got, _ = loadRun(r.ID)
	noteRecovered(r.ID, got.worker("b"), "", true)
	got, _ = loadRun(r.ID)
	if got.worker("b").Alert != "" {
		t.Fatal("alert not cleared")
	}
}

func TestReaperAlertsOnExhaustedBlockAndDialog(t *testing.T) {
	r, w, _, _ := blockFixture(t, blockScreen)
	t.Setenv("ORCH_BLOCK_RETRIES", "0")
	rp := newReaper(r.ID)
	var log strings.Builder
	if _, err := rp.once(&log); err != nil {
		t.Fatal(err)
	}
	got, _ := loadRun(r.ID)
	if a := got.worker(w.Name).Alert; !strings.Contains(a, "cyber_policy") || !strings.Contains(a, "orch fallback b") {
		t.Fatalf("alert %q", a)
	}
	// A worker parked at an approval dialog is reported after two polls.
	r2, f := reapFixture(t)
	oldWake, oldNotify := wakeHead, notifyUser
	wakeHead = func(string, headAlert) bool { return false }
	notifyUser = func(string, string) {}
	t.Cleanup(func() { wakeHead, notifyUser = oldWake, oldNotify })
	f.agents["b"] = "blocked"
	rp = newReaper(r2.ID)
	for i := 0; i < 2; i++ {
		if _, err := rp.once(&log); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = loadRun(r2.ID)
	if !strings.Contains(got.worker("b").Alert, "approval or question dialog") {
		t.Fatalf("%+v", got.worker("b"))
	}
}

func TestRaiseAlertWakesIdleHead(t *testing.T) {
	r, w, _, _ := blockFixture(t, blockScreen)
	var woke []headAlert
	wakeHead = func(head string, a headAlert) bool { woke = append(woke, a); return head == r.HeadPane }
	if err := raiseAlert(r.ID, w, "stopped"); err != nil {
		t.Fatal(err)
	}
	if len(woke) != 1 || takeAlerts(r.HeadPane) != nil {
		t.Fatalf("woken Head should not also get a queued copy: %+v", woke)
	}
	t.Setenv("ORCH_HEAD_WAKE", "0")
	_ = withRun(r.ID, func(x *Run) error { x.worker("b").Alert = ""; return nil })
	if err := raiseAlert(r.ID, w, "stopped"); err != nil {
		t.Fatal(err)
	}
	if len(woke) != 1 || len(takeAlerts(r.HeadPane)) != 1 {
		t.Fatal("ORCH_HEAD_WAKE=0 must queue instead of waking")
	}
}

func TestHeadHooksCarryAlerts(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	a := headAlert{At: time.Now(), Run: "run-1", Worker: "impl", Reason: "it stopped"}
	if err := queueAlert("w1:p1", a); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	in := `{"tool_name":"Bash","tool_input":{"command":"ls"}}`
	if runHook("claude", strings.NewReader(in), &out, os.Stderr) != 0 {
		t.Fatal("exit")
	}
	var v struct {
		H map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.H["hookEventName"] != "PreToolUse" || !strings.Contains(v.H["additionalContext"], "worker impl (run run-1): it stopped") || v.H["permissionDecision"] != "" {
		t.Fatalf("%s %v", out.String(), err)
	}
	out.Reset()
	if runHook("claude", strings.NewReader(in), &out, os.Stderr); out.Len() != 0 {
		t.Fatalf("drained alert repeated: %s", out.String())
	}
	_ = queueAlert("w1:p1", a)
	if runHook("codex-prompt", strings.NewReader(`{"prompt":"hi"}`), &out, os.Stderr); !strings.Contains(out.String(), "UserPromptSubmit") || !strings.Contains(out.String(), "impl") {
		t.Fatalf("prompt hook: %s", out.String())
	}
	// Workers never receive Head alerts.
	_ = queueAlert("w1:p1", a)
	t.Setenv("ORCH_ROLE", "worker")
	out.Reset()
	runHook("claude-prompt", strings.NewReader(`{"prompt":"hi"}`), &out, os.Stderr)
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(alertsDir(), "w1:p1.jsonl")); err != nil {
		t.Fatal("worker drained the Head's alerts")
	}
}

func TestTaskBlockResume(t *testing.T) {
	for _, mode := range []string{"recovers", "exhausts"} {
		t.Run(mode, func(t *testing.T) {
			taskEnv(t)
			t.Setenv("ORCH_BLOCK_RETRIES", "2")
			old := blockDelay
			blockDelay = func(int) time.Duration { return time.Millisecond }
			t.Cleanup(func() { blockDelay = old })
			dir := t.TempDir()
			count := filepath.Join(dir, "count")
			argsLog := filepath.Join(dir, "args")
			script := fmt.Sprintf(`n=0
[ ! -f %q ] || n=$(cat %q)
n=$((n+1))
echo "$n" > %q
printf '%%s\n' "$@" >> %q
echo '{"thread_id":"session-one"}'
`, count, count, count, argsLog)
			if mode == "recovers" {
				script += "if [ \"$n\" -gt 1 ]; then echo '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"finished\"}}'; exit 0; fi\n"
			}
			// Codex exits 0 after a policy stop; the JSON error event is the signal.
			script += "echo '" + blockJSON + "'\nexit 0\n"
			fakeCLI(t, "codex", script)
			task, err := newTask("ask", "codex-low-h", "refactor the parser", dir)
			if err != nil {
				t.Fatal(err)
			}
			// The fake exits on completion; allow ample time for process startup under load.
			task.TimeoutMS = 30000
			task.MaxOutput = 2000
			if err = runTask(task); err != nil {
				t.Fatal(err)
			}
			args, _ := os.ReadFile(argsLog)
			if !strings.Contains(string(args), "session-one") || strings.Contains(strings.ToLower(string(args)), "cyber") {
				t.Fatalf("resume args: %s", args)
			}
			if mode == "recovers" {
				if task.Status != "done" || task.BlockRetries != 1 || task.Answer != "finished" {
					t.Fatalf("%+v", task)
				}
				return
			}
			if task.Status != "failed" || task.BlockRetries != 2 || task.CapacityRetries != 0 {
				t.Fatalf("%+v", task)
			}
			if !strings.Contains(taskReport(task, false), "content-blocked retries: 2/2") {
				t.Fatal(taskReport(task, false))
			}
		})
	}
}

func TestReaperAlertsWhenAgentExits(t *testing.T) {
	r, f := reapFixture(t)
	oldWake, oldNotify := wakeHead, notifyUser
	wakeHead = func(string, headAlert) bool { return false }
	notifyUser = func(string, string) {}
	t.Cleanup(func() { wakeHead, notifyUser = oldWake, oldNotify })
	old := timings
	timings.ReapPoll = time.Second
	timings.IdleNoResult = 3 * time.Second
	t.Cleanup(func() { timings = old })
	delete(f.agents, "b") // the pane w1:p3 stays, but no agent answers
	rp := newReaper(r.ID)
	var log strings.Builder
	for i := 0; i < 2; i++ {
		_, _ = rp.once(&log)
	}
	got, _ := loadRun(r.ID)
	if got.worker("b").Alert != "" {
		t.Fatal("alerted on a brief lookup failure")
	}
	_, _ = rp.once(&log)
	got, _ = loadRun(r.ID)
	if !strings.Contains(got.worker("b").Alert, "no longer running") {
		t.Fatalf("%+v\n%s", got.worker("b"), log.String())
	}
}

func TestNudgeReservationSendsOnce(t *testing.T) {
	r, w, sends, _ := blockFixture(t, "• Edited a.go\n› ")
	old := timings
	timings.IdleNoResult = time.Hour
	t.Cleanup(func() { timings = old })
	_ = withRun(r.ID, func(x *Run) error { x.worker(w.Name).Prompted = time.Now(); return nil })
	done := make(chan [2]bool, 2)
	for i := 0; i < 2; i++ {
		go func() {
			acted, exhausted, err := nudgeIdle(r.ID, w)
			if err != nil {
				t.Error(err)
			}
			done <- [2]bool{acted, exhausted}
		}()
	}
	a, b := <-done, <-done
	if sends.Load() != 1 || a[0] == b[0] || a[1] || b[1] {
		t.Fatalf("sends %d results %v %v", sends.Load(), a, b)
	}
}

func TestAlertQueueSurvivesConcurrentDrain(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	const n = 50
	got := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			if err := queueAlert("h", headAlert{Worker: fmt.Sprint(i), Reason: "x"}); err != nil {
				t.Error(err)
			}
		}
	}()
	for {
		got += len(takeAlerts("h"))
		select {
		case <-done:
			got += len(takeAlerts("h"))
			if got != n {
				t.Fatalf("got %d of %d alerts", got, n)
			}
			return
		default:
		}
	}
}

func TestDeliverAlertWakesOncePerGapAndRetriesOnFailure(t *testing.T) {
	r, _, _, _ := blockFixture(t, blockScreen)
	wakes := 0
	wakeHead = func(string, headAlert) bool { wakes++; return true }
	_ = withRun(r.ID, func(x *Run) error {
		x.Workers = append(x.Workers, &Worker{Name: "e", Pane: "w1:p9", Status: "prompted", Result: x.resultPath("e")})
		return nil
	})
	got, _ := loadRun(r.ID)
	if err := raiseAlert(r.ID, got.worker("b"), "one"); err != nil {
		t.Fatal(err)
	}
	if err := raiseAlert(r.ID, got.worker("e"), "two"); err != nil {
		t.Fatal(err)
	}
	if q := takeAlerts(r.HeadPane); wakes != 1 || len(q) != 1 || q[0].Worker != "e" {
		t.Fatalf("wakes %d queued %+v", wakes, q)
	}
	// A failed delivery leaves no mark, so the next poll retries.
	t.Setenv("ORCH_HEAD_WAKE", "0")
	if err := os.Mkdir(alertPath(r.HeadPane), 0700); err != nil {
		t.Fatal(err)
	}
	if err := raiseAlert(r.ID, got.worker("b"), "three"); err == nil {
		t.Fatal("delivery into a directory should fail")
	}
	if x, _ := loadRun(r.ID); x.worker("b").Alert != "" {
		t.Fatal("failed alert stayed marked")
	}
	_ = os.Remove(alertPath(r.HeadPane))
	if err := raiseAlert(r.ID, got.worker("b"), "three"); err != nil || len(takeAlerts(r.HeadPane)) != 1 {
		t.Fatal("retry was not delivered")
	}
}

func TestTaskStopPrefersErrorEvents(t *testing.T) {
	answer := `{"type":"item.completed","item":{"type":"agent_message","text":"Investigating cyber_policy handling"}}`
	failed := `{"type":"turn.failed","error":{"message":"Selected model is at capacity"}}`
	raw := []byte(answer + "\n" + failed + "\n")
	if inc, ok := taskStop(raw, true); !ok || inc.name != "capacity" {
		t.Fatalf("clean exit: %v %v", inc.name, ok)
	}
	if _, ok := taskStop([]byte(answer+"\n"), true); ok {
		t.Fatal("answer text alone stopped a clean task")
	}
	if inc, ok := taskStop([]byte(blockScreen), false); !ok || inc.name != "content-blocked" {
		t.Fatal("failed exit should read the tail")
	}
}
