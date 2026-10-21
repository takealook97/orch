package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type spawnOpts struct {
	Serves  []int
	FixOf   string
	Run     string
	Key     string
	Name    string
	Role    string
	Purpose string
	Reviews []string
	// AuthorFamilies names authors that are not workers of this run, e.g. the Head itself.
	AuthorFamilies []string
	Cwd            string
	Worktree       string
	Account        string
	Exclude        []string // account labels to skip (fallback rotation)
	Tried          []string
	TriedAccounts  []string
	Fallback       string
}

func requireHead() error {
	if envOrch("ROLE") == "worker" {
		return errors.New("ORCH_ROLE=worker: workers cannot delegate (orch spawn denied)")
	}
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("HERDR_PANE_ID") == "" {
		return errors.New("not inside a Herdr pane (HERDR_ENV=1, HERDR_PANE_ID required)")
	}
	return nil
}

var kindFamily = map[string]string{"claude": familyAnthropic, "codex": familyOpenAI, "agy": familyGoogle}

// resolveFamily accepts a family name or "head" (the family of the CLI running in this pane).
func resolveFamily(v string) (string, error) {
	switch v {
	case familyOpenAI, familyAnthropic, familyGoogle:
		return v, nil
	case "head":
		var res struct {
			Pane struct {
				Agent string `json:"agent"`
			} `json:"pane"`
		}
		if err := herdrJSON(&res, "pane", "get", os.Getenv("HERDR_PANE_ID")); err != nil {
			return "", err
		}
		if f, ok := kindFamily[res.Pane.Agent]; ok {
			return f, nil
		}
		return "", fmt.Errorf("cannot infer family from Head pane agent type %q. Pass a family name to --author-family", res.Pane.Agent)
	}
	return "", fmt.Errorf("unknown family %q (openai|anthropic|google|head)", v)
}

// familiesOf returns the families of the named workers in the run.
// Entries of the form author:<family> stand for authors outside the run (the Head).
func familiesOf(r *Run, names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		if fam, ok := strings.CutPrefix(n, "author:"); ok {
			if !slices.Contains(out, fam) {
				out = append(out, fam)
			}
			continue
		}
		w := r.worker(n)
		if w == nil {
			return nil, fmt.Errorf("worker %q from --reviews is not in the run", n)
		}
		if !slices.Contains(out, w.Family) {
			out = append(out, w.Family)
		}
	}
	return out, nil
}

// criticsOf returns families of critical reviewers that review any of the same targets.
func criticsOf(r *Run, targets []string) []string {
	var out []string
	for _, w := range r.Workers {
		if w.Role != roleCritical {
			continue
		}
		for _, t := range w.Reviews {
			if slices.Contains(targets, t) && !slices.Contains(out, w.Family) {
				out = append(out, w.Family)
			}
		}
	}
	return out
}

// maxFallbacks bounds one replacement lineage (keys plus accounts).
const maxFallbacks = 6

