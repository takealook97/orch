package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Regression tests for the crit2 findings on update, the trigger and launchd.

func TestUpdateRefusesToOverwriteIgnoredFile(t *testing.T) {
	clone, upstream := updateFixture(t)
	os.WriteFile(filepath.Join(upstream, "local.go"), []byte("upstream\n"), 0600)
	updateTestGit(t, upstream, "add", ".")
	updateTestGit(t, upstream, "commit", "--quiet", "-m", "add local.go")
	updateTestGit(t, upstream, "push", "--quiet", "origin", "main")
	os.WriteFile(filepath.Join(clone, ".git", "info", "exclude"), []byte("local.go\n"), 0600)
	os.WriteFile(filepath.Join(clone, "local.go"), []byte("mine\n"), 0600)
	writeUpdateConfig(t, clone, "")
	before := updateTestGit(t, clone, "rev-parse", "HEAD")
	err := runUpdate(context.Background(), updateOptions{Out: &bytes.Buffer{}})
	if data, _ := os.ReadFile(filepath.Join(clone, "local.go")); string(data) != "mine\n" {
		t.Fatalf("ignored file overwritten: %q (err %v)", data, err)
	}
	if err == nil || updateTestGit(t, clone, "rev-parse", "HEAD") != before {
		t.Fatalf("merge over an ignored file must fail and keep HEAD: %v", err)
	}
}

func TestUpdateSeesUntrackedDespiteGitConfig(t *testing.T) {
	clone, _ := updateFixture(t)
	updateTestGit(t, clone, "config", "status.showUntrackedFiles", "no")
	os.WriteFile(filepath.Join(clone, "memo.txt"), []byte("x\n"), 0600)
	writeUpdateConfig(t, clone, "")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skip: working tree is dirty") {
		t.Fatal(out.String())
	}
}

func TestUpdateFollowsCurrentRemoteDefaultBranch(t *testing.T) {
	clone, upstream := updateFixture(t)
	updateTestGit(t, upstream, "push", "--quiet", "origin", "main:trunk")
	bare := updateTestGit(t, upstream, "remote", "get-url", "origin")
	updateTestGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/trunk")
	writeUpdateConfig(t, clone, "")
	before := updateTestGit(t, clone, "rev-parse", "HEAD")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `not default "trunk"`) || updateTestGit(t, clone, "rev-parse", "HEAD") != before {
		t.Fatalf("stale origin/HEAD was followed: %s", out.String())
	}
}

func TestUpdateRechecksBranchBeforeMerge(t *testing.T) {
	clone, _ := updateFixture(t)
	writeUpdateConfig(t, clone, "")
	before := updateTestGit(t, clone, "rev-parse", "HEAD")
	calls := 0
	updateLiveWriters = func(string) int {
		calls++
		if calls == 1 {
			updateTestGit(t, clone, "checkout", "--quiet", "-b", "topic")
		}
		return 0
	}
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `skip: on branch "topic"`) || updateTestGit(t, clone, "rev-parse", "HEAD") != before {
		t.Fatalf("branch switched after the first check was updated: %s", out.String())
	}
}

func TestUpdateRetriesFailedInstall(t *testing.T) {
	clone, upstream := updateFixture(t)
	writeUpdateConfig(t, clone, "exit 9")
	if err := runUpdate(context.Background(), updateOptions{Out: &bytes.Buffer{}}); err == nil {
		t.Fatal("failed install was not reported")
	}
	if updateTestGit(t, clone, "rev-parse", "HEAD") != updateTestGit(t, upstream, "rev-parse", "HEAD") {
		t.Fatal("merge did not happen before the install")
	}
	writeUpdateConfig(t, clone, "touch installed.marker")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err, out.String())
	}
	if !fileExists(filepath.Join(clone, "installed.marker")) || !strings.Contains(out.String(), "up to date, installed ") {
		t.Fatalf("install was not retried: %s", out.String())
	}
	os.Remove(filepath.Join(clone, "installed.marker"))
	out.Reset()
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(clone, "installed.marker")) {
		t.Fatalf("installed commit was installed again: %s", out.String())
	}
}

