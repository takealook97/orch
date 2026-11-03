# Decision log and design-doc changes (2026-09-26)

The original design doc is `design.md` in this directory. Decisions made or changed during implementation are collected here. Where the two conflict, this document wins.

## Decisions (design doc S14)

| # | Decision | Rationale |
|---|---|---|
| D1 | Opus uses `--effort medium` (or low); Sonnet uses `--effort medium` | Design-doc recommendation; Opus effort: medium or low only, never high |
| D2 | Head rules apply globally, but are enforced only inside Herdr (`HERDR_ENV=1`) | `orch` does not work without Herdr |
| D3 | Inside Herdr, all built-in Agent/Workflow/subagents are blocked (no exceptions) | User's choice |
| D4 | Image generation uses Codex `image_generation` (`codex-high-m`, purpose `image`) | It is a stable feature, and `~/.codex/generated_images` shows real usage traces |
| D5 | Each tab holds terminal height / `ORCH_MIN_ROW_HEIGHT` rows (default 2; minimum 1, maximum `ORCH_MAX_ROWS`, default 20). Further workers use unfocused overflow tabs; queue at the per-run worker cap (`ORCH_MAX_WORKERS`, default 12) | Updated by D18 (2026-10-18) |
| D6 | The run directory is `~/.orch/runs/<id>` | Keeps the repo clean |
| D7 | The constitution source lives in one place, `constitution/orch-block.md`, and `orch install` inserts it between markers in all three files | The three files carry different content alongside it |
| D8 | The skill has one source, `skill/SKILL.md`, and `~/.claude/skills/orch` and `~/.codex/skills/orch` point to it via symlink. The name is `orch` | The two copies had already drifted apart |
| D9 | No per-command integration is built; only the shared principle (skill S1) is kept. If a command instructs a subagent, it is redirected through `orch` | "the research command" was not a specific command |
| D10 | All three CLIs use a PreToolUse hook, and `orch spawn` also self-checks (refuses if `ORCH_ROLE=worker`) | Confirmed hook support in all three CLIs |
| D11 | Delegation is judged by cost, not time: execution that needs no top-tier judgment goes to a cheaper key regardless of length; the 5-minute floor applies only to parallel splits | The Head runs on the top-tier model, so its usage and context are the scarce resource (see "Cost-based delegation" below) |
| D12 | `orch ask` / `orch do` run a catalog key's CLI non-interactively without a pane (`claude -p`, `codex exec`, `agy -p`); `orch test` runs a command with no model and returns only a summary | Removes the fixed cost (spawn, contract, prompt, wait, collect) that made short delegation look unprofitable |
| D13 | Implementation defaults to `codex-high-l` (sol, effort low) when the contract carries a file-level plan; `codex-high-m` is kept for work where the worker must choose the approach or find the cause | The Head writes the plan into the contract, so the worker has little judgment left; the `mech-impl` chain escalates to `codex-high-m` on failure. Not yet measured on `gpt-6.1-sol` (2026-09-30) |
| D14 | Every handoff (pane contract `## Plan`, `orch ask`/`orch do` requirement text) carries a `/plan`-style step plan | User's standing rule (2026-09-30): workers run a plan, the Head does the judgment |
| D15 | Length picks the channel, not the owner: inside Herdr, a one-off task expected to take 5+ minutes (or of unknown length) and every independent review go to a pane worker; `orch ask`/`orch do` keep bounded tasks under 5 minutes and print a `hint:` line when a task ran 5+ minutes without a pane. The hook's Head pane rule treats backslash-escaped `; & |` as literal and lets plain `--help`/`-h` lookups through; it does not parse quotes (two review rounds showed a quote heuristic keeps opening false negatives) | 2026-10-06: Heads routed reviews and phase-sized work through `orch ask`/`orch do` (39 of 110 ad-hoc tasks ran 5+ minutes), so the user saw no panes; D11's "regardless of length" was read as "never a pane". Hook false positives: a `--help` lookup on pane split, and a grep pattern whose quoted `\|` alternation was read as a pipe |
| D13 | D3 stays: built-in subagents remain blocked inside Herdr. The cheap path is `orch ask`/`orch do`, which keeps the model catalog and `ORCH_ROLE=worker` injection | Model pinning is kept while a light path exists |

