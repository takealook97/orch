# orch

`orch` is a multi-agent executor that runs on Herdr panes. The main session (the Head) hands subtasks to workers as contract files, then has a different model family verify the results.

The Head usually runs on the top-tier model, so orch delegates by cost, not time: the Head keeps judgment and review, and execution goes to cheaper catalog keys. One-off tasks skip panes entirely with `orch ask`/`orch do`; `orch test` keeps full test logs out of the Head's context.

- Setup guide: `docs/setup.md`
- Design doc: `skill/references/design.md`
- Decisions and changes made during implementation: `skill/references/decisions.md`
- Operating manual for the Head: `skill/SKILL.md` (skill name `orch`)

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

## Flow (Head)

```bash
orch run new --text "<original request>" --mode full --playbook feature
# write ~/.orch/runs/<id>/acceptance.md, decisions.md
orch contract new impl --role produce --model codex-high-m      # fill in the template
orch spawn --model codex-high-m --name impl --role produce --purpose impl
orch prompt impl
orch wait impl                 # judged by the status line in the result file -> finished panes close immediately
orch spawn --model claude-medium-m --name crit --role verify-critical --reviews impl
orch prompt crit && orch wait crit
orch run end                   # close all remaining panes and confirm removal
```

## One-off tasks (no pane)

```bash
orch ask --model codex-low-h "list the implementations of Store and their call paths"     # read-only, answer capped at 2000 chars
orch do  --model codex-high-m --own 'parser/**' --test "go test ./parser/..." "handle empty input in Parse"
orch do  --model codex-high-l --own 'docs/**' --background "draft the changelog for v0.4"      # returns task-N at once
orch wait task-2                                                                             # same report when it finishes
orch test "go test ./..."                                                                    # no model: exit code, failing names, capped excerpt
```

Flags go before the text; a text that starts with `-` goes after `--` (`orch ask --model codex-low-h -- "-1 or 1?"`).

`orch do` returns only status, changed files, diff stat, owned-scope violations (a violation fails the task), orch's own final test run, and a capped summary; the full diff, answer, and logs stay in `~/.orch/runs/<id>/tasks/task-N/`. No `orch run new` is needed. Children run with `ORCH_ROLE=worker`, so they cannot delegate further. Add `--json` for a machine-readable report.

## DAG and effect measurement

```bash
# write nodes (contracts) and dependencies into ~/.orch/runs/<id>/dag.yaml
orch dag check && orch dag run      # spawns ready nodes in critical-path order; finished panes close immediately
                                    # run: nodes are model-less gates, pane: false nodes go through orch ask (no row)
orch checklist --shuffle          # neutral-lane checklist template (full+ order shuffle)

python3 eval/run.py selfcheck   # self-check the eval set
python3 eval/run.py run         # 20 tasks: cross- vs same-family review, parallel vs single implementation -> eval/out/<timestamp>/report.md
python3 eval/run.py run --only delegate   # Head direct (claude-high-m) vs lower-key orch ask/do: Head tokens, total cost, quality, rework, time
```

## Account (optional)

Pane workers default to `--account auto`: Codex and Claude launch through the resolved `cr codex` / `cr claude` executable when CR is installed. Orch supplies the catalog model and effort, Codex inline-screen flags, and worker context; CR selects and rotates accounts. Inherited `CR_LAUNCH_EMAIL` is cleared, and `CR_HOME` is carried through. Other providers and systems without CR use native startup. `--account cr` requires CR and a supported provider; explicit `--account same` passes the Head's login to the native CLI, and `--account hook` uses `ORCH_ACCOUNT_CMD`. One-off `orch ask` / `orch do` still default to `same`.

CR launches keep unfocused owned panes, assign the worker name after detection, and check registration, readiness, and folder trust. Failed launches clean up their owned pane. Model fallback stays on CR for supported providers and leaves account rotation to CR; it does not invoke the legacy account hook. To select accounts through the optional hook instead, wire an adapter into `ORCH_ACCOUNT_CMD` and use `orch spawn --account hook`. An adapter for the cr registry ships inside the skill at `skill/scripts/orch-account-cr`. With registry priority `custom`, the hook selects usable accounts by ascending nonzero rank (zero or missing ranks last), then email local part and full email, regardless of capacity mode. Other priorities retain the legacy headroom order: minimum of 5h and weekly remaining descending, then weekly remaining descending. It only reads the cr registry, and it aligns the chosen account's home with global instructions, skill and hook links, and Codex hook trust state (it never overwrites existing files).

## Model catalog

`orch models` shows the full list. The Head picks by key only; `orch` assembles the CLI arguments.

