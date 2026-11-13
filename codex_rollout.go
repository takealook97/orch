package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
)

// Herdr can keep reporting "working" after a Codex turn ended in an error, so the screen
// is never read. The Codex rollout (one JSON event per line) records how each turn ended.

const (
	rolloutTailBytes  = 512 << 10 // only the end of the rollout decides the current turn
	rolloutHeadBytes  = 1 << 20   // the first prompt follows the instructions preamble (~150KB seen)
	rolloutRescanGap  = 15 * time.Second
	rolloutStartSlack = time.Minute
)

// codexHomes lists CODEX_HOME candidates: ~/.codex and the cr accounts; swappable in tests.
var codexHomes = func() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	cands := []string{filepath.Join(home, ".codex")}
	if m, err := filepath.Glob(filepath.Join(home, ".local", "share", "cr", "accounts", "codex", "*")); err == nil {
		cands = append(cands, m...)
	}
	var out []string
	for _, d := range cands {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// rolloutScan remembers when each worker's rollout was last searched for.
var rolloutScan = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// rolloutDays are the session day directories a worker's rollout can sit in.
func rolloutDays(started, now time.Time) []string {
	var days []string
	add := func(t time.Time) {
		if d := t.Format("2006/01/02"); !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	if !started.IsZero() {
		add(started.Local())
		add(started.UTC())
	}
	add(now.Local())
	add(now.UTC())
	return days
}

// findCodexRollout returns the newest rollout written since the worker started whose head
// holds the worker's first prompt: its marker and its contract path.
func findCodexRollout(w *Worker) string {
	if w.Contract == "" {
		return ""
	}
	type cand struct {
		path string
		mod  time.Time
	}
	since := w.Started.Add(-rolloutStartSlack)
	var cands []cand
	seen := map[string]bool{}
	for _, home := range codexHomes() {
		for _, day := range rolloutDays(w.Started, time.Now()) {
			matches, _ := filepath.Glob(filepath.Join(home, "sessions", day, "rollout-*.jsonl"))
			for _, p := range matches {
				if seen[p] {
					continue
				}
				seen[p] = true
				fi, err := os.Stat(p)
				if err != nil || (!w.Started.IsZero() && fi.ModTime().Before(since)) {
					continue
				}
				cands = append(cands, cand{p, fi.ModTime()})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	needles := [][]byte{[]byte(promptMarker(w.Name)), []byte(w.Contract)}
	for _, c := range cands {
		if fileHeadContains(c.path, needles) {
			return c.path
		}
	}
	return ""
}

func fileHeadContains(path string, needles [][]byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, rolloutHeadBytes))
	if err != nil {
		return false
	}
	for _, n := range needles {
		if !bytes.Contains(head, n) {
			return false
		}
	}
	return true
}

// codexRolloutPath returns the worker's rollout, searching at most once per rescan gap and
// storing a find on the worker so later polls skip the search.
func codexRolloutPath(runID string, w *Worker) string {
	if w.Kind != "codex" {
		return ""
	}
	if w.Rollout != "" {
		if _, err := os.Stat(w.Rollout); err == nil {
			return w.Rollout
		}
	}
	key := runID + "/" + w.Name
	rolloutScan.Lock()
	if t, ok := rolloutScan.last[key]; ok && time.Since(t) < rolloutRescanGap {
		rolloutScan.Unlock()
		return ""
	}
	rolloutScan.last[key] = time.Now()
	rolloutScan.Unlock()
	p := findCodexRollout(w)
	if p == "" {
		return ""
	}
	_ = withRun(runID, func(r *Run) error {
		if live := r.worker(w.Name); live != nil {
			live.Rollout = p
		}
		return nil
	})
	w.Rollout = p
	return p
}

type rolloutEvent struct {
	Type    string `json:"type"`
	Payload struct {
		Type  string `json:"type"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"payload"`
}

// rolloutIncident reports the incident the rollout's last turn ended in: a task_complete
// with an error matching the block or capacity markers. Quota and other errors are left
// to the existing paths.
func rolloutIncident(path string) (incident, bool) {
	tail, ok := rolloutTail(path)
	if !ok {
		return incident{}, false
	}
	lines := bytes.Split(tail, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var ev rolloutEvent
		if json.Unmarshal(lines[i], &ev) != nil || ev.Type != "event_msg" {
			continue
		}
		switch ev.Payload.Type {
		case "task_started", "turn_aborted":
			return incident{}, false
		case "task_complete":
			if ev.Payload.Error == nil {
				return incident{}, false
			}
			return errorIncident(ev.Payload.Error.Message)
		}
	}
	return incident{}, false
}

func errorIncident(msg string) (incident, bool) {
	line := []string{msg}
	switch {
	case containsMarker(line, quotaMarkers):
	case containsMarker(line, blockMarkers):
		return blockIncident, true
	case containsMarker(line, capacityMarkers):
		return capacityIncident, true
	}
	return incident{}, false
}

// rolloutTail returns the complete lines in the last rolloutTailBytes of the file.
func rolloutTail(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false
	}
	off := max(fi.Size()-rolloutTailBytes, 0)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, false
	}
	buf, err := io.ReadAll(bufio.NewReader(io.LimitReader(f, rolloutTailBytes)))
	if err != nil {
		return nil, false
	}
	if off > 0 { // the first line was cut by the offset
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return nil, false
		}
		buf = buf[i+1:]
	}
	i := bytes.LastIndexByte(buf, '\n')
	if i < 0 { // a line still being written
		return nil, false
	}
	return buf[:i], true
}

// workerRolloutIncident is the incident a Codex worker's rollout shows, if it can be read.
func workerRolloutIncident(runID string, w *Worker) (incident, bool) {
	p := codexRolloutPath(runID, w)
	if p == "" {
		return incident{}, false
	}
	return rolloutIncident(p)
}
