package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHeadGitReadOnlyForms(t *testing.T) {
	allowed := []string{
		"remote", "remote -v", "remote --verbose", "remote show origin", "remote get-url origin",
		"branch", "branch -vv", "branch -a -v", "branch --list feat/*", "branch --contains HEAD",
		"branch --merged main", "branch --no-merged main", "branch --no-contains main", "branch --points-at HEAD",
		"branch --show-current", "branch -vvv --all --remotes --verbose --no-color --color --column --no-column",
		"branch --sort=-committerdate --format=%(refname) --list -- -pattern",
		"stash list", "stash show -p stash@{0}", "tag", "tag -l v*", "tag -n3",
		"tag --list --contains HEAD --no-contains main --merged main --no-merged main --points-at HEAD --sort=refname --format=%(refname) -n2",
		"tag -l -- -d", "reflog", "reflog show HEAD", "worktree list",
		"config --get user.name", "config -l", "config --global --get-regexp alias.*",
		"config --local --system --worktree --show-origin --show-scope --null -z --name-only --bool --int --bool-or-int --path --type=bool --get-all user.name",
		"config --get -- -key", "symbolic-ref HEAD", "symbolic-ref --quiet --short HEAD", "symbolic-ref -- -d",
		"submodule status", "merge-base main HEAD", "shortlog -sn", "ls-remote origin", "-C /tmp remote -v",
		"name-rev HEAD", "count-objects", "diff-tree HEAD", "diff-index HEAD", "diff-files", "cherry", "range-diff main HEAD", "whatchanged", "var GIT_EDITOR",
		"--no-pager log -1", "-C/tmp status", "--git-dir=/tmp/repo status",
	}
	blocked := []string{
		"remote add x url", "remote set-url origin url", "remote remove x", "remote rename x y", "remote set-head origin -a", "remote set-branches origin main", "remote prune origin", "remote update", "remote -v add x url",
		"branch newname", "branch -v newname", "branch -d x", "branch --set-upstream-to=origin/main", "branch -D x", "branch -m x", "branch -M x", "branch -c x", "branch -C x", "branch -f x", "branch -u origin/main", "branch --unset-upstream", "branch --edit-description", "branch --list --color=always", "branch -- --list",
		"stash", "stash push", "stash pop", "tag v1", "tag -a v1 -m x", "tag -d v1", "tag -l -d v1", "tag -l --contains=HEAD", "tag -n3 --force", "tag -n3=4",
		"reflog expire --all", "reflog delete HEAD", "reflog exists HEAD", "worktree", "worktree add ../x",
		"config user.name bob", "config --unset user.name", "config --edit", "config --file x -l", "config --file=x -l", "config --blob=HEAD:x -l", "config --get user.name bob", "config -l key", "config --get=user.name", "config --global", "config -l --global=yes", "config -- --get",
		"symbolic-ref HEAD refs/heads/x", "symbolic-ref -d HEAD", "symbolic-ref --delete HEAD", "symbolic-ref --delete=HEAD", "symbolic-ref", "symbolic-ref -- HEAD other",
		"submodule", "submodule update", "fetch", "pull", "push", "commit -m x", "unknown-alias",
		"-c alias.x=status x", "--config-env=x=y status", "--unknown status", "log --output=x", "log --output x", "diff -Oorder", "grep --open-files-in-pager=less x", "-C",
	}
	for _, group := range []struct {
		name     string
		commands []string
		executes bool
	}{
		{"allowed", allowed, false}, {"blocked", blocked, true},
	} {
		for _, command := range group.commands {
			t.Run(group.name+"/"+command, func(t *testing.T) {
				if got := headGitExecutes(strings.Fields(command)); got != group.executes {
					t.Fatalf("headGitExecutes(%q) = %v, want %v", command, got, group.executes)
				}
			})
		}
	}
}

