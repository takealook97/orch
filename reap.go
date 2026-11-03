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
		return info.Status != "working"
	}
	exists, perr := paneExists(w.Pane)
	return perr == nil && !exists
}

type reaper struct {
	runID      string
	notWorking map[string]int // consecutive polls a finished worker's agent was not working
	idleSince  time.Time
}

func newReaper(runID string) *reaper {
	return &reaper{runID: runID, notWorking: map[string]int{}}
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
	closed := false
	for _, w := range r.liveWorkers() {
		// Never close a worker that has not written its final status line.
		if resultStatus(w.Result) == "" {
			info, infoErr := agentGet(workerAgent(w))
			if infoErr == nil && info.Status == "working" {
				noteCapacityRecovered(rp.runID, w)
			}
			if infoErr == nil && (info.Status == "idle" || info.Status == "done") {
				if screen, err := agentRead(workerAgent(w), 40); err == nil {
					if !capacityText(screen) {
						noteCapacityRecovered(rp.runID, w)
					}
					if _, _, err := resumeOnCapacity(rp.runID, w, screen); err != nil {
						fmt.Fprintf(log, "capacity resume %s: %v\n", w.Name, err)
					}
				}
			}
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
		if err := mergeKnowledge(rp.runID, w.Name, w.Result); err != nil {
			fmt.Fprintf(log, "knowledge merge %s: %v\n", w.Name, err)
		}
		if err := closeWorkerAs(rp.runID, w.Name, w.Pane, true); err != nil {
			fmt.Fprintf(log, "%s reap %s: %v\n", time.Now().Format(time.RFC3339), w.Name, err)
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
