package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCatalogFixed(t *testing.T) {
	if len(catalog) != 7 {
		t.Fatalf("catalog has %d keys, want 7", len(catalog))
	}
	families := map[string]bool{}
	vars := map[string]bool{}
	for _, m := range catalog {
		families[m.Family] = true
		if vars[m.ModelVar] {
			t.Errorf("%s: variable %s shared", m.Key, m.ModelVar)
		}
		vars[m.ModelVar] = true
		if args, err := modelArgs(m); err != nil || !slices.Contains(args, "--model") {
			t.Errorf("%s: model not pinned", m.Key)
		}
	}
	if len(families) != 3 {
		t.Fatalf("families = %v", families)
	}
	if _, err := modelByKey("gpt-5"); err == nil {
		t.Fatal("unknown key accepted")
	}
	for purpose, chain := range chains {
		for _, k := range chain {
			if _, err := modelByKey(k); err != nil {
				t.Errorf("chain %s: %v", purpose, err)
			}
		}
	}
}

func joinArgs(t *testing.T, m Model, noDaemon bool) string {
	t.Helper()
	args, err := launchArgs(m, noDaemon)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(args, " ")
}

func TestLaunchArgs(t *testing.T) {
	for _, m := range catalog {
		t.Setenv(m.ModelVar, "")
	}
	m, _ := modelByKey("sol-m")
	got := joinArgs(t, m, true)
	want := `--dangerously-bypass-approvals-and-sandbox --no-daemon --model gpt-6-sol -c model_reasoning_effort="medium"`
	if got != want {
		t.Fatalf("got %q", got)
	}
	m, _ = modelByKey("opus-m")
	if got := joinArgs(t, m, false); got != "--dangerously-skip-permissions --model opus --effort medium" {
		t.Fatalf("got %q", got)
	}
	m, _ = modelByKey("flash")
	if got := joinArgs(t, m, false); got != "--dangerously-skip-permissions --model gemini-3.8-flash-medium" {
		t.Fatalf("got %q", got)
	}
}

