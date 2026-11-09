package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWorktreeCommitSubject(t *testing.T) {
	cases := []struct{ name, summary, want string }{
		{"label", "summary: Implemented X.", "chore: Implemented X"},
		{"conventional", "fix: handle empty input", "fix: handle empty input"},
		{"multiline", "\n \nsummary: First line.\nSecond line", "chore: First line"},
		{"long", strings.Repeat("가", 100) + ". ", "chore: " + strings.Repeat("가", 65)},
		{"trailing", "SUMMARY: docs: explain behavior...  ", "docs: explain behavior"},
	}
	for _, mode := range []string{"worktree", "completion"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				dir := taskRepo(t)
				putFile(t, dir, "change.txt", "change\n")
				var err error
				if mode == "worktree" {
					_, err = commitWorktree(dir, "run-id", "worker-name", tc.summary)
				} else {
					_, err = commitCompletion(dir, "run-id", "worker-name", tc.summary, []string{"change.txt"})
				}
				if err != nil {
					t.Fatal(err)
				}
				subject := strings.TrimSpace(git(t, dir, "log", "-1", "--format=%s"))
				if subject != tc.want || !utf8.ValidString(subject) || utf8.RuneCountInString(subject) > 72 {
					t.Errorf("subject=%q want=%q", subject, tc.want)
				}
				body := strings.TrimSpace(git(t, dir, "log", "-1", "--format=%b"))
				if body != "orch-run: run-id\norch-worker: worker-name" {
					t.Errorf("body=%q", body)
				}
			})
		}
	}
}