// lockHead serializes every spawn of one Head so the row cap, name checks, split and the
// run record happen atomically even when several orch processes run at once.
func lockHead(head string) (func(), error) {
	dir := filepath.Join(orchHome(), "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	name := strings.NewReplacer(":", "_", "/", "_").Replace(head)
	f, err := os.OpenFile(filepath.Join(dir, "head-"+name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func spawn(o spawnOpts) (*Worker, error) {
	if err := requireHead(); err != nil {
		return nil, err
	}
	m, err := modelByKey(o.Key)
	if err != nil {
		return nil, err
	}
	if _, err := modelArgs(m); err != nil {
		return nil, err
	}
	if !nameRE.MatchString(o.Name) {
		return nil, fmt.Errorf("name %q must match [a-z][a-z0-9_-]{0,31}", o.Name)
	}
	if strings.HasPrefix(o.Name, "task-") {
		return nil, fmt.Errorf("name %q is reserved for one-off tasks", o.Name)
	}
	if !slices.Contains(roles, o.Role) {
		return nil, fmt.Errorf("unknown --role %q (%s)", o.Role, strings.Join(roles, "|"))
	}
	if o.Purpose != "" {
		if _, ok := chains[o.Purpose]; !ok {
			return nil, fmt.Errorf("unknown --purpose %q (%s)", o.Purpose, strings.Join(purposeNames(), "|"))
		}
	} else if _, ok := chains[o.Role]; ok {
		o.Purpose = o.Role
	}
	release := func() {}
	committed := false
	defer func() {
		if !committed {
			release()
		}
	}()
	if o.Fallback == "" {
		var err error
		if release, err = admit(os.Getenv("HERDR_PANE_ID"), admission{"spawn", o.Name, o.Serves, o.FixOf}); err != nil {
			return nil, err
		}
	}
	head := os.Getenv("HERDR_PANE_ID")
	runID, err := resolveRun(o.Run)
	if err != nil {
		return nil, err
	}
	var authorFams []string
	for _, f := range o.AuthorFamilies {
		fam, err := resolveFamily(f)
		if err != nil {
			return nil, err
		}
		authorFams = append(authorFams, fam)
	}

	unlock, err := lockHead(head)
	if err != nil {
		return nil, err
	}
	var noDaemon bool
	var workerEnv []string
	var crPath string
	w, pane, err := func() (*Worker, string, error) {
		defer unlock()
		r, err := loadRun(runID)
		if err != nil {
			return nil, "", err
		}
		if err := ownRun(r); err != nil {
			return nil, "", fmt.Errorf("cannot spawn into another Head run: %w", err)
		}
		if r.Status != "active" {
			return nil, "", fmt.Errorf("run %s is closed", runID)
		}
		if w := r.worker(o.Name); w != nil && w.Status != "closed" && w.Status != "lost" && w.Pane != "" {
			return nil, "", fmt.Errorf("worker %q is already in the run", o.Name)
		}
		authors, err := familiesOf(r, o.Reviews)
		if err != nil {
			return nil, "", err
		}
		for _, fam := range authorFams {
			if !slices.Contains(authors, fam) {
				authors = append(authors, fam)
			}
			if !slices.Contains(o.Reviews, "author:"+fam) {
				o.Reviews = append(o.Reviews, "author:"+fam)
			}
		}
		warnings, err := checkLane(o.Role, m, authors, criticsOf(r, o.Reviews))
		if err != nil {
			return nil, "", err
		}
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "orch: warning:", w)
		}

		if err := withRun(r.ID, func(current *Run) error {
			refreshOverflow(current)
			r = current
			return nil
		}); err != nil {
			return nil, "", err
		}

		// The run cap is independent of the number of visible rows.
		if err := workerCapacity(r); err != nil {
			return nil, "", err
		}

		// Layout preconditions (I1, I6).
		l, err := getLayout(head)
		if err != nil {
			return nil, "", err
		}
		owned, err := ownedPanes(head)
		if err != nil {
			return nil, "", err
		}
		for _, p := range l.Panes {
			if p.ID != head && !slices.Contains(owned, p.ID) {
				return nil, "", fmt.Errorf("I6: pane %s in this tab was not created by orch. Spawn stopped because user panes must not be moved or closed (start Head in an empty tab)", p.ID)
			}
		}

		acc, path, err := paneAccount(o.Account, m.Kind, o.Exclude)
		crPath = path
		if err != nil {
			return nil, "", err
		}
		cwd := o.Cwd
		if o.Worktree != "" {
			cwd = o.Worktree
		}
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		cwd, _ = filepath.Abs(cwd)
		// Work (and trust) the real location, never a symlink name that points elsewhere.
		if real, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = real
		}
		env := append([]string{"ORCH_ROLE=worker", "ORCH_RUN=" + r.ID, "ORCH_WORKER=" + o.Name, "ORCH_MODEL=" + m.Key}, acc.Env...)
		noDaemon = envHas(env, "CR_CODEX_NO_DAEMON=1")
		workerEnv = env

		pane, tab, err := placeWorker(r, l, cwd, env)
		if err != nil {
			return nil, "", err
		}
		triedAcc := slices.Clone(o.TriedAccounts)
		if acc.Label != "" && !slices.Contains(triedAcc, acc.Label) && o.Account == "hook" {
			triedAcc = append(triedAcc, acc.Label)
		}
		w := &Worker{
			Name: o.Name, Key: m.Key, Family: m.Family, Kind: m.Kind, Pane: pane, Tab: tab, Role: o.Role,
			Purpose: o.Purpose, Reviews: o.Reviews, Account: acc.Label, Cwd: cwd, Worktree: o.Worktree,
			Result: r.resultPath(o.Name), Status: "spawned", Started: time.Now(),
			FallbackOf: o.Fallback, Tried: append(slices.Clone(o.Tried), m.Key), TriedAccounts: triedAcc,
		}
		// Record the pane before anything else can fail.
		if err := withRun(r.ID, func(r *Run) error {
			if err := retireName(r, o.Name); err != nil {
				return err
			}
			r.Workers = append(r.Workers, w)
			if tab != "" && tab != r.Tab && !slices.Contains(r.OverflowTabs, tab) {
				r.OverflowTabs = append(r.OverflowTabs, tab)
			}
			r.event("spawn %s key=%s role=%s pane=%s account=%s", w.Name, w.Key, w.Role, w.Pane, w.Account)
			return nil
		}); err != nil {
			_ = closePane(pane)
			// Reload ownership under the run lock; the pre-split snapshot is stale.
			_ = withRun(r.ID, func(current *Run) error {
				if tab != current.Tab && !slices.Contains(current.OverflowTabs, tab) {
					current.OverflowTabs = append(current.OverflowTabs, tab)
				}
				if cleanupErr := cleanupOverflowTabs(current); cleanupErr != nil {
					current.event("overflow cleanup warning: %v", cleanupErr)
				}
				return nil
			})
			return nil, "", err
		}
		// A layout that cannot be restored stops the spawn (and removes the new pane).
		if err := normalizeOwnedLayouts(head); err != nil {
			return w, pane, fmt.Errorf("layout: %w", err)
		}
		return w, pane, nil
	}()
	if err != nil {
		if pane != "" {
			abortWorker(runID, o.Name, err)
		}
		return nil, err
	}

	// Pre-trust the selected launch directory, including an explicit cwd outside Head cwd.
	trustArgs, terr := pretrustLaunch(m.Kind, w.Cwd, workerEnv, o.Cwd != "" || o.Worktree != "")
	if terr != nil {
		fmt.Fprintln(os.Stderr, "orch: warning: folder pretrust failed (automatically approve the trust dialog if shown):", terr)
	}
	launch, err := launchArgs(m, noDaemon)
	if err != nil {
		abortWorker(runID, o.Name, err)
		return nil, err
	}
	args := append([]string{"agent", "start", o.Name, "--kind", m.Kind, "--pane", pane, "--"}, append(launch, trustArgs...)...)
	if crPath != "" {
		err = startCRWorker(o.Name, pane, w.Cwd, crPaneCommand(crPath, m.Kind, w.Cwd, workerEnv, append(launch, trustArgs...)))
	} else {
		err = startWorkerAgent(o.Name, pane, w.Cwd, args)
	}
	if err != nil {
		abortWorker(runID, o.Name, err)
		return nil, fmt.Errorf("agent start failed (pane cleanup attempted): %w", err)
	}
	// Finished panes close even if the Head never runs its wait (one reaper per run).
	ensureReaper(runID)
	committed = true
	return w, nil
}

// startWorkerAgent verifies registration even when Herdr reports a successful start.
func startWorkerAgent(name, pane, cwd string, args []string) error {
	_, startErr := herdrRun(args...)
	debugf("agent start %s: err=%v", name, startErr)
	if startErr != nil && !strings.Contains(startErr.Error(), "agent_not_ready") && !paneShowsTrust(pane) {
		// One retry, as startup may race the shell prompt.
		time.Sleep(timings.StartRetry)
		_, startErr = herdrRun(args...)
		if startErr != nil && !strings.Contains(startErr.Error(), "agent_not_ready") && !paneShowsTrust(pane) {
			return startErr
		}
	}
	if err := waitAgentRegistered(name, pane, cwd, timings.ReadyTimeout); err != nil {
		if startErr != nil {
			return fmt.Errorf("%w (start: %v)", err, startErr)
		}
		return err
	}
	if startErr != nil {
		return waitReady(name, cwd, timings.ReadyTimeout)
	}
	settleTrust(name, cwd, timings.TrustSettleStart)
	return confirmAgentLive(name, pane, cwd)
}

// confirmAgentLive re-checks the agent once the trust settle window has passed. Registration
// right after start is not proof: a CLI stuck on its folder-trust screen drops out of Herdr's
// agent list and leaves what looks like a bare shell.
func confirmAgentLive(name, pane, cwd string) error {
	info, err := agentGet(name)
	if err == nil && info.Pane != pane {
		err = fmt.Errorf("agent lookup returned pane %q", info.Pane)
	}
	trust := paneShowsTrust(pane)
	switch {
	case err != nil && trust:
		return fmt.Errorf("agent %s left pane %s after start (cwd %s): folder-trust dialog is blocking CLI startup; last lookup: %v", name, pane, cwd, err)
	case err != nil:
		return fmt.Errorf("agent %s left pane %s after start (cwd %s): only a shell remains; last lookup: %v", name, pane, cwd, err)
	case trust:
		return fmt.Errorf("agent %s in pane %s is still on a folder-trust dialog (cwd %s)", name, pane, cwd)
	}
	return nil
}

// waitAgentRegistered requires the requested name to belong to the pane we created.
// Raw pane reads work even before Herdr has an agent record; never print their contents.
func waitAgentRegistered(name, pane, cwd string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		info, err := agentGet(name)
		if err == nil {
			if info.Pane == pane {
				return nil
			}
			if info.Pane != "" {
				return fmt.Errorf("agent %s registered in pane %s, expected %s (cwd %s)", name, info.Pane, pane, cwd)
			}
			err = fmt.Errorf("agent lookup returned no pane_id")
		}
		lastErr = err
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		time.Sleep(min(timings.ReadyPoll, remaining))
	}
	cause := "CLI startup, login, or shell launch failure; no folder-trust dialog detected"
	if paneShowsTrust(pane) {
		cause = "folder-trust dialog is blocking CLI startup"
	}
	return fmt.Errorf("agent %s did not register within %s (pane %s, cwd %s): %s; last lookup: %v", name, timeout, pane, cwd, cause, lastErr)
}

