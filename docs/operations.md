# Local evidence, experiments and removal

No Head savings have been demonstrated. Existing ask/do logs do not cover Head or pane usage; do not infer historical missing values.

## Explicit measurement

`orch measure import < record.json` writes one versioned record to `~/.orch/measurements/records.jsonl` (or ORCH_HOME), with a file lock, fsync and duplicate/conflict checks. `orch measure report` groups by session, period, subject, phase, measured/estimate/missing and coverage. Each group retains entity, source and optional model identity, and shows n_total/n_with_value/n_missing plus observed duration subtotals and duration coverage; observed subtotals are partial evidence, never a complete total when values are missing. Different sources or phases must not cover overlapping usage; IDs deduplicate the same observation, not arbitrary overlapping intervals. Use provider event IDs and nonoverlapping period identifiers. Do not import a cumulative session total alongside its component events as separate usage.

Example unavailable Head observation:

```json
{"version":1,"id":"session-1-contract","session":"session-1","period":"turn-1","subject":"head","entity":"head","phase":"contract","source":"manual","kind":"missing","coverage":"unavailable","input_tokens":null,"output_tokens":null,"duration_ms":null}
```

For actual provider evidence, use kind `measured`, source identifying the provider/export, null for unavailable counts, coverage `partial` or `complete`. Estimates use kind `estimate` and stay in separate groups. Zero means an observed zero. Supported subjects are head/pane/task; phases are contract/execution/review/rework. Manual explicit export/import is the supported provider-independent path; there is no automatic transcript parser or fabricated historical backfill. Session and period identities are required, even for missing usage. Coverage is reporter-declared and must match the evidence actually observed.

Optional event codes: new_defect, false_positive, accepted_fix, failure, recovery_started, recovered, recovery_failed. Reason codes: unknown, timeout, interrupted, exit, scope, persistence, capacity, quota, validation, human_confirmed. accepted_fix/recovered/recovery_failed require `related` identifying an existing record in the same session. Give a finding and its fix/recheck distinct IDs. The record is a structured assertion; keep reproduction/proof and acceptance judgment in run findings/decisions, linked by ID. A worker finding alone is not a confirmed defect.

Completed ask/do/test attempts automatically record the CLI-reported tokens as partial execution coverage (or null when unavailable), duration and a failure reason code. Cache hits reuse prior task evidence and create no new token observation. Pane completions record missing usage and executor failure outcomes. Fallback records recovery_started before prompting, then recovered/recovery_failed when the replacement passes/fails the executor completion gate; this is executor recovery, not user acceptance. Confirmed defects, false positives, accepted fixes and human recovery judgments require explicit evidence import. No automatic Head/pane usage is available. Telemetry contains only identities, numeric values and fixed codes, with no prompt or freeform secret fields. Identifiers must not contain secrets. Malformed logs fail closed and are preserved for repair; import never resets them. Records remain local and uninstall preserves them.

## Repeated experiments

```bash
python3 eval/run.py selfcheck
python3 -m unittest discover -s eval -p 'test_*.py'
python3 eval/run.py schedule --only delegate --seed 42 --repetitions 4
python3 eval/run.py run --only delegate --dry-run --seed 42 --repetitions 4
python3 eval/run.py schedule --only duration --duration-bucket 1 --duration-bucket 5 --duration-bucket 15
# Live runs incur provider usage; schedule/dry-run never call a model.
python3 eval/run.py run --only duration --repetitions 4 --seed 42
```

Each fixture/repetition has both arms with alternating first-arm order and seeded pair shuffling. Odd repetition counts differ by one first-arm sample. A is a **top-tier worker proxy**, B a lower-key worker. Actual Head observations remain null; B's character-based instruction/report/review estimate has separate fields. Weighted cost is an assumption, not money or measured Head cost. All ask attempts bypass cache. Fresh workspaces and schedule/intent/raw/result/summary artifacts preserve every attempt, including failed/interrupted ones. The summary reports missing attempts, paired/unmatched counts, quality, failures and observed mean/spread. Duration attempts also retain raw DAG state, actual model identities, scheduler worker spans (with completeness counts), setup/first-start delay and cleanup results. Re-render interrupted experiments with `python3 eval/run.py report <directory>`; missing attempts stay missing. Inspect individual results for token estimates and provider evidence; no recommendation or savings claim follows from these proxies.

Duration workloads use increasing batches of parsing/algorithm implementation functions (3/15/45), single versus three pane workers with exclusive module names and isolated worktrees. Hidden cases check the same functions for both arms; no sleep simulates work. 1/5/15 minutes are target workload labels, not calibrated runtime claims. Actual elapsed time includes setup, scheduling, model work and integration; observed duration buckets have boundaries at 60/300/900 seconds. They cannot establish a break-even point without actual live observations. No expensive live benchmark is part of self-tests. Legacy review/parallel experiments remain available through the old runner.

## Safe uninstall and reinstall

`orch uninstall --repo /path/to/orch` and `--dry-run` preview without writes. `--apply` backs up every changed existing file/link under ORCH_HOME/backups before applying the plan. New installs retain a versioned local ownership manifest with repository, exact binary and block identity. Legacy installs without a manifest use the current block and standard binary paths. Only the exact installed generated constitution block, exact hook commands for that repository's bin/orch or ~/.local/bin/orch, and links targeting that repository's skill/playbooks/bin/orch are owned. Mixed hook groups retain user commands and other events. Modified blocks, wrappers, foreign links, real directories and nonregular config files are preserved. Ambiguous markers, malformed JSON and malformed hook shapes fail before mutations. Repeated removal is harmless; state/history/measurements and backups remain. Empty constitution files may remain; whitespace outside owned markers is preserved. JSON formatting can change; historical matcher values and Codex `/hooks` trust cannot be reconstructed. Remove trust manually. Backups are not restored over later user changes.

