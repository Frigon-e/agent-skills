#!/usr/bin/env bash
# PreToolUse hook (matcher: Grep|Glob). Blocks two rules from the shared guard:
#   deny-builtin-grep  Claude's own Grep tool, which reads file contents.
#   deny-builtin-glob  Claude's own Glob tool, which finds files by name.
# Both reach the tree without a shell, so a shell rule alone leaves them as the
# way around the guard. They are blocked only while orch is healthy: with no
# usable orch they are the fallback, so the PreToolUse event is allowed through.
# Does not touch: a search whose path is absolute and outside the session's git
# repository, where orch ctx has no answer (for example under /tmp or
# ~/.claude), a search with no pattern, and any other tool.
# Replacement: one orch ctx batch, in the same batch syntax the shell rules use.
# Fail open on a payload this script cannot read.
set -u
unset CDPATH
dir=$(cd -- "$(dirname -- "$0")" && pwd) || exit 0
command -v python3 >/dev/null 2>&1 || exit 0
exec python3 "$dir/toolguard.py" builtin-search
