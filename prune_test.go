package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pruneEnv(t *testing.T) time.Time {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RETENTION_DAYS", "7")
	t.Setenv("ORCH_ASK_CACHE_TTL", "168")
	t.Setenv("ORCH_AUTO_PRUNE", "0")
	return time.Now()
}
func prunePut(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func pruneMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}
func pruneRun(t *testing.T, id string, at time.Time) *Run {
	t.Helper()
	r := &Run{ID: id, Status: "closed", Created: at, Events: []Event{{At: at, What: "fixture"}}}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	pruneSaveRun(t, r, at)
	prunePut(t, filepath.Join(r.dir(), "evidence", "blob.bin"), "heavy")
	return r
}
func pruneSaveRun(t *testing.T, r *Run, at time.Time) {
	t.Helper()
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	pruneMtime(t, filepath.Join(r.dir(), "run.json"), at)
}
func pruneGoal(t *testing.T, id, status string, at time.Time) *Goal {
	t.Helper()
	g := &Goal{ID: id, HeadPane: "head", Status: status, Created: at, Hours: 1, MaxWorkers: 1}
	if err := os.MkdirAll(g.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveGoal(g); err != nil {
		t.Fatal(err)
	}
	pruneMtime(t, filepath.Join(g.dir(), "goal.json"), at)
	prunePut(t, filepath.Join(g.dir(), "evidence", "blob.bin"), "heavy")
	return g
}
func pruneExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if (err == nil) != want {
		t.Fatalf("%s exists=%v, want %v (error %v)", path, err == nil, want, err)
	}
}