// abortWorker closes a worker that failed to start and records why.
func abortWorker(runID, name string, cause error) {
	cerr := closeWorker(runID, name)
	_ = withRun(runID, func(r *Run) error {
		if w := r.worker(name); w != nil {
			w.Status = "failed"
			r.event("start failed %s: %v (pane cleaned: %v)", name, cause, cerr)
		}
		return nil
	})
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "orch: could not close failed worker pane:", cerr)
	}
}

// realPath resolves symlinks so scope checks compare real locations, not link names.
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// within reports whether dir is base or below it, after resolving symlinks on both.
func within(base, dir string) bool {
	b, err1 := realPath(base)
	d, err2 := realPath(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(b, d)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func withinHeadCwd(dir string) bool {
	head, err := os.Getwd()
	if err != nil {
		return false
	}
	return within(head, dir)
}

// paneShowsTrust reads the raw pane (the agent name may not be registered yet).
func paneShowsTrust(pane string) bool {
	out, err := herdrRun("pane", "read", pane, "--source", "recent-unwrapped", "--lines", "40")
	return err == nil && trustDialog.MatchString(string(out))
}

// acceptTrust answers a folder-trust dialog on screen, only where orch may pre-trust.
// The Head already works in its cwd with bypass permissions, so a worker's folder-trust
// dialog is accepted only for that directory or below it (trustDialog: heuristics.go).
func acceptTrust(name, cwd string) bool {
	if !trustable(cwd) {
		debugf("trust %s: cwd %s is outside Head cwd", name, cwd)
		return false
	}
	text, err := agentRead(name, 40)
	if !trustDialog.MatchString(text) {
		debugf("trust %s: no trust dialog (read err=%v, %d bytes)", name, err, len(text))
		return false
	}
	debugf("trust %s: trust dialog approved", name)
	if strings.Contains(text, claudeTrustChoice) { // Claude: the default choice is "No, exit"
		_, _ = herdrRun("agent", "send-keys", name, "down")
		time.Sleep(timings.KeyGap)
	}
	_, _ = herdrRun("agent", "send-keys", name, "enter") // Codex/agy: "Trust" is preselected
	return true
}

// settleTrust watches a freshly started agent briefly and accepts a trust dialog once.
func settleTrust(name, cwd string, within time.Duration) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if acceptTrust(name, cwd) {
			time.Sleep(timings.TrustAccepted)
			return
		}
		time.Sleep(timings.Poll)
	}
}

