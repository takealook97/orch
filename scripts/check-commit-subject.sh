#!/usr/bin/env bash
set -euo pipefail

subject="${1:-}"
pattern='^(feat|fix|refactor|docs|test|build|ci|chore|perf|security|revert): [a-z][ -~]*$'

if [[ ${#subject} -gt 72 ]] || [[ ! "$subject" =~ $pattern ]] || [[ "$subject" == *. ]]; then
  printf 'Invalid commit subject: %s\n' "$subject" >&2
  printf 'Use English type: imperative summary (max 72 chars, no final period).\n' >&2
  exit 1
fi
