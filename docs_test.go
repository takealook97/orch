package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestMarkdown(t *testing.T) {
	cleanModelEnv(t)
	t.Setenv("CODEX_HIGH_L", "user-model")
	var out, errs bytes.Buffer
	if code := run([]string{"models", "--markdown"}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatalf("code %d: %s", code, errs.String())
	}
	got := out.String()
	if !strings.Contains(got, "| Key |") || !strings.Contains(got, "`"+embeddedManifest.Models["sol"]+"`") || strings.Contains(got, "user-model") {
		t.Fatalf("non-deterministic markdown: %s", got)
	}
}
func TestManifestDocsGeneration(t *testing.T) {
	root := t.TempDir()
	for _, file := range generatedModelDocs {
		p := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("before\n"+modelsBegin+"\nstale\n"+modelsEnd+"\nafter\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := generateModelDocs(root); err != nil {
		t.Fatal(err)
	}
	for _, file := range generatedModelDocs {
		p := filepath.Join(root, file)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(b), "before\n") || !strings.HasSuffix(string(b), "\nafter\n") || strings.Contains(string(b), "stale") {
			t.Fatalf("bad marker replacement: %s", b)
		}
		if file == "constitution/orch-block.md" && strings.Count(string(b), "\n") != 5 {
			t.Fatalf("constitution is not one generated line: %s", b)
		}
	}
	if err := generateModelDocs(root); err != nil {
		t.Fatal(err)
	}
	// Missing or duplicated markers must be reported, never silently ignored.
	p := filepath.Join(root, generatedModelDocs[0])
	if err := os.WriteFile(p, []byte("unmarked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generateModelDocs(root); err == nil {
		t.Fatal("accepted unmarked document")
	}
}
