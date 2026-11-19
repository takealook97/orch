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

// stubRefresh fakes CLI lists, probes and go commands; git runs for real against a temp bare origin.
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
			switch name {
			case "codex":
				return codexListSample, nil
			case "agy":
				return agyListSample, nil
			case "go", "sh":
				rec.calls = append(rec.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
				return "", nil
			}
			return refreshRun(ctx, dir, name, args...)
		},
		sync: refreshSyncCheckout,
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

// refreshRepo builds a clean main checkout tracking a local bare origin, with HOME,
// ORCH_HOME and the update config isolated so the checkout sync installs a marker only.
func refreshRepo(t *testing.T) (repo, remote string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	remote, repo = filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	gitT(t, root, "init", "-q", "--bare", "-b", "main", remote)
	gitT(t, root, "init", "-q", "-b", "main", repo)
	gitT(t, repo, "config", "user.email", "t@example.com")
	gitT(t, repo, "config", "user.name", "t")
	gitT(t, repo, "config", "commit.gpgsign", "false")
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
	updateLiveWriters = func(string) int { return 0 }
	t.Cleanup(func() { updateLiveWriters = func(root string) int { return len(liveWritersIn(root, nil)) } })
	writeUpdateConfig(t, repo, "touch installed.marker")
	return repo, remote
}

// assertNoRefreshLeftovers checks that only the main worktree remains and TMPDIR is empty.
func assertNoRefreshLeftovers(t *testing.T, repo string) {
	t.Helper()
	if n := strings.Count(gitT(t, repo, "worktree", "list", "--porcelain"), "worktree "); n != 1 {
		t.Fatalf("temporary worktree left behind:\n%s", gitT(t, repo, "worktree", "list"))
	}
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Fatalf("temp entries left: %v", left)
	}
}

func TestRefreshPushesFromWorktreeAndSyncsCheckout(t *testing.T) {
	repo, remote := refreshRepo(t)
	rec := stubRefresh(t, map[string]bool{"gemini-3.9-flash-medium": true})
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := gitT(t, remote, "log", "-1", "--format=%s", "main"); got != "chore: update model ids" {
		t.Fatalf("subject = %q", got)
	}
	if body := gitT(t, remote, "log", "-1", "--format=%b", "main"); !strings.Contains(body, "sol: gpt-6.2-sol -> gpt-6.3-sol") {
		t.Fatalf("body lacks the alias change: %q", body)
	}
	if gitT(t, repo, "rev-parse", "HEAD") != gitT(t, remote, "rev-parse", "main") {
		t.Fatalf("checkout not fast-forwarded through update:\n%s", out.String())
	}
	data, _ := os.ReadFile(filepath.Join(repo, "models.yaml"))
	if !strings.Contains(string(data), "  sol: gpt-6.3-sol\n") || !strings.Contains(string(data), "  gemini-flash: gemini-3.8-flash-medium\n") {
		t.Fatalf("models.yaml:\n%s", data)
	}
	dec, _ := os.ReadFile(filepath.Join(repo, "skill", "references", "decisions.md"))
	if !strings.Contains(string(dec), "2026-12-01: Sol upgraded from gpt-6.2-sol to gpt-6.3-sol.") {
		t.Fatal("decision line missing")
	}
	if !fileExists(filepath.Join(repo, "installed.marker")) {
		t.Fatal("reinstall did not run through the update path")
	}
	wantCalls := []string{"go run . docs gen --root .", "go test -count=1 -run " + refreshTestPattern + " ."}
	if !slices.Equal(rec.calls, wantCalls) {
		t.Fatalf("calls = %q", rec.calls)
	}
	if !strings.Contains(out.String(), "opus: skip") {
		t.Fatalf("claude family should be skipped with a reason:\n%s", out.String())
	}
	assertNoRefreshLeftovers(t, repo)
}

