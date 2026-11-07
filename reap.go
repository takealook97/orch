package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// A per-run reaper closes finished worker panes without depending on the Head: the Head's
// own orch wait may never run, or may run detached and fail the identity check.

// startReaper launches `orch __reap --run ID` detached unless one already runs; swappable in tests.
var startReaper = func(runID string) error {
	if reaperRunning(runID) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(runDir(runID), "reaper.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	c := exec.Command(exe, "__reap", "--run", runID)
	c.Stdin = nil
	c.Stdout = log
	c.Stderr = log
	c.Env = os.Environ()
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

// ensureReaper starts the run's reaper; a failure only costs automatic closing, so it warns.
func ensureReaper(runID string) {
	if err := startReaper(runID); err != nil {
		fmt.Fprintln(os.Stderr, "orch: warning: reaper not started (finished panes close only through orch wait/collect):", err)
	}
}

// reaperLock takes the run's reaper lock without blocking; ok is false when another holds it.
func reaperLock(runID string) (f *os.File, ok bool, err error) {
	f, err = os.OpenFile(filepath.Join(runDir(runID), ".reaper.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}

func reaperRunning(runID string) bool {
	f, ok, err := reaperLock(runID)
	if err != nil || !ok {
		return err == nil
	}
	f.Close() // closing releases the lock
	return false
}

// agentStopped reports whether a worker's agent is not working. A failed agent lookup means
// the agent is gone only when its pane is gone too; a transient Herdr error is not proof.
func agentStopped(w *Worker) bool {
	info, err := agentGet(workerAgent(w))
	if err == nil {
		return info.Status == "idle" || info.Status == "done" || info.Status == "stopped" || info.Status == "finished"
	}
	id := w.Pane
	if backend() == "app" && w.Terminal != "" {
		id = w.Terminal
	}
	exists, perr := paneExists(id)
	return perr == nil && !exists
}

type reaper struct {
	runID      string
	notWorking map[string]int // consecutive polls a finished worker's agent was not working
	idleSince  time.Time
	stalled    map[string]time.Time // when a worker without a result was first seen stopped
	gone       map[string]int       // consecutive polls a worker's agent could not be found
	blocked    map[string]int       // consecutive polls a worker sat at an approval dialog
}

func newReaper(runID string) *reaper {
	return &reaper{runID: runID, notWorking: map[string]int{}, stalled: map[string]time.Time{},
		gone: map[string]int{}, blocked: map[string]int{}}
}

// supervise keeps a worker without a final result moving: it resumes recoverable stops,
// nudges a worker that went idle, and alerts the Head about anything it cannot fix.
func (rp *reaper) supervise(w *Worker, log io.Writer) {
	stamp := func(format string, args ...any) {
		fmt.Fprintf(log, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
	alert := func(reason string) {
		if err := raiseAlert(rp.runID, w, reason); err != nil {
			stamp("alert %s: %v", w.Name, err)
		}
	}
	info, infoErr := agentGet(workerAgent(w))
	if infoErr != nil {
		delete(rp.stalled, w.Name)
		rp.gone[w.Name]++
		if agentStopped(w) { // the pane itself is gone
			if rp.gone[w.Name] >= 2 {
				alert("its pane is gone and no result was written")
			}
			return
		}
		// The pane lives but no agent answers: the CLI exited to a shell or crashed.
		// Lookups can fail briefly, so only a stretch as long as IdleNoResult counts.
		if time.Duration(rp.gone[w.Name])*timings.ReapPoll >= timings.IdleNoResult && rp.gone[w.Name] >= 3 {
			alert("its agent is no longer running in the pane (the CLI exited or crashed) and no result was written")
		}
		return
	}
	delete(rp.gone, w.Name)
	if info.Status != "blocked" {
		delete(rp.blocked, w.Name)
	}
	switch info.Status {
	case "working":
		delete(rp.stalled, w.Name)
		noteRecovered(rp.runID, w, "", true)
		return
	case "blocked":
		delete(rp.stalled, w.Name)
		rp.blocked[w.Name]++
		if rp.blocked[w.Name] >= 2 {
			alert("it is waiting at an approval or question dialog")
		}
		return
	case "idle", "done":
	default:
		return
	}
	screen, err := agentRead(workerAgent(w), 40)
	if err != nil {
		return
	}
	noteRecovered(rp.runID, w, screen, false)
	if quotaText(screen) {
		delete(rp.stalled, w.Name)
		alert("usage quota is exhausted; run orch fallback " + w.Name)
		return
	}
	if inc, acted, exhausted, err := resumeOnIncident(rp.runID, w, screen); inc.name != "" {
		delete(rp.stalled, w.Name)
		switch {
		case err != nil:
			stamp("%s resume %s: %v", inc.name, w.Name, err)
		case exhausted && inc.name == blockIncident.name:
			alert("Codex keeps stopping it with \"This content can't be shown\" (cyber_policy) after every retry; run orch fallback " + w.Name + " to move it to a non-Codex key")
		case exhausted:
			alert("the model stays at capacity after every retry; run orch fallback " + w.Name)
		case acted:
		}
		return
	}
	if w.Prompted.IsZero() {
		return
	}
	since, ok := rp.stalled[w.Name]
	if !ok {
		rp.stalled[w.Name] = time.Now()
		return
	}
	if time.Since(since) < timings.IdleNoResult {
		return
	}
	acted, exhausted, err := nudgeIdle(rp.runID, w)
	switch {
	case err != nil:
		stamp("nudge %s: %v", w.Name, err)
	case acted:
		rp.stalled[w.Name] = time.Now()
	case exhausted:
		alert("it stopped without writing a result and did not resume after a nudge")
	}
}

// once runs one reaper pass and reports whether the reaper should exit.
func (rp *reaper) once(log io.Writer) (bool, error) {
	r, err := loadRun(rp.runID)
	if err != nil {
		return true, err
	}
	if r.Status != "active" {
		return true, nil
	}
	if err := ownRun(r); err != nil {
		return true, err
	}
	if r.EndRequested {
		_, err := closeRun(r.ID, true)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	closed := false
	for _, w := range r.liveWorkers() {
		// Never close a worker that has not written its final status line.
		if resultStatus(w.Result) == "" {
			rp.supervise(w, log)
			delete(rp.notWorking, w.Name)
			continue
		}
		if !agentStopped(w) {
			delete(rp.notWorking, w.Name)
			continue
		}
		rp.notWorking[w.Name]++
		if rp.notWorking[w.Name] < 2 {
			continue
		}
		if _, _, err := collectWorker(rp.runID, w.Name); err != nil {
			fmt.Fprintf(log, "%s reap %s: %v\n", time.Now().Format(time.RFC3339), w.Name, err)
			continue
		}
		removed := false
		if err := withRun(rp.runID, func(r *Run) error {
			if x := r.worker(w.Name); x != nil && x.Pane == "" {
				removed = true
				x.Reaped = true
				r.event("reap %s", w.Name)
			}
			return nil
		}); err != nil {
			return false, err
		}
		if !removed {
			continue
		}
		delete(rp.notWorking, w.Name)
		closed = true
		fmt.Fprintf(log, "%s reap %s: pane closed\n", time.Now().Format(time.RFC3339), w.Name)
	}
	if closed {
		if left, _ := ownedPanes(r.HeadPane); len(left) > 0 {
			if err := normalizeOwnedLayouts(r.HeadPane); err != nil {
				fmt.Fprintf(log, "%s layout: %v\n", time.Now().Format(time.RFC3339), err)
			}
		}
	}
	if r, err = loadRun(rp.runID); err != nil {
		return true, err
	}
	if len(r.liveWorkers()) > 0 {
		rp.idleSince = time.Time{}
		return false, nil
	}
	if rp.idleSince.IsZero() {
		rp.idleSince = time.Now()
	}
	return time.Since(rp.idleSince) > timings.ReapIdle, nil
}

// reapCommand is `orch __reap --run ID`: one reaper per run, until the run closes or has
// had no live workers for a while.
func reapCommand(runID string, log io.Writer) int {
	if runID == "" {
		fmt.Fprintln(log, "orch: __reap requires --run")
		return 1
	}
	lock, ok, err := reaperLock(runID)
	if err != nil {
		fmt.Fprintln(log, "orch:", err)
		return 1
	}
	if !ok {
		return 0 // another reaper serves this run
	}
	defer lock.Close()
	rp := newReaper(runID)
	for {
		done, err := rp.once(log)
		if err != nil {
			fmt.Fprintf(log, "%s reaper: %v\n", time.Now().Format(time.RFC3339), err)
		}
		if done {
			fmt.Fprintf(log, "%s reaper exits\n", time.Now().Format(time.RFC3339))
			if err != nil {
				return 1
			}
			return 0
		}
		time.Sleep(timings.ReapPoll)
	}
}
