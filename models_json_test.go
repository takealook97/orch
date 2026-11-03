package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelsJSON(t *testing.T) {
	t.Setenv("ORCH_MODELS_FILE", t.TempDir()+"/missing")
	for _, m := range catalog {
		t.Setenv(modelVar(m), "")
	}
	t.Setenv("CODEX_MEDIUM_H", "")
	t.Setenv("CLAUDE_HIGH_M", "opus")
	var out, stderr bytes.Buffer
	if code := run([]string{"models", "--json"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var entries []map[string]string
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(catalog) {
		t.Fatalf("got %d entries", len(entries))
	}
	for i, entry := range entries {
		if len(entry) != 7 || entry["key"] != catalog[i].Key || entry["kind"] != catalog[i].Kind || entry["family"] != catalog[i].Family || entry["tier"] != catalog[i].Tier {
			t.Fatalf("entry %d: %#v", i, entry)
		}
		if entry["key"] == "claude-high-m" && (entry["model"] != "opus" || entry["effort"] != "medium" || entry["display"] != "opus") {
			t.Fatalf("override: %#v", entry)
		}
		if entry["key"] == "claude-medium-m" && (entry["model"] != "claude-sonnet-5-5" || entry["effort"] != "medium" || entry["display"] != "sonnet-5.5") {
			t.Fatalf("claude: %#v", entry)
		}
	}
}

func TestModelDisplay(t *testing.T) {
	for id, want := range map[string]string{"claude-opus-5-5": "opus-5.5", "gpt-6.1-sol": "gpt-6.1-sol", "opus": "opus"} {
		if got := modelDisplay(id); got != want {
			t.Errorf("%s: got %q, want %q", id, got, want)
		}
	}
}
