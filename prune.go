package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func retentionWindow() time.Duration {
	days, err := strconv.ParseInt(envOrch("RETENTION_DAYS"), 10, 64)
	if err != nil || days < 0 || days > int64((1<<63-1)/(24*time.Hour)) {
		days = 7
	}
	return time.Duration(days) * 24 * time.Hour
}

func resolvedOrchHome() (string, error) {
	home, err := filepath.Abs(orchHome())
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(home)
}

// prunePath uses the resolved home, rejects lexical escapes and symlink parents,
// and deliberately leaves a final symlink untouched for unlinking by the caller.
func prunePath(home, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	original, err := filepath.Abs(orchHome())
	if err != nil {
		return "", err
	}
	if rel, e := filepath.Rel(original, abs); e == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		abs = filepath.Join(home, rel)
	}
	rel, err := filepath.Rel(home, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to prune outside orch home: %s", path)
	}
	for p := filepath.Dir(abs); p != home; p = filepath.Dir(p) {
		fi, e := os.Lstat(p)
		if e != nil {
			return "", e
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink parent: %s", p)
		}
	}
	return abs, nil
}

func removeTree(path string) (int64, error) {
	home, err := resolvedOrchHome()
	if err != nil {
		return 0, err
	}
	return removeTreeIn(home, path)
}

// chmodPruneDirIn repairs only real directories within the resolved home.
func chmodPruneDirIn(home, path string) error {
	path, err := prunePath(home, path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to chmod non-directory: %s", path)
	}
	return os.Chmod(path, 0700)
}

func removeTreeIn(home, path string) (int64, error) {
	path, err := prunePath(home, path)
	if err != nil {
		return 0, err
	}
	var size int64
	var measure fs.WalkDirFunc
	measure = func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			if d != nil && d.IsDir() {
				if errors.Is(e, fs.ErrPermission) && chmodPruneDirIn(home, p) == nil {
					if entries, err := os.ReadDir(p); err == nil {
						for _, entry := range entries {
							_ = filepath.WalkDir(filepath.Join(p, entry.Name()), measure)
						}
					}
				}
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			fi, e := d.Info()
			if e != nil {
				return nil
			}
			size += fi.Size()
		}
		return nil
	}
	// Sizing is best effort: an unreadable subtree must not block removal.
	_ = filepath.WalkDir(path, measure)
	if err = os.RemoveAll(path); err != nil {
		_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
			if d != nil && d.IsDir() {
				return chmodPruneDirIn(home, p)
			}
			return e
		})
		err = os.RemoveAll(path)
	}
	if err != nil {
		return 0, err
	}
	return size, nil
}

