package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestReviewForeignIdentity(t *testing.T) {
	appTest(t)
	writes := 0
	hcRun = func(args ...string) ([]byte, error) {
		if args[0] == "get" {
			return []byte(`{"state":"live","labels":{"orch.run":"other","orch.worker":"w"}}`), nil
		}
		writes++
		return nil, nil
	}
	root := layoutNode(`{"type":"row","children":[` + appHeadBlock + `,` + appWorkerOne + `]}`)
	if _, err := canonicalAppRoot(root, "tab", "th", nil, map[string]appIdentity{"t1": {"run", "w"}}); err == nil || !strings.Contains(err.Error(), "I6") {
		t.Fatalf("layout: %v", err)
	}
	if err := appClose("t1", appIdentity{"run", "w"}); err == nil || !strings.Contains(err.Error(), "I6") {
		t.Fatalf("close: %v", err)
	}
	if writes != 0 {
		t.Fatalf("writes: %d", writes)
	}
}
func TestReviewPermissionNotTrust(t *testing.T) {
	appTest(t)
	cwd, _ := os.Getwd()
	sent := false
	hcRun = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "get":
			return []byte(`{"state":"live","status":{"state":"blocked","kind":"permission"}}`), nil
		case "read":
			return []byte("Allow Bash: rm -rf build?"), nil
		case "keys":
			sent = true
		}
		return nil, nil
	}
	if appTrust("tw", cwd) || sent {
		t.Fatal("approved non-trust permission")
	}
}
func TestReviewAgentRunForms(t *testing.T) {
	for _, cmd := range []string{"hc run --keep claude", "hc run --on mac2 codex", "hc run -- sh -c 'claude --dangerously-skip-permissions'", "hc run -- /bin/sh -c codex", "hc run --bg -- npx @openai/codex", "hc run -- bunx @anthropic-ai/claude-code"} {
		raw, _ := json.Marshal(map[string]string{"command": cmd})
		in := hookInput{ToolName: "Bash", ToolInput: raw}
		if decide(in, "", true) == "" || decide(in, "worker", true) == "" {
			t.Errorf("allowed %s", cmd)
		}
	}
	for _, cmd := range []string{"hc run -- make test", "hc run -- npm run build", "hc run --help", "grep 'hc run claude' file"} {
		raw, _ := json.Marshal(map[string]string{"command": cmd})
		if decide(hookInput{ToolName: "Bash", ToolInput: raw}, "", true) != "" {
			t.Errorf("blocked %s", cmd)
		}
	}
}
func TestReviewCloseMalformedUnrelatedTab(t *testing.T) {
	appTest(t)
	hcRun = func(args ...string) ([]byte, error) { return nil, errHCNotFound }
	appRun = func(args ...string) ([]byte, error) { return appLS(`{"terminal":"th","pane":"ph"}`), nil }
	hcRPC = func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"value":{"spaces":[{"tabs":[{"root":{"type":"block","id":"ph","content":{"terminal":"th"}}}]},{"tabs":[{"id":"empty"}]}]}}`), nil
	}
	if err := appClose("tw", appIdentity{"run", "worker"}); err != nil {
		t.Fatal(err)
	}
}
func TestReviewRemoveFinishedOnlyOwned(t *testing.T) {
	appTest(t)
	removed := 0
	labels := `{"orch.run":"r","orch.worker":"w"}`
	hcRun = func(args ...string) ([]byte, error) {
		if args[0] == "get" {
			return []byte(`{"state":"finished","labels":` + labels + `}`), nil
		}
		if args[0] == "rm" {
			removed++
			return nil, nil
		}
		return nil, errors.New("unexpected")
	}
	appRemoveFinished("t", appIdentity{"r", "w"})
	labels = `{}`
	appRemoveFinished("t", appIdentity{"r", "w"})
	if removed != 1 {
		t.Fatalf("removed %d", removed)
	}
}
