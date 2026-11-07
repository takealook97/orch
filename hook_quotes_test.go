package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestBashHookCommandAndQuotedData(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_HEAD_GUARD", "1")
	t.Setenv("HERDR_ENV", "1")
	cases := []struct {
		name, command, description string
		deny                       bool
	}{
		{"spawn metadata", `orch spawn --model codex-high-m --name impl-hold-cadence --role produce --no-worktree --cwd /x`, "Core 수정 worker pane 생성(재시도)", false},
		{"goal metadata", `orch goal end`, "기준 번호가 어긋난 구 goal 종료(새 goal로 교체 예정)", false},
		{"ask prose", `orch ask --model codex-high-m --fresh "Read ... (e.g. FORCE_REDEPLOY parameter, 'Build Now') ..."`, "", false},
		{"test sql", `orch test "printf \"select count(*) filter (where x) from t;\n\" | sh q.sh"`, "", false},
		{"python policy", `hc ls --json | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d))"`, "", true},
		{"single quoted syntax", "echo '( ) { } ; | & # $(date) `date`'", "", false},
		{"double quoted syntax", `echo "( ) { } ; | & #"`, "", false},
		{"escaped syntax", `echo \( \) \{ \} \; \| \& \# \$\(date\)`, "", false},
		{"escaped dollar", `echo "\$(date)"`, "", false},
		{"escaped backtick", "echo \"\\`date\\`\"", "", false},
		{"escaped quote", `echo "\" ( ) { } ; | & #"`, "", false},
		{"escaped backslash", `echo "\\ ( ) { } ; | & #"`, "", false},
		{"metadata execution", `git status`, "make check; orch spawn worker", false},
		{"subshell", `(cd x; ls)`, "", true},
		{"group", `{ echo x; }`, "", true},
		{"function", `f() { :; }`, "", true},
		{"substitution", `echo $(date)`, "", true},
		{"quoted substitution", `echo "$(date)"`, "", true},
		{"backslash before substitution", `echo "\\$(date)"`, "", true},
		{"backticks", "echo `date`", "", true},
		{"quoted backticks", "echo \"`date`\"", "", true},
		{"heredoc", "cat <<EOF\nx\nEOF", "", true},
		{"loop", `for i in a; do :; done`, "", true},
		{"post quote execution", `echo "(data)"; make check`, "", true},
		{"post escape execution", `echo \(; make check`, "", true},
		{"unclosed quote", `echo "(data)`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{
				"tool_name":  "Bash",
				"tool_input": map[string]any{"command": tc.command, "description": tc.description, "timeout": 30000},
			})
			if err != nil {
				t.Fatal(err)
			}
			var out, errout bytes.Buffer
			if code := runHook("claude", bytes.NewReader(input), &out, &errout); code != 0 {
				t.Fatalf("hook exit %d: %s", code, &errout)
			}
			if denied := strings.Contains(out.String(), `"permissionDecision":"deny"`); denied != tc.deny {
				t.Fatalf("deny=%v want=%v: %s %s", denied, tc.deny, &out, &errout)
			}
			if tc.name == "python policy" && strings.Contains(out.String(), "Unsupported shell syntax") {
				t.Fatalf("python execution should be denied by policy, not parsing: %s", &out)
			}
		})
	}
}

func TestHeadScannerQuotedArgv(t *testing.T) {
	for _, command := range []string{
		`echo '( ) { } ; | & #'`,
		`echo "( ) { } ; | & #"`,
		`echo \(\ \)\ \{\ \}\ \;\ \|\ \&\ \#`,
	} {
		commands, err := scanHeadCommands(command)
		if err != nil || len(commands) != 1 || len(commands[0].argv) != 2 || commands[0].argv[1] != "( ) { } ; | & #" {
			t.Fatalf("%s: commands=%+v err=%v", command, commands, err)
		}
	}
}

func TestBashCommandTextShapes(t *testing.T) {
	for _, tool := range []string{"Bash", "bash", "tools.Bash", "mcp__shell__Bash"} {
		args := map[string]any{"command": "git status", "description": "(metadata)", "other": []string{"make check"}}
		in := hookJSON(tool, args)
		if got := in.commandText(); got != "git status" {
			t.Fatalf("%s: %q", tool, got)
		}
		encoded, _ := json.Marshal(args)
		in = hookJSON(tool, string(encoded))
		if got := in.commandText(); got != "git status" {
			t.Fatalf("encoded %s: %q", tool, got)
		}
		if got := hookJSON(tool, map[string]string{"description": "(metadata)"}).commandText(); got != "" {
			t.Fatalf("missing command %s: %q", tool, got)
		}
	}
	var in hookInput
	if err := json.Unmarshal([]byte(`{"toolCall":{"name":"Bash","args":{"command":"git status","description":"(metadata)"}}}`), &in); err != nil {
		t.Fatal(err)
	}
	if got := in.commandText(); got != "git status" {
		t.Fatal(got)
	}
	for _, tool := range []string{"exec_command", "run_command"} {
		if got := hookJSON(tool, map[string]string{"cmd": "git status"}).commandText(); got != "git status" {
			t.Fatalf("other tool changed %s: %q", tool, got)
		}
	}
}
