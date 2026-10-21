package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type waitOutcome struct {
	Name   string
	Status string // done, blocked, partial, agent-blocked, quota, idle-no-result, timeout, fallback:<new>, failed
	Detail string
}

// waitWorkers polls result files first; agent state is only a secondary signal.
func waitWorkers(runFlag string, names []string, timeout time.Duration, fallback bool) ([]waitOutcome, error) {
	runID, err := resolveRun(runFlag)
	if err != nil {
		return nil, err
	}
	r, err := loadRun(runID)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		for _, w := range r.liveWorkers() {
			if w.Status == "prompted" {
				names = append(names, w.Name)
			}
		}
		// Workers the reaper already closed still owe the Head their outcome.
		for _, w := range r.Workers {
			if w.Reaped {
				names = append(names, w.Name)
			}
		}
		for _, task := range r.Tasks {
			if task.Status == "running" {
				names = append(names, task.ID)
			}
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no workers to wait for")
	}
	pending := slices.Clone(names)
	idleSince := map[string]time.Time{}
	var outcomes []waitOutcome
	deadline := time.Now().Add(timeout)
	lastRead := time.Time{}
	head := os.Getenv("HERDR_PANE_ID")
	reported := map[string]bool{}
	for len(pending) > 0 {
		r, err = loadRun(runID)
		if err != nil {
			return outcomes, err
		}
		readNow := time.Since(lastRead) > timings.ScreenRead
		if readNow && len(r.liveWorkers()) > 0 {
			lastRead = time.Now()
			// A pane nobody recorded may come from a worker bypassing the guards. Report it;
			// never close it (only panes this Head spawned are ever closed).
			for _, id := range unknownPanes(head) {
				if !reported[id] {
					reported[id] = true
					fmt.Fprintf(os.Stderr, "orch: warning: unrecorded pane %s appeared in this tab. check whether a worker bypassed controls (left open)\n", id)
				}
			}
		}
		var still []string
		for _, name := range pending {
			if strings.HasPrefix(name, "task-") {
				t, e := readTask(runID, name)
				if e != nil {
					outcomes = append(outcomes, waitOutcome{name, "failed", e.Error()})
					continue
				}
				if t.Status == "running" {
					if why := taskDead(t); why != "" {
						t.Status, t.Note = "failed", why
						_ = saveTask(t)
					}
				}
				if t.Status != "running" {
					outcomes = append(outcomes, waitOutcome{name, t.Status, taskReport(t, false)})
					continue
				}
				if time.Now().After(deadline) {
					outcomes = append(outcomes, waitOutcome{name, "timeout", "wait deadline reached"})
					continue
				}
				still = append(still, name)
				continue
			}
			w := r.worker(name)
			if w == nil {
				outcomes = append(outcomes, waitOutcome{name, "failed", "worker not in run"})
				continue
			}
			st := resultStatus(w.Result)
			if st != "" && w.Pane != "" && !agentStopped(w) {
				// A status line alone is not a finish while the agent still works on the file.
				if time.Now().After(deadline) {
					outcomes = append(outcomes, waitOutcome{name, "timeout", "result has a status line but the agent is still working"})
					continue
				}
				still = append(still, name)
				continue
			}
			if st != "" {
				setStatus(runID, name, st)
				// A finished worker's pane is always closed at once (no reuse).
				detail := w.Result
				if w.Pane == "" && w.Reaped {
					detail += " · pane closed by the reaper"
				}
				if w.Pane != "" {
					if err := closeWorker(runID, name); err != nil {
						// A pane that could not be removed is not a clean finish.
						outcomes = append(outcomes, waitOutcome{name, "close-failed", detail + " · result is " + st + ", pane close failed: " + err.Error()})
						continue
					} else {
						detail += " · pane closure confirmed"
						_ = normalizeOwnedLayouts(os.Getenv("HERDR_PANE_ID"))
					}
				}
				markReported(runID, name)
				outcomes = append(outcomes, waitOutcome{name, st, detail})
				continue
			}
			info, _ := agentGet(name)
			if info.Status == "blocked" {
				outcomes = append(outcomes, waitOutcome{name, "agent-blocked", "orch read " + name + " to inspect, then decide"})
				continue
			}
			if readNow {
				if text, err := agentRead(name, 80); err == nil && quotaText(text) {
					saveTail(r, w, text)
					if fallback {
						nw, ferr := doFallback(runID, name, "quota exhausted")
						if ferr == nil {
							outcomes = append(outcomes, waitOutcome{name, "fallback:" + nw, "quota exhausted → " + nw})
							still = append(still, nw)
							continue
						}
						outcomes = append(outcomes, waitOutcome{name, "quota", ferr.Error()})
						continue
					}
					setStatus(runID, name, "quota")
					outcomes = append(outcomes, waitOutcome{name, "quota", "quota exhausted"})
					continue
				}
			}
			if info.Status == "idle" || info.Status == "done" {
				if idleSince[name].IsZero() {
					idleSince[name] = time.Now()
				} else if time.Since(idleSince[name]) > timings.IdleNoResult {
					outcomes = append(outcomes, waitOutcome{name, "idle-no-result", "agent stopped without a result file. orch collect to inspect output"})
					continue
				}
			} else {
				delete(idleSince, name)
			}
			still = append(still, name)
		}
		pending = still
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			for _, n := range pending {
				outcomes = append(outcomes, waitOutcome{n, "timeout", "orch read to check progress, then extend or fall back"})
			}
			break
		}
		time.Sleep(timings.WaitPoll)
	}
	return outcomes, nil
}

