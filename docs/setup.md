# Setup

This guide takes a machine from nothing to a working `orch` Head. It covers prerequisites, installation, verification, optional account splitting, updates, and rollback.

## 1. Prerequisites

| Requirement | Why | Check |
|---|---|---|
| macOS or Linux with a POSIX shell | `orch` shells out to `herdr`, `git`, `ps`, `sh` | — |
| Go 1.27+ | builds the `orch` binary | `go version` |
| Herdr (tested with 0.8.2) | every worker runs in a Herdr pane | `command -v herdr` |
| `git` | contract scope checks and changed-file detection | `git --version` |
| `codex` CLI, logged in | runs `luna-m` `luna-h` `sol-l` `sol-m` | `command -v codex` |
| `claude` CLI, logged in | runs `opus-m` `sonnet-m` | `command -v claude` |
| `agy` CLI, logged in | runs `flash` | `command -v agy` |
| `python3` (optional) | only for the cr account adapter and the eval harness | `python3 --version` |
| `~/.local/bin` on `PATH` | `orch` is linked there | `echo $PATH` |

You can install with only some of the model CLIs, but keys whose CLI is missing cannot be spawned. Install all three for full catalog coverage.

`orch` does not depend on any other skill. The `herdr` or `hd` skills may be installed alongside it, but the Head must not use them to drive panes; the hook blocks direct `herdr pane` calls.

## 2. Install

```bash
git clone https://github.com/takealook97/orch.git ~/IdeaProjects/orch
cd ~/IdeaProjects/orch
make install
```

`make install` runs, in order:

1. `make check`: `go vet`, `go test`, and a playbook schema check.
2. `make link`: builds `bin/orch` and symlinks `~/.local/bin/orch` to it.
3. `orch install --repo <repo>`: backs up every file it is about to touch into `~/.orch/backups/<timestamp>/`, then installs the four layers.

| Layer | Target | How |
|---|---|---|
| Constitution | `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.gemini/GEMINI.md` | Inserts or replaces the block between `<!-- orch:begin ... -->` and `<!-- orch:end -->`. Everything outside the markers is left alone |
| Hooks | `~/.claude/settings.json` (PreToolUse, matcher `Agent\|Task\|Workflow\|Bash`), `~/.codex/hooks.json`, `~/.gemini/config/hooks.json` (`orch-guard`) | Adds or updates one `orch hook <cli>` entry per CLI. Other hooks are kept |
| Skill | `~/.claude/skills/orch`, `~/.codex/skills/orch` | Symlinks to `<repo>/skill`. The skill is self-contained: `SKILL.md`, `references/design.md`, `references/decisions.md`, `scripts/orch-account-cr` |
| Playbooks | `~/.orch/playbooks` | Symlink to `<repo>/playbooks` |

Preview without writing anything:

```bash
orch install --repo ~/IdeaProjects/orch --dry-run
```

### Codex hook trust

Codex will not run a new or changed hook until you trust it. If you use Codex as the Head, open Codex once after installing, go to `/hooks`, and trust the `orch hook codex` entry.

## 3. Verify

```bash
orch doctor
```

`orch doctor` checks:

- the `herdr`, `codex`, `claude`, `agy`, `git` binaries are on `PATH`
- you are inside a Herdr session (`HERDR_ENV=1`)
- you are not a worker (`ORCH_ROLE` is not `worker`)
- each constitution file contains the orch block and it matches `constitution/orch-block.md`
- each CLI's hook file references `orch hook <cli>`
- both skill links resolve to a directory with `SKILL.md`
- `~/.orch/playbooks` exists and passes the schema check
- the account mode (`same`, or the `ORCH_ACCOUNT_CMD` in use)

Run it from a pane inside Herdr; outside Herdr the "Herdr session" check fails by design.

Smoke test from a Head pane:

```bash
orch models                                   # the 7 catalog keys
orch run new --text "smoke test" --mode light
orch contract new probe --role recon --model luna-m
orch spawn --model luna-m --name probe --role recon --purpose explore
orch prompt probe && orch wait probe
orch run end                                  # closes every pane this run opened
```

## 4. Accounts (optional)

By default every worker uses the Head's login (`--account same`). To spread load across several accounts, point `ORCH_ACCOUNT_CMD` at an adapter and spawn with `--account hook`.

