# Advanced procedures

Full/full+ mode, contracts, review and recovery details are in [the operating manual](../SKILL.md). Technical design history is in [design](design.md), and superseded decisions are marked in [decisions](decisions.md).

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
# If concurrent writers created worktrees: orch merge --test "go test ./..." (merge, test, remove)
orch run end                   # close all remaining panes and confirm removal
```

## DAG and effect measurement

```bash
# write nodes (contracts) and dependencies into ~/.orch/runs/<id>/dag.yaml
orch dag check && orch dag run      # spawns ready nodes in critical-path order; finished panes close immediately
                                    # run: nodes are model-less gates, pane: false nodes go through orch ask (no row)
orch checklist --shuffle          # neutral-lane checklist template (full+ order shuffle)

python3 eval/run.py selfcheck   # self-check the eval set
python3 eval/run.py run         # 20 tasks: cross- vs same-family review, parallel vs single implementation -> eval/out/<timestamp>/report.md
python3 eval/run.py run --only delegate   # top-tier worker proxy (claude-high-m) vs lower-key orch ask/do: Head tokens, total cost, quality, rework, time
```

## Account (optional)

Pane workers default to `--account auto`: Codex and Claude launch through the resolved `cr codex` / `cr claude` executable when CR is installed. Orch supplies the catalog model and effort, Codex inline-screen flags, and worker context; CR selects and rotates accounts. Inherited `CR_LAUNCH_EMAIL` is cleared, and `CR_HOME` is carried through. Other providers and systems without CR use native startup. `--account cr` requires CR and a supported provider; explicit `--account same` passes the Head's login to the native CLI, and `--account hook` uses `ORCH_ACCOUNT_CMD`. One-off `orch ask` / `orch do` still default to `same`.

CR launches keep unfocused owned panes, assign the worker name after detection, and check registration, readiness, and folder trust. Failed launches clean up their owned pane. Model fallback stays on CR for supported providers and leaves account rotation to CR; it does not invoke the legacy account hook. To select accounts through the optional hook instead, wire an adapter into `ORCH_ACCOUNT_CMD` and use `orch spawn --account hook`. An adapter for the cr registry ships inside the skill at `skill/scripts/orch-account-cr`. With registry priority `custom`, the hook selects usable accounts by ascending nonzero rank (zero or missing ranks last), then email local part and full email, regardless of capacity mode. Other priorities retain the legacy headroom order: minimum of 5h and weekly remaining descending, then weekly remaining descending. It only reads the cr registry, and it aligns the chosen account's home with global instructions, skill and hook links, and Codex hook trust state (it never overwrites existing files).

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

The Herdr cockpit is available at https://github.com/takealook97/herdr-cockpit.
