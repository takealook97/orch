package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHUDCRPick(t *testing.T) {
	now := time.Now()
	ptr := func(n float64) *float64 { return &n }
	base := func() hudRegistry {
		return hudRegistry{Priority: "capacity", Accounts: []hudRegistryAccount{
			{Provider: "codex", Email: "a@example.com", State: "ready", Plan: "plus", FiveHour: hudRegistryWindow{Remaining: ptr(60)}, Weekly: hudRegistryWindow{Remaining: ptr(20)}},
			{Provider: "codex", Email: "b@example.com", State: "ready", Plan: "pro", FiveHour: hudRegistryWindow{Remaining: ptr(40)}, Weekly: hudRegistryWindow{Remaining: ptr(80)}},
		}}
	}
	tests := []struct {
		name   string
		change func(*hudRegistry)
		want   string
	}{
		{"headroom", func(r *hudRegistry) {}, "b@example.com"},
		{"custom rank", func(r *hudRegistry) { r.Priority = "custom"; r.Accounts[0].Rank = 1; r.Accounts[1].Rank = 2 }, "a@example.com"},
		{"missing rank last", func(r *hudRegistry) { r.Priority = "custom"; r.Accounts[1].Rank = 2 }, "b@example.com"},
		{"custom email tie", func(r *hudRegistry) { r.Priority = "custom"; r.Accounts[0].Rank = 1; r.Accounts[1].Rank = 1 }, "a@example.com"},
		{"weekly exhausted", func(r *hudRegistry) { r.Accounts[1].Weekly.Remaining = ptr(0) }, "a@example.com"},
		{"five hour exhausted", func(r *hudRegistry) { r.Accounts[1].FiveHour.Remaining = ptr(-1) }, "a@example.com"},
		{"elapsed reset stays exhausted", func(r *hudRegistry) {
			r.Accounts[1].Weekly = hudRegistryWindow{Remaining: ptr(0), ResetsAt: now.Add(-time.Hour)}
		}, "a@example.com"},
		{"ignored still launchable", func(r *hudRegistry) {
			r.Accounts[1].Email = "b@example.com"
			r.Ignored = []string{"codex:b@example.com"}
		}, "b@example.com"},
		{"unavailable", func(r *hudRegistry) { r.Accounts[1].State = "error" }, "a@example.com"},
		{"none usable", func(r *hudRegistry) { r.Accounts[0].State = ""; r.Accounts[1].State = "error" }, ""},
		{"least capacity", func(r *hudRegistry) { r.Capacity = "least" }, "a@example.com"},
		{"neutral email", func(r *hudRegistry) { r.Capacity = "neutral" }, "a@example.com"},
		{"weekly tie five hour", func(r *hudRegistry) { r.Accounts[0].Weekly.Remaining = ptr(80) }, "a@example.com"},
		{"known weekly before unknown", func(r *hudRegistry) { r.Accounts[1].Weekly.Remaining = nil }, "a@example.com"},
		{"known five hour before unknown", func(r *hudRegistry) { r.Accounts[0].Weekly.Remaining = ptr(80); r.Accounts[0].FiveHour.Remaining = nil }, "b@example.com"},
		{"default plan high", func(r *hudRegistry) { r.Priority = "" }, "b@example.com"},
		{"plan low", func(r *hudRegistry) { r.Priority = "plan-low" }, "a@example.com"},
		{"plan override", func(r *hudRegistry) { r.Priority = "plan-high"; r.Accounts[0].PlanOverride = "pro 20x" }, "a@example.com"},
		{"plan weights", func(r *hudRegistry) { r.Priority = "plan-high"; r.PlanWeights = map[string]int{"codex:plus": 9} }, "a@example.com"},
		{"provider isolation", func(r *hudRegistry) { r.Accounts[1].Provider = "claude" }, "a@example.com"},
		{"unknown quotas usable", func(r *hudRegistry) {
			r.Accounts[0].State = "error"
			r.Accounts[1].Weekly.Remaining = nil
			r.Accounts[1].FiveHour.Remaining = nil
		}, "b@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base()
			tt.change(&r)
			a, ok := hudCRPick(r, "codex", now)
			if ok != (tt.want != "") || a.Email != tt.want {
				t.Fatalf("got %q, %v; want %q", a.Email, ok, tt.want)
			}
		})
	}
}

func TestHUDSelectedCLI(t *testing.T) {
	t.Setenv("CR_HOME", t.TempDir())
	t.Setenv("ORCH_HOME", t.TempDir())
	old := hudFindGemini
	hudFindGemini = func() (string, error) { return "agy", nil }
	defer func() { hudFindGemini = old }()
	registry := `{"priority":"custom","accounts":[{"provider":"claude","email":"claude@example.com","home":"/fake/claude","state":"ready"},{"provider":"codex","email":"codex@example.com","home":"/fake/codex","state":"ready"}]}`
	path := filepath.Join(os.Getenv("CR_HOME"), "registry.json")
	if err := os.WriteFile(path, []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	cache := map[string]hudCacheEntry{}
	for _, a := range []HUDAccount{{Provider: "codex", Home: "/fake/codex"}, {Provider: "claude", Home: "/fake/claude"}, {Provider: "gemini"}} {
		cache[hudAccountKey(a)] = hudCacheEntry{Account: a, FetchedAt: time.Now()}
	}
	if err := hudWriteCache(cache); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := cmdHud([]string{"usage", "--selected"}, &out, &errout); code != 0 {
		t.Fatalf("%d: %s", code, &errout)
	}
	var result HUDUsage
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 3 {
		t.Fatalf("accounts: %+v", result)
	}
	for i, p := range []string{"codex", "claude", "gemini"} {
		if a := result.Accounts[i]; a.Provider != p || !a.Selected {
			t.Fatalf("account %d: %+v", i, a)
		}
	}
	// Selection is re-read even while the usage cache remains fresh.
	if err := os.WriteFile(path, []byte(`{"accounts":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdHud([]string{"usage", "--selected"}, &out, &errout); code != 0 {
		t.Fatal(code)
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Provider != "gemini" {
		t.Fatalf("registry not re-read: %+v", result)
	}
	for _, args := range [][]string{{"usage", "--selected", "--terminals="}, {"usage", "--terminals=t1", "--selected"}} {
		out.Reset()
		errout.Reset()
		if code := cmdHud(args, &out, &errout); code != 2 || out.Len() != 0 || errout.Len() == 0 {
			t.Fatalf("conflicting args: %d %s %s", code, &out, &errout)
		}
	}
}