// unknownPanes lists panes in the Head's tab that no active run of this Head recorded.
func unknownPanes(head string) []string {
	l, err := getLayout(head)
	if err != nil {
		return nil
	}
	owned, err := ownedPanes(head)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range l.Panes {
		if p.ID != head && !slices.Contains(owned, p.ID) {
			out = append(out, p.ID)
		}
	}
	return out
}

// markReported clears the reaper's mark once wait or collect has reported the worker.
func markReported(runID, name string) {
	_ = withRun(runID, func(r *Run) error {
		if w := r.worker(name); w != nil {
			w.Reaped = false
		}
		return nil
	})
}

func setStatus(runID, name, st string) {
	_ = withRun(runID, func(r *Run) error {
		if w := r.worker(name); w != nil && w.Status != st {
			w.Status = st
			r.event("status %s=%s", name, st)
		}
		return nil
	})
}

func saveTail(r *Run, w *Worker, text string) string {
	dir := r.evidenceDir(w.Name)
	_ = os.MkdirAll(dir, 0700)
	p := filepath.Join(dir, "transcript-tail.txt")
	_ = os.WriteFile(p, []byte(text), 0600)
	return p
}

// doFallback replaces a failed worker: same key on another account when an account hook exists,
// otherwise the next key of the purpose chain that still satisfies the lane constraints.
func doFallback(runID, name, reason string) (string, error) {
	r, err := loadRun(runID)
	if err != nil {
		return "", err
	}
	w := r.worker(name)
	if w == nil {
		return "", fmt.Errorf("worker %q not found", name)
	}
	if w.Contract == "" {
		return "", errors.New("cannot fall back a worker without a contract")
	}
	// Bound the whole replacement lineage so accounts or keys can never cycle.
	if len(w.Tried)+len(w.TriedAccounts) >= maxFallbacks {
		setStatus(runID, name, "blocked")
		return "", fmt.Errorf("%s: reached the fallback limit (%d). report to the user", name, maxFallbacks)
	}
	authors, _ := familiesOf(r, w.Reviews)
	critics := criticsOf(r, w.Reviews)
	key, account := "", fallbackAccount(w)
	var exclude []string
	if envOrch("ACCOUNT_CMD") != "" && w.Account != "" && w.Account != "cr" {
		// Exclude every account this key has already used in the lineage.
		exclude = append(slices.Clone(w.TriedAccounts), w.Account)
		if _, err := hookAccount(w.Kind, exclude); err == nil {
			key, account = w.Key, "hook"
		}
	}
	if key != w.Key {
		exclude = nil // a new key starts with a fresh account history
	}
	if key == "" {
		purpose := w.Purpose
		if purpose == "" {
			return "", fmt.Errorf("%s: purpose missing; cannot choose fallback chain", name)
		}
		m, ok := nextFallback(purpose, w.Key, w.Role, authors, critics, w.Tried)
		if !ok {
			setStatus(runID, name, "blocked")
			return "", fmt.Errorf("%s: all models in fallback chain (%s) are unavailable", name, purpose)
		}
		key = m.Key
	}
	if account == "cr" {
		next, err := modelByKey(key)
		if err != nil {
			return "", err
		}
		if next.Kind != "codex" && next.Kind != "claude" {
			account = "same"
		}
	}
	// Record why it was replaced, then free the row so the replacement fits under the row cap.
	failStatus := "failed"
	if strings.Contains(reason, "quota") {
		failStatus = "quota"
	}
	setStatus(runID, name, failStatus)
	if err := closeWorker(runID, name); err != nil {
		return "", fmt.Errorf("%s: fallback stopped because the old pane could not be closed: %w", name, err)
	}
	newName := fallbackName(r, name)
	nw, err := spawn(spawnOpts{
		Run: runID, Key: key, Name: newName, Role: w.Role, Purpose: w.Purpose, Reviews: w.Reviews,
		AuthorFamilies: nil, Cwd: w.Cwd, Worktree: w.Worktree, Account: account, Exclude: exclude,
		Tried: w.Tried, TriedAccounts: exclude, Fallback: name,
	})
	if err != nil {
		return "", err
	}
	// Carry the partial result and transcript into the new contract.
	body, err := os.ReadFile(w.Contract)
	if err != nil {
		return "", err
	}
	note := fmt.Sprintf("\n\n## Fallback handoff\n- previous worker: %s (%s) · reason: %s\n- previous partial result: %s\n- tail of previous output: %s\n- Continue from the material above instead of restarting. In the result report fallback field, write '%s → %s, %s'.\n",
		name, w.Key, reason, w.Result, filepath.Join(r.evidenceDir(name), "transcript-tail.txt"), w.Key, key, reason)
	body = []byte(fallbackContract(r, string(body), name, newName, note))
	if err := os.WriteFile(r.contractPath(newName), body, 0600); err != nil {
		return "", err
	}
	_ = withRun(runID, func(r *Run) error {
		r.event("fallback %s(%s) → %s(%s): %s", name, w.Key, newName, key, reason)
		return nil
	})
	if err := prompt(runID, nw.Name, "", false, 0); err != nil {
		return nw.Name, err
	}
	return nw.Name, nil
}

