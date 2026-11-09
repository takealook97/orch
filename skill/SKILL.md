---
name: orch
description: Head's operating manual for delegation and independent verification through orch. Keep the Head on judgment; execute through catalog workers and orch test.
---

# orch entry point

## Role and standing gates

If `ORCH_ROLE=worker`, stop reading this guide: follow only the supplied contract, do not delegate, and write its result file with status added last.

The Head only plans, evaluates, instructs, reviews, judges and communicates. All execution, including tiny edits, research execution, builds, tests, Git, installation and integration mechanics, goes through orch workers or orch test. Read-only inspection and writing run contracts/DAG/acceptance/decisions/findings and draft goal plans support judgment. There is no takeover exception. When judgment is unresolved, decide first and issue revised instructions.

Spend Head tokens/context first, total tokens second, time third. Delegate execution to cheaper catalog keys regardless of length; length selects the channel. Use only the seven canonical keys from `orch models`; do not inherit CLI model/effort defaults or set models directly. The manifest is `models.yaml`; the generated full catalog lives in the repository README. Overrides and selection rules are in the advanced guide.

Head reads stay in context and are re-read on every later turn. Send investigation to `orch ask`; read files directly only when a judgment needs the exact code, and read only the needed range.

Inside Herdr, delegate only through orch; never use built-in subagent/Workflow tools or drive agent panes directly. `ORCH_MUX=herdr|app` overrides detection; otherwise `HERDR_ENV=1` chooses legacy, then `HC_TERMINAL_ID` chooses app. Outside both, ask/do/test still work but panes do not. Runs cannot cross backends.

External writes (deploy, payment, ordering, publishing) require separate authorization. Bypass flags waive local sandbox/approval prompts only; they do not waive this gate or repository gates.

With an active goal, work only for a criterion (`--serves`), log anything else with `orch goal note`, and stop/report at the budget. Compile and freeze a new `/goal` before execution.

## Choose the lightest mode

Estimate within 30 seconds. E is Head judgment effort; F is files/modules; P is independent subtasks; U is pinned acceptance; R is immediate reversibility.

| Mode | Threshold | Required flow |
|---|---|---|
| direct | E <=10 calls/5min, F <=2 in one module, P <=1, U pinned, R immediate, no high risk | ask/do; test if needed; no run ceremony |
| light | Exceeds direct, F <=5 in 1–2 modules, U pinned, R possible, no high risk | Producer plus independent review pane; mechanical checks |
| full | F >5 or 3+ modules, P >=3, U needs research, or R cumbersome | Investigate, Head design, produce, verify, integrate |
| full+ | Any high-risk marker | full plus design challenge, two critical lanes from different families, adversarial lane |

High risk: auth/permissions/secrets/encryption; deletion/migration/schema/backup; money/billing/usage calculations; core concurrency; production config/deploy pipeline/published output; request for thorough complete verification.

Before selecting full/full+, read [advanced procedures — modes, contracts and verification](references/advanced.md). Upgrade when risk or scope grows; downgrade and close unneeded workers when scope shrinks. Record mode changes with a reason.

## Select the channel

Before new research, use `orch recall "query"`; use `--all-repos` when needed. Ask caches successful identical questions; `--fresh` bypasses the cache.

| Work | Channel |
|---|---|
| Bounded read-only question, expected under 5 minutes | `orch ask --model <key> "question"` |
| Bounded execution under 5 minutes, with exclusive owned globs | `orch do --model <key> --own 'paths/**' "instructions"` |
| Only test/build pass/fail is needed | `orch test "command"` |
| 5+ minutes, unknown duration, parallel work, user-visible progress, every independent review | Contract and `orch spawn --model <key>`, then prompt/wait |

A `hint:` on ask/do means the channel was wrong; use panes for that kind of task next time. When unsure, use a pane. Long read-only reviews also require panes. Read [advanced procedures — channels and worker selection](references/advanced.md) before choosing workers, fallback, escalation or recovery.

## Production and verification

Write finite acceptance criteria and Head decisions before production. Contracts specify scope, permissions, plan, tests and result format. Workers do not resolve missing Head design decisions. Read [advanced procedures — contracts and DAG](references/advanced.md) before creating a run, contract, DAG or goal.

One writer per checkout. Parallel writes require independent deliverables, disjoint globs, a locked interface, worker-cap compliance, and isolated worktrees; shared configuration belongs to one writer. If isolation is unavailable, keep writes serial. Read [advanced procedures — parallelism and integration](references/advanced.md) before parallel writes or merging worktrees. Integration must be verified again.

An author cannot independently verify their own work. Critical verification must use another model family. Gemini can never be the sole verifier or the adversarial reviewer. Tests/self-review support evidence; they are not independent review.

Mechanical checks gate other reviews. Light uses critical review for code (different family), neutral for docs/UI; when both apply choose critical. Full requires critical and neutral when a user-facing surface changed. Full+ adds the second critical and adversarial lanes. Every independent lane uses a fresh pane. Read [advanced procedures — verification lanes and adjudication](references/advanced.md) before assigning or accepting reviews.

Accept findings only when reproduced or proven, not by model vote. Record confirmed defects, plausible risks, unfounded concerns and resolutions in findings.md. Fix confirmed defects and rerun affected checks/lanes. At most two fix iterations at one plan; then revise the design, resolve ambiguity/environment, or escalate once with revised instructions.

## Mandatory operational references

- Before accounts, capacity retry, fallback or layout work: read [advanced procedures — accounts, recovery and layout](references/advanced.md).
- Before goal compilation, budgeting, admission or resumption: read [advanced procedures — goals](references/advanced.md).
- Before measuring usage, interpreting savings, or uninstall/reinstall: read [operations](references/operations.md). Missing coverage is never zero; provider identity is not proof of Head ownership.
- For technical rationale read [design](references/design.md); [decisions](references/decisions.md) wins over superseded design.

## Cleanup and report

Keep panes across active-goal turns. Close finished or stalled/unneeded owned workers promptly; wait/collect close finished panes automatically. Close only panes orch recorded for this run, never the user's or another Head's. Use unfocused placement; do not zoom or prompt panes directly.

At task/goal completion, failure, cancellation or early exit, call `orch run end` and report its removal confirmation. Report retained worktrees and their reason; do not remove unmerged/foreign worktrees. Read [advanced procedures — cleanup](references/advanced.md) before recovering orphaned runs or worktrees.

Report mode, keys/fallbacks/escalations, confirmed defects and fixes, plausible risks, unknowns and mechanical evidence. A successful technical review alone is insufficient for acceptance.
