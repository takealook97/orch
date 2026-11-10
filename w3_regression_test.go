package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %v %s", args, err, out)
	}
}

func newDiffTask(t *testing.T, dir string, changed []string) *Task {
	t.Helper()
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return &Task{Cwd: dir, Dir: t.TempDir(), StartHead: strings.TrimSpace(string(head)), Changed: changed, DiffLines: 60}
}

func TestDiffForTaskTreatsChangedNamesAsLiterals(t *testing.T) {
	dir := gitInit(t)
	for _, f := range []string{"[x].txt", "x.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-qm", "base")
	task := newDiffTask(t, dir, []string{"[x].txt"})
	for f, body := range map[string]string{"[x].txt": "task change\n", "x.txt": "UNRELATED_SECRET\n"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	diffForTask(task)
	if task.Diffstat != "1 files, +1 -1" {
		t.Fatalf("diffstat=%q", task.Diffstat)
	}
	if strings.Contains(task.NetDiff, "UNRELATED_SECRET") || !strings.Contains(task.NetDiff, "+task change") {
		t.Fatalf("net diff leaks or misses: %q", task.NetDiff)
	}
	patch, _ := os.ReadFile(task.DiffPath)
	if strings.Contains(string(patch), "UNRELATED_SECRET") {
		t.Fatalf("diff.patch leaks unrelated file")
	}
}

func TestDiffForTaskRecomputesNetDiff(t *testing.T) {
	dir := taskRepo(t)
	task := newDiffTask(t, dir, []string{"owned.txt"})
	if err := os.WriteFile(filepath.Join(dir, "owned.txt"), []byte("small\n"), 0600); err != nil {
		t.Fatal(err)
	}
	diffForTask(task)
	if !strings.Contains(task.NetDiff, "+small") {
		t.Fatalf("first capture should include diff: %q", task.NetDiff)
	}
	out, _ := exec.Command("seq", "1", "70").Output()
	if err := os.WriteFile(filepath.Join(dir, "owned.txt"), out, 0600); err != nil {
		t.Fatal(err)
	}
	diffForTask(task)
	if task.NetDiff != "" {
		t.Fatalf("stale net diff survived: %q", task.NetDiff)
	}
}

func TestDiffForTaskByteCap(t *testing.T) {
	dir := taskRepo(t)
	task := newDiffTask(t, dir, []string{"owned.txt"})
	long := strings.Repeat("y", netDiffMaxBytes+10) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "owned.txt"), []byte(long), 0600); err != nil {
		t.Fatal(err)
	}
	diffForTask(task)
	if task.NetDiff != "" {
		t.Fatalf("over-cap diff included: %d bytes", len(task.NetDiff))
	}
}

func TestReadHeadSessionsOversizedLine(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	p := headSessionPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	valid := `{"version":1,"session":"keep","requests":1}` + "\n"
	body := valid + strings.Repeat("x", 16<<20+1) + "\n" + valid
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(p)
	rows, skipped, err := readHeadSessions(f)
	f.Close()
	if err != nil || skipped != 1 || len(rows) != 1 {
		t.Fatalf("rows=%d skipped=%d err=%v", len(rows), skipped, err)
	}
	// Compaction path (file above the threshold) and append must work.
	if err := recordHeadSession(HeadSession{Version: 1, Session: "new", Requests: 2}); err != nil {
		t.Fatalf("compaction/append failed: %v", err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"new"`) || !strings.Contains(string(b), `"keep"`) || len(b) > 1<<20 {
		t.Fatalf("unexpected rewritten file (%d bytes)", len(b))
	}
}

func TestHeadSummarySymlinkRejected(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	p := headSessionPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.jsonl")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if err := recordHeadSession(HeadSession{Version: 1, Session: "s", Requests: 1}); err == nil {
		t.Fatal("symlinked summary path must be an error")
	}
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Fatalf("symlink target was written: %q", b)
	}
}