// fallbackContract renames the contract to the replacement, then appends the handoff that
// points at the old worker's files (appended last so those references are not rewritten).
func fallbackContract(r *Run, body, name, newName, note string) string {
	text := strings.Replace(body, "# Contract: "+name, "# Contract: "+newName, 1)
	text = strings.ReplaceAll(text, "# Result: "+name+"\n", "# Result: "+newName+"\n")
	text = strings.ReplaceAll(text, r.resultPath(name), r.resultPath(newName))
	return text + note
}

func fallbackName(r *Run, name string) string {
	base := name
	if i := strings.LastIndex(name, "-f"); i > 0 {
		base = name[:i]
	}
	for i := 1; ; i++ {
		suffix := fmt.Sprintf("-f%d", i)
		b := base
		if len(b)+len(suffix) > 32 {
			b = strings.TrimRight(b[:32-len(suffix)], "-_") // keep the leading letter
		}
		if n := b + suffix; r.worker(n) == nil {
			return n
		}
	}
}

// collect reports a worker's result and enforces the contract's write scope.
func collect(runFlag, name string) (string, error) {
	out, _, err := collectWorker(runFlag, name)
	return out, err
}

// collectWorker is collect that also returns the worker's final status ("" without one).
func collectWorker(runFlag, name string) (string, string, error) {
	runID, err := resolveRun(runFlag)
	if err != nil {
		return "", "", err
	}
	r, err := loadRun(runID)
	if err != nil {
		return "", "", err
	}
	w := r.worker(name)
	if w == nil {
		return "", "", fmt.Errorf("worker %q not found", name)
	}
	var b strings.Builder
	st := resultStatus(w.Result)
	if st == "" {
		fmt.Fprintf(&b, "result file or status line missing: %s\n", w.Result)
		if w.Pane != "" && w.Status != "closed" {
			if text, err := agentRead(name, 120); err == nil {
				p := saveTail(r, w, text)
				fmt.Fprintf(&b, "terminal output backup: %s\n", p)
			}
		}
		return b.String(), "", nil
	}
	fmt.Fprintf(&b, "status: %s\nresult: %s\n", st, w.Result)
	if data, err := os.ReadFile(w.Result); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "- summary:") || strings.HasPrefix(line, "summary:") {
				if rs := []rune(line); len(rs) > 300 {
					line = string(rs[:300]) + "…"
				}
				fmt.Fprintln(&b, strings.TrimPrefix(line, "- "))
				break
			}
		}
	}
	var closeErr error
	if w.Pane == "" && w.Reaped {
		fmt.Fprintf(&b, "pane closed by the reaper\n")
	}
	if w.Pane != "" && !agentStopped(w) {
		fmt.Fprintf(&b, "agent still working; pane left open\n")
	} else if w.Pane != "" {
		if closeErr = closeWorker(runID, name); closeErr != nil {
			fmt.Fprintf(&b, "pane close failed: %v\n", closeErr)
		} else {
			fmt.Fprintf(&b, "pane closure confirmed\n")
			_ = normalizeOwnedLayouts(r.HeadPane)
		}
	}
	markReported(runID, name)
	if w.Contract != "" && w.Cwd != "" {
		body, err := os.ReadFile(w.Contract)
		// Preserve non-git tasks that declare no commit baseline.
		checkScope := err != nil || isGitDir(w.Cwd) || explicitBase(contractField(string(body), "base commit"))
		var root string
		var changed []string
		if err == nil && checkScope {
			root, err = scopeRepository(w.Cwd, string(body))
		}
		if err == nil && checkScope {
			changed, err = changedFiles(root, contractField(string(body), "base commit"))
		}
		if err != nil {
			// Fail closed: an unverifiable scope is not a pass.
			st = "failed"
			fmt.Fprintf(&b, "scope could not be checked; marked failed: %v\n", err)
		} else if checkScope {
			// A file counts when it changed after the prompt (content differs from the baseline).
			if root != repoRoot(w.Cwd) {
				fmt.Fprintf(&b, "scope checked in repository: %s\n", root)
			}
			baseline := w.Baseline
			if (w.BaselineRoot != "" && w.BaselineRoot != root) || (w.BaselineRoot == "" && root != repoRoot(w.Cwd)) {
				baseline = nil
			}
			fresh := freshChanges(root, changed, baseline)
			if bad := repositoryScopeViolations(w.Cwd, root, fresh, ownedGlobs(string(body))); len(bad) > 0 {
				st = "failed"
				fmt.Fprintf(&b, "scope violation; marked failed: change outside owned files %s\n", strings.Join(bad, ", "))
			} else if len(fresh) > 0 {
				fmt.Fprintf(&b, "changed files (in scope): %s\n", strings.Join(fresh, ", "))
			}
		}
	}
	setStatus(runID, name, st)
	if closeErr != nil {
		return b.String(), st, fmt.Errorf("worker %s pane could not be closed: %w", name, closeErr)
	}
	return b.String(), st, nil
}