func debugf(format string, a ...any) {
	if envOrch("DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "orch[debug]: "+format+"\n", a...)
	}
}

func waitReady(name, cwd string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	trusted := false
	for time.Now().Before(deadline) {
		info, err := agentGet(name)
		if err == nil && (info.Status == "idle" || info.Status == "done") {
			settleTrust(name, cwd, timings.TrustSettleReady)
			return nil
		}
		if err == nil && info.Status == "blocked" && !trusted {
			trusted = acceptTrust(name, cwd)
		}
		time.Sleep(timings.ReadyPoll)
	}
	return fmt.Errorf("agent %s is not ready (blocked or login required); inspect with orch read %s", name, name)
}

func promptMessage(r *Run, w *Worker) string {
	return fmt.Sprintf("%s Read contract %s and work only within its scope and permissions. "+
		"Write the result to %s in the contract Result report format. Add the status line only after the file is complete. "+
		"Do not delegate: no panes, subagents, Agent/Workflow tools, or orch.",
		promptMarker(w.Name), w.Contract, w.Result)
}

func contractHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func checkPromptContract(w *Worker, body []byte) error {
	if w.Prompted.IsZero() {
		return nil
	}
	if w.ContractHash == "" || w.ContractHash != contractHash(body) {
		closeCommand := strings.Join([]string{"orch", "close", w.Name}, " ")
		return fmt.Errorf("worker %s was already prompted; a running worker cannot reliably apply a revised contract (or its original hash is unavailable): %s, then spawn with a new name", w.Name, closeCommand)
	}
	return nil
}

