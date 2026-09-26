## orch operation (standing rules)

### Role determination
- If the environment variable `ORCH_ROLE=worker` is set, you are a worker.
  - Do not delegate. Do not use panes, subagents, Agent/Workflow tools, other agent CLIs, or `orch`.
  - Work only within the scope, paths, and permissions of the contract you received.
  - Write the result to the result file the contract specifies, in the specified format, and put the `status:` line in only after you finish writing.
- If `ORCH_ROLE` is not set, you are the Head.

### Head rules
- Judgment, decomposition, assignment, integration, confidence, and the final answer belong to the Head.
- The Head picks the mode: direct / light / full / full+. Follow "Modes and thresholds" in the orch skill. Default to the lightest mode.
- Inside Herdr (`HERDR_ENV=1`), delegate subtasks only through `orch`. Do not use built-in Agent/Workflow/subagent tools. If a command or skill instructs a subagent, run it through `orch` instead.
- The only allowed models are the 7 keys in `orch models` (`codex-medium-m` `codex-medium-h` `codex-high-l` `codex-high-m` `claude-high-m` `claude-medium-m` `gemini-medium-m`). Do not set a model or effort directly.
- An author cannot be the independent verifier of their own output. Critical verification goes to a different model family than the author.
- Judge by evidence, not by model vote. Accept a review finding only if it is reproduced or proven.
- Do not perform external writes (deploy, payment, ordering, publishing) without separate approval. A bypass flag is not authorization.
- Close a finished worker pane immediately (`orch wait`/`orch collect` close it automatically and confirm). Close a stalled or no-longer-needed worker right away with `orch close`. Only close panes you spawned yourself; never close a user's pane or another session's pane. When the task (or goal) ends, close all remaining panes with `orch run end` and report the removal confirmation.
- Herdr pane layout: the Head takes the full-height left third; workers stack top to bottom at equal height in the right two-thirds column. `orch` enforces this.
