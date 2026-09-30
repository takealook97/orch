package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

const usage = `orch — Herdr multi-agent runner (Head only; workers are denied)

Run management
  orch run new [--request FILE|-] [--text "..."] [--mode direct|light|full|full+] [--playbook T] [--goal ID]
  orch run mode <mode> --reason "..." [--run ID]
  orch run end [--run ID]                 close all worker panes, confirm removal, end run
  orch run list [--all]                   --all also shows ad-hoc runs of one-off tasks
  orch status [--run ID]

Delegation
  orch ask --model K [--cwd DIR] [--max-output N] [--timeout MS] [--account same|hook] [--background] [--json] "question"
  orch do --model K --own GLOBS [--test CMD] [--worktree DIR] [--cwd DIR] [--background] [--timeout MS] [--account same|hook] [--json] "requirement"
  orch test [--cwd DIR] [--max-lines N] [--timeout MS] [--json] "CMD"
                                        ask/do/test: flags first; the text starts at the first
                                        non-flag argument (use -- before text that starts with -)
  orch models                             six catalog keys
  orch contract new <name> --role R --model K [--run ID]    create contract template
  orch spawn --model K --name N --role R [--purpose P] [--reviews a,b] [--author-family head|openai|anthropic|google]
           [--cwd D|--worktree D] [--account same|hook] [--run ID]
  orch prompt <name> [--contract FILE] [--wait] [--timeout MS]
  orch wait [name...] [--timeout MS] [--no-fallback]      detect completion from the result file status line
  orch collect <name>                     inspect result and check owned-file scope
  orch read <name> [--lines N]            terminal output (only when no result file exists)
  orch fallback <name> [--reason "..."]   switch to next fallback key
  orch close <name> | --run ID | --all

DAG (dag.yaml in the run directory)
  orch dag check|run|status [--run ID] [--timeout MS]   spawn ready nodes in critical-path order and run to completion
  orch checklist [--shuffle] [--seed N]   create neutral-lane checklist from acceptance.md (full+ shuffles item order)

Maintenance
  orch layout                             Head 1/3 · restore equal rows on the right
  orch resume --run ID                    resume /goal: make this pane Head and mark missing workers
  orch gc [--orphans]                     clean panes spawned by this Head for closed runs
                                        --orphans: also clean runs whose Head disappeared (abnormal exit)
  orch doctor
  orch install [--repo DIR] [--dry-run]   install constitution, hooks, skill, and playbooks (after backup)
  orch playbook check [DIR]
  orch hook claude|codex|agy              PreToolUse guard (stdin JSON)

roles: recon design produce integrate verify-mech verify-critical verify-neutral verify-adversarial
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// parse lets flags appear after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				continue
			}
			if f := fs.Lookup(name); f != nil {
				if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
					continue
				}
				if i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	return pos, fs.Parse(flags)
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "orch:", err)
		return 1
	}
	cmd, rest := args[0], args[1:]
	// A worker may only read: every command that changes runs or panes is Head-only.
	readOnly := contains1([]string{"hook", "models", "doctor", "playbook", "help", "install", "status", "checklist"}, cmd) ||
		(cmd == "run" && len(rest) > 0 && rest[0] == "list") ||
		(cmd == "dag" && len(rest) > 0 && (rest[0] == "check" || rest[0] == "status"))
	if envOrch("ROLE") == "worker" && !readOnly {
		return fail(fmt.Errorf("ORCH_ROLE=worker: workers cannot use orch %s (Head only)", cmd))
	}
	// A detached __task or __reap was verified by its parent before launch (it is reparented).
	if !readOnly && cmd != "__task" && cmd != "__reap" && os.Getenv("HERDR_PANE_ID") != "" {
		if err := verifyCaller(os.Getenv("HERDR_PANE_ID")); err != nil {
			if os.Getppid() == 1 {
				err = fmt.Errorf("%w; detached orch processes cannot manage panes; run orch wait in the foreground or with your harness's background-task feature (not &, nohup or setsid)", err)
			}
			return fail(err)
		}
	}
	fs := flag.NewFlagSet("orch "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	runFlag := fs.String("run", "", "run id")

	switch cmd {
	case "ask", "do", "test":
		return taskCommand(cmd, rest, stdout, stderr)
	case "__task":
		pos, err := parse(fs, rest)
		if err != nil || len(pos) != 1 {
			return fail(fmt.Errorf("usage: orch __task --run ID task-N"))
		}
		return taskChild(*runFlag, pos[0], stdout, stderr)
	case "__reap":
		if _, err := parse(fs, rest); err != nil {
			return 2
		}
		return reapCommand(*runFlag, stderr)
	case "hook":
		if len(rest) != 1 {
			return fail(fmt.Errorf("usage: orch hook claude|codex|agy"))
		}
		return runHook(rest[0], stdin, stdout, stderr)

	case "models":
		tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "key\tCLI\tfamily\tcost\targs\toverride\tstrengths")
		for _, m := range catalog {
			args, err := modelArgs(m)
			shown := strings.Join(args, " ")
			if err != nil {
				shown = "invalid: " + err.Error()
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Key, m.Kind, m.Family, m.Cost, shown, modelVar(m), m.Summary)
		}
		tw.Flush()
		fmt.Fprintf(stdout, "\npurposes(fallback chain): %s\n", strings.Join(purposeNames(), " "))
		return 0

	case "run":
		if len(rest) == 0 {
			return fail(fmt.Errorf("usage: orch run new|mode|end|list"))
		}
		sub := rest[0]
		reqFile := fs.String("request", "", "request file or -")
		text := fs.String("text", "", "request text")
		mode := fs.String("mode", "light", "mode")
		playbook := fs.String("playbook", "", "playbook type")
		goal := fs.String("goal", "", "goal id")
		reason := fs.String("reason", "", "reason")
		all := fs.Bool("all", false, "include ad-hoc task runs")
		pos, err := parse(fs, rest[1:])
		if err != nil {
			return 2
		}
		switch sub {
		case "new":
			if err := requireHead(); err != nil {
				return fail(err)
			}
			if !contains1(modes, *mode) {
				return fail(fmt.Errorf("--mode must be one of %v", modes))
			}
			req := *text
			if *reqFile == "-" {
				b, _ := io.ReadAll(stdin)
				req = string(b)
			} else if *reqFile != "" {
				b, err := os.ReadFile(*reqFile)
				if err != nil {
					return fail(err)
				}
				req = string(b)
			}
			if strings.TrimSpace(req) == "" {
				return fail(fmt.Errorf("original request is empty (--text, --request)"))
			}
			if *playbook != "" && !fileExists(filepath.Join(orchHome(), "playbooks", *playbook+".yaml")) {
				return fail(fmt.Errorf("playbook %s not found", *playbook))
			}
			r, err := createRun(req, *mode, *playbook, *goal)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintln(stdout, r.ID)
			fmt.Fprintf(stderr, "run directory: %s\nnext: fill acceptance.md and decisions.md, then create a contract with orch contract new\n", r.dir())
			return 0
		case "mode":
			if len(pos) != 1 || !contains1(modes, pos[0]) || *reason == "" {
				return fail(fmt.Errorf("usage: orch run mode <%s> --reason \"...\"", strings.Join(modes, "|")))
			}
			id, err := resolveRun(*runFlag)
			if err != nil {
				return fail(err)
			}
			return failIf(fail, withRun(id, func(r *Run) error {
				r.event("mode %s → %s: %s", r.Mode, pos[0], *reason)
				r.Mode = pos[0]
				return nil
			}))
		case "end":
			id, err := resolveRun(*runFlag)
			if err != nil {
				return fail(err)
			}
			notes, err := closeRun(id, true)
			for _, n := range notes {
				fmt.Fprintln(stdout, n)
			}
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(stdout, "run %s ended; removal of all recorded panes confirmed\n", id)
			return 0
		case "list":
			runs, err := allRuns()
			if err != nil {
				return fail(err)
			}
			tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "run\tstatus\tmode\tHead\tworker(live/total)")
			for _, r := range runs {
				if r.Adhoc && !*all {
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\n", r.ID, r.Status, r.Mode, r.HeadPane, len(r.liveWorkers()), len(r.Workers))
			}
			tw.Flush()
			return 0
		}
		return fail(fmt.Errorf("unknown run subcommand %q", sub))

	case "contract":
		role := fs.String("role", "", "role")
		key := fs.String("model", "", "catalog key")
		pos, err := parse(fs, rest)
		if err != nil {
			return 2
		}
		if len(pos) != 2 || pos[0] != "new" {
			return fail(fmt.Errorf("usage: orch contract new <name> --role R --model K"))
		}
		if _, err := modelByKey(*key); err != nil {
			return fail(err)
		}
		id, err := resolveRun(*runFlag)
		if err != nil {
			return fail(err)
		}
		r, err := loadRun(id)
		if err != nil {
			return fail(err)
		}
		p := r.contractPath(pos[1])
		if fileExists(p) {
			return fail(fmt.Errorf("already exists: %s", p))
		}
		if err := os.WriteFile(p, []byte(contractTemplate(r, pos[1], *role, *key)), 0600); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, p)
		return 0

	case "spawn":
		key := fs.String("model", "", "catalog key")
		name := fs.String("name", "", "worker name")
		role := fs.String("role", "", "role")
		purpose := fs.String("purpose", "", "purpose (fallback chain)")
		reviews := fs.String("reviews", "", "reviewed workers")
		authorFam := fs.String("author-family", "", "author outside the run: openai|anthropic|google|head")
		cwd := fs.String("cwd", "", "working directory")
		wt := fs.String("worktree", "", "worktree path")
		account := fs.String("account", "same", "same|hook")
		if _, err := parse(fs, rest); err != nil {
			return 2
		}
		w, err := spawn(spawnOpts{Run: *runFlag, Key: *key, Name: *name, Role: *role, Purpose: *purpose,
			Reviews: splitList(*reviews), AuthorFamilies: splitList(*authorFam), Cwd: *cwd, Worktree: *wt, Account: *account})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s pane=%s key=%s account=%s result=%s\n", w.Name, w.Pane, w.Key, w.Account, w.Result)
		return 0

	case "prompt":
		contract := fs.String("contract", "", "contract file")
		wait := fs.Bool("wait", false, "wait for settled state")
		timeout := fs.Int("timeout", 120000, "ms")
		pos, err := parse(fs, rest)
		if err != nil {
			return 2
		}
		if len(pos) != 1 {
			return fail(fmt.Errorf("usage: orch prompt <name> [--contract FILE]"))
		}
		return failIf(fail, prompt(*runFlag, pos[0], *contract, *wait, *timeout))

	case "wait":
		timeout := fs.Int("timeout", 1800000, "ms")
		noFallback := fs.Bool("no-fallback", false, "disable fallback")
		pos, err := parse(fs, rest)
		if err != nil {
			return 2
		}
		outs, err := waitWorkers(*runFlag, pos, time.Duration(*timeout)*time.Millisecond, !*noFallback)
		code := 0
		for _, o := range outs {
			if strings.HasPrefix(o.Name, "task-") && strings.HasPrefix(o.Detail, "task: ") {
				fmt.Fprintln(stdout, o.Detail)
			} else {
				fmt.Fprintf(stdout, "%s\t%s\t%s\n", o.Name, o.Status, o.Detail)
			}
			// A replaced worker is judged by its replacement's own outcome line.
			if o.Status != "done" && !strings.HasPrefix(o.Status, "fallback:") {
				code = 1
			}
		}
		if err != nil {
			return fail(err)
		}
		return code

	case "collect":
		pos, err := parse(fs, rest)
		if err != nil || len(pos) != 1 {
			return fail(fmt.Errorf("usage: orch collect <name>"))
		}
		out, st, err := collectWorker(*runFlag, pos[0])
		fmt.Fprint(stdout, out)
		if err != nil {
			return fail(err)
		}
		if st == "failed" {
			return 1
		}
		return 0

	case "read":
		lines := fs.Int("lines", 40, "lines")
		pos, err := parse(fs, rest)
		if err != nil || len(pos) != 1 {
			return fail(fmt.Errorf("usage: orch read <name>"))
		}
		text, err := agentRead(pos[0], *lines)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, text)
		return 0

	case "fallback":
		reason := fs.String("reason", "manual fallback", "reason")
		pos, err := parse(fs, rest)
		if err != nil || len(pos) != 1 {
			return fail(fmt.Errorf("usage: orch fallback <name>"))
		}
		id, err := resolveRun(*runFlag)
		if err != nil {
			return fail(err)
		}
		nw, err := doFallback(id, pos[0], *reason)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s → %s\n", pos[0], nw)
		return 0

	case "close":
		all := fs.Bool("all", false, "all runs of this Head")
		pos, err := parse(fs, rest)
		if err != nil {
			return 2
		}
		head := os.Getenv("HERDR_PANE_ID")
		var cerr error
		switch {
		case *all:
			runs, _ := allRuns()
			for _, r := range runs {
				if r.Status == "active" && r.HeadPane == head {
					if _, err := closeRun(r.ID, false); err != nil {
						cerr = err
					}
				}
			}
		case len(pos) == 1:
			id, err := resolveRun(*runFlag)
			if err != nil {
				return fail(err)
			}
			cerr = closeWorker(id, pos[0])
		case *runFlag != "":
			_, cerr = closeRun(*runFlag, false)
		default:
			return fail(fmt.Errorf("usage: orch close <name> | --run ID | --all"))
		}
		if head != "" {
			if left, _ := ownedPanes(head); len(left) > 0 {
				if err := normalizeLayout(head); err != nil {
					fmt.Fprintln(stderr, "orch: layout:", err)
				}
			}
		}
		if cerr != nil {
			return fail(cerr)
		}
		fmt.Fprintln(stdout, "closure confirmed")
		return 0

	case "status":
		if _, err := parse(fs, rest); err != nil {
			return 2
		}
		id, err := resolveRun(*runFlag)
		if err != nil {
			return fail(err)
		}
		r, err := loadRun(id)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "run %s · %s · mode=%s · Head=%s\n", r.ID, r.Status, r.Mode, r.HeadPane)
		tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "worker\tkey\trole\tstatus\tresult\tpane\taccount\telapsed")
		for _, w := range r.Workers {
			res := resultStatus(w.Result)
			if res == "" {
				res = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", w.Name, w.Key, w.Role, w.Status, res, dash(w.Pane), w.Account, time.Since(w.Started).Round(time.Second))
		}
		tw.Flush()
		return 0

	case "layout":
		head := os.Getenv("HERDR_PANE_ID")
		if head == "" {
			return fail(fmt.Errorf("HERDR_PANE_ID missing"))
		}
		if err := normalizeLayout(head); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "layout valid (Head 1/3, equal rows on the right)")
		return 0

	case "resume":
		if _, err := parse(fs, rest); err != nil {
			return 2
		}
		if *runFlag == "" {
			return fail(fmt.Errorf("usage: orch resume --run ID"))
		}
		if err := requireHead(); err != nil {
			return fail(err)
		}
		rep, err := resume(*runFlag)
		for _, l := range rep {
			fmt.Fprintln(stdout, l)
		}
		return failIf(fail, err)

	case "gc":
		orphans := fs.Bool("orphans", false, "also clean runs whose Head pane is gone")
		if _, err := parse(fs, rest); err != nil {
			return 2
		}
		rep, err := gc(*orphans)
		for _, l := range rep {
			fmt.Fprintln(stdout, l)
		}
		if len(rep) == 0 && err == nil {
			fmt.Fprintln(stdout, "nothing to clean")
		}
		return failIf(fail, err)

	case "doctor":
		code := 0
		for _, c := range doctor() {
			mark := "ok  "
			if !c.ok {
				mark, code = "FAIL", 1
			}
			fmt.Fprintf(stdout, "%s %s  %s\n", mark, c.name, c.info)
		}
		return code

	case "install":
		wd, _ := os.Getwd()
		repo := fs.String("repo", wd, "orch repository")
		dry := fs.Bool("dry-run", false, "show only")
		if _, err := parse(fs, rest); err != nil {
			return 2
		}
		rep, err := install(*repo, *dry)
		if rep != nil {
			for _, l := range rep.lines {
				fmt.Fprintln(stdout, l)
			}
		}
		return failIf(fail, err)

	case "dag":
		timeout := fs.Int("timeout", 7200000, "ms")
		pos, err := parse(fs, rest)
		if err != nil || len(pos) != 1 {
			return fail(fmt.Errorf("usage: orch dag check|run|status [--run ID] [--timeout MS]"))
		}
		id, err := resolveRun(*runFlag)
		if err != nil {
			return fail(err)
		}
		r, err := loadRun(id)
		if err != nil {
			return fail(err)
		}
		d, err := loadDag(r)
		if err != nil {
			return fail(err)
		}
		switch pos[0] {
		case "check":
			errs := validateDag(r, d)
			for _, e := range errs {
				fmt.Fprintln(stdout, e)
			}
			if len(errs) > 0 {
				return 1
			}
			crit := criticalLength(d)
			fmt.Fprintf(stdout, "DAG check passed: %d nodes, critical path length %d\n", len(d.Nodes), maxValue(crit))
			return 0
		case "run":
			st, err := dagRun(id, time.Duration(*timeout)*time.Millisecond, stdout)
			if st != nil {
				fmt.Fprintln(stdout, "summary:", dagSummary(d, st))
			}
			if err != nil {
				return fail(err)
			}
			if !dagAllDone(d, st) {
				return fail(errDagIncomplete)
			}
			return 0
		case "status":
			st := loadDagState(r, d)
			tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "node	status	worker	dependency	notes")
			for _, n := range d.Nodes {
				ns := st.Nodes[n.Name]
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", n.Name, ns.Status, dash(ns.Worker), dash(strings.Join(n.Needs, ",")), ns.Detail)
			}
			tw.Flush()
			return 0
		}
		return fail(fmt.Errorf("unknown DAG subcommand %q", pos[0]))

	case "checklist":
		shuffle := fs.Bool("shuffle", false, "shuffle order (full+ position-bias check)")
		seed := fs.Int64("seed", 0, "shuffle seed (0 = time)")
		if _, err := parse(fs, rest); err != nil {
			return 2
		}
		id, err := resolveRun(*runFlag)
		if err != nil {
			return fail(err)
		}
		r, err := loadRun(id)
		if err != nil {
			return fail(err)
		}
		table, err := checklist(r, *shuffle, *seed)
		if err != nil {
			return fail(err)
		}
		fmt.Fprint(stdout, table)
		return 0

	case "playbook":
		pos, err := parse(fs, rest)
		if err != nil || len(pos) == 0 || pos[0] != "check" {
			return fail(fmt.Errorf("usage: orch playbook check [DIR]"))
		}
		dir := filepath.Join(orchHome(), "playbooks")
		if len(pos) > 1 {
			dir = pos[1]
		}
		errs := checkPlaybooks(dir)
		for _, e := range errs {
			fmt.Fprintln(stdout, e)
		}
		if len(errs) > 0 {
			return 1
		}
		fmt.Fprintln(stdout, "playbook schema check passed:", dir)
		return 0
	}
	return fail(fmt.Errorf("unknown command %q (orch help)", cmd))
}

func failIf(fail func(error) int, err error) int {
	if err != nil {
		return fail(err)
	}
	return 0
}

func contains1(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func maxValue(m map[string]int) int {
	best := 0
	for _, v := range m {
		best = max(best, v)
	}
	return best
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
