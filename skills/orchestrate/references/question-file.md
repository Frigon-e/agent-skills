# Question card file

A lead writes one card per open `needs-decision` or `blocked` line at
`<task dir>/questions/<key>.md`, where `<key>` is the `--key` of that report.
`orch ask` turns the open cards into one page for the user. A decision with
no card still appears, as a free-text question using the status line.

Outside orchestrate (for example the `brainstorm` skill), `orch ask --cards
<topic>/questions` renders every `<key>.md` in that folder, with no run and
no `orch send` line. Move an answered card to `questions/answered/` so it
leaves the page. Includes are confined to `<topic>/`, and the page goes to
`<topic>/ask/` unless `--out` says otherwise.

## Format

```
---
question: Does a script make the question page, or does the orchestrator write it?
recommend: script
change: easy
---
## Context
Markdown, 4-8 sentences or a short list: goal, state (with the files or
numbers you checked), cause, constraints, and what you have not verified.
Do not repeat the options here.

include: diagram.svg How a question moves from lead to you

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
| `.html` | Inserted after cleaning: scripts, event handlers, form controls, ids and `javascript:` links are removed. Layout, `class` and `style` stay. |
| `.mmd`, `.mermaid` | Mermaid diagram. |
| `.svg`, `.png`, `.jpg`, `.jpeg`, `.webp`, `.gif` | Image; alt text is required. An SVG runs no script. |

Limits: the file must resolve (symlinks followed) inside your task directory,
exist, and be at most 1 MiB. Other extensions are refused.

SVG diagrams: the page can be 360px wide, and an image only shrinks. Draw
for a viewBox about 420 wide, with text at 14px or more, and stack steps
from top to bottom, not side by side. An `<img>` does not take the page's
theme, so put the colours in a `<style>` with an
`@media (prefers-color-scheme: dark)` block, and keep text contrast at 4.5:1
in both themes.

If a card fails to parse or an include is refused, `orch ask` still builds the
page: that question shows as a free-text fallback with a warning naming the
error, and the output lists it under `warnings[N]`. Fix the card and rerun.
