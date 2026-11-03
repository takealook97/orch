package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
}
type TestResult struct {
	Cmd        string   `json:"cmd"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	Failures   []string `json:"failures,omitempty"`
	Excerpt    string   `json:"excerpt,omitempty"`
	Log        string   `json:"log"`
	TimedOut   bool     `json:"timed_out,omitempty"`
	// Interrupted is internal: orch itself was signalled while the test ran.
	Interrupted bool `json:"-"`
}
type Task struct {
	ID              string            `json:"task"`
	Run             string            `json:"run"`
	Kind            string            `json:"kind"`
	Key             string            `json:"key,omitempty"`
	Status          string            `json:"status"`
	ExitCode        int               `json:"exit_code"`
	DurationMS      int64             `json:"duration_ms"`
	Answer          string            `json:"answer,omitempty"`
	AnswerPath      string            `json:"answer_path,omitempty"`
	Truncated       bool              `json:"truncated,omitempty"`
	Changed         []string          `json:"changed,omitempty"`
	ScopeViolations []string          `json:"scope_violations,omitempty"`
	Diffstat        string            `json:"diffstat,omitempty"`
	DiffPath        string            `json:"diff_path,omitempty"`
	Test            *TestResult       `json:"test,omitempty"`
	Usage           *Usage            `json:"usage,omitempty"`
	Dir             string            `json:"dir"`
	Cwd             string            `json:"cwd,omitempty"`
	Own             []string          `json:"own,omitempty"`
	TestCmd         string            `json:"test_cmd,omitempty"`
	TimeoutMS       int               `json:"timeout_ms,omitempty"`
	MaxOutput       int               `json:"max_output,omitempty"`
	Account         string            `json:"account,omitempty"`
	Baseline        map[string]string `json:"baseline,omitempty"`
	// Pid is the orch process running the task (foreground orch or the __task runner); 0 until
	// a background runner claims it. Heartbeat is refreshed while it runs. Pgid is the process
	// group of the task's child CLI, which can outlive a killed runner.
	Pid       int       `json:"pid,omitempty"`
	Pgid      int       `json:"pgid,omitempty"`
	Heartbeat time.Time `json:"heartbeat,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// taskMu serializes this process's writes of task.json (heartbeat versus final save).
var taskMu sync.Mutex

func taskPath(runID, id string) string { return filepath.Join(runDir(runID), "tasks", id) }
func writeTask(t *Task) error {
	taskMu.Lock()
	defer taskMu.Unlock()
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(t.Dir, "task.json")
	tmp, err := os.CreateTemp(t.Dir, ".task-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
func readTask(runID, id string) (*Task, error) {
	if !regexp.MustCompile(`^task-[1-9][0-9]*$`).MatchString(id) {
		return nil, fmt.Errorf("invalid task id %q", id)
	}
	b, err := os.ReadFile(filepath.Join(taskPath(runID, id), "task.json"))
	if err != nil {
		return nil, err
	}
	var t Task
	err = json.Unmarshal(b, &t)
	return &t, err
}
func taskRun(text string) (string, error) {
	if id, err := activeTaskRun(); err == nil {
		return id, nil
	}
	r, err := newRun(text, "direct", "", "", true)
	if err != nil {
		return "", err
	}
	return r.ID, nil
}

// pidAlive reports whether a process exists (a permission error still means it exists).
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// groupAlive reports whether a process group still has a member.
func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// taskDead explains why a task recorded as running has no live runner, or returns "".
// A claimed task is dead only when its runner and its child group are both gone; a stale
// heartbeat alone (a suspended laptop) is not proof. The heartbeat decides only for a task
// no runner has claimed.
func taskDead(t *Task) string {
	if t.Pid > 0 || t.Pgid > 0 {
		if t.Pid > 0 && pidAlive(t.Pid) || t.Pgid > 0 && groupAlive(t.Pgid) {
			return ""
		}
		return fmt.Sprintf("runner died (pid %d and child group %d are gone)", t.Pid, t.Pgid)
	}
	if !t.Heartbeat.IsZero() && time.Since(t.Heartbeat) > timings.TaskStale {
		return fmt.Sprintf("runner died (no heartbeat since %s)", t.Heartbeat.Format(time.RFC3339))
	}
	return ""
}

// taskEdit serializes this process's read-modify-write updates of task.json.
var taskEdit sync.Mutex

// editTask applies fn to the task on disk while it is still running.
func editTask(t *Task, fn func(x *Task)) {
	taskEdit.Lock()
	defer taskEdit.Unlock()
	if x, err := readTask(t.Run, t.ID); err == nil && x.Status == "running" {
		fn(x)
		_ = writeTask(x)
	}
}

// heartbeat refreshes the task's heartbeat on disk until the returned stop function runs.
func heartbeat(t *Task) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(timings.TaskHeartbeat)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				editTask(t, func(x *Task) { x.Heartbeat = time.Now() })
			}
		}
	}()
	return func() { close(stop); <-done }
}
func activeTaskRun() (string, error) {
	if envOrch("RUN") == "" && headID() == "" {
		return "", errors.New("no active Head pane")
	}
	return resolveRun("")
}
func newTask(kind, key, text, cwd string) (*Task, error) {
	id, err := taskRun(text)
	if err != nil {
		return nil, err
	}
	return newTaskIn(id, kind, key, text, cwd)
}

