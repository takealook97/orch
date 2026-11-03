#!/usr/bin/env bash
set -euo pipefail

# Dry-run performs only machine discovery and uname; every mutation is printed.
dry_run=false
machines=()
for arg in "$@"; do
    case "$arg" in
        --dry-run) dry_run=true ;;
        --help|-h) echo "Usage: $0 [--dry-run] [machine ...]"; exit 0 ;;
        -*) echo "Unknown option: $arg" >&2; exit 2 ;;
        *) machines+=("$arg") ;;
    esac
done
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
command -v hc >/dev/null || { echo 'hc is required' >&2; exit 1; }
command -v python3 >/dev/null || { echo 'python3 is required' >&2; exit 1; }
listing=$(hc machine ls --json)
connected=$(printf '%s' "$listing" | python3 -c '
import json, sys
value = json.load(sys.stdin)
for machine in value["machines"]:
    if machine.get("state") == "connected":
        print(machine.get("name") or machine["id"])
')
print_command() {
    printf '+ '
    printf '%q ' "$@"
    printf '\n'
}
run() {
    if "$dry_run"; then print_command "$@"; else "$@"; fi
}
# hc file write takes at most 8 MB per call: send 7 MB parts, join them remotely and
# install only when the joined file has the local sha256.
upload() {
    local machine=$1 binary=$2 parts sum part n=0 remote=.local/libexec/orch-hud
    parts=$(mktemp -d) || return 1
    split -b 7m "$binary" "$parts/p." || { rm -rf "$parts"; return 1; }
    sum=$(shasum -a 256 "$binary" | cut -d' ' -f1)
    for part in "$parts"/p.*; do
        hc file write --on "$machine" "$remote.part.$n" < "$part" >/dev/null || { rm -rf "$parts"; return 1; }
        n=$((n + 1))
    done
    rm -rf "$parts"
    hc exec --on "$machine" sh -c '
        dir="$HOME/.local/libexec"; n=$1; want=$2; i=0; : > "$dir/orch-hud.new"
        while [ "$i" -lt "$n" ]; do cat "$dir/orch-hud.part.$i" >> "$dir/orch-hud.new"; i=$((i + 1)); done
        rm -f "$dir"/orch-hud.part.*
        got=$( (sha256sum "$dir/orch-hud.new" 2>/dev/null || shasum -a 256 "$dir/orch-hud.new") | cut -d" " -f1)
        [ "$got" = "$want" ] || { rm -f "$dir/orch-hud.new"; echo "sha256 mismatch" >&2; exit 1; }
        chmod +x "$dir/orch-hud.new" && mv "$dir/orch-hud.new" "$dir/orch-hud"
    ' sh "$n" "$sum"
}
built=' ' 
install_machine() {
    local machine=$1 platform target goos goarch binary output
    if ! platform=$(hc exec --on "$machine" uname -sm); then
        echo "$machine: FAILED (uname)" >&2
        return 1
    fi
    case "$platform" in
        'Darwin arm64') target=darwin-arm64; goos=darwin; goarch=arm64 ;;
        'Linux x86_64') target=linux-amd64; goos=linux; goarch=amd64 ;;
        'Linux aarch64') target=linux-arm64; goos=linux; goarch=arm64 ;;
        *) echo "$machine: SKIPPED (unsupported platform: $platform)"; return 0 ;;
    esac
    binary="$root/bin/orch-hud-$target"
    if [[ "$built" != *" $target "* ]]; then
        run mkdir -p "$root/bin" || return 1
        run env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go -C "$root" build -trimpath -ldflags "-s -w" -o "$binary" . || return 1
        built+="$target "
    fi
    run hc exec --on "$machine" sh -c 'mkdir -p "$HOME/.local/libexec"' || return 1
    if "$dry_run"; then
        printf '+ upload %q to %q:.local/libexec/orch-hud in 7 MB parts, verify sha256, install\n' "$binary" "$machine"
    else
        upload "$machine" "$binary" || return 1
    fi
    if "$dry_run"; then
        print_command hc exec --on "$machine" sh -c '"$HOME/.local/libexec/orch-hud" hud sys'
        echo "$machine: DRY-RUN ($target)"
    else
        output=$(hc exec --on "$machine" sh -c '"$HOME/.local/libexec/orch-hud" hud sys') || return 1
        printf '%s' "$output" | python3 -c '
import json, sys
value = json.load(sys.stdin)
assert isinstance(value.get("cpu_pct"), (int, float)), "missing cpu_pct"
assert not value.get("error"), value.get("error")
assert value.get("mem_total_bytes", 0) > 0, "missing memory total"
' || return 1
        echo "$machine: OK ($target)"
    fi
}
failed=0
if ((${#machines[@]})); then
    for machine in "${machines[@]}"; do
        if ! printf '%s\n' "$connected" | python3 -c 'import sys; sys.exit(sys.argv[1] not in sys.stdin.read().splitlines())' "$machine"; then
            echo "$machine: FAILED (not connected)" >&2
            failed=1
            continue
        fi
        if ! install_machine "$machine"; then echo "$machine: FAILED" >&2; failed=1; fi
    done
else
    while IFS= read -r machine; do
        [[ -n "$machine" ]] || continue
        if ! install_machine "$machine"; then echo "$machine: FAILED" >&2; failed=1; fi
    done <<< "$connected"
fi
exit "$failed"
