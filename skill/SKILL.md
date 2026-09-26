---
name: orch
description: Head's operating manual for multi-agent work through Herdr panes with the `orch` executor — choose a mode (direct/light/full/full+), write contracts, pick catalog keys by purpose, run independent verification lanes, and clean up. Use whenever work might be delegated or independently verified.
---

# orch operating manual (for the Head)

Enforcement lives in the `orch` executor and its hooks. This document is the judgment guide. Everything this skill needs ships in its own directory: the full design doc is `references/design.md`, and `references/decisions.md` records later decisions (it wins where the two differ).
A worker (`ORCH_ROLE=worker`) does not use this document. It follows only its contract.

Check whether you're inside Herdr first: `test "$HERDR_ENV" = 1`. If not, pane delegation is impossible; do the work directly and say so.
Delegate only through `orch`, in Claude and Codex alike. Don't drive Herdr panes directly, even if a Herdr skill is installed.

## 1. Modes and thresholds

Estimate within 30 seconds of receiving the request.

| Symbol | Item |
|---|---|
| E | Expected effort if the Head does it directly (tool calls, time) |
| F | Number of files changed, module boundaries |
| P | Number of independent, non-blocking subtasks that clear the delegation break-even point |
| U | Can the acceptance criteria be pinned down one sentence at a time right now |
| R | Can it be reverted immediately with git |
| H | High-risk marker |

| Mode | Condition | Flow |
|---|---|---|
| direct | All of: E <= 10 calls/5 min · F <= 2 files, one module · P <= 1 · U pinned · R immediate · no H | The Head does it directly. No run. The Head runs the tests once if any exist |
| light | Exceeds direct, but F <= 5, 1-2 modules · U pinned · R possible · no H | 1 producer (parallel if P >= 2) + 1 independent verification lane |
| full | Any of: F > 5 or 3+ modules · P >= 3 · U needs investigation to pin down · R is cumbersome | Investigate -> (design) -> produce -> verification lanes -> integrate |
| full+ | Any H applies | full + 2 critical lanes from different families + an adversarial lane |

H: auth/permissions/secrets/encryption / data deletion, migration, schema, backup / money, payment, billing, usage calculation / core concurrency structures / production config, deploy pipeline, externally published output / the user asks for "thorough, complete verification".

light's single verification lane: if it's code and tests exist, the Head runs the tests directly plus **1 critical lane** (a different family). If it's docs or user-facing output, **1 neutral lane** (`gemini-medium-m`). If both apply, use critical.

**Delegation break-even point** (for one subtask): direct execution would take 5+ minutes or 10+ tool calls · running it concurrently shortens the critical path · an independent perspective is required (verification) · it would heavily pollute the Head's context (large logs, long documents). If none apply, do it directly. Do not split into pieces under 5 minutes. The fixed cost of delegation is 10-30 seconds of pane startup, plus writing the contract, plus the worker rebuilding context, plus judging the result.

Adjustment: if scope turns out small, downgrade immediately and close unneeded workers. Upgrade if H surfaces, criteria shift, or the same defect appears twice. Record it with `orch run mode <mode> --reason "..."`.

Work coming from `/goal` or another command follows the same rule. A command sets the goal and iteration style; this document decides who does it. If a command instructs a subagent, parallel agents, or a Workflow, run it through `orch` instead.

## 2. Flow

```bash
orch run new --text "<original request verbatim>" --mode full --playbook feature   # prints the run id, ~/.orch/runs/<id>/
# fill in acceptance.md (numbered acceptance criteria) and decisions.md (naming, interface, format decisions)
orch contract new impl-api --role produce --model codex-high-m      # contract template -> fill it in
orch spawn --model codex-high-m --name impl-api --role produce --purpose impl --worktree ../wt-api
orch prompt impl-api                                          # send only the contract path and result path
orch wait impl-api                                            # judged by the status line in the result file; auto-falls back on capacity exhaustion
orch collect impl-api                                         # checks results + owned-file scope (a violation is a failure)
orch spawn --model claude-medium-m --name crit-api --role verify-critical --reviews impl-api
...
orch run end                                                  # close every pane and confirm removal
```

