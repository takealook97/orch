package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// A DAG (design §11.3) lives in <run>/dag.yaml. Each node is one contract; needs are the
// result files it waits for. Reviews imply needs. A node starts only when every need is done.
// A node with run is a model-less mechanical gate: orch runs the command itself. A node with
// pane: false runs its contract through the read-only one-off task path (orch ask) instead
// of a pane, so it takes no row. Neither can be reviewed.
type DagNode struct {
	Serves       []int    `yaml:"serves"`
	FixOf        string   `yaml:"fix_of"`
	Name         string   `yaml:"name"`
	Model        string   `yaml:"model"`
	Run          string   `yaml:"run"`
	Pane         *bool    `yaml:"pane"`
	Role         string   `yaml:"role"`
	Purpose      string   `yaml:"purpose"`
	Contract     string   `yaml:"contract"` // relative to the run dir; default contracts/<name>.md
	Needs        []string `yaml:"needs"`
	Reviews      []string `yaml:"reviews"`
	AuthorFamily []string `yaml:"author_family"`
	Cwd          string   `yaml:"cwd"`
	Worktree     string   `yaml:"worktree"`
	Account      string   `yaml:"account"`
}

type Dag struct {
	Nodes []DagNode `yaml:"nodes"`
}

// inline reports whether orch runs the node itself instead of spawning a pane.
func (n DagNode) inline() bool { return n.Run != "" || n.Pane != nil && !*n.Pane }

// dir is where the node runs: the worktree, else cwd, else the Head's directory.
func (n DagNode) dir() string {
	d := n.Cwd
	if n.Worktree != "" {
		d = n.Worktree
	}
	if d == "" {
		d, _ = os.Getwd()
	}
	d, _ = filepath.Abs(d)
	if real, err := filepath.EvalSymlinks(d); err == nil {
		d = real
	}
	return d
}

