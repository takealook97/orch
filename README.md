# orch

`orch` is a multi-agent executor that runs on legacy Herdr and Herdr.app panes. The main session (the Head) hands subtasks to workers as contract files, then has a different model family verify the results.

The Head usually runs on the top-tier model, so orch delegates by cost, not time: the Head keeps judgment and review, and execution goes to cheaper catalog keys. One-off tasks skip panes entirely with `orch ask`/`orch do`; `orch test` keeps full test logs out of the Head's context.

- Setup guide: `docs/setup.md`
- Design doc: `skill/references/design.md`
- Decisions and changes made during implementation: `skill/references/decisions.md`
- Operating manual for the Head: `skill/SKILL.md` (skill name `orch`)

Detection: `ORCH_MUX=herdr|app` overrides `HERDR_ENV=1` (legacy), then `HC_TERMINAL_ID` (app). Herdr.app requires `hc`, a running hc server, and `herdr-app`; legacy requires `herdr`. Runs retain their backend. Both preserve focus and protect foreign panes.

## Four layers

| Layer | Location | Role |
|---|---|---|
| (1) Constitution | `constitution/orch-block.md` -> `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.gemini/GEMINI.md` | Role determination (Head/worker) and standing rules |
| (2) Executor | `orch` (single Go binary) | Model catalog, pane lifecycle and layout, contract/result files, fallback, cleanup |
| (3) Hook | `orch hook claude\|codex\|agy` | Blocks re-delegation by workers and the Head's built-in subagents inside Herdr |
| (4) Playbooks | `playbooks/*.yaml` -> `~/.orch/playbooks` | Stages and lanes per task type |

## Install

Full guide (prerequisites, verification, accounts, update, rollback): [`docs/setup.md`](docs/setup.md).

```bash
make install     # check(vet, test, playbook check) -> link ~/.local/bin/orch -> orch install (backup, then install constitution/hook/skill/playbooks)
orch doctor
```

`orch install` first backs up any file it will change to `~/.orch/backups/<timestamp>/`. After installing, if you use Codex as the Head, you must trust the new hook once on the `/hooks` screen before it runs.

## Start with direct or light

The Head plans, evaluates, instructs, reviews, judges and communicates. Execution goes through orch workers or orch test, including tiny edits, Git, builds and installation. Read-only inspection and run contract/acceptance/decision writing support judgment. **direct** means lightweight orchestration with ask/do/test. **light** adds an independent review pane.

## One-off tasks (no pane)

```bash
orch ask --model codex-low-h "list the implementations of Store and their call paths"     # read-only, answer capped at 2000 chars
orch do  --model codex-high-m --own 'parser/**' --test "go test ./parser/..." "handle empty input in Parse"
orch do  --model codex-high-l --own 'docs/**' --background "draft the changelog for v0.4"      # returns task-N at once
orch wait task-2                                                                             # same report when it finishes
orch test "go test ./..."                                                                    # no model: exit code, failing names, capped excerpt
```

Flags go before the text; a text that starts with `-` goes after `--` (`orch ask --model codex-low-h -- "-1 or 1?"`).

Successful `orch do`/`orch ask`/`orch test` text reports start with `done <task> <kind> key=<key> <seconds>s` (seconds to one decimal), adding `test=0`, `commit=<first 7 characters>`, and `files=<n> +<adds> -<deletions>` when present. The answer follows for ask/do, then one `dir: <task dir>` line; successful tests have no answer section. Failures, scope violations, hints, and nonzero tests keep the full report. The full diff, answer, and logs stay in `~/.orch/runs/<id>/tasks/task-N/`. No `orch run new` is needed. Children run with `ORCH_ROLE=worker`, so they cannot delegate further. `--json` is unchanged.

## Local statistics and review contracts