func TestModelOverride(t *testing.T) {
	t.Setenv(varLunaH, "gpt-7-luna")
	t.Setenv(varLunaM, "")
	t.Setenv(varOpusM, "claude-opus-6")
	m, _ := modelByKey("luna-h")
	if got := joinArgs(t, m, false); got != `--dangerously-bypass-approvals-and-sandbox --model gpt-7-luna -c model_reasoning_effort="high"` {
		t.Fatalf("got %q", got)
	}
	if got := joinArgs(t, key("luna-m"), false); !strings.Contains(got, "--model gpt-6-luna ") {
		t.Fatalf("luna-m followed luna-h override: %q", got)
	}
	m, _ = modelByKey("opus-m")
	if got := joinArgs(t, m, false); got != "--dangerously-skip-permissions --model claude-opus-6 --effort medium" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"--yolo", "gpt 7", `x"y`} {
		t.Setenv(varSolM, bad)
		if _, err := modelArgs(key("sol-m")); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func key(k string) Model { m, _ := modelByKey(k); return m }

func TestLaneConstraints(t *testing.T) {
	cases := []struct {
		role    string
		key     string
		authors []string
		critics []string
		wantErr bool
		warn    bool
	}{
		{roleCritical, "sonnet-m", []string{familyOpenAI}, nil, false, false},
		{roleCritical, "sol-m", []string{familyOpenAI}, nil, true, false},
		{roleCritical, "opus-m", []string{familyOpenAI, familyAnthropic}, nil, true, false},
		{roleCritical, "flash", nil, nil, true, false}, // needs --reviews
		{roleNeutral, "flash", []string{familyOpenAI}, nil, false, false},
		{roleNeutral, "sonnet-m", []string{familyOpenAI}, nil, false, true},
		{roleNeutral, "flash", []string{familyGoogle}, nil, true, false},
		{roleAdversarial, "flash", []string{familyOpenAI}, nil, true, false},
		{roleAdversarial, "opus-m", []string{familyOpenAI}, []string{familyAnthropic}, false, true},
		{roleAdversarial, "sol-m", []string{familyAnthropic}, []string{familyOpenAI}, false, true},
		{roleMech, "luna-h", nil, nil, false, false},
		{roleProduce, "flash", nil, nil, false, false},
	}
	for _, c := range cases {
		w, err := checkLane(c.role, key(c.key), c.authors, c.critics)
		if (err != nil) != c.wantErr {
			t.Errorf("%s/%s: err=%v wantErr=%v", c.role, c.key, err, c.wantErr)
		}
		if (len(w) > 0) != c.warn {
			t.Errorf("%s/%s: warnings=%v want=%v", c.role, c.key, w, c.warn)
		}
	}
}

func TestFallbackRespectsFamily(t *testing.T) {
	// Critical review of an Anthropic author: sonnet-m/opus-m are skipped.
	m, ok := nextFallback("verify-critical", "sol-m", roleCritical, []string{familyAnthropic}, nil, []string{"sol-m"})
	if !ok || m.Key != "luna-h" {
		t.Fatalf("got %v %v", m.Key, ok)
	}
	// Implementation chain follows the user example sol-m → opus-m → sonnet-m.
	m, _ = nextFallback("impl", "sol-m", roleProduce, nil, nil, []string{"sol-m"})
	if m.Key != "opus-m" {
		t.Fatalf("impl fallback = %s", m.Key)
	}
	m, _ = nextFallback("impl", "opus-m", roleProduce, nil, nil, []string{"sol-m", "opus-m"})
	if m.Key != "sonnet-m" {
		t.Fatalf("impl fallback 2 = %s", m.Key)
	}
	// Adversarial never falls back to flash.
	if _, ok := nextFallback("verify-adversarial", "sonnet-m", roleAdversarial, []string{familyOpenAI}, nil, []string{"opus-m", "sonnet-m"}); ok {
		t.Fatal("adversarial fell back to something invalid")
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		p, n string
		ok   bool
	}{
		{"src/**", "src/a/b.go", true},
		{"src/**/*.go", "src/a.go", true},
		{"src/*.go", "src/a/b.go", false},
		{"docs/*.md", "docs/x.md", true},
		{"README.md", "README.md", true},
		{"**/*_test.go", "a/b/c_test.go", true},
	}
	for _, c := range cases {
		if globMatch(c.p, c.n) != c.ok {
			t.Errorf("%s ~ %s != %v", c.p, c.n, c.ok)
		}
	}
	bad := scopeViolations([]string{"src/a.go", "go.mod"}, []string{"src/**"})
	if !slices.Equal(bad, []string{"go.mod"}) {
		t.Fatalf("bad = %v", bad)
	}
}

func TestContractTemplateRoundTrip(t *testing.T) {
	r := &Run{ID: "x"}
	body := contractTemplate(r, "impl-a", roleProduce, "sol-m")
	if err := validateContract(body); err != nil {
		t.Fatal(err)
	}
	if ownedGlobs(body) != nil {
		t.Fatal("template should own nothing")
	}
	body = strings.Replace(body, "Owned files (glob): none", "Owned files (glob): `src/**`, docs/*.md", 1)
	if g := ownedGlobs(body); !slices.Equal(g, []string{"src/**", "docs/*.md"}) {
		t.Fatalf("globs = %v", g)
	}
	body = strings.Replace(body, "base commit: <hash or none>", "base commit: abc123", 1)
	if b := contractField(body, "base commit"); b != "abc123" {
		t.Fatalf("base = %q", b)
	}
	if err := validateContract("# x\n## Goal\n"); err == nil {
		t.Fatal("incomplete contract accepted")
	}
}

func TestResultStatus(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.md")
	if resultStatus(p) != "" {
		t.Fatal("missing file has status")
	}
	os.WriteFile(p, []byte("# Result\n- status: done | blocked | partial\n"), 0600)
	if s := resultStatus(p); s != "" {
		t.Fatalf("untouched template line read as final status %q", s)
	}
	os.WriteFile(p, []byte("# Result: a\n- status: partial\n- summary: x\n"), 0600)
	if s := resultStatus(p); s != "partial" {
		t.Fatalf("status = %q", s)
	}
}

// Layout snapshot captured from Herdr 0.8.2 with Head + 3 workers after uneven resizes.
const layoutJSON = `{"area":{"height":55,"width":165,"x":26,"y":1},"panes":[
{"pane_id":"h","rect":{"height":55,"width":83,"x":26,"y":1}},
{"pane_id":"a","rect":{"height":28,"width":82,"x":109,"y":1}},
{"pane_id":"b","rect":{"height":14,"width":82,"x":109,"y":29}},
{"pane_id":"c","rect":{"height":13,"width":82,"x":109,"y":43}}],
"splits":[
{"direction":"right","id":"split_0_root","ratio":0.5,"rect":{"height":55,"width":165,"x":26,"y":1}},
{"direction":"down","id":"split_1_1","ratio":0.5,"rect":{"height":55,"width":82,"x":109,"y":1}},
{"direction":"down","id":"split_2_11","ratio":0.5,"rect":{"height":27,"width":82,"x":109,"y":29}}]}`

func TestPlanResize(t *testing.T) {
	var l Layout
	if err := json.Unmarshal([]byte(layoutJSON), &l); err != nil {
		t.Fatal(err)
	}
	if err := checkLayout(l, "h"); err != nil {
		t.Fatal(err)
	}
	if layoutOK(l, "h") {
		t.Fatal("uneven layout reported ok")
	}
	ops := planResize(l, "h")
	var gotRoot, gotRow bool
	for _, op := range ops {
		switch {
		case op.Pane == "h":
			gotRoot = op.Direction == "left" && abs(op.Amount-(0.5-headRatio)) < 1e-3
		case op.Pane == "b":
			// split_1: first child {a}, total 3 → target 1/3 < 0.5 → top pane of second child moves up.
			gotRow = op.Direction == "up" && abs(op.Amount-(0.5-1.0/3)) < 1e-3
		}
	}
	if !gotRoot || !gotRow || len(ops) != 2 {
		t.Fatalf("ops = %+v", ops)
	}
	if rowCap(l) != 6 {
		t.Fatalf("rowCap = %d", rowCap(l))
	}
}

func TestCheckLayoutRejectsLeftColumnPane(t *testing.T) {
	l := Layout{Area: Rect{0, 0, 90, 30}, Panes: []LayoutPane{
		{"h", Rect{0, 0, 30, 15}}, {"x", Rect{0, 15, 30, 15}}, {"w", Rect{30, 0, 60, 30}},
	}}
	if checkLayout(l, "h") == nil {
		t.Fatal("split Head accepted")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func hookJSON(tool string, input any) hookInput {
	raw, _ := json.Marshal(input)
	return hookInput{ToolName: tool, ToolInput: raw}
}

func TestHookDecisions(t *testing.T) {
	cases := []struct {
		name    string
		in      hookInput
		role    string
		inHerdr bool
		deny    bool
	}{
		{"head agent in herdr", hookJSON("Agent", map[string]any{"prompt": "x"}), "", true, true},
		{"head workflow in herdr", hookJSON("Workflow", map[string]any{}), "", true, true},
		{"head agent outside herdr", hookJSON("Agent", map[string]any{}), "", false, false},
		{"head raw split", hookJSON("Bash", map[string]any{"command": "herdr pane split --current"}), "", true, true},
		{"head raw split escape", hookJSON("Bash", map[string]any{"command": "ORCH_RAW_HERDR=1 herdr pane split --current"}), "", true, false},
		{"head orch spawn", hookJSON("Bash", map[string]any{"command": "orch spawn --model opus-m"}), "", true, false},
		{"head herdr read", hookJSON("Bash", map[string]any{"command": "herdr pane list"}), "", true, false},
		{"worker agent", hookJSON("Agent", map[string]any{}), "worker", false, true},
		{"worker orch spawn", hookJSON("Bash", map[string]any{"command": "cd x && orch spawn --model opus-m"}), "worker", true, true},
		{"worker codex exec", hookJSON("exec_command", map[string]any{"cmd": []any{"codex", "exec", "hi"}}), "worker", true, true},
		{"worker test run", hookJSON("Bash", map[string]any{"command": "go test ./..."}), "worker", true, false},
		{"worker grep orch", hookJSON("Bash", map[string]any{"command": "grep -r 'orch spawn' docs"}), "worker", true, false},
		{"head prose mention", hookJSON("Bash", map[string]any{"command": "python3 - <<'EOF'\ns='run `herdr pane split --help` once'\nEOF"}), "", true, false},
		{"head chained split", hookJSON("Bash", map[string]any{"command": "cd x && herdr pane split --pane a"}), "", true, true},
		{"head subshell split", hookJSON("Bash", map[string]any{"command": "P=$(herdr pane split --pane a)"}), "", true, true},
		{"head env split", hookJSON("Bash", map[string]any{"command": "FOO=1 herdr agent start x"}), "", true, true},
		{"worker abs orch", hookJSON("Bash", map[string]any{"command": "/Users/x/.local/bin/orch spawn --model opus-m"}), "worker", true, true},
		{"codex namespaced spawn", hookJSON("collaboration.spawn_agent", map[string]any{}), "worker", false, true},
		{"json string args", hookInput{ToolName: "run_command", ToolInput: json.RawMessage(`"{\"CommandLine\":\"herdr pane split --help\"}"`)}, "worker", false, true},
		{"agy args with prose fields", hookInput{ToolCall: &struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}{"run_command", json.RawMessage(`{"CommandLine":"herdr pane split --help","Cwd":"/x","toolAction":"Running herdr command","toolSummary":"Run herdr help"}`)}}, "worker", false, true},
		{"codex unseparated spawn", hookJSON("collaborationspawn_agent", map[string]any{}), "worker", false, true},
		{"codex interrupt allowed", hookJSON("collaborationinterrupt_agent", map[string]any{}), "worker", false, false},
		{"not a subagent", hookJSON("agent_status_read", map[string]any{}), "worker", false, false},
		{"worker bash -c split", hookJSON("exec_command", map[string]any{"cmd": "bash -c 'herdr pane split --no-focus'"}), "worker", true, true},
		{"worker sh -lc split", hookJSON("Bash", map[string]any{"command": `sh -lc "herdr agent start x --kind codex"`}), "worker", true, true},
		{"worker eval split", hookJSON("Bash", map[string]any{"command": `eval "herdr pane split"`}), "worker", true, true},
		{"worker bash -c codex exec", hookJSON("Bash", map[string]any{"command": "bash -c 'codex exec test'"}), "worker", true, true},
		{"worker orch close forged", hookJSON("Bash", map[string]any{"command": "HERDR_PANE_ID=VICTIM orch close --run VICTIM"}), "worker", true, true},
		{"worker env -u orch resume", hookJSON("Bash", map[string]any{"command": "env -u ORCH_ROLE orch resume --run VICTIM"}), "worker", true, true},
		{"worker abs herdr", hookJSON("Bash", map[string]any{"command": "/Users/x/.local/bin/herdr pane split"}), "worker", true, true},
		{"worker writes prose", hookJSON("Bash", map[string]any{"command": "cat > r.md <<'EOF'\n- attempt: `herdr pane split --help` blocked\nEOF"}), "worker", true, false},
		{"worker quote-joined herdr", hookJSON("exec_command", map[string]any{"cmd": "he''rdr pane split --no-focus"}), "worker", true, true},
		{"worker quote-joined orch", hookJSON("exec_command", map[string]any{"cmd": "env -u ORCH_ROLE HERDR_PANE_ID=V or''ch close --run R"}), "worker", true, true},
		{"worker backslash herdr", hookJSON("Bash", map[string]any{"command": `\herdr pane split`}), "worker", true, true},
		{"worker echo quoted mention", hookJSON("Bash", map[string]any{"command": `echo "herdr pane split is blocked"`}), "worker", true, false},
		{"codex spawn_agent", hookJSON("spawn_agent", map[string]any{}), "", true, true},
		{"agy subagent", hookInput{ToolCall: &struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}{"manage_subagents", json.RawMessage(`{}`)}}, "worker", false, true},
		{"agy run_command", hookInput{ToolCall: &struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}{"run_command", json.RawMessage(`{"CommandLine":"herdr agent start x --kind codex"}`)}}, "worker", false, true},
	}
	for _, c := range cases {
		got := decide(c.in, c.role, c.inHerdr) != ""
		if got != c.deny {
			t.Errorf("%s: deny=%v want %v", c.name, got, c.deny)
		}
	}
}