func prompt(runFlag, name, contractFile string, wait bool, timeoutMS int) error {
	runID, err := resolveRun(runFlag)
	if err != nil {
		return err
	}
	var w *Worker
	var msg string
	var hash string
	var alreadyPrompted bool
	err = withRun(runID, func(r *Run) error {
		w = r.worker(name)
		if w == nil {
			return fmt.Errorf("worker %q is not in run %s", name, runID)
		}
		dest := r.contractPath(name)
		source := dest
		if contractFile != "" {
			source = contractFile
		}
		body, err := os.ReadFile(source)
		if err != nil {
			return fmt.Errorf("contract missing: %s ('orch contract new' to create one or specify --contract)", dest)
		}
		if err := checkPromptContract(w, body); err != nil {
			return err
		}
		if !w.Prompted.IsZero() {
			alreadyPrompted = true
			return nil
		}
		if err := validateContract(string(body)); err != nil {
			return err
		}
		abs, _ := filepath.Abs(source)
		if abs != dest {
			if err := os.WriteFile(dest, body, 0600); err != nil {
				return err
			}
		}
		w.Contract = dest
		hash = contractHash(body)
		if w.Cwd != "" {
			if root, err := scopeRepository(w.Cwd, string(body)); err == nil && isGitDir(root) {
				base := contractField(string(body), "base commit")
				if files, err := changedFiles(root, base); err == nil {
					w.Baseline = snapshot(root, files)
					w.BaselineRoot = root
				}
			}
		}
		msg = promptMessage(r, w)
		return nil
	})
	if err != nil {
		return err
	}
	if alreadyPrompted {
		fmt.Printf("worker %s already prompted with this contract; nothing sent\n", name)
		return nil
	}
	// A slow CLI may raise its folder-trust dialog after start; clear it before typing.
	settleTrust(name, w.Cwd, timings.TrustSettlePrompt)
	// Herdr's agent.prompt moves the UI to the agent's workspace and tab, which drags the
	// user back to the Head. Type the one-line prompt into the pane and press Enter instead;
	// neither pane.send_text nor agent.send_keys changes focus.
	send := func() error {
		if info, err := agentGet(name); err == nil && info.Status == "blocked" {
			return fmt.Errorf("agent %s is stopped at an approval or question dialog; inspect with orch read %s", name, name)
		}
		if _, err := herdrRun("pane", "send-text", w.Pane, msg); err != nil {
			return err
		}
		time.Sleep(timings.KeyGap)
		_, err := herdrRun("agent", "send-keys", name, "enter")
		return err
	}
	if err := send(); err != nil {
		return fmt.Errorf("prompt send failed: %w", err)
	}
	if !delivered(name, w.Cwd, w.Result, timings.Delivery) {
		// An agent that is still booting can swallow input. Resend once, only when the
		// marker is not in its transcript and it is not working on anything.
		text, _ := agentRead(name, 400)
		if !strings.Contains(text, promptMarker(name)) && !busy(name) && resultStatus(w.Result) == "" {
			debugf("prompt %s: delivery check failed; resend once", name)
			if err := send(); err != nil {
				return fmt.Errorf("prompt resend failed: %w", err)
			}
			if !delivered(name, w.Cwd, w.Result, timings.Delivery) {
				return fmt.Errorf("prompt was not delivered (%s). orch read %s to inspect", name, name)
			}
		}
	}
	if wait {
		// agent wait only observes state; it does not move focus.
		_, _ = herdrRun("agent", "wait", name, "--timeout", fmt.Sprint(timeoutMS))
	}
	return withRun(runID, func(r *Run) error {
		w := r.worker(name)
		w.Status = "prompted"
		w.Prompted = time.Now()
		w.ContractHash = hash
		r.event("prompt %s contract=%s", name, w.Contract)
		return nil
	})
}

