package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// completionChanges unions current working-tree changes, modified/discarded
// baseline dirt, and commits since the frozen scope base. Only working-tree
// changes are baseline-filtered: committing user dirt is still a worker change.
// Commits made only on other branches remain outside this HEAD-based check.
func completionChanges(cwd, root, base string, baseline map[string]string, owned []string) ([]string, []string, error) {
	changed, err := changedFiles(root, "")
	if err != nil {
		return nil, nil, err
	}
	changed = freshChanges(root, changed, baseline)
	seen := make(map[string]bool, len(changed))
	for _, f := range changed {
		seen[f] = true
	}
	add := func(f string) {
		if f != "" && !seen[f] {
			changed = append(changed, f)
			seen[f] = true
		}
	}
	// Discarded dirt can disappear from git diff entirely.
	for f, hash := range baseline {
		if fileHash(root, f) != hash {
			add(f)
		}
	}
	if (explicitBase(base) || base == emptyTree) && exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "HEAD").Run() == nil {
		committed, err := gitOutput(root, "-c", "core.quotepath=off", "diff", "--no-renames", "--name-only", base, "HEAD", "--")
		if err != nil {
			return nil, nil, err
		}
		for _, f := range strings.Split(committed, "\n") {
			add(f)
		}
	}
	sort.Strings(changed)
	return changed, repositoryScopeViolations(cwd, root, changed, owned), nil
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