`orch stats [--since <duration>] [--json]` reads local `runs/*/tasks/*/task.json` (ask/do/test) and `run.json` pane workers without changing files or triggering disk cleanup. The default window is `168h`. Statuses outside the done and failed groups (such as working, closed and running) count only toward n. Records are grouped by kind, catalog key and purpose when recorded, sorted by kind then descending count. Columns show record count, done, failed (including timeout, blocked, partial, quota, capacity, content-blocked and lost), records with test failures or scope violations, total capacity/content-block retries, fallback replacements, and mean recorded seconds. Missing values print `-` (JSON `null`); malformed records are counted in `skipped`. One-off fallbacks and pane test/scope outcomes are not recorded and remain unknown. Means use only available completed durations: task `duration_ms`, or pane `closed - started`. Filtering uses record `started`, falling back to run `created` for older tasks without start timestamps; if run metadata is unavailable, tasks fall back to their task.json modification time. Heartbeat times are never used. This reports retained local evidence; pruning can remove older records.

```bash
orch stats --since 336h
orch contract new crit --role verify-critical --model claude-medium-m --reviews impl --diff HEAD~1..HEAD --mech task-4 --focus "empty input" --run RUN_ID
```

`--reviews` generates a filled, read-only review contract and saves the nonempty diff as `evidence/<name>.diff`. `--cwd` selects the repository; otherwise the reviewed worker's recorded worktree, then cwd, then the current directory is used. `--mech` accepts a task id (resolved to this run's `tasks/<id>/test.log`) or an existing file path. Critical reviews require severity-sorted defects with file:line and repro evidence (top 10); neutral reviews embed the acceptance checklist; adversarial reviews require `--focus` and seek a refutation or counterexample. Focus text is included in every role's plan. Without `--reviews`, the existing editable template is unchanged.

## Advanced operations

See [advanced procedures](skill/references/advanced.md) for full/full+, DAG, accounts and layout; [measurement, eval, uninstall and guard boundaries](docs/operations.md) for local evidence and safe removal. Savings have not been demonstrated.