// newTaskIn registers a one-off task in run id.
func newTaskIn(id, kind, key, text, cwd string) (*Task, error) {
	var t *Task
	err := withRun(id, func(r *Run) error {
		max := 0
		for _, x := range r.Tasks {
			if n, e := strconv.Atoi(strings.TrimPrefix(x.ID, "task-")); e == nil && n > max {
				max = n
			}
		}
		name := fmt.Sprintf("task-%d", max+1)
		t = &Task{ID: name, Run: id, Kind: kind, Key: key, Status: "running", Dir: taskPath(id, name), Cwd: cwd, Pid: os.Getpid(), Heartbeat: time.Now()}
		if err := os.MkdirAll(t.Dir, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(t.Dir, "prompt.md"), []byte(text), 0600); err != nil {
			return err
		}
		if err := writeTask(t); err != nil {
			return err
		}
		r.Tasks = append(r.Tasks, t)
		return nil
	})
	return t, err
}
func saveTask(t *Task) error {
	if err := writeTask(t); err != nil {
		return err
	}
	return withRun(t.Run, func(r *Run) error {
		for i, x := range r.Tasks {
			if x.ID == t.ID {
				r.Tasks[i] = t
				return nil
			}
		}
		return fmt.Errorf("task %s missing", t.ID)
	})
}
func taskArgs(m Model, kind, cwd, answer, prompt string) ([]string, error) {
	ma, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	switch m.Kind {
	case "claude":
		a := append([]string{"-p"}, ma...)
		if kind == "ask" {
			// dontAsk denies anything not pre-approved, so the read-only tools are listed twice:
			// --tools makes them available, --allowedTools lets them run without a prompt.
			a = append(a, "--tools", "Read,Grep,Glob,WebSearch,WebFetch", "--allowedTools", "Read,Grep,Glob,WebSearch,WebFetch", "--permission-mode", "dontAsk")
		} else {
			a = append(a, "--permission-mode", "bypassPermissions")
		}
		// No MCP connectors and no skill listing: a one-off task needs neither, and together
		// they add a large prompt to every call that the cheaper key would pay for.
		return append(a, "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", prompt), nil
	case "codex":
		a := append([]string{"exec"}, ma...)
		if kind == "ask" {
			a = append(a, "--sandbox", "read-only")
		} else {
			a = append(a, "--dangerously-bypass-approvals-and-sandbox")
		}
		return append(a, "--skip-git-repo-check", "--json", "-C", cwd, "-o", answer, prompt), nil
	case "agy":
		// agy's -p takes the prompt as its value (unlike claude's -p flag), so the
		// prompt must follow -p directly; anything else is read as the prompt.
		a := append([]string{}, ma...)
		if kind == "ask" {
			a = append(a, "--mode", "plan")
		} else {
			a = append(a, "--dangerously-skip-permissions")
		}
		return append(a, "--output-format", "json", "-p", prompt), nil
	}
	return nil, fmt.Errorf("unknown CLI kind %s", m.Kind)
}
func taskPrompt(t *Task, requirement string) string {
	p := "You are an orch worker on a one-off task. There is no contract file. Do not delegate. ORCH_ROLE=worker.\n"
	if t.Kind == "do" {
		p += fmt.Sprintf("Edit only files matching these owned globs: %s. Do not edit other files.\n", strings.Join(t.Own, ", "))
		if t.TestCmd != "" {
			p += fmt.Sprintf("Run %q until it passes.\n", t.TestCmd)
		}
		p += "End with a final message whose first line is SUMMARY: <one line>, followed by at most 10 lines.\n"
	}
	return p + "\n" + requirement
}