- To spawn several workers at once, call spawn repeatedly, then wait on all of them together with `orch wait a b c`.
- If there are multiple pieces with dependencies, **delegate as a DAG.** Write `dag.yaml` in the run directory, then `orch dag check` -> `orch dag run`. It spawns nodes whose inputs are ready, in order of longest critical path, closes finished panes, and skips downstream nodes whose dependencies aren't done (the mechanical lane is the gate). If interrupted, calling `orch dag run` again resumes it. Check status with `orch dag status`.

  ```yaml
  nodes:
    - {name: recon-api, model: codex-medium-m, role: recon, purpose: explore}
    - {name: impl-api, model: codex-high-m, role: produce, purpose: impl, needs: [recon-api], worktree: ../wt-api}
    - {name: mech-api, model: codex-medium-h, role: verify-mech, needs: [impl-api]}
    - {name: crit-api, model: claude-medium-m, role: verify-critical, reviews: [impl-api], needs: [mech-api]}
    - {name: neu-api, model: gemini-medium-m, role: verify-neutral, reviews: [impl-api], needs: [mech-api]}
  ```
  Contracts default to `contracts/<name>.md`. `reviews` also implies `needs`. Output the Head wrote is `author_family: [head]`.
- If the right column is full (terminal height / 8, max 6 rows), spawn is refused. Nodes are spawned in critical-path order as rows free up.
- **A finished worker pane must be closed immediately.** The moment `orch wait` sees the status line in the result file, it closes that pane and confirms removal. Same for `orch collect`, fallback, and `orch run end`. Panes are never reused; the next contract gets a new worker. A worker that stalled with no result (`idle-no-result`) is also closed right away with `orch close <name>` once you've made a judgment call.
- Don't read a worker's conversation transcript. Use `orch read` only when the result file is missing or something is stuck.
- `agent-blocked`, `idle-no-result`, and `timeout` are unresolved. Check with `orch read`, then choose among extending, `orch fallback`, or reassigning. Don't answer an approval question that falls outside already-granted scope; escalate it to the user.

## 3. Contracts

Fill in the template `orch contract new` produces. If a required section (Goal, Inputs, Scope, Permissions, Deliverables, Result report) is missing, `orch prompt` refuses.

- **Write it to be self-contained.** Even if a fallback starts fresh on a different model or account, the contract alone must be enough to do the work.
- Hand over **paths** (files, line ranges, commit hashes), not inline content.
- Give `Owned files (glob)` exclusively. If read-only, write "none". `orch collect` diffs changes since the base commit and fails anything outside scope.
- Write down `Allowed evidence sources`. Unrelated Slack or external-connector searches are forbidden.
- Forbidden under Permissions: deploy, external writes, git push, out-of-scope edits, re-delegation.
- If a worker needs a new decision, it goes under "Blockers". The Head folds it into decisions.md and updates any affected contracts.

## 4. Model placement (by purpose, chosen per contract)

`orch models` shows the catalog. Families: OpenAI (`codex-*`), Anthropic (`claude-high-m` `claude-medium-m`), Google (`gemini-medium-m`).
Pick the **cheapest key** that satisfies the contract, but reach for `claude-high-m` right away if the difficulty is visible upfront. Don't stall by retrying with a weaker model.