Claude Stop can collect Head usage automatically with phase `session`; `ORCH_AUTO_MEASURE=0` opts out and `~/.orch/measure.log` records errors. See [Automatic Head collection](docs/operations.md#automatic-head-collection) for installation and partial-coverage limits.

## Disk retention

`orch gc [--orphans] [--disk]` also prunes old disk data. Use `orch gc --disk` for disk cleanup alone without pane access; `--disk` and `--orphans` cannot be combined. Head invocations automatically launch detached disk cleanup at most once per 24 hours, with output in `~/.orch/prune.log`.

`ORCH_RETENTION_DAYS` sets the retention window in whole days (default 7; invalid values use 7; 0 disables disk pruning). `ORCH_AUTO_PRUNE=0` disables only the automatic trigger. Old closed runs and ended goals retain metadata, Markdown at every depth, task records and lock files; heavy evidence and scratch data are removed. Old ad-hoc runs are deleted entirely. Active or recently updated runs/goals, runs holding worktrees or unfinished/live tasks, and ended goals referenced by non-closed runs are protected. Runs linked to a goal are compacted only when that goal is ended or missing. Compaction markers prevent repeat walks until new activity occurs.

Old `tests/*.log`, `codex-goal-cache/*.json` and `cache/ask/*.json` files are removed; ask entries retain at least their configured cache TTL. Symlinks are never followed during cleanup. Measurements, backups, locks and hook-debug logs are outside this policy. Workers should keep toolchains, virtualenvs, package/module caches and build outputs in `$TMPDIR` outside `~/.orch`, with only small logs/excerpts under `evidence/`.

## Model catalog

`orch models` shows the full list. `orch models --json` emits the resolved catalog as objects with `key`, `kind`, `family`, `tier`, `model`, `effort`, and `display` fields. The Head picks by key only; `orch` assembles the CLI arguments.

<!-- orch:models:begin -->
| Key | CLI | Family | Tier | Default model | Effort | Model variable |
|---|---|---|---|---|---|---|
| `codex-low-h` | codex | openai | Luna | `gpt-6-luna` | high | `CODEX_LOW_H` |
| `codex-medium-m` | codex | openai | Terra | `gpt-6.1-terra` | medium | `CODEX_MEDIUM_M` |
| `codex-high-l` | codex | openai | Sol | `gpt-6.1-sol` | low | `CODEX_HIGH_L` |
| `codex-high-m` | codex | openai | Sol | `gpt-6.1-sol` | medium | `CODEX_HIGH_M` |
| `claude-high-m` | claude | anthropic | Opus | `opus-5.5-medium` | — | `CLAUDE_HIGH_M` |
| `claude-medium-m` | claude | anthropic | Sonnet | `sonnet-5.5-medium` | — | `CLAUDE_MEDIUM_M` |
| `gemini-medium-m` | agy | google | Gemini | `gemini-3.8-flash-medium` | — | `GEMINI_MEDIUM_M` |

OpenAI tiers: Luna < Terra < Sol (independent of effort). Deprecated alias: codex-medium-h -> codex-low-h. CODEX_LOW_H takes precedence over CODEX_MEDIUM_H.

Defaults are generated from `models.yaml`. Override shared model ids in `~/.orch/models.yaml` (or `ORCH_MODELS_FILE`), for example `models: {sol: new-model-id}` updates both Sol keys. An overlay may also set per-key effort with `keys: [{key: codex-high-l, effort: low}]`; other fields are rejected. Per-key environment variables override overlay model ids. `orch models` shows resolved ids and their source.
<!-- orch:models:end -->

Use Luna for broad cheap lookup and extraction, Terra for constrained writing and scoped code fixes, and Sol for larger planned implementation and general debugging. Screen work is a candidate Terra role, not benchmark-proven superiority. Creative writing and deep document work keep the Sonnet/Opus defaults; Gemini remains the independent neutral-review default.

## Development

```bash
make check       # go vet, go test, playbook schema check
git config core.hooksPath .githooks
```

Commits use an English `type: imperative summary` subject of at most 72 characters. The local hook enforces the format. Allowed types: `feat`, `fix`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `perf`, `security`, `revert`.

## Goals

Codex 0.162 skips UserPromptSubmit for the native `/goal <objective>` command. Its PreToolUse guard reads goal events from the rollout transcript and blocks the first tool call until an orch goal is frozen for the Head pane; a draft does not suffice. During compilation its goal-specific rule permits orch goal/help/models/status commands and edits under ORCH_HOME; the managed Head execution guard independently restricts direct edits to run contracts/dag.yaml/acceptance/decisions/findings and the current Head's newest draft goal.md. The `codex-prompt` hook still covers plain prompts that start with `/goal` text. Claude transcripts have no such goal events and are unaffected.

Long `/goal` sessions can use a frozen criteria list and a budget. Start with `orch goal new --text "<goal verbatim>"`, fill `goal.md` with 1–8 numbered criteria and a `- check: CMD` for each, non-goals, absolute Scope globs, and hours/max-workers/fix-rounds budgets. Then run `orch goal freeze`.

Every spawn/do needs `--serves N[,M]`; fixes use `--fix-of SHARD` (or spawn names `fix-api` / `fix2-api`) and default to two rounds per shard. New runs link automatically to the active goal of the Head pane. Discoveries outside the list go to `orch goal note "..."`, not more work. Inspect progress with `orch goal status` and finish with `orch goal check`: exit 0 means met, 1 checks failed, 2 bad file/no active goal, 3 budget stopped, and 4 criteria tampered. At the budget stop, report results and backlog and ask the user before `orch goal extend --hours N --max-workers N --reason "user: ..."`. `orch goal end` prints the backlog.

Claude and Codex prompt hooks provide goal setup context for matching prompts and restrict Claude edit tools and Codex `apply_patch` edits (including shell heredoc patches) to the goal's Scope, allowing orch/Claude configuration and temporary files. Trust the new `codex-prompt` hook once in Codex `/hooks`. The managed Head execution guard additionally redirects recognized execution and direct edits; its precise coverage is documented in [operations](docs/operations.md). agy has no prompt hook. See the skill's Goals section for the full file format and flow.
