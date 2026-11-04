package main

import (
	"fmt"
	"strings"
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
		line := strings.SplitN(strings.TrimSpace(summary), "\n", 2)[0]
		args = []string{"--literal-pathspecs", "commit", "--only", "-m", "orch: " + runID + "/" + name + ":" + line, "--"}
		args = append(args, fresh...)
		if _, err = gitOutput(dir, args...); err != nil {
			return "", err
		}
	}
	return gitOutput(dir, "rev-parse", "HEAD")
}