func TestUpdateFirstSightRecordsBaselineWithoutInstall(t *testing.T) {
	clone, _ := updateFixture(t)
	updateTestGit(t, clone, "pull", "--quiet", "--ff-only")
	writeUpdateConfig(t, clone, "touch installed.marker")
	if err := runUpdate(context.Background(), updateOptions{Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	st, err := loadUpdateState()
	if err != nil || st.Repos[updateStateKey(clone)].InstalledCommit != updateTestGit(t, clone, "rev-parse", "HEAD") {
		t.Fatalf("baseline not recorded: %+v %v", st, err)
	}
	if fileExists(filepath.Join(clone, "installed.marker")) {
		t.Fatal("first sight of an up-to-date checkout ran install")
	}
}

func TestUpdateReportsLogWriteFailure(t *testing.T) {
	clone, upstream := updateFixture(t)
	writeUpdateConfig(t, clone, "")
	if err := os.MkdirAll(updateLogPath(), 0700); err != nil {
		t.Fatal(err)
	}
	err := runUpdate(context.Background(), updateOptions{})
	if err == nil || !strings.Contains(err.Error(), "update steps finished but writing") {
		t.Fatalf("log failure lost: %v", err)
	}
	if updateTestGit(t, clone, "rev-parse", "HEAD") != updateTestGit(t, upstream, "rev-parse", "HEAD") {
		t.Fatal("update itself should still have happened")
	}
}

func TestUpdateDryRunWritesNoGitMetadata(t *testing.T) {
	clone, _ := updateFixture(t)
	writeUpdateConfig(t, clone, "")
	ref := updateTestGit(t, clone, "rev-parse", "refs/remotes/origin/main")
	if err := runUpdate(context.Background(), updateOptions{DryRun: true, Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(clone, ".git", "FETCH_HEAD")) || updateTestGit(t, clone, "rev-parse", "refs/remotes/origin/main") != ref {
		t.Fatal("dry run fetched")
	}
	if fileExists(updateStampPath()) {
		t.Fatal("dry run touched the stamp")
	}
}

func TestAutoUpdateTriggerSpawnsOnceUnderRace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	t.Setenv("ORCH_AUTO_UPDATE", "")
	t.Setenv("ORCH_ROLE", "")
	var spawned atomic.Int32
	orig := autoUpdateSpawn
	autoUpdateSpawn = func() error { spawned.Add(1); time.Sleep(20 * time.Millisecond); return nil }
	t.Cleanup(func() { autoUpdateSpawn = orig })
	if maybeAutoUpdate([]string{"models", "refresh", "--dry-run"}) || spawned.Load() != 0 {
		t.Fatal("models refresh triggered an update")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); maybeAutoUpdate([]string{"status"}) }()
	}
	wg.Wait()
	if spawned.Load() != 1 {
		t.Fatalf("stale stamp spawned %d updates", spawned.Load())
	}
	old := time.Now().Add(-7 * time.Hour)
	os.Chtimes(updateStampPath(), old, old)
	lock, err := diskLock(updateTriggerLockPath(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockDisk(lock)
	if maybeAutoUpdate([]string{"status"}) || spawned.Load() != 1 {
		t.Fatal("trigger did not back off while another process held the lock")
	}
}

func TestInstallLaunchAgentRetriesFailedRegistration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	var calls []string
	loaded, failBootstrap := false, true
	origRun, origOS := launchctlRun, launchdGOOS
	launchctlRun = func(args ...string) error {
		calls = append(calls, args[0])
		switch args[0] {
		case "print":
			if !loaded {
				return errors.New("not loaded")
			}
		case "bootstrap":
			if failBootstrap {
				return errors.New("bootstrap failed")
			}
			loaded = true
		}
		return nil
	}
	launchdGOOS = "darwin"
	t.Cleanup(func() { launchctlRun, launchdGOOS = origRun, origOS })
	save := func(string) error { return nil }
	if err := installLaunchAgent(&installReport{}, "/bin/orch", false, save); err == nil {
		t.Fatal("bootstrap failure was not reported")
	}
	failBootstrap = false
	calls = nil
	if err := installLaunchAgent(&installReport{}, "/bin/orch", false, save); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "print,bootout,bootstrap" || !loaded {
		t.Fatalf("identical plist was not registered again: %v", calls)
	}
}
