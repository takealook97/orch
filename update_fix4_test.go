package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// N1: a local branch named origin/main must not hide the fetched remote commit.
func TestUpdateIgnoresLocalBranchNamedOriginMain(t *testing.T) {
	clone, upstream := updateFixture(t)
	updateTestGit(t, clone, "branch", "origin/main", "HEAD")
	updateTestGit(t, clone, "config", "core.warnAmbiguousRefs", "false")
	msg, err := updateOneRepo(context.Background(), updateRepo{Path: clone}, false)
	if err != nil || !strings.HasPrefix(msg, "updated ") {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
	if got, want := updateTestGit(t, clone, "rev-parse", "HEAD"), updateTestGit(t, upstream, "rev-parse", "HEAD"); got != want {
		t.Fatalf("HEAD=%s, want origin %s", got, want)
	}
}

// N7: a missing, null or mismatched state is unknown: install runs and only success is recorded.
func TestUpdateUnknownStateRunsInstall(t *testing.T) {
	for name, state := range map[string]string{
		"missing":    "",
		"null":       "null",
		"no-entry":   `{"repos":{}}`,
		"null-repos": `{"repos":null}`,
		"mismatch":   `{"repos":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			clone, _ := updateFixture(t)
			updateTestGit(t, clone, "pull", "--quiet", "--ff-only")
			if err := os.MkdirAll(orchHome(), 0700); err != nil {
				t.Fatal(err)
			}
			if state != "" {
				if err := os.WriteFile(updateStatePath(), []byte(state), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := updateRepo{Path: clone, Install: "touch install-ran"}
			msg, err := updateOneRepo(context.Background(), r, false)
			if err != nil || !fileExists(filepath.Join(clone, "install-ran")) {
				t.Fatalf("install did not run: msg=%q err=%v", msg, err)
			}
			st, err := loadUpdateState()
			head := updateTestGit(t, clone, "rev-parse", "HEAD")
			if err != nil || st.Repos[updateStateKey(clone)].InstalledCommit != head {
				t.Fatalf("state=%+v err=%v", st, err)
			}
		})
	}
}

// N7: a failing install on an unknown state records nothing, so the next run installs again.
func TestUpdateUnknownStateFailedInstallRecordsNothing(t *testing.T) {
	clone, _ := updateFixture(t)
	updateTestGit(t, clone, "pull", "--quiet", "--ff-only")
	if _, err := updateOneRepo(context.Background(), updateRepo{Path: clone, Install: "exit 3"}, false); err == nil {
		t.Fatal("failed install not reported")
	}
	st, err := loadUpdateState()
	if err != nil || len(st.Repos) != 0 {
		t.Fatalf("state=%+v err=%v", st, err)
	}
}

// N7: truncated JSON stays an error and the file is kept as is.
func TestUpdateTruncatedStateIsAnError(t *testing.T) {
	clone, _ := updateFixture(t)
	updateTestGit(t, clone, "pull", "--quiet", "--ff-only")
	os.MkdirAll(orchHome(), 0700)
	if err := os.WriteFile(updateStatePath(), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := updateOneRepo(context.Background(), updateRepo{Path: clone, Install: "touch install-ran"}, false)
	data, _ := os.ReadFile(updateStatePath())
	if err == nil || string(data) != "{" || fileExists(filepath.Join(clone, "install-ran")) {
		t.Fatalf("err=%v state=%q", err, data)
	}
}

// 12: `models refresh --dry-run` starts neither the automatic prune nor the automatic update.
func TestModelsDryRunEntrySkipsAutoMaintenance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_AUTO_UPDATE", "1")
	t.Setenv("ORCH_AUTO_PRUNE", "1")
	t.Setenv("ORCH_RETENTION_DAYS", "7")
	if err := os.MkdirAll(orchHome(), 0700); err != nil {
		t.Fatal(err)
	}
	if !touchStamp(filepath.Join(orchHome(), "prune.stamp"), time.Now()) {
		t.Fatal("prune stamp")
	}
	spawned := 0
	saved := autoUpdateSpawn
	autoUpdateSpawn = func() error { spawned++; return nil }
	t.Cleanup(func() { autoUpdateSpawn = saved })
	for _, args := range [][]string{
		{"models", "refresh", "--dry-run"},
		{"models", "refresh", "--repo", ".", "-dry-run"},
		{"models", "refresh", "--dry-run=true"},
	} {
		entryMaintenance(args)
	}
	if fileExists(filepath.Join(orchHome(), "locks", "prune-trigger.lock")) || fileExists(updateTriggerLockPath()) || spawned != 0 {
		var b bytes.Buffer
		entries, _ := os.ReadDir(orchHome())
		for _, e := range entries {
			b.WriteString(e.Name() + " ")
		}
		t.Fatalf("dry-run entry wrote under ORCH_HOME: %s spawned=%d", b.String(), spawned)
	}
}