func TestRetentionWindow(t *testing.T) {
	for _, tc := range []struct {
		value string
		days  int
	}{{"", 7}, {"0", 0}, {"invalid", 7}, {"-1", 7}, {"14", 14}, {"1.5", 7}, {"9223372036854775807", 7}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("ORCH_RETENTION_DAYS", tc.value)
			if got := retentionWindow(); got != time.Duration(tc.days)*24*time.Hour {
				t.Fatal(got)
			}
		})
	}
}
func TestPruneEligibility(t *testing.T) {
	for _, name := range []string{"closed-old", "active", "closed-new", "event-new", "mtime-new", "heartbeat-new", "adhoc-old", "worktree", "adhoc-worktree", "active-goal", "draft-goal", "ended-goal", "missing-goal", "live-task", "unfinished-task", "live-adhoc", "finished-live-runner"} {
		t.Run(name, func(t *testing.T) {
			now := pruneEnv(t)
			old := now.Add(-8 * 24 * time.Hour)
			r := pruneRun(t, "run", old)
			want := name == "closed-old" || name == "adhoc-old" || name == "ended-goal" || name == "missing-goal"
			switch name {
			case "active":
				r.Status = "active"
			case "closed-new":
				r.Created = now
			case "event-new":
				r.Events = append(r.Events, Event{At: now})
			case "adhoc-old":
				r.Adhoc = true
			case "worktree", "adhoc-worktree":
				r.Adhoc = name == "adhoc-worktree"
				r.Workers = []*Worker{{Name: "impl", Worktree: "/missing/worktree", Branch: "kept-branch"}}
			case "active-goal", "draft-goal", "ended-goal":
				status := strings.TrimSuffix(name, "-goal")
				r.Goal = "goal"
				pruneGoal(t, "goal", status, old)
			case "missing-goal":
				r.Goal = "missing"
			case "heartbeat-new":
				r.Tasks = []*Task{{ID: "task-1", Status: "done", Heartbeat: now}}
			case "live-task", "live-adhoc":
				r.Tasks = []*Task{{ID: "task-1", Status: "running", Pid: os.Getpid()}}
				r.Adhoc = name == "live-adhoc"
			case "unfinished-task":
				r.Tasks = []*Task{{ID: "task-1", Status: "unknown"}}
			case "finished-live-runner":
				r.Tasks = []*Task{{ID: "task-1", Status: "done", Pid: os.Getpid()}}
			}
			pruneSaveRun(t, r, old)
			if name == "mtime-new" {
				pruneMtime(t, filepath.Join(r.dir(), "run.json"), now)
			}
			report, _ := pruneDisk(now)
			pruneExists(t, filepath.Join(r.dir(), "evidence", "blob.bin"), !want)
			if name == "adhoc-old" {
				pruneExists(t, r.dir(), false)
			} else {
				pruneExists(t, filepath.Join(r.dir(), ".compacted"), want)
			}
			if want && !strings.Contains(strings.Join(report, "\n"), r.ID+":") {
				t.Fatal(report)
			}
		})
	}
}
func TestPruneKeepSetRecallAndMarkers(t *testing.T) {
	now := pruneEnv(t)
	old := now.Add(-8 * 24 * time.Hour)
	r := pruneRun(t, "keep", old)
	r.Workers = []*Worker{{Name: "impl", Result: r.resultPath("impl")}}
	// run.json must survive even when it exceeds one MiB.
	r.Events[0].What = strings.Repeat("x", (1<<20)+1)
	pruneSaveRun(t, r, old)
	kept := []string{"metadata.json", "config.yaml", "config.yml", "head", "findings.md", "knowledge.md", "contracts/nested/dag/impl.md", "results/impl.md", "tasks/task-1/task.json", "tasks/task-1/answer.md", "tasks/task-1/prompt.md", "deep/nested/large.md", ".lock", ".dag-scheduler.lock", "nested/.lock"}
	for _, name := range kept {
		prunePut(t, filepath.Join(r.dir(), name), "retentionneedle "+strings.Repeat("x", 100))
	}
	prunePut(t, filepath.Join(r.dir(), "deep/nested/large.md"), strings.Repeat("m", (1<<20)+1))
	inode, err := os.Stat(filepath.Join(r.dir(), ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	removed := []string{"output.log", "evidence/sub/cache.bin", "tasks/task-1/output.log", "nested/config.json", "large.bin"}
	for _, name := range removed {
		prunePut(t, filepath.Join(r.dir(), name), strings.Repeat("b", 1024))
	}
	outside := filepath.Join(t.TempDir(), "outside")
	prunePut(t, outside, "external")
	if err := os.Symlink(outside, filepath.Join(r.dir(), "external.md")); err != nil {
		t.Fatal(err)
	}
	report, freed := pruneDisk(now)
	if freed < 5*1024 {
		t.Fatalf("freed=%d report=%v", freed, report)
	}
	for _, name := range append(kept, "run.json") {
		pruneExists(t, filepath.Join(r.dir(), name), true)
	}
	for _, name := range append(removed, "external.md", "evidence") {
		pruneExists(t, filepath.Join(r.dir(), name), false)
	}
	pruneExists(t, outside, true)
	after, _ := os.Stat(filepath.Join(r.dir(), ".lock"))
	if !os.SameFile(inode, after) {
		t.Fatal("lock inode changed")
	}
	hits, err := recallSearch("retentionneedle", "", true, 50)
	if err != nil || len(hits) == 0 {
		t.Fatalf("recall=%v %v", hits, err)
	}
	marker, _ := os.ReadFile(filepath.Join(r.dir(), ".compacted"))
	prunePut(t, filepath.Join(r.dir(), "skip.bin"), "marker prevents walking")
	report, freed = pruneDisk(now.Add(time.Minute))
	if freed != 0 || strings.Contains(strings.Join(report, "\n"), "run compacted") {
		t.Fatal(report, freed)
	}
	pruneExists(t, filepath.Join(r.dir(), "skip.bin"), true)
	same, _ := os.ReadFile(filepath.Join(r.dir(), ".compacted"))
	if !bytes.Equal(marker, same) {
		t.Fatal("marker rewritten")
	}
	// New activity after compaction causes another pass once it ages past retention.
	prunePut(t, filepath.Join(r.dir(), "new.bin"), "new evidence")
	r.Events = append(r.Events, Event{At: now.Add(time.Hour)})
	pruneSaveRun(t, r, now.Add(time.Hour))
	pruneDisk(now.Add(10 * 24 * time.Hour))
	pruneExists(t, filepath.Join(r.dir(), "new.bin"), false)
	newer, _ := os.ReadFile(filepath.Join(r.dir(), ".compacted"))
	if bytes.Equal(marker, newer) {
		t.Fatal("marker did not advance")
	}
}
func TestPruneGoals(t *testing.T) {
	now := pruneEnv(t)
	old := now.Add(-8 * 24 * time.Hour)
	for _, name := range []string{"ended", "active", "draft", "referenced", "new", "event"} {
		status := "ended"
		if name == "active" || name == "draft" {
			status = name
		}
		g := pruneGoal(t, name, status, old)
		if name == "referenced" {
			r := pruneRun(t, "active-ref", old)
			r.Status = "active"
			r.Goal = name
			pruneSaveRun(t, r, old)
		}
		if name == "new" {
			pruneMtime(t, filepath.Join(g.dir(), "goal.json"), now)
		}
		if name == "event" {
			g.Events = []Event{{At: now}}
			if err := saveGoal(g); err != nil {
				t.Fatal(err)
			}
			pruneMtime(t, filepath.Join(g.dir(), "goal.json"), old)
		}
	}
	pruneDisk(now)
	for _, name := range []string{"ended", "active", "draft", "referenced", "new", "event"} {
		dir := filepath.Join(goalsDir(), name)
		pruneExists(t, filepath.Join(dir, "evidence", "blob.bin"), name != "ended")
		pruneExists(t, filepath.Join(dir, "goal.json"), true)
		pruneExists(t, filepath.Join(dir, "head"), true)
	}
}
func TestPruneOldFilesAndDisabled(t *testing.T) {
	now := pruneEnv(t)
	old := now.Add(-8 * 24 * time.Hour)
	for _, dir := range []string{"tests", "codex-goal-cache", "cache/ask"} {
		ext := ".json"
		if dir == "tests" {
			ext = ".log"
		}
		for _, name := range []string{"old", "new"} {
			p := filepath.Join(orchHome(), dir, name+ext)
			prunePut(t, p, "log")
			if name == "old" {
				pruneMtime(t, p, old)
			}
		}
	}
	r := pruneRun(t, "disabled", old)
	t.Setenv("ORCH_RETENTION_DAYS", "0")
	report, freed := pruneDisk(now)
	if len(report) != 0 || freed != 0 {
		t.Fatal(report, freed)
	}
	pruneExists(t, filepath.Join(r.dir(), "evidence", "blob.bin"), true)
	t.Setenv("ORCH_RETENTION_DAYS", "7")
	t.Setenv("ORCH_ASK_CACHE_TTL", "240")
	report, freed = pruneDisk(now)
	if freed == 0 || !strings.Contains(strings.Join(report, "\n"), "tests: 1 old logs deleted") {
		t.Fatal(report, freed)
	}
	pruneExists(t, filepath.Join(orchHome(), "tests", "old.log"), false)
	pruneExists(t, filepath.Join(orchHome(), "tests", "new.log"), true)
	pruneExists(t, filepath.Join(orchHome(), "codex-goal-cache", "old.json"), false)
	pruneExists(t, filepath.Join(orchHome(), "codex-goal-cache", "new.json"), true)
	pruneExists(t, askCachePath("old"), true)
	pruneDisk(now.Add(3 * 24 * time.Hour))
	pruneExists(t, askCachePath("old"), false)
	pruneExists(t, askCachePath("new"), true)
}
func TestRemoveTreeSafetyAndReadonly(t *testing.T) {
	pruneEnv(t)
	outside := filepath.Join(t.TempDir(), "target")
	prunePut(t, outside, "outside")
	for _, path := range []string{orchHome(), outside, filepath.Join(orchHome(), "..", "escape")} {
		if _, err := removeTree(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	dir := filepath.Join(orchHome(), "readonly")
	prunePut(t, filepath.Join(dir, "cache", "file"), "cache data")
	if err := os.Chmod(filepath.Join(dir, "cache"), 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	n, err := removeTree(dir)
	if err != nil || n != int64(len("cache data")) {
		t.Fatal(n, err)
	}
	pruneExists(t, outside, true)
	// A symlink in an intermediate path must not lead removal outside the home.
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(orchHome(), "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := removeTree(filepath.Join(orchHome(), "escape", "target")); err == nil {
		t.Fatal("followed symlink parent")
	}
	r := pruneRun(t, "readonly-run", time.Now().Add(-8*24*time.Hour))
	if err := os.Chmod(filepath.Join(r.dir(), "evidence"), 0555); err != nil {
		t.Fatal(err)
	}
	pruneDisk(time.Now())
	pruneExists(t, filepath.Join(r.dir(), "evidence"), false)
}
func TestPruneSymlinkRoots(t *testing.T) {
	for _, name := range []string{"runs", "goals", "tests", "codex-goal-cache", "cache", "cache/ask"} {
		t.Run(name, func(t *testing.T) {
			now := pruneEnv(t)
			outside := t.TempDir()
			p := filepath.Join(outside, "old.log")
			prunePut(t, p, "outside")
			pruneMtime(t, p, now.Add(-30*24*time.Hour))
			target := filepath.Join(orchHome(), name)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
			report, _ := pruneDisk(now)
			if !strings.Contains(strings.Join(report, "\n"), "skipped: "+name+" is a symlink") {
				t.Fatal(report)
			}
			pruneExists(t, p, true)
		})
	}
}
func TestPruneLocks(t *testing.T) {
	now := pruneEnv(t)
	old := now.Add(-8 * 24 * time.Hour)
	r := pruneRun(t, "locked", old)
	if err := os.MkdirAll(filepath.Join(orchHome(), "locks"), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := diskLock(filepath.Join(orchHome(), "locks", "prune.lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	report, _ := pruneDisk(now)
	unlockDisk(lock)
	if len(report) != 1 || report[0] != "prune already running" {
		t.Fatal(report)
	}
	scheduler, err := diskLock(filepath.Join(r.dir(), ".dag-scheduler.lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	pruneDisk(now)
	pruneExists(t, filepath.Join(r.dir(), "evidence", "blob.bin"), true)
	unlockDisk(scheduler)
	pruneDisk(now)
	pruneExists(t, filepath.Join(r.dir(), "evidence", "blob.bin"), false)
	// The stable lock file is retained by compaction and remains usable.
	lock, err = diskLock(filepath.Join(r.dir(), ".lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	unlockDisk(lock)
}
func TestGCDiskIdentityAndWorkerGate(t *testing.T) {
	pruneEnv(t)
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "foreign")
	fakeCLI(t, "herdr", "exit 1\n")
	for _, tc := range []struct {
		args []string
		want int
	}{{[]string{"gc", "--disk"}, 0}, {[]string{"gc"}, 1}, {[]string{"gc", "--disk=true"}, 1}, {[]string{"gc", "--disk", "--orphans"}, 1}} {
		var out, errs bytes.Buffer
		if got := run(tc.args, strings.NewReader(""), &out, &errs); got != tc.want {
			t.Fatalf("%v: %d %s", tc.args, got, errs.String())
		}
		if tc.want == 1 && !strings.Contains(errs.String(), "identity check failed") {
			t.Fatal(errs.String())
		}
	}
	t.Setenv("HERDR_PANE_ID", "")
	var out, errs bytes.Buffer
	if got := run([]string{"gc", "--disk", "--orphans"}, strings.NewReader(""), &out, &errs); got != 1 || !strings.Contains(errs.String(), "cannot be combined") {
		t.Fatal(got, errs.String())
	}
	t.Setenv("ORCH_ROLE", "worker")
	errs.Reset()
	if got := run([]string{"gc", "--disk"}, strings.NewReader(""), &out, &errs); got != 1 || !strings.Contains(errs.String(), "workers cannot use orch gc") {
		t.Fatal(got, errs.String())
	}
}
func TestAutoPruneGates(t *testing.T) {
	for _, name := range []string{"worker", "disabled", "retention-zero", "gc", "internal", "fresh-stamp", "busy"} {
		t.Run(name, func(t *testing.T) {
			now := pruneEnv(t)
			t.Setenv("ORCH_AUTO_PRUNE", "1")
			t.Setenv("ORCH_ROLE", "")
			args := []string{"models"}
			switch name {
			case "worker":
				t.Setenv("ORCH_ROLE", "worker")
			case "disabled":
				t.Setenv("ORCH_AUTO_PRUNE", "0")
			case "retention-zero":
				t.Setenv("ORCH_RETENTION_DAYS", "0")
			case "gc":
				args = []string{"gc", "--disk"}
			case "internal":
				args = []string{"__task"}
			case "fresh-stamp":
				prunePut(t, filepath.Join(orchHome(), "prune.stamp"), "")
				pruneMtime(t, filepath.Join(orchHome(), "prune.stamp"), now)
			case "busy":
				if err := os.MkdirAll(filepath.Join(orchHome(), "locks"), 0700); err != nil {
					t.Fatal(err)
				}
				lock, err := diskLock(filepath.Join(orchHome(), "locks", "prune-trigger.lock"), true)
				if err != nil {
					t.Fatal(err)
				}
				defer unlockDisk(lock)
			}
			maybeAutoPrune(args)
			pruneExists(t, filepath.Join(orchHome(), "prune.log"), false)
			if name != "fresh-stamp" {
				pruneExists(t, filepath.Join(orchHome(), "prune.stamp"), false)
			}
		})
	}
}
func TestAutoPruneDetachedCLI(t *testing.T) {
	now := pruneEnv(t)
	old := now.Add(-8 * 24 * time.Hour)
	r := pruneRun(t, "auto", old)
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_AUTO_PRUNE", "1")
	t.Setenv("HERDR_PANE_ID", "foreign")
	t.Setenv("ORCH_MUX", "herdr")
	binary := filepath.Join(t.TempDir(), "orch")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if out, err := exec.Command(binary, "help").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(filepath.Join(orchHome(), "prune.log")); err == nil && strings.Contains(string(b), "disk prune freed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached prune did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pruneExists(t, filepath.Join(r.dir(), "evidence", "blob.bin"), false)
	stamp, err := os.Stat(filepath.Join(orchHome(), "prune.stamp"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(binary, "help").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	same, _ := os.Stat(filepath.Join(orchHome(), "prune.stamp"))
	if !same.ModTime().Equal(stamp.ModTime()) {
		t.Fatal("triggered again within 24 hours")
	}
	// An expired stamp triggers another detached pass.
	pruneMtime(t, filepath.Join(orchHome(), "prune.stamp"), now.Add(-25*time.Hour))
	if out, err := exec.Command(binary, "help").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	// Wait for the prune lock so cleanup cannot race t.TempDir removal.
	deadline = time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(orchHome(), "prune.log"))
		if strings.Contains(string(b), "disk prune freed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second detached prune did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Wait for the child to release the global lock before fixture cleanup.
	for {
		lock, err := diskLock(filepath.Join(orchHome(), "locks", "prune.lock"), true)
		if err == nil {
			unlockDisk(lock)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPruneRechecksUnderStateLock(t *testing.T) {
	for _, kind := range []string{"run", "goal"} {
		t.Run(kind, func(t *testing.T) {
			now := pruneEnv(t)
			old := now.Add(-8 * 24 * time.Hour)
			var dir string
			var change func()
			if kind == "run" {
				r := pruneRun(t, "locked", old)
				dir = r.dir()
				change = func() { r.Status = "active"; pruneSaveRun(t, r, old) }
			} else {
				g := pruneGoal(t, "locked", "ended", old)
				dir = g.dir()
				change = func() {
					g.Status = "active"
					if err := saveGoal(g); err != nil {
						t.Fatal(err)
					}
					pruneMtime(t, filepath.Join(dir, "goal.json"), old)
				}
			}
			lock, err := diskLock(filepath.Join(dir, ".lock"), true)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { defer close(done); pruneDisk(now) }()
			select {
			case <-done:
				unlockDisk(lock)
				t.Fatal("prune did not honor state lock")
			case <-time.After(50 * time.Millisecond):
			}
			change()
			unlockDisk(lock)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("prune stuck")
			}
			pruneExists(t, filepath.Join(dir, "evidence", "blob.bin"), true)
		})
	}
}

func TestPruneResolvedHome(t *testing.T) {
	now := pruneEnv(t)
	realHome := orchHome()
	link := filepath.Join(t.TempDir(), "orch-link")
	if err := os.Symlink(realHome, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCH_HOME", link)
	r := pruneRun(t, "resolved", now.Add(-8*24*time.Hour))
	report, _ := pruneDisk(now)
	if !strings.Contains(strings.Join(report, "\n"), "run compacted") {
		t.Fatal(report)
	}
	pruneExists(t, filepath.Join(r.dir(), "evidence"), false)
}