| Key | CLI | Family | Model variable | Default model |
|---|---|---|---|---|
| `codex-low-h` | codex, effort high | OpenAI | `CODEX_LOW_H` | `gpt-6-luna` |
| `codex-medium-m` | codex, effort medium | OpenAI | `CODEX_MEDIUM_M` | `gpt-6.1-terra` |
| `codex-high-l` | codex, effort low | OpenAI | `CODEX_HIGH_L` | `gpt-6.1-sol` |
| `codex-high-m` | codex, effort medium | OpenAI | `CODEX_HIGH_M` | `gpt-6.1-sol` |
| `claude-high-m` | claude, effort medium | Anthropic | `CLAUDE_HIGH_M` | `opus` |
| `claude-medium-m` | claude, effort medium | Anthropic | `CLAUDE_MEDIUM_M` | `sonnet` |
| `gemini-medium-m` | agy | Google | `GEMINI_MEDIUM_M` | `gemini-3.8-flash-medium` |

Keys follow `<cli>-<tier>-<effort initial>` (`h`/`m`/`l`), where the tier is the model's rank in its vendor lineup, and each key's variable is the key upper-cased with underscores. When a CLI ships a new model, set the variable in the Head's shell (for example `export CODEX_HIGH_M=gpt-7`); no rebuild is needed. Keys and efforts stay the same; `orch models` shows the resolved arguments.

The seven canonical entries include the explicit OpenAI hierarchy **Luna < Terra < Sol**, independent of reasoning effort: Luna uses high, Terra medium, and Sol low or medium. `codex-medium-h` remains a deprecated alias for Luna (`codex-low-h`). `CODEX_LOW_H` wins over the legacy `CODEX_MEDIUM_H`; the legacy override is consulted only when the canonical value is empty after trimming. Saved runs need no migration. Terra uses the independent `CODEX_MEDIUM_M` override.

Use Luna for broad cheap lookup and extraction, Terra for constrained writing and scoped code fixes, and Sol for larger planned implementation and general debugging. Screen work is a candidate Terra role, not benchmark-proven superiority. Creative writing and deep document work keep the Sonnet/Opus defaults; Gemini remains the independent neutral-review default.

## Layout

```
+--------------+-----------------------------+
|              | worker 1                    |
|   Head       +-----------------------------+
|   1/3        | worker 2                    |
|   full       +-----------------------------+
|   height     | worker 3 (same height)      |
+--------------+-----------------------------+
```

`orch` only closes panes it spawned. If a tab has a user pane, it stops spawning.

The Head tab holds at most terminal height / `ORCH_MIN_ROW_HEIGHT` (default 2) worker rows (minimum 1, maximum `ORCH_MAX_ROWS`, default 20). Additional workers use unfocused overflow tabs, stacked in equal full-width rows with the same per-tab row cap. `ORCH_MAX_WORKERS` sets the live pane-worker cap per run (positive integer, default 12); ready DAG workers queue at that cap. Goal admission budgets still apply separately. `orch layout` rebalances all owned tabs, and closing the last worker in an overflow tab removes that tab.

A finished worker's pane closes even if the Head never calls `orch wait`: `orch spawn` starts one reaper per run (`orch __reap`, log in `~/.orch/runs/<id>/reaper.log`) that closes a pane once its result file has a status line and the agent has stopped working, then exits when the run has no live workers.

## Development

```bash
make check       # go vet, go test, playbook schema check
git config core.hooksPath .githooks
```

Commits use an English `type: imperative summary` subject of at most 72 characters. The local hook enforces the format. Allowed types: `feat`, `fix`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `perf`, `security`, `revert`.

## Goals

Codex 0.162 skips UserPromptSubmit for the native `/goal <objective>` command. Its PreToolUse guard reads goal events from the rollout transcript and blocks the first tool call until an orch goal is frozen for the Head pane; a draft does not suffice. During compilation it permits orch goal/help/models/status commands and edits under ORCH_HOME. The `codex-prompt` hook still covers plain prompts that start with `/goal` text. Claude transcripts have no such goal events and are unaffected.

Long `/goal` sessions can use a frozen criteria list and a budget. Start with `orch goal new --text "<goal verbatim>"`, fill `goal.md` with 1–8 numbered criteria and a `- check: CMD` for each, non-goals, absolute Scope globs, and hours/max-workers/fix-rounds budgets. Then run `orch goal freeze`.

Every spawn/do needs `--serves N[,M]`; fixes use `--fix-of SHARD` (or spawn names `fix-api` / `fix2-api`) and default to two rounds per shard. New runs link automatically to the active goal of the Head pane. Discoveries outside the list go to `orch goal note "..."`, not more work. Inspect progress with `orch goal status` and finish with `orch goal check`: exit 0 means met, 1 checks failed, 2 bad file/no active goal, 3 budget stopped, and 4 criteria tampered. At the budget stop, report results and backlog and ask the user before `orch goal extend --hours N --max-workers N --reason "user: ..."`. `orch goal end` prints the backlog.

Claude and Codex prompt hooks provide goal setup context for matching prompts and restrict Claude edit tools and Codex `apply_patch` edits (including shell heredoc patches) to the goal's Scope, allowing orch/Claude configuration and temporary files. Trust the new `codex-prompt` hook once in Codex `/hooks`. Other Bash writes remain the Head's responsibility; agy has no prompt hook. See the skill's Goals section for the full file format and flow.
