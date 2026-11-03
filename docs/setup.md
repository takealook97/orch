# Setup

This guide takes a machine from nothing to a working `orch` Head. It covers prerequisites, installation, verification, optional account splitting, updates, and rollback.

## 1. Prerequisites

| Requirement | Why | Check |
|---|---|---|
| macOS or Linux with a POSIX shell | `orch` shells out to `herdr`, `git`, `ps`, `sh` | — |
| Go 1.27+ | builds the `orch` binary | `go version` |
| Legacy Herdr (tested with 0.8.2) or Herdr.app with herdr-core | pane backend | legacy: `command -v herdr`; app: `command -v hc`, `command -v herdr-app`, `hc server status` |
| `git` | contract scope checks and changed-file detection | `git --version` |
| `codex` CLI, logged in | runs `codex-low-h` `codex-medium-m` `codex-high-l` `codex-high-m` | `command -v codex` |
| `claude` CLI, logged in | runs `claude-high-m` `claude-medium-m` | `command -v claude` |
| `agy` CLI, logged in | runs `gemini-medium-m` | `command -v agy` |
| `python3` (optional) | only for the cr account adapter and the eval harness | `python3 --version` |
| `~/.local/bin` on `PATH` | `orch` is linked there | `echo $PATH` |

You can install with only some of the model CLIs, but keys whose CLI is missing cannot be spawned. Install all three for full catalog coverage.

`orch` does not depend on any other skill. The `herdr` or `hd` skills may be installed alongside it, but the Head must not use them to drive panes; the hook blocks direct `herdr pane` calls.

Backend detection uses `ORCH_MUX=herdr|app` first, otherwise `HERDR_ENV=1`, otherwise nonempty `HC_TERMINAL_ID`. Doctor reports the selected backend and Head identity (`HERDR_PANE_ID` or `HC_TERMINAL_ID`). Start the Head inside the intended backend; existing runs cannot be managed from the other backend.

Herdr.app workers are placed by space/tab labels without focus changes. Layout uses versioned document rewrites, protects foreign terminals and mod blocks, and uses the Head terminal height for row limits. Empty overflow tabs normally disappear automatically. `ORCH_RAW_HERDR=1` also bypasses Head redirects for raw app creation and agent `hc run` commands.

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
| Hooks | `~/.claude/settings.json` (PreToolUse, matcher `Agent\|Task\|Workflow\|Bash\|Edit\|Write\|MultiEdit\|NotebookEdit`, plus UserPromptSubmit goal setup), `~/.codex/hooks.json` (PreToolUse and UserPromptSubmit goal setup), `~/.gemini/config/hooks.json` (`orch-guard`) | Adds or updates one `orch hook <cli>` entry per CLI. Other hooks are kept |
| Skill | `~/.claude/skills/orch`, `~/.codex/skills/orch` | Symlinks to `<repo>/skill`. The skill is self-contained: `SKILL.md`, `references/design.md`, `references/decisions.md`, `scripts/orch-account-cr` |
| Playbooks | `~/.orch/playbooks` | Symlink to `<repo>/playbooks` |

Preview without writing anything:

```bash
orch install --repo ~/IdeaProjects/orch --dry-run
```

### Codex hook trust

Codex 0.162 skips UserPromptSubmit for the native `/goal <objective>` command. Its PreToolUse guard reads goal events from the rollout transcript and blocks the first tool call until an orch goal is frozen for the Head pane; a draft does not suffice. During compilation it permits orch goal/help/models/status commands and edits under ORCH_HOME. The `codex-prompt` hook still covers plain prompts that start with `/goal` text. Claude transcripts have no such goal events and are unaffected.

Codex will not run a new or changed hook until you trust it. If you use Codex as the Head, open Codex once after installing, go to `/hooks`, and trust the `orch hook codex` and new `orch hook codex-prompt` entries once. Claude and Codex Heads receive `/goal` setup context; agy has no prompt hook. Scope checks cover Claude edit tools and Codex `apply_patch` edits, including shell heredoc patches. Other shell writes remain the Head's responsibility.

