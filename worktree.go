package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Unverified marks a pane writer whose pane could not be checked (another backend).
type writerRef struct {
	Run, Head, Name, Kind, Cwd string
	Unverified                 bool
}

// paneAliveFn is the pane lookup used for writer liveness; tests replace it.
var paneAliveFn = paneExists

// paneLiveness answers "does this pane still exist" once per id for one scan.
// A failed lookup counts as live: a missed live writer is worse than a needless split.
type paneLiveness struct {
	seen    map[string]bool
	unknown map[string]bool
}

func (p *paneLiveness) alive(backend, id string) bool {
	key := backend + "\x00" + id
	if v, ok := p.seen[key]; ok {
		return v
	}
	exists, err := paneAliveFn(id)
	v := err != nil || exists
	p.seen[key] = v
	if p.unknown == nil {
		p.unknown = map[string]bool{}
	}
	p.unknown[key] = err != nil
	return v
}

// paneWriterLive protects surviving workers regardless of Head liveness.
// A run on another backend or a failed lookup stays live, unverified.
func (p *paneLiveness) paneWriterLive(r *Run, w *Worker) (live, unverified bool) {
	if runBackend(r) != currentBackend() {
		return true, true
	}
	id := w.Pane
	if runBackend(r) == "app" && w.Terminal != "" {
		id = w.Terminal
	}
	if !p.alive(runBackend(r), id) {
		return false, false
	}
	// Head death does not stop a worker or release its checkout.
	return true, p.unknown[runBackend(r)+"\x00"+id]
}

func recordTaskRunner(runID, id string, pid int) error {
	return withRun(runID, func(r *Run) error {
		x, err := readTask(runID, id)
		if err != nil {
			return err
		}
		if x.Status == "running" && x.Pid == 0 {
			x.Pid = pid
			if err := writeTask(x); err != nil {
				return err
			}
		}
		for i, old := range r.Tasks {
			if old.ID == id {
				r.Tasks[i] = x
				return nil
			}
		}
		return fmt.Errorf("task %s missing from run", id)
	})
}

func worktreePrompt(w *Worker) string {
	if w.Branch == "" {
		return ""
	}
	return fmt.Sprintf("\nCheckout isolation: work only in %s (branch %s). Source-checkout paths under %s in the contract refer to corresponding paths in this worktree. Do not edit the source checkout.", w.Worktree, w.Branch, w.WorktreeBase)
}

func workerScopeRepository(w *Worker, body string) (string, error) {
	if w.Branch == "" {
		return scopeRepository(w.Cwd, body)
	}
	root := canonicalRoot(w.Cwd)
	base := contractBase(body)
	if !isGitDir(root) || explicitBase(base) && !hasBaseCommit(root, base) {
		return "", fmt.Errorf("worktree cannot verify contract baseline: %s", root)
	}
	return root, nil
}

func workerScopeBase(w *Worker, body string) string {
	base := contractBase(body)
	if w.Branch != "" && !explicitBase(base) && w.WorktreeStart != "" {
		return w.WorktreeStart
	}
	if !explicitBase(base) {
		if w.StartHead != "" {
			return w.StartHead
		}
		if w.StartEmpty {
			return emptyTree
		}
	}
	return base
}

func workerOwnedGlobs(w *Worker, body string) []string {
	globs := ownedGlobs(body)
	if w.WorktreeBase == "" || w.Worktree == "" {
		return globs
	}
	for i, g := range globs {
		if filepath.IsAbs(g) {
			// Resolve the existing prefix without trying to stat wildcard segments.
			for prefix := g; ; prefix = filepath.Dir(prefix) {
				if real, err := filepath.EvalSymlinks(prefix); err == nil {
					rel, _ := filepath.Rel(prefix, g)
					g = filepath.Join(real, rel)
					break
				}
				if filepath.Dir(prefix) == prefix {
					break
				}
			}
		}
		if strings.HasPrefix(g, w.WorktreeBase+string(filepath.Separator)) {
			globs[i] = w.Worktree + strings.TrimPrefix(g, w.WorktreeBase)
		}
	}
	return globs
}

