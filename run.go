package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Worker struct {
	CapacityRetries int       `json:"capacity_retries,omitempty"`
	LastResume      time.Time `json:"last_resume,omitempty"`
	ModelID         string    `json:"model_id,omitempty"`
	Terminal        string    `json:"terminal,omitempty"`
	Name            string    `json:"name"`
	Key             string    `json:"key"`
	Family          string    `json:"family"`
	Kind            string    `json:"kind"`
	Pane            string    `json:"pane"`
	Tab             string    `json:"tab,omitempty"`
	Role            string    `json:"role"`
	Purpose         string    `json:"purpose,omitempty"`
	Reviews         []string  `json:"reviews,omitempty"`
	Account         string    `json:"account"`
	Cwd             string    `json:"cwd"`
	Worktree        string    `json:"worktree,omitempty"`
	Contract        string    `json:"contract,omitempty"`
	Result          string    `json:"result"`
	Status          string    `json:"status"` // spawned, prompted, done, blocked, partial, failed, quota, lost, closed
	FallbackOf      string    `json:"fallback_of,omitempty"`
	Tried           []string  `json:"tried,omitempty"`
	// Baseline maps files already changed at prompt time to their content hash.
	Baseline      map[string]string `json:"baseline,omitempty"`
	BaselineRoot  string            `json:"baseline_root,omitempty"`
	ContractHash  string            `json:"contract_hash,omitempty"`
	TriedAccounts []string          `json:"tried_accounts,omitempty"`
	Started       time.Time         `json:"started"`
	Prompted      time.Time         `json:"prompted,omitempty"`
	Closed        time.Time         `json:"closed,omitempty"`
	// Reaped marks a worker the reaper closed that wait or collect has not reported yet.
	Reaped bool `json:"reaped,omitempty"`
}

type Event struct {
	At   time.Time `json:"at"`
	What string    `json:"what"`
}

type Run struct {
	Backend       string            `json:"backend,omitempty"`
	ID            string            `json:"id"`
	Created       time.Time         `json:"created"`
	HeadPane      string            `json:"head_pane"`
	Workspace     string            `json:"workspace"`
	Tab           string            `json:"tab"`
	Mode          string            `json:"mode"`
	Playbook      string            `json:"playbook,omitempty"`
	Goal          string            `json:"goal,omitempty"`
	Status        string            `json:"status"` // active, closed
	Adhoc         bool              `json:"adhoc,omitempty"`
	OverflowTabs  []string          `json:"overflow_tabs,omitempty"`
	OverflowSeq   int               `json:"overflow_seq,omitempty"`
	OverflowNames map[string]string `json:"overflow_names,omitempty"`
	Workers       []*Worker         `json:"workers"`
	Tasks         []*Task           `json:"tasks,omitempty"`
	Events        []Event           `json:"events"`
}

// envOrch reads ORCH_<key>.
func envOrch(key string) string { return os.Getenv("ORCH_" + key) }

func orchHome() string {
	if v := envOrch("HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".orch")
}

func runsDir() string         { return filepath.Join(orchHome(), "runs") }
func runDir(id string) string { return filepath.Join(runsDir(), id) }

func (r *Run) dir() string                  { return runDir(r.ID) }
func (r *Run) contractPath(n string) string { return filepath.Join(r.dir(), "contracts", n+".md") }
func (r *Run) resultPath(n string) string   { return filepath.Join(r.dir(), "results", n+".md") }
func (r *Run) evidenceDir(n string) string  { return filepath.Join(r.dir(), "evidence", n) }

func (r *Run) event(format string, args ...any) {
	r.Events = append(r.Events, Event{At: time.Now(), What: fmt.Sprintf(format, args...)})
}

func (r *Run) worker(name string) *Worker {
	for _, w := range r.Workers {
		if w.Name == name {
			return w
		}
	}
	return nil
}

func (r *Run) liveWorkers() []*Worker {
	var out []*Worker
	for _, w := range r.Workers {
		if w.Pane != "" && w.Status != "closed" && w.Status != "lost" {
			out = append(out, w)
		}
	}
	return out
}

func newRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func loadRun(id string) (*Run, error) {
	data, err := os.ReadFile(filepath.Join(runDir(id), "run.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("run %s not found", id)
		}
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("run.json for run %s is corrupt: %w", id, err)
	}
	return &r, nil
}

func saveRun(r *Run) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.dir(), ".run-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(r.dir(), "run.json"))
}

