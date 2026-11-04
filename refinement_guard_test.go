package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefinementFullHooks(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("HERDR_ENV", "1")
	cases := []struct {
		cmd  string
		deny bool
	}{
		{`git --no-pager commit`, true}, {`git -P push`, true}, {`git --literal-pathspecs add f`, true}, {`git --bare gc`, true},
		{`git -c k=v -C d reset`, true}, {`FOO=1 git --no-pager rm x`, true}, {`git --git-dir .git --work-tree . push`, true},
		{`git --frobnicate commit`, true}, {`git --no-pager log`, false}, {`git -P diff`, false}, {`git --bare status`, false},
		{`git -C . -c k=v status`, true}, {`git --namespace=x --no-pager show`, false},
		{`or''ch do "run go test and git push"`, false}, {`rg "git push" .`, false}, {`grep -n 'sed -i' f`, false},
		{`g''it --no-pager pu''sh`, true}, {`env FOO=1 git -P push`, true}, {`env -u X git push`, true},
		{`orch do "safe"; git -P push`, true}, {`echo "$(git -P push)"`, true}, {`orch ask "$(make check)"`, true},
		{`P=$(git status)`, false}, {`bash -lc 'git --no-pager push'`, true}, {`bash -c 'git status'`, false},
		{"echo `git push`", true}, {`$CMD args`, true}, {`eval "$X"`, true}, {`cat <(git status)`, true}, {`echo 'unterminated`, true},
		{`echo '$(git push)'`, false}, {`echo 'go test; git push'`, false}, {`echo x 3>out`, true}, {`echo x >&out`, true},
		{`grep x 2>/dev/null 1>&2`, false}, {`awk '{print $1 > "x"}'`, true}, {`awk '{print $1}'`, false},
		{`sed 's/a/b/w out'`, true}, {`sed -n '1p'`, false}, {`find . -exec true {} \;`, true}, {`go "$SUB"`, true}, {`sed -f script`, true}, {`awk -f script`, true}, {`find . -fprint out`, true}, {`sed 'W out'`, true}, {`git branch`, false}, {`! git push`, true}, {`g?t --no-pager push`, true}, {`g{it,o} push`, true}, {`if git push; then echo ok; fi`, true},
	}
	cases = append(cases, []struct {
		cmd  string
		deny bool
	}{
		{`bash --norc -c 'git push'`, true}, {`bash --noprofile -c 'git push'`, true},
		{`bash --norc --noprofile -lc 'git status'`, false}, {`bash --unknown -c 'git status'`, true},
		{`bash -c "$(cat p.sh)"`, true}, {`builtin eval 'git push'`, true},
		{`sed -e'w out' f`, true}, {`sed --expression='w out' f`, true}, {`sed -e'1p' f`, false},
		{`sed -ne'w out' f`, true}, {`sed -nes/a/b/w f`, true}, {`sed -Ene'w out' f`, true},
		{`sed -ne '1p' f`, false}, {`sed -ne1p f`, false}, {`sed -E 's/a/b/g' f`, false},
		{`sed -nf script f`, true}, {`sed -ni '1p' f`, true}, {`sed -Ein '1p' f`, true},
		{`sed -nz '1p' f`, true}, {`sed --expression '1p' f`, false},
		{`git -c core.fsmonitor='touch x' status`, true}, {`git -ccore.fsmonitor=./x status`, true},
		{`git --config-env=core.fsmonitor=VALUE status`, true}, {`git --config-env core.fsmonitor=VALUE status`, true},
		{`git diff --output=out`, true}, {`git log --output out`, true}, {`git grep -Oless pattern`, true},
		{`git grep --open-files-in-pager=less pattern`, true},
	}...)
	patch := func(path, suffix string) string {
		return fmt.Sprintf("apply_patch <<'EOF'\n*** Begin Patch\n*** Add File: %s\n+git push is planning data\n*** End Patch\nEOF\n%s", path, suffix)
	}
	allowed := filepath.Join(runDir("run-1"), "contracts", "a.md")
	cases = append(cases, []struct {
		cmd  string
		deny bool
	}{
		{patch(allowed, ""), false},
		{strings.Replace(patch(allowed, ""), "<<'EOF'", `<<"EOF"`, 1), false},
		{strings.Replace(patch(allowed, ""), "<<'EOF'", "<<EOF", 1), true},
		{strings.Replace(patch(allowed, ""), "*** Add File:", "*** Update File:", 1), false},
		{strings.TrimSuffix(patch(allowed, ""), "EOF\n"), true},
		{strings.Replace(patch(allowed, ""), "<<'EOF'", "<<'EOF' >out", 1), true}, {patch(allowed, "git push"), true},
		{patch(filepath.Join(runDir("run-1"), "implementation.go"), ""), true},
		{strings.Replace(patch(allowed, ""), "<<'EOF'", "<<'EOF' && git push", 1), true},
	}...)
	for _, cli := range []string{"claude", "codex", "agy"} {
		for _, tc := range cases {
			t.Run(cli+"/"+tc.cmd, func(t *testing.T) {
				var payload any
				switch cli {
				case "claude":
					payload = map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": tc.cmd}}
				case "codex":
					payload = map[string]any{"tool_name": "exec_command", "tool_input": map[string]string{"cmd": tc.cmd}}
				case "agy":
					payload = map[string]any{"toolCall": map[string]any{"name": "run_command", "args": map[string]string{"CommandLine": tc.cmd}}}
				}
				raw, _ := json.Marshal(payload)
				var out, errout bytes.Buffer
				runHook(cli, bytes.NewReader(raw), &out, &errout)
				denied := strings.Contains(out.String(), `"deny"`) || strings.Contains(errout.String(), "Head execution")
				if denied != tc.deny {
					t.Fatalf("deny %v want %v: out=%s stderr=%s", denied, tc.deny, &out, &errout)
				}
			})
		}
	}
}
