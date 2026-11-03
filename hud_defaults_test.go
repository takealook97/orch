package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHUDDefaultAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CR_HOME", filepath.Join(home, "cr"))
	bin := filepath.Join(home, ".local", "bin")
	t.Setenv("PATH", bin)
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"codex-person@example.com","secret":"never-output"}`)) + ".signature"
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"tokens":{"id_token":"`+token+`"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"claude-person@example.com"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	accounts, err := hudDiscoverSelected(time.Now())
	if err != nil || len(accounts) != 2 {
		t.Fatalf("%+v, %v", accounts, err)
	}
	if a := accounts[0]; a.Provider != "codex" || a.Home != codexHome || a.Label != "codex-person" || !a.Selected {
		t.Fatalf("codex: %+v", a)
	}
	if a := accounts[1]; a.Provider != "claude" || a.Home != "" || a.Label != "claude-person" || !a.Selected {
		t.Fatalf("claude: %+v", a)
	}
	if err := os.Remove(filepath.Join(codexHome, "auth.json")); err != nil {
		t.Fatal(err)
	}
	accounts = hudDefaultAccounts()
	if len(accounts) != 1 || accounts[0].Provider != "claude" {
		t.Fatalf("missing auth: %+v", accounts)
	}
	if err := os.MkdirAll(os.Getenv("CR_HOME"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(os.Getenv("CR_HOME"), "registry.json")
	if err := os.WriteFile(registry, []byte(`{"accounts":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	accounts, err = hudDiscoverSelected(time.Now())
	if err != nil || len(accounts) != 0 {
		t.Fatalf("empty registry must not fall back: %+v %v", accounts, err)
	}
	if err := os.WriteFile(registry, []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := hudDiscoverSelected(time.Now()); err == nil {
		t.Fatal("malformed registry silently fell back")
	}
}

func TestHUDDefaultLabelsUnknown(t *testing.T) {
	home := t.TempDir()
	for _, raw := range []string{`{}`, `{"tokens":{"id_token":"broken"}}`, `{"tokens":{"id_token":"h.e30.s"}}`, `{"tokens":{"id_token":"h.!!!.s"}}`} {
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if got := hudDefaultCodexLabel(home); got != "" {
			t.Fatalf("unexpected label %q", got)
		}
	}
	if got := hudDefaultClaudeLabel(home); got != "" {
		t.Fatalf("unexpected label %q", got)
	}
}
