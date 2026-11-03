package main

import (
	"fmt"
	"time"
)

// resumeOnCapacity reserves each retry under the run lock, so wait and the reaper
// cannot send twice. CapacitySeen is when the current incident was first sighted;
// the backoff is measured from it, and a send clears it so the next sighting waits anew.
// Callers must check that no final result exists and the agent is stopped.
func resumeOnCapacity(runID string, w *Worker, screen string) (acted bool, exhausted bool, err error) {
	if quotaText(screen) || !capacityText(screen) {
		return
	}
	var target Worker
	err = withRun(runID, func(r *Run) error {
		live := r.worker(w.Name)
		if live == nil {
			return fmt.Errorf("worker %s missing", w.Name)
		}
		if live.Pane == "" || live.Status == "closed" || live.Status == "lost" || resultStatus(live.Result) != "" {
			return nil
		}
		max := capacityMaxRetries()
		// agy has no supported session continuation; zero retries allow no resume at all.
		if live.Kind == "agy" || max == 0 {
			exhausted = true
			return nil
		}
		now := time.Now()
		if live.CapacitySeen.IsZero() {
			live.CapacitySeen = now
			r.event("%s capacity: detected; backoff %s", live.Name, capacityBackoff(live.CapacityRetries))
			return nil
		}
		delay := capacityBackoff(live.CapacityRetries)
		if now.Sub(live.CapacitySeen) < delay {
			return nil
		}
		if live.CapacityRetries >= max {
			exhausted = true
			return nil
		}
		live.CapacitySeen = time.Time{}
		live.CapacityRetries++
		r.event("%s capacity: resume %d/%d after %s", live.Name, live.CapacityRetries, max, delay)
		target = *live
		acted = true
		return nil
	})
	if err != nil || !acted {
		return
	}
	err = sendWorkerText(&target, capacityResumeText(target.Name))
	return
}

// capacityRecovered ends the current incident: the agent is working again or the
// screen no longer shows the error. The retry cap counts consecutive failed resumes.
func capacityRecovered(runID, name string) error {
	return withRun(runID, func(r *Run) error {
		if live := r.worker(name); live != nil && (live.CapacityRetries != 0 || !live.CapacitySeen.IsZero()) {
			live.CapacityRetries = 0
			live.CapacitySeen = time.Time{}
		}
		return nil
	})
}

// noteCapacityRecovered resets a worker's capacity state only when it has any,
// so polling loops do not take the run lock for healthy workers.
func noteCapacityRecovered(runID string, w *Worker) {
	if w.CapacityRetries == 0 && w.CapacitySeen.IsZero() {
		return
	}
	_ = capacityRecovered(runID, w.Name)
}

func workerCapacityStatus(w *Worker) string {
	if w.CapacityRetries > 0 {
		return fmt.Sprintf("%s · capacity %d/%d", w.Status, w.CapacityRetries, capacityMaxRetries())
	}
	return w.Status
}