// execResult is how a task child ended.
type execResult struct {
	Code        int
	Timed       bool // killed at the timeout
	Interrupted bool // orch got SIGINT, SIGTERM or SIGHUP and stopped the child
	MS          int64
}

// execLogged runs a child in its own process group. The group is killed on timeout, and a
// signal to orch is forwarded to the group (then SIGKILL after a grace period) so an
// interrupted orch never leaves a child editing files behind.
// started, when set, receives the child's pid (its process group id) once it runs.
func execLogged(cwd, bin string, args []string, env []string, timeout time.Duration, logPath string, started func(pgid int)) (execResult, error) {
	log, err := os.Create(logPath)
	if err != nil {
		return execResult{Code: 1}, err
	}
	defer log.Close()
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	start := time.Now()
	if err = cmd.Start(); err != nil {
		return execResult{Code: 1, MS: time.Since(start).Milliseconds()}, err
	}
	if started != nil {
		started(cmd.Process.Pid)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var res execResult
	select {
	case err = <-done:
	case <-timer.C:
		res.Timed = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err = <-done
	case sig := <-sigs:
		res.Interrupted = true
		_ = syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
		select {
		case err = <-done:
		case <-time.After(timings.InterruptGrace):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			err = <-done
		}
		// Anything the child left in its group goes too.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	res.MS = time.Since(start).Milliseconds()
	switch {
	case res.Timed:
		res.Code = 124
		return res, nil
	case res.Interrupted:
		res.Code = 130
		return res, nil
	case err == nil:
		return res, nil
	}
	var ex *exec.ExitError
	if errors.As(err, &ex) {
		res.Code = ex.ExitCode()
		return res, nil
	}
	res.Code = 1
	return res, err
}
func int64From(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// usageFrom normalizes usage so input_tokens always includes cached input: Codex reports it
// that way; Claude reports uncached input, cache writes and cache reads separately.
func usageFrom(v map[string]any) *Usage {
	u := &Usage{
		InputTokens:       int64From(v["input_tokens"]) + int64From(v["cache_creation_input_tokens"]) + int64From(v["cache_read_input_tokens"]),
		OutputTokens:      int64From(v["output_tokens"]),
		CachedInputTokens: int64From(v["cached_input_tokens"]) + int64From(v["cache_read_input_tokens"]),
	}
	if u.InputTokens+u.OutputTokens+u.CachedInputTokens == 0 {
		return nil
	}
	return u
}
func parseAnswer(kind string, raw []byte) (string, *Usage) {
	answer := ""
	var usage *Usage
	read := func(v map[string]any) {
		if s, ok := v["result"].(string); ok && s != "" {
			answer = s
		}
		if s, ok := v["response"].(string); ok && s != "" {
			answer = s
		}
		if s, ok := v["text"].(string); ok && s != "" {
			answer = s
		}
		if item, ok := v["item"].(map[string]any); ok {
			if item["type"] == "agent_message" {
				if s, ok := item["text"].(string); ok {
					answer = s
				}
			}
		}
		if u, ok := v["usage"].(map[string]any); ok {
			if got := usageFrom(u); got != nil {
				usage = got
			}
		}
	}
	if kind == "codex" {
		sc := bufio.NewScanner(bytes.NewReader(raw))
		sc.Buffer(make([]byte, 4096), 10*1024*1024)
		for sc.Scan() {
			var v map[string]any
			if json.Unmarshal(sc.Bytes(), &v) == nil {
				read(v)
			}
		}
	} else {
		var v map[string]any
		if json.Unmarshal(raw, &v) == nil {
			read(v)
		} else {
			sc := bufio.NewScanner(bytes.NewReader(raw))
			for sc.Scan() {
				var x map[string]any
				if json.Unmarshal(sc.Bytes(), &x) == nil {
					read(x)
				}
			}
		}
	}
	if answer == "" {
		answer = strings.TrimSpace(string(raw))
	}
	return answer, usage
}
func capText(s string, n int, path string) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]) + "… (truncated; full: " + path + ")", true
}

