package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// A worker can stop without a result while the Head is not inside orch wait: the Head has
// ended its turn, or is busy with something else. The reaper then raises an alert, which
// reaches the Head three ways: a wake-up prompt when the Head agent is idle, the Head's next
// hook call otherwise, and a Herdr notification for the user.

type headAlert struct {
	At     time.Time `json:"at"`
	Run    string    `json:"run"`
	Worker string    `json:"worker"`
	Reason string    `json:"reason"`
}

func (a headAlert) line() string {
	return fmt.Sprintf("worker %s (run %s): %s", a.Worker, a.Run, a.Reason)
}

func alertsDir() string { return filepath.Join(orchHome(), "alerts") }

func alertPath(head string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' || r < ' ' {
			return '_'
		}
		return r
	}, head)
	return filepath.Join(alertsDir(), safe+".jsonl")
}

// alertAdvice is what the Head should do next; it closes every alert message.
func alertAdvice() string {
	return "Inspect with orch status and orch read <worker>, then act: answer or close it, orch fallback, or a revised contract. Do not leave it stopped."
}

// headWake reports whether an idle Head may be woken with a prompt (ORCH_HEAD_WAKE=0 disables).
func headWake() bool { return envOrch("HEAD_WAKE") != "0" }

// raiseAlert records a stall once per worker and reason, then tells the Head and the user.
func raiseAlert(runID string, w *Worker, reason string) error {
	var head string
	fresh := false
	err := withRun(runID, func(r *Run) error {
		live := r.worker(w.Name)
		if live == nil || live.Alert == reason || resultStatus(live.Result) != "" {
			return nil
		}
		live.Alert = reason
		r.event("%s alert: %s", live.Name, reason)
		head, fresh = r.HeadPane, true
		return nil
	})
	if err != nil || !fresh {
		return err
	}
	a := headAlert{At: time.Now(), Run: runID, Worker: w.Name, Reason: reason}
	notifyUser("orch: worker "+w.Name+" stopped", reason)
	if err := deliverAlert(head, a); err != nil {
		// Undo the mark so the next poll tries again instead of staying silent.
		_ = withRun(runID, func(r *Run) error {
			if live := r.worker(w.Name); live != nil && live.Alert == reason {
				live.Alert = ""
			}
			return nil
		})
		return err
	}
	return nil
}

// wakeGap keeps several reapers of one Head from typing into it back to back.
const wakeGap = time.Minute

// deliverAlert wakes an idle Head or queues the alert, under the Head's alert lock so
// concurrent reapers serialize their wakes and a drain never races an append.
func deliverAlert(head string, a headAlert) error {
	if head == "" {
		return nil
	}
	return withAlertLock(head, func() error {
		marker := alertPath(head) + ".woke"
		recent := false
		if st, err := os.Stat(marker); err == nil && time.Since(st.ModTime()) < wakeGap {
			recent = true
		}
		if !recent && headWake() && wakeHead(head, a) {
			_ = os.WriteFile(marker, nil, 0600)
			_ = os.Chtimes(marker, time.Now(), time.Now())
			return nil
		}
		return appendAlert(head, a)
	})
}

// withAlertLock holds an exclusive flock on the Head's alert lock file.
func withAlertLock(head string, fn func() error) error {
	if err := os.MkdirAll(alertsDir(), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(alertPath(head)+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func queueAlert(head string, a headAlert) error {
	if head == "" {
		return nil
	}
	return withAlertLock(head, func() error { return appendAlert(head, a) })
}

// appendAlert must run under withAlertLock.
func appendAlert(head string, a headAlert) error {
	f, err := os.OpenFile(alertPath(head), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(a)
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// takeAlerts drains the Head's queued alerts. The stat keeps the per-tool-call hook cheap;
// the lock keeps a concurrent append from landing in a file that is being removed.
func takeAlerts(head string) []headAlert {
	if head == "" {
		return nil
	}
	p := alertPath(head)
	if _, err := os.Stat(p); err != nil {
		return nil
	}
	var out []headAlert
	_ = withAlertLock(head, func() error {
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var a headAlert
			if json.Unmarshal(sc.Bytes(), &a) == nil && a.Worker != "" {
				out = append(out, a)
			}
		}
		return os.Remove(p)
	})
	return out
}

// alertContext formats drained alerts for a hook's additionalContext.
func alertContext(alerts []headAlert) string {
	if len(alerts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[orch alert] A worker stopped without a result and needs the Head:\n")
	for _, a := range alerts {
		b.WriteString("- " + a.line() + "\n")
	}
	b.WriteString(alertAdvice())
	return b.String()
}

// wakeHead types the alert into the Head only while its agent is idle, so it never lands in
// the middle of a turn. It reports whether the prompt was sent.
var wakeHead = func(head string, a headAlert) bool {
	info, err := headAgent(head)
	if err != nil || (info.Status != "idle" && info.Status != "done") {
		return false
	}
	msg := "[orch alert] " + a.line() + ". " + alertAdvice()
	if backend() == "app" {
		_, err = hcRun("send", head, msg)
		return err == nil
	}
	if _, err := herdrRun("pane", "send-text", head, msg); err != nil {
		return false
	}
	time.Sleep(timings.KeyGap)
	_, err = herdrRun("pane", "send-keys", head, "enter")
	return err == nil
}

func headAgent(head string) (AgentInfo, error) {
	if backend() == "app" {
		t, err := hcGet(head)
		if err != nil {
			return AgentInfo{}, err
		}
		if t.Status == nil {
			return AgentInfo{}, fmt.Errorf("terminal %s has no agent", head)
		}
		return AgentInfo{Name: head, Status: t.Status.State}, nil
	}
	return agentGet(head)
}

// notifyUser shows a Herdr notification; it is best effort and never blocks recovery.
var notifyUser = func(title, body string) {
	if backend() != "herdr" {
		return
	}
	_, _ = herdrRun("notification", "show", title, "--body", body, "--sound", "request")
}

// nudgeIdle tells a worker that stopped without a result to finish or report, at most
// idleNudges times per worker and once per IdleNoResult window. The reservation runs under
// the run lock so wait and the reaper cannot both send. Neither acted nor exhausted means a
// recent nudge is still pending.
func nudgeIdle(runID string, w *Worker) (acted bool, exhausted bool, err error) {
	var target Worker
	err = withRun(runID, func(r *Run) error {
		live := r.worker(w.Name)
		if live == nil || live.Pane == "" || live.Status == "closed" || live.Status == "lost" || resultStatus(live.Result) != "" {
			return nil
		}
		// A nudge just sent by the other loop still has its idle window to take effect.
		if !live.NudgedAt.IsZero() && time.Since(live.NudgedAt) < timings.IdleNoResult {
			return nil
		}
		if live.Prompted.IsZero() || live.Kind == "agy" || live.Nudges >= idleNudges() {
			exhausted = true
			return nil
		}
		live.Nudges++
		live.NudgedAt = time.Now()
		r.event("%s idle without result: nudge %d/%d", live.Name, live.Nudges, idleNudges())
		target = *live
		acted = true
		return nil
	})
	if err != nil || !acted {
		return
	}
	err = sendWorkerText(&target, idleNudgeText(target.Name, target.Result))
	return
}
