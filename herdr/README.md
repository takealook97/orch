# Herdr UI

Repository-owned mods and layouts for Herdr.app:

- `mods/usage-bar`: CPU/RAM, the active space's Codex and Claude account usage, reset times, and Codex reset coupon counts/expiry. Percent colors change at 50% and 80%. Expired windows show 0% until refreshed.
- `mods/space-tree`: the companion space/tab/pane sidebar (provided by its own implementation).
- `layouts/deepeet.json`: a clone of shipped `main`, with a 44 px usage row above tabs and a 3:1 space-tree/agents sidebar.

The usage bar runs `/Users/deepeet/.local/bin/orch hud sys` every 5 seconds and `orch hud usage --terminals … --max-age 120s` every minute. It reads default accounts without `--terminals` every five minutes. It needs an installed `orch` with the `hud` subcommands. Provider network/authentication access happens inside that command; the mod does not read credentials. Click any group to force a usage refresh. Coupons are displayed only; clicks never redeem them.

Only local terminals in the active space are considered. Each provider with no matching agent falls back to its first account without an error, dimmed and marked `(기본)`. Failed reads retain the last data with a stale marker. Before any successful read, a failed command or invalid JSON shows `orch hud 없음`.

At widths below 900 px the system details disappear; below 780 px account label/plan lines disappear; below 660 px weekly cells retain text without progress bars. Extra accounts can be scrolled horizontally. Dates use KST and Korean labels.

Install both companion mods and the layout after they are present in this repository:

```sh
make herdr
```

This copies each mod into `~/.config/herdr-app/mods/<name>/` using `rsync -a --delete` (excluding `*.test.mjs`) and copies layout JSON into `~/.config/herdr-app/layouts/`. It rejects all shipped mod/layout names before copying anything. Files removed from a repo mod are removed from that installed mod, so keep local changes in the repository. Installation does not switch the layout.

Open the layout chooser with **Ctrl-Shift-L**, or run `layout.switch deepeet` through Herdr's command interface. The original `main` layout remains available.

Validate before installation:

```sh
node --test herdr/mods/usage-bar/format.test.mjs
herdr-app check "$PWD/herdr/mods/usage-bar"
python3 -m json.tool herdr/layouts/deepeet.json >/dev/null
make -n herdr
```
