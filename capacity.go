package main

import (
	"fmt"
	"time"
)

// resumeOnCapacity reserves each retry under the run lock, so wait and the reaper
// cannot send twice. LastResume starts as the first detection time (retry zero).
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
		// agy has no supported session continuation.
		if live.Kind == "agy" {
			exhausted = true
			return nil
		}
		now := time.Now()
		if live.LastResume.IsZero() {
			live.LastResume = now
			r.event("%s capacity: detected; backoff %s", live.Name, capacityBackoff(0))
			return nil
		}
		if now.Sub(live.LastResume) < capacityBackoff(live.CapacityRetries) {
			return nil
		}
		max := capacityMaxRetries()
		if live.CapacityRetries >= max {
			exhausted = true
			return nil
		}
		delay := capacityBackoff(live.CapacityRetries)
		live.LastResume = now
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

func workerCapacityStatus(w *Worker) string {
	if w.CapacityRetries > 0 {
		return fmt.Sprintf("%s · capacity %d/%d", w.Status, w.CapacityRetries, capacityMaxRetries())
	}
	return w.Status
}
