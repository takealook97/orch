# orch

`orch` is a multi-agent executor that runs on Herdr panes. The main session (the Head) hands subtasks to workers as contract files, then has a different model family verify the results.

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
orch contract new impl --role produce --model sol-m      # fill in the template
orch spawn --model sol-m --name impl --role produce --purpose impl
orch prompt impl
orch wait impl                 # judged by the status line in the result file -> finished panes close immediately
orch spawn --model sonnet-m --name crit --role verify-critical --reviews impl
orch prompt crit && orch wait crit
orch run end                   # close all remaining panes and confirm removal
```

## DAG and effect measurement

```bash
# write nodes (contracts) and dependencies into ~/.orch/runs/<id>/dag.yaml
orch dag check && orch dag run      # spawns ready nodes in critical-path order; finished panes close immediately
orch checklist --shuffle          # neutral-lane checklist template (full+ order shuffle)

python3 eval/run.py selfcheck   # self-check the eval set
python3 eval/run.py run         # 20 tasks: cross- vs same-family review, parallel vs single implementation -> eval/out/<timestamp>/report.md
```

## Account (optional)

By default `orch` passes the Head's login to workers. To split capacity, wire an adapter into `ORCH_ACCOUNT_CMD` and use `orch spawn --account hook`. An adapter for the cr registry ships inside the skill at `skill/scripts/orch-account-cr`. It only reads the cr registry, and it aligns the chosen account's home with global instructions, skill and hook links, and Codex hook trust state (it never overwrites existing files).

## Model catalog

`orch models` shows the full list. The Head picks by key only; `orch` assembles the CLI arguments.

| Key | CLI | Family |
|---|---|---|
| `luna-m` `luna-h` | codex (gpt-6-luna medium/high) | OpenAI |
| `sol-l` `sol-m` | codex (gpt-6-sol low/medium) | OpenAI |
| `opus-m` `sonnet-m` | claude (opus medium, sonnet medium) | Anthropic |
| `flash` | agy (gemini-3.8-flash-medium) | Google |

When a CLI ships a new model, point the keys at it without rebuilding: set `ORCH_LUNA_MODEL`, `ORCH_SOL_MODEL`, `ORCH_OPUS_MODEL`, `ORCH_SONNET_MODEL`, or `ORCH_FLASH_MODEL` in the Head's shell (for example `export ORCH_SOL_MODEL=gpt-7-sol`). Keys and efforts stay the same; `orch models` shows the resolved arguments.

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

## Development

```bash
make check       # go vet, go test, playbook schema check
```
