package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
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

func TestReviewMissingOverflowTab(t *testing.T) {
	for _, tc := range []struct {
		name, state   string
		head, wantErr bool
	}{{"gone", "finished", false, false}, {"live", "live", false, true}, {"head", "finished", true, true}, {"get error", "error", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			appTest(t)
			hcRPC = func(string, any) (json.RawMessage, error) {
				return json.RawMessage(`{"version":1,"value":{"spaces":[{"id":"s","tabs":[]}]}}`), nil
			}
			hcRun = func(args ...string) ([]byte, error) {
				if tc.state == "error" {
					return nil, errors.New("unavailable")
				}
				return []byte(`{"state":"` + tc.state + `"}`), nil
			}
			head := ""
			if tc.head {
				head = "th"
			}
			err := normalizeAppTab("s", "missing", head, "", []string{"tw"}, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestReviewTrustBeforeStatus(t *testing.T) {
	appTest(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	keys := []string{}
	hcRun = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "get":
			if len(keys) > 0 {
				return []byte(`{"state":"live","status":{"state":"idle"}}`), nil
			}
			return []byte(`{"state":"live","status":null}`), nil
		case "read":
			if len(keys) == 0 {
				return []byte("Do you trust the files in this folder?"), nil
			}
			return []byte("ready"), nil
		case "keys":
			keys = append(keys, args[2:]...)
			return nil, nil
		}
		return nil, nil
	}
	if err := appWaitReady("tw", cwd); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "enter" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestReviewHCRunAdditionalForms(t *testing.T) {
	for _, cmd := range []string{"hc run --cwd=/tmp claude", "hc run --parent codex echo ok", "hc run --timeout codex echo ok", "hc run -- /usr/bin/env K=V codex", "hc run --bg -- bash -c 'codex --help'", "hc run --on box claude"} {
		raw, _ := json.Marshal(map[string]string{"command": cmd})
		blocked := decide(hookInput{ToolName: "Bash", ToolInput: raw}, "", true) != ""
		want := !strings.Contains(cmd, " echo ok")
		if blocked != want {
			t.Errorf("%q blocked=%v want %v", cmd, blocked, want)
		}
	}
}

func TestReviewNormalizeVanishedOverflow(t *testing.T) {
	appTest(t)
	appRun = func(...string) ([]byte, error) { return appLS(`{"terminal":"th","pane":"ph"}`), nil }
	hcRun = func(...string) ([]byte, error) { return nil, errHCNotFound }
	hcRPC = func(method string, params any) (json.RawMessage, error) {
		if method == "doc.set" {
			return nil, errors.New("unexpected document write")
		}
		return json.RawMessage(`{"version":1,"value":{"spaces":[{"id":"s","tabs":[{"id":"tab","root":{"type":"row","id":"tab","children":[{"type":"block","id":"ph","content":{"terminal":"th"}}]}}]}]}}`), nil
	}
	r := &Run{ID: "r1", Backend: "app", HeadPane: "th", Workspace: "s", Tab: "tab", Status: "active", OverflowTabs: []string{"gone"}, Workers: []*Worker{{Name: "w", Terminal: "tw", Pane: "pw", Tab: "gone", Status: "working", Started: time.Now()}}}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	if err := normalizeAppLayouts("th"); err != nil {
		t.Fatal(err)
	}
}