## 3. Verify

```bash
orch doctor
```

`orch doctor` checks:

- `codex`, `claude`, `agy`, `git`, and the selected backend binaries are on `PATH` (`herdr` for legacy; `hc` and `herdr-app` plus a healthy `hc server status` for app)
- the backend and Head identity resolve (`HERDR_ENV=1` / `HERDR_PANE_ID` for legacy; `HC_TERMINAL_ID` for app; `ORCH_MUX` overrides selection)
- you are not a worker (`ORCH_ROLE` is not `worker`)
- each constitution file contains the orch block and it matches `constitution/orch-block.md`
- each CLI's hook file references `orch hook <cli>`
- both skill links resolve to a directory with `SKILL.md`
- `~/.orch/playbooks` exists and passes the schema check
- the account mode (`same`, or the `ORCH_ACCOUNT_CMD` in use)

Run it from a pane inside Herdr; outside Herdr the "Herdr session" check fails by design.

Smoke test from a Head pane:

```bash
orch models                                   # the 7 canonical catalog keys
orch run new --text "smoke test" --mode light
orch contract new probe --role recon --model codex-low-h
orch spawn --model codex-low-h --name probe --role recon --purpose explore
orch prompt probe && orch wait probe
orch run end                                  # closes every pane this run opened
```

One-off tasks need no run and no pane (they also work outside Herdr):

```bash
orch test "go version"                        # no model: exit 0, no failures
orch ask --model codex-low-h "what does this repository build?"
```

## 4. Accounts (optional)

By default every worker uses the Head's login (`--account same`). To spread load across several accounts, point `ORCH_ACCOUNT_CMD` at an adapter and spawn with `--account hook`.

The bundled adapter reads the cr (Capacity Roller) registry. It never writes to cr.

```bash
export ORCH_ACCOUNT_CMD=~/.claude/skills/orch/scripts/orch-account-cr
orch doctor    # "account hook" should show the path
```

- Registry location: `$CR_HOME/registry.json`, default `~/.local/share/cr/registry.json`.
- It covers `codex` and `claude` accounts. `agy` (`gemini-medium-m`) always uses the Head's login.
- It picks the ready account with the most 5-hour and weekly headroom, and links the global instructions, skills, and the orch hook into that account's home without overwriting existing files.

Any other adapter works if it follows the same protocol: it is called as `<cmd> <kind> <comma-separated excluded labels>`, prints `KEY=VALUE` lines (environment for the worker pane) plus `LABEL=<id>`, and exits 1 when no account fits.

## 5. Environment variables

| Variable | Set by | Meaning |
|---|---|---|
| `ORCH_HOME` | you (optional) | state directory, default `~/.orch` (runs, locks, backups, playbooks) |
| `ORCH_ACCOUNT_CMD` | you (optional) | account adapter for `--account hook` |
| `ORCH_DEBUG=1` | you (optional) | verbose worker diagnostics on stderr |
| `CODEX_LOW_H`, `CODEX_MEDIUM_M`, `CODEX_HIGH_L`, `CODEX_HIGH_M`, `CLAUDE_HIGH_M`, `CLAUDE_MEDIUM_M`, `GEMINI_MEDIUM_M` | you (optional) | model id passed to `--model` for one key, read by `orch spawn` in the Head's shell. See the model catalog in the README for the key mapping and defaults. Keys and efforts stay fixed; `orch models` shows the resolved arguments |
| `ORCH_RAW_HERDR=1` | you, per command | lets one plain `herdr` command through the hook, for non-delegation panes such as builds or log tails. Close the pane when done |
| `ORCH_ROLE`, `ORCH_RUN`, `ORCH_WORKER`, `ORCH_MODEL` | `orch spawn`, `orch ask`, `orch do` | injected into worker panes and one-off task processes. Never set these in the Head |

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
2. Remove the Claude PreToolUse guard and UserPromptSubmit goal-prompt entry from `~/.claude/settings.json`, the `orch hook codex` and `orch hook codex-prompt` entries from `~/.codex/hooks.json` (PreToolUse and UserPromptSubmit goal setup), and the `orch-guard` key from `~/.gemini/config/hooks.json`.
3. Delete the links: `~/.claude/skills/orch`, `~/.codex/skills/orch`, `~/.orch/playbooks`, `~/.local/bin/orch`.
4. Optionally delete `~/.orch` (run history and backups).

