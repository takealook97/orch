package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type reviewContractOptions struct{ Reviews, Diff, Mech, Focus, Cwd string }

var reviewTaskID = regexp.MustCompile(`^task-[1-9][0-9]*$`)

// reviewContract leaves the hand-written template unchanged unless a review is requested.
func reviewContract(r *Run, name, role, key string, o reviewContractOptions) (string, error) {
	if o.Reviews == "" {
		return contractTemplate(r, name, role, key), nil
	}
	if !validReviewName(name) {
		return "", fmt.Errorf("invalid review name %q", name)
	}
	if role != "verify-critical" && role != "verify-neutral" && role != "verify-adversarial" {
		return "", fmt.Errorf("--reviews requires a review role")
	}
	if strings.TrimSpace(o.Diff) == "" {
		return "", fmt.Errorf("--reviews requires --diff")
	}
	if strings.HasPrefix(o.Diff, "-") {
		return "", fmt.Errorf("--diff must be a revision range, not a git option")
	}
	if role == "verify-adversarial" && strings.TrimSpace(o.Focus) == "" {
		return "", fmt.Errorf("adversarial review requires --focus")
	}
	author := r.worker(o.Reviews)
	if author == nil {
		return "", fmt.Errorf("reviewed worker %q not found", o.Reviews)
	}
	repo := o.Cwd
	if repo == "" {
		repo = author.Worktree
	}
	if repo == "" {
		repo = author.Cwd
	}
	if repo == "" {
		repo = "."
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	mech := ""
	if o.Mech != "" {
		mech = o.Mech
		if reviewTaskID.MatchString(mech) {
			mech = filepath.Join(r.dir(), "tasks", mech, "test.log")
		}
		mech, err = filepath.Abs(mech)
		if err != nil {
			return "", err
		}
		info, e := os.Stat(mech)
		if e != nil {
			return "", fmt.Errorf("mechanical evidence: %w", e)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("mechanical evidence is not a file: %s", mech)
		}
	}
	goal := "Find reproducible defects in the reviewed change."
	plan := "1. Inspect the diff and affected code; find defects with file:line locations and repro commands or exact traces.\n2. Sort findings by severity (CRITICAL/HIGH/MEDIUM/LOW), reporting at most the top 10."
	report := "## Findings (sorted by severity, top 10)\n- Record severity, file:line, defect and repro/evidence; write none when no defect is proven.\n"
	switch role {
	case "verify-neutral":
		goal = "Judge every acceptance criterion with evidence."
		plan = "1. Judge each checklist row as met / unmet / cannot verify from evidence; cite code, tests or documents for each judgment.\n2. Carry over machine-checkable results from the mechanical evidence; report additions and confusing points."
		table, e := checklist(r, false, 0)
		if e != nil {
			return "", e
		}
		report = "## Checklist\n" + table
	case "verify-adversarial":
		goal = "Refute the focus target or produce a concrete counterexample."
		plan = "1. Attempt to refute the focus target using the diff and available evidence.\n2. Produce a reproducible counterexample, or explain the attempted attacks and remaining limits without claiming proof of correctness."
		report = "## Counterexample or refutation\n- Record the target, attempted attack, concrete counterexample and reproducible evidence, or explain why none was found.\n"
	}
	if o.Focus != "" {
		plan += "\n- Focus: " + o.Focus
	}
	cmd := exec.Command("git", "-C", repo, "diff", "--no-ext-diff", "--no-textconv", o.Diff, "--")
	var errs strings.Builder
	cmd.Stderr = &errs
	diff, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff %q: %w: %s", o.Diff, err, strings.TrimSpace(errs.String()))
	}
	if len(strings.TrimSpace(string(diff))) == 0 {
		return "", fmt.Errorf("diff range %q is empty", o.Diff)
	}
	diffPath := filepath.Join(r.dir(), "evidence", name+".diff")
	inputs := fmt.Sprintf("- Original request: %s/request.md\n- Acceptance criteria: %s/acceptance.md\n- Head decisions: %s/decisions.md\n- Reviewed worker: %s\n- Diff: %s (git diff %s)\n- Repository: %s", r.dir(), r.dir(), r.dir(), o.Reviews, diffPath, o.Diff, repo)
	if mech != "" {
		inputs += "\n- Mechanical evidence: " + mech
	}
	body := fmt.Sprintf(`# Contract: %s
- run: %s · stage: %s
- model: %s   (for the record; orch enforces the actual choice)

## Goal
%s

## Inputs
%s

## Non-goals
- Style nits, unrelated refactors, or fixing the implementation.

## Baseline
- diff range: %s   · worktree: %s (read-only)
- decision log: %s/decisions.md (read and follow it)

## Plan
%s

## Scope
- Owned files (glob): none
- Changes outside owned files fail the contract (orch collect checks the diff)
- Allowed evidence sources: the repository and this run directory, plus the mechanical evidence file when supplied. Do not search unrelated connectors.

## Permissions
- Allowed: read-only inspection; report findings to the result file. Run verification only in a temporary copy with temporary HOME and ORCH_HOME.
- Keep toolchains, virtualenvs, package/module caches and build outputs in a temp dir ($TMPDIR) outside ~/.orch; store only small logs/excerpts under evidence/.
- Forbidden: deployment, external writes, git push, out-of-scope edits, delegation (no panes, subagents, or orch)

## Deliverables
- A completed review result at %s.

## Budget
- Time: 30 minutes; report partial and stop if the budget will be exceeded.

## Self-check
- Every conclusion has primary evidence; distinguish confirmed defects from plausible risks and unknowns.
- Put only verified, reusable facts with a source path or command in the optional Reusable facts section (at most 8).

## Result report
Write the following format to %s. Fill all instructions with actual findings. Add the status line only after the file is complete.

`+"```markdown"+`
# Result: %s
- model_used: %s · fallback: none
- summary: Summarize the outcome in up to three lines.
%s
## Evidence
- Record commands with exit codes and evidence locations.
## Reusable facts
- Record verified facts with source paths, or none.
## Uncertainty
- Explain evidence limitations, or none.
## Blockers
- Record needed decisions or inputs, or none.
- status: done | blocked | partial
`+"```"+`
`, name, r.ID, role, key, goal, inputs, o.Diff, repo, r.dir(), plan, r.resultPath(name), r.resultPath(name), name, key, report)
	if err = validateContract(body); err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(diffPath), 0700); err != nil {
		return "", err
	}
	if err = os.WriteFile(diffPath, diff, 0600); err != nil {
		return "", err
	}
	return body, nil
}

func validReviewName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\")
}
