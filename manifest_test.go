package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func overlayFile(t *testing.T, s string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCH_MODELS_FILE", p)
}
func TestManifestEmbedded(t *testing.T) {
	mf, err := loadManifest(embeddedModels)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mf.catalog()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, catalog) {
		t.Fatalf("embedded catalog differs: %#v", got)
	}
	if len(got) != 7 || len(mf.Models) != 6 || mf.Keys[2].Model != "sol" || mf.Keys[3].Model != "sol" {
		t.Fatalf("unexpected catalog: %#v", mf)
	}
	if !reflect.DeepEqual(mf.Fallbacks, chains) {
		t.Fatal("fallbacks differ")
	}
}
func TestManifestOverlay(t *testing.T) {
	cleanModelEnv(t)
	overlayFile(t, "version: 1\nmodels:\n  sol: future-sol\nkeys:\n  - key: codex-high-l\n    effort: high\n")
	for _, key := range []string{"codex-high-l", "codex-high-m"} {
		m, err := modelByKey(key)
		if err != nil {
			t.Fatal(err)
		}
		id, source, err := resolvedModel(m)
		if err != nil || id != "future-sol" || source != "overlay" {
			t.Fatalf("%s: %s %s %v", key, id, source, err)
		}
		want := "medium"
		if key == "codex-high-l" {
			want = "high"
		}
		if m.Effort != want {
			t.Fatalf("effort %s", m.Effort)
		}
	}
	t.Setenv("CODEX_HIGH_L", "env-sol")
	m, _ := modelByKey("codex-high-l")
	id, source, err := resolvedModel(m)
	if err != nil || id != "env-sol" || source != "env" {
		t.Fatalf("env: %s %s %v", id, source, err)
	}
	t.Setenv("CODEX_MEDIUM_H", "legacy")
	m, _ = modelByKey("codex-medium-h")
	id, _, _ = resolvedModel(m)
	if id != "legacy" {
		t.Fatal(id)
	}
	t.Setenv("CODEX_LOW_H", "canonical")
	id, _, _ = resolvedModel(m)
	if id != "canonical" {
		t.Fatal(id)
	}
}
func TestManifestInvalidOverlay(t *testing.T) {
	cleanModelEnv(t)
	for _, s := range []string{"models: {unknown: id}", "models: {sol: '--bad'}", "models: {sol: ''}", "keys: [{key: codex-high-l, kind: claude}]", "keys: [{key: missing, effort: high}]", "keys: [{key: codex-high-l, effort: nonsense}]", "keys: [{key: codex-high-l, effort: low}, {key: codex-high-l, effort: high}]", "aliases: {x: codex-low-h}", "fallbacks: {x: [codex-low-h]}", "version: 2", "unexpected: true", "models: {sol: id}\n---\nmodels: {sol: other}"} {
		t.Run(s, func(t *testing.T) {
			overlayFile(t, s)
			if _, err := modelByKey("codex-high-l"); err == nil {
				t.Fatalf("accepted %s", s)
			}
		})
	}
}
func TestManifestInvalidEmbedded(t *testing.T) {
	for _, change := range []struct{ old, new string }{{"model: luna", "model: missing"}, {"key: codex-medium-m", "key: codex-low-h"}, {"codex-medium-h: codex-low-h", "codex-medium-h: absent"}, {"image: [codex-high-m]", "image: [absent]"}} {
		_, err := loadManifest([]byte(strings.Replace(string(embeddedModels), change.old, change.new, 1)))
		if err == nil {
			t.Fatalf("accepted invalid %s", change.new)
		}
	}
}

func TestManifestTaskRecord(t *testing.T) {
	taskEnv(t)
	cleanModelEnv(t)
	overlayFile(t, "models: {sol: manifest-sol}\n")
	fakeCLI(t, "codex", `echo '{"type":"item.completed","item":{"type":"agent_message","text":"OK"}}'`)
	var out, errout bytes.Buffer
	code := run([]string{"ask", "--model", "codex-high-l", "--cwd", t.TempDir(), "--json", "hello"}, strings.NewReader(""), &out, &errout)
	var task Task
	if err := json.Unmarshal(out.Bytes(), &task); err != nil || code != 0 || task.ModelID != "manifest-sol" {
		t.Fatalf("code %d, task %s, err %v stderr %s", code, out.String(), err, errout.String())
	}
	saved, err := readTask(task.Run, task.ID)
	if err != nil || saved.ModelID != "manifest-sol" {
		t.Fatalf("saved task %+v err %v", saved, err)
	}
	r, err := loadRun(task.Run)
	if err != nil || len(r.Tasks) != 1 || r.Tasks[0].ModelID != "manifest-sol" {
		t.Fatalf("run task %+v err %v", r, err)
	}
}

