package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
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

func TestSkillEntryReferences(t *testing.T) {
	b, err := os.ReadFile("skill/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(b, []byte("\n")) > 120 {
		t.Fatal("entry exceeds 120 lines")
	}
	for _, file := range []string{"skill/SKILL.md", "skill/references/advanced.md"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`\]\(([^)]+\.md)(?:#[^)]*)?\)`)
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			if _, err := os.Stat(filepath.Join(filepath.Dir(file), m[1])); err != nil {
				t.Errorf("%s broken link %s", file, m[1])
			}
		}
	}
	for _, file := range []string{"docs/setup.md", "skill/SKILL.md", "skill/references/design.md", "skill/references/advanced.md", "constitution/orch-block.md"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("| Key | CLI | Family | Tier |")) {
			t.Errorf("duplicate full catalog in %s", file)
		}
	}
}

func TestOperationsMirror(t *testing.T) {
	canonical, e := os.ReadFile("docs/operations.md")
	if e != nil {
		t.Fatal(e)
	}
	mirror, e := os.ReadFile("skill/references/operations.md")
	if e != nil {
		t.Fatal(e)
	}
	expected := append([]byte("<!-- Generated mirror of docs/operations.md; update the canonical repository document first. -->\n"), canonical...)
	if !bytes.Equal(mirror, expected) {
		t.Fatal("skill operations mirror is stale")
	}
}

func TestDocsGenIgnoresInheritedPaneIdentity(t *testing.T) {
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "pX")
	t.Setenv("HC_TERMINAL_ID", "tX")
	root := t.TempDir()
	for _, file := range generatedModelDocs {
		path := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(modelsBegin+"\nstale\n"+modelsEnd+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errs bytes.Buffer
	if code := run([]string{"docs", "gen", "--root", root}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatalf("code %d: %s", code, errs.String())
	}
	for _, file := range generatedModelDocs {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil || strings.Contains(string(data), "stale") {
			t.Fatalf("%s: %s %v", file, data, err)
		}
	}
}

func TestCommandNeedsHeadIdentity(t *testing.T) {
	for _, name := range []string{"docs", "models", "help", "playbook", "doctor", "measure"} {
		if commandNeedsHeadIdentity(name) {
			t.Errorf("utility %s requires identity", name)
		}
	}
	for _, name := range []string{"spawn", "close", "run", "goal", "do", "ask", "install", "uninstall", "unknown"} {
		if !commandNeedsHeadIdentity(name) {
			t.Errorf("%s bypasses identity predicate", name)
		}
	}
}