| Purpose | Primary | Conditional alternative | `--purpose` |
|---|---|---|---|
| Broad file/symbol search, bulk classification, translation | `codex-medium-m` | irregular naming -> `codex-medium-h`; ambiguous classification criteria -> `claude-medium-m` | explore |
| Evidence extraction, log/test-failure analysis, data extraction | `codex-medium-h` | tangled root-cause candidates or schema interpretation -> `codex-high-m` | extract |
| Web, long documents, PDFs, reading images/screenshots | `gemini-medium-m` | deep technical interpretation -> `claude-high-m`; visual detail -> `claude-medium-m` | web |
| Synthesizing conflicting evidence, root-cause hypotheses, ambiguous design | `claude-high-m` | -- | synthesis |
| Drafting interfaces/schemas | `codex-high-m` | wide blast radius -> `claude-high-m` | impl |
| Mechanical implementation with a complete spec | `codex-high-l` | very narrow, 1-2 files -> `codex-medium-h` | mech-impl |
| General feature implementation, bug fixes, test code | `codex-high-m` | -- | impl |
| Hard implementation/debugging (concurrency, performance, security, complex state, flaky repro) | `claude-high-m` | reliable repro, localized cause -> `codex-high-m` | hard-impl |
| Alternative implementation/design (for comparison) | `claude-medium-m` | primary is Anthropic -> `codex-high-m` | alt |
| Docs, reports, research synthesis | `claude-medium-m` | deep technical content or conflicting sources -> `claude-high-m` | docs |
| Short summaries, changelogs | `codex-medium-h` | -- | extract |
| Data analysis | `codex-high-m` | ambiguous interpretation or causal judgment -> `claude-high-m` | analysis |
| Merge/integration mechanics | `codex-high-m` | semantic conflicts -> `claude-high-m` | integrate |
| Image generation | `codex-high-m` (Codex image_generation) | -- | image |

Effort is chosen separately from the model (bulk classification: `codex-medium-m`; precise extraction: `codex-medium-h`; fully specified: `codex-high-l`; mixed judgment: `codex-high-m`). `claude-high-m` effort: medium or low only, never high. No model has a fixed role, but `gemini-medium-m` can never be the sole verifier.

**Fallback**: if `orch wait` detects capacity exhaustion in the output, it first tries a different account with the same key (when `ORCH_ACCOUNT_CMD` exists), then the next key in the `--purpose` chain, opening a new pane and appending prior partial results to the same contract. Verification lanes only use keys that still pass the family constraint. If the whole chain is blocked, leave that piece as blocked and continue with the rest. A quality failure is not a fallback but an **escalation** (`codex-high-m` -> `claude-high-m`, `codex-medium-h` -> `codex-high-m`).

## 5. Verification lanes

Plan verification at the same time you assign the production work. An author's own tests or self-review are only supporting evidence.

