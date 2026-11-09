package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Both pane collection and do's post-test gate compare against the frozen baseline.
// Git ignores generated artifacts according to the repository's existing rules.
func completionChanges(cwd, root, base string, baseline map[string]string, owned []string) ([]string, []string, error) {
	changed, err := changedFiles(root, base)
	if err != nil {
		return nil, nil, err
	}
	fresh := freshChanges(root, changed, baseline)
	return fresh, repositoryScopeViolations(cwd, root, fresh, owned), nil
}

// Commit only accepted fresh paths. Unrelated staged or preexisting changes stay intact.
// If a commit succeeded but its state save failed, retry finds HEAD without adding a commit.
func commitCompletion(dir, runID, name, summary string, fresh []string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("missing worktree directory")
	}
	if len(fresh) == 0 {
		return gitOutput(dir, "rev-parse", "HEAD")
	}
	// --only commits the specified paths from the working tree and preserves other index entries.
	args := []string{"--literal-pathspecs", "diff", "--name-only", "HEAD", "--"}
	args = append(args, fresh...)
	diff, err := gitOutput(dir, args...)
	if err != nil {
		return "", err
	}
	args = []string{"--literal-pathspecs", "ls-files", "--others", "--exclude-standard", "--"}
	args = append(args, fresh...)
	untracked, err := gitOutput(dir, args...)
	if err != nil {
		return "", err
	}
	if diff != "" || untracked != "" {
		args = []string{"--literal-pathspecs", "add", "-A", "--"}
		args = append(args, fresh...)
		if _, err = gitOutput(dir, args...); err != nil {
			return "", err
		}
		args = []string{"--literal-pathspecs", "commit", "--only", "-m", commitSubject(summary), "-m", commitBody(runID, name), "--"}
		args = append(args, fresh...)
		if _, err = gitOutput(dir, args...); err != nil {
			return "", err
		}
	}
	return gitOutput(dir, "rev-parse", "HEAD")
}

var conventionalSubject = regexp.MustCompile(`^(feat|fix|refactor|docs|test|chore|perf|ci)(\([^\r\n)]+\))?!?:\s+`)

// commitSubject keeps the first summary line readable in standard Git log views.
func commitSubject(summary string) string {
	line := ""
	for _, candidate := range strings.Split(summary, "\n") {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			line = candidate
			break
		}
	}
	if len(line) >= len("summary:") && strings.EqualFold(line[:len("summary:")], "summary:") {
		line = strings.TrimSpace(line[len("summary:"):])
	}
	if line == "" {
		line = "complete work"
	}
	if !conventionalSubject.MatchString(line) {
		line = "chore: " + line
	}
	runes := []rune(line)
	if len(runes) > 72 {
		line = string(runes[:72])
	}
	return strings.TrimRightFunc(line, func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
}

func commitBody(runID, name string) string {
	return fmt.Sprintf("orch-run: %s\norch-worker: %s", runID, name)
}