// diffForTask writes diff.patch for the task's own changed files only (paths relative to the
// repository root), so the patch applies with git apply and matches Changed.
func diffForTask(t *Task) {
	root := repoRoot(t.Cwd)
	base := "HEAD"
	if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "HEAD").Run() != nil {
		base = emptyTree
	}
	var tracked, untracked []string
	for _, f := range t.Changed {
		if exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", f).Run() == nil {
			tracked = append(tracked, f)
		} else {
			untracked = append(untracked, f)
		}
	}
	var patch bytes.Buffer
	if len(tracked) > 0 {
		b, _ := exec.Command("git", append([]string{"-C", root, "diff", "--no-renames", "--binary", base, "--"}, tracked...)...).Output()
		patch.Write(b)
	}
	for _, f := range untracked {
		// Exit status 1 only means the files differ.
		b, _ := exec.Command("git", "-C", root, "diff", "--no-index", "--binary", "--", "/dev/null", f).Output()
		patch.Write(b)
	}
	t.DiffPath = filepath.Join(t.Dir, "diff.patch")
	_ = os.WriteFile(t.DiffPath, patch.Bytes(), 0600)
	files, adds, dels := 0, 0, 0
	stat := exec.Command("git", "apply", "--numstat", "-")
	stat.Stdin = bytes.NewReader(patch.Bytes())
	out, _ := stat.Output()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		files++
		a, _ := strconv.Atoi(f[0]) // "-" for binary files counts as 0
		d, _ := strconv.Atoi(f[1])
		adds += a
		dels += d
	}
	t.Diffstat = fmt.Sprintf("%d files, +%d -%d", files, adds, dels)
}
func runTask(t *Task) error {
	start := time.Now()
	cacheKey, _ := os.ReadFile(filepath.Join(t.Dir, "ask-cache-key"))
	stopBeat := heartbeat(t)
	defer func() {
		stopBeat()
		t.DurationMS = time.Since(start).Milliseconds()
		_ = saveTask(t)
		if t.Kind == "ask" {
			storeAskCache(string(cacheKey), t)
		}
	}()
	m, err := modelByKey(t.Key)
	if err != nil {
		t.Status = "failed"
		return err
	}
	acc, err := pickAccount(t.Account, m.Kind, nil)
	if err != nil {
		t.Status = "failed"
		return err
	}
	req, err := os.ReadFile(filepath.Join(t.Dir, "prompt.md"))
	if err != nil {
		t.Status = "failed"
		return err
	}
	answerPath := filepath.Join(t.Dir, "answer.md")
	prompt := taskPrompt(t, string(req))
	args, err := taskArgs(m, t.Kind, t.Cwd, answerPath, prompt)
	if err != nil {
		t.Status = "failed"
		return err
	}
	env := append(os.Environ(), "ORCH_ROLE=worker", "ORCH_RUN="+t.Run, "ORCH_WORKER="+t.ID, "ORCH_MODEL="+t.Key)
	env = append(env, acc.Env...)
	budget := time.Duration(t.TimeoutMS) * time.Millisecond
	res, err := execLogged(t.Cwd, m.Kind, args, env, budget, filepath.Join(t.Dir, "output.log"), func(pgid int) {
		t.Pgid = pgid
		editTask(t, func(x *Task) { x.Pgid = pgid })
	})
	t.ExitCode = res.Code
	if err != nil {
		t.Status = "failed"
		return err
	}
	if res.Interrupted {
		t.Status, t.Note = "failed", "interrupted"
		return nil
	}
	raw, _ := os.ReadFile(filepath.Join(t.Dir, "output.log"))
	answer, usage := parseAnswer(m.Kind, raw)
	if m.Kind == "codex" {
		if b, e := os.ReadFile(answerPath); e == nil && len(b) > 0 {
			answer = string(b)
		}
	}
	t.Usage = usage
	t.AnswerPath = answerPath
	_ = os.WriteFile(answerPath, []byte(answer), 0600)
	limit := t.MaxOutput
	if t.Kind == "do" {
		limit = 1500
	}
	t.Answer, t.Truncated = capText(answer, limit, answerPath)
	testTimed := false
	if t.Kind == "do" {
		changed, e := changedFiles(t.Cwd, "")
		if e != nil {
			t.Status = "failed"
			return e
		}
		t.Changed = freshChanges(t.Cwd, changed, t.Baseline)
		sort.Strings(t.Changed)
		t.ScopeViolations = scopeViolations(t.Changed, rootGlobs(t.Cwd, t.Own))
		diffForTask(t)
		if t.TestCmd != "" && !res.Timed {
			// The final test gets what is left of the task budget, at least a minute.
			left := max(budget-time.Since(start), time.Minute)
			tr, te := executeTest(t.TestCmd, t.Cwd, 40, int(left/time.Millisecond), filepath.Join(t.Dir, "test.log"))
			t.Test = tr
			if te != nil {
				t.Status = "failed"
				return te
			}
			if tr.Interrupted {
				t.Status, t.Note = "failed", "interrupted"
				return nil
			}
			testTimed = tr.TimedOut
		}
	}
	switch {
	case res.Timed || testTimed:
		t.Status = "timeout"
	case res.Code != 0 || len(t.ScopeViolations) > 0 || t.Test != nil && t.Test.ExitCode != 0:
		t.Status = "failed"
	default:
		t.Status = "done"
	}
	return nil
}

