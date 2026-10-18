# orch pipeline design document

> A general-purpose pipeline in which the main session (Head) takes a task handed down by a user utterance, splits it across subagents, verifies the result with a different model family, and integrates everything into one answer.
> Written: 2026-09-26 · References: external case studies and research (§17), `~/.claude/skills/orch/SKILL.md`, `~/.codex/skills/orch/SKILL.md`, `~/IdeaProjects/cr` (Capacity Roller), Herdr 0.8.2

---

## 0. Summary

- **The main session is always a separate, standing Head (commander).** It owns judgment, decomposition, assignment, integration, confidence, and the final answer. The scope of work Head does directly is set by the direct-mode threshold in §8 and the delegation break-even point.
- **Subtasks split into stages (recon/triage -> design -> produce -> verify -> integrate), and work inside each stage must be able to run fully in parallel.**
- **Models are fixed at 5, and model x effort keys are fixed at 6.** Codex medium tier (high effort only), Codex high tier (low/medium), Claude high tier, Claude medium tier, Gemini medium tier (agy). Model ids come from per-key environment variables (`CODEX_MEDIUM_H` ... `GEMINI_MEDIUM_M`). No model owns a fixed role; each contract picks a key from the **purpose-based assignment table (§9)**.
- **The default is the lightest mode.** The pipeline only kicks in once a threshold (§8) is crossed. Delegation is reserved for subtasks that clear the break-even point.
- **Work that gets delegated defaults to parallel.** Read-heavy work — recon, review, verification — always runs in parallel. Write work only runs in parallel when the owned files don't overlap and the interface is already fixed (the parallel-write gate). Stage boundaries overlap at the shard level too (§11, evidence in §17).
- **On failure, follow the fallback chain tied to the purpose.** First, a different account on the same model; then, once accounts are exhausted, the next key in the purpose's fallback order (§9.4).
- **Verification splits into a critical lane and a neutral lane.** The neutral lane is `gemini-medium-m` filling out a requirements checklist; the critical lane is a different model family from the author hunting for defects.
- **Rules don't live in a single skill.** They split across four layers:
  1. Constitution: short rules that are always loaded
  2. Executor `orch`: enforces model pinning, pane lifecycle, and account selection in code
  3. Hooks: block bypass paths
  4. Playbooks: define stages and lanes per task type
- The `orch` skill stays as the **operating manual** Head consults. Enforcement belongs to the executor and the hooks.

---

## 1. Background and problem

### 1.1 Current state (verified facts)

| Item | State |
|---|---|
| `orch` skill | There are separate copies for Claude and Codex, and **they already differ.** Only the Codex copy has an "evidence-source restriction" line, and the model-selection wording differs |
| Allowed models | Both skills (the Codex medium tier allows only high effort, the high tier only medium), the "orch task gate" in `~/.codex/AGENTS.md`, and the Deepeet rule in `~/.claude/CLAUDE.md` all **allow only 4 models.** Adding Gemini would violate the rule |
| Scope of application | Codex's `AGENTS.md` says "orch is required for every task"; Claude only applies it to the Deepeet workspace. Asymmetric |
| cr | `cr` is a dashboard invoked with no arguments, and only supports `status`, `doctor`, `--serve`. Running a specific account is only possible via `runPreferred()` inside the dashboard. **There is no non-interactive account-selection interface** |
| cr account environment | `providerEnv()` injects `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `CR_CODEX_NO_DAEMON`. `ensureSharedAgentConfig()` symlinks the global `AGENTS.md`/`CLAUDE.md` and skills into each account's home |
| cr capacity handling | `quotaWriter` detects capacity-error text, kills the process, and moves to the next account. The conversation starts over |
| Herdr | `herdr agent start --kind` supports `codex`, `claude`, `agy`, `gemini`, and more |
| agy models | Includes `gemini-3.8-flash-{high,medium,low}`, and can also select Claude or GPT-OSS models. **Must be pinned to Gemini only** |
| Claude CLI | Supports `--model` and `--effort <level>`. Effort can be pinned |
| Codex CLI | Supports `-m/--model`, `-c model_reasoning_effort=...`, non-interactive `exec` and `review` |

### 1.2 Why a skill alone isn't enough

1. **A skill only applies when it's invoked.** If a command with its own instructions — `/goal`, a research command — takes over the flow, orch's skill may never load.
2. **A command's own delegation bypasses orch.** Claude's `Agent`/`Workflow` tools or a Codex subagent are not Herdr panes and inherit the parent model. The 5-model routing doesn't apply to them.
3. **Recursive delegation risk.** A worker reads the same `AGENTS.md`/`CLAUDE.md` as Head, since `cr` symlinks these files into every account's home — the worker is guaranteed to read them. If it follows "orch is required for every task," the worker opens another pane.
4. **Prose rules drift.** Model names, effort, `--no-focus`, account env, and cleanup steps all have to be reproduced correctly by the model every single time. One mistake and the default model is used silently.
5. **Lifecycles don't line up with turns.** `/goal` runs across many turns. "Close the pane once the task is done" has no clearly defined end point.

---

## 2. Goals and non-goals

### Goals
- G1. Handle **every kind of task** — development, research, documentation, image generation, error fixes, analysis, and more — under the same framework.
- G2. No matter what the main session receives (`/goal`, a research command, plain conversation), **every subtask delegation goes through the same path** (`orch`).
- G3. Work inside a stage can run **fully in parallel** — recon shards, implementation shards, and verification lanes all qualify.
- G4. Model and effort are **pinned in code**. They never fall back to the CLI default.
- G5. Verification is **independent**. The author never verifies its own result.
- G6. Every contract, result, and piece of evidence **is left as a file**, so work can be resumed or audited.
- G7. Depending on the nature of the task, **the pipeline may not run at all.** Head decides.

### Non-goals
- Letting models vote to settle on an answer.
- Automating external writes (deployment, payment, purchase orders, publishing). This gate stays in place.
- A multi-level hierarchy where a worker calls another worker.

---

## 3. Overall structure

```
┌───────────────────────── User ──────────────────────────┐
│ utterance / /goal / research command / other commands   │
└─────────────────────────────────────────────────────────┘
                              │
                              v
