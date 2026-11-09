package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var requiredSections = []string{"## Goal", "## Inputs", "## Scope", "## Permissions", "## Deliverables", "## Result report"}

func validateContract(body string) error {
	var missing []string
	for _, s := range requiredSections {
		if !strings.Contains(body, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("contract is missing required sections: %s", strings.Join(missing, ", "))
	}
	return nil
}

func contractTemplate(r *Run, name, role, key string) string {
	return fmt.Sprintf(`# Contract: %[1]s
- run: %[2]s · stage: %[3]s
- model: %[4]s   (for the record; orch enforces the actual choice)

## Goal
<one or two sentences>

## Inputs
- Original request: %[5]s/request.md
- Acceptance criteria: %[5]s/acceptance.md (items relevant to this contract: #)
- References: <paths, URLs, earlier result files>

## Non-goals
- <what not to do>

## Baseline
- base commit: <hash or none>   · worktree: <path or none>
- decision log: %[5]s/decisions.md (read and follow it)
- Interface assumptions: <signatures and schemas shared with other pieces>

## Plan
- <ordered steps written like a /plan handover. Implementation: file, function and signature, exact change, edge cases, and the test that proves it. Recon/verify/docs: where to look, what to check or claim, and how each finding is proven. Follow it as written and report any gap under Blockers>

## Scope
- Owned files (glob): none
- Changes outside owned files fail the contract (orch collect checks the diff)
- Allowed evidence sources: <local paths, specific sites>. Do not search unrelated connectors

## Permissions
- Allowed: <test runs, local file writes ...>
- Keep toolchains, virtualenvs, package/module caches and build outputs in a temp dir ($TMPDIR) outside ~/.orch; store only small logs/excerpts under evidence/.
- Leave temporary directories in place; never run `+"`rm -rf`"+`, especially on paths built from command substitution — orch cannot answer an approval dialog.
- Forbidden: deployment, external writes, git push, out-of-scope edits, delegation (no panes, subagents, or orch)

## Deliverables
- <files, diffs, documents, image paths>

## Budget
- Time: <minutes> · report partial and stop if the budget will be exceeded

## Self-check
- <commands to run, conditions to check>
- Put only verified, reusable facts with a source path or command in the optional Reusable facts section (at most 8).

## Result report
Write the following format to %[6]s. Add the status line only after the file is complete.

`+"```markdown"+`
# Result: %[1]s
- status: done | blocked | partial
- model_used: %[4]s · fallback: none
- summary: <up to three lines>
## Deliverables
- <path and description>
## Evidence
- command: `+"`<cmd>`"+` → exit <code>, output excerpt or evidence/ path
## Reusable facts
- <fact> — <source path or command>
## Uncertainty
- <what is uncertain and why>
## Blockers
- <needed decision, permission, or input>
`+"```"+`
`, name, r.ID, role, key, r.dir(), r.resultPath(name))
}

var statusLine = regexp.MustCompile(`(?m)^\s*-?\s*status:\s*(done|blocked|partial)\s*$`)

// resultStatus reads the result file and returns done|blocked|partial, or "" if not final.
func resultStatus(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	m := statusLine.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// contractField returns the value after "- <label>:" in the contract.
func contractField(body, label string) string {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "- ")
		if rest, ok := strings.CutPrefix(line, label); ok {
			rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ":"))
			if i := strings.Index(rest, "   "); i >= 0 {
				rest = rest[:i]
			}
			if i := strings.Index(rest, " · "); i >= 0 {
				rest = rest[:i]
			}
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func ownedGlobs(body string) []string {
	v := contractField(body, "Owned files (glob)")
	if v == "" || v == "none" || strings.HasPrefix(v, "<") {
		return nil
	}
	var out []string
	for _, g := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		g = strings.Trim(g, "`")
		if g != "" {
			out = append(out, g)
		}
	}
	return out
}

// globMatch matches slash paths with '*' (one segment) and '**' (any segments).
func globMatch(pattern, name string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for i := 0; i <= len(n); i++ {
				if matchSegs(p[1:], n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		if ok, _ := path.Match(p[0], n[0]); !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}

// emptyTree is git's well-known empty tree; it stands in for HEAD before the first commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// repoRoot returns the top level of the git repository containing dir, or dir itself.
func repoRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return dir
	}
	return strings.TrimSpace(string(out))
}

// contractBase reads the base commit: the first word of the field, so a note such as
// "cbe6ed2 (before this work)" still names the commit. Placeholders stay as written.
func contractBase(body string) string {
	base := contractField(body, "base commit")
	if strings.HasPrefix(base, "<") {
		return base
	}
	if f := strings.Fields(base); len(f) > 0 {
		return f[0]
	}
	return base
}

func explicitBase(base string) bool {
	return base != "" && base != "none" && !strings.HasPrefix(base, "<")
}

func hasBaseCommit(dir, base string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", "--end-of-options", base+"^{commit}").Run() == nil
}

var contractPaths = regexp.MustCompile("`(/[^`]+)`|\"(/[^\"]+)\"|'(/[^']+)'|(/[^\\s`\"'(),;\\[\\]]+)")