func TestHookOutputShapes(t *testing.T) {
	t.Setenv("ORCH_ROLE", "worker")
	in := `{"tool_name":"Agent","tool_input":{}}`
	var out bytes.Buffer
	runHook("claude", strings.NewReader(in), &out, &out)
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) || !strings.Contains(out.String(), "permissionDecisionReason") {
		t.Fatalf("claude out = %s", out.String())
	}
	out.Reset()
	runHook("agy", strings.NewReader(`{"toolCall":{"name":"manage_subagents","args":{}}}`), &out, &out)
	if !strings.Contains(out.String(), `"decision":"deny"`) {
		t.Fatalf("agy out = %s", out.String())
	}
	out.Reset()
	runHook("agy", strings.NewReader(`{"toolCall":{"name":"view_file","args":{}}}`), &out, &out)
	if strings.TrimSpace(out.String()) != `{"decision":"allow"}` {
		t.Fatalf("agy worker silent = %s", out.String())
	}
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("HERDR_ENV", "")
	out.Reset()
	runHook("agy", strings.NewReader(`{"toolCall":{"name":"view_file","args":{}}}`), &out, &out)
	if strings.TrimSpace(out.String()) != `{"decision":"ask"}` {
		t.Fatalf("agy head silent = %s", out.String())
	}
	out.Reset()
	runHook("claude", strings.NewReader(in), &out, &out)
	if out.Len() != 0 {
		t.Fatalf("silent case wrote %q", out.String())
	}
}

