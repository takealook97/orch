package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func dagRunFixture(t *testing.T, yamlText string, contracts ...string) *Run {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	r, err := createRun("x", "full", "", "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.dir(), "dag.yaml"), []byte(yamlText), 0600)
	for _, c := range contracts {
		os.WriteFile(r.contractPath(c), []byte(contractTemplate(r, c, roleProduce, "codex-high-m")), 0600)
	}
	return r
}

const goodDag = `nodes:
  - {name: recon, model: codex-medium-h, role: recon, purpose: explore}
  - {name: impl-a, model: codex-high-m, role: produce, purpose: impl, needs: [recon]}
  - {name: impl-b, model: claude-high-m, role: produce, purpose: hard-impl, needs: [recon]}
  - {name: mech-a, model: codex-medium-h, role: verify-mech, needs: [impl-a]}
  - {name: crit-a, model: claude-medium-m, role: verify-critical, reviews: [impl-a], needs: [mech-a]}
  - {name: crit-b, model: codex-high-m, role: verify-critical, reviews: [impl-b]}
  - {name: docs, model: claude-medium-m, role: produce, purpose: docs}
`

func TestDagValidateGood(t *testing.T) {
	r := dagRunFixture(t, goodDag, "recon", "impl-a", "impl-b", "mech-a", "crit-a", "crit-b", "docs")
	d, err := loadDag(r)
	if err != nil {
		t.Fatal(err)
	}
	if errs := validateDag(r, d); len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	// reviews imply needs
	if n := d.node("crit-b"); !slices.Contains(n.Needs, "impl-b") {
		t.Fatalf("crit-b needs = %v", n.Needs)
	}
}

func TestDagValidateRejects(t *testing.T) {
	bad := `nodes:
  - {name: a, model: gpt-5, role: produce, needs: [c]}
  - {name: b, model: codex-high-m, role: produce, needs: [a]}
  - {name: c, model: codex-high-m, role: produce, needs: [b]}
  - {name: crit, model: codex-medium-h, role: verify-critical, reviews: [b]}
  - {name: adv, model: gemini-medium-m, role: verify-adversarial, reviews: [b]}
  - {name: neu, model: gemini-medium-m, role: verify-neutral}
  - {name: nocontract, model: codex-high-m, role: produce, contract: contracts/missing.md}
`
	r := dagRunFixture(t, bad, "a", "b", "c", "crit", "adv", "neu")
	d, _ := loadDag(r)
	errs := strings.Join(validateDag(r, d), "\n")
	for _, want := range []string{"not in catalog", "dependency cycle", "critical lane must use a different family", "do not use gemini-medium-m", "reviews or author_family", "contract missing"} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in:\n%s", want, errs)
		}
	}
}

func TestDagReadyOrderAndSkip(t *testing.T) {
	r := dagRunFixture(t, goodDag, "recon", "impl-a", "impl-b", "mech-a", "crit-a", "crit-b", "docs")
	d, _ := loadDag(r)
	st := loadDagState(r, d)
	ready := dagReady(d, st)
	// recon heads the longest chain (recon→impl-a→mech-a→crit-a), docs is a single node.
	if !slices.Equal(ready, []string{"recon", "docs"}) {
		t.Fatalf("ready = %v", ready)
	}
	st.Nodes["recon"].Status = "done"
	ready = dagReady(d, st)
	if ready[0] != "impl-a" || !slices.Contains(ready, "impl-b") {
		t.Fatalf("ready = %v", ready)
	}
	// A failed mechanical gate skips everything behind it.
	st.Nodes["impl-a"].Status = "done"
	st.Nodes["mech-a"].Status = "failed"
	_ = dagReady(d, st)
	if st.Nodes["crit-a"].Status != "skipped" {
		t.Fatalf("crit-a = %s", st.Nodes["crit-a"].Status)
	}
	if crit := criticalLength(d); crit["recon"] != 4 || crit["docs"] != 1 {
		t.Fatalf("crit = %v", crit)
	}
}

func TestChecklist(t *testing.T) {
	r := dagRunFixture(t, goodDag)
	os.WriteFile(filepath.Join(r.dir(), "acceptance.md"), []byte("# Acceptance criteria\n\n1. first | pipe\n2. second\n3. third\n"), 0600)
	table, err := checklist(r, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table, `| 1 | first \| pipe |`) || !strings.Contains(table, "## additions outside the request") {
		t.Fatalf("table = %s", table)
	}
	shuffled, _ := checklist(r, true, 7)
	for _, n := range []string{"| 1 |", "| 2 |", "| 3 |"} {
		if !strings.Contains(shuffled, n) {
			t.Fatalf("shuffle lost %s", n)
		}
	}
	if !strings.Contains(shuffled, "seed=7") {
		t.Fatal("seed not recorded")
	}
}

func TestDagCheckResolvesAuthorFamily(t *testing.T) {
	old := herdrRun
	t.Cleanup(func() { herdrRun = old })
	herdrRun = func(args ...string) ([]byte, error) { // Head pane runs Claude
		return []byte(`{"result":{"pane":{"agent":"claude"}}}`), nil
	}
	y := `nodes:
  - {name: same, model: claude-medium-m, role: verify-critical, author_family: [head]}
  - {name: typo, model: codex-high-m, role: verify-critical, author_family: [bogus]}
  - {name: ok, model: codex-high-m, role: verify-critical, author_family: [head]}
`
	r := dagRunFixture(t, y, "same", "typo", "ok")
	d, _ := loadDag(r)
	errs := strings.Join(validateDag(r, d), "\n")
	if !strings.Contains(errs, "same:") || !strings.Contains(errs, "typo:") || strings.Contains(errs, "ok:") {
		t.Fatalf("errs:\n%s", errs)
	}
}

