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
        run env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go -C "$root" build -o "$binary" . || return 1
        built+="$target "
    fi
    run hc exec --on "$machine" sh -c 'mkdir -p "$HOME/.local/libexec"' || return 1
    if "$dry_run"; then
        printf '+ hc file write --on %q .local/libexec/orch-hud < %q\n' "$machine" "$binary"
    else
        hc file write --on "$machine" .local/libexec/orch-hud < "$binary" || return 1
    fi
    run hc exec --on "$machine" sh -c 'chmod +x "$HOME/.local/libexec/orch-hud"' || return 1
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
