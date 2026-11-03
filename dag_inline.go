package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// inlineResult is how a node that orch ran itself (run, or pane: false) ended.
// interrupted means orch was signalled: the node is not final and the DAG run stops.
type inlineResult struct {
	node, status, detail, worker string
	interrupted                  bool
}

// startInline launches a run or pane: false node in the background and reports on done.
// Checks that need the run (ownership, lane families) happen before it returns.
func startInline(runID string, d Dag, st *dagState, n *DagNode, budget time.Duration, done chan<- inlineResult) error {
	if budget <= 0 {
		return fmt.Errorf("no time left before the DAG deadline")
	}
	// Reload: workers spawned since the DAG run started are only in the saved run.
	r, err := loadRun(runID)
	if err != nil {
		return err
	}
	if err := ownRun(r); err != nil {
		return fmt.Errorf("cannot run a node in another Head run: %w", err)
	}
	if r.Status != "active" {
		return fmt.Errorf("run %s is closed", runID)
	}
	result := r.resultPath(n.Name)
	if n.Run != "" {
		st.Nodes[n.Name].Worker = "run"
		go func() { done <- runGate(r, *n, result, budget) }()
		return nil
	}
	m, err := modelByKey(n.Model)
	if err != nil {
		return err
	}
	authors, critics, err := inlineLane(r, d, st, n, m)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(r.dir(), n.Contract))
	if err != nil {
		return err
	}
	prompt := inlinePrompt(r, n, string(body))
	purpose := n.Purpose
	if purpose == "" {
		if _, ok := chains[n.Role]; ok {
			purpose = n.Role
		}
	}
	deadline := time.Now().Add(budget)
	start := func(key string) (*Task, error) {
		t, err := newTaskIn(r.ID, "ask", key, prompt, n.dir())
		if err != nil {
			return nil, err
		}
		t.TimeoutMS = int(time.Until(deadline) / time.Millisecond)
		t.MaxOutput = 2000
		t.Account = n.Account
		if t.Account == "" {
			t.Account = "same"
		}
		return t, saveTask(t)
	}
	t, err := start(m.Key)
	if err != nil {
		return err
	}
	st.Nodes[n.Name].Worker = t.ID
	go func() {
		// Capacity exhaustion moves to the next key of the purpose chain that still passes
		// the lane rules, as a pane worker's fallback would.
		tried := []string{m.Key}
		for {
			res := runInlineAsk(t, n.Name, result)
			if !res.quota || purpose == "" || time.Until(deadline) <= 0 {
				done <- res.inlineResult
				return
			}
			if res.sameKindSpent {
				tried = withoutKind(purpose, inlineRanKind(t.Key, m.Kind), tried)
			}
			skip := tried
			next, ok := nextFallback(purpose, t.Key, n.Role, authors, critics, skip)
			if !ok {
				done <- res.inlineResult
				return
			}
			tried = append(tried, next.Key)
			nt, err := start(next.Key)
			if err != nil {
				done <- inlineResult{node: n.Name, status: "failed", detail: "fallback: " + err.Error(), worker: t.ID}
				return
			}
			t = nt
		}
	}()
	return nil
}

// inlineRanKind is the CLI kind of the key that just ran, or the node's kind if it is unknown.
func inlineRanKind(key, fallbackKind string) string {
	if m, err := modelByKey(key); err == nil {
		return m.Kind
	}
	return fallbackKind
}

// inlineLane applies the lane family rules spawn would, using the workers the node reviews,
// and returns the author and critic families for fallback.
func inlineLane(r *Run, d Dag, st *dagState, n *DagNode, m Model) (authors, critics []string, err error) {
	if !isVerifyRole(n.Role) {
		return nil, nil, nil
	}
	authors, err = familiesOf(r, currentWorkers(st, n.Reviews))
	if err != nil {
		return nil, nil, err
	}
	for _, f := range n.AuthorFamily {
		fam, err := resolveFamily(f)
		if err != nil {
			return nil, nil, err
		}
		if !slices.Contains(authors, fam) {
			authors = append(authors, fam)
		}
	}
	critics = criticsOf(r, currentWorkers(st, n.Reviews))
	for _, o := range d.Nodes {
		if o.Role != roleCritical || o.Name == n.Name || st.Nodes[o.Name].Status != "done" {
			continue
		}
		for _, rv := range o.Reviews {
			if slices.Contains(n.Reviews, rv) {
				if om, err := modelByKey(o.Model); err == nil && !slices.Contains(critics, om.Family) {
					critics = append(critics, om.Family)
				}
			}
		}
	}
	warnings, err := checkLane(n.Role, m, authors, critics)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "orch: warning:", w)
	}
	return authors, critics, err
}

