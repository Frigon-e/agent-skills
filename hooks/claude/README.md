# Claude Code hooks

Scripts here run only once registered in `~/.claude/settings.json` (not tracked in this repo). Point the command straight at this folder, no symlink:

```json
"hooks": {
  "PreToolUse": [
    {
      "matcher": "Bash",
      "hooks": [
        { "type": "command", "command": "\"$HOME/.agents/hooks/claude/deny-find-chains.sh\"" },
        { "type": "command", "command": "\"$HOME/.agents/hooks/claude/deny-sed-inplace.sh\"" },
        { "type": "command", "command": "\"$HOME/.agents/hooks/claude/deny-cat-head-tail.sh\"" },
        { "type": "command", "command": "\"$HOME/.agents/hooks/claude/deny-grep-rg.sh\"" }
      ]
    }
  ]
}
```

- Make each script executable (`chmod +x`).
- The hook reads its event as JSON on stdin. Exit 0 to allow, exit 2 to block (stderr goes back to Claude).
- Event names, matchers and the JSON shape: https://docs.claude.com/en/docs/claude-code/hooks

## Tool guard

These are the Claude side of the pi guard in `skills/orchestrate/tool/pi_ctx_reminder.ts`, which stays the source of truth. `toolguard.py` is a line for line port of its five rules; each shell script below is a thin wrapper that names the rule group it enforces, so a script is one file to read and one block list to change.

| Script | Rule | Blocks | Does not touch |
|---|---|---|---|
| `deny-find-chains.sh` | `deny-find-chains`, `deny-find` | `find` with `-exec/-execdir/-ok/-okdir/-delete`; a search tool (`find`, `fd`, `rg`, `ag`) feeding `grep`/`rg`/`ag` or `xargs grep`; a plain `find` over a tree | a `find` reading stdin (it formats a pipe), a quoted word or a path that merely contains `find`, `fd` alone |
| `deny-sed-inplace.sh` | `deny-sed-inplace` | `sed`/`gsed` with `-i`, `-i.bak`, `-i ''` or `--in-place`; `perl` with `-i` or `-pi` | `sed` that prints to stdout, a piped transform (`git diff \| sed 's/^/  /'`), `grep -i` |
| `deny-cat-head-tail.sh` | `deny-cat-head-tail` | a bare `cat`, `head`, `tail` or `sed -n` over exactly one file | `tail -f`/`--follow`, several files, a pipe on either side, a redirect, `cat` reading stdin |
| `deny-grep-rg.sh` | `deny-grep-rg` | `grep`, `egrep`, `fgrep`, `ag`, `git grep` over files or a directory, and `rg` too while `orch` is on PATH | a `grep` filtering another command's output (`go test ./... \| grep FAIL`), `grep -q` as a condition, `grep` reading stdin, a quoted word, `orch ctx` itself, and `rg` when `orch` is not installed |

The replacement is the same order as pi: one `orch ctx` call first when `orch` is on PATH, then `fd` for a file search and `rg` for a content search, then the built-in Claude tools (`Glob`, `Grep`, `Edit`). Two rules name one fixed tool whichever tools are installed: `sed -i` always names `Edit`, because `orch ctx` is read-only, and `deny-cat-head-tail` always names `Read` with offset and limit (a section or the whole file), never `orch ctx`.

A blocked command stays blocked on every retry. Two switches are the human escapes, same as pi:

- `ORCH_TOOL_GUARD=off` disables all four scripts.
- `ORCH_TOOL_GUARD_SKIP=find-chains,sed-inplace,cat-head-tail,grep-rg` disables rules by short name or full name. Plain `find` has its own id, `find` (or `deny-find`), inside `deny-find-chains.sh`.

Two more switches are for the message, not for what is blocked:

- `ORCH_TOOL_GUARD_HINT=pattern` makes a `deny-grep-rg` block end with one line built from the pattern that was typed, instead of leaving the model to pick the kind itself: `orch ctx sym <Name> --refs 3` for one identifier, `orch ctx text '<pattern>' --glob '<dir>/**'` for a string, a route or a path, and `orch ctx find "Code that <words>"` for a broad pattern (an alternation of three or more, a `.*` gap, four or more words, a question, or a regex character class). Unset or `generic` keeps the text above, so the two can be A/B tested. Without `orch` on PATH nothing is added, and no other rule changes.
- `ORCH_TOOL_GUARD_LOG=<path>` appends one JSON line per block: `ts`, `harness` (`claude`), `rule`, `hint` (`generic` or `pattern`), `kind` (`symbol`, `text`, `concept`, or empty for a rule with no pattern) and `command` cut at 200 characters. Off by default, and it is the only thing the guard writes.

`prefer-read-tool.sh` stays. It is a non-blocking reminder for the wider reader set (`less`, `more`, `bat`, `nl`, and `sed` without `-n` as well), so it complements `deny-cat-head-tail.sh`; it is not replaced by it. Register both if you want the block and the reminder.

## Tests

`toolguard_cases.json` is one decision table shared with the pi guard: every case names the command, the PATH situation (`orch` on PATH, `fd` and `rg` but no `orch`, or neither) and the rule that must block it, or no rule when it is allowed. Its `patternKinds` table is the second half: each pattern and the kind `deny-grep-rg` must give it, at least eight per kind, run through both implementations so the tailored hint cannot drift. `toolguard_test.py` runs every case through the real shell scripts with only stub tools on PATH, plus `bash -n`/`shellcheck`, the two switches, the non-Bash and broken-stdin paths, the pattern hint in both modes (including the generic reason byte for byte and the A/B log), and the task's required examples:

```sh
python3 hooks/claude/toolguard_test.py
```

The pi extension runs the same file in `TestCtxReminderSharedToolguardCases` (`skills/orchestrate/tool/ctx_reminder_test.go`) and compares blocked vs allowed and the rule name, so a disagreement between the two implementations fails a test.