var failPatterns = []*regexp.Regexp{regexp.MustCompile(`^--- FAIL: (\S+)`), regexp.MustCompile(`^FAIL\s+(\S+)`), regexp.MustCompile(`^FAILED\s+(\S+)`), regexp.MustCompile(`^\s*✕\s+(.+)`)}

// failContext is how many lines before the first failure match the excerpt keeps, so a
// compiler error printed above "FAIL pkg [setup failed]" stays visible.
const failContext = 10

func executeTest(line, cwd string, maxLines, timeoutMS int, logPath string) (*TestResult, error) {
	res, err := execLogged(cwd, "sh", []string{"-c", line}, os.Environ(), time.Duration(timeoutMS)*time.Millisecond, logPath, nil)
	r := &TestResult{Cmd: line, ExitCode: res.Code, DurationMS: res.MS, Log: logPath, TimedOut: res.Timed, Interrupted: res.Interrupted}
	if err != nil {
		return r, err
	}
	b, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	marker := "… (truncated; full: " + logPath + ")"
	seen := map[string]bool{}
	first := -1
	cut := false
	for i, l := range lines {
		for _, p := range failPatterns {
			if m := p.FindStringSubmatch(l); len(m) > 1 {
				if first < 0 {
					first = i
				}
				if !seen[m[1]] {
					if len(r.Failures) < 20 {
						r.Failures = append(r.Failures, m[1])
					} else {
						cut = true
					}
					seen[m[1]] = true
				}
				break
			}
		}
	}
	if cut {
		r.Failures = append(r.Failures, marker)
	}
	if first < 0 && res.Code != 0 {
		first = 0
	}
	if first >= 0 {
		// Context never pushes the first failure out of a short excerpt.
		from := max(first-min(failContext, maxLines/2), 0)
		end := min(from+maxLines, len(lines))
		r.Excerpt = strings.Join(lines[from:end], "\n")
		if end < len(lines) {
			r.Excerpt += "\n" + marker
		}
	}
	return r, nil
}

// A one-off task this long should have been a pane worker the user can watch (skill section 1).
const paneworthyTask = 5 * time.Minute

// paneHint tells the Head after the fact that a pane-less task ran long enough to need a pane.
func paneHint(t *Task, inHerdr bool) string {
	if !inHerdr || (t.Kind != "ask" && t.Kind != "do") || t.DurationMS < paneworthyTask.Milliseconds() {
		return ""
	}
	return fmt.Sprintf("ran %d min without a pane; hand work this long to a pane worker (orch spawn) so the user can watch it", t.DurationMS/time.Minute.Milliseconds())
}