## D16. Goal guard (2026-10-15)

Thirty-hour `/goal` sessions can grow through open-ended goal text, repeated review/fix loops, and side quests. A goal is now separate from runs, owned by one Head pane, with a frozen list of at most eight checkable criteria, Scope globs, hours/worker budgets, and per-shard fix caps. Spawn/do admission requires `--serves`; ask/test stay free. Runs link to the active goal for bookkeeping. The goal worker cap counts admitted spawn/do in `goal.json`, reserved at admission and refunded if spawn/do fails before starting. `run new --goal` must name an existing goal. Extra findings go to a backlog instead of expanding the list. Budget exhaustion stops new work and requires a report and user decision before a logged extension. Criteria cannot be amended in place. Claude and Codex Heads get a `/goal` setup prompt and scope checks for Claude edit tools and Codex `apply_patch` edits, including shell heredoc patches. Codex needs the new `codex-prompt` hook trusted once in `/hooks`. Other Bash writes and agy prompt hooks are outside this guard. The guard is a speed bump, not a security boundary.

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
   - The playbook's `mechanics` value was not a catalog key, so it was changed to `codex-high-m`.

## Cost-based delegation (2026-09-26)

Source: the proposal "orch 위임 기준 재검토: 시간 기준에서 비용 기준으로" (2026-09-26).

- **Priority:** (1) Head tokens and context, (2) total tokens, (3) wall-clock. Time wins only while the user is waiting.
- **Why the old rule changed:** the 5-minute rule came from the 2026-09-26 eval, which compared single vs parallel workers on the same key (`codex-high-l`) and measured time only. It never compared a top-tier Head doing the work against a cheaper worker doing it, and it never measured tokens or Head context.
- **Rule:** delegate when the Head's own work (reading, editing, trial and error) is much larger than instruction plus returned report, no top-tier judgment is needed mid-task, and the result can be reviewed from a summary, diff stat, and test result.
- **Commands:**
  - `orch ask --model K "question"`: read-only (claude `--tools Read,Grep,Glob --permission-mode dontAsk`, codex `--sandbox read-only`, agy `--mode plan`). The answer is capped (`--max-output`, default 2000 characters) and the full answer stays on disk.
  - `orch do --model K --own GLOBS [--test CMD] [--worktree DIR] [--background] "requirement"`: writes. Scope is checked against `--own` exactly like `orch collect` (a change outside it fails the task). With `--test`, orch reruns the command itself at the end. Only changed files, diff stat, test result, and a capped summary come back; the full diff stays on disk.
  - `orch test "CMD"`: no model. Exit code, duration, failing test names, a capped first-failure excerpt, and the log path.
  - Records live in `<run>/tasks/task-N/`. Without an active run, an ad-hoc closed run holds them. `--background` returns at once; `orch wait task-N` collects.
  - Children get `ORCH_ROLE=worker`, so the worker hook and code checks block re-delegation.
- **Pane cleanup no longer depends on the Head (round 2):** a Head's detached `orch wait` died on the identity check and finished panes stayed open. Now `orch spawn` starts a per-run reaper (`orch __reap`, singleton by lock) that closes a pane once its result has a status line and the agent is not working on two polls, and exits when the run closes or has had no live workers for 60 s. `orch wait`/`orch collect` report reaped workers as finished. Confirmed in a live Herdr run on 2026-09-26: a probe worker's pane closed with no `orch wait` call.
- **Task hardening (round 2):** a foreground `orch ask/do/test` forwards SIGINT/SIGTERM/SIGHUP to the child's process group; task.json records pid and heartbeat, and `orch wait` marks a task whose runner died as failed; `__task` claims only a running, unclaimed task; `diff.patch` covers only the task's own changes and applies with `git apply`; scope checks run from the repo root without rename detection; the test excerpt keeps up to 10 lines of context before the first failure; timeouts report `timeout`; ad-hoc runs are closed from the first save, hidden from `orch run list` (use `--all`), and pruned by `orch gc` after 7 days.
- **Other token leaks closed:** `orch read` defaults to 40 lines, `orch collect` prints the result's `summary:` line, and the skill tells the Head to read summaries first and not to poll.
- **Open questions:** whether non-interactive permission limits are enough without hooks; how to measure subscription usage per model (cr reads account-wide 5-hour/7-day usage only, so the eval uses token counts). The eval arm `python3 eval/run.py run --only delegate` measures Head tokens, total tokens, quality, rework, and time per task type.

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

