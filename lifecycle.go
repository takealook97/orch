package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type workerCloseError struct{ err error }

func (e *workerCloseError) Error() string { return e.err.Error() }
func (e *workerCloseError) Unwrap() error { return e.err }

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
	capacityWaiting := map[string]bool{}
	var outcomes []waitOutcome
	deadline := time.Now().Add(timeout)
	lastRead := time.Time{}
	head := headID()
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
						if err := saveTask(t); err != nil {
							return outcomes, err
						}
					}
				}
				if t.Status != "running" {
					outcomes = append(outcomes, waitOutcome{name, t.Status, taskReport(t, false)})
					continue
				}
				if time.Now().After(deadline) {
					outcomes = append(outcomes, waitOutcome{name, "timeout", waitTimeoutDetail("working", name)})
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
					outcomes = append(outcomes, waitOutcome{name, "timeout", waitTimeoutDetail(workerAgentState(w), name)})
					continue
				}
				still = append(still, name)
				continue
			}
			if st != "" {
				detail, accepted, err := collectWorker(runID, name)
				if err != nil {
					var closeFailure *workerCloseError
					if errors.As(err, &closeFailure) {
						outcomes = append(outcomes, waitOutcome{name, "close-failed", detail + err.Error()})
						continue
					}
					return outcomes, err
				}
				if accepted == "" {
					// State can change between the preliminary check and collection.
					still = append(still, name)
					continue
				}
				outcomes = append(outcomes, waitOutcome{name, accepted, detail})
				continue
			}
			info, _ := agentGet(workerAgent(w))
			if info.Status == "blocked" {
				outcomes = append(outcomes, waitOutcome{name, "agent-blocked", "orch read " + name + " to inspect, then decide"})
				continue
			}
			if readNow {
				text, readErr := agentRead(workerAgent(w), 80)
				if readErr == nil && quotaText(text) {
					saveTail(r, w, text)
					if fallback {
						nw, ferr := doFallbackFrom(runID, name, "quota exhausted", crExhausted(text))
						if ferr == nil {
							outcomes = append(outcomes, waitOutcome{name, "fallback:" + nw, "quota exhausted → " + nw})
							still = append(still, nw)
							continue
						}
						outcomes = append(outcomes, waitOutcome{name, "quota", ferr.Error()})
						continue
					}
					if err := setStatus(runID, name, "quota"); err != nil {
						return outcomes, err
					}
					outcomes = append(outcomes, waitOutcome{name, "quota", "quota exhausted"})
					continue
				}

				if readErr == nil && (info.Status == "idle" || info.Status == "done") {
					inc, isIncident := screenIncident(text)
					capacityWaiting[name] = isIncident
					noteRecovered(runID, w, text, false)
					if isIncident {
						_, acted, exhausted, resumeErr := resumeOnIncident(runID, w, text)
						if resumeErr != nil {
							return outcomes, resumeErr
						}
						if acted {
							delete(idleSince, name)
						}
						if exhausted {
							saveTail(r, w, text)
							why := "model at capacity"
							if inc.name == blockIncident.name {
								why = "content blocked by Codex cyber_policy"
							}
							if fallback {
								// A cyber_policy stop follows the request, not the account: leave Codex.
								nw, ferr := doFallbackFrom(runID, name, why, inc.name == blockIncident.name && w.Kind == "codex")
								if ferr == nil {
									outcomes = append(outcomes, waitOutcome{name, "fallback:" + nw, why + " → " + nw})
									still = append(still, nw)
									continue
								}
								if err := setStatus(runID, name, inc.name); err != nil {
									return outcomes, err
								}
								outcomes = append(outcomes, waitOutcome{name, inc.name, ferr.Error()})
								continue
							}
							if err := setStatus(runID, name, inc.name); err != nil {
								return outcomes, err
							}
							outcomes = append(outcomes, waitOutcome{name, inc.name, why + "; retries exhausted. orch fallback " + name + " moves it to another key"})
							continue
						}
					}
				}
			}
			if info.Status == "working" {
				noteCapacityRecovered(runID, w)
				delete(capacityWaiting, name)
			}
			if capacityWaiting[name] {
				delete(idleSince, name)
				still = append(still, name)
				continue
			}
			if info.Status == "idle" || info.Status == "done" {
				if idleSince[name].IsZero() {
					idleSince[name] = time.Now()
				} else if time.Since(idleSince[name]) > timings.IdleNoResult {
					acted, exhausted, nudgeErr := nudgeIdle(runID, w)
					if nudgeErr != nil {
						return outcomes, nudgeErr
					}
					if acted {
						delete(idleSince, name)
					}
					if !exhausted {
						still = append(still, name)
						continue
					}
					outcomes = append(outcomes, waitOutcome{name, "idle-no-result", "agent stopped without a result file even after a nudge. orch read " + name + " to inspect, then answer, fall back or close it"})
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
				state := "working" // Pending tasks are still running.
				if w := r.worker(n); w != nil {
					state = workerAgentState(w)
				}
				outcomes = append(outcomes, waitOutcome{n, "timeout", waitTimeoutDetail(state, n)})
			}
			break
		}
		time.Sleep(timings.WaitPoll)
	}
	return outcomes, nil
}

