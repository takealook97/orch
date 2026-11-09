package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func reviewFixture(t *testing.T) (*Run, string) {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	r := &Run{ID: "review", Workers: []*Worker{{Name: "author", Cwd: t.TempDir()}}}
	for _, d := range []string{"contracts", "results", "evidence", "tasks/task-1"} {
		if err := os.MkdirAll(filepath.Join(r.dir(), d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(r.dir(), "acceptance.md"), []byte("1. Works correctly\n2. Has evidence\n"), 0600)
	os.WriteFile(filepath.Join(r.dir(), "tasks/task-1/test.log"), []byte("pass"), 0600)
	repo := r.Workers[0].Cwd
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
	}
	git("init")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	os.WriteFile(filepath.Join(repo, "a"), []byte("one\n"), 0600)
	git("add", "a")
	git("commit", "-m", "first")
	os.WriteFile(filepath.Join(repo, "a"), []byte("two\n"), 0600)
	git("commit", "-am", "second")
	return r, repo
}

func TestReviewContractGenerated(t *testing.T) {
	for _, role := range []string{"verify-critical", "verify-neutral", "verify-adversarial"} {
		t.Run(role, func(t *testing.T) {
			r, _ := reviewFixture(t)
			body, err := reviewContract(r, "reviewer", role, "codex-high-l", reviewContractOptions{Reviews: "author", Diff: "HEAD~1..HEAD", Mech: "task-1", Focus: "edge cases"})
			if err != nil {
				t.Fatal(err)
			}
			if err = validateContract(body); err != nil {
				t.Fatal(err)
			}
			for _, s := range []string{"<", "Owned files (glob): none", "Focus: edge cases", "tasks/task-1/test.log"} {
				if s == "<" {
					if strings.Contains(body, s) {
						t.Fatalf("placeholder: %s", body)
					}
				} else if !strings.Contains(body, s) {
					t.Fatalf("missing %s", s)
				}
			}
			if role == "verify-neutral" && !strings.Contains(body, "| 1 | Works correctly |") {
				t.Fatal(body)
			}
			b, err := os.ReadFile(filepath.Join(r.dir(), "evidence/reviewer.diff"))
			if err != nil || !strings.Contains(string(b), "-one\n+two") {
				t.Fatalf("diff %s %v", b, err)
			}
		})
	}
}

func TestReviewContractErrors(t *testing.T) {
	r, _ := reviewFixture(t)
	for _, o := range []reviewContractOptions{{Reviews: "author", Diff: "HEAD..HEAD"}, {Reviews: "author"}, {Reviews: "missing", Diff: "HEAD~1..HEAD"}, {Reviews: "author", Diff: "HEAD~1..HEAD", Mech: "missing"}} {
		if _, err := reviewContract(r, "bad", "verify-critical", "codex-high-l", o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	if _, err := reviewContract(r, "bad", "verify-adversarial", "codex-high-l", reviewContractOptions{Reviews: "author", Diff: "HEAD~1..HEAD"}); err == nil {
		t.Fatal("missing focus accepted")
	}
}

func TestReviewContractPlainTemplate(t *testing.T) {
	r, _ := reviewFixture(t)
	got, err := reviewContract(r, "plain", "produce", "codex-high-l", reviewContractOptions{})
	if err != nil || got != contractTemplate(r, "plain", "produce", "codex-high-l") {
		t.Fatalf("template changed: %v", err)
	}
}

func TestReviewContractCommand(t *testing.T) {
	r, repo := reviewFixture(t)
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("HERDR_PANE_ID", "")
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := run([]string{"contract", "new", "cli", "--run", r.ID, "--role", "verify-critical", "--model", "codex-high-l", "--reviews", "author", "--diff", "HEAD~1..HEAD", "--cwd", repo}, strings.NewReader(""), &out, &errs)
	if code != 0 {
		t.Fatalf("code=%d %s", code, errs.String())
	}
	b, err := os.ReadFile(r.contractPath("cli"))
	if err != nil || validateContract(string(b)) != nil || !strings.Contains(string(b), "severity") {
		t.Fatalf("%s %v", b, err)
	}
	// The regular command path must still emit the byte-identical editable template.
	out.Reset()
	code = run([]string{"contract", "new", "plain", "--run", r.ID, "--role", "produce", "--model", "codex-high-l"}, strings.NewReader(""), &out, &errs)
	b, err = os.ReadFile(r.contractPath("plain"))
	if code != 0 || err != nil || string(b) != contractTemplate(r, "plain", "produce", "codex-high-l") {
		t.Fatalf("code=%d %v %s", code, err, errs.String())
	}
}

func TestReviewContractRepositoryPrecedence(t *testing.T) {
	r, repo := reviewFixture(t)
	r.Workers[0].Worktree = repo
	r.Workers[0].Cwd = t.TempDir()
	if _, err := reviewContract(r, "worktree", "verify-critical", "codex-high-l", reviewContractOptions{Reviews: "author", Diff: "HEAD~1..HEAD"}); err != nil {
		t.Fatal(err)
	}
	r.Workers[0].Worktree = t.TempDir()
	if _, err := reviewContract(r, "override", "verify-critical", "codex-high-l", reviewContractOptions{Reviews: "author", Diff: "HEAD~1..HEAD", Cwd: repo}); err != nil {
		t.Fatal(err)
	}
	r.Workers[0].Worktree = ""
	r.Workers[0].Cwd = ""
	t.Chdir(repo)
	if _, err := reviewContract(r, "current", "verify-critical", "codex-high-l", reviewContractOptions{Reviews: "author", Diff: "HEAD~1..HEAD"}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewContractMechanicalPath(t *testing.T) {
	r, repo := reviewFixture(t)
	t.Chdir(repo)
	if err := os.WriteFile("task-evidence.log", []byte("pass"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := reviewContract(r, "path", "verify-critical", "codex-high-l", reviewContractOptions{Reviews: "author", Diff: "HEAD~1..HEAD", Mech: "task-evidence.log"})
	if err != nil || !strings.Contains(body, filepath.Join(repo, "task-evidence.log")) {
		t.Fatalf("%v %s", err, body)
	}
}