func TestSpliceBlock(t *testing.T) {
	doc := "# mine\n\ntext\n"
	a := spliceBlock(doc, "rule v1")
	if !strings.HasPrefix(a, doc) || !strings.Contains(a, "rule v1") {
		t.Fatalf("append failed: %q", a)
	}
	b := spliceBlock(a+"\ntail\n", "rule v2")
	if strings.Contains(b, "rule v1") || !strings.Contains(b, "rule v2") || !strings.HasSuffix(b, "tail\n") {
		t.Fatalf("replace failed: %q", b)
	}
	if got, ok := extractBlock(b); !ok || got != "rule v2" {
		t.Fatalf("extract = %q %v", got, ok)
	}
}

func TestEnsureGroupedHookIdempotent(t *testing.T) {
	hooks := map[string]any{}
	if !ensureGroupedHook(hooks, "Agent", "'/x/orch' hook claude") {
		t.Fatal("first add not reported")
	}
	if ensureGroupedHook(hooks, "Agent", "'/y/orch' hook claude") {
		t.Fatal("second add duplicated")
	}
	list := hooks["PreToolUse"].([]any)
	if len(list) != 1 || !strings.Contains(list[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string), "/y/orch") {
		t.Fatalf("hooks = %v", hooks)
	}
}

func TestPlaybooksValid(t *testing.T) {
	errs := checkPlaybooks("playbooks")
	for _, e := range errs {
		t.Error(e)
	}
}

