package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestOverflowCleanupQuietUnowned(t *testing.T) {
	r, f := overflowFixture(t)
	tab := "quiet-unowned"
	r.OverflowTabs = []string{tab}
	f.tabs[tab] = []string{"foreign"}
	for i := 0; i < 3; i++ {
		if err := cleanupOverflowTabs(r); err != nil {
			t.Fatalf("unexpected warning: %v", err)
		}
	}
	if !slices.Contains(r.OverflowTabs, tab) || len(f.tabs[tab]) != 1 || slices.Contains(f.closed, tab) {
		t.Fatal("unowned tab was not preserved")
	}
}

func TestOverflowCleanupQuietCloseFailure(t *testing.T) {
	r, f := overflowFixture(t)
	tab := "quiet-close-failure"
	overflowCloseWarnings.Delete(tab)
	t.Cleanup(func() { overflowCloseWarnings.Delete(tab) })
	r.OverflowTabs = []string{tab}
	f.tabs[tab] = nil
	original := herdrRun
	attempts := 0
	herdrRun = func(args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "tab" && args[1] == "close" && args[2] == tab {
			attempts++
			return nil, fmt.Errorf("temporary tab-close failure")
		}
		return original(args...)
	}
	warnings := 0
	for i := 0; i < 3; i++ {
		if err := cleanupOverflowTabs(r); err != nil {
			warnings++
			if !strings.Contains(err.Error(), "temporary tab-close failure") {
				t.Fatal(err)
			}
		}
	}
	if warnings != 3 || attempts != 3 || !slices.Contains(r.OverflowTabs, tab) {
		t.Fatalf("warnings=%d attempts=%d tabs=%v", warnings, attempts, r.OverflowTabs)
	}
	herdrRun = original
	if err := cleanupOverflowTabs(r); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.OverflowTabs, tab) || !slices.Contains(f.closed, tab) {
		t.Fatal("retry did not close empty tab")
	}
}