// runGate runs a mechanical gate command and writes its summary as the node's result file.
func runGate(r *Run, n DagNode, result string, budget time.Duration) inlineResult {
	cwd := n.dir()
	log := filepath.Join(r.dir(), "evidence", n.Name+".log")
	tr, err := executeTest(n.Run, cwd, 40, int(budget/time.Millisecond), log)
	if tr.Interrupted {
		return inlineResult{node: n.Name, detail: "interrupted", worker: "run", interrupted: true}
	}
	status := testStatus(tr, err)
	var b strings.Builder
	fmt.Fprintf(&b, "# Result: %s\n- command: %s\n- cwd: %s\n- duration: %d ms\n", n.Name, n.Run, cwd, tr.DurationMS)
	writeTest(&b, "- ", tr)
	if err != nil {
		fmt.Fprintf(&b, "- error: %v\n", err)
	}
	detail := fmt.Sprintf("exit %d in %d ms", tr.ExitCode, tr.DurationMS)
	switch {
	case status == "done":
		b.WriteString("\nstatus: done\n")
	case tr.TimedOut:
		detail = "timed out after " + detail
		b.WriteString("\nstatus: blocked\n")
	default:
		if len(tr.Failures) > 0 {
			detail += ": " + strings.Join(tr.Failures, ", ")
		}
		b.WriteString("\nstatus: blocked\n")
	}
	if werr := os.WriteFile(result, []byte(b.String()), 0600); werr != nil {
		return inlineResult{node: n.Name, status: "failed", detail: "result file: " + werr.Error(), worker: "run"}
	}
	if status != "done" {
		return inlineResult{node: n.Name, status: "failed", detail: detail, worker: "run"}
	}
	return inlineResult{node: n.Name, status: "done", detail: detail, worker: "run"}
}

// askResult is a finished pane: false task; quota marks a capacity failure worth a fallback.
type askResult struct {
	inlineResult
	quota bool
	// sameKindSpent: the cr wrapper reported every account of this CLI exhausted.
	sameKindSpent bool
}

// runInlineAsk runs a pane: false node's task and stores its answer as the result file.
func runInlineAsk(t *Task, node, result string) askResult {
	end := func(status, detail string) askResult {
		return askResult{inlineResult: inlineResult{node: node, status: status, detail: detail, worker: t.ID}}
	}
	if err := runTask(t); err != nil {
		return end("failed", fmt.Sprintf("%s: %v", t.ID, err))
	}
	if t.Note == "interrupted" {
		r := end("", "interrupted")
		r.interrupted = true
		return r
	}
	if t.Status != "done" {
		r := end("failed", fmt.Sprintf("%s (%s) ended %s (exit %d)", t.ID, t.Key, t.Status, t.ExitCode))
		if out, err := os.ReadFile(filepath.Join(t.Dir, "output.log")); err == nil && (quotaText(string(out)) || capacityText(string(out))) {
			r.quota = true
			r.sameKindSpent = crExhausted(string(out))
		}
		return r
	}
	answer, err := os.ReadFile(t.AnswerPath)
	if err != nil {
		return end("failed", fmt.Sprintf("%s: %v", t.ID, err))
	}
	if err := os.WriteFile(result, answer, 0600); err != nil {
		return end("failed", "result file: "+err.Error())
	}
	status := resultStatus(result)
	if status == "" {
		return end("attention", t.ID+": answer has no status line")
	}
	return end(status, t.ID+" ("+t.Key+")")
}

func inlinePrompt(r *Run, n *DagNode, body string) string {
	return fmt.Sprintf("This DAG node runs without a pane and with read-only tools. Do not write any file. "+
		"Your final message is saved verbatim as the result file %s, so give the full result in the format the "+
		"contract asks for and end it with the status: line.%s\n\n%s", r.resultPath(n.Name), promptReferences(r, n.Name, n.Role), body)
}