// delivered reports whether the prompt reached the conversation: a result exists, its marker
// is visible in the transcript, or the agent stays "working" for several consecutive polls.
// A single "working" flash is not proof (a booting CLI can flash it and drop the input), and
// a transcript check alone fails for CLIs that redraw the screen (Codex's alternate screen).
func delivered(name, cwd, result string, within time.Duration) bool {
	marker := promptMarker(name)
	deadline := time.Now().Add(within)
	working := 0
	for time.Now().Before(deadline) {
		if acceptTrust(name, cwd) {
			return false // the prompt went into the dialog; the caller resends
		}
		if resultStatus(result) != "" {
			return true
		}
		if info, err := agentGet(name); err == nil && info.Status == "working" {
			if working++; working >= workingPollsForDelivery {
				return true
			}
		} else {
			working = 0
		}
		if text, err := agentRead(name, 400); err == nil && strings.Contains(text, marker) {
			return true
		}
		time.Sleep(timings.Poll)
	}
	return false
}

// busy reports whether the agent is working right now (never resend into a busy agent).
func busy(name string) bool {
	info, err := agentGet(name)
	return err == nil && (info.Status == "working" || info.Status == "blocked")
}

func isGitDir(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}

// retireName frees a worker name for a new spawn: the previous worker of that name becomes
// <name>~<HHMMSS>, and its result file moves along so the new worker never inherits a
// status line it did not write.
func retireName(r *Run, name string) error {
	suffix := time.Now().Format("150405")
	old := r.worker(name)
	if old != nil {
		suffix = old.Started.Format("150405")
	}
	src := r.resultPath(name)
	dst := r.resultPath(name + "~" + suffix)
	if _, err := os.Stat(src); err == nil {
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move previous result of %s: %w", name, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if old != nil {
		old.Name = name + "~" + suffix
		if old.Result == src {
			old.Result = dst
		}
	}
	return nil
}
