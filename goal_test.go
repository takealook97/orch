package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validGoalFile = `# Goal
## Request
request
## Criteria
1. Requested behavior works
   - check: true
2. Regression suite passes
   - check: printf ok
## Non-goals
- Unrelated changes
## Scope
- /workspace/src/**
## Budget
- hours: 4
- max-workers: 20
- fix-rounds: 2
`

func goalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "goal-head")
}
func goalCall(args ...string) (int, string, string) {
	var out, err bytes.Buffer
	c := goalCommand(args, strings.NewReader(""), &out, &err)
	return c, out.String(), err.String()
}
func frozenTestGoal(t *testing.T) *Goal {
	t.Helper()
	goalEnv(t)
	c, out, err := goalCall("new", "--text", "request")
	if c != 0 {
		t.Fatal(c, err)
	}
	id := strings.TrimSpace(out)
	if e := os.WriteFile(filepath.Join(goalsDir(), id, "goal.md"), []byte(validGoalFile), 0600); e != nil {
		t.Fatal(e)
	}
	if c, _, err = goalCall("freeze"); c != 0 {
		t.Fatal(c, err)
	}
	g, e := loadGoal(id)
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func TestParseGoalFile(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty", ""}, {"template", goalTemplate("test")},
		{"zero", strings.Replace(validGoalFile, "1. Requested behavior works\n   - check: true\n2. Regression suite passes\n   - check: printf ok", "", 1)},
		{"too many", strings.Replace(validGoalFile, "## Non-goals", "3. a\n- check: true\n4. b\n- check: true\n5. c\n- check: true\n6. d\n- check: true\n7. e\n- check: true\n8. f\n- check: true\n9. g\n- check: true\n## Non-goals", 1)},
		{"missing check", strings.Replace(validGoalFile, "   - check: true\n", "", 1)},
		{"empty check", strings.Replace(validGoalFile, "check: true", "check: ", 1)},
		{"duplicate", strings.Replace(validGoalFile, "2. Regression", "1. Regression", 1)},
		{"no scope", strings.Replace(validGoalFile, "- /workspace/src/**", "", 1)},
		{"no budget", strings.Split(validGoalFile, "## Budget")[0]},
		{"no fix budget", strings.Replace(validGoalFile, "- fix-rounds: 2", "", 1)},
		{"hours zero", strings.Replace(validGoalFile, "hours: 4", "hours: 0", 1)},
		{"hours nan", strings.Replace(validGoalFile, "hours: 4", "hours: NaN", 1)},
		{"workers zero", strings.Replace(validGoalFile, "max-workers: 20", "max-workers: 0", 1)},
		{"fix negative", strings.Replace(validGoalFile, "fix-rounds: 2", "fix-rounds: -1", 1)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, _, _, e := parseGoalFile(tt.body); e == nil {
				t.Fatal("accepted invalid goal")
			}
		})
	}
	for _, word := range []string{"완벽", "싹다", "남은 위험", "남은위험", "모든 위험", "끝까지", "PERFECT", "everything", "all remaining"} {
		t.Run(word, func(t *testing.T) {
			_, _, _, _, _, e := parseGoalFile(strings.Replace(validGoalFile, "Requested behavior works", word, 1))
			if e == nil || !strings.Contains(e.Error(), "criterion 1") || !strings.Contains(strings.ToLower(e.Error()), strings.ToLower(word)) {
				t.Fatal(e)
			}
		})
	}
	cs, scope, h, w, f, e := parseGoalFile(validGoalFile)
	if e != nil || len(cs) != 2 || len(scope) != 1 || h != 4 || w != 20 || f != 2 {
		t.Fatal(cs, scope, h, w, f, e)
	}
}
func TestGoalLifecycle(t *testing.T) {
	g := frozenTestGoal(t)
	if g.Status != "active" || g.Frozen.IsZero() || g.CriteriaHash != criteriaHash(g.Criteria) {
		t.Fatal(g)
	}
	if c, _, _ := goalCall("new", "--text", "another"); c != 2 {
		t.Fatal(c)
	}
	if c, _, _ := goalCall("freeze", "--goal", g.ID); c != 2 {
		t.Fatal("refroze active", c)
	}
	if c, _, e := goalCall("note", "follow-up idea"); c != 0 {
		t.Fatal(e)
	}
	if c, out, e := goalCall("status"); c != 0 || !strings.Contains(out, "backlog: 1") || !strings.Contains(out, "workers: 0/20") {
		t.Fatal(c, out, e)
	}
	if c, _, _ := goalCall("extend", "--hours", "1", "--reason", "agent: extend"); c != 2 {
		t.Fatal(c)
	}
	if c, _, e := goalCall("extend", "--hours", "1", "--max-workers", "2", "--reason", "user: continue"); c != 0 {
		t.Fatal(e)
	}
	x, _ := loadGoal(g.ID)
	if x.Hours != 5 || x.MaxWorkers != 22 || len(x.Events) != 1 {
		t.Fatal(x)
	}
	if c, out, e := goalCall("end"); c != 0 || !strings.Contains(out, "follow-up idea") {
		t.Fatal(c, out, e)
	}
	if a, e := activeGoal("goal-head"); e != nil || a != nil {
		t.Fatal(a, e)
	}
}
func TestGoalTamper(t *testing.T) {
	g := frozenTestGoal(t)
	b := strings.Replace(validGoalFile, "Requested behavior works", "Different behavior", 1)
	if e := os.WriteFile(filepath.Join(g.dir(), "goal.md"), []byte(b), 0600); e != nil {
		t.Fatal(e)
	}
	for _, sub := range []string{"status", "check"} {
		if c, _, e := goalCall(sub); c != 4 || !strings.Contains(e, "tampered") {
			t.Fatal(c, e)
		}
	}
	if _, e := admit("goal-head", admission{Kind: "do", Serves: []int{1}}); e == nil || !strings.Contains(e.Error(), "tampered") {
		t.Fatal(e)
	}
}
func TestGoalAdmission(t *testing.T) {
	g := frozenTestGoal(t)
	for _, tt := range []struct {
		o    admission
		want string
	}{
		{admission{Kind: "spawn", Name: "impl"}, "--serves"},
		{admission{Kind: "do", Serves: []int{3}}, "valid: 1,2"},
		{admission{Kind: "spawn", Name: "impl", Serves: []int{1, 2}}, ""},
		{admission{Kind: "do", Serves: []int{1}, FixOf: "api"}, ""},
		{admission{Kind: "spawn", Name: "fix2-api", Serves: []int{2}}, ""},
		{admission{Kind: "spawn", Name: "fix-api", Serves: []int{1}}, "mark it blocked"},
		{admission{Kind: "do", Serves: []int{1}, FixOf: "api"}, "mark it blocked"},
	} {
		_, e := admit("goal-head", tt.o)
		if tt.want == "" && e != nil || tt.want != "" && (e == nil || !strings.Contains(e.Error(), tt.want)) {
			t.Fatalf("%+v: %v", tt, e)
		}
	}
	x, _ := loadGoal(g.ID)
	if x.Fixes["api"] != 2 || len(x.Events) != 3 {
		t.Fatal(x)
	}
	if _, e := admit("other-head", admission{Kind: "do"}); e != nil {
		t.Fatal(e)
	}
}
func TestGoalBudgets(t *testing.T) {
	for _, kind := range []string{"hours", "workers"} {
		t.Run(kind, func(t *testing.T) {
			g := frozenTestGoal(t)
			if kind == "hours" {
				if e := withGoal(g.ID, func(x *Goal) error { x.Frozen = time.Now().Add(-5 * time.Hour); return nil }); e != nil {
					t.Fatal(e)
				}
			} else {
				r, e := newRun("work", "full", "", "", false)
				if e != nil {
					t.Fatal(e)
				}
				if e = withRun(r.ID, func(r *Run) error {
					r.Workers = make([]*Worker, 18)
					r.Tasks = []*Task{{Kind: "ask"}, {Kind: "test"}, {Kind: "do"}, {Kind: "do"}}
					return nil
				}); e != nil {
					t.Fatal(e)
				}
				if e := withGoal(g.ID, func(g *Goal) error { g.Admitted = 20; return nil }); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := admit("goal-head", admission{Kind: "do", Serves: []int{1}}); e == nil || !strings.Contains(e.Error(), "budget reached") {
				t.Fatal(e)
			}
			if c, out, e := goalCall("check"); c != 3 || !strings.Contains(out, "goal stopped: budget") || !strings.Contains(e, "report results") {
				t.Fatal(c, out, e)
			}
		})
	}
}
func TestGoalRunLink(t *testing.T) {
	g := frozenTestGoal(t)
	for _, adhoc := range []bool{false, true} {
		r, e := newRun("request", "direct", "", "", adhoc)
		if e != nil || r.Goal != g.ID {
			t.Fatal(r, e)
		}
	}
	t.Setenv("HERDR_PANE_ID", "other")
	r, e := newRun("request", "direct", "", "", true)
	if e != nil || r.Goal != "" {
		t.Fatal(r, e)
	}
}
func TestGoalCheck(t *testing.T) {
	for _, tt := range []struct {
		check string
		code  int
	}{{"true", 0}, {"printf 'one\\ntwo\\nthree\\nfour\\nfive\\nsix\\n'; false", 1}} {
		t.Run(fmt.Sprint(tt.code), func(t *testing.T) {
			g := frozenTestGoal(t)
			if tt.code != 0 {
				body := strings.Replace(validGoalFile, "check: true", "check: "+tt.check, 1)
				os.WriteFile(filepath.Join(g.dir(), "goal.md"), []byte(body), 0600)
				withGoal(g.ID, func(x *Goal) error {
					cs, _, _, _, _, e := parseGoalFile(body)
					x.Criteria = cs
					x.CriteriaHash = criteriaHash(cs)
					return e
				})
			}
			c, out, e := goalCall("check")
			if c != tt.code || !strings.Contains(out, "#2 PASS") {
				t.Fatal(c, out, e)
			}
			if c == 0 && !strings.Contains(out, "goal met") {
				t.Fatal(out)
			}
			if c == 1 && (!strings.Contains(out, "six") || strings.Contains(out, "one")) {
				t.Fatal(out)
			}
		})
	}
	goalEnv(t)
	if c, _, _ := goalCall("check"); c != 2 {
		t.Fatal(c)
	}
}
func TestGoalScopeHook(t *testing.T) {
	g := frozenTestGoal(t)
	home, _ := os.UserHomeDir()
	for _, tt := range []struct {
		tool, path string
		deny       bool
	}{
		{"Edit", "/workspace/src/a.go", false}, {"namespace__Write", "/workspace/src/deep/a.go", false},
		{"MultiEdit", "/workspace/other/a.go", true}, {"ns.NotebookEdit", "/workspace/other/a.ipynb", true},
		{"Edit", filepath.Join(home, ".orch", "anything"), false}, {"Edit", "~/.claude/config", false},
		{"Edit", "/tmp/test", false}, {"Edit", "/private/tmp/test", false}, {"Bash", "/elsewhere", false},
	} {
		fields := map[string]string{"file_path": tt.path}
		if strings.Contains(tt.tool, "Notebook") {
			fields = map[string]string{"notebook_path": tt.path}
		}
		raw, _ := json.Marshal(fields)
		r := decide(hookInput{ToolName: tt.tool, ToolInput: raw}, "", true)
		if (r != "") != tt.deny {
			t.Fatalf("%+v: %s", tt, r)
		}
	}
	if e := withGoal(g.ID, func(x *Goal) error { x.Status = "ended"; return nil }); e != nil {
		t.Fatal(e)
	}
	if r := decide(hookInput{ToolName: "Edit", ToolInput: json.RawMessage(`{"file_path":"/outside"}`)}, "", true); r != "" {
		t.Fatal(r)
	}
}
func TestGoalPromptHook(t *testing.T) {
	goalEnv(t)
	for _, tt := range []struct {
		prompt string
		inject bool
	}{{"/goal build it", true}, {"  /goal a\nquoted \" $ `", true}, {"<command-name>/goal</command-name>", true}, {"mention /goal in prose", false}, {"/goals", false}, {"hello", false}} {
		b, _ := json.Marshal(map[string]string{"prompt": tt.prompt})
		var out bytes.Buffer
		if c := runHook("claude-prompt", bytes.NewReader(b), &out, &out); c != 0 {
			t.Fatal(c)
		}
		if (out.Len() > 0) != tt.inject {
			t.Fatal(tt, out.String())
		}
		if tt.inject {
			var output map[string]any
			if json.Unmarshal(out.Bytes(), &output) != nil || !strings.Contains(out.String(), "UserPromptSubmit") {
				t.Fatal(out.String())
			}
		}
	}
	g := frozenTestGoal(t)
	_ = g
	var out bytes.Buffer
	runHook("claude-prompt", strings.NewReader(`{"prompt":"/goal build"}`), &out, &out)
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
	t.Setenv("ORCH_ROLE", "worker")
	runHook("claude-prompt", strings.NewReader(`{"prompt":"/goal build"}`), &out, &out)
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
}
func TestGoalInstallMatcherUpgrade(t *testing.T) {
	hooks := map[string]any{}
	cmd := hookCommand("/local/orch", "claude")
	old := "Agent|Task|Workflow|Bash"
	newMatcher := old + "|Edit|Write|MultiEdit|NotebookEdit"
	if !ensureGroupedHook(hooks, old, cmd) || !ensureGroupedHook(hooks, newMatcher, cmd) || ensureGroupedHook(hooks, newMatcher, cmd) {
		t.Fatal(hooks)
	}
	list := hooks["PreToolUse"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["matcher"] != newMatcher {
		t.Fatal(hooks)
	}
	if !ensurePromptHook(hooks, hookCommand("/local/orch", "claude-prompt")) || ensurePromptHook(hooks, hookCommand("/local/orch", "claude-prompt")) || len(hooks["UserPromptSubmit"].([]any)) != 1 {
		t.Fatal(hooks)
	}
}
func TestGoalServesFlags(t *testing.T) {
	for _, s := range []string{"x", "1,", "1,no"} {
		if _, e := parseServes(s); e == nil {
			t.Fatal(s)
		}
	}
	for _, kind := range []string{"ask", "test"} {
		var out bytes.Buffer
		if c := taskCommand(kind, []string{"--serves", "1", "x"}, &out, &out); c != 2 || !strings.Contains(out.String(), "unknown flag --serves") {
			t.Fatal(c, out.String())
		}
	}
}

func TestGoalAdmissionWiring(t *testing.T) {
	g := frozenTestGoal(t)
	// Both entry points must reject before attempting Herdr RPC or an agent CLI.
	_, e := spawn(spawnOpts{Key: "codex-high-l", Name: "impl", Role: "produce"})
	if e == nil || !strings.Contains(e.Error(), "--serves") {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if c := taskCommand("do", []string{"--model", "codex-high-l", "--own", "goal.go", "implement"}, &out, &out); c != 1 || !strings.Contains(out.String(), "--serves") {
		t.Fatal(c, out.String())
	}
	rs, e := allRuns()
	if e != nil || len(rs) != 0 {
		t.Fatal(rs, e)
	}
	if c, _, e := goalCall("status"); c != 0 {
		t.Fatal(e)
	}
	// Creating task records alone does not spend admissions.
	for _, kind := range []string{"do", "ask", "test"} {
		if _, e := newTask(kind, "codex-high-l", "test", t.TempDir()); e != nil {
			t.Fatal(e)
		}
	}
	w, _ := goalUsage(g)
	if w != 0 {
		t.Fatal("usage", w)
	}
}
func TestGoalNoGoalCompatibility(t *testing.T) {
	goalEnv(t)
	if _, e := admit("goal-head", admission{Kind: "spawn", Name: "fix-api"}); e != nil {
		t.Fatal(e)
	}
	if g, e := activeGoal(""); g != nil || e != nil {
		t.Fatal(g, e)
	}
	var out bytes.Buffer
	t.Setenv("HERDR_ENV", "")
	runHook("claude-prompt", strings.NewReader(`{"prompt":"/goal x"}`), &out, &out)
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
}
func TestGoalRequestInputAndFreezeRejection(t *testing.T) {
	goalEnv(t)
	var out, err bytes.Buffer
	if c := goalCommand([]string{"new", "--request", "-"}, strings.NewReader("verbatim\nrequest"), &out, &err); c != 0 {
		t.Fatal(c, err.String())
	}
	id := strings.TrimSpace(out.String())
	b, e := os.ReadFile(filepath.Join(goalsDir(), id, "goal.md"))
	if e != nil || !strings.Contains(string(b), "verbatim\nrequest") {
		t.Fatal(string(b), e)
	}
	if c, _, _ := goalCall("freeze"); c != 2 {
		t.Fatal(c)
	}
	g, e := loadGoal(id)
	if e != nil || g.Status != "draft" {
		t.Fatal(g, e)
	}
}

func TestGoalCheckBadFile(t *testing.T) {
	g := frozenTestGoal(t)
	if e := os.Remove(filepath.Join(g.dir(), "goal.md")); e != nil {
		t.Fatal(e)
	}
	if c, _, _ := goalCall("check"); c != 2 {
		t.Fatal("missing goal file exit", c)
	}
}

func TestGoalJunkDirectory(t *testing.T) {
	g := frozenTestGoal(t)
	if err := os.Mkdir(filepath.Join(goalsDir(), "junk"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := activeGoal("goal-head"); err != nil || got == nil || got.ID != g.ID {
		t.Fatal(got, err)
	}
	if err := os.RemoveAll(g.dir()); err != nil {
		t.Fatal(err)
	}
	release, err := admit("goal-head", admission{Kind: "do"})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if c, _, err := goalCall("new", "--text", "request"); c != 0 {
		t.Fatal(c, err)
	}
}

func TestGoalAdmissionReservations(t *testing.T) {
	g := frozenTestGoal(t)
	if err := withGoal(g.ID, func(g *Goal) error { g.MaxWorkers = 3; return nil }); err != nil {
		t.Fatal(err)
	}
	r, err := newRun("unlinked", "direct", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := withRun(r.ID, func(r *Run) error { r.Goal = ""; r.Workers = make([]*Worker, 5); return nil }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := admit("goal-head", admission{Kind: "do", Serves: []int{1}}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := admit("goal-head", admission{Kind: "do", Serves: []int{1}}); err == nil || !strings.Contains(err.Error(), "budget reached") {
		t.Fatal(err)
	}
	if c, out, err := goalCall("status"); c != 0 || !strings.Contains(out, "workers: 3/3") {
		t.Fatal(c, out, err)
	}
}

func TestGoalAdmissionRefund(t *testing.T) {
	g := frozenTestGoal(t)
	if err := withGoal(g.ID, func(g *Goal) error { g.MaxWorkers = 1; g.FixRounds = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		release, err := admit("goal-head", admission{Kind: "do", Serves: []int{1}, FixOf: "ui"})
		if err != nil {
			t.Fatal(err)
		}
		release()
		got, err := loadGoal(g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.Fixes["ui"]; ok || got.Admitted != 0 {
			t.Fatal(got)
		}
		if got.Events[len(got.Events)-1].What != "release do " {
			t.Fatal(got.Events)
		}
	}
	if _, err := admit("goal-head", admission{Kind: "do", Serves: []int{1}, FixOf: "ui"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunNewUnknownGoal(t *testing.T) {
	goalEnv(t)
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	herdrRun = func(args ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, os.Getpid())), nil
	}
	var out bytes.Buffer
	if c := run([]string{"run", "new", "--text", "request", "--goal", "bogus"}, strings.NewReader(""), &out, &out); c == 0 || !strings.Contains(out.String(), "unknown goal bogus") {
		t.Fatal(c, out.String())
	}
}

func TestGoalScopeSymlink(t *testing.T) {
	g := frozenTestGoal(t)
	// Keep the fixture outside the deliberately allowed /tmp tree.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".orch-scope-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	s, o := filepath.Join(root, "scope"), filepath.Join(root, "outside")
	for _, dir := range []string{s, o} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(o, filepath.Join(s, "link")); err != nil {
		t.Fatal(err)
	}
	if err := withGoal(g.ID, func(g *Goal) error { g.Scope = []string{filepath.Join(s, "**")}; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path string
		deny bool
	}{{filepath.Join(s, "link", "x.go"), true}, {filepath.Join(s, "real.go"), false}} {
		raw, _ := json.Marshal(map[string]string{"file_path": tt.path})
		if reason := goalScopeReason(hookInput{ToolName: "Edit", ToolInput: raw}); (reason != "") != tt.deny {
			t.Fatal(tt, reason)
		}
	}
}

func TestGoalCodexPatchScope(t *testing.T) {
	g := frozenTestGoal(t)
	for _, tt := range []struct {
		name, tool, cwd, patch string
		deny                   bool
	}{
		{"update", "apply_patch", "", "*** Update File: /workspace/src/a.go", false},
		{"add", "namespace__APPLY_PATCH", "", "*** Add File: /workspace/src/new.go", false},
		{"move", "functions.apply_patch", "", "*** Update File: /workspace/src/a.go\n*** Move to: /workspace/src/b.go", false},
		{"outside update", "apply_patch", "", "*** Update File: /workspace/other/a.go", true},
		{"outside add", "apply_patch", "", "*** Add File: /workspace/other/a.go", true},
		{"outside delete", "apply_patch", "", "*** Delete File: /workspace/other/a.go", true},
		{"outside move", "apply_patch", "", "*** Update File: /workspace/src/a.go\n*** Move to: /workspace/other/a.go", true},
		{"relative inside", "apply_patch", "/workspace", "*** Add File:  src/a.go  ", false},
		{"relative outside", "apply_patch", "/workspace/src", "*** Update File: ../other/a.go", true},
		{"multi file", "apply_patch", "", "*** Add File: /workspace/src/a.go\n*** Update File: /workspace/other/b.go", true},
		{"shell heredoc", "Bash", "", "apply_patch <<'PATCH'\n*** Begin Patch\n*** Update File: /workspace/other/a.go\n@@\n-hi\n+hello\n*** End Patch\nPATCH", true},
		{"shell inside", "shell", "/workspace", "apply_patch <<'PATCH'\n*** Begin Patch\n*** Add File: src/a.go\n+hello\n*** End Patch\nPATCH", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"command": tt.patch})
			in := hookInput{ToolName: tt.tool, Cwd: tt.cwd, ToolInput: raw}
			if r := decide(in, "", true); (r != "") != tt.deny {
				t.Fatalf("deny=%v: %s", tt.deny, r)
			}
			if r := decide(in, "worker", true); r != "" {
				t.Fatal(r)
			}
			if r := decide(in, "", false); r != "" {
				t.Fatal(r)
			}
		})
	}
	// Real PreToolUse shape, including cwd and non-command string fields.
	for _, field := range []string{"command", "input", "patch", "other"} {
		data := fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_input":{%q:"*** Update File: ../other/a.go"},"cwd":"/workspace/src"}`, field)
		var out bytes.Buffer
		runHook("codex", strings.NewReader(data), &out, &out)
		if !strings.Contains(out.String(), "outside the active goal") {
			t.Fatal(out.String())
		}
	}
	if err := withGoal(g.ID, func(g *Goal) error { g.Status = "ended"; return nil }); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runHook("codex", strings.NewReader(`{"tool_name":"apply_patch","tool_input":{"command":"*** Update File: /outside"}}`), &out, &out)
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
}

func TestCodexGoalPromptHook(t *testing.T) {
	goalEnv(t)
	for _, tt := range []struct {
		prompt string
		inject bool
	}{
		{"/goal x", true}, {"<command-name>/goal</command-name>", true}, {"hello", false}, {"/goals x", false},
	} {
		raw, _ := json.Marshal(map[string]string{"prompt": tt.prompt})
		var out bytes.Buffer
		runHook("codex-prompt", bytes.NewReader(raw), &out, &out)
		if (out.Len() != 0) != tt.inject {
			t.Fatal(tt, out.String())
		}
		if tt.inject {
			var response struct {
				Output struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Output.Event != "UserPromptSubmit" || !strings.Contains(response.Output.Context, "orch goal new") {
				t.Fatal(out.String(), err)
			}
		}
	}
	for _, mode := range []string{"worker", "outside", "active"} {
		t.Run(mode, func(t *testing.T) {
			goalEnv(t)
			switch mode {
			case "worker":
				t.Setenv("ORCH_ROLE", "worker")
			case "outside":
				t.Setenv("HERDR_ENV", "")
			case "active":
				frozenTestGoal(t)
			}
			var out bytes.Buffer
			runHook("codex-prompt", strings.NewReader(`{"prompt":"/goal x"}`), &out, &out)
			if out.Len() != 0 {
				t.Fatal(out.String())
			}
		})
	}
}

func TestCodexPromptInstall(t *testing.T) {
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	bin := filepath.Join(home(), ".local", "bin", "orch")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, nil, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": "offon-gate", "timeout": float64(30)}}}
	initial := map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{foreign}}}
	if err := os.MkdirAll(filepath.Dir(codexHooks()), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(initial)
	if err := os.WriteFile(codexHooks(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := install(repo, false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(codexHooks())
	if err != nil {
		t.Fatal(err)
	}
	var installed map[string]any
	if err := json.Unmarshal(before, &installed); err != nil {
		t.Fatal(err)
	}
	groups := installed["hooks"].(map[string]any)["UserPromptSubmit"].([]any)
	if len(groups) != 2 {
		t.Fatal(string(before))
	}
	kept, _ := json.Marshal(groups[0])
	want, _ := json.Marshal(foreign)
	if !bytes.Equal(kept, want) {
		t.Fatal(string(kept))
	}
	command := groups[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if !strings.HasSuffix(command, " hook codex-prompt") || !ownHook.MatchString(command) {
		t.Fatal(command)
	}
	if _, err := install(repo, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(codexHooks())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("second install changed hooks", err)
	}
}

func TestGoalCodexPatchPathResolution(t *testing.T) {
	g := frozenTestGoal(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := withGoal(g.ID, func(g *Goal) error { g.Scope = []string{filepath.Join(wd, "**")}; return nil }); err != nil {
		t.Fatal(err)
	}
	if reason := decide(hookJSON("apply_patch", map[string]string{"patch": "*** Add File: relative.go"}), "", true); reason != "" {
		t.Fatal("cwd fallback:", reason)
	}
	// Use a home directory fixture: temporary directories are always allowed.
	root, err := os.MkdirTemp(home(), ".orch-codex-scope-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	scope := filepath.Join(root, "scope")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{scope, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(scope, "link")); err != nil {
		t.Fatal(err)
	}
	if err := withGoal(g.ID, func(g *Goal) error { g.Scope = []string{filepath.Join(scope, "**")}; return nil }); err != nil {
		t.Fatal(err)
	}
	in := hookJSON("apply_patch", map[string]string{"input": "*** Add File: link/new.go"})
	in.Cwd = scope
	if reason := decide(in, "", true); !strings.Contains(reason, "outside the active goal") {
		t.Fatal("symlink escape allowed:", reason)
	}
}