func TestPlaybookRejectsBadKey(t *testing.T) {
	p := Playbook{Type: "x", DefaultMode: "full", RetryLimit: 2, Stages: map[string]Stage{
		"integrate": {Mechanics: "sol"},
		"verify":    {Lanes: map[string]Lane{"adversarial": {Model: "flash"}, "critical": {Model: "sonnet-m"}}},
	}}
	errs := validatePlaybook(p)
	if len(errs) != 3 {
		t.Fatalf("errs = %v", errs)
	}
}

func TestRunStateAndResolve(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("ORCH_RUN", "")
	r, err := createRun("request", "full", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"request.md", "acceptance.md", "decisions.md", "findings.md", "run.json"} {
		if !fileExists(filepath.Join(r.dir(), f)) {
			t.Errorf("missing %s", f)
		}
	}
	id, err := resolveRun("")
	if err != nil || id != r.ID {
		t.Fatalf("resolve = %s %v", id, err)
	}
	err = withRun(r.ID, func(r *Run) error {
		r.Workers = append(r.Workers, &Worker{Name: "a", Pane: "w1:p2", Status: "prompted", Family: familyOpenAI})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	owned, _ := ownedPanes("w1:p1")
	if !slices.Equal(owned, []string{"w1:p2"}) {
		t.Fatalf("owned = %v", owned)
	}
	t.Setenv("HERDR_PANE_ID", "w1:p9")
	if _, err := resolveRun(""); err == nil {
		t.Fatal("another Head resolved this run")
	}
}

func TestSpawnRefusesWorker(t *testing.T) {
	t.Setenv("ORCH_ROLE", "worker")
	if _, err := spawn(spawnOpts{Key: "opus-m", Name: "x", Role: roleProduce}); err == nil || !strings.Contains(err.Error(), "worker") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	if _, err := spawn(spawnOpts{Key: "gpt-5", Name: "x", Role: roleProduce}); err == nil || !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("err = %v", err)
	}
}

func TestQuotaText(t *testing.T) {
	if !quotaText("Error: You've hit your usage limit. Try again at 5pm") || quotaText("all good") {
		t.Fatal("quota detection")
	}
}

func TestParseFlagsAfterPositional(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	var out, errb bytes.Buffer
	code := run([]string{"prompt"}, strings.NewReader(""), &out, &errb)
	if code == 0 {
		t.Fatal("prompt without name accepted")
	}
}

func TestCloseRefusesOtherHeadsRun(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	t.Setenv("HERDR_PANE_ID", "w1:p9")
	if _, err := closeRun(r.ID, true); err == nil || !strings.Contains(err.Error(), "another Head") {
		t.Fatalf("err = %v", err)
	}
}

func TestFallbackContractRewrite(t *testing.T) {
	r := &Run{ID: "x"}
	body := contractTemplate(r, "fb", roleRecon, "luna-m")
	note := "\n## Fallback handoff\n- previous partial result: " + r.resultPath("fb") + "\n"
	got := fallbackContract(r, body, "fb", "fb-f1", note)
	if !strings.Contains(got, "# Contract: fb-f1") || !strings.Contains(got, "# Result: fb-f1\n") {
		t.Fatal("names not rewritten")
	}
	if strings.Contains(got, "# Result: fb\n") {
		t.Fatal("old result title survived")
	}
	if !strings.Contains(got, "previous partial result: "+r.resultPath("fb")) {
		t.Fatal("handoff lost the old result path")
	}
	if strings.Count(got, r.resultPath("fb-f1")) < 1 {
		t.Fatal("new result path missing")
	}
}

func TestWorkerCannotDriveOrch(t *testing.T) {
	t.Setenv("ORCH_ROLE", "worker")
	var out, errb bytes.Buffer
	for _, args := range [][]string{{"close", "--all"}, {"run", "end"}, {"resume", "--run", "x"}, {"gc", "--orphans"}, {"fallback", "a"}} {
		errb.Reset()
		if code := run(args, strings.NewReader(""), &out, &errb); code == 0 || !strings.Contains(errb.String(), "Head only") {
			t.Errorf("%v: code=%d err=%s", args, code, errb.String())
		}
	}
	if code := run([]string{"models"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatal("models should stay readable for workers")
	}
}

func TestFallbackNameKeepsValidName(t *testing.T) {
	r := &Run{}
	long := "a" + strings.Repeat("b", 31) // 32 chars, valid
	n := fallbackName(r, long)
	if !nameRE.MatchString(n) || len(n) > 32 || !strings.HasSuffix(n, "-f1") {
		t.Fatalf("fallback name %q invalid", n)
	}
	if n := fallbackName(r, "impl-f1"); n != "impl-f1" && n != "impl-f2" {
		t.Fatalf("got %q", n)
	}
}

func gitInit(t *testing.T) string {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestChangedFilesWithoutHEAD(t *testing.T) {
	dir := gitInit(t)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0600)
	files, err := changedFiles(dir, "none")
	if err != nil {
		t.Fatalf("unborn HEAD should not fail: %v", err)
	}
	if !slices.Contains(files, "a.go") {
		t.Fatalf("files = %v", files)
	}
}

func TestFreshChangesSeesEditsToDirtyFiles(t *testing.T) {
	dir := gitInit(t)
	p := filepath.Join(dir, "secret.txt")
	os.WriteFile(p, []byte("v1"), 0600)
	before, _ := changedFiles(dir, "")
	base := snapshot(dir, before)
	if got := freshChanges(dir, before, base); len(got) != 0 {
		t.Fatalf("untouched dirty file reported: %v", got)
	}
	os.WriteFile(p, []byte("v2"), 0600) // the worker edits an already-dirty file
	after, _ := changedFiles(dir, "")
	if got := freshChanges(dir, after, base); !slices.Equal(got, []string{"secret.txt"}) {
		t.Fatalf("edit to dirty file missed: %v", got)
	}
}

// fakeHerdr swaps the herdr runner; list controls the pane list (nil = list error).
func fakeHerdr(t *testing.T, list func() ([]byte, error)) {
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pane" && args[1] == "list" {
			return list()
		}
		if len(args) >= 2 && args[0] == "pane" && args[1] == "close" {
			return nil, errors.New("close failed")
		}
		return []byte(`{"result":{}}`), nil
	}
}

