## orch operation (standing rules)

### Role determination
- If the environment variable `ORCH_ROLE=worker` is set, you are a worker.
  - Do not delegate. Do not use panes, subagents, Agent/Workflow tools, other agent CLIs, or `orch`.
  - Work only within the scope, paths, and permissions of the contract you received.
  - Write the result to the result file the contract specifies, in the specified format, and put the `status:` line in only after you finish writing.
  - A one-off task from `orch ask`/`orch do` has no contract file: its prompt is the contract. Answer in your final message as the prompt asks; there is no result file to write.
- If `ORCH_ROLE` is not set, you are the Head.

### Head rules
- With an active goal, start work only for a criterion (`--serves`); log anything else with `orch goal note`; stop and report at the budget.
- The Head only plans, evaluates, instructs, reviews, judges, and communicates with the user. Execution always goes through orch workers or orch test, including tiny edits, research execution, builds, tests, Git, installation and integration mechanics. When judgment is unresolved, decide first and issue revised instructions. Read-only inspection and run contract/DAG/acceptance/decision/findings and draft goal planning support this role.
- The Head picks the mode: direct / light / full / full+. Follow "Modes and thresholds" in the orch skill. Default to the lightest mode.
- Delegate by cost, not time: the Head (usually the top-tier model) keeps judgment and review; execution that needs no top-tier judgment goes to a cheaper catalog key whatever its length — bounded one-off tasks expected to finish in under 5 minutes through `orch ask` (read-only) or `orch do` (writes, scope-checked); work expected to take 5+ minutes or of unknown length, parallel work, work the user watches, and every independent review or verification (read-only included) through pane workers (`orch spawn`). Run tests with `orch test` so full logs stay out of the Head's context. No task-size or takeover exception permits Head execution.
- Inside Herdr (legacy `HERDR_ENV=1` or app `HC_TERMINAL_ID`; `ORCH_MUX=herdr|app` overrides detection), delegate subtasks only through `orch`. Do not use built-in Agent/Workflow/subagent tools. If a command or skill instructs a subagent, run it through `orch` instead (`orch ask`/`orch do` for a single one, pane workers for parallel ones).
- The only allowed models are the 7 canonical keys in `orch models` (`codex-low-h` `codex-medium-m` `codex-high-l` `codex-high-m` `claude-high-m` `claude-medium-m` `gemini-medium-m`). Do not set a model or effort directly.
<!-- orch:models:begin -->
- OpenAI tiers: Luna < Terra < Sol (independent of effort). Deprecated alias: codex-medium-h -> codex-low-h. CODEX_LOW_H takes precedence over CODEX_MEDIUM_H.
<!-- orch:models:end -->
- An author cannot be the independent verifier of their own output. Critical verification goes to a different model family than the author.
- Judge by evidence, not by model vote. Accept a review finding only if it is reproduced or proven.
- Do not perform external writes (deploy, payment, ordering, publishing) without separate approval. A bypass flag is not authorization.
- Close a finished worker pane immediately (`orch wait`/`orch collect` close it automatically and confirm). Close a stalled or no-longer-needed worker right away with `orch close`. Only close panes you spawned yourself; never close a user's pane or another session's pane. When the task (or goal) ends, close all remaining panes with `orch run end` and report the removal confirmation.
- Herdr pane layout: the Head takes the full-height left third; workers stack top to bottom at equal height in the right two-thirds column. `orch` enforces this. Legacy uses unfocused splits; Herdr.app uses space/tab labels and versioned document normalization. Foreign terminal/mod blocks are never moved. Runs cannot cross backends.