func diskKeep(rel string, info fs.FileInfo) bool {
	// Lock inodes must survive even when located inside a nested DAG directory.
	if strings.HasSuffix(info.Name(), ".lock") {
		return true
	}
	if !info.Mode().IsRegular() {
		return false
	}
	if filepath.Ext(rel) == ".md" {
		return true
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 1 {
		switch rel {
		case "run.json", "goal.json", "head", ".compacted":
			return true
		}
		switch filepath.Ext(rel) {
		case ".json", ".yaml", ".yml":
			return true
		}
	}
	return len(parts) == 3 && parts[0] == "tasks" && parts[2] == "task.json"
}

func compactDir(dir string, keep func(string, fs.FileInfo) bool) (int64, error) {
	home, err := resolvedOrchHome()
	if err != nil {
		return 0, err
	}
	return compactDirIn(home, dir, keep, time.Now())
}
func compactDirIn(home, dir string, keep func(string, fs.FileInfo) bool, now time.Time) (int64, error) {
	dir, err := prunePath(home, dir)
	if err != nil {
		return 0, err
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return 0, fmt.Errorf("cannot compact directory %s", dir)
	}
	var freed int64
	var walk func(string) error
	walk = func(parent string) error {
		entries, err := os.ReadDir(parent)
		if errors.Is(err, fs.ErrPermission) {
			if e := chmodPruneDirIn(home, parent); e != nil {
				return errors.Join(err, e)
			}
			entries, err = os.ReadDir(parent)
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(parent, entry.Name())
			info, err := entry.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			if entry.IsDir() {
				if err := walk(path); err != nil {
					return err
				}
				remaining, err := os.ReadDir(path)
				if err != nil {
					return err
				}
				if len(remaining) != 0 {
					continue
				}
				// Resume may write into these run directories after compaction.
				if parent == dir && filepath.Dir(dir) == filepath.Join(home, "runs") && contains1([]string{"contracts", "results", "evidence", "tasks"}, entry.Name()) {
					continue
				}
			} else if keep(rel, info) {
				continue
			}
			n, err := removeTreeIn(home, path)
			if err != nil && errors.Is(err, fs.ErrPermission) {
				if e := chmodPruneDirIn(home, parent); e != nil {
					return errors.Join(err, e)
				}
				n, err = removeTreeIn(home, path)
			}
			freed += n
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return freed, err
	}
	return freed, os.WriteFile(filepath.Join(dir, ".compacted"), []byte(now.Format(time.RFC3339Nano)+"\n"), 0600)
}

func latestTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
func metadataActivity(created time.Time, events []Event, path string) time.Time {
	for _, event := range events {
		created = latestTime(created, event.At)
	}
	if info, err := os.Stat(path); err == nil {
		created = latestTime(created, info.ModTime())
	}
	return created
}
func runLastActivity(r *Run) time.Time {
	last := metadataActivity(r.Created, r.Events, filepath.Join(r.dir(), "run.json"))
	for _, t := range r.Tasks {
		if t != nil {
			last = latestTime(last, t.Heartbeat)
			if disk, err := readTask(r.ID, t.ID); err == nil {
				last = latestTime(last, disk.Heartbeat)
			}
		}
	}
	return last
}
func goalLastActivity(g *Goal) time.Time {
	return metadataActivity(g.Created, g.Events, filepath.Join(g.dir(), "goal.json"))
}
func alreadyCompacted(dir string, activity time.Time) bool {
	b, err := os.ReadFile(filepath.Join(dir, ".compacted"))
	if err != nil {
		return false
	}
	stamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
	return err == nil && !stamp.Before(activity)
}
func runHoldsWorktree(r *Run) bool {
	for _, e := range worktreeEntries(r) {
		if *e.Dir != "" {
			return true
		}
		if *e.Branch != "" {
			if _, err := gitOutput(*e.Base, "show-ref", "--verify", "refs/heads/"+*e.Branch); err == nil {
				return true
			}
		}
	}
	return false
}
func runHasLiveTask(r *Run) bool {
	for _, t := range r.Tasks {
		if t == nil {
			continue
		}
		if disk, err := readTask(r.ID, t.ID); err == nil {
			t = disk
		}
		// Finished tasks cannot pin a run through a recycled runner PID.
		if contains1([]string{"done", "failed", "timeout"}, t.Status) {
			continue
		}
		// Unfinished tasks need a live runner or heartbeat. An unclaimed task
		// with neither has aged past TaskStale by the run's retention cutoff.
		if (t.Pid > 0 || t.Pgid > 0 || !t.Heartbeat.IsZero()) && taskDead(t) == "" {
			return true
		}
	}
	return false
}
func runPrunable(r *Run, cutoff time.Time) bool {
	if r.Status != "closed" || !runLastActivity(r).Before(cutoff) || runHoldsWorktree(r) || runHasLiveTask(r) {
		return false
	}
	if r.Goal == "" {
		return true
	}
	fi, err := os.Lstat(filepath.Join(goalsDir(), r.Goal))
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !fi.IsDir() {
		return false
	}
	g, err := loadGoal(r.Goal)
	return err == nil && g.Status == "ended"
}

// diskLock never follows a symlink to a lock file.
func diskLock(path string, nonblock bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	flags := syscall.LOCK_EX
	if nonblock {
		flags |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), flags); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func unlockDisk(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }

func compactRuns(home string, now time.Time, keep time.Duration) ([]string, int64) {
	runs, err := allRuns()
	if err != nil {
		return []string{fmt.Sprintf("runs: %v", err)}, 0
	}
	return pruneSelectedRuns(home, now, keep, runs)
}
func pruneSelectedRuns(home string, now time.Time, keep time.Duration, runs []*Run) ([]string, int64) {
	var report []string
	var freed int64
	for _, candidate := range runs {
		// Do not create a lock file or otherwise modify ineligible directories.
		if !runPrunable(candidate, now.Add(-keep)) {
			continue
		}
		dir, err := prunePath(home, candidate.dir())
		if err != nil {
			continue
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		lock, err := diskLock(filepath.Join(dir, ".lock"), false)
		if err != nil {
			continue
		}
		func() {
			defer unlockDisk(lock)
			scheduler := filepath.Join(dir, ".dag-scheduler.lock")
			if _, err := os.Lstat(scheduler); err == nil {
				f, err := diskLock(scheduler, true)
				if err != nil {
					return
				}
				defer unlockDisk(f)
			} else if !errors.Is(err, os.ErrNotExist) {
				return
			}
			r, err := loadRun(candidate.ID)
			if err != nil || r.ID != candidate.ID || !runPrunable(r, now.Add(-keep)) {
				return
			}
			var n int64
			if r.Adhoc {
				n, err = removeTreeIn(home, dir)
				if err == nil {
					report = append(report, r.ID+": ad-hoc run deleted")
				}
			} else {
				if alreadyCompacted(dir, runLastActivity(r)) {
					return
				}
				n, err = compactDirIn(home, dir, diskKeep, now)
				if err == nil {
					report = append(report, fmt.Sprintf("%s: run compacted (freed %s)", r.ID, diskSize(n)))
				}
			}
			freed += n
			if err != nil {
				report = append(report, fmt.Sprintf("%s: prune failed: %v", candidate.ID, err))
			}
		}()
	}
	return report, freed
}
func compactGoals(home string, now time.Time, keep time.Duration) ([]string, int64) {
	var report []string
	var freed int64
	entries, err := os.ReadDir(goalsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return []string{fmt.Sprintf("goals: %v", err)}, 0
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		g, err := loadGoal(entry.Name())
		if err != nil || g.Status != "ended" || !goalLastActivity(g).Before(now.Add(-keep)) {
			continue
		}
		dir, err := prunePath(home, g.dir())
		if err != nil {
			continue
		}
		lock, err := diskLock(filepath.Join(dir, ".lock"), false)
		if err != nil {
			continue
		}
		func() {
			defer unlockDisk(lock)
			g, err := loadGoal(entry.Name())
			if err != nil || g.Status != "ended" || !goalLastActivity(g).Before(now.Add(-keep)) || alreadyCompacted(dir, goalLastActivity(g)) {
				return
			}
			// A symlinked/unreadable runs directory cannot prove absence of live references.
			info, err := os.Lstat(runsDir())
			if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !info.IsDir() {
				return
			}
			runs, err := allRuns()
			if err != nil {
				return
			}
			for _, r := range runs {
				if r.Goal == g.ID && (r.Status != "closed" || runHasLiveTask(r)) {
					return
				}
			}
			n, err := compactDirIn(home, dir, diskKeep, now)
			freed += n
			if err != nil {
				report = append(report, fmt.Sprintf("%s: prune failed: %v", g.ID, err))
				return
			}
			report = append(report, fmt.Sprintf("%s: goal compacted (freed %s)", g.ID, diskSize(n)))
		}()
	}
	return report, freed
}
func pruneOldFiles(glob string, cutoff time.Time) (int, int64) {
	home, err := resolvedOrchHome()
	if err != nil {
		return 0, 0
	}
	return pruneOldFilesIn(home, glob, cutoff)
}
func pruneOldFilesIn(home, glob string, cutoff time.Time) (int, int64) {
	count := 0
	var freed int64
	paths, _ := filepath.Glob(glob)
	for _, path := range paths {
		path, err := prunePath(home, path)
		if err != nil {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(path); err == nil {
			count++
			freed += info.Size()
		}
	}
	return count, freed
}
func diskSize(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}
func pruneDisk(now time.Time) ([]string, int64) {
	keep := retentionWindow()
	if keep == 0 {
		return nil, 0
	}
	if err := os.MkdirAll(orchHome(), 0700); err != nil {
		return []string{err.Error()}, 0
	}
	home, err := resolvedOrchHome()
	if err != nil {
		return []string{err.Error()}, 0
	}
	locks := filepath.Join(home, "locks")
	if info, err := os.Lstat(locks); err == nil && !info.IsDir() {
		return []string{"skipped: locks is a symlink or not a directory"}, 0
	}
	if err := os.MkdirAll(locks, 0700); err != nil {
		return []string{err.Error()}, 0
	}
	lock, err := diskLock(filepath.Join(locks, "prune.lock"), true)
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return []string{"prune already running"}, 0
		}
		return []string{err.Error()}, 0
	}
	defer unlockDisk(lock)
	var report []string
	var freed int64
	safeDir := func(name string) bool {
		info, err := os.Lstat(filepath.Join(home, name))
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			report = append(report, "skipped: "+name+" is a symlink")
			return false
		}
		return err == nil && info.IsDir()
	}
	if safeDir("runs") {
		lines, n := compactRuns(home, now, keep)
		report = append(report, lines...)
		freed += n
	}
	if safeDir("goals") {
		lines, n := compactGoals(home, now, keep)
		report = append(report, lines...)
		freed += n
	}
	for _, item := range []struct {
		dir, pattern, label string
		age                 time.Duration
	}{
		{"tests", "*.log", "tests: %d old logs deleted", keep},
		{"codex-goal-cache", "*.json", "codex-goal-cache: %d old entries deleted", keep},
		{"cache/ask", "*.json", "cache/ask: %d old entries deleted", max(keep, askCacheTTL())},
	} {
		if item.dir == "cache/ask" && !safeDir("cache") {
			continue
		}
		if !safeDir(item.dir) {
			continue
		}
		count, n := pruneOldFilesIn(home, filepath.Join(home, item.dir, item.pattern), now.Add(-item.age))
		freed += n
		if count > 0 {
			report = append(report, fmt.Sprintf(item.label, count))
		}
	}
	report = append(report, "disk prune freed "+diskSize(freed))
	return report, freed
}

func maybeAutoPrune(args []string) {
	if envOrch("ROLE") == "worker" || envOrch("AUTO_PRUNE") == "0" || retentionWindow() == 0 || len(args) > 0 && (args[0] == "gc" || strings.HasPrefix(args[0], "__")) {
		return
	}
	home := orchHome()
	if os.MkdirAll(home, 0700) != nil {
		return
	}
	home, err := resolvedOrchHome()
	if err != nil {
		return
	}
	if info, err := os.Lstat(filepath.Join(home, "locks")); err == nil && !info.IsDir() {
		return
	}
	if os.MkdirAll(filepath.Join(home, "locks"), 0700) != nil {
		return
	}
	lock, err := diskLock(filepath.Join(home, "locks", "prune-trigger.lock"), true)
	if err != nil {
		return
	}
	defer unlockDisk(lock)
	stamp := filepath.Join(home, "prune.stamp")
	now := time.Now()
	if info, err := os.Stat(stamp); err == nil && now.Sub(info.ModTime()) <= 24*time.Hour {
		return
	}
	f, err := os.OpenFile(stamp, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return
	}
	f.Close()
	if os.Chtimes(stamp, now, now) != nil {
		return
	}
	retry := func() { _ = os.Chtimes(stamp, time.Unix(0, 0), time.Unix(0, 0)) }
	exe, err := os.Executable()
	if err != nil {
		retry()
		return
	}
	log, err := os.OpenFile(filepath.Join(home, "prune.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		retry()
		return
	}
	defer log.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		retry()
		return
	}
	defer input.Close()
	cmd := exec.Command(exe, "gc", "--disk")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = input
	cmd.Stdout = log
	cmd.Stderr = log
	if cmd.Start() != nil {
		retry()
		return
	}
	_ = cmd.Process.Release()
}

func diskPruneReport(now time.Time) []string { report, _ := pruneDisk(now); return report }
