#!/usr/bin/env bash
# PreToolUse hook (matcher: Bash). Blocks rule deny-cat-head-tail from the
# shared guard: a bare cat, head, tail or sed -n over exactly one file, which
# floods the context with raw text.
# Does not touch: tail -f/--follow, several files at once, a pipe on either
# side (cat f | grep TODO, head -n 5 f | wc -l), a redirect (cat f > copy) and
# cat reading stdin.
# Replacement: the built-in Read tool with offset and limit (a section or the
# whole file), whether or not orch is installed.
set -u
unset CDPATH
dir=$(cd -- "$(dirname -- "$0")" && pwd) || exit 0
command -v python3 >/dev/null 2>&1 || exit 0
exec python3 "$dir/toolguard.py" cat-head-tail
