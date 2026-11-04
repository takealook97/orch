package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMeasurementEvidence(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	m := Measurement{Version: 1, ID: "m", Session: "s", Period: "p", Subject: "head", Entity: "head", Phase: "contract", Source: "manual", Kind: "missing", Coverage: "unavailable"}
	if err := recordMeasurement(m); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recordMeasurement(m); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var b bytes.Buffer
	if err := measurementCommand([]string{"report"}, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"n_missing":1`) || !strings.Contains(b.String(), `"n_total":1`) {
		t.Fatal(b.String())
	}
	data, _ := os.ReadFile(measurementPath())
	if !bytes.Contains(data, []byte(`"input_tokens":null`)) {
		t.Fatal(string(data))
	}
	zero := int64(0)
	m.Input = &zero
	if recordMeasurement(m) == nil {
		t.Fatal("missing accepted a value")
	}
	m.Kind = "measured"
	m.Coverage = "partial"
	m.Output = &zero
	if recordMeasurement(m) == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	m.ID = "zero"
	if err := recordMeasurement(m); err != nil {
		t.Fatal(err)
	}
	m.ID = "fix"
	m.Event = "accepted_fix"
	m.Related = "absent"
	if recordMeasurement(m) == nil {
		t.Fatal("unlinked fix accepted")
	}
	m.Related = "m"
	if err := recordMeasurement(m); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(measurementPath(), os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("{broken\n")
	f.Close()
	m.ID = "next"
	if recordMeasurement(m) == nil {
		t.Fatal("corrupt log overwritten")
	}
}

func TestHeadExecutionGuardManaged(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	shell := func(c string) hookInput {
		v, _ := json.Marshal(map[string]string{"command": c})
		return hookInput{ToolName: "Bash", ToolInput: v}
	}
	for _, cmd := range []string{"go test ./...", "make check", "git commit -am fix", "python3 script.py", "sed -i '' s/a/b/ x", "echo x > x", "ORCH_RAW_HERDR=1 make check", "orch status; go build ."} {
		if decide(shell(cmd), "", true) == "" {
			t.Errorf("Head allowed %s", cmd)
		}
		if decide(shell(cmd), "worker", true) != "" && !strings.Contains(cmd, "orch status") {
			t.Errorf("worker blocked %s", cmd)
		}
	}
	for _, cmd := range []string{"rg -n foo .", "cat main.go", "git status --short", "git diff", "orch test 'go test ./...'", "orch do --model codex-medium-m --own '*.go' 'fix it'"} {
		if r := decide(shell(cmd), "", true); r != "" {
			t.Errorf("inspection/orchestration blocked %s: %s", cmd, r)
		}
	}
	edit := func(p string) hookInput {
		v, _ := json.Marshal(map[string]string{"path": p, "content": "make check"})
		return hookInput{ToolName: "write_file", ToolInput: v}
	}
	if decide(edit("main.go"), "", true) == "" {
		t.Fatal("Head edit permitted")
	}
	for _, p := range []string{filepath.Join(runDir("run-1"), "contracts", "x.md"), filepath.Join(runDir("run-1"), "acceptance.md"), filepath.Join(runDir("run-1"), "decisions.md")} {
		if r := decide(edit(p), "", true); r != "" {
			t.Fatal(p, r)
		}
	}
	if decide(edit(filepath.Join(runDir("run-1"), "task.go")), "", true) == "" {
		t.Fatal("arbitrary run file allowed")
	}
	t.Setenv("ORCH_RUN", "")
	t.Setenv("ORCH_HEAD_GUARD", "")
	if r := decide(edit("main.go"), "", false); r != "" {
		t.Fatal("ordinary session blocked", r)
	}
	if r := decide(shell("go test ./..."), "", true); r != "" {
		t.Fatal("unmanaged Herdr session blocked", r)
	}
}

func TestUninstallOwnership(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	repo := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(p), 0700)
		if e := os.WriteFile(p, []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
	}
	sharedBin := filepath.Join(home(), ".local/bin/orch")
	os.MkdirAll(filepath.Dir(sharedBin), 0700)
	os.Symlink(filepath.Join(repo, "bin/orch"), sharedBin)
	original := "User rules\n\n"
	p := constitutionFiles()[0]
	write(p, original+markBegin+"\n"+strings.TrimSpace(constitutionBlock)+"\n"+markEnd)
	cmd := hookCommand(filepath.Join(home(), ".local/bin/orch"), "claude")
	config := map[string]any{"other": "keep", "hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"command": cmd}, map[string]any{"command": "echo user"}}}}}}
	config["hooks"].(map[string]any)["UserPromptSubmit"] = []any{}
	config["hooks"].(map[string]any)["PreToolUse"] = append(config["hooks"].(map[string]any)["PreToolUse"].([]any), map[string]any{"matcher": "user-empty", "hooks": []any{}})
	data, _ := json.Marshal(config)
	write(claudeSettings(), string(data))
	link := skillLinks()[0]
	os.MkdirAll(filepath.Dir(link), 0700)
	os.Symlink(filepath.Join(repo, "skill"), link)
	real := skillLinks()[1]
	os.MkdirAll(real, 0700)
	write(filepath.Join(real, "mine"), "preserve")
	state := filepath.Join(orchHome(), "runs", "mine", "run.json")
	write(state, "history")
	before, _ := os.ReadFile(p)
	if _, e := uninstall(repo, false); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("dry run changed file")
	}
	if fileExists(filepath.Join(orchHome(), "backups")) {
		t.Fatal("dry run created backup")
	}
	if _, e := uninstall(repo, true); e != nil {
		t.Fatal(e)
	}
	after, _ = os.ReadFile(p)
	if string(after) != original {
		t.Fatal(string(after))
	}
	s, e := readJSONFile(claudeSettings())
	if e != nil {
		t.Fatal(e)
	}
	hooks := s["hooks"].(map[string]any)
	if list, ok := hooks["UserPromptSubmit"].([]any); !ok || len(list) != 0 {
		t.Fatal("user empty event lost", hooks)
	}
	groups := hooks["PreToolUse"].([]any)
	if list, ok := groups[1].(map[string]any)["hooks"].([]any); !ok || len(list) != 0 {
		t.Fatal("empty hook group corrupted", groups)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "hook claude") || !strings.Contains(string(b), "echo user") {
		t.Fatal(string(b))
	}
	if _, e = os.Lstat(link); !os.IsNotExist(e) {
		t.Fatal("owned symlink kept")
	}
	if !fileExists(filepath.Join(real, "mine")) || !fileExists(state) {
		t.Fatal("user files lost")
	}
	if _, e = uninstall(repo, true); e != nil {
		t.Fatal(e)
	}
	if e = safeInstallLink(real, filepath.Join(repo, "skill")); e == nil {
		t.Fatal("reinstall would replace directory")
	}
	os.Symlink("/foreign", link)
	if e = safeInstallLink(link, filepath.Join(repo, "skill")); e == nil {
		t.Fatal("foreign link accepted")
	}
}

func TestUninstallFailsSafe(t *testing.T) {
	for _, bad := range []string{"null", "{broken", `{"hooks":{"PreToolUse":null}}`} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
			p := constitutionFiles()[0]
			os.MkdirAll(filepath.Dir(p), 0700)
			body := spliceBlock("user", constitutionBlock)
			os.WriteFile(p, []byte(body), 0600)
			os.WriteFile(claudeSettings(), []byte(bad), 0600)
			if _, e := uninstall(t.TempDir(), true); e == nil {
				t.Fatal("bad JSON accepted")
			}
			after, _ := os.ReadFile(p)
			if string(after) != body {
				t.Fatal("mutated before validation")
			}
		})
	}
}

func TestInstallUninstallManifestAndForeignData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "skill"), 0700)
	os.WriteFile(filepath.Join(repo, "skill", "SKILL.md"), []byte("skill"), 0600)
	// A foreign command whose executable happens to be named orch must survive.
	os.MkdirAll(filepath.Dir(claudeSettings()), 0700)
	foreign := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"'/foreign/orch' hook claude"}]}]}}`
	os.WriteFile(claudeSettings(), []byte(foreign), 0600)
	if _, e := install(repo, false); e != nil {
		t.Fatal(e)
	}
	m, e := loadOwnership()
	if e != nil || m == nil || m.Repo != repo {
		t.Fatal(m, e)
	}
	if _, e := install(repo, false); e != nil {
		t.Fatal("reinstall", e)
	}
	if _, e := uninstall(repo, true); e != nil {
		t.Fatal(e)
	}
	data, _ := os.ReadFile(claudeSettings())
	if !strings.Contains(string(data), "/foreign/orch") {
		t.Fatal(string(data))
	}
	var doc map[string]any
	json.Unmarshal(data, &doc)
	hs := doc["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(hs) != 1 {
		t.Fatal(string(data))
	}
	// Modified generated blocks and foreign symlinks are preserved.
	p := constitutionFiles()[0]
	modified := spliceBlock("user", "custom rules")
	os.WriteFile(p, []byte(modified), 0600)
	link := skillLinks()[0]
	os.Symlink("/foreign", link)
	if _, e := uninstall(repo, true); e != nil {
		t.Fatal(e)
	}
	data, _ = os.ReadFile(p)
	if string(data) != modified {
		t.Fatal("modified block removed")
	}
	if target, _ := os.Readlink(link); target != "/foreign" {
		t.Fatal(target)
	}
	// Ambiguous markers and malformed ownership stop before any mutation.
	os.WriteFile(p, []byte(markBegin+"\n"+markBegin+"\n"+markEnd), 0600)
	if _, e := uninstall(repo, true); e == nil {
		t.Fatal("ambiguous markers accepted")
	}
	os.WriteFile(ownershipPath(), []byte("null"), 0600)
	if _, e := uninstall(repo, true); e == nil {
		t.Fatal("invalid ownership accepted")
	}
}

func TestPaneRecoveryEvidence(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	m := Measurement{Version: 1, ID: "retry-recovery", Session: "run", Period: "run", Subject: "pane", Entity: "retry", Phase: "rework", Source: "orch-fallback", Kind: "missing", Coverage: "unavailable", Event: "recovery_started", Reason: "quota"}
	if e := recordMeasurement(m); e != nil {
		t.Fatal(e)
	}
	w := &Worker{Name: "retry", FallbackOf: "original", Role: "produce"}
	if e := recordPaneCompletion("run", w, "done"); e != nil {
		t.Fatal(e)
	}
	if e := recordPaneCompletion("run", w, "done"); e != nil {
		t.Fatal(e)
	}
	rows, e := loadMeasurements()
	if e != nil || len(rows) != 2 || rows[1].Event != "recovered" || rows[1].Related != m.ID {
		t.Fatal(rows, e)
	}
	if e := recordPaneCompletion("run", &Worker{Name: "old", FallbackOf: "legacy"}, "failed"); e != nil {
		t.Fatal(e)
	}
	rows, _ = loadMeasurements()
	if rows[2].Related != "" || rows[2].Event != "failure" {
		t.Fatal("invented legacy recovery")
	}
}

func TestServesRecoveryExamples(t *testing.T) {
	for _, bad := range []string{"0", "-1", "bad", "1,,2"} {
		_, e := parseServes(bad)
		if e == nil || !strings.Contains(e.Error(), "--serves 1,2") || !strings.Contains(e.Error(), "goal status") {
			t.Fatal(bad, e)
		}
	}
	if n, e := parseServes("1,2"); e != nil || len(n) != 2 {
		t.Fatal(n, e)
	}
}

func TestInstallForeignBinaryPreflight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "skill"), 0700)
	os.WriteFile(filepath.Join(repo, "skill", "SKILL.md"), []byte("skill"), 0600)
	linked := filepath.Join(home(), ".local/bin/orch")
	os.MkdirAll(filepath.Dir(linked), 0700)
	os.Symlink("/foreign", linked)
	if _, e := install(repo, false); e == nil {
		t.Fatal("foreign binary allowed")
	}
	if fileExists(constitutionFiles()[0]) {
		t.Fatal("preflight mutated constitution")
	}
	if p, _ := os.Readlink(linked); p != "/foreign" {
		t.Fatal("foreign binary replaced")
	}
	os.Remove(linked)
	os.WriteFile(linked, []byte("foreign binary"), 0700)
	if _, e := install(repo, false); e == nil {
		t.Fatal("foreign regular binary allowed")
	}
}