// withRun loads a run under an exclusive lock, applies fn and saves it.
func withRun(id string, fn func(*Run) error) error {
	lock, err := os.OpenFile(filepath.Join(runDir(id), ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("cannot lock run %s: %w", id, err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	r, err := loadRun(id)
	if err != nil {
		return err
	}
	// A failed callback leaves run.json untouched (no partial ownership changes).
	if err := fn(r); err != nil {
		return err
	}
	return saveRun(r)
}

func allRuns() ([]*Run, error) {
	entries, err := os.ReadDir(runsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := loadRun(e.Name())
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

// resolveRun picks --run, then ORCH_RUN, then the newest active run owned by this Head pane.
func resolveRun(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if v := envOrch("RUN"); v != "" {
		return v, nil
	}
	runs, err := allRuns()
	if err != nil {
		return "", err
	}
	head := headID()
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Status == "active" && runs[i].HeadPane == head {
			return runs[i].ID, nil
		}
	}
	return "", errors.New("no active run. Create one with 'orch run new' or specify --run")
}

// ownedPanes returns every live worker pane of active runs owned by head, oldest first.
func ownedPanes(head string) ([]string, error) {
	runs, err := allRuns()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range runs {
		if runBackend(r) != currentBackend() || (r.Status != "active" && runBackend(r) != "app") || r.HeadPane != head {
			continue
		}
		for _, w := range r.liveWorkers() {
			out = append(out, w.Pane)
		}
	}
	return out, nil
}

const acceptanceTemplate = `# Acceptance criteria

Criteria derived by Head from the original request. Use one sentence per numbered item.

1.
`

const decisionsTemplate = `# Decision log

Head records implicit decisions (names, interfaces, libraries, formats, scope) as numbered items.
Every contract references this file as input. Workers list needed new decisions under Blockers in the result report.

`

const findingsTemplate = `# Findings

Classify as confirmed defect / plausible risk / unsupported concern / resolved. Give primary evidence for each item.

`

func createRun(request, mode, playbook, goal string) (*Run, error) {
	return newRun(request, mode, playbook, goal, false)
}

// newRun creates a run directory. An ad-hoc run (one-off tasks) is closed from its first
// save, so no other orch call can ever resolve it as the active run.
func newRun(request, mode, playbook, goal string, adhoc bool) (*Run, error) {
	if goal == "" {
		if g, err := activeGoal(headID()); err == nil && g != nil {
			goal = g.ID
		}
	}
	r := &Run{
		ID:        newRunID(),
		Backend:   backend(),
		Created:   time.Now(),
		HeadPane:  headID(),
		Workspace: headSpace(),
		Tab:       headTab(),
		Mode:      mode,
		Playbook:  playbook,
		Goal:      goal,
		Status:    "active",
		Workers:   []*Worker{},
	}
	if backend() == "app" && !adhoc && (r.Workspace == "" || r.Tab == "") {
		return nil, fmt.Errorf("cannot resolve Head space/tab from herdr-app ls")
	}
	for _, sub := range []string{"contracts", "results", "evidence"} {
		if err := os.MkdirAll(filepath.Join(r.dir(), sub), 0700); err != nil {
			return nil, err
		}
	}
	files := map[string]string{
		"request.md":    request,
		"acceptance.md": acceptanceTemplate,
		"decisions.md":  decisionsTemplate,
		"findings.md":   findingsTemplate,
		"knowledge.md":  knowledgeTemplate,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(r.dir(), name), []byte(body), 0600); err != nil {
			return nil, err
		}
	}
	r.event("run created mode=%s playbook=%s goal=%s", mode, playbook, goal)
	if adhoc {
		r.Status, r.Adhoc = "closed", true
		r.event("ad-hoc task run (closed; owns no panes)")
	}
	return r, saveRun(r)
}

const knowledgeTemplate = "# Knowledge\n<!-- Verified reusable facts, one bullet each with a source path; merged by orch, curated by the Head. -->\n"

// mergeKnowledge serializes read/dedupe/write with the run state and event log.
func mergeKnowledge(runID, worker, result string) error {
	if st := resultStatus(result); st != "done" && st != "partial" {
		return nil
	}
	return withRun(runID, func(r *Run) error {
		data, err := os.ReadFile(result)
		if err != nil {
			return err
		}
		path := filepath.Join(r.dir(), "knowledge.md")
		old, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(old) == 0 {
			old = []byte(knowledgeTemplate)
		}
		factKey := func(line string) string {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if i := strings.Index(line, " — "); i >= 0 {
				return line[:i]
			}
			if i := strings.LastIndex(line, " (from "); i >= 0 {
				line = line[:i]
			}
			return line
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(old), "\n") {
			if strings.HasPrefix(line, "- ") {
				seen[factKey(line)] = true
			}
		}
		body := string(old)
		active, count, bullets := false, 0, 0
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "## ") {
				active = strings.TrimSpace(line) == "## Reusable facts"
				continue
			}
			if !active || !strings.HasPrefix(line, "- ") || bullets >= 8 {
				continue
			}
			bullets++
			key := factKey(line)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			if !strings.HasSuffix(body, "\n") {
				body += "\n"
			}
			body += strings.TrimSpace(line) + " (from " + worker + ")\n"
			count++
		}
		if count == 0 {
			return nil
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			return err
		}
		r.event("knowledge +%d from %s", count, worker)
		return nil
	})
}