## Operational rules confirmed 2026-09-29

- Waiting uses foreground `orch wait` by default. While the user is waiting for progress, do not move it to a background task.
- A child repository's worktree outside a vat workspace cannot run workspace-dependent `vat` or `make check` commands ("not inside a vat workspace"). The contract must specify checks in the original checkout or require an unavailable-check report.
- Chain checks and commits with `&&`, never `;`, so a failed check prevents the commit.
- Do not add orch-specific branches to repository rules or scripts. Orch is one person's local tool; repository rules apply equally to every worker.
- Scope collection selects a repository containing the contract's base commit: cwd first, absolute contract paths next, then child repositories through depth two (including linked worktrees). The selected repository is reported when it differs from cwd's repository; no match fails scope verification.
- A prompted worker retains its original contract content hash. Identical re-prompts do nothing. Changed contracts, or legacy prompts without a saved hash, are refused with close-and-new-name guidance.

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
- **The deepeet workspace:** the deepeet repo's `.claude/skills/orch/SKILL.md` and `.codex/skills/orch/SKILL.md` defer to this skill and add only deepeet rules (Brain via CTBF, evidence sources, approval gates). Its "orch is mandatory for every task" rule covers direct mode and `orch ask`/`orch do`.
- **Eval interpretation:** the P7 eval set (`eval/`) has a small sample (12 review pairs, 8 implementation pairs). It shows direction only; drawing conclusions needs repeated measurement.

2026-09-30: removed codex-medium-m; Luna runs only at effort high (codex-medium-h). Catalog is 6 keys.

2026-09-30: neutral lane optional in full when no user-facing surface; clean merges need no model; no third attempt after 2 fix iterations, Head classifies the cause; full+ adds a design challenge after design.

2026-09-30: the Head owns design (claude-high-m only condenses evidence); design_check reviews the Head's design with a different family; QA defaults to the lightest other-family key: author OpenAI -> claude-medium-m, otherwise codex-high-l; textual merge conflicts and Head-Claude design checks use codex-high-l.



## D17. Codex native goal compilation (2026-10-15)

Codex 0.162 does not fire UserPromptSubmit for native `/goal <objective>`. PreToolUse now reads complete `thread_goal_updated` rollout events, caching the last state and byte offset atomically under ORCH_HOME/codex-goal-cache. Active native goals block Head tools inside Herdr until the pane has a frozen orch goal (drafts do not count). Compilation permits command-position orch goal/help/models/status shell commands and edits wholly under ORCH_HOME. Paused, cleared, and other non-active goal states release this guard. Workers and sessions outside Herdr are unaffected, as are Claude transcripts without these events. The codex-prompt hook still covers plain prompts starting with `/goal` text. Existing raw-Herdr bypass ordering is preserved.
2026-09-30: claude-medium-m (sonnet) is rated low+: about Luna level, below Sol. Critical lane for a Google author defaults to claude-medium-m; the docs fallback chain tries codex-high-m before claude-high-m.
2026-09-30: removed pipeline bottlenecks without changing model preferences. Mechanical gates run as model-less DAG run nodes; critical and neutral lanes default to pane: false (orch ask, read-only, no row); clean worktrees merge in one pass with one full verification.

## D18. Overflow worker tabs and per-run cap (2026-10-18)

Keep the Head full-height on the left third and at most rowCap workers on the right. Additional pane workers use run-owned overflow tabs created without focus, with full-width, equal-height rows and the same row cap. Reuse a tab with room before creating another; never move workers after spawn. Persist worker tab IDs and run-owned overflow tabs so reaper, wait/collect/close, run end, gc, and layout include them. Remove empty overflow tabs and confirm removal; never close a tab containing user panes.