func canonicalRoot(cwd string) string {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	root, _ := filepath.Abs(repoRoot(cwd))
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return root
}

// Heartbeat and child group updates live in task.json, which may be newer than run.json.
func taskWriterLive(runID string, t *Task) (live, unknown bool) {
	if t.Status != "running" && t.Status != "pending" {
		return false, false
	}
	current, err := readTask(runID, t.ID)
	if err != nil {
		return true, true
	}
	return (current.Status == "running" || current.Status == "pending") && taskDead(current) == "", false
}

// Deliberately scan every Head/backend: checkout ownership crosses UI boundaries.
func liveWritersIn(root string, except func(writerRef) bool) []writerRef {
	runs, _ := allRuns()
	root = canonicalRoot(root)
	var out []writerRef
	liveness := &paneLiveness{seen: map[string]bool{}}
	add := func(ref writerRef) {
		if canonicalRoot(ref.Cwd) == root && (except == nil || !except(ref)) {
			out = append(out, ref)
		}
	}
	inRoot := func(cwd string) bool { return canonicalRoot(cwd) == root }
	for _, r := range runs {
		for _, w := range r.Workers {
			if (w.Role == "produce" || w.Role == "integrate") && w.Pane != "" && w.Status != "closed" && w.Status != "lost" {
				if !inRoot(w.Cwd) {
					continue
				}
				if live, unverified := liveness.paneWriterLive(r, w); live {
					add(writerRef{r.ID, r.HeadPane, w.Name, "spawn", w.Cwd, unverified})
				}
			}
		}
		for _, t := range r.Tasks {
			if t.Kind == "do" {
				if live, unknown := taskWriterLive(r.ID, t); live {
					add(writerRef{r.ID, r.HeadPane, t.ID, "do", t.Cwd, unknown})
				}
			}
		}
	}
	return out
}

func unverifiedMark(ref writerRef) string {
	if ref.Unverified {
		return " (unverified)"
	}
	return ""
}

func warnOtherHeads(r *Run, cwd string, out io.Writer) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if !isGitDir(cwd) {
		return
	}
	root := canonicalRoot(cwd)
	groups := map[string][]string{}
	unverified := map[string]bool{}
	for _, ref := range liveWritersIn(root, nil) {
		if ref.Head == r.HeadPane {
			continue
		}
		other, err := loadRun(ref.Run)
		if err == nil && other.Status == "active" {
			key := ref.Head + " run " + ref.Run
			groups[key] = append(groups[key], ref.Name)
			unverified[key] = unverified[key] || ref.Unverified
		}
	}
	var keys []string
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		note := ""
		if unverified[key] {
			note = " [unverified: writer liveness could not be confirmed]"
		}
		fmt.Fprintf(out, "orch: warning: Head %s is also writing in %s (%s)%s; writers here will use worktrees\n", key, root, strings.Join(groups[key], ","), note)
	}
}

func warnFirstWriter(r *Run, cwd string, out io.Writer) {
	for _, w := range r.Workers {
		if w.Role == "produce" || w.Role == "integrate" {
			return
		}
	}
	// A newly allocated do task already appears in the run.
	n := 0
	for _, t := range r.Tasks {
		if t.Kind == "do" {
			n++
		}
	}
	if n <= 1 {
		warnOtherHeads(r, cwd, out)
	}
}

