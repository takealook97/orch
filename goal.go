package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Criterion struct {
	N     int    `json:"n"`
	Text  string `json:"text"`
	Check string `json:"check"`
}
type Goal struct {
	ID           string         `json:"id"`
	HeadPane     string         `json:"head_pane"`
	Status       string         `json:"status"` // draft, active, ended
	Created      time.Time      `json:"created"`
	Frozen       time.Time      `json:"frozen"`
	Hours        float64        `json:"hours"`
	MaxWorkers   int            `json:"max_workers"`
	Admitted     int            `json:"admitted"`
	FixRounds    int            `json:"fix_rounds"`
	Criteria     []Criterion    `json:"criteria"`
	Scope        []string       `json:"scope"`
	CriteriaHash string         `json:"criteria_hash"`
	Fixes        map[string]int `json:"fixes"`
	Events       []Event        `json:"events"`
}

func goalsDir() string      { return filepath.Join(orchHome(), "goals") }
func (g *Goal) dir() string { return filepath.Join(goalsDir(), g.ID) }
func loadGoal(id string) (*Goal, error) {
	if filepath.Base(id) != id || id == "." {
		return nil, errors.New("invalid goal id")
	}
	b, e := os.ReadFile(filepath.Join(goalsDir(), id, "goal.json"))
	if e != nil {
		return nil, e
	}
	var g Goal
	e = json.Unmarshal(b, &g)
	return &g, e
}
func saveGoal(g *Goal) error {
	b, e := json.MarshalIndent(g, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(g.dir(), ".goal-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(append(b, '\n')); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), filepath.Join(g.dir(), "goal.json"))
}
func withGoal(id string, fn func(*Goal) error) error {
	if filepath.Base(id) != id || id == "." {
		return errors.New("invalid goal id")
	}
	f, e := os.OpenFile(filepath.Join(goalsDir(), id, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	g, e := loadGoal(id)
	if e != nil {
		return e
	}
	if e = fn(g); e != nil {
		return e
	}
	return saveGoal(g)
}
func newestGoal(head, status string) (*Goal, error) {
	if head == "" {
		return nil, nil
	}
	es, e := os.ReadDir(goalsDir())
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var best *Goal
	for _, entry := range es {
		if !entry.IsDir() {
			continue
		}
		g, e := loadGoal(entry.Name())
		if e != nil {
			continue
		}
		if g.HeadPane == head && g.Status == status && (best == nil || g.Created.After(best.Created)) {
			best = g
		}
	}
	return best, nil
}
func activeGoal(head string) (*Goal, error) { return newestGoal(head, "active") }

var activeGoalFor = activeGoal

func goalTemplate(request string) string {
	return "# Goal\n\n## Request\n\n" + request + "\n\n## Criteria\n\n<!-- Example only: replace this comment with your criteria.\n1. Implement the requested behavior\n   - check: go test ./...\n-->\n\n## Non-goals\n\n- Fill in excluded work.\n\n## Scope\n\n<!-- Add absolute or ~-prefixed globs, one per bullet. -->\n\n## Budget\n\n- hours: 4\n- max-workers: 20\n- fix-rounds: 2\n"
}

var goalCriterionLine = regexp.MustCompile(`^(\d+)\.\s+(.+)$`)

func parseGoalFile(text string) (cs []Criterion, scope []string, hours float64, maxWorkers, fixRounds int, err error) {
	bad := func(s string) { err = errors.New(s) }
	section := ""
	pending := -1
	seen := map[int]bool{}
	budget := map[string]bool{}
	comment := false
	for _, raw := range strings.Split(text, "\n") {
		l := strings.TrimSpace(raw)
		if strings.Contains(l, "<!--") {
			comment = true
		}
		if comment {
			if strings.Contains(l, "-->") {
				comment = false
			}
			continue
		}
		if l == "" {
			continue
		}
		if pending >= 0 {
			if !strings.HasPrefix(l, "- check:") || strings.TrimSpace(strings.TrimPrefix(l, "- check:")) == "" {
				bad(fmt.Sprintf("criterion %d needs a non-empty check", cs[pending].N))
				return
			}
			cs[pending].Check = strings.TrimSpace(strings.TrimPrefix(l, "- check:"))
			pending = -1
			continue
		}
		if strings.HasPrefix(l, "## ") {
			section = strings.TrimPrefix(l, "## ")
			continue
		}
		switch section {
		case "Criteria":
			if m := goalCriterionLine.FindStringSubmatch(l); m != nil {
				n, e := strconv.Atoi(m[1])
				if e != nil || seen[n] {
					bad("duplicate or invalid criterion number")
					return
				}
				seen[n] = true
				for _, word := range []string{"완벽", "싹다", "남은 위험", "남은위험", "모든 위험", "끝까지", "perfect", "everything", "all remaining"} {
					if strings.Contains(strings.ToLower(m[2]), word) {
						bad(fmt.Sprintf("criterion %d contains banned word %q", n, word))
						return
					}
				}
				cs = append(cs, Criterion{N: n, Text: m[2]})
				pending = len(cs) - 1
			}
		case "Scope":
			if strings.HasPrefix(l, "- ") {
				s := strings.TrimSpace(l[2:])
				if s != "" {
					if !filepath.IsAbs(s) && !strings.HasPrefix(s, "~/") {
						bad("Scope globs must be absolute or ~-prefixed")
						return
					}
					scope = append(scope, s)
				}
			}
		case "Budget":
			if strings.HasPrefix(l, "- ") {
				k, v, ok := strings.Cut(l[2:], ":")
				if !ok {
					continue
				}
				v = strings.TrimSpace(v)
				var e error
				switch k {
				case "hours":
					hours, e = strconv.ParseFloat(v, 64)
				case "max-workers":
					maxWorkers, e = strconv.Atoi(v)
				case "fix-rounds":
					fixRounds, e = strconv.Atoi(v)
				default:
					continue
				}
				if e != nil {
					bad("invalid budget " + k)
					return
				}
				budget[k] = true
			}
		}
	}
	switch {
	case pending >= 0:
		bad("missing check")
	case len(cs) == 0 || len(cs) > 8:
		bad("need 1-8 criteria")
	case len(scope) == 0:
		bad("need Scope globs")
	case !budget["hours"] || !budget["max-workers"] || !budget["fix-rounds"] || hours <= 0 || math.IsNaN(hours) || math.IsInf(hours, 0) || maxWorkers <= 0 || fixRounds < 0:
		bad("invalid or missing Budget")
	}
	return
}
func criteriaHash(cs []Criterion) string {
	b, _ := json.Marshal(cs)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

var errGoalCriteriaTampered = errors.New("criteria tampered: end this goal and start a new one to change criteria")

func goalTamper(g *Goal) error {
	b, e := os.ReadFile(filepath.Join(g.dir(), "goal.md"))
	if e != nil {
		return e
	}
	cs, _, _, _, _, e := parseGoalFile(string(b))
	if e != nil {
		return e
	}
	if criteriaHash(cs) != g.CriteriaHash {
		return errGoalCriteriaTampered
	}
	return nil
}
func goalUsage(g *Goal) (workers int, fixes map[string]int) {
	return g.Admitted, g.Fixes
}
func goalBudget(g *Goal) error {
	w := g.Admitted
	elapsed := time.Since(g.Frozen).Hours()
	if elapsed > g.Hours || w >= g.MaxWorkers {
		return fmt.Errorf("goal %s budget reached (%.2fh of %.2fh, %d of %d workers): stop starting work, run orch goal status, report results and backlog to the user, and ask whether to extend", g.ID, elapsed, g.Hours, w, g.MaxWorkers)
	}
	return nil
}

type admission struct {
	Kind, Name string
	Serves     []int
	FixOf      string
}

var fixName = regexp.MustCompile(`^fix\d*-(.+)$`)

func admit(head string, o admission) (release func(), err error) {
	release = func() {}
	g, e := activeGoal(head)
	if e != nil || g == nil {
		return release, e
	}
	shard := ""
	err = withGoal(g.ID, func(g *Goal) error {
		if g.Status != "active" {
			return errors.New("goal is no longer active")
		}
		if e := goalTamper(g); e != nil {
			return e
		}
		if e := goalBudget(g); e != nil {
			return e
		}
		if len(o.Serves) == 0 {
			return fmt.Errorf("this goal (%s) only admits work that serves a criterion: add --serves N. Not on the list? record it with orch goal note \"...\" and keep going", g.ID)
		}
		valid := map[int]bool{}
		var ns []string
		for _, c := range g.Criteria {
			valid[c.N] = true
			ns = append(ns, strconv.Itoa(c.N))
		}
		for _, n := range o.Serves {
			if !valid[n] {
				return fmt.Errorf("unknown criterion %d (valid: %s); record other work with orch goal note", n, strings.Join(ns, ","))
			}
		}
		shard = o.FixOf
		if shard == "" && o.Kind == "spawn" {
			if m := fixName.FindStringSubmatch(o.Name); m != nil {
				shard = m[1]
			}
		}
		if g.Fixes == nil {
			g.Fixes = map[string]int{}
		}
		if shard != "" {
			if g.Fixes[shard] >= g.FixRounds {
				return fmt.Errorf("shard %s used its %d fix rounds: mark it blocked, record it with orch goal note, and report it", shard, g.FixRounds)
			}
			g.Fixes[shard]++
		}
		g.Admitted++
		g.Events = append(g.Events, Event{time.Now(), fmt.Sprintf("admit %s %s serves=%v fix-of=%s", o.Kind, o.Name, o.Serves, shard)})
		return nil
	})
	if err != nil {
		return release, err
	}
	release = func() {
		_ = withGoal(g.ID, func(g *Goal) error {
			if g.Admitted > 0 {
				g.Admitted--
			}
			if shard != "" {
				if g.Fixes[shard] > 1 {
					g.Fixes[shard]--
				} else {
					delete(g.Fixes, shard)
				}
			}
			g.Events = append(g.Events, Event{time.Now(), fmt.Sprintf("release %s %s", o.Kind, o.Name)})
			return nil
		})
	}
	return release, nil
}
func parseServes(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	var out []int
	for _, v := range strings.Split(s, ",") {
		n, e := strconv.Atoi(strings.TrimSpace(v))
		if e != nil {
			return nil, fmt.Errorf("invalid --serves %q", s)
		}
		out = append(out, n)
	}
	return out, nil
}
func goalCommand(rest []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(rest) == 0 || rest[0] == "--help" || rest[0] == "-h" {
		fmt.Fprintln(stdout, "orch goal new --text T | --request FILE|-\norch goal freeze|status|check [--goal ID]\norch goal note TEXT\norch goal extend --hours N --max-workers N --reason 'user: ...'\norch goal end")
		return 0
	}
	sub := rest[0]
	fs := flag.NewFlagSet("goal "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("goal", "", "goal id")
	text := fs.String("text", "", "request")
	request := fs.String("request", "", "request file or -")
	hours := fs.Float64("hours", 0, "additional hours")
	workers := fs.Int("max-workers", 0, "additional workers")
	reason := fs.String("reason", "", "user: reason")
	if e := fs.Parse(rest[1:]); e != nil {
		return 2
	}
	fail := func(e error, code int) int { fmt.Fprintln(stderr, "orch:", e); return code }
	head := os.Getenv("HERDR_PANE_ID")
	if sub != "status" && sub != "check" {
		if e := requireHead(); e != nil {
			return fail(e, 1)
		}
	}
	if sub == "new" {
		g, e := activeGoal(head)
		if e != nil {
			return fail(e, 2)
		}
		if g != nil {
			return fail(errors.New("an active goal already exists"), 2)
		}
		if (*text == "") == (*request == "") {
			return fail(errors.New("use --text or --request"), 2)
		}
		body := *text
		if *request != "" {
			var b []byte
			if *request == "-" {
				b, e = io.ReadAll(stdin)
			} else {
				b, e = os.ReadFile(*request)
			}
			if e != nil {
				return fail(e, 2)
			}
			body = string(b)
		}
		g = &Goal{ID: newRunID(), HeadPane: head, Status: "draft", Created: time.Now(), Fixes: map[string]int{}}
		if e = os.MkdirAll(g.dir(), 0700); e != nil {
			return fail(e, 2)
		}
		for n, b := range map[string]string{"goal.md": goalTemplate(body), "backlog.md": "# Backlog\n\nFound while working, not on the criteria list. Report these at the end; do not work them.\n"} {
			if e = os.WriteFile(filepath.Join(g.dir(), n), []byte(b), 0600); e != nil {
				return fail(e, 2)
			}
		}
		if e = saveGoal(g); e != nil {
			return fail(e, 2)
		}
		fmt.Fprintln(stdout, g.ID)
		fmt.Fprintln(stderr, filepath.Join(g.dir(), "goal.md"), "— fill it, then orch goal freeze")
		return 0
	}
	var g *Goal
	var e error
	if *id != "" {
		g, e = loadGoal(*id)
	} else if sub == "freeze" {
		g, e = newestGoal(head, "draft")
	} else {
		g, e = activeGoal(head)
	}
	if e != nil {
		return fail(e, 2)
	}
	if g == nil {
		return fail(errors.New("no active goal (or draft to freeze)"), 2)
	}
	if g.HeadPane != head {
		return fail(errors.New("goal belongs to another Head pane"), 2)
	}
	if sub != "freeze" && g.Status != "active" {
		return fail(errors.New("goal is not active"), 2)
	}
	switch sub {
	case "freeze":
		e = withGoal(g.ID, func(x *Goal) error {
			if x.Status != "draft" {
				return errors.New("only a draft can be frozen")
			}
			b, e := os.ReadFile(filepath.Join(x.dir(), "goal.md"))
			if e != nil {
				return e
			}
			cs, sc, h, w, f, e := parseGoalFile(string(b))
			if e != nil {
				return e
			}
			a, e := activeGoal(head)
			if e != nil {
				return e
			}
			if a != nil {
				return errors.New("an active goal already exists")
			}
			x.Criteria, x.Scope, x.Hours, x.MaxWorkers, x.FixRounds = cs, sc, h, w, f
			x.Status = "active"
			x.Frozen = time.Now()
			x.CriteriaHash = criteriaHash(cs)
			g = x
			return nil
		})
		if e != nil {
			return fail(e, 2)
		}
		for _, c := range g.Criteria {
			fmt.Fprintf(stdout, "%d. %s\n", c.N, c.Text)
		}
		fmt.Fprintln(stdout, "completion: orch goal check exits 0 (goal met) or 3 (goal stopped: budget)")
	case "status", "check":
		if g.Status != "active" {
			return fail(errors.New("goal is not active"), 2)
		}
		if e = goalTamper(g); e != nil {
			if errors.Is(e, errGoalCriteriaTampered) {
				return fail(e, 4)
			}
			return fail(e, 2)
		}
		if sub == "status" {
			w, fix := goalUsage(g)
			b, e := os.ReadFile(filepath.Join(g.dir(), "backlog.md"))
			if e != nil {
				return fail(e, 2)
			}
			count := 0
			for _, l := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(l, "- ") {
					count++
				}
			}
			fmt.Fprintf(stdout, "goal: %s\nelapsed: %.2f/%.2fh\nworkers: %d/%d\nbacklog: %d\n", g.ID, time.Since(g.Frozen).Hours(), g.Hours, w, g.MaxWorkers, count)
			var shards []string
			for s := range fix {
				shards = append(shards, s)
			}
			sort.Strings(shards)
			for _, s := range shards {
				fmt.Fprintf(stdout, "fixes %s: %d/%d\n", s, fix[s], g.FixRounds)
			}
			for _, c := range g.Criteria {
				fmt.Fprintf(stdout, "%d. %s\n", c.N, c.Text)
			}
			return 0
		}
		if e = goalBudget(g); e != nil {
			fmt.Fprintln(stdout, "goal stopped: budget")
			return fail(e, 3)
		}
		code := 0
		cwd, e := os.Getwd()
		if e != nil {
			return fail(e, 2)
		}
		for _, c := range g.Criteria {
			tr, e := executeTest(c.Check, cwd, 5, 600000, filepath.Join(g.dir(), fmt.Sprintf("check-%d.log", c.N)))
			if e != nil || tr.ExitCode != 0 || tr.TimedOut || tr.Interrupted {
				code = 1
				fmt.Fprintf(stdout, "#%d FAIL\n", c.N)
				b, _ := os.ReadFile(tr.Log)
				lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
				fmt.Fprintln(stdout, strings.Join(lines[max(0, len(lines)-5):], "\n"))
			} else {
				fmt.Fprintf(stdout, "#%d PASS\n", c.N)
			}
		}
		if code == 0 {
			fmt.Fprintln(stdout, "goal met")
		}
		return code
	case "note":
		if fs.NArg() != 1 {
			return fail(errors.New("note needs one quoted text"), 2)
		}
		e = withGoal(g.ID, func(x *Goal) error {
			f, e := os.OpenFile(filepath.Join(x.dir(), "backlog.md"), os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			defer f.Close()
			_, e = fmt.Fprintf(f, "- %s %s\n", time.Now().Format(time.RFC3339), strings.ReplaceAll(fs.Arg(0), "\n", " "))
			return e
		})
	case "extend":
		if !strings.HasPrefix(*reason, "user:") || math.IsNaN(*hours) || math.IsInf(*hours, 0) || *hours < 0 || *workers < 0 || (*hours == 0 && *workers == 0) {
			return fail(errors.New("extend needs positive additions and --reason starting with user:"), 2)
		}
		e = withGoal(g.ID, func(x *Goal) error {
			x.Hours += *hours
			x.MaxWorkers += *workers
			x.Events = append(x.Events, Event{time.Now(), fmt.Sprintf("extend hours=%g max-workers=%d reason=%s", *hours, *workers, *reason)})
			return nil
		})
	case "end":
		e = withGoal(g.ID, func(x *Goal) error { x.Status = "ended"; return nil })
		if e == nil {
			b, err := os.ReadFile(filepath.Join(g.dir(), "backlog.md"))
			if err != nil {
				return fail(err, 2)
			}
			fmt.Fprint(stdout, string(b))
		}
	default:
		return fail(fmt.Errorf("unknown goal subcommand %q", sub), 2)
	}
	if e != nil {
		return fail(e, 2)
	}
	return 0
}