func closeWorker(runID, name string) error { return closeWorkerAs(runID, name, "", false) }

// closeWorkerAs closes a worker's pane; reap marks the close as the reaper's so wait and
// collect still report the worker.
// closeWorkerAs closes name's pane. A non-empty expect is the pane the caller observed: if the
// name now points at another pane (the worker was closed and the name reused), nothing is closed.
func closeWorkerAs(runID, name, expect string, reap bool) error {
	unlock, err := lockHead(os.Getenv("HERDR_PANE_ID"))
	if err != nil {
		return err
	}
	defer unlock()
	var pane string
	err = withRun(runID, func(r *Run) error {
		if err := ownRun(r); err != nil {
			return err
		}
		w := r.worker(name)
		if w == nil {
			return fmt.Errorf("worker %q not found", name)
		}
		pane = w.Pane
		return nil
	})
	if err != nil {
		return err
	}
	if expect != "" && pane != expect {
		return nil // the name was reused for a new worker; never close its pane
	}
	if pane == "" {
		cleanRunOverflowWarning(runID)
		return nil
	}
	var cerr error
	if pane != "" {
		cerr = closePane(pane) // closes if live and confirms removal; lookup errors are failures
	}
	_ = withRun(runID, func(r *Run) error {
		w := r.worker(name)
		if cerr == nil && w.Pane != pane {
			return nil // another orch process recorded this close first, or the name was reused
		}
		if cerr == nil {
			w.Closed = time.Now()
			// Keep a final outcome (done/partial/blocked/failed/quota); otherwise mark closed.
			if st := resultStatus(w.Result); st != "" {
				w.Status = st
			} else if w.Status != "failed" && w.Status != "quota" && w.Status != "blocked" {
				w.Status = "closed"
			}
			w.Pane = ""
			r.event("close %s pane=%s", name, pane)
			if reap {
				w.Reaped = true
				r.event("reap %s", name)
			}
		} else {
			r.event("close failed %s: %v", name, cerr)
		}
		return nil
	})
	if cerr == nil {
		cleanRunOverflowWarning(runID)
		return nil
	}
	return cerr
}

