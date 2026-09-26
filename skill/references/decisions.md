# Decision log and design-doc changes (2026-09-26)

The original design doc is `design.md` in this directory. Decisions made or changed during implementation are collected here. Where the two conflict, this document wins.

## Decisions (design doc S14)

| # | Decision | Rationale |
|---|---|---|
| D1 | Opus uses `--effort medium` (or low); Sonnet uses `--effort medium` | Design-doc recommendation; Opus effort: medium or low only, never high |
| D2 | Head rules apply globally, but are enforced only inside Herdr (`HERDR_ENV=1`) | `orch` does not work without Herdr |
| D3 | Inside Herdr, all built-in Agent/Workflow/subagents are blocked (no exceptions) | User's choice |
| D4 | Image generation uses Codex `image_generation` (`sol-m`, purpose `image`) | It is a stable feature, and `~/.codex/generated_images` shows real usage traces |
| D5 | The right column's row count is terminal height / 8, capped at 6 rows. Over that, spawn is refused and queued | Design-doc recommendation |
| D6 | The run directory is `~/.orch/runs/<id>` | Keeps the repo clean |
| D7 | The constitution source lives in one place, `constitution/orch-block.md`, and `orch install` inserts it between markers in all three files | The three files carry different content alongside it |
| D8 | The skill has one source, `skill/SKILL.md`, and `~/.claude/skills/orch` and `~/.codex/skills/orch` point to it via symlink. The name is `orch` | The two copies had already drifted apart |
| D9 | No per-command integration is built; only the shared principle (skill S1) is kept. If a command instructs a subagent, it is redirected through `orch` | "the research command" was not a specific command |
| D10 | All three CLIs use a PreToolUse hook, and `orch spawn` also self-checks (refuses if `ORCH_ROLE=worker`) | Confirmed hook support in all three CLIs |

## Changes from the design doc

1. **`orch` is a standalone tool that does not depend on cr code** (per user instruction).
   - By default, the account is `same`: it passes the Head's login variables (`CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `CR_CODEX_NO_DAEMON`) to a worker of the same provider.
   - To switch accounts, wire in the optional external command `ORCH_ACCOUNT_CMD <kind> <exclude-label>`. It returns `KEY=VALUE` lines and `LABEL=`. Hook cr into this connection point if you want to use it.
2. **Layout: the Head takes the full-height left third** (previously half). Workers stack top to bottom at equal height in the right two-thirds column. If a tab has a pane `orch` did not create, spawn stops (I6).
3. **A finished pane is always closed immediately.**
   - `orch wait` and `orch collect` close the pane the moment they see the status line in the result file, and confirm it was removed.
   - Panes are never reused. The "pane reuse" item in design-doc S8.6 is removed.
4. **Only panes you spawned yourself are closed.** `close`, `run end`, and `gc` close only pane IDs recorded in the current Head's run.
   - Another Head's run is refused.
   - A run whose Head has disappeared is cleaned up only via an explicit `gc --orphans`, or by picking it up with `resume`.