func TestManifestSpawnAndFallbackRecord(t *testing.T) {
	r, f := overflowFixture(t)
	cleanModelEnv(t)
	overlayFile(t, "models: {sol: first-sol}\n")
	timings.TrustSettlePrompt = 0
	timings.KeyGap = 0
	herdrRun = func(args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "pane send-text", "agent send-keys":
			return []byte(`{"result":{}}`), nil
		case "pane read", "agent read":
			return []byte(`{"result":{"text":"[orch worker first-f1]"}}`), nil
		}
		return f.run(args...)
	}
	w, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-l", Name: "first", Role: roleProduce, Purpose: "mech-impl", Account: "same", Cwd: t.TempDir()})
	if err != nil || w.ModelID != "first-sol" {
		t.Fatalf("spawn %+v %v", w, err)
	}
	if err := os.WriteFile(r.contractPath("first"), []byte(contractTemplate(r, "first", roleProduce, w.Key)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := withRun(r.ID, func(r *Run) error { r.worker("first").Contract = r.contractPath("first"); return nil }); err != nil {
		t.Fatal(err)
	}
	overlayFile(t, "models: {sol: fallback-sol}\n")
	name, err := doFallback(r.ID, "first", "quota")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := loadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.worker("first").ModelID != "first-sol" || saved.worker(name).ModelID != "fallback-sol" || saved.worker(name).Key != "codex-high-m" {
		t.Fatalf("fallback records: %+v %+v", saved.worker("first"), saved.worker(name))
	}
}

func TestManifestResolutionUsesCurrentOverlay(t *testing.T) {
	cleanModelEnv(t)
	overlayFile(t, "models: {sol: temporary-sol}\nkeys: [{key: codex-high-l, effort: high}]\n")
	m, err := modelByKey("codex-high-l")
	if err != nil {
		t.Fatal(err)
	}
	overlayFile(t, "{}\n")
	resolved, id, source, err := resolveModel(m)
	if err != nil || id != embeddedManifest.Models["sol"] || resolved.Effort != "low" || source != "embedded" {
		t.Fatalf("stale overlay: %+v %s %s %v", resolved, id, source, err)
	}
	overlayFile(t, "keys: [{key: codex-high-l, effort: medium}]\n")
	_, id, source, err = resolveModel(m)
	if err != nil || id != embeddedManifest.Models["sol"] || source != "embedded" {
		t.Fatalf("id provenance: %s %s %v", id, source, err)
	}
}
func TestManifestDefaultOverlayPath(t *testing.T) {
	cleanModelEnv(t)
	dir := t.TempDir()
	t.Setenv("ORCH_HOME", dir)
	t.Setenv("ORCH_MODELS_FILE", "")
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte("models: {sol: home-sol}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := modelByKey("codex-high-l")
	if err != nil {
		t.Fatal(err)
	}
	if id, _, err := resolvedModel(m); err != nil || id != "home-sol" {
		t.Fatalf("home overlay: %s %v", id, err)
	}
	overlayFile(t, "models: {sol: custom-sol}\n")
	if id, _, err := resolvedModel(m); err != nil || id != "custom-sol" {
		t.Fatalf("custom overlay: %s %v", id, err)
	}
}

func TestManifestSpawnRecordsInvocationSnapshot(t *testing.T) {
	r, f := overflowFixture(t)
	cleanModelEnv(t)
	overlayFile(t, "models: {sol: first-sol}\n")
	launched := ""
	herdrRun = func(args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "pane split":
			overlayFile(t, "models: {sol: changed-sol}\n")
		case "pane run", "pane wait-output":
			return []byte(`{"result":{}}`), nil
		case "agent start":
			for i, arg := range args {
				if arg == "--model" {
					launched = args[i+1]
				}
			}
		}
		return f.run(args...)
	}
	w, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-l", Name: "snapshot", Role: roleProduce, Account: "same", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if w.ModelID != launched {
		t.Fatalf("recorded %s, launched %s", w.ModelID, launched)
	}
}

func TestManifestTaskRecordsSelectionBeforeLaunch(t *testing.T) {
	taskEnv(t)
	cleanModelEnv(t)
	overlayFile(t, "models: {sol: first-sol}\n")
	task, err := newTask("ask", "codex-high-l", "hello", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if task.ModelID != "first-sol" {
		t.Fatalf("pending task %+v", task)
	}
	overlayFile(t, "models: {sol: changed-sol}\n")
	fakeCLI(t, "codex", `case "$(cat "$ORCH_HOME/runs/$ORCH_RUN/tasks/$ORCH_WORKER/task.json")" in
 *'"model_id": "changed-sol"'*) echo '{"result":"OK"}' ;;
 *) echo '{"result":"model id was not saved before launch"}'; exit 1 ;;
esac`)
	task.Account = "same"
	task.TimeoutMS = 1000
	task.MaxOutput = 2000
	if err := runTask(task); err != nil || task.Status != "done" || task.ModelID != "changed-sol" {
		t.Fatalf("task %+v err %v", task, err)
	}
}

func TestManifestEmptyEffortOverride(t *testing.T) {
	cleanModelEnv(t)
	overlayFile(t, "keys: [{key: codex-low-h, effort: ''}]\n")
	if _, err := modelByKey("codex-low-h"); err == nil {
		t.Fatal("empty effort accepted")
	}
	overlayFile(t, "keys: [{key: gemini-medium-m, effort: ''}]\n")
	if _, err := modelByKey("gemini-medium-m"); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorModelEnvWarning(t *testing.T) {
	cleanModelEnv(t)
	t.Setenv("CODEX_LOW_H", "override")
	found := false
	for _, c := range doctor() {
		if strings.Contains(c.info, "CODEX_LOW_H") {
			found = true
			if !c.ok || !c.warn || !strings.Contains(c.info, "shadows") || !strings.Contains(c.info, "~/.orch/models.yaml") {
				t.Fatalf("%+v", c)
			}
		}
	}
	if !found {
		t.Fatal("missing warning")
	}
}

func TestManifestWorkerDocsDenied(t *testing.T) {
	t.Setenv("ORCH_ROLE", "worker")
	var out, errs bytes.Buffer
	if code := run([]string{"docs", "gen"}, strings.NewReader(""), &out, &errs); code == 0 {
		t.Fatal("worker docs permitted")
	}
}