// A confirmed pane close succeeds even if tab cleanup needs a later retry.
func cleanRunOverflowWarning(runID string) {
	if err := cleanRunOverflow(runID); err != nil {
		fmt.Fprintln(os.Stderr, "orch: warning: overflow cleanup:", err)
	}
}

// cleanRunOverflow persists partial cleanup even when another tab cannot be closed.
func cleanRunOverflow(runID string) error {
	var cleanupErr error
	err := withRun(runID, func(r *Run) error {
		cleanupErr = cleanupOverflowTabs(r)
		if cleanupErr != nil {
			r.event("overflow cleanup warning: %v", cleanupErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return cleanupErr
}

// closeRun closes every recorded pane of a run, verifies removal and reports leftovers.
func closeRun(runID string, end bool) ([]string, error) {
	r, err := loadRun(runID)
	if err != nil {
		return nil, err
	}
	if err := ownRun(r); err != nil {
		return nil, err
	}
	var failed []string
	for _, w := range r.liveWorkers() {
		if err := closeWorker(runID, w.Name); err != nil {
			failed = append(failed, w.Name)
		}
	}
	unlock, err := lockHead(r.HeadPane)
	if err != nil {
		return nil, err
	}
	cleanupErr := cleanRunOverflow(runID)
	unlock()
	if cleanupErr != nil {
		failed = append(failed, cleanupErr.Error())
	}
	var notes []string
	r, _ = loadRun(runID)
	for _, w := range r.Workers {
		if w.Worktree != "" {
			if _, err := os.Stat(w.Worktree); err == nil {
				notes = append(notes, fmt.Sprintf("worktree remains: %s (worker %s). remove with git worktree remove after merging", w.Worktree, w.Name))
			}
		}
	}
	if end {
		_ = withRun(runID, func(r *Run) error {
			r.Status = "closed"
			r.event("run ended")
			return nil
		})
	}
	if len(failed) > 0 {
		return notes, fmt.Errorf("workers still open: %s", strings.Join(failed, ", "))
	}
	return notes, nil
}

// ownRun enforces that a Head only ever closes panes it spawned itself.
func ownRun(r *Run) error {
	if head := os.Getenv("HERDR_PANE_ID"); r.HeadPane != head {
		return fmt.Errorf("run %s was spawned by another Head (%s). close only panes you spawned (to take over, use orch resume --run %s)", r.ID, r.HeadPane, r.ID)
	}
	return nil
}

// gc closes recorded panes of this Head's closed runs. With orphans, it also cleans runs
// whose Head pane no longer exists (a crashed session); live Heads' runs are never touched.
func gc(orphans bool) ([]string, error) {
	runs, err := allRuns()
	if err != nil {
		return nil, err
	}
	// Ad-hoc task runs own no panes; old ones are only clutter.
	report := pruneAdhoc(runs, adhocRetention)
	panes, err := listPanes("")
	if err != nil {
		return report, err
	}
	live := map[string]bool{}
	for _, p := range panes {
		live[p.ID] = true
	}
	head := os.Getenv("HERDR_PANE_ID")
	for _, r := range runs {
		if r.Adhoc {
			continue
		}
		mine := r.HeadPane == head
		deadHead := !live[r.HeadPane]
		switch {
		case mine && r.Status != "active":
		case orphans && deadHead:
		default:
			continue
		}
		for _, w := range r.liveWorkers() {
			if live[w.Pane] {
				if err := closePane(w.Pane); err != nil {
					report = append(report, fmt.Sprintf("%s/%s: %v", r.ID, w.Name, err))
					continue
				}
			}
			report = append(report, fmt.Sprintf("%s/%s pane %s cleaned", r.ID, w.Name, w.Pane))
			_ = withRun(r.ID, func(r *Run) error {
				if x := r.worker(w.Name); x != nil {
					x.Pane = ""
					if x.Status != "done" && x.Status != "partial" {
						x.Status = "closed"
					}
				}
				return nil
			})
		}
		unlock, err := lockHead(r.HeadPane)
		if err != nil {
			return report, err
		}
		if err := cleanRunOverflow(r.ID); err != nil {
			report = append(report, fmt.Sprintf("%s: %v", r.ID, err))
		}
		unlock()
		if r.Status == "active" {
			_ = withRun(r.ID, func(r *Run) error {
				r.Status = "closed"
				r.event("gc: Head pane %s disappeared; run ended", r.HeadPane)
				return nil
			})
			report = append(report, fmt.Sprintf("%s: Head disappeared; run ended", r.ID))
		}
	}
	return report, nil
}

// adhocRetention is how long gc keeps the directory of an ad-hoc task run.
const adhocRetention = 7 * 24 * time.Hour

// pruneAdhoc deletes ad-hoc run directories created more than keep ago.
func pruneAdhoc(runs []*Run, keep time.Duration) []string {
	var report []string
	for _, r := range runs {
		if !r.Adhoc || time.Since(r.Created) <= keep {
			continue
		}
		if err := os.RemoveAll(r.dir()); err != nil {
			report = append(report, fmt.Sprintf("%s: ad-hoc run not deleted: %v", r.ID, err))
			continue
		}
		report = append(report, fmt.Sprintf("%s: ad-hoc run deleted", r.ID))
	}
	return report
}

// resume re-attaches a run to the current Head pane and marks vanished workers as lost.
func resume(runID string) ([]string, error) {
	var report []string
	head := os.Getenv("HERDR_PANE_ID")
	err := withRun(runID, func(r *Run) error {
		if r.HeadPane != head {
			// Only a run whose Head is gone can be taken over; a live Head keeps its panes.
			alive, err := paneExists(r.HeadPane)
			if err != nil {
				return fmt.Errorf("could not check existing Head pane %s: %w", r.HeadPane, err)
			}
			if alive {
				return fmt.Errorf("run %s Head pane %s is still alive; do not take over another Head run", r.ID, r.HeadPane)
			}
			report = append(report, fmt.Sprintf("Head pane %s → %s", r.HeadPane, head))
			r.HeadPane = head
			r.Workspace = os.Getenv("HERDR_WORKSPACE_ID")
			r.Tab = os.Getenv("HERDR_TAB_ID")
		}
		r.Status = "active"
		for _, w := range r.liveWorkers() {
			exists, err := paneExists(w.Pane)
			if err != nil {
				return err
			}
			if !exists {
				w.Status = "lost"
				report = append(report, fmt.Sprintf("%s: pane %s missing; marked lost (restart from contract)", w.Name, w.Pane))
				w.Pane = ""
			} else {
				report = append(report, fmt.Sprintf("%s: pane %s retained (%s)", w.Name, w.Pane, w.Status))
			}
		}
		r.event("resume at %s", head)
		return nil
	})
	return report, err
}