func gitOutput(dir string, args ...string) (string, error) {
	argv := append([]string{"-C", dir}, args...)
	b, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

type worktreeState struct {
	Dir, Branch, Base, Cwd, Start string
	Auto                          bool
}

func spawnWorktree(r *Run, o spawnOpts, out io.Writer) (worktreeState, error) {
	// A replacement continues the old worker's partial edits, so it keeps the old placement.
	// An old worker that shared the checkout must not trigger a fresh auto worktree.
	if o.Fallback != "" && o.Worktree == "" {
		return worktreeState{}, nil
	}
	warnFirstWriter(r, o.Cwd, out)
	state, err := prepareWorktree(r.ID, o.Name, o.Cwd, o.Worktree, o.NoWorktree, out)
	if err != nil {
		return state, err
	}
	oldName := o.Name
	if o.Fallback != "" {
		oldName = o.Fallback
	}
	if old := r.worker(oldName); old != nil && old.Worktree == state.Dir && state.Dir != "" {
		state.Branch, state.Base, state.Auto, state.Start = old.Branch, old.WorktreeBase, old.WorktreeAuto, old.WorktreeStart
		if o.Fallback != "" {
			state.Cwd = old.Cwd
		}
	}
	return state, nil
}

func nodeWorktree(s string) string {
	if s == "none" {
		return ""
	}
	return s
}

// worktreeRootAllowed is swappable in tests: test processes may add worktrees only to
// throwaway repositories, never to the checkout the tests run from.
var worktreeRootAllowed = func(root string) bool { return true }

func prepareWorktree(runID, name, cwd, explicit string, disabled bool, out io.Writer) (worktreeState, error) {
	var state worktreeState
	if explicit != "" && disabled {
		return state, fmt.Errorf("--worktree and --no-worktree are mutually exclusive")
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	root := canonicalRoot(cwd)
	if explicit != "" {
		path, err := filepath.Abs(explicit)
		if err != nil {
			return state, err
		}
		state = worktreeState{Dir: path, Base: root}
		if _, err := os.Stat(path); err == nil {
			if !isGitDir(path) {
				return state, fmt.Errorf("worktree path is not a git checkout: %s", path)
			}
			// An existing directory is the user's own checkout: a plain cwd, as before worktrees
			// existed. Without a Branch orch never commits, merges or removes it.
			return state, nil
		} else if !os.IsNotExist(err) {
			return state, err
		}
	} else {
		if disabled || !isGitDir(cwd) || !worktreeRootAllowed(root) {
			return state, nil
		}
		writers := liveWritersIn(root, func(ref writerRef) bool { return ref.Run == runID && ref.Name == name })
		if len(writers) == 0 {
			return state, nil
		}
		suffix := runID
		if len(suffix) > 6 {
			suffix = suffix[len(suffix)-6:]
		}
		path := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-wt-"+suffix+"-"+name)
		candidate := path
		for i := 2; ; i++ {
			if _, err := os.Lstat(candidate); os.IsNotExist(err) {
				break
			} else if err != nil {
				return state, err
			}
			candidate = fmt.Sprintf("%s-%d", path, i)
		}
		state = worktreeState{Dir: candidate, Base: root, Auto: true}
		var names []string
		for _, ref := range writers {
			names = append(names, ref.Run+"/"+ref.Name+unverifiedMark(ref))
		}
		state.Branch = "orch/" + runID + "/" + name
		fmt.Fprintf(out, "orch: %s: checkout %s already has live writer(s) %s; using worktree %s (branch %s)\n", name, root, strings.Join(names, ","), state.Dir, state.Branch)
		if dirty, err := gitOutput(root, "status", "--porcelain"); err != nil {
			return state, err
		} else if dirty != "" {
			fmt.Fprintf(out, "orch: worktree starts from HEAD; uncommitted changes in %s are not included\n", root)
		}
	}
	if state.Branch == "" {
		state.Branch = "orch/" + runID + "-" + name
	}
	if !worktreeRootAllowed(root) {
		return state, fmt.Errorf("refusing to add a worktree to %s", root)
	}
	_, err := gitOutput(root, "worktree", "add", "-b", state.Branch, state.Dir, "HEAD")
	if err == nil && state.Auto {
		realCwd, _ := filepath.Abs(cwd)
		if real, e := filepath.EvalSymlinks(realCwd); e == nil {
			realCwd = real
		}
		if rel, e := filepath.Rel(root, realCwd); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			state.Cwd = filepath.Join(state.Dir, rel)
		}
	}
	if err == nil {
		state.Start, err = gitOutput(state.Dir, "rev-parse", "HEAD")
	}
	return state, err
}

func applyTaskWorktree(t *Task, state worktreeState) {
	if state.Dir == "" {
		return
	}
	t.Worktree, t.Branch, t.WorktreeBase, t.WorktreeAuto = state.Dir, state.Branch, state.Base, state.Auto
	t.WorktreeStart = state.Start
	t.Cwd = state.Dir
	if state.Cwd != "" {
		t.Cwd = state.Cwd
	}
}

func commitWorktree(dir, runID, name, summary string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("missing worktree directory")
	}
	if dirty, err := gitOutput(dir, "status", "--porcelain"); err != nil {
		return "", err
	} else if dirty != "" {
		if _, err := gitOutput(dir, "add", "-A"); err != nil {
			return "", err
		}
		if _, err := gitOutput(dir, "commit", "-m", commitSubject(summary), "-m", commitBody(runID, name)); err != nil {
			return "", err
		}
	}
	return gitOutput(dir, "rev-parse", "HEAD")
}