func TestDagValidateInlineNodes(t *testing.T) {
	y := `nodes:
  - {name: impl, model: codex-high-m, role: produce, purpose: impl}
  - {name: gate, run: "go test ./...", role: verify-mech, needs: [impl]}
  - {name: crit, model: claude-medium-m, role: verify-critical, reviews: [impl], needs: [gate], pane: false}
  - {name: gated, model: codex-medium-h, run: "true", role: verify-mech}
  - {name: runprod, run: "true", role: produce}
  - {name: writer, model: codex-high-l, role: produce, pane: false}
  - {name: revgate, model: claude-medium-m, role: verify-critical, reviews: [gate]}
`
	r := dagRunFixture(t, y, "impl", "crit", "writer", "revgate")
	d, _ := loadDag(r)
	if n := d.node("gate"); n.Contract != "" || !n.inline() {
		t.Fatalf("gate = %+v", n)
	}
	errs := strings.Join(validateDag(r, d), "\n")
	for _, want := range []string{"gated: a run node takes no model", "runprod: a run node must have role verify-mech", "writer: pane: false is read-only", "revgate: reviews gate, which has no pane worker"} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in:\n%s", want, errs)
		}
	}
	for _, clean := range []string{"impl:", "gate:", "crit:"} {
		if strings.HasPrefix(errs, clean) || strings.Contains(errs, "\n"+clean) {
			t.Errorf("unexpected error for %s in:\n%s", clean, errs)
		}
	}
}

func TestDagRunInlineGateAndAsk(t *testing.T) {
	fakeCLI(t, "claude", `cat <<'J'
{"result":"# Result\nfindings: none\nstatus: done"}
J`)
	y := `nodes:
  - {name: recon, model: claude-medium-m, role: recon, pane: false}
  - {name: gate, run: "echo ok", role: verify-mech, needs: [recon]}
  - {name: after, run: "echo after", role: verify-mech, needs: [gate]}
  - {name: broken, run: "echo 'FAIL pkg/x' && exit 1", role: verify-mech}
  - {name: behind, run: "echo never", role: verify-mech, needs: [broken]}
`
	r := dagRunFixture(t, y, "recon")
	var out strings.Builder
	st, err := dagRun(r.ID, time.Minute, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want := map[string]string{"recon": "done", "gate": "done", "after": "done", "broken": "failed", "behind": "skipped"}
	for n, s := range want {
		if got := st.Nodes[n].Status; got != s {
			t.Errorf("%s = %s, want %s\n%s", n, got, s, out.String())
		}
	}
	if !strings.Contains(st.Nodes["broken"].Detail, "pkg/x") {
		t.Errorf("broken detail = %q", st.Nodes["broken"].Detail)
	}
	if resultStatus(r.resultPath("gate")) != "done" || resultStatus(r.resultPath("broken")) != "blocked" {
		t.Error("gate result files lack the expected status lines")
	}
	if b, _ := os.ReadFile(r.resultPath("recon")); !strings.Contains(string(b), "findings: none") {
		t.Errorf("recon result = %q", b)
	}
}

func TestDagInlineCriticSeesLaterWorkerAndOwnership(t *testing.T) {
	fakeCLI(t, "claude", `cat <<'J'
{"result":"no defects\nstatus: done"}
J`)
	y := `nodes:
  - {name: impl, model: codex-high-m, role: produce, purpose: impl}
  - {name: crit, model: claude-medium-m, role: verify-critical, reviews: [impl], pane: false}
`
	r := dagRunFixture(t, y, "impl", "crit")
	d, _ := loadDag(r)
	st := loadDagState(r, d)
	// The author was spawned after the DAG run loaded the run: only the saved run knows it.
	withRun(r.ID, func(x *Run) error {
		x.Workers = append(x.Workers, &Worker{Name: "impl", Key: "codex-high-m", Family: familyOpenAI, Role: roleProduce, Status: "closed"})
		return nil
	})
	st.Nodes["impl"].Status, st.Nodes["impl"].Worker = "done", "impl"
	done := make(chan inlineResult, 1)
	if err := startInline(r.ID, d, st, d.node("crit"), time.Minute, done); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.status != "done" {
		t.Fatalf("crit = %+v", res)
	}
	t.Setenv("HERDR_PANE_ID", "w9:p9")
	if err := startInline(r.ID, d, st, d.node("crit"), time.Minute, done); err == nil || !strings.Contains(err.Error(), "another Head") {
		t.Fatalf("foreign Head err = %v", err)
	}
}

func TestDagInlineAskFallsBackOnQuota(t *testing.T) {
	fakeCLI(t, "codex", `echo "ERROR: quota exceeded"; exit 1`)
	fakeCLI(t, "agy", `printf '# Result\nfound it\nstatus: done\n'`)
	y := `nodes:
  - {name: recon, model: codex-medium-h, role: recon, purpose: explore, pane: false}
`
	r := dagRunFixture(t, y, "recon")
	var out strings.Builder
	st, err := dagRun(r.ID, time.Minute, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if ns := st.Nodes["recon"]; ns.Status != "done" || !strings.Contains(ns.Detail, "gemini-medium-m") || ns.Worker != "task-2" {
		t.Fatalf("recon = %+v\n%s", ns, out.String())
	}
}
