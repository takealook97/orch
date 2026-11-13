package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	rolloutStarted   = `{"timestamp":"t","type":"event_msg","payload":{"type":"task_started"}}`
	rolloutDone      = `{"timestamp":"t","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"ok"}}`
	rolloutAborted   = `{"timestamp":"t","type":"event_msg","payload":{"type":"turn_aborted"}}`
	rolloutAgentMsg  = `{"timestamp":"t","type":"response_item","payload":{"type":"message","role":"assistant"}}`
	rolloutBlockLine = `{"timestamp":"t","type":"event_msg","payload":{"type":"task_complete","last_agent_message":null,"error":{"message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request.","codex_error_info":"cyber_policy"}}}`
)

func rolloutError(msg string) string {
	return fmt.Sprintf(`{"timestamp":"t","type":"event_msg","payload":{"type":"task_complete","error":{"message":%q}}}`, msg)
}

func writeRollout(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRolloutIncident(t *testing.T) {
	huge := strings.Repeat(rolloutAgentMsg+"\n", (rolloutTailBytes/len(rolloutAgentMsg))+64)
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"cyber error", []string{rolloutStarted, rolloutAgentMsg, rolloutBlockLine}, "content-blocked"},
		{"capacity error", []string{rolloutStarted, rolloutError("Selected model is at capacity. Please try a different model.")}, "capacity"},
		{"new turn after error", []string{rolloutStarted, rolloutBlockLine, rolloutStarted, rolloutAgentMsg}, ""},
		{"aborted after error", []string{rolloutBlockLine, rolloutAborted}, ""},
		{"clean finish", []string{rolloutStarted, rolloutDone}, ""},
		{"quota error", []string{rolloutStarted, rolloutError("You've hit your usage limit. Try again later.")}, ""},
		{"other error", []string{rolloutStarted, rolloutError("stream disconnected before completion")}, ""},
		{"broken lines ignored", []string{rolloutStarted, rolloutBlockLine, `{"type":"event_msg","payload":`, "not json", rolloutAgentMsg}, "content-blocked"},
		{"partial last line ignored", []string{rolloutBlockLine, `{"timestamp":"t","type":"event_msg","payload":{"type":"task_started"}`}, "content-blocked"},
		{"only the tail counts", []string{rolloutBlockLine, rolloutStarted, huge + rolloutError("model is at capacity")}, "capacity"},
		{"error scrolled out of the tail", []string{rolloutStarted, rolloutBlockLine, huge}, ""},
		{"empty", []string{""}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			p := filepath.Join(t.TempDir(), "rollout.jsonl")
			body := strings.Join(tc.lines, "\n")
			if tc.name == "partial last line ignored" {
				body = strings.Join(tc.lines, "\n") // no trailing newline: the last line is still being written
			} else {
				body += "\n"
			}
			if err := os.WriteFile(p, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			// Act
			inc, ok := rolloutIncident(p)
			// Assert
			if ok != (tc.want != "") || inc.name != tc.want {
				t.Fatalf("got %q %v, want %q", inc.name, ok, tc.want)
			}
		})
	}
	if _, ok := rolloutIncident(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Fatal("a missing file is no incident")
	}
}

// fakeCodexHome points codexHomes at a temporary CODEX_HOME and returns its day directory.
func fakeCodexHome(t *testing.T, day time.Time) string {
	t.Helper()
	home := t.TempDir()
	old := codexHomes
	codexHomes = func() []string { return []string{filepath.Join(home, "missing"), home} }
	t.Cleanup(func() { codexHomes = old })
	return filepath.Join(home, "sessions", day.Format("2006/01/02"))
}