func taskReport(t *Task, asJSON bool) string {
	hint := paneHint(t, inHerdr())
	if asJSON {
		v := map[string]any{"task": t.ID, "run": t.Run, "status": t.Status, "exit_code": t.ExitCode, "duration_ms": t.DurationMS, "dir": t.Dir}
		if hint != "" {
			v["hint"] = hint
		}
		if t.Key != "" {
			v["key"] = t.Key
		}
		if t.Note != "" {
			v["note"] = t.Note
		}
		if t.Answer != "" {
			v["answer"] = t.Answer
			v["answer_path"] = t.AnswerPath
			v["truncated"] = t.Truncated
		}
		if t.Kind == "do" {
			v["changed"] = t.Changed
			v["scope_violations"] = t.ScopeViolations
			v["diffstat"] = t.Diffstat
			v["diff_path"] = t.DiffPath
		}
		if t.Test != nil {
			v["test"] = t.Test
		}
		if t.Usage != nil {
			v["usage"] = t.Usage
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "task: %s\nrun: %s\nstatus: %s\nexit code: %d\nduration: %d ms\n", t.ID, t.Run, t.Status, t.ExitCode, t.DurationMS)
	if t.Note != "" {
		fmt.Fprintf(&b, "note: %s\n", t.Note)
	}
	if t.Answer != "" {
		fmt.Fprintf(&b, "answer: %s\n", t.Answer)
	}
	if len(t.Changed) > 0 {
		fmt.Fprintf(&b, "changed: %s\n", strings.Join(t.Changed, ", "))
	}
	if t.Diffstat != "" {
		fmt.Fprintf(&b, "diffstat: %s\n", t.Diffstat)
	}
	if len(t.ScopeViolations) > 0 {
		fmt.Fprintf(&b, "scope violations: %s\n", strings.Join(t.ScopeViolations, ", "))
	}
	if t.Test != nil {
		writeTest(&b, "test ", t.Test)
	}
	if t.DiffPath != "" {
		fmt.Fprintf(&b, "diff: %s\n", t.DiffPath)
	}
	if t.Dir != "" {
		fmt.Fprintf(&b, "dir: %s\n", t.Dir)
	}
	if hint != "" {
		fmt.Fprintf(&b, "hint: %s\n", hint)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
func taskChild(runID, id string, stdout, stderr io.Writer) int {
	if runID == "" {
		fmt.Fprintln(stderr, "orch: __task requires --run")
		return 1
	}
	t, err := claimTask(runID, id)
	if err != nil {
		fmt.Fprintln(stderr, "orch:", err)
		return 1
	}
	err = runTask(t)
	if err != nil {
		fmt.Fprintln(stderr, "orch:", err)
	}
	fmt.Fprintln(stdout, taskReport(t, false))
	if t.Status != "done" {
		return 1
	}
	return 0
}

// claimTask makes this process the runner of a running task that no live process runs yet.
func claimTask(runID, id string) (*Task, error) {
	if _, err := readTask(runID, id); err != nil {
		return nil, err
	}
	var t *Task
	err := withRun(runID, func(r *Run) error {
		x, err := readTask(runID, id)
		if err != nil {
			return err
		}
		if x.Status != "running" {
			return fmt.Errorf("task %s is %s; only a running task that no process runs can be started", id, x.Status)
		}
		if x.Pid != 0 && x.Pid != os.Getpid() && pidAlive(x.Pid) {
			return fmt.Errorf("task %s is already run by pid %d", id, x.Pid)
		}
		if x.Pgid != 0 && groupAlive(x.Pgid) {
			return fmt.Errorf("task %s still has a live child process group %d", id, x.Pgid)
		}
		x.Pid, x.Pgid, x.Heartbeat = os.Getpid(), 0, time.Now()
		t = x
		return writeTask(x)
	})
	return t, err
}

type ownedFlags []string

func (v *ownedFlags) String() string { return strings.Join(*v, ",") }

func (v *ownedFlags) Set(value string) error {
	*v = append(*v, value)
	return nil
}

var taskFlagName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// errUnknownTaskFlag marks an error parseTask raises itself; flag.Parse errors are
// already printed by the flag package.
type errUnknownTaskFlag struct{ msg string }

func (e errUnknownTaskFlag) Error() string { return e.msg }

// parseTask parses flags up to the first positional argument (or "--"); everything after
// it is the question, requirement or command, so a prompt may start with "-".
func parseTask(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if strings.HasPrefix(a, "--") {
			longName, _, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
			if taskFlagName.MatchString(longName) && fs.Lookup(longName) == nil {
				var names []string
				fs.VisitAll(func(f *flag.Flag) { names = append(names, "--"+f.Name) })
				return nil, errUnknownTaskFlag{fmt.Sprintf("orch: unknown flag --%s for %s (flags: %s; put -- before a prompt that starts with -)", longName, fs.Name(), strings.Join(names, ", "))}
			}
		}
		if !strings.HasPrefix(a, "-") || len(a) < 2 || f == nil {
			pos = append(pos, args[i:]...)
			break
		}
		flags = append(flags, a)
		if hasValue {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return pos, fs.Parse(flags)
}

func taskCommand(kind string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("orch "+kind, flag.ContinueOnError)
	fs.SetOutput(stderr)
	key := fs.String("model", "", "catalog key")
	cwd := fs.String("cwd", "", "directory")
	maxOutput := fs.Int("max-output", 2000, "characters")
	var fresh bool
	if kind == "ask" {
		fs.BoolVar(&fresh, "fresh", false, "bypass cached answer")
	}
	var own ownedFlags
	fs.Var(&own, "own", "owned globs")
	testCmd := fs.String("test", "", "test command")
	worktree := fs.String("worktree", "", "worktree directory")
	background := fs.Bool("background", false, "run in background")
	timeout := fs.Int("timeout", 0, "milliseconds")
	account := fs.String("account", "same", "same|hook")
	asJSON := fs.Bool("json", false, "JSON output")
	maxLines := fs.Int("max-lines", 40, "test excerpt lines")
	var servesFlag, fixOf string
	if kind == "do" {
		fs.StringVar(&servesFlag, "serves", "", "goal criteria, comma-separated")
		fs.StringVar(&fixOf, "fix-of", "", "fix shard")
	}
	pos, err := parseTask(fs, args)
	if err != nil {
		var unknown errUnknownTaskFlag
		if errors.As(err, &unknown) {
			fmt.Fprintln(stderr, err)
		}
		return 2
	}
	serves, err := parseServes(servesFlag)
	if err != nil {
		fmt.Fprintln(stderr, "orch:", err)
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "orch: expected one question, requirement, or command after the flags (quote it; put -- before one that starts with -)")
		return 2
	}
	if *timeout <= 0 {
		if kind == "ask" {
			*timeout = 600000
		} else {
			*timeout = 1800000
		}
	}
	if *cwd == "" {
		*cwd, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	*cwd, err = filepath.Abs(*cwd)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *maxOutput < 0 || *maxLines < 0 {
		fmt.Fprintln(stderr, "orch: caps must be nonnegative")
		return 2
	}
	if kind == "test" {
		return testCommand(pos[0], *cwd, *maxLines, *timeout, *asJSON, stdout, stderr)
	}
	if _, err = modelByKey(*key); err != nil {
		fmt.Fprintln(stderr, "orch:", err)
		return 1
	}
	if kind == "do" && strings.TrimSpace(strings.Join(own, ",")) == "" {
		fmt.Fprintln(stderr, "orch: --own is required")
		return 2
	}
	cacheKey := ""
	if kind == "ask" {
		cacheKey = askCacheKey(pos[0], *key, *maxOutput, *cwd)
		if !fresh {
			if cached := lookupAskCache(cacheKey); cached != nil {
				fmt.Fprintln(stdout, cachedAskReport(cached, *maxOutput, *asJSON))
				return 0
			}
		}
	}
	release := func() {}
	committed := false
	defer func() {
		if !committed {
			release()
		}
	}()
	if kind == "do" {
		if release, err = admit(headID(), admission{"do", "", serves, fixOf}); err != nil {
			fmt.Fprintln(stderr, "orch:", err)
			return 1
		}
	}
	t, err := newTask(kind, *key, pos[0], *cwd)
	if err != nil {
		fmt.Fprintln(stderr, "orch:", err)
		return 1
	}
	if cacheKey != "" {
		_ = os.WriteFile(filepath.Join(t.Dir, "ask-cache-key"), []byte(cacheKey), 0600)
	}
	// newTask has recorded the task in its run; it remains counted from here.
	committed = true
	t.Own = splitOwned(strings.Join(own, ","))
	t.TestCmd = *testCmd
	t.TimeoutMS = *timeout
	t.MaxOutput = *maxOutput
	t.Account = *account
	if *background {
		t.Pid = 0 // the __task runner claims the task with its own pid
	}
	if kind == "do" {
		if *worktree != "" {
			path, e := filepath.Abs(*worktree)
			if e != nil {
				err = e
			} else {
				if _, e = os.Stat(path); errors.Is(e, os.ErrNotExist) {
					branch := "orch/" + t.Run + "-" + t.ID
					c := exec.Command("git", "-C", *cwd, "worktree", "add", "-b", branch, path, "HEAD")
					if out, ce := c.CombinedOutput(); ce != nil {
						err = fmt.Errorf("git worktree add: %v %s", ce, out)
					}
				}
				t.Cwd = path
			}
		}
		if err == nil {
			var files []string
			files, err = changedFiles(t.Cwd, "")
			if err == nil {
				t.Baseline = snapshot(t.Cwd, files)
			}
		}
	}
	if err != nil {
		t.Status = "failed"
		_ = saveTask(t)
		fmt.Fprintln(stderr, "orch:", err)
		return 1
	}
	if err = saveTask(t); err != nil {
		fmt.Fprintln(stderr, "orch:", err)
		return 1
	}
	if *background {
		// A runner that never starts must not leave the task "running".
		notStarted := func(e error) int {
			t.Status, t.Note = "failed", "runner did not start: "+e.Error()
			_ = saveTask(t)
			fmt.Fprintln(stderr, "orch:", e)
			return 1
		}
		exe, e := os.Executable()
		if e != nil {
			return notStarted(e)
		}
		out, e := os.Create(filepath.Join(t.Dir, "child.log"))
		if e != nil {
			return notStarted(e)
		}
		defer out.Close()
		c := exec.Command(exe, "__task", "--run", t.Run, t.ID)
		c.Stdin = nil
		c.Stdout = out
		c.Stderr = out
		c.Env = os.Environ()
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if e = c.Start(); e != nil {
			return notStarted(e)
		}
		_ = c.Process.Release()
		if *asJSON {
			fmt.Fprintln(stdout, taskReport(t, true))
		} else {
			fmt.Fprintf(stdout, "task: %s\nrun: %s\nwait: orch wait %s --run %s\n", t.ID, t.Run, t.ID, t.Run)
		}
		return 0
	}
	err = runTask(t)
	if err != nil {
		fmt.Fprintln(stderr, "orch:", err)
	}
	fmt.Fprintln(stdout, taskReport(t, *asJSON))
	if t.Status != "done" {
		return 1
	}
	return 0
}
func splitOwned(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}
func testCommand(line, cwd string, maxLines, timeout int, asJSON bool, stdout, stderr io.Writer) int {
	var path string
	_, err := activeTaskRun()
	if err == nil {
		t, e := newTask("test", "", line, cwd)
		if e != nil {
			fmt.Fprintln(stderr, "orch:", e)
			return 1
		}
		path = filepath.Join(t.Dir, "test.log")
		stopBeat := heartbeat(t)
		tr, e := executeTest(line, cwd, maxLines, timeout, path)
		stopBeat()
		t.Test = tr
		t.ExitCode = tr.ExitCode
		t.DurationMS = tr.DurationMS
		t.Status = testStatus(tr, e)
		if tr.Interrupted {
			t.Note = "interrupted"
		}
		_ = saveTask(t)
		fmt.Fprintln(stdout, taskReport(t, asJSON))
		if t.Status != "done" {
			return 1
		}
		return 0
	}
	path = filepath.Join(orchHome(), "tests", time.Now().Format("20060102-150405.000")+".log")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		fmt.Fprintln(stderr, "orch:", e)
		return 1
	}
	tr, e := executeTest(line, cwd, maxLines, timeout, path)
	if e != nil {
		fmt.Fprintln(stderr, "orch:", e)
	}
	if asJSON {
		b, _ := json.Marshal(map[string]any{"status": testStatus(tr, e), "exit_code": tr.ExitCode, "duration_ms": tr.DurationMS, "test": tr, "dir": filepath.Dir(path)})
		fmt.Fprintln(stdout, string(b))
	} else {
		var b strings.Builder
		fmt.Fprintf(&b, "status: %s\nduration: %d ms\n", testStatus(tr, e), tr.DurationMS)
		writeTest(&b, "", tr)
		fmt.Fprint(stdout, b.String())
	}
	if tr.ExitCode != 0 || e != nil {
		return 1
	}
	return 0
}

// testStatus is done, timeout or failed for a finished test command.
func testStatus(tr *TestResult, err error) string {
	switch {
	case tr.TimedOut:
		return "timeout"
	case tr.ExitCode != 0 || err != nil:
		return "failed"
	}
	return "done"
}

// writeTest prints a test result, leaving out empty failure and excerpt lines.
func writeTest(b *strings.Builder, prefix string, tr *TestResult) {
	fmt.Fprintf(b, "%sexit code: %d\n", prefix, tr.ExitCode)
	if len(tr.Failures) > 0 {
		fmt.Fprintf(b, "%sfailures: %s\n", prefix, strings.Join(tr.Failures, ", "))
	}
	if tr.Excerpt != "" {
		fmt.Fprintf(b, "%sfirst failure:\n%s\n", prefix, strings.TrimRight(tr.Excerpt, "\n"))
	}
	fmt.Fprintf(b, "%slog: %s\n", prefix, tr.Log)
}
