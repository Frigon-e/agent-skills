# Spec file

A spec describes one feature and splits it into work packages. `orch plan
<spec>` makes one task per package, so a spec with settled decisions goes
to leads with no planning in the orchestrator. The `brainstorm` skill
writes specs; a person or a scout lead can write one too.

The spec lives in the repo (where the repo keeps specs, for example
`docs/<product>/specs/`). Commit it to the base branch before `orch plan`:
leads read it from their own worktree. `orch plan` also accepts a spec that
sits in a linked worktree of the repo, when it is committed to that
worktree's branch (the spec's `base:`).

## Format

```
---
harness: claude
model: sonnet
effort: medium
base: dev
---
# Server transfer queue

## Behaviour
What the feature does, from the user's view. 1-3 short paragraphs.

## Design rules
Decisions already made, one per line, with a link to the ADR when one exists.

## Authority
May decide: names, internal structure, test layout
Must escalate: API shape, data model changes, anything a user sees
Never: push, merge, edit files another package owns

## Packages

### transfer-api: Transfer API and its tests
kind: ship
model: sonnet
depends: transfer-model
owns: internal/panel/transfer/api.go, internal/panel/transfer/api_test.go

What this package builds. Pointers as `path:line` - why it matters.

#### Acceptance
`scripts/agent/check go ./internal/panel/transfer/` passes; a test shows
a queued transfer survives a panel restart.

### transfer-model: Transfer table and store
kind: ship
model: haiku
effort: low
owns: internal/panel/transfer/store.go, internal/panel/transfer/store_test.go, migrations/

Body.

#### Acceptance
...

## Open questions
None.
```

## Rules

- **Front matter** gives defaults for `harness` (claude or pi), `model`,
  `effort` and `base`. A package line overrides its default. `base`
  falls back to the repo config.
- **`## Behaviour`, `## Design rules` and `## Authority`** are required.
  Authority needs its three lines; every task gets them.
- **`## Packages`** holds one `### <id>: <title>` block per package:
  - `<id>` becomes the task id (lowercase, starts with a letter, at most
    27 characters).
  - The first lines are `key: value`: `kind` (ship or scout, required),
    `model`, `effort`, `harness`, `depends` (ids of other packages in
    this spec), `owns` (comma-separated files or directories, required
    for ship). A blank line ends them.
  - The text after the blank line is the package's own spec.
  - `#### Acceptance` is required: commands and expected results, or for
    a scout, the evidence each finding needs.
- **Ownership is disjoint.** No file or directory belongs to two packages,
  and no owned directory contains another package's file. A change and its
  tests stay in one package.
- **Model per package** follows the judgment the package still needs, not
  its size: haiku for a short package that acceptance tests can fail,
  sonnet for settled work that nothing mechanical checks or that is high
  risk, the strongest model only while a design is still open (which a
  finished spec should not have).
- **`## Open questions`** must be empty or say `None.`. `orch plan` refuses
  a spec with open questions: ask them first.

## What `orch gate` does

`orch gate <spec>` runs before `orch plan` and records a verdict for the
spec's exact text (a change to the spec voids it). It checks that every
`path:line` pointer and every acceptance command exists, then runs four
read-only passes on a cheap model (`ORCH_GATE_MODEL`, default DeepSeek
Flash through pi): scope, open questions, and two adversaries. A model
claim counts only when its evidence line exists in the repo, and only a
question whose answer changes visible behaviour blocks. The verdict is
`pass`, `fail` or `unverified` (a pass could not run, or pi is marked
limited); only `pass` unlocks `orch plan`. The report, findings and
question cards are in `<run root>/gate/<hash>/`.

## What `orch plan` does

`orch plan <spec> [--dry-run] [--skip-gate]` checks the whole spec first and
lists every problem; it makes no task until the spec is clean. It refuses a
spec the gate has not passed, a spec that
is not committed to the base branch or has uncommitted changes, unknown or
circular dependencies, overlapping ownership, and a task id already used
by a task from somewhere else.

Each task.md points to the spec (`spec: <path>#<id>` in its front matter)
and copies only the package's own text: Intent names the feature and the
package, Scope In is `owns` and Scope Out is the other packages' files,
Pointers name the spec's sections, Spec is the package body, Acceptance
and Authority come from the spec. A rerun skips tasks it made before
(`exists`), so it is safe to run again after adding packages.

Output lists each task with its status and the waves: a wave holds the
packages whose dependencies are all in earlier waves. Check and spawn the
first wave; spawn the next one when its dependencies are done.
