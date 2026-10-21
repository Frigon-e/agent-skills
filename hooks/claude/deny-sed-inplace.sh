#!/usr/bin/env bash
# PreToolUse hook (matcher: Bash). Blocks rule deny-sed-inplace from the shared
# guard: sed/gsed with -i, -i.bak, -i '' or --in-place, and perl with -i or -pi,
# all of which rewrite a file instead of printing to stdout.
# Does not touch: sed that prints (sed 's/a/b/' file), a piped transform
# (git diff | sed 's/^/  /'), grep -i, and sed reading stdin.
# Replacement: the built-in Edit tool. orch ctx is read-only.
set -u
unset CDPATH
dir=$(cd -- "$(dirname -- "$0")" && pwd) || exit 0
command -v python3 >/dev/null 2>&1 || exit 0
exec python3 "$dir/toolguard.py" sed-inplace
