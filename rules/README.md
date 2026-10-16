# Rules

`~/.claude/rules` is a symlink to `rules/claude/`. Claude Code loads every `.md` file in it at session start, so a new file there is live in the next session with no registration step. Keep non-rule files (like this README) out of `rules/claude/`, or they load as rules too.

Scope a rule to matching files with `paths` frontmatter; without it the rule always loads:

```markdown
---
paths:
  - "**/*.test.ts"
---

# Rule title
...
```

Set up the link on a new machine with `ln -s ../.agents/rules/claude ~/.claude/rules`.
