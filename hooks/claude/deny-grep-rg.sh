#!/usr/bin/env bash
# PreToolUse hook (matcher: Bash). Blocks rule deny-grep-rg from the shared
# guard: grep, egrep, fgrep, ag or git grep over files or a directory, and rg
# too while orch is installed (with no orch, rg is the preferred grep).
# Does not touch: a grep filtering another command's output (go test ./... |
# grep FAIL), grep -q as a condition, grep reading stdin, a quoted word, and
# orch ctx itself.
# Replacement: one orch ctx batch, else rg, else the built-in Grep tool.
set -u
unset CDPATH
dir=$(cd -- "$(dirname -- "$0")" && pwd) || exit 0
command -v python3 >/dev/null 2>&1 || exit 0
exec python3 "$dir/toolguard.py" grep-rg
