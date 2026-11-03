package main

import (
	"reflect"
	"testing"
)

func TestCLIAdapterGolden(t *testing.T) {
	cleanModelEnv(t)
	for _, m := range catalog {
		t.Setenv(modelVar(m), "fixture-"+m.Key)
	}
	cases := []struct {
		key, mode string
		noDaemon  bool
		want      []string
	}{
		{"codex-low-h", "interactive", false, []string{"--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--model", "<model>", "-c", "model_reasoning_effort=\"high\""}},
		{"codex-low-h", "ask", false, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"high\"", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-low-h", "do", false, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"high\"", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-low-h", "interactive", true, []string{"--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--no-daemon", "--model", "<model>", "-c", "model_reasoning_effort=\"high\""}},
		{"codex-low-h", "ask", true, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"high\"", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-low-h", "do", true, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"high\"", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-medium-m", "interactive", false, []string{"--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\""}},
		{"codex-medium-m", "ask", false, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\"", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-medium-m", "do", false, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\"", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-medium-m", "interactive", true, []string{"--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--no-daemon", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\""}},
		{"codex-medium-m", "ask", true, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\"", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-medium-m", "do", true, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\"", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-high-l", "interactive", false, []string{"--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--model", "<model>", "-c", "model_reasoning_effort=\"low\""}},
		{"codex-high-l", "ask", false, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"low\"", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-high-l", "do", false, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"low\"", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-high-l", "interactive", true, []string{"--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--no-daemon", "--model", "<model>", "-c", "model_reasoning_effort=\"low\""}},
		{"codex-high-l", "ask", true, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"low\"", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-high-l", "do", true, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"low\"", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-high-m", "interactive", false, []string{"--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\""}},
		{"codex-high-m", "ask", false, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\"", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-high-m", "do", false, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\"", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-high-m", "interactive", true, []string{"--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--no-daemon", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\""}},
		{"codex-high-m", "ask", true, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\"", "--sandbox", "read-only", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"codex-high-m", "do", true, []string{"exec", "--model", "<model>", "-c", "model_reasoning_effort=\"medium\"", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--json", "-C", "/repo", "-o", "/answer", "hello"}},
		{"claude-high-m", "interactive", false, []string{"--dangerously-skip-permissions", "--model", "<model>", "--effort", "medium"}},
		{"claude-high-m", "ask", false, []string{"-p", "--model", "<model>", "--effort", "medium", "--tools", "Read,Grep,Glob,WebSearch,WebFetch", "--allowedTools", "Read,Grep,Glob,WebSearch,WebFetch", "--permission-mode", "dontAsk", "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", "hello"}},
		{"claude-high-m", "do", false, []string{"-p", "--model", "<model>", "--effort", "medium", "--permission-mode", "bypassPermissions", "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", "hello"}},
		{"claude-high-m", "interactive", true, []string{"--dangerously-skip-permissions", "--model", "<model>", "--effort", "medium"}},
		{"claude-high-m", "ask", true, []string{"-p", "--model", "<model>", "--effort", "medium", "--tools", "Read,Grep,Glob,WebSearch,WebFetch", "--allowedTools", "Read,Grep,Glob,WebSearch,WebFetch", "--permission-mode", "dontAsk", "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", "hello"}},
		{"claude-high-m", "do", true, []string{"-p", "--model", "<model>", "--effort", "medium", "--permission-mode", "bypassPermissions", "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", "hello"}},
		{"claude-medium-m", "interactive", false, []string{"--dangerously-skip-permissions", "--model", "<model>", "--effort", "medium"}},
		{"claude-medium-m", "ask", false, []string{"-p", "--model", "<model>", "--effort", "medium", "--tools", "Read,Grep,Glob,WebSearch,WebFetch", "--allowedTools", "Read,Grep,Glob,WebSearch,WebFetch", "--permission-mode", "dontAsk", "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", "hello"}},
		{"claude-medium-m", "do", false, []string{"-p", "--model", "<model>", "--effort", "medium", "--permission-mode", "bypassPermissions", "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", "hello"}},
		{"claude-medium-m", "interactive", true, []string{"--dangerously-skip-permissions", "--model", "<model>", "--effort", "medium"}},
		{"claude-medium-m", "ask", true, []string{"-p", "--model", "<model>", "--effort", "medium", "--tools", "Read,Grep,Glob,WebSearch,WebFetch", "--allowedTools", "Read,Grep,Glob,WebSearch,WebFetch", "--permission-mode", "dontAsk", "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", "hello"}},
		{"claude-medium-m", "do", true, []string{"-p", "--model", "<model>", "--effort", "medium", "--permission-mode", "bypassPermissions", "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", "hello"}},
		{"gemini-medium-m", "interactive", false, []string{"--dangerously-skip-permissions", "--model", "<model>"}},
		{"gemini-medium-m", "ask", false, []string{"--model", "<model>", "--mode", "plan", "--output-format", "json", "-p", "hello"}},
		{"gemini-medium-m", "do", false, []string{"--model", "<model>", "--dangerously-skip-permissions", "--output-format", "json", "-p", "hello"}},
		{"gemini-medium-m", "interactive", true, []string{"--dangerously-skip-permissions", "--model", "<model>"}},
		{"gemini-medium-m", "ask", true, []string{"--model", "<model>", "--mode", "plan", "--output-format", "json", "-p", "hello"}},
		{"gemini-medium-m", "do", true, []string{"--model", "<model>", "--dangerously-skip-permissions", "--output-format", "json", "-p", "hello"}},
	}
	for _, c := range cases {
		t.Run(c.key+"/"+c.mode+fmtBool(c.noDaemon), func(t *testing.T) {
			want := append([]string(nil), c.want...)
			for i, arg := range want {
				if arg == "<model>" {
					want[i] = "fixture-" + c.key
				}
			}
			m, err := modelByKey(c.key)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			if c.mode == "interactive" {
				got, err = launchArgs(m, c.noDaemon)
			} else {
				got, err = taskArgs(m, c.mode, "/repo", "/answer", "hello")
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("argv = %#v, %v; want %#v", got, err, want)
			}
		})
	}
}
func fmtBool(b bool) string {
	if b {
		return "/no-daemon"
	}
	return "/daemon"
}
func cleanModelEnv(t *testing.T) {
	t.Helper()
	for _, m := range catalog {
		t.Setenv(modelVar(m), "")
	}
	t.Setenv("CODEX_MEDIUM_H", "")
	t.Setenv("ORCH_MODELS_FILE", t.TempDir()+"/missing.yaml")
}