// entries adapts worker and task state without separate lifecycle implementations.
type worktreeEntry struct {
	Name                      string
	Dir, Branch, Base, Commit *string
	Auto, Committed, Merged   *bool
	Task                      *Task
	Busy                      bool
}

// Branch is set only for worktrees orch created with `git worktree add`, so it doubles
// as the ownership mark: entries without one are plain cwds that orch must never manage.
func worktreeEntries(r *Run) []worktreeEntry {
	var out []worktreeEntry
	for _, w := range r.Workers {
		if w.Branch != "" {
			out = append(out, worktreeEntry{w.Name, &w.Worktree, &w.Branch, &w.WorktreeBase, &w.Commit, &w.WorktreeAuto, &w.Committed, &w.Merged, nil, w.Pane != "" && w.Status != "closed" && w.Status != "lost"})
		}
	}
	for _, t := range r.Tasks {
		if t.Branch != "" {
			out = append(out, worktreeEntry{t.ID, &t.Worktree, &t.Branch, &t.WorktreeBase, &t.Commit, &t.WorktreeAuto, &t.Committed, &t.Merged, t, func() bool { live, _ := taskWriterLive(r.ID, t); return live }()})
		}
	}
	return out
}

func removeWorktree(e worktreeEntry, out io.Writer) error {
	if *e.Dir != "" {
		if len(liveWritersIn(*e.Dir, nil)) > 0 {
			return fmt.Errorf("worktree remains: %s (branch %s, writer still live)", *e.Dir, *e.Branch)
		}
		if _, err := gitOutput(*e.Base, "worktree", "remove", *e.Dir); err == nil {
			fmt.Fprintf(out, "removed: %s\n", *e.Dir)
		} else {
			_, statErr := os.Stat(*e.Dir)
			registered, listErr := gitOutput(*e.Base, "worktree", "list", "--porcelain")
			if !os.IsNotExist(statErr) || listErr != nil || strings.Contains(registered, "worktree "+*e.Dir+"\n") {
				return err
			}
		}
		*e.Dir = ""
	}
	if _, err := gitOutput(*e.Base, "show-ref", "--verify", "refs/heads/"+*e.Branch); err == nil {
		if _, err := gitOutput(*e.Base, "branch", "-d", *e.Branch); err != nil {
			return err
		}
	}
	return nil
}

