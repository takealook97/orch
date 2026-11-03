# Herdr UI

Repository-owned mods and layouts for Herdr.app:

- `mods/usage-bar`: compact system, Codex, Claude, and Gemini cards in a 44 px top row. CPU and RAM have mini gauges; providers show 5h and Week percentages/reset times; Codex adds reset credits and expiry. Percent colors change at 50% and 80%. Missing windows retain a `—` placeholder; expired windows show 0% until refreshed.
- `mods/space-tree`: the companion space/tab/pane sidebar, provided by its own implementation.
- `layouts/deepeet.json`: a clone of shipped `main`, with the usage row above tabs and a 3:1 space-tree/agents sidebar.

The usage bar runs `/Users/deepeet/.local/bin/orch hud sys` every 3 seconds and `orch hud usage --terminals … --max-age 45s` every minute. It reads default accounts without `--terminals`, using `--max-age 120s`, every two minutes. An installed `orch` with HUD subcommands handles provider authentication, caching, backoff, and network access. Click any card to force a refresh; credits are never redeemed.

Only local terminals in the active space choose accounts. The first account for each provider in the filtered response is used, falling back to the first error-free default account with dim styling. Gemini comes from either response without requiring terminal membership. Cards stay in Codex, Claude, Gemini order. Account labels show email local parts only, with no plan or default suffix. Failed reads retain previous values at 60% opacity. The bar has no clock or stale icon.

Space or terminal-set changes schedule a refresh after one second. An active-space agent changing from working to done, idle, or blocked schedules a debounced refresh after two seconds. Event reads respect backend cache floors. The previous usage response remains visible while new data loads; obsolete responses are ignored.

Cards are vertically centered, rounded, subtly raised, and separated by 8 px gaps. Below 980 px, progress bars disappear; below 820 px account lines also disappear; below 700 px Week reset details also disappear. Cards scroll horizontally when necessary. All labels are English and calendar dates use the user's local timezone. See `mods/usage-bar/RECIPE.md` for details.

Install the companion mods and layout after they are present in this repository:

```sh
make herdr
```

This copies each mod into `~/.config/herdr-app/mods/<name>/` using `rsync -a --delete` (excluding `*.test.mjs`) and copies layout JSON into `~/.config/herdr-app/layouts/`. It rejects all shipped mod/layout names before copying anything. Files removed from a repository mod are removed from that installed mod, so keep local changes in the repository. Installation does not switch the layout.

Open the layout chooser with **Ctrl-Shift-L**, or run `layout.switch deepeet` through Herdr's command interface. The original `main` layout remains available.

Validate before installation:

```sh
node --test herdr/mods/usage-bar/
herdr-app check "$PWD/herdr/mods/usage-bar"
python3 -m json.tool herdr/layouts/deepeet.json >/dev/null
make -n herdr
```