func TestHeadGitOptionGuard(t *testing.T) {
	for _, tc := range []struct {
		command string
		blocked bool
	}{
		{"ls-remote origin", false},
		{"ls-remote --heads origin refs/*", false},
		{"ls-remote -h --tags -t --refs -q --quiet --symref --sort=refname --get-url --exit-code origin", false},
		{"ls-remote --upload-pack=/tmp/x origin", true},
		{"ls-remote --upload-pack /tmp/x origin", true},
		{"ls-remote -u /tmp/x origin", true},
		{"ls-remote -u/tmp/x origin", true},
		{"ls-remote --exec=/tmp/x origin", true},
		{"ls-remote --exec /tmp/x origin", true},
		{"ls-remote --server-option=x origin", true},
		{"ls-remote --server-option x origin", true},
		{"ls-remote -o x origin", true},
		{"ls-remote -ox origin", true},
		{"ls-remote --upload=/tmp/x origin", true},
		{"ls-remote origin --upload-pack=/tmp/x", true},
		{"ls-remote --heads=yes origin", true},
		{"ls-remote -- origin", true},
		{"symbolic-ref HEAD", false},
		{"symbolic-ref -q --short HEAD", false},
		{"symbolic-ref --quiet --no-recurse HEAD", false},
		{"symbolic-ref --recurse HEAD", false},
		{"symbolic-ref -qd HEAD", true},
		{"symbolic-ref -dq HEAD", true},
		{"symbolic-ref --del HEAD", true},
		{"symbolic-ref --delete HEAD", true},
		{"symbolic-ref --d HEAD", true},
		{"symbolic-ref HEAD --del", true},
		{"symbolic-ref --short=yes HEAD", true},
		{"symbolic-ref -m message HEAD", true},
		{"branch --del x", true},
		{"branch -vd x", true},
		{"branch --set-u origin/main", true},
		{"branch --list --del x", true},
		{"branch --list -vd x", true},
		{"branch --list --set-u origin/main", true},
		{"tag --del v1", true},
		{"tag -vd v1", true},
		{"tag --set-u v1", true},
		{"tag -l --del v1", true},
		{"tag -l -vd v1", true},
		{"tag -l --set-u v1", true},
		{"tag -nd v1", true},
		{"tag -n1d v1", true},
		{"tag -l -nd v1", true},
		{"tag -n", false},
		{"tag -n12", false},
		{"remote --verbose", false},
		{"remote -v add x y", true},
		{"remote --del x", true},
		{"remote -vd x", true},
		{"remote --set-u x", true},
		{"stash --del x", true},
		{"stash -vd x", true},
		{"stash --set-u x", true},
		{"reflog --del HEAD", true},
		{"reflog -vd HEAD", true},
		{"reflog --set-u HEAD", true},
		{"worktree --del x", true},
		{"worktree -vd x", true},
		{"worktree --set-u x", true},
		{"config --list --del", true},
		{"config --list -vd", true},
		{"config --list --set-u", true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if got := headGitExecutes(strings.Fields(tc.command)); got != tc.blocked {
				t.Fatalf("headGitExecutes(%q) = %v, want %v", tc.command, got, tc.blocked)
			}
		})
	}
}

func TestHeadGuardBlockedCommand(t *testing.T) {
	t.Setenv("ORCH_HEAD_GUARD", "1")
	const prefix = "Head execution is delegated, including tiny edits, builds, tests and Git. Use orch do for writes, orch ask for research, or orch test for commands; Head may inspect and write run contracts/acceptance/decisions."
	for _, tc := range []struct{ command, suffix string }{
		{"git log -1 && git remote add x y", "git remote add x y"},
		{"git $action", "git $action"},
		{"git status && (git remote -v)", "git status && (git remote -v)"},
	} {
		got := headExecutionReason(hookJSON("Bash", map[string]string{"command": tc.command}))
		if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, " Blocked: "+tc.suffix) {
			t.Errorf("%s: %s", tc.command, got)
		}
	}
	if got := headExecutionReason(hookJSON("Bash", map[string]string{"command": "git log --oneline -8 && git status -sb && git remote -v"})); got != "" {
		t.Fatalf("read-only compound refused: %s", got)
	}
	if got := headExecutionReason(hookJSON("Write", map[string]string{"path": "/outside/file.go"})); got != prefix {
		t.Fatalf("Write refusal changed: %s", got)
	}
}

func TestHeadGuardBlockedCommandSuffix(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"joined", "git remote -v", "git remote -v"},
		{"boundary", strings.Repeat("a", 160), strings.Repeat("a", 160)},
		{"ascii", strings.Repeat("a", 161), strings.Repeat("a", 160) + "…"},
		{"unicode", strings.Repeat("한", 54), strings.Repeat("한", 53) + "…"},
		{"split rune", strings.Repeat("a", 159) + "한", strings.Repeat("a", 159) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := blockedCommandSuffix(strings.Fields(tc.input))
			if got != " Blocked: "+tc.want || !utf8.ValidString(got) {
				t.Fatalf("suffix = %q, want %q", got, " Blocked: "+tc.want)
			}
		})
	}
}
