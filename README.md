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
orch contract new impl --role produce --model codex-high-m      # fill in the template
orch spawn --model codex-high-m --name impl --role produce --purpose impl
orch prompt impl
orch wait impl                 # judged by the status line in the result file -> finished panes close immediately
orch spawn --model claude-medium-m --name crit --role verify-critical --reviews impl
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

| Key | CLI | Family | Model variable | Default model |
|---|---|---|---|---|
| `codex-medium-m` | codex, effort medium | OpenAI | `CODEX_MEDIUM_M` | `gpt-6-luna` |
| `codex-medium-h` | codex, effort high | OpenAI | `CODEX_MEDIUM_H` | `gpt-6-luna` |
| `codex-high-l` | codex, effort low | OpenAI | `CODEX_HIGH_L` | `gpt-6-sol` |
| `codex-high-m` | codex, effort medium | OpenAI | `CODEX_HIGH_M` | `gpt-6-sol` |
| `claude-high-m` | claude, effort medium | Anthropic | `CLAUDE_HIGH_M` | `opus` |
| `claude-medium-m` | claude, effort medium | Anthropic | `CLAUDE_MEDIUM_M` | `sonnet` |
| `gemini-medium-m` | agy | Google | `GEMINI_MEDIUM_M` | `gemini-3.8-flash-medium` |

Keys follow `<cli>-<tier>-<effort initial>` (`h`/`m`/`l`), where the tier is the model's rank in its vendor lineup, and each key's variable is the key upper-cased with underscores. When a CLI ships a new model, set the variable in the Head's shell (for example `export CODEX_HIGH_M=gpt-7`); no rebuild is needed. Keys and efforts stay the same; `orch models` shows the resolved arguments.

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
