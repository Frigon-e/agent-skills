#!/usr/bin/env bash
# PreToolUse hook (matcher: Bash). Reminds Claude to use the Read tool instead of
# cat/head/tail/sed/less/more/bat/nl when a command only reads a file.
# Non-blocking: the command still runs; the reminder is added to Claude's context.
set -u

cmd=$(jq -r '.tool_input.command // empty' 2>/dev/null) || exit 0
[ -n "$cmd" ] || exit 0

# Split on ; && || and newlines (not on single pipes), so each line starts a command.
segments=${cmd//&&/$'\n'}
segments=${segments//||/$'\n'}
segments=${segments//;/$'\n'}

hit=""
while IFS= read -r seg; do
  seg=${seg%%|*}                       # drop pipe targets: `x | head` filters stdin, not a file read
  seg="${seg#"${seg%%[![:space:]]*}"}" # trim leading space
  [[ $seg =~ ^(cat|head|tail|less|more|bat|nl|sed)([[:space:]]|$) ]] || continue
  tool=${BASH_REMATCH[1]}
  case $seg in *'<<'* | *'>'*) continue ;; esac # heredocs and redirects are writes
  [[ $tool == sed && $seg =~ [[:space:]]-[a-zA-Z]*i ]] && continue # sed -i edits in place
  [[ $tool == tail && $seg =~ [[:space:]]-[a-zA-Z]*[fF] ]] && continue # tail -f follows a live log

  # Count non-flag args: a file is needed (sed also takes a script first).
  read -r -a words <<<"$seg"
  args=0
  for w in "${words[@]:1}"; do
    [[ $w == -* ]] || args=$((args + 1))
  done
  need=1
  [ "$tool" = sed ] && need=2
  [ "$args" -ge "$need" ] && { hit=$tool; break; }
done <<<"$segments"

[ -n "$hit" ] || exit 0

jq -n --arg t "$hit" '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    additionalContext: ("Reminder: do not read files with `" + $t + "` in Bash. Use the Read tool instead (pass offset and limit for part of a file). Read output has line numbers, so cited path:line stay correct.")
  }
}'
