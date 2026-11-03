package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHUDModelDisplay(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5": "opus 5.5", "claude-sonnet-5-5": "sonnet 5.5",
		"claude-haiku-5-5-20261001": "haiku 5.5", "claude-fable-5-1": "fable 5.1",
		"gpt-6.1-sol": "gpt-6.1-sol", "opus": "opus", "claude-opus-4-20250514": "opus-4",
	} {
		if got := hudModelDisplay(id); got != want {
			t.Errorf("%s: got %q, want %q", id, got, want)
		}
	}
}

func TestHUDClaudeTranscriptModel(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/project_a.b 한"
	dir := filepath.Join(home, "projects", "-tmp-project-a-b--")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string, when time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	write("old.jsonl", "{\"model\":\"claude-haiku-5-5\"}\n", now.Add(-time.Hour))
	write("new.jsonl", "{\"model\":\"claude-sonnet-5-5\"}\n{\"message\":{\"model\":\"claude-opus-5-5\"}}\n{\"model\":\"claude-haiku", now)
	if got := hudClaudeTranscriptModel(home, cwd); got != "claude-opus-5-5" {
		t.Fatalf("newest/last/truncated: %q", got)
	}
	write("new.jsonl", "{\"model\":\"claude-too-old-1-0\"}\n"+strings.Repeat("x", 300*1024)+"\n{\"model\":\"claude-fable-5-1\"}\n", now)
	if got := hudClaudeTranscriptModel(home, cwd); got != "claude-fable-5-1" {
		t.Fatalf("tail: %q", got)
	}
	write("new.jsonl", "{\"model\":\"claude-too-old-1-0\"}\n"+strings.Repeat("x", 300*1024), now)
	if got := hudClaudeTranscriptModel(home, cwd); got != "" {
		t.Fatalf("must not read before tail: %q", got)
	}
	if got := hudClaudeTranscriptModel(home, "/missing"); got != "" {
		t.Fatalf("missing: %q", got)
	}
	if got := hudClaudeTranscriptModel(home, ""); got != "" {
		t.Fatalf("empty cwd: %q", got)
	}
}

func TestHUDClaudeLiveOverride(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{\"message\":{\"model\":\"claude-opus-5-5\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	scripts := map[string]string{
		"hc": "#!/bin/sh\nprintf '%s\\n' '{\"pid\":100,\"cwd\":\"/fixture\"}'\n",
		"ps": "#!/bin/sh\nif [ \"$1\" = \"-A\" ]; then\nprintf '%s\\n' '100 1 zsh' '101 100 claude'\nelse\nprintf '%s\\n' \"claude CLAUDE_CONFIG_DIR=$FIXTURE_HOME OTHER=value\"\nfi\n",
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("FIXTURE_HOME", home)
	out := HUDPanes{Terminals: []HUDTerminal{{Address: "t1", Program: "claude-code", Model: "sonnet"}}, Runs: []HUDRun{{Workers: []HUDWorker{{Terminal: "t1", Model: "opus", Effort: "high"}, {Terminal: "t2", Model: "catalog-fallback"}}}}}
	hudResolveClaudeModels(context.Background(), &out)
	if got := out.Terminals[0].Model; got != "opus 5.5" {
		t.Fatalf("terminal: %q", got)
	}
	if got := out.Runs[0].Workers[0]; got.Model != "opus 5.5" || got.Effort != "high" {
		t.Fatalf("worker: %+v", got)
	}
	if got := out.Runs[0].Workers[1].Model; got != "catalog-fallback" {
		t.Fatalf("other worker: %q", got)
	}
	hudApplyClaudeModel(&out, "t1", "")
	if out.Runs[0].Workers[0].Model != "opus 5.5" {
		t.Fatal("empty model replaced fallback")
	}

	// Only the first twelve Claude terminals are inspected.
	bounded := HUDPanes{}
	for i := 0; i < 13; i++ {
		bounded.Terminals = append(bounded.Terminals, HUDTerminal{Address: fmt.Sprintf("t%d", i), Program: "claude", Model: "sonnet"})
	}
	hudResolveClaudeModels(context.Background(), &bounded)
	for i, terminal := range bounded.Terminals {
		want := "opus 5.5"
		if i == 12 {
			want = "sonnet"
		}
		if terminal.Model != want {
			t.Fatalf("terminal %d: %q, want %q", i, terminal.Model, want)
		}
	}
	// Missing transcript keeps both argv and catalog values.
	t.Setenv("FIXTURE_HOME", t.TempDir())
	out.Terminals[0].Model = "sonnet"
	out.Runs[0].Workers[0].Model = "opus"
	hudResolveClaudeModels(context.Background(), &out)
	if out.Terminals[0].Model != "sonnet" || out.Runs[0].Workers[0].Model != "opus" {
		t.Fatal("missing transcript replaced fallback")
	}
}

func TestHUDClaudeResolutionDeadline(t *testing.T) {
	bin := t.TempDir()
	for name, script := range map[string]string{
		"ps": "#!/bin/sh\nprintf '%s\\n' '100 1 claude'\n",
		"hc": "#!/bin/sh\nexec /bin/sleep 10\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	out := HUDPanes{Terminals: []HUDTerminal{{Address: "t1", Program: "claude", Model: "opus"}}}
	start := time.Now()
	hudResolveClaudeModels(context.Background(), &out)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("deadline exceeded: %v", elapsed)
	}
	if out.Terminals[0].Model != "opus" {
		t.Fatal("deadline replaced fallback")
	}
}
