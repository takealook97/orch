package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const codexListSample = `{"models":[
{"slug":"gpt-6.3-sol","visibility":"list"},
{"slug":"gpt-6.2-sol","visibility":"list"},
{"slug":"gpt-6.1-terra","visibility":"list"},
{"slug":"gpt-7-luna","visibility":"hide"},
{"slug":"gpt-6-luna","visibility":"list"},
{"slug":"gpt-reserve","visibility":"hide"}]}`

const agyListSample = "gemini-3.9-flash-high\tGemini 3.9 Flash (High)\n" +
	"gemini-3.9-flash-medium\tGemini 3.9 Flash (Medium)\n" +
	"claude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n" +
	"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n"

func TestRefreshParseCodexModels(t *testing.T) {
	ids, err := parseCodexModels([]byte(codexListSample))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-6.3-sol", "gpt-6.2-sol", "gpt-6.1-terra", "gpt-6-luna"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if _, err := parseCodexModels([]byte("not json")); err == nil {
		t.Fatal("expected error for bad json")
	}
}

func TestRefreshParseAgyModels(t *testing.T) {
	got := parseAgyModels([]byte("Fetching available models...\n" + agyListSample))
	want := []string{"gemini-3.9-flash-high", "gemini-3.9-flash-medium", "claude-sonnet-4-6", "gpt-oss-120b-medium"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRefreshVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{{"6.2", "6.1", 1}, {"6", "6.0", 0}, {"6.10", "6.9", 1}, {"5.6", "6", -1}}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s,%s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	list := []string{"gpt-6.3-sol", "gpt-6.2-sol", "gpt-6.4-astra", "gemini-3.9-flash-medium", "gemini-4-flash-high", "weird"}
	newest := []struct{ cur, want string }{
		{"gpt-6.2-sol", "gpt-6.3-sol"},
		{"gpt-6.3-sol", ""},
		{"gemini-3.8-flash-medium", "gemini-3.9-flash-medium"},
		{"opus-5.5-medium", ""},
	}
	for _, c := range newest {
		if got := newestModelID(c.cur, list); got != c.want {
			t.Errorf("newestModelID(%s) = %q, want %q", c.cur, got, c.want)
		}
	}
}

func TestRefreshDecisionInsert(t *testing.T) {
	text := "## D20\nprobes via `doctor --probe-models`.\n\n2026-11-19: Sol upgraded from a to b.\n\n## D21\n"
	changes := []modelChange{{old: "gpt-6.2-sol", new: "gpt-6.3-sol", model: Model{Tier: "Sol"}}}
	got, err := insertDecision(text, decisionLine("2026-12-01", changes))
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-11-19: Sol upgraded from a to b.\n\n2026-12-01: Sol upgraded from gpt-6.2-sol to gpt-6.3-sol.\n\n## D21"
	if !strings.Contains(got, want) {
		t.Fatalf("got:\n%s", got)
	}
}

type refreshRecorder struct {
	calls  []string
	probes []string
}

// stubRefresh fakes CLI lists, go and install commands; git runs for real except push.
func stubRefresh(t *testing.T, failProbe map[string]bool) *refreshRecorder {
	t.Helper()
	rec := &refreshRecorder{}
	saved := refreshHooks
	t.Cleanup(func() { refreshHooks = saved })
	refreshHooks = refreshDeps{
		now: func() time.Time { return time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC) },
		probe: func(_ context.Context, m Model, id string) error {
			rec.probes = append(rec.probes, id)
			if failProbe[id] {
				return errors.New("unknown model")
			}
			return nil
		},
		run: func(ctx context.Context, dir, name string, args ...string) (string, error) {
			line := strings.TrimSpace(name + " " + strings.Join(args, " "))
			switch {
			case name == "codex":
				return codexListSample, nil
			case name == "agy":
				return agyListSample, nil
			case name == "go" || name == "sh" || line == "git push":
				rec.calls = append(rec.calls, line)
				return "", nil
			}
			return refreshRun(ctx, dir, name, args...)
		},
	}
	return rec
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// refreshRepo builds a clean main checkout tracking a local bare origin.
func refreshRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	gitT(t, root, "init", "-q", "--bare", "-b", "main", remote)
	gitT(t, root, "init", "-q", "-b", "main", repo)
	gitT(t, repo, "config", "user.email", "t@example.com")
	gitT(t, repo, "config", "user.name", "t")
	decisions, err := os.ReadFile("skill/references/decisions.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "skill", "references"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "skill", "references", "decisions.md"), decisions, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "models.yaml"), embeddedModels, 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "init")
	gitT(t, repo, "remote", "add", "origin", remote)
	gitT(t, repo, "push", "-q", "-u", "origin", "main")
	return repo
}

func TestRefreshAppliesVerifiedChangesAndCommits(t *testing.T) {
	repo := refreshRepo(t)
	rec := stubRefresh(t, map[string]bool{"gemini-3.9-flash-medium": true})
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	data, _ := os.ReadFile(filepath.Join(repo, "models.yaml"))
	if !strings.Contains(string(data), "  sol: gpt-6.3-sol\n") {
		t.Fatalf("sol not updated:\n%s", data)
	}
	if !strings.Contains(string(data), "  gemini-flash: gemini-3.8-flash-medium\n") {
		t.Fatalf("failed probe must not be applied:\n%s", data)
	}
	if got := gitT(t, repo, "log", "-1", "--format=%s"); got != "chore: update model ids (gpt-6.2-sol -> gpt-6.3-sol)" {
		t.Fatalf("subject = %q", got)
	}
	dec, _ := os.ReadFile(filepath.Join(repo, "skill", "references", "decisions.md"))
	if !strings.Contains(string(dec), "2026-12-01: Sol upgraded from gpt-6.2-sol to gpt-6.3-sol.") {
		t.Fatal("decision line missing")
	}
	if gitT(t, repo, "status", "--porcelain") != "" {
		t.Fatal("repo left dirty after commit")
	}
	wantCalls := []string{"go run . docs gen --root .", "go test -count=1 -run " + refreshTestPattern + " .", "git push", "sh -c " + refreshInstallShell}
	if !slices.Equal(rec.calls, wantCalls) {
		t.Fatalf("calls = %q\nout:\n%s", rec.calls, out.String())
	}
	if !strings.Contains(out.String(), "opus: skip") {
		t.Fatalf("claude family should be skipped with a reason:\n%s", out.String())
	}
}

func TestRefreshSkipsDirtyRepo(t *testing.T) {
	repo := refreshRepo(t)
	rec := stubRefresh(t, nil)
	if err := os.WriteFile(filepath.Join(repo, "stray.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skip: working tree is dirty") || len(rec.probes) != 0 || len(rec.calls) != 0 {
		t.Fatalf("out=%s probes=%v calls=%v", out.String(), rec.probes, rec.calls)
	}
}

func TestRefreshSkipsNonDefaultBranch(t *testing.T) {
	repo := refreshRepo(t)
	stubRefresh(t, nil)
	gitT(t, repo, "checkout", "-q", "-b", "feature")
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skip: on branch feature") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestRefreshDryRunWritesNothing(t *testing.T) {
	repo := refreshRepo(t)
	rec := stubRefresh(t, nil)
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, DryRun: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if gitT(t, repo, "status", "--porcelain") != "" || len(rec.calls) != 0 {
		t.Fatalf("dry run changed something: calls=%v", rec.calls)
	}
	if !slices.Contains(rec.probes, "gpt-6.3-sol") || !strings.Contains(out.String(), "candidate gpt-6.2-sol -> gpt-6.3-sol") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestRefreshRestoresOnTestFailure(t *testing.T) {
	repo := refreshRepo(t)
	stubRefresh(t, nil)
	inner := refreshHooks.run
	refreshHooks.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "go" && len(args) > 0 && args[0] == "test" {
			return "", errors.New("catalog tests failed")
		}
		return inner(ctx, dir, name, args...)
	}
	err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &bytes.Buffer{}})
	if err == nil || gitT(t, repo, "status", "--porcelain") != "" {
		t.Fatalf("err=%v, repo must be restored", err)
	}
}