func TestClosePaneListFailureIsNotSuccess(t *testing.T) {
	fakeHerdr(t, func() ([]byte, error) { return nil, errors.New("socket down") })
	if err := closePane("w1:p2"); err == nil {
		t.Fatal("closePane reported success although the pane list failed")
	}
}

func TestClosePaneAlreadyGone(t *testing.T) {
	fakeHerdr(t, func() ([]byte, error) { return []byte(`{"result":{"panes":[{"pane_id":"w1:p1"}]}}`), nil })
	if err := closePane("w1:p2"); err != nil {
		t.Fatalf("absent pane: %v", err)
	}
}

func TestResumeRefusesLiveHead(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	fakeHerdr(t, func() ([]byte, error) {
		return []byte(`{"result":{"panes":[{"pane_id":"w1:p1"},{"pane_id":"w1:p9"}]}}`), nil
	})
	t.Setenv("HERDR_PANE_ID", "w1:p9")
	if _, err := resume(r.ID); err == nil || !strings.Contains(err.Error(), "still alive") {
		t.Fatalf("err = %v", err)
	}
	// Once the original Head is gone, the run can be taken over.
	fakeHerdr(t, func() ([]byte, error) { return []byte(`{"result":{"panes":[{"pane_id":"w1:p9"}]}}`), nil })
	if _, err := resume(r.ID); err != nil {
		t.Fatalf("takeover of dead Head's run: %v", err)
	}
}