// waitTimeoutDetail states the agent state and the exact rerun command. A
// foreground rerun replaces polling with status or screen reads.
func waitTimeoutDetail(state, name string) string {
	return state + "; rerun: orch wait " + name
}

// workerAgentState maps the multiplexer agent status to working, idle, blocked or unknown.
func workerAgentState(w *Worker) string {
	info, err := agentGet(workerAgent(w))
	if err != nil {
		return "unknown"
	}
	switch info.Status {
	case "working":
		return "working"
	case "idle", "done", "stopped", "finished":
		return "idle"
	case "blocked":
		return "blocked"
	}
	return "unknown"
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
func markReported(runID, name string) error {
	return withRun(runID, func(r *Run) error {
		if w := r.worker(name); w != nil {
			w.Reaped = false
		}
		return nil
	})
}

func setStatus(runID, name, st string) error {
	return withRun(runID, func(r *Run) error {
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
	return doFallbackFrom(runID, name, reason, blockedCodex(runID, name))
}

// blockedCodex reports a Codex worker that the cyber_policy stop has hit. That stop follows
// the request rather than the account, so its fallback must leave the Codex kind.
func blockedCodex(runID, name string) bool {
	r, err := loadRun(runID)
	if err != nil {
		return false
	}
	w := r.worker(name)
	return w != nil && w.Kind == "codex" && (w.Status == blockIncident.name || w.BlockRetries > 0 || !w.BlockSeen.IsZero() || strings.Contains(w.Alert, "cyber_policy"))
}

// doFallbackFrom with sameKindSpent skips account rotation and every key of the worker's
// own CLI kind, because the cr wrapper reported all of that CLI's accounts exhausted.
func doFallbackFrom(runID, name, reason string, sameKindSpent bool) (string, error) {
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
		if err := setStatus(runID, name, "blocked"); err != nil {
			return "", err
		}
		return "", fmt.Errorf("%s: reached the fallback limit (%d). report to the user", name, maxFallbacks)
	}
	authors, _ := familiesOf(r, w.Reviews)
	critics := criticsOf(r, w.Reviews)
	key, account := "", fallbackAccount(w)
	var exclude []string
	// Keys of an exhausted CLI kind stay in the lineage so later fallbacks never return to it.
	tried := w.Tried
	if sameKindSpent && w.Purpose != "" {
		tried = withoutKind(w.Purpose, w.Kind, tried)
	}
	if !sameKindSpent && envOrch("ACCOUNT_CMD") != "" && w.Account != "" && w.Account != "cr" {
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
		m, ok := nextFallback(purpose, w.Key, w.Role, authors, critics, tried)
		if !ok {
			if err := setStatus(runID, name, "blocked"); err != nil {
				return "", err
			}
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
	} else if strings.Contains(reason, "cyber_policy") || w.Status == blockIncident.name {
		failStatus = blockIncident.name
	} else if strings.Contains(reason, "capacity") {
		failStatus = "capacity"
	}
	if err := setStatus(runID, name, failStatus); err != nil {
		return "", err
	}
	if err := closeWorker(runID, name); err != nil {
		return "", fmt.Errorf("%s: fallback stopped because the old pane could not be closed: %w", name, err)
	}
	newName := fallbackName(r, name)
	nw, err := spawn(spawnOpts{
		Run: runID, Key: key, Name: newName, Role: w.Role, Purpose: w.Purpose, Reviews: w.Reviews,
		AuthorFamilies: nil, Cwd: w.Cwd, Worktree: w.Worktree, Account: account, Exclude: exclude,
		Tried: tried, TriedAccounts: exclude, Fallback: name,
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
	if err := withRun(runID, func(r *Run) error {
		r.event("fallback %s(%s) → %s(%s): %s", name, w.Key, newName, key, reason)
		return nil
	}); err != nil {
		return nw.Name, err
	}
	reasonCode := "capacity"
	if strings.Contains(reason, "quota") {
		reasonCode = "quota"
	} else if failStatus == blockIncident.name {
		reasonCode = "policy"
	}
	if err := recordMeasurement(Measurement{Version: 1, ID: nw.Name + "-recovery", Session: runID, Period: "run", Subject: "pane", Entity: nw.Name, Phase: "rework", Source: "orch-fallback", Kind: "missing", Coverage: "unavailable", Event: "recovery_started", Reason: reasonCode}); err != nil {
		fmt.Fprintln(os.Stderr, "orch: warning: persist recovery measurement:", err)
	}
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
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", "", fmt.Errorf("invalid worker name")
	}
	unlock, err := exclusiveFile(filepath.Join(runDir(runID), ".complete-"+name+".lock"), true)
	if err != nil {
		return "", "", err
	}
	defer unlock()
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
			if text, err := agentRead(workerAgent(w), 120); err == nil {
				p := saveTail(r, w, text)
				fmt.Fprintf(&b, "terminal output backup: %s\n", p)
			}
		}
		return b.String(), "", nil
	}
	fmt.Fprintf(&b, "status: %s\nresult: %s\n", st, w.Result)

	if data, err := os.ReadFile(w.Result); err == nil {
		if summary := collectSummary(string(data)); summary != "" {
			fmt.Fprintln(&b, summary)
		}
	}
	if w.Merged && w.Worktree == "" {
		fmt.Fprintf(&b, "merged worktree already removed: %s (%s)\n", w.Branch, w.Commit)
		return b.String(), st, nil
	}

	var closeErr error
	if w.Pane == "" && w.Reaped {
		fmt.Fprintf(&b, "pane closed by the reaper\n")
	}
	if w.Pane != "" && !agentStopped(w) {
		fmt.Fprintf(&b, "agent still working; pane left open\n")
		return b.String(), "", nil
	} else if w.Pane != "" {
		if closeErr = closeWorker(runID, name); closeErr != nil {
			fmt.Fprintf(&b, "pane close failed: %v\n", closeErr)
		} else {
			fmt.Fprintf(&b, "pane closure confirmed\n")
			_ = normalizeOwnedLayouts(r.HeadPane)
		}
	}
	var fresh []string
	if w.Contract != "" && w.Cwd != "" {
		body, err := os.ReadFile(w.Contract)
		// Preserve non-git tasks that declare no commit baseline.
		checkScope := err != nil || isGitDir(w.Cwd) || explicitBase(contractBase(string(body)))
		if err == nil && checkScope {
			var root string
			root, err = workerScopeRepository(w, string(body))
			if err == nil && root != repoRoot(w.Cwd) {
				fmt.Fprintf(&b, "scope checked in repository: %s\n", root)
			}
			if err == nil {
				baseline := w.Baseline
				if (w.BaselineRoot != "" && w.BaselineRoot != root) || (w.BaselineRoot == "" && root != repoRoot(w.Cwd)) {
					baseline = nil
				}
				var bad []string
				fresh, bad, err = completionChanges(w.Cwd, root, paneCommitBase(w), baseline, workerOwnedGlobs(w, string(body)))
				if len(bad) > 0 {
					st = "failed"
					fmt.Fprintf(&b, "scope violation; marked failed: change outside owned files %s\n", strings.Join(bad, ", "))
				}
				if len(fresh) > 0 {
					fmt.Fprintf(&b, "changed files: %s\n", strings.Join(fresh, ", "))
				}
			}
		} else if err == nil {
			fmt.Fprintln(&b, "scope: unchecked (non-git task without explicit baseline)")
		}
		if err != nil {
			st = "failed"
			fmt.Fprintf(&b, "scope could not be checked; marked failed: %v\n", err)
		}

	}
	if w.Branch != "" && !w.Committed && (st == "done" || st == "partial") && closeErr == nil && (w.Pane == "" || agentStopped(w)) {
		body, _ := os.ReadFile(w.Result)
		hash, err := commitCompletion(w.Worktree, runID, name, collectSummary(string(body)), fresh)
		if err != nil {
			st = "failed"
			fmt.Fprintf(&b, "worktree commit failed; left in %s: %v\n", w.Worktree, err)
		} else if err := withRun(runID, func(r *Run) error {
			x := r.worker(name)
			x.Commit, x.Committed = hash, true
			r.event("committed %s (%s)", name, hash)
			return nil
		}); err != nil {
			return b.String(), st, err
		} else {
			fmt.Fprintf(&b, "committed: %s (%s)\n", w.Branch, hash)
		}
	} else if w.Branch != "" && st == "failed" {
		fmt.Fprintf(&b, "worktree left uncommitted: %s (branch %s)\n", w.Worktree, w.Branch)
		if err := withRun(runID, func(r *Run) error { r.worker(name).Committed = false; return nil }); err != nil {
			return b.String(), st, err
		}
	}
	if err := withRun(runID, func(r *Run) error {
		x := r.worker(name)
		if x == nil {
			return fmt.Errorf("worker disappeared")
		}
		x.Status = st
		x.Accepted = closeErr == nil && (st == "done" || st == "partial")
		return nil
	}); err != nil {
		return b.String(), st, err
	}
	if closeErr != nil {
		return b.String(), st, &workerCloseError{fmt.Errorf("worker %s pane could not be closed: %w", name, closeErr)}
	}
	if err := recordPaneCompletion(runID, w, st); err != nil {
		fmt.Fprintf(&b, "warning: persist pane measurement: %v\n", err)
	}
	if st == "done" || st == "partial" {
		if err := mergeKnowledge(runID, name, w.Result); err != nil {
			return b.String(), st, err
		}
	}
	if err := markReported(runID, name); err != nil {
		return b.String(), st, err
	}
	return b.String(), st, nil
}

func closeWorker(runID, name string) error { return closeWorkerAs(runID, name, "", false) }

// closeWorkerAs closes a worker's pane; reap marks the close as the reaper's so wait and
// collect still report the worker.
// closeWorkerAs closes name's pane. A non-empty expect is the pane the caller observed: if the
// name now points at another pane (the worker was closed and the name reused), nothing is closed.
func closeWorkerAs(runID, name, expect string, reap bool) error {
	unlock, err := lockHead(headID())
	if err != nil {
		return err
	}
	defer unlock()
	var pane, terminal string
	err = withRun(runID, func(r *Run) error {
		if err := ownRun(r); err != nil {
			return err
		}
		w := r.worker(name)
		if w == nil {
			return fmt.Errorf("worker %q not found", name)
		}
		pane = w.Pane
		terminal = w.Terminal
		return nil
	})
	if err != nil {
		return err
	}
	if expect != "" && pane != expect {
		return nil // the name was reused for a new worker; never close its pane
	}
	if pane == "" {
		return cleanRunOverflowWarning(runID)
	}
	var cerr error
	if pane != "" {
		if backend() == "app" {
			cerr = appClose(terminal, appIdentity{runID, name})
		} else {
			exists, probeErr := paneExists(pane)
			if probeErr != nil {
				cerr = probeErr
			} else if exists {
				cerr = closePane(pane)
			}
		}
	}
	persistErr := withRun(runID, func(r *Run) error {
		w := r.worker(name)
		if w == nil {
			return fmt.Errorf("worker disappeared")
		}
		if cerr == nil && w.Pane != pane {
			return nil // another orch process recorded this close first, or the name was reused
		}
		if cerr == nil {
			w.Closed = time.Now()
			// Keep a final outcome (done/partial/blocked/failed/quota); otherwise mark closed.
			if st := resultStatus(w.Result); st != "" && w.Status != "failed" {
				w.Status = st
			} else if w.Status != "failed" && w.Status != "quota" && w.Status != "capacity" && w.Status != blockIncident.name && w.Status != "blocked" {
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
	if persistErr != nil {
		return errors.Join(cerr, persistErr)
	}
	if cerr == nil {
		if err := cleanRunOverflowWarning(runID); err != nil {
			return err
		}
		if backend() == "app" {
			if err := normalizeAppLayouts(headID()); err != nil {
				if persistErr := withRun(runID, func(r *Run) error {
					r.event("warning: app layout normalization after close %s: %v", name, err)
					return nil
				}); persistErr != nil {
					return persistErr
				}
			}
		}
		return nil
	}
	return cerr
}

// A confirmed pane close succeeds even if tab cleanup needs a later retry.
func cleanRunOverflowWarning(runID string) error {
	cleanupErr, persistErr := cleanRunOverflowState(runID)
	if cleanupErr != nil {
		var closeErr *overflowCloseError
		warn := true
		if errors.As(cleanupErr, &closeErr) {
			_, warned := overflowCloseWarnings.LoadOrStore(closeErr.tab, struct{}{})
			warn = !warned
		}
		if warn {
			fmt.Fprintln(os.Stderr, "orch: warning: overflow cleanup:", cleanupErr)
		}
	}
	return persistErr
}

func cleanRunOverflow(runID string) error {
	cleanupErr, persistErr := cleanRunOverflowState(runID)
	return errors.Join(cleanupErr, persistErr)
}

// Keep UI cleanup warnings separate from mandatory persistence errors.
func cleanRunOverflowState(runID string) (cleanupErr, persistErr error) {
	unchanged := errors.New("overflow state unchanged")
	persistErr = withRun(runID, func(r *Run) error {
		before, err := json.Marshal(r)
		if err != nil {
			return err
		}
		cleanupErr = cleanupOverflowTabs(r)
		if cleanupErr != nil {
			r.event("overflow cleanup warning: %v", cleanupErr)
		}
		after, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if string(before) == string(after) {
			return unchanged
		}
		return nil
	})
	if errors.Is(persistErr, unchanged) {
		persistErr = nil
	}
	return
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
	if r.Status == "closed" && end {
		return nil, nil
	}
	if end {
		if err := withRun(runID, func(r *Run) error { r.EndRequested = true; r.Status = "active"; return nil }); err != nil {
			return nil, err
		}
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
	r, err = loadRun(runID)
	if err != nil {
		return notes, err
	}
	worktreeNotes, worktreeErr := cleanupRecordedWorktrees(r, false)
	notes = append(notes, worktreeNotes...)
	if worktreeErr != nil {
		failed = append(failed, worktreeErr.Error())
	}
	if end && len(failed) == 0 {
		if err := withRun(runID, func(r *Run) error { r.Status = "closed"; r.EndRequested = false; r.event("run ended"); return nil }); err != nil {
			return notes, err
		}
	}
	if len(failed) > 0 {
		return notes, fmt.Errorf("workers still open: %s", strings.Join(failed, ", "))
	}
	return notes, nil
}

// ownRun enforces that a Head only ever closes panes it spawned itself.
func ownRun(r *Run) error {
	if err := checkRunBackend(r); err != nil {
		return err
	}
	if head := headID(); r.HeadPane != head {
		return fmt.Errorf("run %s was spawned by another Head (%s). close only panes you spawned (to take over, use orch resume --run %s)", r.ID, r.HeadPane, r.ID)
	}
	return nil
}

// staleRunActivity checks persisted metadata only. A stale Head or a different
// backend cannot prove a worker stopped, so live statuses always pin the run.
func staleRunActivity(r *Run, cutoff time.Time) (time.Time, bool) {
	if r.Status != "active" {
		return time.Time{}, false
	}
	for _, w := range r.Workers {
		if w != nil && (contains1([]string{"working", "prompted", "spawned"}, w.Status) ||
			(w.Pane != "" || w.Terminal != "") && w.Status != "closed" && w.Status != "lost") {
			return time.Time{}, false
		}
	}
	info, err := os.Stat(filepath.Join(r.dir(), "run.json"))
	if err != nil {
		return time.Time{}, false
	}
	last := info.ModTime()
	tasks := make(map[string]*Task)
	for _, task := range r.Tasks {
		if task != nil {
			tasks[task.ID] = task
		}
	}
	paths, err := filepath.Glob(filepath.Join(r.dir(), "tasks", "*", "task.json"))
	if err != nil {
		return time.Time{}, false
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return time.Time{}, false
		}
		var task Task
		if json.Unmarshal(data, &task) != nil {
			return time.Time{}, false
		}
		tasks[filepath.Base(filepath.Dir(path))] = &task
		info, err := os.Stat(path)
		if err != nil {
			return time.Time{}, false
		}
		last = latestTime(last, info.ModTime())
	}
	for _, task := range tasks {
		if contains1([]string{"running", "queued", "pending"}, task.Status) {
			return last, false
		}
	}
	return last, last.Before(cutoff)
}

// forgetMissingWorktrees clears only absent paths, preserving Git branches.
// The caller holds the run lock; task files must agree with run.json.
func forgetMissingWorktrees(r *Run) (bool, error) {
	changed := false
	clear := func(path *string, merged *bool) bool {
		if *path == "" {
			return false
		}
		if _, err := os.Lstat(*path); !os.IsNotExist(err) {
			return false
		}
		*path = ""
		// An empty merged entry otherwise triggers retained-branch cleanup.
		*merged = false
		return true
	}
	for _, w := range r.Workers {
		if w != nil {
			changed = clear(&w.Worktree, &w.Merged) || changed
		}
	}
	for _, task := range r.Tasks {
		if task != nil && clear(&task.Worktree, &task.Merged) {
			changed = true
			if err := writeTask(task); err != nil {
				return changed, err
			}
		}
	}
	return changed, nil
}

// cleanupRecordedWorktrees refreshes persisted records before normal cleanup.
func cleanupRecordedWorktrees(r *Run, gcMode bool) ([]string, error) {
	unchanged := errors.New("worktree records unchanged")
	err := withRun(r.ID, func(fresh *Run) error {
		changed, err := forgetMissingWorktrees(fresh)
		if err != nil {
			return err
		}
		if !changed {
			return unchanged
		}
		return nil
	})
	if err != nil && !errors.Is(err, unchanged) {
		return nil, err
	}
	fresh, err := loadRun(r.ID)
	if err != nil {
		return nil, err
	}
	return cleanupRunWorktreesChecked(fresh, gcMode)
}

// closeStaleRun rechecks under the run lock and forgets absent worktree paths
// without touching panes, existing worktrees, or Git branches.
func closeStaleRun(candidate *Run, now time.Time, keep time.Duration) ([]string, bool, error) {
	if keep <= 0 {
		return nil, false, nil
	}
	cutoff := now.Add(-keep)
	if _, eligible := staleRunActivity(candidate, cutoff); !eligible {
		return nil, false, nil
	}
	skipped := errors.New("run no longer stale")
	var report []string
	err := withRun(candidate.ID, func(r *Run) error {
		last, eligible := staleRunActivity(r, cutoff)
		if !eligible {
			return skipped
		}
		if _, err := forgetMissingWorktrees(r); err != nil {
			return err
		}
		r.Status = "closed"
		r.event("gc: stale run closed after retention window")
		report = append(report, fmt.Sprintf("closed stale run %s (backend %s, head %s, idle %dd)", r.ID, runBackend(r), r.HeadPane, int(now.Sub(last)/(24*time.Hour))))
		for _, w := range r.Workers {
			if w != nil && w.Worktree != "" {
				report = append(report, fmt.Sprintf("worktree remains: %s (branch %s)", w.Worktree, w.Branch))
			}
		}
		for _, task := range r.Tasks {
			if task != nil && task.Worktree != "" {
				report = append(report, fmt.Sprintf("worktree remains: %s (branch %s)", task.Worktree, task.Branch))
			}
		}
		return nil
	})
	if errors.Is(err, skipped) {
		return nil, false, nil
	}
	return report, err == nil, err
}

// gc cleans closed runs and, with orphans, restores dead-Head cleanup first.
// Remaining idle active runs may close using metadata regardless of backend or Head.
func gc(orphans bool) ([]string, error) {
	runs, err := allRuns()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var report []string
	handled := map[string]bool{}
	finish := func() ([]string, error) {
		if orphans {
			for _, r := range runs {
				if handled[r.ID] {
					continue
				}
				lines, _, err := closeStaleRun(r, now, retentionWindow())
				if err != nil {
					return report, err
				}
				report = append(report, lines...)
			}
		}
		return report, nil
	}
	// These runs cannot enter the dead-Head pane path. Close them before
	// any pane access or cleanup error can prevent metadata-only retention.
	if orphans {
		for _, r := range runs {
			if !r.Adhoc && runBackend(r) == currentBackend() {
				continue
			}
			lines, _, err := closeStaleRun(r, now, retentionWindow())
			if err != nil {
				return report, err
			}
			report = append(report, lines...)
			handled[r.ID] = true
		}
	}
	var paneRuns []*Run
	for _, r := range runs {
		if r.Status == "closed" && (r.Adhoc || r.HeadPane != headID() || runBackend(r) != currentBackend()) {
			notes, err := cleanupRecordedWorktrees(r, true)
			report = append(report, notes...)
			if err != nil {
				return report, err
			}
		}
		if !r.Adhoc && runBackend(r) == currentBackend() && (r.HeadPane == headID() || orphans) {
			paneRuns = append(paneRuns, r)
		}
	}
	// Pane cleanup persists run state. Prune old closed evidence before those
	// writes can refresh its activity timestamp, as in the original orphan path.
	report = append(report, diskPruneReport(now)...)
	if len(paneRuns) == 0 {
		return finish()
	}
	panes, err := listPanes("")
	if err != nil {
		return report, err
	}
	live := map[string]bool{}
	for _, p := range panes {
		live[p.ID] = true
	}
	head := headID()
	for _, r := range paneRuns {
		mine := r.HeadPane == head
		deadHead := !live[r.HeadPane]
		if backend() == "app" {
			exists, err := paneExists(r.HeadPane)
			if err != nil {
				return report, err
			}
			deadHead = !exists
		}
		switch {
		case mine && r.Status != "active":
		case orphans && deadHead:
		default:
			continue
		}
		handled[r.ID] = true
		var runCleanupErr error
		for _, w := range r.liveWorkers() {
			exists := live[w.Pane]
			if backend() == "app" {
				var err error
				exists, err = paneExists(w.Terminal)
				if err != nil {
					runCleanupErr = errors.Join(runCleanupErr, err)
					report = append(report, fmt.Sprintf("%s/%s: %v", r.ID, w.Name, err))
					continue
				}
			}
			if exists || backend() == "app" {
				var closeErr error
				if backend() == "app" {
					closeErr = appClose(w.Terminal, appIdentity{r.ID, w.Name})
				} else {
					closeErr = closePane(w.Pane)
				}
				if err := closeErr; err != nil {
					runCleanupErr = errors.Join(runCleanupErr, err)
					report = append(report, fmt.Sprintf("%s/%s: %v", r.ID, w.Name, err))
					continue
				}
			}
			report = append(report, fmt.Sprintf("%s/%s pane %s cleaned", r.ID, w.Name, w.Pane))
			if err := withRun(r.ID, func(r *Run) error {
				if x := r.worker(w.Name); x != nil {
					x.Pane = ""
					if x.Status != "done" && x.Status != "partial" {
						x.Status = "closed"
					}
				}
				return nil
			}); err != nil {
				return report, err
			}
		}
		unlock, err := lockHead(r.HeadPane)
		if err != nil {
			return report, err
		}
		if err := cleanRunOverflow(r.ID); err != nil {
			runCleanupErr = errors.Join(runCleanupErr, err)
			report = append(report, fmt.Sprintf("%s: %v", r.ID, err))
		}
		unlock()
		if runCleanupErr != nil {
			persistErr := withRun(r.ID, func(r *Run) error { r.Status = "active"; r.EndRequested = true; return nil })
			return report, errors.Join(runCleanupErr, persistErr)
		}
		if r.Status == "active" {
			if err := withRun(r.ID, func(r *Run) error {
				r.Status = "closed"
				r.event("gc: Head pane %s disappeared; run ended", r.HeadPane)
				return nil
			}); err != nil {
				return report, err
			}
			report = append(report, fmt.Sprintf("%s: Head disappeared; run ended", r.ID))
		}
		if fresh, err := loadRun(r.ID); err == nil {
			notes, err := cleanupRecordedWorktrees(fresh, true)
			report = append(report, notes...)
			if err != nil {
				return report, err
			}
		}
	}
	return finish()
}

// pruneAdhoc retains the standalone helper for callers with an explicit window.
func pruneAdhoc(runs []*Run, keep time.Duration) []string {
	if keep <= 0 {
		return nil
	}
	home, err := resolvedOrchHome()
	if err != nil {
		return nil
	}
	info, err := os.Lstat(runsDir())
	if err != nil || !info.IsDir() {
		return nil
	}
	// Restrict the helper to the supplied runs.
	var report []string
	for _, r := range runs {
		if r == nil || !r.Adhoc {
			continue
		}
		lines, _ := pruneSelectedRuns(home, time.Now(), keep, []*Run{r})
		report = append(report, lines...)
	}
	return report
}

// resume re-attaches a run to the current Head pane and marks vanished workers as lost.
func resume(runID string) ([]string, error) {
	var report []string
	head := headID()
	if backend() == "app" && (headSpace() == "" || headTab() == "") {
		return nil, fmt.Errorf("cannot resolve app Head location")
	}
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
			space, tab := headSpace(), headTab()
			if backend() == "app" && (space == "" || tab == "") {
				return fmt.Errorf("cannot resolve app Head location")
			}
			r.HeadPane = head
			r.Workspace = space
			r.Tab = tab
		}
		r.Status = "active"
		for _, w := range r.liveWorkers() {
			exists, err := paneExists(w.Pane)
			if err != nil {
				return err
			}
			if !exists {
				if backend() == "app" {
					appRemoveFinished(w.Terminal, appIdentity{r.ID, w.Name})
				}
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

// collectSummary preserves up to three physical lines and caps displayed text at 600 runes.
func collectSummary(body string) string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if len(lines) == 0 {
			if !strings.HasPrefix(line, "- summary:") && !strings.HasPrefix(line, "summary:") {
				continue
			}
			lines = append(lines, strings.TrimPrefix(line, "- "))
			continue
		}
		trimmed := strings.TrimSpace(line)
		if len(lines) >= 3 || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "##") {
			break
		}
		lines = append(lines, line)
	}
	text := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if rs := []rune(text); len(rs) > 600 {
		text = string(rs[:599]) + "…"
	}
	return text
}