5. **DAG execution is `orch dag check|run|status`** (design-doc S11.3's `cr orch run --dag`). It reads `dag.yaml` in the run directory. It spawns nodes whose inputs are ready, in order of longest critical path, and skips downstream nodes whose dependencies are not done. State is kept in `dag-state.json` so a run can resume after an interruption.
6. **The neutral checklist is built with `orch checklist [--shuffle]`** (S10.3). It is generated from acceptance.md and supports order-shuffling for full+.
7. **The account adapter `scripts/orch-account-cr` (shipped inside the skill) is optional.** It only reads the cr registry and answers in the `ORCH_ACCOUNT_CMD` format. It also prepares the chosen account's home.
   - It symlinks the global instruction file, skill, and `hooks.json`.
   - It copies the user's Codex hook trust state (hash and `enabled` value) into that home. Confirmed that Codex's trust hash is path-independent.
8. **Prompt delivery is confirmed via the conversation transcript.** A booting CLI can show a brief `working` state and still drop input. So delivery is only considered confirmed once the `[orch worker <name>]` marker appears on the worker's screen; if it doesn't appear within 20 seconds, it is resent once.
9. **Output the Head wrote can also be designated for verification.** Use `orch spawn --author-family head|openai|anthropic|google`. The same family constraints apply as when a worker is designated as author.
10. **Resolved contradictions in the design doc.**
   - The verification lane in light mode is one critical lane for code, or one neutral lane for docs/UI (per S8.3).
   - `decisions.md` goes in the run directory.
   - `orch doctor` is now defined.
   - The playbook's `mechanics: sol` was not a catalog key, so it was changed to `sol-m`.

## CLI facts confirmed during implementation

| Item | Confirmed result |
|---|---|
| agy rules file | It reads `~/.gemini/GEMINI.md`. Confirmed by inserting a marker phrase and asking |
| agy hook format | Goes under `PreToolUse` in a named group in `~/.gemini/config/hooks.json`. Input is `toolCall.name` and `toolCall.args` (`run_command` uses `CommandLine`). Output must include `decision`; if absent, all tools are refused (fail-closed). So when there is no opinion, it returns `allow` for workers and `ask` otherwise |
| Codex hook trust | A new hook does not run until trusted. Trusted once via the `/hooks` screen in `~/.codex` (`hooks.state` in `config.toml`). `-c bypass_hook_trust=true` had no effect. **If a Codex Head runs under a different `CODEX_HOME`, such as a cr account home, it must be trusted there too** |
| Codex subagent tool name | Arrives at the hook without a separator, as `collaborationspawn_agent`. `-c features.multi_agent=false` did not remove this tool. The hook blocks it |
| Claude folder trust prompt | Even in bypass mode, a new folder still triggers the trust prompt. `orch` auto-approves only when it is the Head's cwd or a subfolder of it |
| Herdr resize | `--amount` adds or subtracts a raw ratio value for that split. To lower the boundary between two rows, apply `down` to the upper pane; to raise it, apply `up` to the lower pane |

## Not stealing focus (measured 2026-09-26)

Analysis of the Herdr server logs showed that two APIs move the UI to the target pane's workspace and tab while handling a request. That is why the Head tab got pulled into focus even while the user was elsewhere.
- `pane.zoom`: moves focus even when not zoomed. Focus moved in 55 of 265 requests.
- `agent.prompt`: focus moved in 78 of 850 requests.

Changes made:
- Un-zoom is only called when the layout JSON shows `zoomed: true`.
- Prompting types one line via `pane.send_text`, then sends `agent.send_keys enter`. Nothing is sent while a dialog is open (`blocked`).

`pane.split --no-focus`, `pane.resize`, `pane.close`, `agent.start`, `agent.send_keys`, and `agent.read` did not move focus. After the fix, measurement showed zero focus moves caused by orch requests. The regression test is `TestNormalizeLayoutDoesNotZoomWhenNotZoomed`.

## Known limits

- **The hook is best-effort.** It checks command strings with regexes, looking at line starts, and text after `;` `&` `|` `(` `$(`, and after `bash -c`/`sh -c`/`eval`/`xargs`. It does not catch:
  - backtick substitution
  - process execution via another language, e.g. `python -c "os.system('...')"`
  - indirect execution through a script file

  It also does not catch variable substitution (`h=herdr; $h ...`). Four extra layers cover this case:
  - **Caller identity check:** every command that changes a run or pane checks whether the calling process's ancestry includes the shell (`shell_pid`) of the `HERDR_PANE_ID` pane. Forging the environment variable still cannot close another Head's pane.
  - Code-level check in `orch`: if `ORCH_ROLE=worker`, all Head-only commands are refused.
  - `orch spawn`'s I6 check: stops if the tab has an unknown pane.
  - `orch wait`'s unknown-pane warning: reports only, does not close.

- **Hooks in a different account home:** if a worker runs under a `CODEX_HOME` or `CLAUDE_CONFIG_DIR` different from the Head's, that home may lack hook configuration. In that case, recursive-delegation prevention relies on two things:
  - `orch spawn`'s `ORCH_ROLE` check (code)
  - the constitution rule (cr symlinks a global `AGENTS.md`/`CLAUDE.md` into the account home)
- **The deepeet workspace:** the deepeet repo's `.claude/skills/orch/SKILL.md` tells agents to use `herdr` directly. The global hook blocks this path, so that skill needs to be rewritten around `orch`. Out of scope here, left untouched.
- **Eval interpretation:** the P7 eval set (`eval/`) has a small sample (12 review pairs, 8 implementation pairs). It shows direction only; drawing conclusions needs repeated measurement.
