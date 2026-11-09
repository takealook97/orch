package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGCStaleClosureSurvivesPaneListError(t *testing.T) {
	now := pruneEnv(t)
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "current-head")
	t.Setenv("HC_TERMINAL_ID", "")
	old := now.Add(-10 * 24 * time.Hour)
	stale := pruneRun(t, "stale-foreign", old)
	stale.Status, stale.Backend, stale.HeadPane = "active", "app", "old-head"
	pruneSaveRun(t, stale, old)
	owned := pruneRun(t, "recent-owned", now)
	owned.Backend, owned.HeadPane = "herdr", "current-head"
	pruneSaveRun(t, owned, now)
	original := herdrRun
	t.Cleanup(func() { herdrRun = original })
	paneErr := errors.New("pane listing unavailable")
	herdrRun = func(args ...string) ([]byte, error) { return nil, paneErr }
	report, err := gc(true)
	if !errors.Is(err, paneErr) {
		t.Fatalf("gc error=%v, want pane error", err)
	}
	saved, err := loadRun(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "closed" {
		t.Fatalf("stale status=%s report=%v", saved.Status, report)
	}
	if strings.Count(strings.Join(report, "\n"), "closed stale run "+stale.ID) != 1 {
		t.Fatalf("closure report=%v", report)
	}
}

func TestGCKeepsOwnedClosedRunMtime(t *testing.T) {
	now := pruneEnv(t)
	t.Setenv("ORCH_MUX", "herdr")
	t.Setenv("HERDR_PANE_ID", "current-head")
	t.Setenv("HC_TERMINAL_ID", "")
	old := now.Add(-3 * 24 * time.Hour)
	r := pruneRun(t, "owned-closed", old)
	r.Status, r.Backend, r.HeadPane = "closed", "herdr", "current-head"
	pruneSaveRun(t, r, old)
	path := filepath.Join(r.dir(), "run.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original := herdrRun
	t.Cleanup(func() { herdrRun = original })
	herdrRun = func(args ...string) ([]byte, error) {
		return []byte(`{"result":{"panes":[{"pane_id":"current-head"}]}}`), nil
	}
	if _, err := gc(false); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("mtime changed: before=%v after=%v", before.ModTime(), after.ModTime())
	}
}
