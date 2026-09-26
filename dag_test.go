package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
		os.WriteFile(r.contractPath(c), []byte(contractTemplate(r, c, roleProduce, "sol-m")), 0600)
	}
	return r
}

const goodDag = `nodes:
  - {name: recon, model: luna-m, role: recon, purpose: explore}
  - {name: impl-a, model: sol-m, role: produce, purpose: impl, needs: [recon]}
  - {name: impl-b, model: opus-m, role: produce, purpose: hard-impl, needs: [recon]}
  - {name: mech-a, model: luna-h, role: verify-mech, needs: [impl-a]}
  - {name: crit-a, model: sonnet-m, role: verify-critical, reviews: [impl-a], needs: [mech-a]}
  - {name: crit-b, model: sol-m, role: verify-critical, reviews: [impl-b]}
  - {name: docs, model: sonnet-m, role: produce, purpose: docs}
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
  - {name: b, model: sol-m, role: produce, needs: [a]}
  - {name: c, model: sol-m, role: produce, needs: [b]}
  - {name: crit, model: luna-h, role: verify-critical, reviews: [b]}
  - {name: adv, model: flash, role: verify-adversarial, reviews: [b]}
  - {name: neu, model: flash, role: verify-neutral}
  - {name: nocontract, model: sol-m, role: produce, contract: contracts/missing.md}
`
	r := dagRunFixture(t, bad, "a", "b", "c", "crit", "adv", "neu")
	d, _ := loadDag(r)
	errs := strings.Join(validateDag(r, d), "\n")
	for _, want := range []string{"not in catalog", "dependency cycle", "critical lane must use a different family", "do not use flash", "reviews or author_family", "contract missing"} {
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
  - {name: same, model: sonnet-m, role: verify-critical, author_family: [head]}
  - {name: typo, model: sol-m, role: verify-critical, author_family: [bogus]}
  - {name: ok, model: sol-m, role: verify-critical, author_family: [head]}
`
	r := dagRunFixture(t, y, "same", "typo", "ok")
	d, _ := loadDag(r)
	errs := strings.Join(validateDag(r, d), "\n")
	if !strings.Contains(errs, "same:") || !strings.Contains(errs, "typo:") || strings.Contains(errs, "ok:") {
		t.Fatalf("errs:\n%s", errs)
	}
}
