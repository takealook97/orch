# Usage bar

A 44 px top bar shows a system card followed by compact Codex, Claude, and Gemini cards. Each provider has a name, an account local part, 5h and Week usage, and reset details. Codex also shows available reset credits and their next expiry. All colors come from the current Herdr theme.

## What you see

The chrome row has 12 px outer padding, a 1 px bottom border, and 8 px gaps between 36 px tall cards. Cards use a raised background, a 1 px border, 6 px rounded corners, and 8 px horizontal padding. Content is vertically centered.

The system card has two mini rows: `CPU 24%` with a 40 px by 2 px progress bar, and `RAM 94%` with another bar and dim `15/16G` detail. CPU appears once.

A provider card has a 12 px semibold name over its 10.8 px dim account local part. Plans and default-account suffixes are absent. Gemini has only its provider name. Each window has a dim `5h` or `Week` label, a colored percentage, and a 36 px by 2 px bar on the first line. The second line shows `↻ in 42m`, `↻ in 2h 13m`, `↻ Tomorrow 15:37`, or `↻ Tue 11/10 10:59`. Calendar times follow the user's local timezone. An elapsed reset shows 0% and `↻ soon`. Missing windows retain their column with `—` and an empty reset line.

Values below 50% use foreground, 50–79% working, and 80%+ blocked. The Codex credit column reads `2 resets` over `until 12/1`; expiry within three days uses blocked color and `D-2`. No credit icon is shown. Default cards and stale values have 60% opacity. There is no update clock or stale icon.

Below 980 px, hide progress bars. Below 820 px, also hide account local parts. Below 700 px, also hide Week reset details and narrow that column. Cards remain in system, Codex, Claude, Gemini order; horizontal scrolling handles widths that cannot fit all four.

## How it behaves

Only local terminals in the active space choose accounts. For each provider, use the first account in the filtered response; otherwise use the first error-free account in the default response, dimmed. Gemini can come from either response without terminal membership. Keep all three provider cards visible with placeholders before data arrives.

Click a card to read system data and force usage/default refresh. Mouse-down stops window dragging on the cards. Credits are display-only and are never redeemed. Failed process/JSON reads keep the previous response and dim affected values. Partial account failures retain that same account's prior windows. Obsolete responses from a previous space are ignored; the last response stays visible while its replacement loads.

System data refreshes every 3 seconds. Active-space usage refreshes every 60 seconds with `--terminals <comma-list> --max-age 45s`. Default usage is read without `--terminals` every 120 seconds with `--max-age 120s`. An empty terminal set never becomes an unfiltered active query. Provider-specific cache floors and backoff belong to the HUD backend, including Claude's 120 second floor and Gemini's 300 second floor.

A changed active space or local terminal set schedules usage refresh after one second. When an active-space agent transitions from working to done, idle, or blocked, refresh after two seconds. These event reads are debounced and use the cache rather than `--refresh`. Render compares the live snapshot and schedules a callback; context updates and refresh work happen outside render. Polls and commands use `cx.timer` and `cx.spawn`, with no animation timers.

## Built from

`main.js` exports a `View`; `ui.js` builds GPUI card elements; `format.js` contains pure local-time and account-selection helpers. App data comes from `spaces()`, `panes()`, `agents()`, and `theme()`. All system/provider work runs through `exec()` using `/Users/deepeet/.local/bin/orch hud sys|usage`. Authentication and network access stay inside that command. The mod never reads credentials.

`mod.json` uses `place: none` and title Usage. The companion `deepeet` layout inserts a 44 px usage block above tabs. Never overwrite shipped mod or layout names.

## Verification

Run `node --test herdr/mods/usage-bar/` and `herdr-app check "$PWD/herdr/mods/usage-bar"`. Tests exercise English/local-time formatting (including a non-KST timezone and DST), rounding at 24 hours, fallback selection, Gemini, missing windows, responsive element descriptions, polling arguments, debounce transitions, stale retention, and in-flight space changes with a fixture-shaped HUD response.

The app check loads initial descriptions; it does not lay out later async states. Actual installed-layout spacing, scrolling, clicking, and live refresh should be inspected after the separately authorized installation. No preview image is bundled with this source change.
