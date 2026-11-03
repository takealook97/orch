package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// latestSessionID accepts JSON objects or JSONL events, including nested ids.
func latestSessionID(raw []byte) string {
	var latest string
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			// Walk nested values in a stable order; outer session fields take priority.
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				visit(x[k])
			}
			for _, k := range []string{"thread_id", "session_id"} {
				if id, ok := x[k].(string); ok && id != "" {
					latest = id
				}
			}
		case []any:
			for _, item := range x {
				visit(item)
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	// A log can mix plain-text errors and JSON events, so decode each line too.
	var v any
	if dec.Decode(&v) == nil {
		visit(v)
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if json.Unmarshal(line, &v) == nil {
			visit(v)
		}
	}
	return latest
}

// jsonErrorCapacity reports whether an error-type JSON event carries a capacity marker.
func jsonErrorCapacity(raw []byte) bool {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var ev map[string]any
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		_, hasErr := ev["error"]
		if typ, _ := ev["type"].(string); typ == "error" || typ == "turn.failed" || hasErr {
			if containsMarker([]string{string(line)}, capacityMarkers) {
				return true
			}
		}
	}
	return false
}

// capacityPause remains signal-aware during long backoffs.
func capacityPause(d time.Duration) bool {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-sigs:
		return false
	}
}

func runTaskAttempts(t *Task, m Model, args, env []string, budget time.Duration, start time.Time, answerPath string) (res execResult, raw []byte, err error) {
	logPath := filepath.Join(t.Dir, "output.log")
	var sessionID string
	for {
		left := budget - time.Since(start)
		if left <= 0 && t.CapacityRetries > 0 {
			res = execResult{Code: 124, Timed: true}
			return
		}
		left = max(left, 0)
		offset := int64(0)
		if t.CapacityRetries > 0 {
			if st, e := os.Stat(logPath); e == nil {
				offset = st.Size()
			}
		}
		res, err = execLoggedMode(t.Cwd, m.Kind, args, env, left, logPath, func(pgid int) {
			t.Pgid = pgid
			editTask(t, func(x *Task) { x.Pgid = pgid })
		}, t.CapacityRetries > 0)
		all, _ := os.ReadFile(logPath)
		if offset <= int64(len(all)) {
			raw = all[offset:]
		}
		if id := latestSessionID(raw); id != "" {
			sessionID = id
		}
		if err != nil || res.Timed || res.Interrupted || quotaText(string(raw)) || !capacityText(string(raw)) {
			return
		}
		// A clean exit with answer text that merely mentions capacity is a success.
		if res.Code == 0 && !jsonErrorCapacity(raw) {
			return
		}
		if m.Kind == "agy" || sessionID == "" || t.CapacityRetries >= capacityMaxRetries() {
			if res.Code == 0 {
				res.Code = 1
			}
			return
		}
		a, e := adapterFor(m.Kind)
		if e != nil {
			err = e
			return
		}
		args, err = a.resume(m, t.Kind, t.Cwd, answerPath, sessionID, capacityContinue)
		if err != nil {
			return
		}
		delay := capacityBackoff(t.CapacityRetries)
		left = budget - time.Since(start)
		if delay >= left {
			if !capacityPause(left) {
				res = execResult{Code: 130, Interrupted: true}
			} else {
				res = execResult{Code: 124, Timed: true}
			}
			return
		}
		if !capacityPause(delay) {
			res = execResult{Code: 130, Interrupted: true}
			return
		}
		t.CapacityRetries++
		editTask(t, func(x *Task) { x.CapacityRetries = t.CapacityRetries; x.Pgid = 0 })
		_ = withRun(t.Run, func(r *Run) error {
			r.event("%s capacity: resume %d/%d after %s", t.ID, t.CapacityRetries, capacityMaxRetries(), delay)
			return nil
		})
		// A stale last-message file must not mask the resumed attempt's outcome.
		_ = os.Remove(answerPath)
	}
}
