package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKnowledgeTemplate(t *testing.T) {
	r := dagRunFixture(t, "nodes: []")
	b, err := os.ReadFile(filepath.Join(r.dir(), "knowledge.md"))
	if err != nil || !strings.HasPrefix(string(b), "# Knowledge\n<!--") {
		t.Fatalf("knowledge: %q, %v", b, err)
	}
	body := contractTemplate(r, "a", roleProduce, "codex-high-l")
	if !strings.Contains(body, "## Reusable facts") || !strings.Contains(body, "verified, reusable facts") {
		t.Fatal("missing reusable facts instructions")
	}
}

func TestKnowledgeMerge(t *testing.T) {
	r := dagRunFixture(t, "nodes: []")
	for _, tc := range []struct {
		name, status, facts string
		count               int
	}{
		{"a", "done", "- A fact — run.go\n- A fact — run.go\n", 1},
		{"a", "done", "- A fact — run.go\n", 1},
		{"b", "partial", "- A fact — run.go\n- Other fact — go test\n", 2},
		{"c", "blocked", "- Blocked fact — x\n", 2},
	} {
		if err := os.WriteFile(r.resultPath(tc.name), []byte("- status: "+tc.status+"\n## Reusable facts\n"+tc.facts+"## Uncertainty\n- Not a fact\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := mergeKnowledge(r.ID, tc.name, r.resultPath(tc.name)); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(r.dir(), "knowledge.md"))
		if strings.Count(string(b), " (from ") != tc.count {
			t.Fatalf("merge: %s", b)
		}
	}
	b, _ := os.ReadFile(filepath.Join(r.dir(), "knowledge.md"))
	if !strings.Contains(string(b), "- A fact — run.go (from a)") {
		t.Fatal(string(b))
	}
	rr, _ := loadRun(r.ID)
	if len(rr.Events) < 2 || rr.Events[len(rr.Events)-1].What != "knowledge +1 from b" {
		t.Fatalf("events: %+v", rr.Events)
	}
}

func TestKnowledgeVerifyPrompt(t *testing.T) {
	r := dagRunFixture(t, "nodes: []")
	for _, role := range []string{roleProduce, "recon", "verify-mech", "verify-critical"} {
		msg := promptMessage(r, &Worker{Name: "a", Role: role})
		if strings.Contains(msg, "Run knowledge") == strings.HasPrefix(role, "verify-") {
			t.Fatalf("role %s: %s", role, msg)
		}
	}
}

func TestUpstreamRefs(t *testing.T) {
	r := dagRunFixture(t, `nodes:
  - {name: a, role: produce, needs: [up, missing]}
  - {name: verify, role: verify-mech, needs: [up]}
`)
	os.WriteFile(r.resultPath("up"), []byte("result"), 0600)
	for _, name := range []string{"a", "verify", "non-dag"} {
		role := roleProduce
		if name == "verify" {
			role = "verify-mech"
		}
		for _, msg := range []string{promptMessage(r, &Worker{Name: name, Role: role}), inlinePrompt(r, &DagNode{Name: name, Role: role}, "contract")} {
			if strings.Contains(msg, "Upstream results:") != (name != "non-dag") {
				t.Fatal(msg)
			}
			if strings.Contains(msg, r.resultPath("missing")) {
				t.Fatal(msg)
			}
			if name != "non-dag" && !strings.Contains(msg, r.resultPath("up")) {
				t.Fatal(msg)
			}
			if name == "verify" && strings.Contains(msg, "Run knowledge") {
				t.Fatal(msg)
			}
		}
	}
}

func TestCollectSummary(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"one", "- summary: one\n## Evidence\n- other", "summary: one"},
		{"three", "- summary: one\n  two\n  three\n- model_used: x", "summary: one\n  two\n  three"},
		{"four", "summary: one\n two\n three\n four", "summary: one\n two\n three"},
		{"field", "- summary: one\n- status: done", "summary: one"},
		{"cap", "- summary: " + strings.Repeat("한", 700), "summary: " + strings.Repeat("한", 590) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := collectSummary(tc.body); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestKnowledgeCompletionPaths(t *testing.T) {
	for _, path := range []string{"wait", "collect", "reap"} {
		t.Run(path, func(t *testing.T) {
			r, _ := reapFixture(t)
			if err := os.WriteFile(r.resultPath("a"), []byte("- summary: first\n second\n third\n fourth\n- status: done\n## Reusable facts\n- Verified fact — run.go\n"), 0600); err != nil {
				t.Fatal(err)
			}
			switch path {
			case "wait":
				if _, err := waitWorkers(r.ID, []string{"a"}, time.Second, false); err != nil {
					t.Fatal(err)
				}
			case "collect":
				out, _, err := collectWorker(r.ID, "a")
				if err != nil || !strings.Contains(out, "summary: first\n second\n third\n") || strings.Contains(out, "fourth") {
					t.Fatalf("%s: %v", out, err)
				}
			case "reap":
				rp := newReaper(r.ID)
				var log strings.Builder
				for i := 0; i < 2; i++ {
					if _, err := rp.once(&log); err != nil {
						t.Fatal(err)
					}
				}
			}
			b, _ := os.ReadFile(filepath.Join(r.dir(), "knowledge.md"))
			if !strings.Contains(string(b), "- Verified fact — run.go (from a)") {
				t.Fatalf("%s", b)
			}
		})
	}
}

func TestKnowledgeConcurrentMerge(t *testing.T) {
	r := dagRunFixture(t, "nodes: []")
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(r.resultPath(name), []byte("- status: done\n## Reusable facts\n- Shared fact — "+name+".go\n- Unique "+name+" — "+name+".go\n"), 0600); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := mergeKnowledge(r.ID, name, r.resultPath(name)); err != nil {
				t.Error(err)
			}
		}(name)
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(r.dir(), "knowledge.md"))
	if strings.Count(string(b), "- Shared fact") != 1 || strings.Count(string(b), "- Unique") != 3 {
		t.Fatal(string(b))
	}
}

func TestKnowledgeMergeFailureStillCloses(t *testing.T) {
	for _, path := range []string{"wait", "collect", "reap"} {
		t.Run(path, func(t *testing.T) {
			r, f := reapFixture(t)
			if err := os.Remove(filepath.Join(r.dir(), "knowledge.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(r.dir(), "knowledge.md"), 0700); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(r.resultPath("a"), []byte("- status: done\n## Reusable facts\n- Fact — run.go\n"), 0600)
			switch path {
			case "wait":
				waitWorkers(r.ID, []string{"a"}, time.Second, false)
			case "collect":
				collectWorker(r.ID, "a")
			case "reap":
				rp := newReaper(r.ID)
				var log strings.Builder
				for i := 0; i < 2; i++ {
					rp.once(&log)
				}
			}
			if f.live("w1:p2") {
				t.Fatal("knowledge merge failure left finished worker pane open")
			}
		})
	}
}
