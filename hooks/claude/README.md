# Claude Code hooks

Scripts here run only once registered in `~/.claude/settings.json` (not tracked in this repo). Point the command straight at this folder, no symlink:

```json
"hooks": {
  "PreToolUse": [
    {
      "matcher": "Bash",
      "hooks": [
        { "type": "command", "command": "\"$HOME/.agents/hooks/claude/<name>.sh\"" }
      ]
    }
  ]
}
```

- Make the script executable (`chmod +x`).
- The hook reads its event as JSON on stdin. Exit 0 to allow, exit 2 to block (stderr goes back to Claude).
- Event names, matchers and the JSON shape: https://docs.claude.com/en/docs/claude-code/hooks