func TestSpawnRefusesOtherHeadsRun(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	t.Setenv("HERDR_PANE_ID", "w1:p9")
	_, err := spawn(spawnOpts{Run: r.ID, Key: "sol-m", Name: "x", Role: roleProduce})
	if err == nil || !strings.Contains(err.Error(), "another Head") {
		t.Fatalf("err = %v", err)
	}
}

func TestLockHeadSerializes(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	unlock, err := lockHead("w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u, err := lockHead("w1:p1")
		if err == nil {
			u()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second spawn entered while the first held the Head lock")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("lock not released")
	}
}

func TestFallbackLineageIsBounded(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	_ = withRun(r.ID, func(r *Run) error {
		r.Workers = append(r.Workers, &Worker{Name: "a", Key: "luna-m", Purpose: "explore", Contract: "c.md",
			Tried: []string{"luna-m", "luna-h", "flash"}, TriedAccounts: []string{"A", "B", "C"}})
		return nil
	})
	if _, err := doFallback(r.ID, "a", "quota exhausted"); err == nil || !strings.Contains(err.Error(), "fallback limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyCaller(t *testing.T) {
	old, oldPP := herdrRun, parentPID
	t.Cleanup(func() { herdrRun, parentPID = old, oldPP })
	herdrRun = func(args ...string) ([]byte, error) {
		return []byte(`{"result":{"process_info":{"shell_pid":500}}}`), nil
	}
	// Ancestry: self -> 900 -> 500 (the Head's shell) -> 1
	parentPID = func(pid int) (int, error) {
		switch pid {
		case 500:
			return 1, nil
		case 900:
			return 500, nil
		}
		return 900, nil
	}
	if err := verifyCaller("w1:p1"); err != nil {
		t.Fatalf("real Head rejected: %v", err)
	}
	// A worker forging HERDR_PANE_ID has another ancestry: self -> 700 -> 1
	parentPID = func(pid int) (int, error) {
		if pid == 700 {
			return 1, nil
		}
		return 700, nil
	}
	if err := verifyCaller("w1:p1"); err == nil {
		t.Fatal("forged HERDR_PANE_ID accepted")
	}
}

func TestHookAccountRejectsExcluded(t *testing.T) {
	t.Setenv("ORCH_ACCOUNT_CMD", "f(){ echo LABEL=A; }; f")
	if _, err := hookAccount("codex", []string{"A"}); err == nil {
		t.Fatal("excluded account reused")
	}
	if acc, err := hookAccount("codex", []string{"B"}); err != nil || acc.Label != "A" {
		t.Fatalf("acc=%v err=%v", acc, err)
	}
}

func TestWithRunDoesNotPersistFailure(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	_ = withRun(r.ID, func(r *Run) error {
		r.HeadPane = "w1:p9"
		return errors.New("boom")
	})
	got, _ := loadRun(r.ID)
	if got.HeadPane != "w1:p1" {
		t.Fatalf("failed mutation persisted: %s", got.HeadPane)
	}
}

func TestWaitReportsCloseFailure(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, _ := createRun("x", "light", "", "")
	os.WriteFile(r.resultPath("a"), []byte("# Result: a\n- status: done\n"), 0600)
	_ = withRun(r.ID, func(x *Run) error {
		x.Workers = append(x.Workers, &Worker{Name: "a", Pane: "w1:p2", Status: "prompted", Result: r.resultPath("a")})
		return nil
	})
	fakeHerdr(t, func() ([]byte, error) { return nil, errors.New("socket down") })
	outs, err := waitWorkers(r.ID, []string{"a"}, time.Second, false)
	if err != nil || len(outs) != 1 || outs[0].Status != "close-failed" {
		t.Fatalf("outs=%+v err=%v", outs, err)
	}
}

func TestTrustClaudeRecordsExactFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".claude.json")
	os.WriteFile(file, []byte(`{"numStartups":3,"projects":{"/a":{"hasTrustDialogAccepted":true}}}`), 0600)
	if err := trustClaude(file, "/a/b/c"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	if err := trustClaude(file, "/a/b/c"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("rewrote config although the folder was already recorded")
	}
	var cfg map[string]any
	json.Unmarshal(before, &cfg)
	p := cfg["projects"].(map[string]any)["/a/b/c"].(map[string]any)
	if p["hasTrustDialogAccepted"] != true || cfg["numStartups"].(float64) != 3 {
		t.Fatalf("cfg = %v", cfg)
	}
}

func TestTrustAgyIdempotent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(file, []byte(`{"model":"m","trustedWorkspaces":["/a"]}`), 0600)
	trustAgy(file, "/b")
	trustAgy(file, "/b")
	var cfg map[string]any
	data, _ := os.ReadFile(file)
	json.Unmarshal(data, &cfg)
	if l := cfg["trustedWorkspaces"].([]any); len(l) != 2 || cfg["model"] != "m" {
		t.Fatalf("cfg = %v", cfg)
	}
}