func TestRefreshNeverTouchesUserCheckout(t *testing.T) {
	repo, remote := refreshRepo(t)
	stubRefresh(t, nil)
	gitT(t, repo, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(repo, "models.yaml"), []byte("user edit\n"), 0644)
	os.WriteFile(filepath.Join(repo, "stray.txt"), []byte("x"), 0644)
	head := gitT(t, repo, "rev-parse", "HEAD")
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "models.yaml")); string(data) != "user edit\n" {
		t.Fatalf("user edit lost: %q", data)
	}
	if !fileExists(filepath.Join(repo, "stray.txt")) || gitT(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("user checkout changed")
	}
	if gitT(t, remote, "log", "-1", "--format=%s", "main") != "chore: update model ids" {
		t.Fatal("refresh should still publish from origin/main")
	}
	if !strings.Contains(out.String(), "checkout: skip: working tree is dirty") {
		t.Fatalf("out=%s", out.String())
	}
	assertNoRefreshLeftovers(t, repo)
}

func TestRefreshDryRunWritesNothing(t *testing.T) {
	repo, remote := refreshRepo(t)
	rec := stubRefresh(t, nil)
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, filepath.Dir(other), "clone", "-q", remote, other)
	os.WriteFile(filepath.Join(other, "n.txt"), []byte("n"), 0644)
	gitT(t, other, "add", ".")
	gitT(t, other, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "n")
	gitT(t, other, "push", "-q", "origin", "main")
	ref := gitT(t, repo, "rev-parse", "refs/remotes/origin/main")
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, DryRun: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if gitT(t, repo, "status", "--porcelain") != "" || len(rec.calls) != 0 || len(rec.probes) != 0 {
		t.Fatalf("dry run changed or probed: calls=%v probes=%v", rec.calls, rec.probes)
	}
	if fileExists(filepath.Join(repo, ".git", "FETCH_HEAD")) || gitT(t, repo, "rev-parse", "refs/remotes/origin/main") != ref {
		t.Fatal("dry run wrote git refs")
	}
	if !strings.Contains(out.String(), "candidate gpt-6.2-sol -> gpt-6.3-sol") {
		t.Fatalf("out=%s", out.String())
	}
	assertNoRefreshLeftovers(t, repo)
}

func TestRefreshTestFailureChangesNothing(t *testing.T) {
	repo, remote := refreshRepo(t)
	stubRefresh(t, nil)
	inner := refreshHooks.run
	refreshHooks.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "go" && len(args) > 0 && args[0] == "test" {
			return "", errors.New("catalog tests failed")
		}
		return inner(ctx, dir, name, args...)
	}
	before := gitT(t, remote, "rev-parse", "main")
	err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &bytes.Buffer{}})
	if err == nil || gitT(t, repo, "status", "--porcelain") != "" || gitT(t, remote, "rev-parse", "main") != before {
		t.Fatalf("err=%v; origin and checkout must be unchanged", err)
	}
	assertNoRefreshLeftovers(t, repo)
}

func TestRefreshPushFailureIsRetriedNextRun(t *testing.T) {
	repo, remote := refreshRepo(t)
	stubRefresh(t, nil)
	inner := refreshHooks.run
	failPush := true
	refreshHooks.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "push" && failPush {
			return "", errors.New("network down")
		}
		return inner(ctx, dir, name, args...)
	}
	before := gitT(t, remote, "rev-parse", "main")
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &bytes.Buffer{}}); err == nil {
		t.Fatal("push failure not reported")
	}
	if gitT(t, remote, "rev-parse", "main") != before || gitT(t, repo, "rev-parse", "HEAD") != before {
		t.Fatal("failed push changed origin or the checkout")
	}
	failPush = false
	var out bytes.Buffer
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if gitT(t, remote, "log", "-1", "--format=%s", "main") != "chore: update model ids" {
		t.Fatalf("retry did not publish:\n%s", out.String())
	}
	assertNoRefreshLeftovers(t, repo)
}

func TestRefreshReinstallFailureIsRetriedByUpdate(t *testing.T) {
	repo, remote := refreshRepo(t)
	stubRefresh(t, nil)
	writeUpdateConfig(t, repo, "exit 7")
	err := refreshModels(context.Background(), modelsRefreshOptions{Repo: repo, Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "pushed to origin/main but updating") {
		t.Fatalf("reinstall failure: %v", err)
	}
	if gitT(t, repo, "rev-parse", "HEAD") != gitT(t, remote, "rev-parse", "main") {
		t.Fatal("checkout should be fast-forwarded before the install")
	}
	writeUpdateConfig(t, repo, "touch installed.marker")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err, out.String())
	}
	if !fileExists(filepath.Join(repo, "installed.marker")) {
		t.Fatalf("update did not retry the install: %s", out.String())
	}
}
