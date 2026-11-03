package main

import (
	"os"
	"sync"
)

func backend() string {
	if v := os.Getenv("ORCH_MUX"); v != "" {
		if v == "herdr" || v == "app" {
			return v
		}
		return ""
	}
	if os.Getenv("HERDR_ENV") == "1" {
		return "herdr"
	}
	if os.Getenv("HC_TERMINAL_ID") != "" {
		return "app"
	}
	return ""
}
func inHerdr() bool { return backend() != "" }
func headID() string {
	if backend() == "app" {
		return os.Getenv("HC_TERMINAL_ID")
	}
	return os.Getenv("HERDR_PANE_ID")
}

var headLocation struct {
	sync.Mutex
	terminal, space, tab string
}

func appHeadLocation() (string, string) {
	headLocation.Lock()
	defer headLocation.Unlock()
	id := headID()
	if id != "" && headLocation.terminal == id {
		return headLocation.space, headLocation.tab
	}
	ls, err := appList()
	if err != nil {
		return "", ""
	}
	space, tab := findAppTerminalTab(ls, id)
	if tab != "" {
		headLocation.terminal, headLocation.space, headLocation.tab = id, space, tab
	}
	return space, tab
}

// findAppTerminalTab returns the space and tab that hold terminal id, or empty strings.
func findAppTerminalTab(ls appInventory, id string) (string, string) {
	for _, s := range ls.Spaces {
		for _, t := range s.Tabs {
			for _, p := range t.Terminals {
				if p.Terminal == id {
					return s.ID, t.ID
				}
			}
		}
	}
	return "", ""
}

// setAppHeadLocation replaces the cached Head location after an uncached lookup.
func setAppHeadLocation(id, space, tab string) {
	headLocation.Lock()
	defer headLocation.Unlock()
	headLocation.terminal, headLocation.space, headLocation.tab = id, space, tab
}
func headSpace() string {
	if backend() == "app" {
		s, _ := appHeadLocation()
		return s
	}
	return os.Getenv("HERDR_WORKSPACE_ID")
}
func headTab() string {
	if backend() == "app" {
		_, t := appHeadLocation()
		return t
	}
	return os.Getenv("HERDR_TAB_ID")
}

// currentBackend is the backend this process acts on; outside any Herdr it is the legacy one.
func currentBackend() string {
	if b := backend(); b != "" {
		return b
	}
	return "herdr"
}

func runBackend(r *Run) string {
	if r.Backend == "" {
		return "herdr"
	}
	return r.Backend
}