Reinstall preflights links and configurations and refuses foreign links, real directories and malformed config rather than overwriting them. Configuration writes use temporary-file rename. Files are snapshot-checked before uninstall mutation, but simultaneous external config writers are not transactionally locked; stop them while applying. An apply I/O failure may leave a partial removal; backups and idempotent retry are the recovery path.

## Head execution guard boundary

The Head only plans/evaluates/instructs/reviews/judges/communicates. There is no tiny-task or takeover exception. Tests/builds and Git/install execution go through orch workers or orch test; read-only inspection is permitted.

PreToolUse applies the practical execution redirect in an orch-managed context: ORCH_RUN, an active run owned by the current Head in Herdr, or `ORCH_HEAD_GUARD=1` in Herdr. Outside Herdr an explicit ORCH_RUN enables the same redirect. Set ORCH_HEAD_GUARD=1 for a direct-mode Herdr Head without a run. Workers are exempt from this execution redirect and keep their existing no-delegation guard; unrelated ordinary sessions are unaffected. A finished run is no longer an implicit managed context. Policy still applies outside enforcement coverage.

Recognized edit/patch tools are denied except contracts/**, dag.yaml, acceptance.md, decisions.md and findings.md under the selected run, and goal.md in the current Head's newest draft goal. Frozen goal state and implementation files remain protected. Only exact known edit API names exclude their bodies from shell inspection; missing edit paths fail closed. Paths resolve symlinks. Known build/test/interpreter/package commands, mutating Git subcommands, common file operations and output redirections in recognized shell tools are redirected. Read-only cat/rg/git status/diff and orch orchestration pass. Quoted orch requirements/test commands are argument data, while shell substitutions, redirections and chained recognized execution remain inspected. ORCH_RAW_HERDR bypasses only the pane-creation redirect. Unknown tool names, shell aliases/functions, opaque scripts and arbitrary language/shell syntax are not comprehensively parsed. This is a workflow guard, not a security sandbox or proof that arbitrary execution is impossible. A user can alter environment/hook configuration; that changes enforcement, not policy.

Exact MCP filesystem names `mcp__filesystem__write_file` and `mcp__filesystem__edit_file` receive Head, active-goal scope and Codex goal-compilation path checks, including missing-path rejection. Path recognition does not grant a command-body exemption to MCP tools or terminal write APIs. Before masking quoted inspection arguments, the guard checks common awk `system`, print/printf redirects and pipes, and sed `w`/`e` commands and substitution flags. Ordinary awk comparisons, grep patterns and Git log formats remain inspection data. Known Go module mutations/get, find deletion/exec actions and explicit shell script invocations are also redirected. These checks cover the listed forms rather than a complete parser for either language or shell.

Test `fakeCLI` fixtures place failing sentinels for `codex`, `claude`, `agy`, `gemini` and `cr` ahead of the real PATH. Explicit mocks override them, including multiple mocks for provider fallback tests; missing or misspelled mocks fail locally while system tooling remains available.

## Provider failures and recovery

A provider process exiting zero does not establish success: nonempty denied_actions or an empty response fail the task and prevent ask caching, including reuse of older denied-result caches. Inspect output.log and resolve the specific permission before retrying; orch does not change global permissions. Structured provider envelopes are never substituted for an empty answer. Failed writing tasks still capture changed paths for scope review, but skip final tests and completion commits.

`do` requires a Git repository to freeze a baseline and validate owned paths. Non-Git run directories are planning artifacts: the Head can edit the exact permitted planning files directly; use --cwd with a repository for implementation work.

A known interrupted inline DAG command is stopped and persisted pending before the resume instruction is printed. An uncertain launch or interrupted scheduler without confirmed child termination still requires inspection and state reconciliation. Knowledge merge failures propagate and are retryable after repairing knowledge.md; facts are deduplicated, and finished panes are closed before merge attempts. Worktrees with missing/corrupt task.json remain protected as unverified writers: inspect the recorded process/pane and restore task state from backup before retrying run end. No automatic timeout deletes unknown writers. Already closed runs stay closed.

Automatic measurement failures produce warnings and do not change task/test/pane execution results or prevent successful ask caching. Required execution state persistence remains fatal. Explicit measure import/report keeps rejecting malformed logs and conflicting records; preserve and repair the evidence file before retrying. Evidence n_total counts rows, including recovery events, rather than workers or execution attempts. Duration coverage and token coverage are separate; n_missing tracks unavailable token pairs even when another metric is measured. A catalog key used when ModelID is unavailable is a fallback identity, not a provider observation.

Uninstall removes a matching ownership manifest last, after backing it up and applying the owned changes, so a later installation can use another repository. Without a manifest, shared-bin hooks and the common constitution block are removed only when ~/.local/bin/orch points to the requested repository's bin/orch. Ambiguous legacy ownership is preserved. JSON cleanup retains large integer values and literal HTML characters, while whitespace and key order may change.