┌────────────────────────────── Head (main session) ──────────────────────────────┐
│ (1) Constitution (CLAUDE.md, AGENTS.md, GEMINI.md) -> always loaded             │
│ orch's skill (operating manual)         -> consulted as needed                  │
│ (4) Playbooks (~/.orch/playbooks/*.yaml) -> define stages and lanes             │
│                                                                                 │
│ decompose -> write contract -> orch spawn/prompt -> read result file -> judge   │
└─────────────────────────────────────────────────────────────────────────────────┘
                      │                                          │
                      │  (3) hook: blocks built-in Agent/Workflow │
                      v                                          v
┌───────────────────────────────── (2) Executor: orch ──────────────────────────────────┐
│ model catalog (fixed) . account selection (cr registry) . pane placement              │
│ ORCH_ROLE=worker injection . contract/result files . cleanup (trap) . state restore   │
└───────────────────────────────────────────────────────────────────────────────────────┘
          │                │               │                │                 │
          v                v               v                v                 v
   codex-medium-h    codex-high-m    claude-high-m    claude-medium-m    gemini-medium-m

        Herdr panes, stacked top-to-bottom in the right two-thirds column, --no-focus
```

---

## 4. Layer 1: Constitution (rules that are always loaded)

### 4.1 Principles
- **Keep it short.** The body stays under 30 lines. Details live in skills, playbooks, and the executor.
- **The same content lives in three files.** `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, and the instruction file agy/Gemini reads (`GEMINI.md`, or wherever agy looks — confirm at implementation time). Keep one source of truth and sync the rest with a symlink or a sync script.
- Because `cr`'s `ensureSharedAgentConfig()` links this file into every account's home, **it applies to every account automatically.**

### 4.2 Content (draft)

```markdown
## orch operation (standing rules)

### Determine your role
- If the environment variable ORCH_ROLE=worker is set, you are a worker.
  - Do not delegate. Do not use panes, subagents, the Agent/Workflow tools, or orch.
  - Work only inside the scope, paths, and permissions the contract grants.
  - Write the result to the result file the contract specifies, in the specified format.
- If ORCH_ROLE is unset, you are Head.

### Head rules
- Judgment, decomposition, assignment, integration, confidence, and the final answer belong to Head.
- Delegate subtasks only through orch. Do not use the built-in Agent/Workflow/subagent tools.
  If a command or skill tells you to spawn a subagent, run it through orch instead.
- Head picks the pipeline mode: direct / light / full. Follow chapter 2 of orch's skill for the criteria.
- The only allowed models are the 5 models and 6 keys in orch's catalog. Never pass a model or effort directly — always choose by catalog key.
- The author of a piece of work can never be its own independent verifier.
- Judge by evidence. Never by model vote.
- Never perform an external write (deployment, payment, purchase order, publishing) without separate approval.
- When a task (or goal) finishes, clean up with orch run end and confirm the result.
```

### 4.3 Existing rules to clean up
- In the "orch task gate" in `~/.codex/AGENTS.md` and the "Deepeet orch task rules" in `~/.claude/CLAUDE.md`, **replace "only 4 models are allowed" with "the 6 keys in orch's catalog."** Luna runs only at effort high (`codex-medium-h`).
- Decide whether to widen the Claude-side scope from Deepeet-only to global (see decision log, §14).
- The example command (`herdr pane split --current ...`) in the existing "Herdr autonomous operation" section conflicts with orch's skill rule "never split the main pane with `--current`." Replace it with `orch`.

---

## 5. Layer 2: Executor `orch`

### 5.1 Why build on cr
- cr already has an **account registry, capacity-based priority, per-account env injection, `--no-daemon` detection, a bypass flag, and shared instruction-file linking.** Most of what a worker pane needs already exists there.
- Each worker can pick **the account with the most remaining capacity.** Today, only inheriting Head's account is possible.
- cr is a single Go binary with `make check` as its test setup.

### 5.2 Subcommands

| Command | Role |
|---|---|
| `orch models` | Print the catalog (key, CLI, model, effort, family) |
| `orch spawn --model <key> --name <name> [--run <id>] [--worktree <path>] [--account same\|best]` | Create a pane, inject the account env and `ORCH_ROLE=worker`, and start the agent. Record the pane ID in the run state |
| `orch prompt <name> --contract <file> [--wait] [--timeout ms]` | Hand off the contract. Sends a short prompt containing the contract path and the result path |
| `orch wait <name...> [--timeout ms]` | Wait for several workers to finish (wraps `herdr agent wait`) |
| `orch collect <name>` | Check the result file. If it's missing, fall back to `herdr agent read` for the terminal output |
| `orch close <name> \| --run <id> \| --all` | Close panes, rebalance the layout, and confirm removal |
| `orch status [--run <id>]` | Show running workers, their state, account, and elapsed time |
| `orch resume --run <id>` | Reattach or clean up remaining panes from the state file (for resuming `/goal`) |
| `orch gc` | Clean up panes that are recorded but have no owner, and old runs |

### 5.3 Model catalog (fixed in code)

Each model x effort combination gets its own key. Head picks only by key.

| Key | CLI (`--kind`) | Args | Family | Relative cost | Character |
|---|---|---|---|---|---|
| `codex-medium-h` | codex | `--model $CODEX_MEDIUM_H -c model_reasoning_effort="high"` | OpenAI | low | exploration, bulk classification, precise extraction, log and test analysis, focused verification |
| `codex-high-l` | codex | `--model $CODEX_HIGH_L -c model_reasoning_effort="low"` | OpenAI | medium | implementation from a file-level plan in the contract, mechanical integration, bulk repetitive edits |
| `codex-high-m` | codex | `--model $CODEX_HIGH_M -c model_reasoning_effort="medium"` | OpenAI | medium+ | general implementation, debugging, verification that needs tool execution, technical review |
| `claude-high-m` | claude | `--model $CLAUDE_HIGH_M --effort medium` (see D1; medium or low only, never high) | Anthropic | highest | hard implementation, ambiguous design, changes that cross multiple areas, hard debugging, deep review |
| `claude-medium-m` | claude | `--model $CLAUDE_MEDIUM_M --effort <D1>` | Anthropic | low+ | independent implementation, alternative approaches, docs/writing, code review, scenario QA |
| `gemini-medium-m` | agy | `--model $GEMINI_MEDIUM_M` | Google | low | requirements checklist, broad document/web/multimodal reading, user-perspective QA |

**Claude high tier effort: medium or low only; never high.**

- `codex-high-h` and `gemini-high-*`/`gemini-low-*` are not in the catalog. Grow it only by adding a new key when one is actually needed.
- Common flags: Codex `--dangerously-bypass-approvals-and-sandbox` (+ conditional `--no-daemon`), Claude `--dangerously-skip-permissions`, agy `--dangerously-skip-permissions`. Reuse cr's `launchArgs()`.
- **`spawn` refuses any model outside the catalog.** Head never writes a model string directly.
- agy is not managed by cr's account registry. It runs without account selection; only its login state is checked, in `orch doctor`.

### 5.4 Account selection
- `--account same` (default): pass through Head's `CODEX_HOME`/`CLAUDE_CONFIG_DIR`/`CR_CODEX_NO_DAEMON` as-is. This is what orch's skill does today.
- `--account best`: use the first available account in the order from cr registry's `sortedAccounts()`. Useful for splitting capacity when many workers share a family.
- If Head and the worker use different providers (e.g., a Codex Head spawning a Claude worker), use that provider's best account.
- **Capacity exhaustion:** the agent inside a pane runs outside cr's `quotaWriter`. When `orch wait` detects a `quotaText()` pattern in the output, it opens a new pane on the next account and resends the same contract. Because the conversation restarts, the contract must be self-contained (§7).

### 5.5 Pane layout (invariant, enforced in code)

```
┌────────────────────────┬──────────┐
│                        │ worker 1 │
│                        ├──────────┤
│ Head (main pane)       │ worker 2 │
│                        ├──────────┤
│ entire left column     │ worker 3 │
│                        ├──────────┤
│ width 1/3, height 100% │ worker 4 │
└────────────────────────┴──────────┘
```

**Invariants (must never break)**
- **I1.** The left column holds exactly one pane: Head. It spans the full height, and no other pane is ever created in the left column or splits Head vertically.
- **I2.** The root split is always left/right, with Head at a 1/3 ratio.
- **I3.** In the Head tab, workers appear only in the right column, stacked top to bottom. Overflow tabs contain full-width worker rows. No left/right split happens inside either worker stack.
- **I4.** Row heights in the right column differ by at most one line.
- **I5.** Pane splits use `--no-focus`; overflow tab creation omits `--focus`. Spawning never changes the user's focus.
- **I6.** Panes the user opened by hand are never moved or closed.

**Creation rules**
- Zero workers: `herdr pane split --pane $HERDR_PANE_ID --direction right --ratio 0.3333 ...`.
- While the Head column has room, split **the last worker in the right column** with `--direction down`. When full, split the bottom worker in a run-owned overflow tab with room, or create an unfocused overflow tab and use its initial pane.
- Splitting Head's pane with `--current` or `--direction down` is **refused** by `orch`. That code path does not exist.
- Read pane/tab IDs from the Herdr response (`pane split`: `.result.pane`; `tab create`: `.result.tab` and `.result.root_pane`). Record each worker's tab and the run's overflow tab IDs in run state. Never move workers between tabs after spawning.

**Post-change verification (after every spawn, close, resize)**
1. Read zoom state; unzoom only when explicitly repairing a zoomed Head layout. Spawn refuses a zoomed Head layout to preserve focus.
2. Read the tree with `herdr pane layout --pane $HERDR_PANE_ID`.
3. Check I1-I4. If any is broken, restore the ratio with `herdr pane resize`. `--amount` is a ratio delta, not a count of cells.
4. Check again after restoring. If two attempts fail to fix it, stop spawning and report to Head. Never keep spawning into a broken layout.

**On close**
- After closing a worker, rebalance remaining rows in the Head and overflow tabs evenly. Close an overflow tab once its last worker pane is removed, confirm removal, and keep failed cleanup IDs for retry. Preserve user panes added to an overflow tab.
- Closing the last worker and letting Head take the full width back is allowed. The next spawn splits back to 1/3.

**Row cap (D5)**
- Each tab holds at most terminal height / `ORCH_MIN_ROW_HEIGHT` (default 2) worker rows, clamped to 1 through `ORCH_MAX_ROWS` (default 20). The Head keeps the same left-third layout; extra workers use unfocused overflow tabs owned by the run.
- `ORCH_MAX_WORKERS` sets a separate live pane-worker cap per run (positive integer, default 12). At that cap spawn reports "is full", and DAG ready workers queue in critical-path order. Closed/lost workers and nodes without panes do not count. Goal admission budgets remain an additional limit.
- `orch layout`, reaper, wait/collect/close, run end, and gc include overflow panes. Workers need not be visible in the focused tab.

### 5.6 The run directory (single source of truth for state and evidence)

```
~/.orch/runs/<run-id>/
  run.json              # original request, mode, playbook, stage state, worker list (pane id, model, account)
  request.md            # the user's original request (passed as-is to verification lanes)
  acceptance.md         # acceptance criteria Head compiled (numbered)
  contracts/<name>.md   # contract files
  results/<name>.md     # worker results (fixed format)
  evidence/<name>/      # command output, screenshots, produced files
  findings.md           # Head's verdict log (classification + rationale)
```

- Never keep this inside the working repository — it would dirty git state. If the repo needs a pointer, link only `.orch/` there.
- Multi-turn work like `/goal` resumes from `run.json`.

### 5.7 Herdr operating rules
- **A prompt timeout does not mean it wasn't delivered.** Before resending, confirm with `herdr agent read` whether the input landed, to avoid a duplicate submission.
- Never guess a pane ID. Only read it from the returned JSON.
- Judge completion by **the `status:` line in the result file**, not the agent state (`done`/`idle`). Treat state as a secondary signal only.

### 5.8 Cleanup guarantee
- Record the pane ID in the run state the instant `spawn` creates it.
- `close --run` closes every recorded ID and confirms removal with `herdr pane list`. On failure, retry and report.
- Reconcile not only panes but also the **worktrees and branches** this run created against the list. Report anything left behind, with a reason.
- If the Head session dies abnormally, `orch gc` cleans up orphaned panes on the next run. The criterion is that the run is no longer active, or Head's pane is gone.
- **The lifecycle boundary is the run, not the turn.** While `/goal` is in progress, panes stay alive and can be reused for another shard.

---

## 6. Layer 3: Hooks

| Location | Event | Action |
|---|---|---|
| Claude Head | PreToolUse `Agent`, `Workflow` | Block if `ORCH_ROLE` is unset, and return a message telling it to delegate through orch. Pass through if it's on the exception list (§14) |
| Claude worker | PreToolUse `Bash` matching `herdr pane split`, `herdr agent start`, `orch` | Block if `ORCH_ROLE=worker` |
| Claude worker | PreToolUse `Agent`, `Workflow` | Block if `ORCH_ROLE=worker` |
| Codex | equivalent hook feature | **Support needs confirming.** If absent, rely on the constitution rule and `orch spawn`'s own check (rejects when `ORCH_ROLE=worker`) |
| agy | equivalent hook feature | Needs confirming. Same fallback as above if absent |
| Everywhere | `orch spawn` | Rejects when `ORCH_ROLE=worker`. **Recursion is blocked in code regardless of which CLI is calling** |

- `~/.claude/settings.json` already has a `herdr-agent-state.sh` hook. Add these to the same file.

---

## 7. Contracts

Every delegation goes through a contract file. The prompt itself only says "read this contract and do it; write the result to this path."

```markdown
# Contract: <name>
- run: <run-id> · stage: recon | design | produce | verify-mech | verify-critical | verify-neutral | verify-adversarial
- model: <catalog key>   (for the record only; orch enforces the actual choice)

## Goal
<one or two sentences>

## Inputs
- Original request: ~/.orch/runs/<id>/request.md
- Acceptance criteria: ~/.orch/runs/<id>/acceptance.md (#1-#N; the ones relevant to this contract: #2, #4)
- References: <paths, URLs, earlier result files>

## Non-goals
- <what not to do>

## Baseline
- base commit: <hash>   · worktree: <path>
- decision log: ~/.orch/runs/<id>/decisions.md (read and follow it)
- Interface assumptions: <signatures, schemas that other shards depend on>

## Scope
- Owned files (glob): <exclusive paths>   ("none" if read-only)
- Editing a file outside scope automatically fails the result (orch collect checks the diff)
- Allowed evidence sources: <local paths, specific sites, etc.>. Do not search unrelated connectors

## Permissions
- Allowed: <test runs, local file writes ...>
- Forbidden: deployment, external writes, git push, out-of-scope edits, re-delegation

## Deliverables
- <files, diffs, documents, image paths>

## Budget
- Time: <minutes> · if you expect to run over, report partial and stop

## Self-check (by the author)
- <commands to run, conditions to check>

## Result report
Write it to ~/.orch/runs/<id>/results/<name>.md in the format below.
```

### Result report format

```markdown
# Result: <name>
- status: done | blocked | partial      <- completion is judged by this line, not by the on-screen state
- model_used: <catalog key> · account: <redacted id> · fallback: <none | original key -> key used, reason>
- summary: <three lines or fewer>
## Deliverables
- <path and description>
## Evidence
- command: `<cmd>` -> exit <code>, an output excerpt or an evidence/ path
## Uncertainty
- <what you're not sure of, and why>
## Blockers
- <decisions, permissions, or inputs you need>
```

### 7.1 Decision log
- Head records implicit decisions (names, interfaces, libraries, formats, scope calls) with numbers in `~/.orch/runs/<id>/decisions.md`.
- Every contract references this file as an input, to stop workers from building conflicting assumptions (§17 C3).
- If a worker has to make a new decision, it goes in the result report's "Blockers" section, and Head folds it back into the decision log.

- A contract must be **self-contained.** Even after a capacity exhaustion forces a restart on a different account, the same contract alone must be enough to redo the work.

---

## 8. Modes and thresholds: whether to run the pipeline at all

### 8.1 Principle: delegate only when the benefit outweighs the cost
- Delegation has a **fixed cost**: starting a pane (10-30 seconds), writing the contract, the tokens a worker spends re-reading context, and the tokens Head spends reading and judging the result.
- Every extra verification lane is also a worker. **Adding a lane is also a cost.**
- So the default is **the lightest mode.** Only step up once a threshold below is crossed — never "just in case."
- Parallel-first (§11) is a principle **for work that's already been delegated.** It does not mean splitting work that isn't worth delegating in the first place.

### 8.2 Metrics (Head estimates these within 30 seconds of receiving the request)

| Symbol | Item | How to measure |
|---|---|---|
| **E** | Expected effort if Head does it directly | expected tool-call count or time |
| **F** | Change footprint | number of files touched, whether it crosses module boundaries |
| **P** | Number of independent subtasks | count of chunks that can run at the same time without waiting on each other's results, among those that clear the delegation break-even point (§8.4) |
| **U** | Uncertainty | can the acceptance criteria be pinned down sentence by sentence right now |
| **R** | Cost of reverting | can git undo it immediately, or does it leave a trace in data or externally |
| **H** | High-risk flag | does it match any item in §8.3's list |

### 8.3 Mode thresholds

| Mode | Entry condition | Flow | Worker count |
|---|---|---|---|
| **direct** | **All** of: E <= 10 tool calls or about 5 minutes · F <= 2 files, one module · P <= 1 · U = acceptance criteria can be pinned down immediately · R = git reverts it instantly · no H | Head does it directly. No run directory. If there's a test or build, Head runs it once directly | 0 |
| **light** | Crosses one or more of the direct conditions, but: F <= 5 files, one or two modules · U = can be pinned down · R = git can revert it · no H | One deliverable (parallel if P >= 2) + **one independent verification lane**. Mechanical checks are Head running the command directly | 1-3 |
| **full** | **One or more** of: F > 5 files or 3+ modules · P >= 3 · U = acceptance criteria need investigation to pin down · R = reverting is a hassle (multiple commits, config, a pre-publish stage of a document) | recon -> (Head design) -> produce -> mechanical + critical (+ neutral if user-facing output changed) -> integrate | 3 up to the D5 cap |
| **full+** | **Any** H | same as full, plus a design challenge of the Head's decisions.md by a different family (`--author-family head`) after design and before produce, 2 critical lanes from different families + an adversarial lane | same as full |

**H: high-risk flags (any one triggers full+)**
- Auth, permissions, secrets, encryption
- Data deletion, migration, schema changes, backup/restore
- Money, payments, billing, usage calculation
- Concurrency primitives (locks, queues, transaction boundaries)
- Operational config, deployment pipelines, artifacts published externally
- The user explicitly asks for "thorough" or "fully verified" work

**Picking the one verification lane for light mode**
- Code with existing tests -> Head runs the tests directly + **one critical lane** (a different family)
- Docs, writing, user-facing output -> **one neutral lane** (`gemini-medium-m`)
- Both apply -> the critical lane takes priority

### 8.4 Delegation break-even point (per individual subtask)
Head decides whether to delegate a subtask or do it directly using the following.

**Delegate if one or more apply:**
1. Doing it directly would take Head **about 5 minutes or 10+ tool calls or more** — roughly 3x the fixed cost of delegating.
2. Running it **at the same time as another subtask shortens the critical path.**
3. The mode **requires an independent perspective** (a verification lane).
4. It would badly pollute Head's context (large logs, long documents). If a worker returns just a summary, it saves Head's tokens.

**Do it directly if none of the above apply:**
- E.g., reading one file, running one command, a short edit, a small adjustment between result files.

**Floor for splitting**
- Never split a subtask below the break-even size. If the split-off chunks would each be under 5 minutes, merge them and give them to one worker.
- Parallel width = the number of independent subtasks that clear the break-even point. Beyond the D5 cap, spawn in critical-path order.

### 8.5 Adjusting the mode
- **Step down**: if the investigation shows the scope is smaller than expected, drop a level right away, and close any worker that's no longer needed.
- **Step up**: raise the level if an H flag surfaces mid-task, the acceptance criteria shift, or a confirmed defect repeats twice.
- Record every mode change in `run.json` along with the reason.

### 8.6 Token-efficiency rules (apply in every mode)
- **Pass paths, not content.** Contracts carry file paths, line ranges, and commit hashes — not long pasted bodies.
- **Results are a summary plus files.** Keep a worker's result summary to 3 lines or fewer; put detail in the evidence files. Head opens only the files it needs.
- **Head does not read worker transcripts.** Use `agent read` only when the result file is missing or the worker is stuck.
- **Cut early**: if a shard's mechanical check fails, cancel that shard's other verification lanes. Rerun them once it's fixed.
- **Rerun only the affected lanes.** If a fix is a one-line doc change, don't rerun the code review.
- **Reuse panes**: once a worker with a given key finishes, it can take the next contract in the same run if that contract **doesn't need independence** from the previous one. Never reuse the same pane for both author and verifier.
- **Cheapest key first**: if the primary in the assignment table (§9.2) is a cheap key, use it. Only escalate per the criteria in §9.4.

### 8.7 Example calls

| Request | Metrics | Mode |
|---|---|---|
| "Rename this function" (2 files) | E small, F 2, P 1, no H | direct |
| "Add install steps to the README" | E small, user-facing doc | direct. If it's long, light (claude-medium-m writes + gemini-medium-m neutral) |
| "Fix the login failure bug" | auth = H | full+ |
| "Add an `orch status` subcommand to cr" | F 2-3, one module, U pinnable | light (codex-high-m implements + Head tests + claude-medium-m critical) |
| "Fully implement `orch` in cr" | F > 5, P >= 3 | full |
| "Research multi-agent setups built on Herdr" | many subquestions, sources need checking | full (parallel research + synthesis + neutral/critical) |
| "What does this error message mean?" | a question | direct |

**Work handed down by `/goal` or another command follows the same thresholds.** The command decides the goal and the iteration style; this chapter decides the mode and the delegation.

---

## 9. Model assignment (by purpose)

### 9.1 Assignment principles
1. **Purpose decides the model**, not the stage name — pick by what the contract actually does (explore, extract, implement, judge, cross-check, etc.).
2. **Judge along four axes:**
   - Uncertainty: is the answer fixed, or does it need interpretation
   - Technical difficulty: local, or does it cross multiple areas
   - Input shape: code and logs, or web/long documents/images
   - Cost of being wrong: how expensive is a mistake
3. **Pick the cheapest key that satisfies the contract** — but don't stall on a hard task by retrying it repeatedly with a weak model. If the difficulty is visible up front, go straight to `claude-high-m`.
4. **Effort is chosen separately from the model.** Luna runs only at effort high (`codex-medium-h`) for exploration, bulk classification, and precise extraction. Even within the Codex high tier, implementation from a file-level plan gets `codex-high-l`, and one where the worker still has to choose the approach gets `codex-high-m` (decision D13).
5. **No model has a fixed title.** `gemini-medium-m` also takes recon or produce work when the purpose fits — it just can never be the **sole verifier.**
6. Don't save `claude-high-m` for planning or arbitration only. Use it directly on hard implementation.
7. **The verification-family constraint (§9.3) outranks both assignment and fallback.**

### 9.2 Assignment table by purpose

The `Primary` is the default. `Conditional alternative` is used from the start when the contract's nature matches the condition. Fallback (on failure) is handled separately in §9.4.

#### A. Recon / triage

| Purpose | Primary | Conditional alternative | Reason |
|---|---|---|---|
| Find files, symbols, call sites (broad) | `codex-medium-h` | Ambiguous classification criteria -> `claude-medium-m` | Speed matters, little judgment needed |
| Evidence extraction (exact quotes, line numbers, values) | `codex-medium-h` | — | Precision matters |
| Log, stack-trace, test-failure analysis | `codex-medium-h` | Multiple tangled root-cause candidates -> `codex-high-m` | Precise judgment over narrow input |
| Bulk classification, tagging, list cleanup (dozens of items or more) | `codex-medium-h` per shard | Classification criteria are ambiguous -> `claude-medium-m` | Cheap, fast model, parallelized across shards |
| Web research, long documents, PDFs, comparing multiple sources | `gemini-medium-m` per topic | Technically deep source interpretation -> `claude-high-m` | Strong on broad and multimodal input, and cheap |
| Reading images, screenshots, video, design files | `gemini-medium-m` | Fine-grained judgment matters -> `claude-medium-m` | Multimodal |
| Data extraction, aggregation (SQL, CSV, scripts) | `codex-medium-h` | Schema interpretation needed -> `codex-high-m` | Tool execution + precision |
| Reconciling conflicting evidence, forming root-cause hypotheses | `claude-high-m` | — | Needs interpretation and synthesis |
| Requirements decomposition, acceptance-criteria drafts | Head | Unfamiliar domain -> ask `claude-high-m` for a draft | Head's own territory |

#### B. Design

| Purpose | Primary | Conditional alternative | Reason |
|---|---|---|---|
| Condensing large or conflicting evidence for the Head, root-cause hypotheses | `claude-high-m` | — | Evidence synthesis; the Head decides |
| Alternative design (for comparison) | `claude-medium-m` | If the primary design is Anthropic's -> `codex-high-m` | A different perspective, a different family if possible |
| Interface, schema, data-model drafts | `codex-high-m` | Impact is wide -> `claude-high-m` | Clear technical judgment |

#### C. Produce: code

| Purpose | Primary | Conditional alternative | Reason |
|---|---|---|---|
| Planned implementation: the contract fixes files, functions, and the acceptance test (features, bug fixes with a known cause, renames, boilerplate) | `codex-high-l` | Scope is very narrow, 1-2 files -> `codex-medium-h` | The Head already made the judgment; default implementer |
| Implementation where the worker still has to choose the approach or find the cause | `codex-high-m` | — | Judgment is left to the worker |
| Hard implementation (concurrency, performance, security, complex state) | `claude-high-m` | — | Assign it from the start. Never try a weak model first |
| Changes across multiple modules, large refactors | Head (design) + `claude-high-m` (core) + `codex-high-m`/`codex-high-l` per shard | — | Design decisions belong to the Head; core implementation goes to `claude-high-m`, mechanical shards run in parallel on the Codex high tier |
| Hard debugging (unstable repro, cause spans multiple layers) | `claude-high-m` | Repro is reliable and the cause is local -> `codex-high-m` | — |
| Alternative implementation (for comparison) | `claude-medium-m` | If the primary implementation is Anthropic's -> `codex-high-m` | A different family |
| Writing test code | `codex-high-m` | Scenario design is the focus -> `claude-medium-m` | — |
| Merging, conflict resolution, mechanical integration | `codex-high-l` | Conflicts are semantic -> `claude-high-m` | — |

#### D. Produce: writing, analysis, other

| Purpose | Primary | Conditional alternative | Reason |
|---|---|---|---|
| Docs, reports, manuals | `claude-medium-m` | Technically deep content -> `claude-high-m` | Writing quality |
| Research synthesis report | `claude-medium-m` | Sources conflict -> `claude-high-m` | — |
| Short summaries, changelogs, boilerplate documents | `codex-medium-h` | — | Cheap and good enough |
| Data analysis (calculations, statistics) | `codex-high-m` | Interpretation is ambiguous, causal judgment needed -> `claude-high-m` | Tool execution + judgment |
| Image generation | Decide after D4 | — | Needs confirming which generation tool is supported |
| Translation, format conversion | `codex-medium-h` | Heavy on technical terminology -> `claude-medium-m` | — |

#### E. Verification

| Lane / purpose | Primary | Conditional alternative | Reason |
|---|---|---|---|
| Mechanical: rerun tests, builds, lint | no model (DAG `run` node or `orch test`); `codex-medium-h` reads a failure | Environment setup is complex -> `codex-high-m` | Run it and record it exactly |
| Mechanical: run repro scenarios, recompute | `codex-medium-h` | — | — |
| Mechanical: inspect rendered output, produced files | `gemini-medium-m` | — | Multimodal |
| Critical: general code review | Author is OpenAI -> `claude-medium-m` (or `claude-high-m` if complex) · Anthropic -> `codex-high-l` (or `codex-high-m` if complex or full+) · Google -> `claude-medium-m` (or `claude-high-m` if complex) | — | A different family; QA defaults to the lightest key of the other family; escalate only on complexity, full+, or a missed defect |
| Critical: complex change or one crossing multiple areas | Author is OpenAI/Google -> `claude-high-m` · Anthropic -> `codex-high-m` | — | Depth + a different family |
| Critical: confirm one narrow, explicit claim | `codex-high-l` (author isn't OpenAI) | Author is OpenAI -> `claude-medium-m` | — |
| Critical: fact/source rebuttal (research) | `claude-high-m` | `claude-high-m` wrote the synthesis -> `codex-high-m` | — |
| Neutral: requirements checklist | `gemini-medium-m` | — | A different family, checklist-style work |
| Adversarial: attacking a specific target | Per §9.3: a family different from both the author and the critical reviewer. Default `claude-high-m`; if the author is Anthropic, `codex-high-m` | — | Depth first. Never `gemini-medium-m` |

#### F. Primary use per key, at a glance

| Key | Main use | Avoid for |
|---|---|---|
| `codex-medium-h` | Broad exploration, bulk classification, translation/conversion, precise extraction, log/test analysis, mechanical verification, narrow implementation, short boilerplate docs | Implementation crossing multiple areas, design |
| `codex-high-l` | Fully specified mechanical implementation, bulk repetitive edits | Ambiguous requirements, debugging |
| `codex-high-m` | General implementation, debugging, integration, interface design, reviewing Anthropic's work | — |
| `claude-high-m` | Hard implementation and debugging, ambiguous design, evidence synthesis, deep review, adversarial verification | Simple exploration, bulk repetitive work |
| `claude-medium-m` | Docs/writing, alternative implementation/design, reviewing OpenAI's work, scenario QA | Precise extraction |
| `gemini-medium-m` | Neutral verification, web/document/multimodal research, checking rendered output | Being the sole verifier, adversarial verification, hard implementation |

### 9.3 Verification-family constraint
There are exactly three families: OpenAI (`codex-*`), Anthropic (`claude-high-m`, `claude-medium-m`), Google (`gemini-medium-m`).
- **Required**: the critical lane must be a different family from the author.
- **Recommended**: the adversarial lane should differ from both the author and the critical reviewer.
- If satisfying the recommended condition would force `gemini-medium-m` into the adversarial lane, **drop the recommended condition.** Assign a session that differs from the author but may share a family with the critical reviewer — depth matters more there than family diversity.
- Never let the neutral lane's `gemini-medium-m` session double as another lane's `gemini-medium-m` session.
- If the author spans multiple families (e.g., `claude-high-m` for the core, `codex-high-*` for shards), **judge author family per shard.**

### 9.4 Fallback (tied to purpose)

#### Failure types and first response

| Type | Detection | First response |
|---|---|---|
| **Capacity/usage exhaustion** | `quotaText()` pattern or rate-limit wording in the output | (1) Same key, **a different account** (cr registry order) -> once accounts are exhausted, (2) the next key in the fallback chain |
| **Startup failure** | persistent `agent_not_ready`, expired login, CLI error | Retry once -> same key, different account -> fallback chain |
| **No response / timeout** | `wait` times out, stuck on `unknown`/`blocked` | Check state with `agent read` -> extend once if still progressing, fallback chain if stalled |
| **Quality failure** | result doesn't satisfy the contract, a confirmed defect repeats | **Escalate, don't fall back.** Move to a stronger model, not the next in the chain order (§10.5) |

- Switching accounts restarts the conversation. The contract must be self-contained (§7).
- When falling back, **attach the previous worker's partial result and logs to the new contract's inputs.** Don't start from zero.
- Record every fallback and escalation in `run.json` and in the result report's `model_used`, and show it in the final report.
- After a fallback, **recheck the family constraint (§9.3).** If the fallback model ends up in the same family as the author, that verification lane is void.
- Test the fallback paths for real. Inject a failure deliberately to confirm (§15 P6).

#### Fallback chain by purpose

| Purpose | Primary | Fallback 1 | Fallback 2 | Note |
|---|---|---|---|---|
| Broad exploration, bulk classification | `codex-medium-h` | `gemini-medium-m` | — | Fall back to another family |
| Precise extraction, log/test analysis | `codex-medium-h` | `codex-high-m` | `claude-medium-m` | |
| Web/document/multimodal research | `gemini-medium-m` | `claude-medium-m` | `claude-high-m` | |
| Evidence synthesis, hypothesis forming | `claude-high-m` | `codex-high-m` | `claude-medium-m` | |
| Mechanical implementation | `codex-high-l` | `codex-high-m` | `claude-medium-m` | |
| General implementation, bug fixes | `codex-high-m` | `claude-high-m` | `claude-medium-m` | user-specified example |
| Hard implementation, hard debugging | `claude-high-m` | `codex-high-m` | `claude-medium-m` | |
| Alternative implementation/design | `claude-medium-m` | `claude-high-m` | `codex-high-m` | If falling back would defeat the point of "alternative" (a different family), prefer `codex-high-m` |
| Docs/writing, research synthesis | `claude-medium-m` | `claude-high-m` | `codex-high-m` | |
| Data analysis | `codex-high-m` | `claude-high-m` | `claude-medium-m` | |
| Integration, merging | `codex-high-m` | `claude-high-m` | Head does it directly | |
| Mechanical verification | `codex-medium-h` | `codex-high-m` | `claude-medium-m` | the author's own session is forbidden |
| Critical review | assignment table E | swap within the same **different family** (`claude-medium-m`<->`claude-high-m`, and `codex-high-l`<->`codex-high-m` rather than `codex-high-m`<->`claude-high-m`) | whatever's left in a family different from the author | **The family constraint wins.** Never fall back into the author's family |
| Neutral verification | `gemini-medium-m` | `claude-medium-m` (or `codex-high-m` if the author is Anthropic) | any other different family | Keep the output format (the checklist). Mark it "gemini-medium-m substitute" in the report |
| Adversarial verification | assignment table E | a session in a different family from both the author and the critical reviewer | — | Never fall back to `gemini-medium-m` |

#### If the whole fallback chain is blocked
- Mark that shard `blocked` and **keep the other shards moving.** Don't stop everything.
- If it's small enough for Head to do directly, do it directly.
- Otherwise, report to the user what's blocked, which account/model is exhausted, and when it should free up (the reset time from the cr registry).

#### Avoiding fallback in the first place
- Before `spawn`, check **that family's remaining capacity** in the cr registry. If a family is nearly out, start with the assignment table's conditional alternative or fallback 1 right away. Still respect the family constraint.
- When spawning many workers on the same key, use `--account best` to spread them across accounts.

---

## 10. Verification design

### 10.1 Principles
- **Plan verification when you assign the work**, not after a worker declares it done.
- The author's own test runs and self-review are supporting evidence only — **they never count as independent verification.**
- A different session of the same model is not a different perspective. When a genuinely different viewpoint is needed, change the family.
- **The mechanical lane is the first gate.** If it fails, don't spin up the LLM lanes (critical, neutral, adversarial) — send it straight back for a fix. Once it passes, launch the LLM lanes **at the same time.** For deliverables with nothing to execute (writing, research), launch the LLM lanes right away.
- **Rows are for writers.** The mechanical gate runs as a `run` node (no model, no pane). Critical and neutral lanes default to `pane: false`: they run through `orch ask` with read-only tools, so the contract carries the diff path and the mechanical result. A reviewer gets a pane only when it must run code to reproduce a finding; adversarial lanes keep their pane.
- Give LLM reviewers test-execution permission too, and require every finding to cite evidence (file:line, a repro command).

### 10.2 Lane definitions

| Lane | Character | Who | Input | Output |
|---|---|---|---|---|
| **Mechanical** | fact-checking | no model: a DAG `run` node or `orch test`; `codex-medium-h` (not the author) when the checks need judgment | the deliverable, list of commands to run | commands, inputs, exit codes, output excerpts, list of failures |
| **Critical** | find defects | a different family from the author | original request, acceptance criteria, the diff/deliverable, mechanical results | list of defects (location, repro scenario, severity) |
| **Neutral** | check against requirements | `gemini-medium-m` | original request, acceptance criteria, the deliverable, evidence of actual behavior. **Never given the author's self-assessment** | requirements checklist |
| **Adversarial** | try to break it | see §9.3 | a specific attack target (concurrency, data loss, security, fact/source, accessibility, or operational failure) | a counterexample, or evidence the attack failed |

### 10.3 Neutral lane output format (fixed)

```markdown
| # | Acceptance criterion | Verdict | Evidence location | Note |
|---|---|---|---|---|
| 1 | ... | met | src/foo.go:42, evidence/test.txt | |
| 2 | ... | not met | — | error message differs from the requirement |
| 3 | ... | cannot confirm from evidence | — | no evidence of actual behavior |

## Added but not requested
- ...
## Confusing from the user's perspective
- ...
```

- **One verdict per item.** Never bundle multiple items into one verdict. Every verdict needs supporting evidence.
- Items a script can check (file existence, command results, formatting) are judged by the mechanical lane, not the LLM — the checklist just copies that result in.
- In full+, shuffle the item order and judge again to check for position bias. If the two verdicts disagree on an item, Head checks it directly.
- The neutral lane is **not about finding defects.** It scores the result against the request. Finding defects is the job of the critical and adversarial lanes.
- "Cannot confirm from evidence" is not a failure — it's **a signal that evidence is missing.** Head assigns the mechanical lane to gather more.

### 10.4 Minimum lanes per mode

| Mode | Mechanical | Critical | Neutral | Adversarial |
|---|---|---|---|---|
| direct | Head directly, if needed | — | — | — |
| light | required (if there's something to run) | optional | required | — |
| full | required | required | if user-facing output (docs, UI, CLI text, API surface) changed | optional |
| full+ | required | required, two different families | required | required |

required = must run · optional = run if useful · two = two of them

In full, skip the neutral lane when the change has no user-facing surface; record the skip in the final report.

### 10.5 Verdicts and the fix loop
- Head checks every finding against primary evidence and files it in `findings.md` under one of four categories:
  - **Confirmed defect**: reproduced or backed by evidence
  - **Plausible risk**: not proven, but has a rationale
  - **Unfounded concern**: contradicted by the evidence
  - **Resolved**
- Confirmed defects go back to an implementer (the original author or another worker), and **only the affected lanes** rerun.
- **The retry cap is 2 fix iterations.** No third attempt at the same plan: Head classifies the cause — design error -> revise decisions.md and contracts; ambiguous requirement -> ask the user; environment problem -> report a blocker; worker capability -> escalate the key (§9) and rerun once.
- **Neither a neutral-lane pass nor a technical reviewer's pass alone is enough to accept the work.**
- **Don't apply a review finding just because it was raised.** Only a finding that's reproduced or proven by a test becomes a confirmed defect. Cross-family review has an asymmetric effect — folding in an unfounded finding from a weaker reviewer can make a stronger author's result worse (§17 C4). Reviewers have no veto.
- State plausible risks in the final report. Don't hide them.

---

## 11. Parallelization (the default)

### 11.1 Principles
- **The default is parallel. Serial is the exception, and it needs a reason.** The only accepted reason is a genuine data dependency — B literally cannot start without A's output.
- **Stages are not barriers.** Recon -> produce -> verify flows shard by shard. As soon as one shard's recon finishes, that shard's produce step starts; once produce finishes, that shard's verification starts. It never waits on other shards.
- **Head decomposes work to minimize the critical path.** The longest dependency chain sets the total time. Split long shards further, and spawn other shards during any wait.
- **Never trade correctness for parallelism.** If parallelism causes write scopes to overlap or makes inputs unstable, serialize just that part.
- **Reads and writes have different defaults.** Read-heavy work — recon, review, verification — is always parallel. Write work (produce) only runs in parallel once it clears the **parallel-write gate** below. Evidence in §17 C1-C2.

**Parallel-write gate (all must hold before spawning multiple writers)**
1. There are 2+ independent deliverables.
2. Each deliverable's owned-file glob doesn't overlap another's.
3. The interface between shards is already fixed in the decision log (§7.1).
4. Concurrent writers <= the worker cap (`ORCH_MAX_WORKERS`, default 12).

If it doesn't pass, one writer proceeds at a time. Recon and verification prep still run in parallel in the meantime.

### 11.2 Parallelization techniques

| Technique | Description | Example |
|---|---|---|
| **Shard split** | Split by target; each shard writes a different output file | per-directory exploration, per-subquestion web research, per-source extraction |
| **Exclusive write scope** | Each produce shard gets a non-overlapping write path; separate worktrees for concurrent writes to the same git repo | per-module implementation workers |
| **Shard-level flow** | No stage barriers — the next stage starts as soon as a shard finishes | shard 2 implementation runs while shard 1 verification runs |
| **Concurrent lanes** | Run all four verification lanes as soon as their inputs are ready | mechanical, critical, and neutral at once |
| **Contract-first** | Fix an interface or schema first, then implement both sides at once | fix an API spec -> build the server, client, and tests together |
| **Speculative parallel** | Start work likely to be needed before it's confirmed needed | check 2-3 root-cause hypotheses at once, investigate a settled part before the design is finalized |
| **Alternative race** | Two families attempt a hard implementation at once; keep the better one. Doubles cost, so reserve it for full+, and only when the first attempt failed or approaches genuinely diverge | in full+, `claude-high-m` and `codex-high-m` implement the same shard |
| **Verification prep ahead of time** | Prepare acceptance criteria, test scenarios, and checklist templates while produce is still running | fix the neutral lane's checklist items in advance |

### 11.3 Dependency management
- Head records the work as a **DAG** (dependency graph) in `run.json`. Each node is one contract; each edge means "needs this result file."
- `orch` reads the DAG and **spawns any node whose inputs are ready right away** — Head doesn't have to judge this by hand every time (extend to an `orch run --dag` form after P6).
- Once the concurrent-worker cap (D5) is hit, **spawn nodes on the critical path first.**

### 11.4 Correctness safeguards
- Only one worker writes to a given checkout. Split into worktrees for concurrent writers.
- If a read-only worker shares a checkout with a writer, the writer that changes its input must not run at the same time — or hand the reader a snapshot (a worktree or commit hash) as input.
- If several shards must change a shared file (config, a lock file, a schema), give that file to **exactly one worker** and have the rest wait for its result. This is a legitimate case for serialization.
- Even after every shard's verification passes, **run one full verification pass on the integrated result.** Shard-level verification can't catch interactions between shards.
- **Merge clean worktrees in one pass, verify once.** Run the build, type check, full test suite, and interface contract tests against the merged tree. Worktrees only prevent file conflicts, not semantic conflicts (§17 C2), and those contract tests are what catch them. On failure, revert merges newest first to find the culprit. Merge one at a time only for worktrees that touch shared files (config, lockfiles, schema).

### 11.5 Example flow

```
time ->
recon        [codex-medium-h:A][codex-medium-h:logs][gemini-medium-m:docs]                ← start together
design            [Head: fix the interface]                     ← waits only on recon A
verify-prep       [gemini-medium-m: checklist template][codex-medium-h: test scenarios]  ← alongside design
produce                 [codex-high-m:1 wt1][codex-high-l:2 wt2][claude-high-m:3 wt3][claude-medium-m:docs]
verify                        [codex-medium-h mech:1][claude-medium-m critical:1][gemini-medium-m neutral:1]  ← as soon as shard 1 finishes
                                    [codex-medium-h mech:2][claude-medium-m critical:2][gemini-medium-m neutral:2]
                                         [codex-medium-h mech:3][codex-high-m critical:3][gemini-medium-m neutral:3]
integrate                                                    [codex-high-m merge][codex-medium-h full tests][gemini-medium-m full checklist]
verdict                                                                         [Head]
```

---

## 12. Playbooks by task type

Playbooks live at `~/.orch/playbooks/<type>.yaml`. Head picks a type, adjusts it if needed, and records the actual plan in `run.json`.

### 12.1 Summary table

| Type | Default mode | Recon | Produce | Verify |
|---|---|---|---|---|
| Feature development | full | `codex-medium-h` per area | `codex-high-m` (or `claude-high-m` if hard; mechanical shards go to `codex-high-l`) per worktree | mechanical `codex-medium-h` + critical (different family) + neutral `gemini-medium-m` if user-facing output changed |
| Bug fix | light -> full | `codex-medium-h` (repro, logs, candidate causes) | `codex-high-m` (`claude-high-m` if the cause spans multiple layers) | mechanical (repro is gone + regression test) + critical |
| Investigation/research | full | `gemini-medium-m` (broad) + `codex-medium-h` (code, data) in parallel | `claude-medium-m` synthesizes | neutral `gemini-medium-m` (source cross-check, a different session) + critical `claude-high-m` (fact rebuttal) |
| Documentation | light | `codex-medium-h`/`gemini-medium-m` gather material | `claude-medium-m` | neutral `gemini-medium-m` + critical `codex-high-m` (facts, consistency) |
| Analysis | full | `codex-medium-h` (data extraction) | `codex-high-m`/`claude-high-m` | mechanical `codex-medium-h` (recompute) + critical (different family) |
| Image generation | light | `gemini-medium-m` (references, style analysis) | whichever CLI has generation tooling (D4) | neutral `gemini-medium-m` (requirements check) + critical `claude-medium-m` (visual quality) |
| Refactoring | full | `codex-medium-h` (impact scope) | `claude-high-m` (core) + `codex-high-l` per mechanical shard | mechanical (full tests, same behavior) + critical `claude-high-m` |
| Design/architecture review | full | `codex-medium-h` + `gemini-medium-m` | `claude-high-m` | critical `codex-high-m` + neutral `gemini-medium-m` if user-facing output changed |
| Simple question, single-file edit | direct | — | Head | Head directly, if needed |

### 12.2 Playbook example (`feature.yaml`)

```yaml
type: feature
default_mode: full
stages:
  recon:
    parallel: by_area
    model: codex-medium-h
    output: results/recon-<area>.md
  design:
    when: "ambiguous || cross_module"
    owner: head
    notes: "The Head writes decisions.md (interfaces, names, file-level plan). Optional helper: claude-high-m (synthesis) only to condense large or conflicting recon evidence; the Head still decides."
  design_check:
    when: "design ran && mode == full+"
    model: other_family(author)
    purpose: verify-critical
    notes: "Author is the Head (spawn with --author-family head); default the lightest other-family key: Head Anthropic -> codex-high-l, Head OpenAI -> claude-medium-m; codex-high-m / claude-high-m only when the design is complex. 5-10 min pre-implementation challenge of decisions.md: missing requirements, conflicts with the existing system, needless abstraction, migration risk, rollback path. No code review."
  produce:
    parallel: by_worktree
    model: codex-high-m
    escalate_to: claude-high-m
    escalate_when: ["failed_once", "cross_cutting"]
  verify:
    start: per_shard          # start as soon as a shard finishes
    lanes:
      mechanical: { run: true, model: codex-medium-h, required: true }   # run node; the model only reads a failure
      critical:   { model: other_family(author), pane: false, required: true }
      neutral:    { model: gemini-medium-m, pane: false, required: false, when: "user_facing || mode == full+", withhold: [self_assessment] }
      adversarial:{ model: other_family(author, critical), when: "mode == full+" }
  integrate:
    owner: head
    mechanics: head
    escalate_to: codex-high-l
    notes: "Clean merge: Head runs git merge and orch test directly, no model. Textual conflicts: codex-high-l. Semantic conflicts: claude-high-m. Merge all clean worktrees in one pass, then run the full suite and the interface contract tests once; on failure, find the culprit by reverting merges newest first. Merge one at a time only for worktrees that touch shared files (config, lockfiles, schema)."
retry_limit: 2
```

### 12.3 Example research flow
1. Head splits the question into 3-6 subquestions and lists what `acceptance.md` needs answered.
2. Recon (parallel): `gemini-medium-m` per subquestion (web, documents), `codex-medium-h` for local code and data. Every result must carry **a source URL or path plus a quote.**
3. Produce: `claude-medium-m` writes the synthesis report. Every claim gets a source number.
4. Verify (parallel):
   - Neutral `gemini-medium-m` (a session that didn't do the recon): item-by-item satisfaction and claim-to-source cross-check
   - Critical `claude-high-m`: try to rebut the core claims, check source credibility
5. Head judges and reports. Leave anything unconfirmed as "unknown."

---

## 13. Wiring into commands (`/goal`, research commands, etc.)

- **Principle: a command decides what to do (the goal, the iteration condition); orch decides who does it.**
- If a command tells Head to use a subagent, parallel agents, or Workflow, Head converts that into an `orch` call. Hooks block the built-in path, so it can't be bypassed by accident.
- `/goal`:
  - One goal = one run. Record the goal ID in `run.json`.
  - Keep panes alive across turns. Once the goal completes, is canceled, or fails, run `orch close --run`.
  - When a goal resumes, restore state with `orch resume --run <id>`.
- Research-style commands follow the flow in §12.3. Whatever output format the command requires goes into the produce-stage contract.
- Before implementing, inventory the commands in use and check each one's delegation style (built-in tool, its own loop, an external call).

---

## 14. Decisions needed

| # | Item | Options | Recommendation |
|---|---|---|---|
| D1 | Claude effort default | high tier: medium/low (never high) · medium tier: medium/high | high tier `medium`. medium tier `medium`; consider `high` when it takes the critical-review lane. **Claude high tier effort: medium or low only; never high.** |
| D2 | Scope of orch application on the Claude side | Deepeet-only / global | Global. Adjust with the mode (direct/light/full) |
| D3 | Built-in Agent exception | block everywhere / allow read-only Explore only | Block everywhere at first. Allow Explore only if that proves too restrictive |
| D4 | Who handles image generation | Codex's image tool / agy's (Gemini) image tool / external | Decide after checking each CLI's image-generation support. Also decide whether to add it to the catalog |
| D5 | Row cap for the right column, and overflow handling | fixed N / based on terminal height | terminal height / `ORCH_MIN_ROW_HEIGHT` (default 2), clamped to 1-`ORCH_MAX_ROWS` (default 20) rows per tab. Extra workers use unfocused overflow tabs; queue at `ORCH_MAX_WORKERS` (default 12), never use the left column (§5.5) |
| D6 | Location of the run directory | `~/.orch/runs` / a hidden directory inside the repo | `~/.orch/runs`, with only a link in the repo |
| D7 | Location and sync of the constitution source | one file + symlinks / a generator script | One shared block in one file, inserted into all three files by a script (since each file also has content of its own) |
| D8 | Copies of orch's skill | two copies / one + symlink | One + symlink. Handle the naming difference between Codex's `hd` and Claude's `herdr` in wording only |
| D9 | Research commands | which CLI, which command | Needs confirming with the user |
| D10 | Codex/agy hook support | present / absent | Confirm; if absent, fall back to `orch spawn`'s own check |

---

## 15. Build order

| Stage | Content | Done when |
|---|---|---|
| P0 | Settle decisions D1-D10 | reflected in this document |
| P1 | Minimal `orch models/spawn/prompt/wait/collect/close/status`. Catalog, account env, `ORCH_ROLE` injection, run directory | `make check` passes. spawn -> prompt -> collect -> close actually works for each of the 5 models. spawn rejects a model outside the catalog and any spawn while `ORCH_ROLE=worker` |
| P2 | Write the constitution block and apply it to all three files. Replace the existing "4 models" rule | Freshly started Claude, Codex, and agy sessions answer role-determination questions per the rule |
| P3 | Claude hooks (block Agent/Workflow, block a worker from creating panes) | Head's Agent calls are blocked with a guidance message. A worker's `herdr pane split` is blocked |
| P4 | Revise orch's skill (trim it to an operating manual, the 6-key assignment table, verification lanes, modes) and merge the copies | both paths point at the same file |
| P5 | Write playbooks (feature, bugfix, research, docs, analysis, image, refactor) | each yaml passes schema validation |
| P6 | `resume`, `gc`, reassignment on capacity exhaustion. Fault-injection tests for the fallback path, and tests that block a worker's herdr/`orch` calls | interrupting and resuming a goal restores its panes. `gc` cleans up orphaned panes after a forced kill |
| P7 | Real-world verification: run one feature-development, one research, and one docs task through full mode end to end. Then build a regression set of about 20 representative tasks and directly measure, in this environment, the effect of parallel vs. single-agent and cross-family vs. same-family review | every run leaves a per-lane result file, all the way through cleanup confirmation |

---

## 16. Risks and mitigations

| Risk | Mitigation |
|---|---|
| A worker ignores the rules and delegates anyway | `orch spawn`'s `ORCH_ROLE` check blocks it in code; Claude adds a hook as a second layer |
| Silently falling back to the CLI's default model | model selection only happens through a catalog key; cr assembles the actual args |
| Orphaned panes | recorded the instant they spawn, closed per run, cleaned by `gc` |
| Capacity exhaustion mid-task | contracts are self-contained by design, reassigned to the next account |
| `gemini-medium-m` getting swayed by the author's own claims | never given the self-assessment, fixed output format, never accepted alone |
| Verification cost and time growing | controlled by mode, per-shard pipelined verification, cheapest suitable model |
| The three constitution files drifting apart | generated from one shared block, compared by `orch doctor` |
| The temptation to judge by model vote | Head classifies against primary evidence, with rationale recorded in `findings.md` |
| An external-write accident | forbidden in the contract's permissions section, and requires separate approval per the constitution. A bypass flag is not a grant of permission |

---

## 17. External case studies and evidence (web research, 2026-09)

Legend: **verified** = opened the original source directly · **search only** = only read a search-result summary. For 2026 arXiv papers, only the abstract was checked. **No experiment specifically covering gpt-6-luna/sol or Gemini Flash (agy) was found.** The numbers below come from other model versions, so they are not carried over directly into this environment — they'll be measured directly with the P7 evaluation set.

### 17.1 Similar projects

| Name | Gist | What this design borrowed |
|---|---|---|
| Herdr docs (verified) herdr.dev/docs/agent-automation/, herdr.dev/llms.txt | agent start/prompt/wait/read, 5 states | Confirm with read after a prompt timeout, then resend (§5.7); only use the pane ID from the return value |
| oh-my-claudecode team (verified) github.com/Yeachan-Heo/oh-my-claudecode | claude/codex/gemini/agy workers in tmux panes, plan->exec->verify->fix | A single owner (Head) drives the execution loop; finished workers terminate |
| Claude Code agent teams (verified) code.claude.com/docs/en/agent-teams | lead + team members, a shared task list, completion hooks | 3-5 team members recommended -> concurrent-writer cap of 4. Team members don't see the lead's conversation -> self-contained contracts. Orphaned-session cleanup |
| Codex subagents (verified) learn.chatgpt.com/docs/agent-configuration/subagents | built-in subagents, a cap on concurrent threads | Recommended mainly for reads. States explicitly that parallel writes increase conflicts -> the parallel-write gate (§11.1) |
| Anthropic's multi-agent research system (verified) anthropic.com/engineering/multi-agent-research-system | orchestrator + parallel subagents | Uses about 15x the tokens of chat, +90.2% research performance, less room for parallelism in coding. Results come back as files, references only get passed along -> §8 thresholds, §7 result files |
| Cognition, "Don't build multi-agents" (verified) cognition.com/blog/dont-build-multi-agents | the opposing view | Parallel workers build conflicting assumptions -> the decision log (§7.1) |
| claude-code-router (search only) github.com/musistudio/claude-code-router | account pools, key rotation, fallback models | Account rotation mirrors the fallback-model structure. Issue #1804 (title only) notes "fallback doesn't actually switch provider" -> fallback fault-injection tests |
| Claude Squad, uzi, Agent of Empires, container-use, Vibe Kanban (search only) | tmux/worktree/container-based parallel agents | Unit of work = a worktree; conflicts surface at merge time; command history is kept for audits |

### 17.2 Evidence applied to this design

| # | Evidence | Source | Where it's applied |
|---|---|---|---|
| C1 | On sequential-reasoning tasks, every multi-agent variant was 39-70% worse. Once single-agent performance passes about 45%, coordination gains shrink or turn negative. Errors amplify 17.2x without central coordination, 4.4x with it | arxiv.org/abs/2512.08296 | §8 thresholds, §11.1 write gate, the Head-centered structure |
| C2 | Across 107,000+ agent PRs, merge-conflict rate was 27.67% (Codex 32.31%, Claude Code 26.86%). Worktrees don't prevent semantic conflicts | arxiv.org/abs/2604.03551 | §11.1 write gate, §11.4 verify the merged tree |
| C3 | If implicit decisions aren't shared, parallel workers build conflicting assumptions | Cognition blog | §7.1 decision log |
| C4 | Cross-review is asymmetric: Claude reviewing Codex went 71.6%->89.7%; Codex reviewing Claude went 91.4%->82.8% (p=.046). Reviewers couldn't run tests in that setup | arxiv.org/abs/2607.21656 | §10.5 only proven findings get applied, no veto. §10.1 give reviewers test-execution permission |
| C5 | Same-family preference bias is 3.4-8.4 points. Reordering flips 55.4% of verdicts. Self-preference bias doesn't shrink as the model improves, but splitting the rubric into separate dimensions cuts it by about 31.5% on average | arxiv.org/abs/2609.17857, arxiv.org/abs/2604.22891, arxiv.org/abs/2410.21819 | §9.3 family constraint, §10.3 one verdict per item plus order shuffling |
| C6 | Rubric-based verification is noisy even for top models. Bundled verdicts hurt accuracy, and majority vote has diminishing returns | arxiv.org/abs/2606.29920 | §10.3 one verdict per item, deterministic items judged by script. No model voting |
| C7 | Declaring a base commit, target resources, and dependencies before work starts, and failing anything that exceeds scope (small sample, only shows feasibility) | arxiv.org/abs/2607.21909 | §7 base commit, owned files, auto-fail on out-of-scope edits |
| C8 | Over-spawning agents for simple work is the most common failure. Simple lookups need 1, comparisons need 2-4 | Anthropic's research post | §8 modes and the delegation break-even point |
| C9 | Multi-agent failures fall into 3 categories and 14 types: design flaws, inter-agent inconsistency, and verification failures | arxiv.org/abs/2503.13657 (MAST) | §16 risk-table checks, P7 evaluation-set classification |

### 17.3 Tensions with this design
- **"Always parallel" vs. C1 and C2.** The evidence shows parallel writes can hurt. So this design splits it: **reads are always parallel; writes are parallel only once they clear the gate.** When the time saved by parallelism is smaller than the cost of merge conflicts and mismatched assumptions, serial is actually more efficient.
- **The value of cross-family review (C4).** Cross-review isn't always a win. So its result is never used as the verdict itself — it's used **as a tool for finding evidence.** Only proven findings get applied.
- **Neutral verdicts from Gemini.** The family-bias research didn't cover Gemini. Position bias and rubric noise persist regardless of family, so §10.3's rules cover that gap.

---

## Appendix A. Rules carried over from the current orch skill

- Head owns judgment, integration, confidence, and the final answer
- Workers report evidence, uncertainty, and blockers. No re-delegation
- Concurrent writers use separate worktrees; one writer per checkout
- Head fills the entire left column; workers stack top-to-bottom in the right column (§5.5 invariants). `--no-focus`, keep `$PWD`, never split Head's pane with `--current`
- Record the pane ID straight from the split response's `.result.pane.pane_id`
- Sessions started with `cr` get `CODEX_HOME`/`CLAUDE_CONFIG_DIR`/`CR_CODEX_NO_DAEMON` passed via `--env`; `--no-daemon` when `CR_CODEX_NO_DAEMON=1`
- Treat `agent_not_ready`, `blocked`, `unknown`, and timeouts as unresolved; confirm with `agent get`/`agent read`
- On success, failure, timeout, cancellation, or early exit, always close owned panes and confirm with `herdr pane list`
- Never search unrelated Slack or other external connectors
- A bypass flag only removes local prompts; it never authorizes an external write or an out-of-scope change

## Appendix B. Code to reuse from cr

| Feature | Location |
|---|---|
| Account priority sort | `main.go` `sortedAccounts()`, `usable()` |
| Account env construction | `provider.go` `providerEnv()` |
| Bypass, `--no-daemon` args | `runner.go` `launchArgs()`, `codexNeedsNoDaemon()` |
| Capacity-error detection | `runner.go` `quotaText()` |
| Shared instruction files, skill linking | `shared_config.go` `ensureSharedAgentConfig()` |
| Registry read | `store.go` `load()` |
| Entry-point pattern | `main.go` `main()`: cr's own entry point only handles `status`, `doctor`, `--serve`; orch ships as its own independent binary with its own entry point, following this pattern rather than adding a branch inside cr |