Close any open runs first with `orch run end` or `orch gc --orphans`.

## 8. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `orch doctor`: "Herdr session" fails | running outside Herdr | start the Head inside a Herdr pane |
| constitution "differs from source" | repo updated, install not rerun | `make install` |
| Head's `Agent`/`Workflow` calls are blocked | the hook is working as intended inside Herdr | use `orch ask` (read-only) or `orch do` (writes) for a one-off task, `orch spawn` for long or parallel work |
| a raw `herdr pane`, `herdr-app do` creation, or agent `hc run` command is blocked | same hook | prefix it with `ORCH_RAW_HERDR=1` |
| Codex worker ignores the hook | hook not trusted | Codex `/hooks` -> trust `orch hook codex` |
| worker stalls on a folder-trust prompt | first run in a new folder | `orch` pre-trusts worker folders; if one still stalls, `orch read <name>` to see it, then `orch close <name>` and respawn |
| `orch wait` fails with "detached orch processes cannot manage panes" | it was started with `&`, `nohup`, or `setsid`, so it failed the Head identity check | run it in the foreground or with the harness's background-task feature. Finished panes still close through the run's reaper |
| A finished worker's pane stays open | the run's reaper is not running (check `~/.orch/runs/<id>/reaper.log`) | `orch collect <name>` or `orch close <name>`; the next `orch spawn` restarts the reaper |
| `orch spawn` stops with a user-pane message | the tab has a pane `orch` did not open | spawn from a tab with only the Head, or close your pane |
| layout drifted after manual resizing | panes resized by hand | `orch layout` |
| panes left behind after a crash | Head exited abnormally | `orch gc --orphans` |

## Model tiers and compatibility

<!-- orch:models:begin -->
| Key | CLI | Family | Tier | Default model | Effort | Model variable |
|---|---|---|---|---|---|---|
| `codex-low-h` | codex | openai | Luna | `gpt-6-luna` | high | `CODEX_LOW_H` |
| `codex-medium-m` | codex | openai | Terra | `gpt-6.1-terra` | medium | `CODEX_MEDIUM_M` |
| `codex-high-l` | codex | openai | Sol | `gpt-6.1-sol` | low | `CODEX_HIGH_L` |
| `codex-high-m` | codex | openai | Sol | `gpt-6.1-sol` | medium | `CODEX_HIGH_M` |
| `claude-high-m` | claude | anthropic | Opus | `opus` | medium | `CLAUDE_HIGH_M` |
| `claude-medium-m` | claude | anthropic | Sonnet | `sonnet` | medium | `CLAUDE_MEDIUM_M` |
| `gemini-medium-m` | agy | google | Gemini | `gemini-3.8-flash-medium` | — | `GEMINI_MEDIUM_M` |

OpenAI tiers: Luna < Terra < Sol (independent of effort). Deprecated alias: codex-medium-h -> codex-low-h. CODEX_LOW_H takes precedence over CODEX_MEDIUM_H.

Defaults are generated from `models.yaml`. Override shared model ids in `~/.orch/models.yaml` (or `ORCH_MODELS_FILE`), for example `models: {sol: new-model-id}` updates both Sol keys. An overlay may also set per-key effort with `keys: [{key: codex-high-l, effort: low}]`; other fields are rejected. Per-key environment variables override overlay model ids. `orch models` shows resolved ids and their source.
<!-- orch:models:end -->

Use Luna for broad cheap lookup and extraction, Terra for constrained writing and scoped code fixes, and Sol for larger planned implementation and general debugging. Screen work is a candidate Terra role, not benchmark-proven superiority. Creative writing and deep document work keep the Sonnet/Opus defaults; Gemini remains the independent neutral-review default.