func TestPretrustScope(t *testing.T) {
	wd, _ := os.Getwd()
	args, err := pretrust("codex", filepath.Join(wd, "eval"), nil)
	if err != nil || len(args) != 2 || !strings.Contains(args[1], `trust_level="trusted"`) {
		t.Fatalf("args=%v err=%v", args, err)
	}
	if args, _ := pretrust("codex", "/etc", nil); args != nil {
		t.Fatalf("trusted a folder outside the Head's cwd: %v", args)
	}
}

// Herdr moves UI focus on pane.zoom and agent.prompt; orch must not issue them needlessly.
func TestNormalizeLayoutDoesNotZoomWhenNotZoomed(t *testing.T) {
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	var calls []string
	herdrRun = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "pane" && args[1] == "layout" {
			return []byte(`{"result":{"layout":{"area":{"x":0,"y":0,"width":90,"height":30},"zoomed":false,
				"panes":[{"pane_id":"h","rect":{"x":0,"y":0,"width":30,"height":30}},{"pane_id":"a","rect":{"x":30,"y":0,"width":60,"height":30}}],
				"splits":[{"id":"r","direction":"right","ratio":0.3333,"rect":{"x":0,"y":0,"width":90,"height":30}}]}}}`), nil
		}
		return []byte(`{"result":{}}`), nil
	}
	if err := normalizeLayout("h"); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if strings.Contains(c, "zoom") || strings.Contains(c, "focus") {
			t.Fatalf("focus-moving call issued: %s", c)
		}
	}
}

func TestTrustScopeRejectsSymlinkEscape(t *testing.T) {
	head := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(head, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(head, "sub"), 0700)
	if within(head, link) {
		t.Fatal("symlink to an outside folder treated as inside the Head cwd")
	}
	if !within(head, filepath.Join(head, "sub")) || !within(head, head) {
		t.Fatal("real subfolder rejected")
	}
	if within(head, filepath.Join(head, "missing")) {
		t.Fatal("unresolvable path accepted")
	}
}

func TestHookBlocksOrchForWorkers(t *testing.T) {
	for _, cmd := range []string{"orch spawn --model opus-m", "orch close --all", "/x/bin/orch resume --run r"} {
		if decide(hookJSON("Bash", map[string]any{"command": cmd}), "worker", true) == "" {
			t.Errorf("worker allowed: %s", cmd)
		}
	}
	if !ownHook.MatchString("'/Users/x/.local/bin/orch' hook agy") {
		t.Error("own hook detection")
	}
}
