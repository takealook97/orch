# Usage bar

A slim top bar shows CPU and memory, followed by each active-space Codex and Claude account's usage and reset time. Codex also shows reset coupon counts and the next expiry. A small timestamp marks the oldest account reading. The bar uses the current Herdr theme.

![Usage bar](preview.png)

The preview is to be captured after installing and selecting the companion layout; it is not included in this source-only implementation.

## What you see

A 44 px high, full-width chrome row sits above tabs, with 12 px outer padding and a 1 px bottom border. Left to right: CPU/RAM, one group per Codex account, then one per Claude account, then the update time at the right. Groups have a 1×24 px border-color divider with 10 px horizontal margins. Cells are 100 px wide with 6 px inner horizontal padding. The first line has a dim label and a 12 px semibold value. Percent cells have a 2 px rounded bar; the second line is dim 10.8 px text. Values below 50% use foreground, 50–79% working, and 80%+ blocked. Coupon counts use accent (dim when zero); expiry within three days uses blocked and `D-N`.

Account headings show Codex/Claude and a second line with label and plan. Missing 5h windows have no 5h cell. RAM detail looks like `22.1/36G`. Resets look like `↻ 42분`, `↻ 2시간 13분`, `↻ 내일 15:37`, or `↻ 11/10(화) 10:59`; an elapsed reset shows 0% and `↻ 곧 갱신`.

## How it behaves

Click a group to read system data again and force account/default usage refresh. Hover gives a refresh hint. Mouse-down stops propagation so clicking a group does not drag the window. There is no keyboard focus or animation. Working/blocked are percentage color roles, not agent-state indicators; done/idle agent states do not change this bar.

Only local terminals in the active space choose accounts. When a provider has no matching account, show its first default account without error, dimmed with `(기본)`. A failed read keeps previous data, dims it, and shows a small stale mark plus an amber `⚠` timestamp. If no data has ever loaded and the command fails, show `orch hud 없음`. Changing the active terminal set triggers a read after a one-second debounce; obsolete responses are ignored.

Below 900 px hide the system detail line, below 780 px hide account label/plan lines, and below 660 px hide weekly progress bars while retaining their text. Overflow accounts scroll horizontally; the timestamp stays at the right.

## Built from

`main.js` exports a `View`, with `h_flex`/`v_flex`/`div` UI helpers in `ui.js` and pure formatting/account selection in `format.js`. It calls `spaces()`, `panes()`, `theme()`, and `exec()` from `herdr`. Timers use `cx.timer.every` and `cx.timer.after`; commands run through `cx.spawn`. No props, shared state, art assets, or shipped mod imports are required. `mod.json` uses `place: none`, title Usage. The companion `deepeet` layout inserts a `usage` block above `tabs`, and uses `space-tree` above the shipped `agents` block in the sidebar.

The absolute `ORCH` constant is `/Users/deepeet/.local/bin/orch`. Run `hud sys` every 5 s, active-terminal `hud usage --terminals <comma-list> --max-age 120s` every 60 s, and unfiltered default usage every 300 s. Clicks append `--refresh` to usage reads. The program owns all authentication and provider network access. This mod never redeems coupons or writes credentials.

## Watch out

Never overwrite shipped mod/layout names. Build against the documented `hud` JSON shape; do not derive account homes from the UI or inspect credentials here. Exclude remote addresses containing `/` and mod panes without terminals. An empty terminal list must not become an unfiltered active-account query. Keep last good responses on process, JSON, or provider failures. Defaults are the first error-free accounts in provider order, not a guessed `default` id. Use only theme colors and no animation timers. Formatting dates uses KST; November 10, 2026 is Tuesday. The default fallback can hide a failing default account until an error-free one is available. A preview and installed-layout interaction verification require the separate installation step.

## Done when

`node --test herdr/mods/usage-bar/format.test.mjs` passes the reset, threshold, expiry, byte and account selection branches. `herdr-app check "$PWD/herdr/mods/usage-bar"` renders without error, including with populated fixtures at wide and narrow widths. The layout passes `python3 -m json.tool`, and `make -n herdr` shows only scoped mod/layout copies. After installation, changing spaces changes displayed accounts within the debounce and command latency; clicking refreshes usage; stopping the data provider leaves stale values visible; coupons never invoke a redemption action.
