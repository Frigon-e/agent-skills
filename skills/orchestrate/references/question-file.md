# Question card file

A lead writes one card per open `needs-decision` or `blocked` line at
`<task dir>/questions/<key>.md`, where `<key>` is the `--key` of that report.
`orch ask` turns the open cards into one page for the user. A decision with
no card still appears, as a free-text question using the status line.

## Format

```
---
question: Does a script make the question page, or does the orchestrator write it?
recommend: script
change: easy
---
## Context
Markdown, 2-3 sentences.

include: diagram.svg

## Options
### script: A script makes the page
Gain: ...
Cost: ...

### orch: The orchestrator writes the page
Gain: ...
Cost: ...
include: shot.png Screenshot of the current page

## Next
Markdown.
```

- Front matter is `key: value` lines between `---` lines. `question` is
  required. `recommend` is optional and must be an option id. `change` is
  optional: `easy` or `hard` (how costly it is to change the answer later).
- Sections are `## Context`, `## Options`, `## Next`. Any other `## `
  section renders after Context.
- Under Options, each `### <id>: <label>` starts an option. The id is a
  lowercase slug (letters, digits, `-`); the body is markdown. No options
  means a free-text question. One option is an error.

## Includes

A line that is exactly `include: <relpath>` or `include: <relpath> <alt text>`
in a section is replaced by the rendered file. `relpath` is relative to the
`questions/` directory.

| Extension | Result |
|---|---|
| `.md` | Rendered markdown (raw HTML is dropped). |
| `.html`, `.svg` | Inserted as is. |
| `.mmd`, `.mermaid` | Mermaid diagram. |
| `.png`, `.jpg`, `.jpeg`, `.webp`, `.gif` | Image; alt text is required. |

Limits: the file must resolve (symlinks followed) inside your task directory,
exist, and be at most 1 MiB. Other extensions are refused.

If a card fails to parse or an include is refused, `orch ask` still builds the
page: that question shows as a free-text fallback with a warning naming the
error, and the warning goes to stderr. Fix the card and rerun.