| Lane | `--role` | Who | Input | Output |
|---|---|---|---|---|
| Mechanical | verify-mech | `codex-medium-h` (not the author) or the Head directly | Deliverables, commands to run | Commands, exit codes, output excerpts, failure list |
| Critical | verify-critical | A **different family** from the author (enforced via `--reviews`) | Original request, acceptance criteria, diff, mechanical results | Defect list (location, repro, severity) |
| Neutral | verify-neutral | `gemini-medium-m` (never given the author's self-assessment) | Original request, acceptance criteria, deliverables, evidence of actual behavior | Requirements checklist |
| Adversarial | verify-adversarial | A different family from the author, `gemini-medium-m` forbidden. Default `claude-high-m`; `codex-high-m` if the author is Anthropic | One concrete attack target | A counterexample or grounds for refuting it |

Default critical-lane placement: author OpenAI -> `claude-medium-m` (or `claude-high-m` if complex) · Anthropic -> `codex-high-m` · Google -> `codex-high-m` (or `claude-high-m` if complex). Confirming a single narrow claim: `codex-medium-h` (or `claude-medium-m` if the author is OpenAI).

| Mode | Mechanical | Critical | Neutral | Adversarial |
|---|---|---|---|---|
| direct | Head, if needed | -- | -- | -- |
| light | yes (if there's something to run) | yes, for code | yes, for docs/UI | -- |
| full | yes | yes | yes | optional |
| full+ | yes | yes, x2, different families | yes | yes |

- **The mechanical lane is the gate.** If it fails, don't spawn LLM lanes; send it back for a fix. If it passes, spawn the rest concurrently.
- Give LLM reviewers permission to run tests too, and require evidence (file:line, repro command) for every finding.
- Build the neutral checklist template with `orch checklist` (generated from acceptance.md). For full+'s second judgment, shuffle the order with `orch checklist --shuffle`.
- The neutral checklist format is fixed: `| # | Acceptance criterion | Verdict (met/not met/cannot confirm from evidence) | Evidence location | Notes |` plus "added but not requested" and "confusing from the user's perspective". One verdict per item. For items confirmable by script, carry over the mechanical-lane result. In full+, shuffle the order and judge once more; the Head confirms the remaining items directly.

**Judgment**: the Head verifies every finding against primary evidence and sorts it into `findings.md` (confirmed defect / plausible risk / unfounded concern / resolved). Only a reproduced or proven finding counts as a defect. Reviewers have no veto. Passing the neutral checklist or a technical review alone is **not** sufficient for acceptance. Send defects to a fix worker and re-run **only the affected lanes**. Up to 2 iterations; after that, escalate or report to the user. Note plausible risks in the final report.

## 6. Parallelism

- Reads (investigation, review, verification) are always parallel.
- Writes are parallel only when **all parallel-write gates** pass: 2+ independent deliverables · owned-file globs don't overlap · the interface is already locked in decisions.md · concurrent writers <= 4. Otherwise, use a single sequential writer.
- Stages are not barriers. As soon as one piece's investigation finishes, start that piece's production; as soon as production finishes, start that piece's verification.
- One writer per checkout. Split concurrent writers with `--worktree`. Shared files (config, lockfiles, schema) belong to a single worker.
- Merge worktrees one at a time, running build, typecheck, and the full test suite at each merge. Even if per-piece verification passed, run full verification again after integration.

## 7. Cleanup and reporting

- The run's lifetime is the standard. Keep panes alive across turns while a `/goal` is in progress. Resume with `orch resume --run <id>`.
- On completion (success, failure, cancellation, or early exit), call `orch run end`. Tell the user the removal confirmation. Report any leftover worktree along with the reason.
- **Close only panes you spawned.** `orch` closes only pane IDs recorded in its run, and refuses another Head's run. Never close a user's pane or another session's pane.
- If a previous session crashed and its Head is gone, pick up that run with `orch resume --run <id>` to clean it up, or use `orch gc --orphans`.
- The final report states the mode, the keys used (including fallbacks and escalations, marked as e.g. "gemini-medium-m substitution"), confirmed defects and their resolution, remaining plausible risks, and unknowns.

## 8. Accounts

Default is `same` (the Head's login). If many same-family workers need to split capacity, use `--account hook` with `ORCH_ACCOUNT_CMD`. An adapter for the cr registry ships with this skill at `scripts/orch-account-cr`; point `ORCH_ACCOUNT_CMD` at its absolute path (e.g. `~/.claude/skills/orch/scripts/orch-account-cr`) (it only reads the registry, picks the account with the most remaining capacity, and aligns that account's home with global instructions, skill and hook links, and Codex hook trust state). On capacity exhaustion, fallback tries a different account with the same key first.

## 9. Layout (enforced by orch)

The Head takes the full-height left third; workers stack top to bottom at equal height in the right two-thirds column. Every spawn uses `--no-focus`. **Don't steal the user's focus:** Herdr's `pane zoom` and `agent prompt` move the UI to the target tab, so don't call them directly (orch doesn't either). If a tab has a user pane, spawn stops (it never moves or closes it). If the layout breaks, use `orch layout`. If you need a pane for a plain command rather than delegation, prefix the command with `ORCH_RAW_HERDR=1` to get past the hook.
