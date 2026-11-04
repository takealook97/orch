# Refinement checklist — 2026-11-04

Baseline revision: 5893d06. Writer scope excludes independent review, commit/push,
installation and run/pane cleanup; those remain Head gates.

1. [x] Git global options: full-hook cases across Claude, Codex and agy in
   `TestRefinementFullHooks` cover known flag/value arities, unknown options,
   quoted concatenation, expansion, separators, substitutions and descriptor writes.
   Applying the original 150-case fixtures (before review additions) to an isolated
   archive of 5893d06 exited 1 with
   84 mismatches; current focused fixtures exit 0. Differences include intended
   denial additions and quoted-data/read-only shell corrections, not 84 distinct bugs.
2. [x] One execution scanner and argv policy: `head_scan.go` emits argv/redirects;
   `head_guard.go` keeps management/edit gates and awk/sed program checks. The
   redundant raw/joined execution regexes and quoted-data masking were removed.
   Worker and pane-creation guards keep their separate existing semantics.
   Unsupported control flow, dynamic executable/script expansion, generic heredocs,
   backticks and malformed syntax delegate; this remains a workflow guard rather than a shell sandbox.
3. [x] Main skill is 75 lines, with mandatory trigger links to advanced procedures.
   All original detailed procedures are retained in the advanced guide. README
   keeps the full generated model catalog; setup/design link to it, constitution
   retains only the tier summary, and models.yaml remains the source. Docs tests
   check references, line budget, catalog duplication and the operations mirror.
4. [x] Explicit local usage collection/export in `usage_collect.go` preserves manual
   import/report. Tests cover final Claude dedup/request consistency, inclusive
   cache semantics, Codex cumulative deltas, replay/growth, mixed/absent models,
   missing/zero/negative/reset counters, malformed/truncated/unknown sources,
   binding/import conflicts, validation before batch writes, and secret nonleak.
5. [x] Existing-source smoke used isolated storage and operator-declared bindings:
   Claude: 164 closed lines, 5 final events, inclusive input 442332, output 4285;
   Codex: 90 closed lines, 11 cumulative intervals, input 510254, output 9496.
   Both have partial coverage, zero missing token pairs, numeric source agreement,
   and unchanged replay. No current Head ownership or savings is established.
   No private session paths/identities or bodies are committed.
6. [x] Final acceptance: focused tests, final make check (including build/docs/vet/
   full Go tests/playbook validation), full go test -race, Python eval tests
   (7 tests) and eval selfcheck all exit 0.
   Initial reviews, repair re-review and final acceptance were accepted by Head.
7. [ ] Head-owned commit/push to main, reinstall/smoke, and owned run/pane cleanup.

## Size and behavior evidence

| Artifact | Before | After |
|---|---:|---:|
| Main skill | 272 lines | 75 lines |
| Advanced guide | 57 lines | 296 lines |
| Head execution guard | 215 lines | 150 lines plus 357 scanner/policy lines |

The guard refactor increases explicit grammar/policy code size while removing
independent matching paths; no line-count reduction is claimed for the guard.
The main skill reduction comes from moving procedures, not deleting gates.

## Known limits

Usage supports the documented local record shapes, not arbitrary provider exports.
Claude observations require a final stop reason; changed finalized usage conflicts.
Codex history must remain append-only; cumulative resets reject the whole batch.
Unclosed final records and missing dimensions stay unavailable/partial. Input
includes Claude cache read/create and Codex cached input; no prices or guessed
billing are inferred. Batch validation is atomic before writes, not a crash-safe
storage transaction. Declared bindings do not independently establish ownership.

## Independent finding repairs

Before/after reproductions use synthetic records and complete hook envelopes;
no live provider calls or private transcript content were used during repair.

| Review finding | Reproduction and disposition |
|---|---|
| critical-guard 1: shell --norc mistaken for -c | Full-hook failure before, pass after; only short-option c selects a script, known long options are separate, unknown long options delegate. --noprofile and safe literal scripts covered. |
| critical-guard 2: dynamic shell -c program | Full-hook failure before, pass after; dynamic script argv delegates. |
| critical-guard 3: builtin eval | Full-hook failure before, pass after; builtin is a recognized wrapper. Other opaque wrappers remain documented limits. |
| critical-guard 4: attached sed -e | Full-hook failure before, pass after; -e and --expression= prefixes feed the existing program policy. --expression= already denied conservatively before; both forms now share inspection. |
| critical-guard 5: planning patch heredoc | Full-hook failure before, pass after; strip only body/operator, preserve header chains and trailing commands, keep path scope and reject unclosed/unquoted heredocs (unquoted bodies can expand commands). |
| critical-guard existing Git config/write options | Full-hook failure before, pass after; -c/--config-env, --output and -O/--open-files-in-pager delegate. Known ordinary inspection remains supported. |
| adversarial 1: copied Claude message in another session | Synthetic failure before, pass after; global message/request identity deduplicates identical copies, keeps original provenance, reports duplicates, rejects conflicting counters/bindings atomically. No provider fork behavior inferred. |
| adversarial 2: Codex gaps | Exact input-gap and fully-missing-gap failures before, pass after; retain missing intervals and dimension coverage; never bridge input/model attribution across unknown snapshots. |
| adversarial 3: missing Claude sessionId | Synthetic failure before, pass after; every usage row requires its own matching identity. |
| adversarial 4: conflicting finals in one batch | Synthetic failure before, pass after; reject changed final observations, ignore incomplete streaming, dedupe identical finals. Growth fixture now uses an incomplete chunk before the final. |
| critical-usage 1: asymmetric subtotals | Gap regression failed before, passes after; sum input/output independently and expose per-dimension observed/missing counts. |
| critical-usage 2: EOF append assumption | No overwrite reproduced; O_APPEND is small storage hardening under the existing lock. Replay/growth tests pass. |
| neutral | No implementation finding; entry skill, model catalog and operations mirror checks retained. |

Sed cluster follow-up: full-hook regressions for `-ne`/`-Ene` write programs,
`-nf` and unknown options failed before the fix; attached and separated safe
expressions, `-E` substitutions, in-place clusters, and `--expression` are covered.
The focused hook and existing guard tests pass. After the fix, serial `make check`
passed (full Go tests 63.039s; playbook schema check passed), followed by
`go test -race ./...` (62.860s). Independent final review and release remain pending.

Repair evidence: `go test ./... -run 'TestRefinementFullHooks|TestRepair|TestCollect|TestMeasurement|TestHead' -count=1` passes.
Final repair `make check` exits 0 (build, generated-doc check, vet, full Go
tests in 53.461s and playbook schema check); `go test -race ./...` exits 0
(full Go race tests in 64.319s). An earlier check caught a missing generated
operations-mirror header, now corrected. A subsequent concurrent check hit the
existing capacity mock 1000ms timeout; the final serial full check passes. Python eval code is unchanged; prior 7-test/selfcheck evidence is
retained without another Python run. Independent repair re-review and release
(commit/push/install) remain pending with the Head.

An initial Claude smoke rejected an application-version string as a schema number;
the parser now distinguishes application version from explicit schema_version and
the corrected smoke passes. Intentional baseline regression failures are comparison
evidence. The old collector session-scoped identity format lacks global message/request provenance; matching legacy messages fail closed, and old collected stores require explicit reconciliation before combining with new observations. Manual import compatibility is preserved. Unknown binaries/wrappers remain workflow-guard limits described in operations.md.

## Head acceptance

Head accepted the independent review and recorded test evidence. Main skill: 272 to 75 lines; common scanner/policy increases guard code size. Real usage smoke uses operator-declared bindings with partial coverage; no savings claim is established.