func mergeOne(base, branch string) (string, error) {
	if _, err := gitOutput(base, "symbolic-ref", "--quiet", "HEAD"); err != nil {
		return "", fmt.Errorf("source checkout must have a current branch: %w", err)
	}
	if _, err := gitOutput(base, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
		return "", fmt.Errorf("merge already in progress in %s", base)
	}
	touched, err := gitOutput(base, "diff", "--name-only", "-z", "HEAD..."+branch)
	if err != nil {
		return "", err
	}
	dirty, err := gitOutput(base, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	set := map[string]bool{}
	for _, p := range strings.Split(touched, "\x00") {
		if p != "" {
			set[p] = true
		}
	}
	var overlap []string
	parts := strings.Split(dirty, "\x00")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if len(p) < 4 {
			continue
		}
		if set[p[3:]] {
			overlap = append(overlap, p[3:])
		}
		if p[0] == 'R' || p[0] == 'C' || p[1] == 'R' || p[1] == 'C' {
			i++
			if i < len(parts) && set[parts[i]] {
				overlap = append(overlap, parts[i])
			}
		}
	}
	if len(overlap) > 0 {
		return "", fmt.Errorf("dirty files overlap branch %s: %s", branch, strings.Join(overlap, ", "))
	}
	if _, err := gitOutput(base, "merge", "--no-ff", "-m", "chore: merge "+branch, branch); err != nil {
		if _, active := gitOutput(base, "rev-parse", "--verify", "MERGE_HEAD"); active == nil {
			if _, abortErr := gitOutput(base, "merge", "--abort"); abortErr != nil {
				return "", fmt.Errorf("branch %s conflicted: %v; abort failed: %w", branch, err, abortErr)
			}
		}
		return "", fmt.Errorf("branch %s merge failed (worktree kept): %w", branch, err)
	}
	return gitOutput(base, "rev-parse", "HEAD")
}

func mergeWorktrees(runID string, names []string, test string, keep, force bool, out, stderr io.Writer) error {
	r, err := loadRun(runID)
	if err != nil {
		return err
	}
	if err := ownRun(r); err != nil {
		return err
	}
	// Serialize integrations with spawn/do admission across Heads.
	unlock, err := lockHead("writers")
	if err != nil {
		return err
	}
	defer unlock()
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	var selected []worktreeEntry
	for _, e := range worktreeEntries(r) {
		if *e.Branch != "" && *e.Committed && (len(names) == 0 || wanted[e.Name]) {
			if e.Busy {
				return fmt.Errorf("%s: writer is still live; collect it before merging", e.Name)
			}
			if *e.Base == "" {
				return fmt.Errorf("%s: missing source checkout", e.Name)
			}
			selected = append(selected, e)
			delete(wanted, e.Name)
		}
	}
	if len(wanted) > 0 {
		return fmt.Errorf("no committed worktree for: %s", strings.Join(names, ", "))
	}
	if len(selected) == 0 {
		return fmt.Errorf("no committed worktrees to merge")
	}
	if !force {
		if err := refuseLiveWriters(runID, selected); err != nil {
			return err
		}
	}
	for _, e := range selected {
		// A retained branch may need integrating again after the source switches branches.
		_, ancestorErr := gitOutput(*e.Base, "merge-base", "--is-ancestor", *e.Branch, "HEAD")
		if !*e.Merged || *e.Dir != "" && ancestorErr != nil {
			hash, err := mergeOne(*e.Base, *e.Branch)
			if err != nil {
				return err
			}
			baseBranch, err := gitOutput(*e.Base, "symbolic-ref", "--short", "HEAD")
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "merged: %s -> %s (%s)\n", *e.Branch, baseBranch, hash)
			if err := withRun(runID, func(current *Run) error {
				current.event("merged %s into %s (%s)", *e.Branch, *e.Base, hash)
				return nil
			}); err != nil {
				return err
			}
		}
	}
	if test != "" {
		seen := map[string]bool{}
		for _, e := range selected {
			if seen[*e.Base] {
				continue
			}
			seen[*e.Base] = true
			if testCommandForRun(runID, test, *e.Base, 40, 1800000, false, out, stderr) != 0 {
				return fmt.Errorf("integration test failed; all worktrees kept")
			}
		}
	}
	for _, e := range selected {
		*e.Merged = true
		if err := updateWorktreeEntry(runID, e); err != nil {
			return err
		}
	}
	if !keep {
		for _, e := range selected {
			err := removeWorktree(e, out)
			if saveErr := updateWorktreeEntry(runID, e); saveErr != nil {
				return saveErr
			}
			if err != nil {
				return fmt.Errorf("cleanup kept %s: %w", e.Name, err)
			}
		}
	}
	return nil
}

