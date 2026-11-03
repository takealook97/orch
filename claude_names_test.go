package main

import (
	"strings"
	"testing"
)

func TestClaudeModelArgs(t *testing.T) {
	for _, c := range []struct{ in, id, effort string }{
		{"opus-5.5-medium", "claude-opus-5-5", "medium"},
		{"sonnet-5.5-high", "claude-sonnet-5-5", "high"},
		{"opus-5", "claude-opus-5", ""},
		{"fable-5.1-max", "claude-fable-5-1", "max"},
		{"claude-opus-5-5", "claude-opus-5-5", ""},
		{"opus", "opus", ""},
		{"opus-5.5-ultra", "opus-5.5-ultra", ""},
	} {
		id, effort := claudeModelArgs(c.in)
		if id != c.id || effort != c.effort {
			t.Errorf("%s: got %q %q, want %q %q", c.in, id, effort, c.id, c.effort)
		}
	}
}

func TestClaudeTaskArgv(t *testing.T) {
	m, _ := modelByKey("claude-medium-m")
	t.Setenv(modelVar(m), "")
	a, err := claudeAdapter{}.task(m, "do", "/tmp", "", "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a, " "); !strings.HasPrefix(got, "-p --model claude-sonnet-5-5 --effort medium --permission-mode") {
		t.Fatalf("got %q", got)
	}
}

func TestManifestRejectsClaudeEffortConflict(t *testing.T) {
	s := strings.Replace(string(embeddedModels), "    model: sonnet\n    effort: \"\"", "    model: sonnet\n    effort: \"medium\"", 1)
	if s == string(embeddedModels) {
		t.Fatal("fixture did not change")
	}
	if _, err := loadManifest([]byte(s)); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("got %v", err)
	}
}

func TestClaudeEnvOverrideKeepsDefaultEffort(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"opus", "--model opus --effort medium"},
		{"claude-opus-5-5", "--model claude-opus-5-5 --effort medium"},
		{"opus-5.5-high", "--model claude-opus-5-5 --effort high"},
		{"opus-5.5", "--model claude-opus-5-5 --effort medium"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("CLAUDE_HIGH_M", tc.env)
			m, err := modelByKey("claude-high-m")
			if err != nil {
				t.Fatal(err)
			}
			args, err := modelArgs(m)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(args, " "); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