The bundled adapter reads the cr (Capacity Roller) registry. It never writes to cr.

```bash
export ORCH_ACCOUNT_CMD=~/.claude/skills/orch/scripts/orch-account-cr
orch doctor    # "account hook" should show the path
```

- Registry location: `$CR_HOME/registry.json`, default `~/.local/share/cr/registry.json`.
- It covers `codex` and `claude` accounts. `agy` (`flash`) always uses the Head's login.
- It picks the ready account with the most 5-hour and weekly headroom, and links the global instructions, skills, and the orch hook into that account's home without overwriting existing files.

Any other adapter works if it follows the same protocol: it is called as `<cmd> <kind> <comma-separated excluded labels>`, prints `KEY=VALUE` lines (environment for the worker pane) plus `LABEL=<id>`, and exits 1 when no account fits.

## 5. Environment variables

| Variable | Set by | Meaning |
|---|---|---|
| `ORCH_HOME` | you (optional) | state directory, default `~/.orch` (runs, locks, backups, playbooks) |
| `ORCH_ACCOUNT_CMD` | you (optional) | account adapter for `--account hook` |
| `ORCH_DEBUG=1` | you (optional) | verbose worker diagnostics on stderr |
| `ORCH_LUNA_MODEL`, `ORCH_SOL_MODEL`, `ORCH_OPUS_MODEL`, `ORCH_SONNET_MODEL`, `ORCH_FLASH_MODEL` | you (optional) | model id passed to `--model` for that model's keys, read by `orch spawn` in the Head's shell. Defaults: `gpt-6-luna`, `gpt-6-sol`, `opus`, `sonnet`, `gemini-3.8-flash-medium`. Keys and efforts stay fixed; `orch models` shows the resolved arguments |
| `ORCH_RAW_HERDR=1` | you, per command | lets one plain `herdr` command through the hook, for non-delegation panes such as builds or log tails. Close the pane when done |
| `ORCH_ROLE`, `ORCH_RUN`, `ORCH_WORKER`, `ORCH_MODEL` | `orch spawn` | injected into worker panes. Never set these in the Head |

## 6. Update

```bash
cd ~/IdeaProjects/orch
git pull
make install
orch doctor
```

The skill and playbooks are symlinks, so pulling updates them immediately. The constitution block and hooks are copies, so rerun `make install` after every pull; `orch doctor` reports "differs from source" when they are stale. If a hook changed, trust it again in Codex `/hooks`.

## 7. Rollback and uninstall

Every `orch install` leaves a timestamped backup:

```bash
ls ~/.orch/backups/
```

To restore, copy the files from the backup directory you want back over the originals.

To uninstall:

1. Remove the block between `<!-- orch:begin` and `<!-- orch:end -->` from `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.gemini/GEMINI.md`.
2. Remove the `orch hook claude` entry from `~/.claude/settings.json`, the `orch hook codex` entry from `~/.codex/hooks.json`, and the `orch-guard` key from `~/.gemini/config/hooks.json`.
3. Delete the links: `~/.claude/skills/orch`, `~/.codex/skills/orch`, `~/.orch/playbooks`, `~/.local/bin/orch`.
4. Optionally delete `~/.orch` (run history and backups).

Close any open runs first with `orch run end` or `orch gc --orphans`.

## 8. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `orch doctor`: "Herdr session" fails | running outside Herdr | start the Head inside a Herdr pane |
| constitution "differs from source" | repo updated, install not rerun | `make install` |
| Head's `Agent`/`Workflow` calls are blocked | the hook is working as intended inside Herdr | delegate with `orch spawn` |
| a plain `herdr pane` command is blocked | same hook | prefix it with `ORCH_RAW_HERDR=1` |
| Codex worker ignores the hook | hook not trusted | Codex `/hooks` -> trust `orch hook codex` |
| worker stalls on a folder-trust prompt | first run in a new folder | `orch` pre-trusts worker folders; if one still stalls, `orch read <name>` to see it, then `orch close <name>` and respawn |
| `orch spawn` stops with a user-pane message | the tab has a pane `orch` did not open | spawn from a tab with only the Head, or close your pane |
| layout drifted after manual resizing | panes resized by hand | `orch layout` |
| panes left behind after a crash | Head exited abnormally | `orch gc --orphans` |
