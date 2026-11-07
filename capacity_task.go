package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
func jsonErrorCapacity(raw []byte) bool { return jsonErrorMarker(raw, capacityMarkers) }

// taskIncident is a recoverable task stop: the capacity or the cyber_policy marker, with
// the task's retry counter for it.
type taskIncident struct {
	name    string
	markers []string
	retries func() int
	delay   func(int) time.Duration
	text    func(int) string
	count   func(*Task) *int
}

var taskIncidents = []taskIncident{
	{blockIncident.name, blockMarkers, blockMaxRetries, blockBackoff,
		func(n int) string { return blockContinues[n%len(blockContinues)] },
		func(t *Task) *int { return &t.BlockRetries }},
	{capacityIncident.name, capacityMarkers, capacityMaxRetries, capacityBackoff,
		func(int) string { return capacityContinue },
		func(t *Task) *int { return &t.CapacityRetries }},
}

// taskStop classifies a stopped attempt. Structured error events decide first, so answer
// text that mentions one marker cannot hide another incident's real error; after a clean
// exit only an error event counts, otherwise the output tail does too.
func taskStop(raw []byte, clean bool) (taskIncident, bool) {
	s := string(raw)
	if quotaText(s) {
		return taskIncident{}, false
	}
	for _, inc := range taskIncidents {
		if jsonErrorMarker(raw, inc.markers) {
			return inc, true
		}
	}
	if clean {
		return taskIncident{}, false
	}
	for _, inc := range taskIncidents {
		if containsMarker(tailLines(s), inc.markers) {
			return inc, true
		}
	}
	return taskIncident{}, false
}

func jsonErrorMarker(raw []byte, markers []string) bool {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var ev map[string]any
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if typ, _ := ev["type"].(string); typ == "error" || typ == "turn.failed" || hasErrorField(ev) {
			if containsMarker([]string{string(line)}, markers) {
				return true
			}
		}
	}
	return false
}

// hasErrorField finds a non-null "error" at any depth; Codex nests the turn error in payload.
func hasErrorField(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "error" && val != nil {
				return true
			}
			if hasErrorField(val) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if hasErrorField(item) {
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
		resumed := t.CapacityRetries > 0 || t.BlockRetries > 0
		left := budget - time.Since(start)
		if left <= 0 && resumed {
			res = execResult{Code: 124, Timed: true}
			return
		}
		left = max(left, 0)
		offset := int64(0)
		if resumed {
			if st, e := os.Stat(logPath); e == nil {
				offset = st.Size()
			}
		}
		var publishErr error
		res, err = execLoggedMode(t.Cwd, m.Kind, args, env, left, logPath, func(pgid int) {
			t.Pgid = pgid
			publishErr = editTask(t, func(x *Task) { x.Pgid = pgid })
			if publishErr != nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}, resumed)
		if publishErr != nil {
			err = errors.Join(err, publishErr)
			return
		}
		all, _ := os.ReadFile(logPath)
		if offset <= int64(len(all)) {
			raw = all[offset:]
		}
		if id := latestSessionID(raw); id != "" {
			sessionID = id
		}
		if err != nil || res.Timed || res.Interrupted {
			return
		}
		// A clean exit with answer text that merely mentions a marker is a success.
		inc, stopped := taskStop(raw, res.Code == 0)
		if !stopped {
			return
		}
		count := inc.count(t)
		if m.Kind == "agy" || sessionID == "" || *count >= inc.retries() {
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
		args, err = a.resume(m, t.Kind, t.Cwd, answerPath, sessionID, inc.text(*count))
		if err != nil {
			return
		}
		delay := inc.delay(*count)
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
		*count++
		n := *count
		if err = editTask(t, func(x *Task) {
			x.CapacityRetries, x.BlockRetries, x.Pgid = t.CapacityRetries, t.BlockRetries, 0
		}); err != nil {
			return
		}
		err = withRun(t.Run, func(r *Run) error {
			r.event("%s %s: resume %d/%d after %s", t.ID, inc.name, n, inc.retries(), delay)
			return nil
		})
		if err != nil {
			return
		}
		// A stale last-message file must not mask the resumed attempt's outcome.
		_ = os.Remove(answerPath)
	}
}
