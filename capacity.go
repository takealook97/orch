package main

import (
	"fmt"
	"time"
)

// incident is a recoverable stop that orch resumes by itself: the agent went idle with an
// error on screen and a short continuation prompt starts it again.
type incident struct {
	name    string // event label and the worker status once retries are exhausted
	match   func(string) bool
	retries func() int
	delay   func(int) time.Duration
	text    func(name string, n int) string
	state   func(*Worker) (*int, *time.Time)
}

var capacityIncident = incident{
	name: "capacity", match: capacityText, retries: capacityMaxRetries, delay: capacityBackoff,
	text:  func(name string, _ int) string { return capacityResumeText(name) },
	state: func(w *Worker) (*int, *time.Time) { return &w.CapacityRetries, &w.CapacitySeen },
}

// blockIncident covers Codex's cyber_policy stop ("This content can't be shown").
var blockIncident = incident{
	name: "content-blocked", match: blockText, retries: blockMaxRetries, delay: blockBackoff,
	text:  blockResumeText,
	state: func(w *Worker) (*int, *time.Time) { return &w.BlockRetries, &w.BlockSeen },
}

var incidents = []incident{blockIncident, capacityIncident}

// screenIncident returns the incident shown in the current screen tail, if any.
func screenIncident(screen string) (incident, bool) {
	if quotaText(screen) {
		return incident{}, false
	}
	for _, inc := range incidents {
		if inc.match(screen) {
			return inc, true
		}
	}
	return incident{}, false
}

// resumeOnCapacity reserves each retry under the run lock, so wait and the reaper
// cannot send twice. CapacitySeen is when the current incident was first sighted;
// the backoff is measured from it, and a send clears it so the next sighting waits anew.
// Callers must check that no final result exists and the agent is stopped.
func resumeOnCapacity(runID string, w *Worker, screen string) (acted bool, exhausted bool, err error) {
	if quotaText(screen) || !capacityText(screen) {
		return
	}
	return resumeOn(capacityIncident, runID, w)
}

// resumeOnIncident handles whichever incident the screen shows.
func resumeOnIncident(runID string, w *Worker, screen string) (inc incident, acted bool, exhausted bool, err error) {
	inc, ok := screenIncident(screen)
	if !ok {
		return
	}
	acted, exhausted, err = resumeOn(inc, runID, w)
	return
}

func resumeOn(inc incident, runID string, w *Worker) (acted bool, exhausted bool, err error) {
	var target Worker
	var text string
	err = withRun(runID, func(r *Run) error {
		live := r.worker(w.Name)
		if live == nil {
			return fmt.Errorf("worker %s missing", w.Name)
		}
		if live.Pane == "" || live.Status == "closed" || live.Status == "lost" || resultStatus(live.Result) != "" {
			return nil
		}
		retries, seen := inc.state(live)
		max := inc.retries()
		// agy has no supported session continuation; zero retries allow no resume at all.
		if live.Kind == "agy" || max == 0 {
			exhausted = true
			return nil
		}
		now := time.Now()
		if seen.IsZero() {
			*seen = now
			r.event("%s %s: detected; backoff %s", live.Name, inc.name, inc.delay(*retries))
			return nil
		}
		delay := inc.delay(*retries)
		if now.Sub(*seen) < delay {
			return nil
		}
		if *retries >= max {
			exhausted = true
			return nil
		}
		*seen = time.Time{}
		text = inc.text(live.Name, *retries)
		*retries++
		r.event("%s %s: resume %d/%d after %s", live.Name, inc.name, *retries, max, delay)
		target = *live
		acted = true
		return nil
	})
	if err != nil || !acted {
		return
	}
	err = sendWorkerText(&target, text)
	return
}

// capacityRecovered ends every current incident: the agent is working again.
// The retry caps count consecutive failed resumes.
func capacityRecovered(runID, name string) error {
	return clearIncidents(runID, name, "", true)
}

// clearIncidents resets the incidents the screen no longer shows; a working agent ends all
// of them and the Head alert too.
func clearIncidents(runID, name, screen string, working bool) error {
	return withRun(runID, func(r *Run) error {
		live := r.worker(name)
		if live == nil {
			return nil
		}
		for _, inc := range incidents {
			if !working && inc.match(screen) {
				continue
			}
			retries, seen := inc.state(live)
			*retries, *seen = 0, time.Time{}
		}
		if working {
			live.Alert = ""
		}
		return nil
	})
}

// noteCapacityRecovered resets a worker's incident state only when it has any,
// so polling loops do not take the run lock for healthy workers.
func noteCapacityRecovered(runID string, w *Worker) {
	noteRecovered(runID, w, "", true)
}

// noteRecovered resets what the agent left behind: everything when it works again,
// otherwise the incidents its screen no longer shows.
func noteRecovered(runID string, w *Worker, screen string, working bool) {
	dirty := working && w.Alert != ""
	for _, inc := range incidents {
		if !working && inc.match(screen) {
			continue
		}
		retries, seen := inc.state(w)
		dirty = dirty || *retries != 0 || !seen.IsZero()
	}
	if dirty {
		_ = clearIncidents(runID, w.Name, screen, working)
	}
}

func workerCapacityStatus(w *Worker) string {
	s := w.Status
	if w.CapacityRetries > 0 {
		s += fmt.Sprintf(" · capacity %d/%d", w.CapacityRetries, capacityMaxRetries())
	}
	if w.BlockRetries > 0 {
		s += fmt.Sprintf(" · content-blocked %d/%d", w.BlockRetries, blockMaxRetries())
	}
	if w.Alert != "" {
		s += " · alert: " + w.Alert
	}
	return s
}