// refuseLiveWriters stops a merge from moving HEAD under a writer still working in the
// source checkout: that writer's scope check would see the merged files as its own changes.
func refuseLiveWriters(runID string, selected []worktreeEntry) error {
	mine := map[string]bool{}
	for _, e := range selected {
		mine[e.Name] = true
	}
	for _, e := range selected {
		writers := liveWritersIn(*e.Base, func(ref writerRef) bool { return ref.Run == runID && mine[ref.Name] })
		if len(writers) == 0 {
			continue
		}
		var names []string
		for _, ref := range writers {
			names = append(names, ref.Run+"/"+ref.Name+unverifiedMark(ref))
		}
		return fmt.Errorf("%s: source checkout %s has live writer(s) %s; collect them first or pass --force", e.Name, *e.Base, strings.Join(names, ","))
	}
	return nil
}

// Persist only the integration fields: orch test may have appended another task meanwhile.
func updateWorktreeEntry(runID string, source worktreeEntry) error {
	return withRun(runID, func(r *Run) error {
		for _, e := range worktreeEntries(r) {
			if e.Name == source.Name {
				*e.Dir, *e.Merged = *source.Dir, *source.Merged
				r.event("worktree %s merged=%t path=%s", e.Name, *e.Merged, *e.Dir)
				if e.Task != nil {
					return writeTask(e.Task)
				}
				return nil
			}
		}
		return fmt.Errorf("worktree entry %s disappeared", source.Name)
	})
}

func cleanupRunWorktrees(r *Run, gcMode bool) []string {
	notes, _ := cleanupRunWorktreesChecked(r, gcMode)
	return notes
}
func cleanupRunWorktreesChecked(r *Run, gcMode bool) ([]string, error) {
	var cleanupErr error
	unlock, err := lockHead("writers")
	if err != nil {
		return []string{err.Error()}, err
	}
	defer unlock()
	var report []string
	for _, e := range worktreeEntries(r) {
		if *e.Dir == "" && !*e.Merged {
			continue
		}
		if *e.Dir == "" {
			if _, err := gitOutput(*e.Base, "show-ref", "--verify", "refs/heads/"+*e.Branch); err != nil {
				continue
			}
		}
		if e.Busy {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("worktree cleanup deferred: %s writer live or unverified; inspect runs/%s task.json and process/pane liveness, restore missing/corrupt task state from backup, then retry run end; do not remove the worktree until liveness is confirmed", *e.Dir, r.ID))
			report = append(report, fmt.Sprintf("worktree remains: %s (branch %s, writer still live)", *e.Dir, *e.Branch))
			continue
		}
		cleanup := *e.Merged
		if gcMode {
			if !*e.Auto {
				continue
			}
			_, err := gitOutput(*e.Base, "merge-base", "--is-ancestor", *e.Branch, "HEAD")
			cleanup = err == nil
		}
		if cleanup {
			_, statErr := os.Stat(*e.Dir)
			if statErr != nil && !os.IsNotExist(statErr) {
				cleanupErr = errors.Join(cleanupErr, statErr)
				report = append(report, statErr.Error())
				continue
			}
			if dirty, err := gitOutput(*e.Dir, "status", "--porcelain"); statErr == nil && (err != nil || dirty != "") {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("worktree cleanup deferred: %s is dirty or unavailable", *e.Dir))
				report = append(report, fmt.Sprintf("worktree remains: %s (branch %s, dirty or unavailable)", *e.Dir, *e.Branch))
				continue
			}
			var b strings.Builder
			if err := removeWorktree(e, &b); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
				report = append(report, err.Error())
			} else {
				*e.Merged = true
				if b.Len() > 0 {
					report = append(report, strings.TrimSpace(b.String()))
				}
			}
			if err := updateWorktreeEntry(r.ID, e); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
				report = append(report, err.Error())
			}
		} else {
			report = append(report, fmt.Sprintf("worktree remains: %s (branch %s, not merged). orch merge %s or git worktree remove", *e.Dir, *e.Branch, e.Name))
		}
	}
	return report, cleanupErr
}