`ORCH_MAX_WORKERS` is a positive integer with default 12 (invalid/non-positive values use the default). Count live workers of the run that have panes, excluding closed/lost workers. Spawn at the cap fails with "is full" so DAG workers queue only at the worker cap, rather than the visible row cap. Goal admission budgets remain separate. The concurrent-writer gate follows this worker cap instead of a fixed 4; the other parallel-write gates still apply.


## D19. Terra between Luna and Sol

2026-10-30: supersedes the 2026-09-30 retirement of codex-medium-m (history above is retained). Seven canonical entries now include codex-low-h = gpt-6-luna/high, codex-medium-m = gpt-6.1-terra/medium, and the unchanged Sol low/medium entries. Model hierarchy is Luna < Terra < Sol regardless of effort. Terra handles constrained writing, scoped known fixes and mechanical extraction; larger complete plans start at Sol low. Screen work is a candidate role, not a measured advantage. Sonnet/Opus writing and deep-document defaults and Gemini independent-neutral review remain unchanged.

The former codex-medium-h key is a deprecated Luna alias, including saved runs. CODEX_LOW_H takes precedence; CODEX_MEDIUM_H is used only when the canonical override is trimmed empty. CODEX_MEDIUM_M is separate. Fallbacks: explore Luna -> Terra -> Gemini; extract Luna -> Terra -> Sol medium -> Sonnet; mech-impl Terra -> Sol low -> Sol medium -> Sonnet. Normalize alias attempts to prevent retrying Luna; never downgrade Terra to Luna. Relative costs are planning weights, not vendor prices or benchmark claims.

## 2026-11-03 — Herdr.app / herdr-core backend

- Detect explicit `ORCH_MUX=herdr|app`, legacy `HERDR_ENV=1`, then app `HC_TERMINAL_ID`. Keep old runs on legacy and persist new backend identity.
- Place hc terminals with `herdr.space` / `herdr.tab` labels; unknown tab IDs create unfocused overflow tabs. Rename afterwards. Keep hc addresses separate from app block IDs.
- Normalize the versioned `herdr-app` document through `hc connect` (`doc.get` / `doc.set`), retry conflicts, re-read to verify, and reject every foreign terminal or mod block. Preserve leaf data and use Head/column weights 1:2 with equal worker rows.
- Always strip `HC_TERMINAL_ID` for hc run/send, use `--no-parent --no-notify`, and pass worker environment through an `env` argv prefix. This prevents child notifications into Head and hc sender tags in prompt text.
- Codex on hc requires `--no-daemon`. Trust is reported through blocked permission status or terminal text and remains restricted by `trustable(cwd)`; Claude needs down then enter.
- Kill, wait for finished/not_found, rm, then confirm disappearance with app inventory. Last-terminal exit automatically removes tabs and empty spaces; explicitly close only an owned empty overflow tab that remains.

- Herdr.app layout normalization tolerates an auto-removed overflow tab only after `hc get` confirms its recorded workers are no longer live; a missing Head tab or an inconclusive lookup remains an error. Readiness checks the screen for folder trust even before agent status arrives, and the `hc run` guard parses value-taking options and launch wrappers.

## 2026-11-03 — Reuse research and recall prior evidence

- Cache only successful one-off asks in the orch home, keyed by normalized question, model key, output cap, Git root, HEAD, and dirty state (including untracked file contents). Non-Git directories, failed asks, do, and test do not populate the cache. Store the launch key with the task so background completion follows the same rules.
- Default TTL is seven days; `ORCH_ASK_CACHE_TTL` is hours (`0` disables). `--fresh` skips lookup and refreshes the entry after success. Hits retain the task report shape and mark the original task directory as cached, without launching a model or registering another task.
- `orch recall` uses case-insensitive AND term matching over ask/do answers, worker results, knowledge, and findings. Filter by repository by default, with `--all-repos` for global recall; rank matching-line count before recency and cap excerpts to three lines of 160 characters each. Run recall before commissioning new research; no vector index is required.