func TestFindCodexRollout(t *testing.T) {
	// Arrange
	now := time.Now()
	dir := fakeCodexHome(t, now)
	w := &Worker{Name: "impl", Kind: "codex", Contract: "/runs/r1/contracts/impl.md", Started: now.Add(-5 * time.Minute)}
	prompt := func(name, contract string) string {
		return fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"[orch worker %s] Read contract %s and work"}]}}`, name, contract)
	}
	mine := filepath.Join(dir, "rollout-a-mine.jsonl")
	other := filepath.Join(dir, "rollout-b-other.jsonl")
	stale := filepath.Join(dir, "rollout-c-stale.jsonl")
	sibling := filepath.Join(dir, "rollout-d-sibling.jsonl")
	writeRollout(t, mine, prompt("impl", w.Contract), rolloutStarted)
	writeRollout(t, other, prompt("rev", "/runs/r1/contracts/rev.md"), rolloutStarted)
	writeRollout(t, stale, prompt("impl", w.Contract), rolloutBlockLine)
	// A fallback worker reuses the contract under another name.
	writeRollout(t, sibling, prompt("impl-fb", w.Contract), rolloutStarted)
	old := now.Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	newer := now.Add(time.Second)
	_ = os.Chtimes(other, newer, newer)
	_ = os.Chtimes(sibling, newer, newer)

	// Act
	got := findCodexRollout(w)

	// Assert
	if got != mine {
		t.Fatalf("got %q, want %q", got, mine)
	}
	if findCodexRollout(&Worker{Name: "impl", Kind: "codex", Started: w.Started}) != "" {
		t.Fatal("a worker without a contract matches nothing")
	}
	if findCodexRollout(&Worker{Name: "x", Kind: "codex", Contract: "/runs/r1/contracts/x.md", Started: w.Started}) != "" {
		t.Fatal("an unknown contract matches nothing")
	}
}

func TestCodexRolloutPathCachesAndRateLimits(t *testing.T) {
	// Arrange
	r, w, _ := capacityFixture(t)
	now := time.Now()
	dir := fakeCodexHome(t, now)
	contract := r.contractPath(w.Name)
	_ = withRun(r.ID, func(x *Run) error {
		b := x.worker(w.Name)
		b.Contract, b.Started = contract, now.Add(-time.Minute)
		return nil
	})
	r, _ = loadRun(r.ID)
	w = r.worker("b")

	// Act: nothing on disk yet, so the scan records its time and finds nothing.
	if p := codexRolloutPath(r.ID, w); p != "" {
		t.Fatalf("found %q before the file exists", p)
	}
	p := filepath.Join(dir, "rollout-x.jsonl")
	writeRollout(t, p, promptMarker("b")+" Read contract "+contract, rolloutStarted)

	// Assert: a second scan inside the gap is skipped.
	if got := codexRolloutPath(r.ID, w); got != "" {
		t.Fatalf("rescanned inside the gap: %q", got)
	}
	rolloutScan.Lock()
	delete(rolloutScan.last, r.ID+"/b")
	rolloutScan.Unlock()
	if got := codexRolloutPath(r.ID, w); got != p {
		t.Fatalf("got %q", got)
	}
	saved, _ := loadRun(r.ID)
	if saved.worker("b").Rollout != p || w.Rollout != p {
		t.Fatalf("rollout not stored: %+v", saved.worker("b"))
	}
	if codexRolloutPath(r.ID, &Worker{Name: "c", Kind: "claude", Contract: contract}) != "" {
		t.Fatal("only codex workers have a rollout")
	}
}

// rolloutFixture is blockFixture with Herdr stuck at "working" and the worker's rollout ending in lines.
func rolloutFixture(t *testing.T, screen string, lines ...string) (*Run, *Worker, *[]string) {
	t.Helper()
	r, w, _, texts := blockFixture(t, screen)
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, p, lines...)
	_ = withRun(r.ID, func(x *Run) error { x.worker(w.Name).Rollout = p; return nil })
	base := herdrRun
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "agent" && args[1] == "get" {
			return []byte(`{"result":{"agent":{"agent_status":"working"}}}`), nil
		}
		return base(args...)
	}
	r, _ = loadRun(r.ID)
	return r, r.worker(w.Name), texts
}

func TestReaperSeesRolloutBlockWhileHerdrSaysWorking(t *testing.T) {
	// Arrange
	r, w, texts := rolloutFixture(t, "• Working (3s)\n› ", rolloutStarted, rolloutBlockLine)
	t.Setenv("ORCH_BLOCK_RETRIES", "2")
	rp := newReaper(r.ID)
	var log strings.Builder

	// Act: first pass detects, second pass after the backoff resumes.
	if _, err := rp.once(&log); err != nil {
		t.Fatal(err)
	}
	got, _ := loadRun(r.ID)
	if got.worker(w.Name).BlockSeen.IsZero() || !hasEvent(got, "content-blocked: detected") {
		t.Fatalf("not detected: %+v %q", got.worker(w.Name), got.Events)
	}
	_ = withRun(r.ID, func(x *Run) error { x.worker(w.Name).BlockSeen = time.Now().Add(-time.Hour); return nil })
	if _, err := rp.once(&log); err != nil {
		t.Fatal(err)
	}

	// Assert: the "working" status does not reset the retry count.
	got, _ = loadRun(r.ID)
	if b := got.worker(w.Name); b.BlockRetries != 1 || len(*texts) != 1 {
		t.Fatalf("worker %+v texts %q log %s", b, *texts, log.String())
	}
	if _, err := rp.once(&log); err != nil {
		t.Fatal(err)
	}
	got, _ = loadRun(r.ID)
	if b := got.worker(w.Name); b.BlockRetries != 1 || b.BlockSeen.IsZero() {
		t.Fatalf("retry count reset while the rollout still shows the error: %+v", b)
	}

	// A new turn in the rollout counts as recovery again.
	writeRollout(t, got.worker(w.Name).Rollout, rolloutStarted, rolloutBlockLine, rolloutStarted)
	if _, err := rp.once(&log); err != nil {
		t.Fatal(err)
	}
	got, _ = loadRun(r.ID)
	if b := got.worker(w.Name); b.BlockRetries != 0 || !b.BlockSeen.IsZero() {
		t.Fatalf("new turn did not recover: %+v", b)
	}
}

func TestReaperRolloutCapacityAndExhaustedAlert(t *testing.T) {
	r, w, _ := rolloutFixture(t, "› ", rolloutStarted, rolloutError("Selected model is at capacity."))
	rp := newReaper(r.ID)
	var log strings.Builder
	if _, err := rp.once(&log); err != nil {
		t.Fatal(err)
	}
	got, _ := loadRun(r.ID)
	if got.worker(w.Name).CapacitySeen.IsZero() {
		t.Fatalf("capacity not detected: %+v", got.worker(w.Name))
	}

	r2, w2, _ := rolloutFixture(t, "› ", rolloutBlockLine)
	t.Setenv("ORCH_BLOCK_RETRIES", "0")
	if _, err := newReaper(r2.ID).once(&log); err != nil {
		t.Fatal(err)
	}
	got, _ = loadRun(r2.ID)
	if a := got.worker(w2.Name).Alert; !strings.Contains(a, "cyber_policy") || !strings.Contains(a, "orch fallback b") {
		t.Fatalf("alert %q", a)
	}
}

func TestReaperIgnoresRolloutOfNonCodexAndMissingFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(w *Worker, dir string)
	}{
		{"claude worker", func(w *Worker, _ string) { w.Kind = "claude" }},
		{"rollout gone", func(w *Worker, dir string) { w.Rollout = filepath.Join(dir, "gone.jsonl") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			r, w, _ := rolloutFixture(t, "› ", rolloutBlockLine)
			dir := t.TempDir()
			_ = withRun(r.ID, func(x *Run) error { tc.apply(x.worker(w.Name), dir); return nil })
			fakeCodexHome(t, time.Now())
			var log strings.Builder
			// Act
			if _, err := newReaper(r.ID).once(&log); err != nil {
				t.Fatal(err)
			}
			// Assert
			got, _ := loadRun(r.ID)
			if b := got.worker("b"); !b.BlockSeen.IsZero() || b.Alert != "" {
				t.Fatalf("%+v", b)
			}
		})
	}
}

func TestWaitSeesRolloutBlockWhileHerdrSaysWorking(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			// Arrange
			r, w, texts := rolloutFixture(t, "• Working (3s)\n› ", rolloutStarted, rolloutBlockLine)
			t.Setenv("ORCH_BLOCK_RETRIES", "2")
			old := blockDelay
			blockDelay = func(int) time.Duration { return 8 * time.Millisecond }
			t.Cleanup(func() { blockDelay = old })

			// Act
			out, err := waitWorkers(r.ID, []string{w.Name}, 2*time.Second, fallback)

			// Assert
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
			if len(*texts) != 2 || got.worker("b").BlockRetries != 2 || got.worker("b").Status != "content-blocked" {
				t.Fatalf("texts %q worker %+v", *texts, got.worker("b"))
			}
		})
	}
}

func hasEvent(r *Run, what string) bool {
	for _, e := range r.Events {
		if strings.Contains(e.What, what) {
			return true
		}
	}
	return false
}