// scopeRepository prefers a unique repository named by an absolute contract
// path, then falls back to a unique repository in cwd and its children through
// depth two. Git discovers both normal repositories and linked worktrees.
func scopeRepository(cwd, body string) (string, error) {
	base := contractBase(body)
	root := repoRoot(cwd)
	if !explicitBase(base) {
		return root, nil
	}
	collect := func(dirs []string) []string {
		seen := map[string]bool{}
		var candidates []string
		try := func(dir string) {
			if !isGitDir(dir) {
				return
			}
			r := repoRoot(dir)
			if resolved, err := filepath.EvalSymlinks(r); err == nil {
				r = resolved
			}
			if seen[r] {
				return
			}
			seen[r] = true
			if hasBaseCommit(r, base) {
				candidates = append(candidates, r)
			}
		}
		for _, dir := range dirs {
			try(dir)
		}
		sort.Strings(candidates)
		return candidates
	}
	// File and glob paths are trimmed back to an existing directory.
	var explicitDirs []string
	for _, match := range contractPaths.FindAllStringSubmatch(body, -1) {
		var token string
		for _, part := range match[1:] {
			if part != "" {
				token = part
				break
			}
		}
		// A URL path is not a filesystem reference.
		if strings.Contains(token, "://") || !filepath.IsAbs(token) {
			continue
		}
		for dir := token; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				explicitDirs = append(explicitDirs, dir)
				break
			}
		}
	}
	if candidates := collect(explicitDirs); len(candidates) == 1 {
		return candidates[0], nil
	}
	var search func(string, int, *[]string)
	search = func(dir string, depth int, dirs *[]string) {
		*dirs = append(*dirs, dir)
		if depth == 2 {
			return
		}
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != ".git" {
				search(filepath.Join(dir, entry.Name()), depth+1, dirs)
			}
		}
	}
	var dirs []string
	search(cwd, 0, &dirs)
	if candidates := collect(dirs); len(candidates) == 1 {
		return candidates[0], nil
	} else if len(candidates) > 1 {
		return "", fmt.Errorf("multiple repositories containing base commit %s found under %s: %s; 계약 Baseline 에 작업 저장소 절대 경로를 적어라", base, cwd, strings.Join(candidates, ", "))
	}
	return "", fmt.Errorf("no repository containing base commit %s found for %s (contract paths and children through depth 2 checked)", base, cwd)
}

// Anchor ownership to cwd and compare absolute paths, so workspace-relative
// globs and absolute contract globs retain their original meaning.
func repositoryScopeViolations(cwd, root string, changed, globs []string) []string {
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	patterns := make([]string, len(globs))
	for i, g := range globs {
		if !filepath.IsAbs(g) {
			g = filepath.Join(cwd, g)
		} else {
			// Resolve the existing prefix without trying to stat wildcard segments.
			prefix := g
			for {
				if real, err := filepath.EvalSymlinks(prefix); err == nil {
					rel, _ := filepath.Rel(prefix, g)
					g = filepath.Join(real, rel)
					break
				}
				parent := filepath.Dir(prefix)
				if parent == prefix {
					break
				}
				prefix = parent
			}
		}
		patterns[i] = filepath.ToSlash(filepath.Clean(g))
	}
	var bad []string
	for _, f := range changed {
		if len(scopeViolations([]string{filepath.ToSlash(filepath.Join(root, f))}, patterns)) > 0 {
			bad = append(bad, f)
		}
	}
	return bad
}

// changedFiles lists files changed in dir's repository relative to base (or HEAD) plus
// untracked files. Paths are relative to the repository root, whatever subdirectory dir is,
// and renames count as a deletion plus an addition so both sides are checked.
func changedFiles(dir, base string) ([]string, error) {
	root := repoRoot(dir)
	if base == "" || base == "none" || strings.HasPrefix(base, "<") {
		base = "HEAD"
		if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "HEAD").Run() != nil {
			base = emptyTree
		}
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	diff, err := exec.Command("git", "-C", root, "-c", "core.quotepath=off", "diff", "--no-renames", "--name-only", base).Output()
	if err != nil {
		return nil, fmt.Errorf("git diff failed (%s): %w", dir, err)
	}
	for _, l := range strings.Split(string(diff), "\n") {
		add(l)
	}
	untracked, err := exec.Command("git", "-C", root, "-c", "core.quotepath=off", "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files failed (%s): %w", dir, err)
	}
	for _, l := range strings.Split(string(untracked), "\n") {
		add(l)
	}
	return out, nil
}

// rootGlobs turns owned globs written relative to cwd into globs relative to the repository
// root, the form changedFiles reports.
func rootGlobs(cwd string, globs []string) []string {
	root, err := filepath.EvalSymlinks(repoRoot(cwd))
	if err != nil {
		return globs
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return globs
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return globs
	}
	out := make([]string, len(globs))
	for i, g := range globs {
		out[i] = filepath.ToSlash(rel) + "/" + strings.TrimPrefix(g, "./")
	}
	return out
}

// fileHash returns the hash of a file's mode and content (path relative to the repository
// root), or "deleted", so a chmod counts as a change too.
func fileHash(root, rel string) string {
	p := filepath.Join(root, rel)
	fi, err := os.Stat(p)
	if err != nil {
		return "deleted"
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "deleted"
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", fi.Mode())
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// snapshot records the content hash of every already-changed file.
func snapshot(dir string, files []string) map[string]string {
	root := repoRoot(dir)
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f] = fileHash(root, f)
	}
	return out
}

// freshChanges keeps files that were not dirty at prompt time or whose content changed since.
func freshChanges(dir string, changed []string, baseline map[string]string) []string {
	root := repoRoot(dir)
	var out []string
	for _, f := range changed {
		if h, ok := baseline[f]; ok && h == fileHash(root, f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// scopeViolations returns changed files that match none of the owned globs.
func scopeViolations(changed, globs []string) []string {
	var bad []string
	for _, f := range changed {
		ok := false
		for _, g := range globs {
			if globMatch(g, f) {
				ok = true
				break
			}
		}
		if !ok {
			bad = append(bad, f)
		}
	}
	return bad
}