// Node states: pending, running, done, partial, blocked, failed, skipped, attention.
type dagNodeState struct {
	Status  string    `json:"status"`
	Worker  string    `json:"worker,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Started time.Time `json:"started,omitempty"`
	Ended   time.Time `json:"ended,omitempty"`
}

type dagState struct {
	Nodes map[string]*dagNodeState `json:"nodes"`
}

func loadDag(r *Run) (Dag, error) {
	var d Dag
	data, err := os.ReadFile(filepath.Join(r.dir(), "dag.yaml"))
	if err != nil {
		return d, fmt.Errorf("dag.yaml missing: %w", err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("dag.yaml: %w", err)
	}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Contract == "" && n.Run == "" {
			n.Contract = filepath.Join("contracts", n.Name+".md")
		}
		for _, rv := range n.Reviews {
			if !slices.Contains(n.Needs, rv) {
				n.Needs = append(n.Needs, rv)
			}
		}
	}
	return d, nil
}

func (d Dag) node(name string) *DagNode {
	for i := range d.Nodes {
		if d.Nodes[i].Name == name {
			return &d.Nodes[i]
		}
	}
	return nil
}

// validateDag checks names, models, roles, contracts, references, cycles and the lane
// constraints that can be decided statically from the reviewed nodes' models.
func validateDag(r *Run, d Dag) []string {
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if len(d.Nodes) == 0 {
		bad("no nodes")
	}
	seen := map[string]bool{}
	for _, n := range d.Nodes {
		if !nameRE.MatchString(n.Name) {
			bad("%s: invalid name format", n.Name)
		}
		if seen[n.Name] {
			bad("%s: duplicate name", n.Name)
		}
		seen[n.Name] = true
	}
	for _, n := range d.Nodes {
		for _, rv := range n.Reviews {
			if t := d.node(rv); t != nil && t.inline() {
				bad("%s: reviews %s, which has no pane worker (run or pane: false)", n.Name, rv)
			}
		}
		if n.Run != "" {
			if n.Model != "" || n.Contract != "" {
				bad("%s: a run node takes no model or contract", n.Name)
			}
			if n.Role != roleMech {
				bad("%s: a run node must have role %s", n.Name, roleMech)
			}
			if n.Worktree != "" && n.Cwd != "" {
				bad("%s: set cwd or worktree, not both", n.Name)
			}
			for _, need := range n.Needs {
				if !seen[need] {
					bad("%s: depends on missing node %q", n.Name, need)
				}
			}
			continue
		}
		m, err := modelByKey(n.Model)
		if err != nil {
			bad("%s: %v", n.Name, err)
			continue
		}
		if n.inline() && n.Role != roleRecon && !isVerifyRole(n.Role) {
			bad("%s: pane: false is read-only; only recon and verify-* nodes can use it", n.Name)
		}
		if !slices.Contains(roles, n.Role) {
			bad("%s: unknown role %q", n.Name, n.Role)
		}
		if n.Purpose != "" {
			if _, ok := chains[n.Purpose]; !ok {
				bad("%s: unknown purpose %q", n.Name, n.Purpose)
			}
		}
		body, err := os.ReadFile(filepath.Join(r.dir(), n.Contract))
		if err != nil {
			bad("%s: contract missing %s", n.Name, n.Contract)
		} else if err := validateContract(string(body)); err != nil {
			bad("%s: %v", n.Name, err)
		}
		for _, need := range n.Needs {
			if !seen[need] {
				bad("%s: depends on missing node %q", n.Name, need)
			}
		}
		var authors, critics []string
		for _, rv := range n.Reviews {
			if t := d.node(rv); t != nil {
				if tm, err := modelByKey(t.Model); err == nil && !slices.Contains(authors, tm.Family) {
					authors = append(authors, tm.Family)
				}
			}
		}
		for _, f := range n.AuthorFamily {
			fam, err := resolveFamily(f) // "head" is resolved exactly as dag run will
			if err != nil {
				bad("%s: author_family %q: %v", n.Name, f, err)
				continue
			}
			if !slices.Contains(authors, fam) {
				authors = append(authors, fam)
			}
		}
		for _, o := range d.Nodes {
			if o.Role != roleCritical {
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
		if isVerifyRole(n.Role) && n.Role != roleMech && len(n.Reviews) == 0 && len(n.AuthorFamily) == 0 {
			bad("%s: %s node requires reviews or author_family", n.Name, n.Role)
		} else if len(authors) > 0 {
			if _, err := checkLane(n.Role, m, authors, critics); err != nil {
				bad("%s: %v", n.Name, err)
			}
		}
	}
	if cyc := dagCycle(d); cyc != "" {
		bad("dependency cycle: %s", cyc)
	}
	return errs
}

func dagCycle(d Dag) string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var found string
	var visit func(string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		if node := d.node(n); node != nil {
			for _, m := range node.Needs {
				if color[m] == grey {
					i := slices.Index(stack, m)
					found = strings.Join(append(slices.Clone(stack[i:]), m), " → ")
					return true
				}
				if color[m] == white && visit(m) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, n := range d.Nodes {
		if color[n.Name] == white && visit(n.Name) {
			return found
		}
	}
	return ""
}

// criticalLength is the longest chain of dependents starting at a node (itself included).
func criticalLength(d Dag) map[string]int {
	children := map[string][]string{}
	for _, n := range d.Nodes {
		for _, need := range n.Needs {
			children[need] = append(children[need], n.Name)
		}
	}
	memo := map[string]int{}
	var depth func(string) int
	depth = func(n string) int {
		if v, ok := memo[n]; ok {
			return v
		}
		best := 0
		for _, c := range children[n] {
			best = max(best, depth(c))
		}
		memo[n] = best + 1
		return memo[n]
	}
	for _, n := range d.Nodes {
		depth(n.Name)
	}
	return memo
}

func dagStatePath(r *Run) string { return filepath.Join(r.dir(), "dag-state.json") }

func loadDagState(r *Run, d Dag) *dagState {
	st := &dagState{Nodes: map[string]*dagNodeState{}}
	if data, err := os.ReadFile(dagStatePath(r)); err == nil {
		_ = json.Unmarshal(data, st)
	}
	for _, n := range d.Nodes {
		if st.Nodes[n.Name] == nil {
			st.Nodes[n.Name] = &dagNodeState{Status: "pending"}
		}
	}
	return st
}

func saveDagState(r *Run, st *dagState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dagStatePath(r), append(data, '\n'), 0600)
}

func finalStatus(s string) bool {
	switch s {
	case "done", "partial", "blocked", "failed", "skipped", "attention":
		return true
	}
	return false
}

// dagReady returns pending nodes whose needs are all done, longest critical path first,
// and marks nodes skipped when a need ended in anything but done.
func dagReady(d Dag, st *dagState) []string {
	crit := criticalLength(d)
	var ready []string
	changed := true
	for changed {
		changed = false
		for _, n := range d.Nodes {
			ns := st.Nodes[n.Name]
			if ns.Status != "pending" {
				continue
			}
			for _, need := range n.Needs {
				if s := st.Nodes[need].Status; finalStatus(s) && s != "done" {
					ns.Status, ns.Detail = "skipped", fmt.Sprintf("dependency node %s is %s", need, s)
					changed = true
					break
				}
			}
		}
	}
	for _, n := range d.Nodes {
		if st.Nodes[n.Name].Status != "pending" {
			continue
		}
		ok := true
		for _, need := range n.Needs {
			if st.Nodes[need].Status != "done" {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, n.Name)
		}
	}
	sort.SliceStable(ready, func(i, j int) bool { return crit[ready[i]] > crit[ready[j]] })
	return ready
}

// dagRun schedules the DAG until every node is final or nothing can progress.
func dagRun(runFlag string, timeout time.Duration, out io.Writer) (*dagState, error) {
	runID, err := resolveRun(runFlag)
	if err != nil {
		return nil, err
	}
	r, err := loadRun(runID)
	if err != nil {
		return nil, err
	}
	d, err := loadDag(r)
	if err != nil {
		return nil, err
	}
	if errs := validateDag(r, d); len(errs) > 0 {
		return nil, fmt.Errorf("DAG check failed:\n  %s", strings.Join(errs, "\n  "))
	}
	st := loadDagState(r, d)
	// Nodes left "running" by an interrupted runner are re-attached if their worker is known.
	// Inline nodes died with that runner, so they start over.
	for _, n := range d.Nodes {
		if ns := st.Nodes[n.Name]; ns.Status == "running" && n.inline() {
			ns.Status, ns.Worker, ns.Detail = "pending", "", ""
		}
	}
	deadline := time.Now().Add(timeout)
	inlineDone := make(chan inlineResult, len(d.Nodes))
	inflight := 0
	interrupted := false
	record := func(res inlineResult) {
		inflight--
		ns := st.Nodes[res.node]
		if res.worker != "" {
			ns.Worker = res.worker
		}
		if res.interrupted {
			// Not a verdict: the node starts over when the DAG run is resumed.
			interrupted = true
			ns.Status, ns.Detail = "pending", "interrupted"
			fmt.Fprintf(out, "%s\tinterrupted\n", res.node)
			return
		}
		if err := mergeKnowledge(r.ID, res.node, r.resultPath(res.node)); err != nil {
			fmt.Fprintf(out, "knowledge merge %s: %v\n", res.node, err)
		}
		ns.Status, ns.Detail, ns.Ended = res.status, res.detail, time.Now()
		fmt.Fprintf(out, "%s\t%s\t%s\n", res.node, res.status, res.detail)
	}
	drain := func() {
		for {
			select {
			case res := <-inlineDone:
				record(res)
			default:
				return
			}
		}
	}
	// collect polls running nodes once (or up to wait) and records finished ones, so a
	// finished worker's pane is closed promptly even while other nodes are being started.
	collect := func(wait time.Duration) error {
		var running []string
		byWorker := map[string]string{}
		for _, n := range d.Nodes {
			if ns := st.Nodes[n.Name]; ns.Status == "running" && !n.inline() {
				running = append(running, ns.Worker)
				byWorker[ns.Worker] = n.Name
			}
		}
		if len(running) == 0 {
			if inflight > 0 {
				select {
				case res := <-inlineDone:
					record(res)
				case <-time.After(wait):
				}
			}
			drain()
			return saveDagState(r, st)
		}
		if inflight > 0 {
			// An inline gate that finishes must not wait out a long pane poll.
			wait = min(wait, timings.DagPoll)
		}
		outs, err := waitWorkers(runID, running, wait, true)
		if err != nil {
			return err
		}
		drain()
		for _, o := range outs {
			node := byWorker[o.Name]
			if node == "" {
				continue
			}
			ns := st.Nodes[node]
			switch {
			case o.Status == "timeout":
			case strings.HasPrefix(o.Status, "fallback:"):
				ns.Worker = strings.TrimPrefix(o.Status, "fallback:")
				byWorker[ns.Worker] = node
				fmt.Fprintf(out, "%s\tfallback\t%s\n", node, o.Detail)
			case o.Status == "done" || o.Status == "partial" || o.Status == "blocked":
				ns.Status, ns.Detail, ns.Ended = o.Status, o.Detail, time.Now()
				fmt.Fprintf(out, "%s\t%s\t%s\n", node, o.Status, o.Detail)
			default: // agent-blocked, idle-no-result, close-failed, quota: the Head decides
				ns.Status, ns.Detail, ns.Ended = "attention", o.Status+": "+o.Detail, time.Now()
				fmt.Fprintf(out, "%s\tattention\t%s %s\n", node, o.Status, o.Detail)
			}
		}
		return saveDagState(r, st)
	}
	for {
		if interrupted {
			for inflight > 0 {
				record(<-inlineDone)
			}
			_ = saveDagState(r, st)
			return st, errors.New("DAG run interrupted; run orch dag run again to resume")
		}
		full := false
		for _, name := range dagReady(d, st) {
			n := d.node(name)
			if n.inline() {
				if err := startInline(runID, d, st, n, time.Until(deadline), inlineDone); err != nil {
					st.Nodes[name].Status, st.Nodes[name].Detail = "failed", err.Error()
					fmt.Fprintf(out, "%s\tfailed\t%v\n", name, err)
					continue
				}
				inflight++
				st.Nodes[name].Status, st.Nodes[name].Started = "running", time.Now()
				what := "run"
				if n.Run == "" {
					what = n.Model + ", no pane"
				}
				fmt.Fprintf(out, "%s\tstarted\t%s (%s)\n", name, st.Nodes[name].Worker, what)
				continue
			}
			if full {
				continue // no row; inline nodes behind it still start
			}
			w, err := spawn(spawnOpts{
				Serves: n.Serves, FixOf: n.FixOf, Run: runID, Key: n.Model, Name: n.Name, Role: n.Role, Purpose: n.Purpose,
				Reviews: currentWorkers(st, n.Reviews), AuthorFamilies: n.AuthorFamily,
				Cwd: n.Cwd, Worktree: n.Worktree, Account: n.Account,
			})
			if err != nil {
				if strings.Contains(err.Error(), "is full") {
					full = true // wait for a free row; critical path first on the next round
					continue
				}
				st.Nodes[name].Status, st.Nodes[name].Detail = "failed", err.Error()
				fmt.Fprintf(out, "%s\tfailed\tspawn: %v\n", name, err)
				continue
			}
			if err := prompt(runID, w.Name, filepath.Join(r.dir(), n.Contract), false, 0); err != nil {
				_ = closeWorker(runID, w.Name)
				st.Nodes[name].Status, st.Nodes[name].Detail = "failed", err.Error()
				fmt.Fprintf(out, "%s\tfailed\tprompt: %v\n", name, err)
				continue
			}
			st.Nodes[name].Status, st.Nodes[name].Worker, st.Nodes[name].Started = "running", w.Name, time.Now()
			fmt.Fprintf(out, "%s\tstarted\t%s (%s)\n", name, w.Name, n.Model)
			if err := collect(timings.DagStartWait); err != nil {
				return st, err
			}
		}
		_ = saveDagState(r, st)

		var running []string
		for _, n := range d.Nodes {
			if ns := st.Nodes[n.Name]; ns.Status == "running" {
				running = append(running, n.Name)
			}
		}
		if len(running) == 0 {
			if len(dagReady(d, st)) == 0 {
				break
			}
			// Ready nodes exist but no row is free (panes of other runs of this Head).
			if time.Now().After(deadline) {
				_ = saveDagState(r, st)
				return st, fmt.Errorf("DAG timed out: no row became free")
			}
			time.Sleep(timings.DagPoll)
			continue
		}
		if time.Now().After(deadline) {
			_ = saveDagState(r, st)
			return st, fmt.Errorf("DAG timed out with nodes running %s", strings.Join(running, ", "))
		}
		if err := collect(timings.DagWait); err != nil {
			return st, err
		}
		_ = saveDagState(r, st)
	}
	_ = withRun(runID, func(r *Run) error {
		r.event("DAG run finished: %s", dagSummary(d, st))
		return nil
	})
	return st, nil
}

func currentWorkers(st *dagState, nodes []string) []string {
	var out []string
	for _, n := range nodes {
		if s := st.Nodes[n]; s != nil && s.Worker != "" {
			out = append(out, s.Worker)
		} else {
			out = append(out, n)
		}
	}
	return out
}

func dagSummary(d Dag, st *dagState) string {
	count := map[string]int{}
	for _, n := range d.Nodes {
		count[st.Nodes[n.Name].Status]++
	}
	var parts []string
	for _, k := range []string{"done", "partial", "blocked", "failed", "skipped", "attention", "running", "pending"} {
		if count[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, count[k]))
		}
	}
	return strings.Join(parts, " ")
}

func dagAllDone(d Dag, st *dagState) bool {
	for _, n := range d.Nodes {
		if st.Nodes[n.Name].Status != "done" {
			return false
		}
	}
	return true
}

var errDagIncomplete = errors.New("not all DAG nodes finished as done")

// References supplement the message, leaving contract hashes stable.
func promptReferences(r *Run, name, role string) string {
	var lines []string
	if !strings.HasPrefix(role, "verify-") {
		lines = append(lines, "Run knowledge (read first, reuse instead of re-deriving): "+filepath.Join(r.dir(), "knowledge.md"))
	}
	if d, err := loadDag(r); err == nil {
		if n := d.node(name); n != nil {
			var paths []string
			for _, need := range n.Needs {
				path := r.resultPath(need)
				if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
					paths = append(paths, path)
				}
			}
			if len(paths) > 0 {
				lines = append(lines, "Upstream results: "+strings.Join(paths, ", "))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n" + strings.Join(lines, "\n")
}
