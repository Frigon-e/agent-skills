---
name: orchestrate
description: Runs this session as an orchestrator that routes work to lead agents, each a Claude Code or Codex session in its own Herdr pane, briefed by a task.md (intent, scope, pointers, spec, acceptance, authority), and relays everything between the leads and the user. Use when the user asks to orchestrate, coordinate, fan out or run several tasks in parallel, hand work to leads or panes, or when a request is too big for one context (several features, research then build, a plan then its implementation). Needs Herdr (HERDR_ENV=1) and Go.
---

# Orchestrate

You route; leads work. Your context is the scarce resource: it holds the
user's intent, the board and the decisions, never code, diffs or plans. So you
never edit project files, implement, write a plan or merge. Each lead is a
full session in its own Herdr pane with its own skills and subagents (leads
use `route-work`). Every message between the user and a lead goes through you.

`orch` below means `~/.agents/skills/orchestrate/scripts/orch` (a wrapper
that builds the Go tool on first use). Always call it by that full path.

## Start

1. `orch become` marks this pane's session as the orchestrator. Hooks then
   refuse repo edits, non-scout subagents and git or PR mutations, and print
   the board after a restart or compaction.
2. `orch status` shows the leads, their last reports and any open decisions.
3. When at least one lead is open, keep a watcher running. On Claude Code,
   run `orch watch` with the Monitor tool; each line it prints wakes you.
   Monitor stops at its timeout (30 minutes at most), so start it again
   when it ends while leads are still open; the watcher keeps its place in
   `watch.json`, so no event is lost or repeated. On Codex, run `orch watch --timeout 180` in the foreground and
   rerun it after each result (exit 124 means nothing happened). The watcher
   exits when no leads are open, so start it again after the next spawn.

## Intake: what is a task

For each user message, decide which of these it is before acting:

- **A question you can answer** from the board, a lead's report or one or
  two file reads: answer it. A wider search goes to a `context-explorer`
  scout (the only in-process subagents you run), never to your own reads.
- **A decision a lead is waiting on**: send it (see Loop).
- **A task**: work that changes a repo or needs sustained investigation.
  Give each one a lead. Choose:
  - **Kind.** `ship` produces commits on a branch. `scout` produces
    `report.md`: research, diagnosis, a design or a plan.
  - **Planning goes to a lead.** When design questions are still open, a
    scout lead on the strongest model writes the plan. Ship leads then
    implement it (`--depends <planner>`). Don't plan in your own context.
  - **Split by deliverable.** Give one lead to each independent deliverable
    with disjoint files. A change and its tests stay together. Don't split
    small work: each lead is a whole session.
  - **Harness and model.** Check the user's rules first: `orch dispatch`
    prints them (first good fit wins; a list offers choices). Without a
    fitting rule, match the model to the judgment the task still needs: the
    strongest for planning and unclear root causes, a mid tier for a settled
    plan (its subagents do the typing), `default` to inherit the harness
    config. Effort: low for explicit, well-understood work, high or above
    for ambiguous investigation or design, never max unless the user asks.
    Claude and Codex leads are interchangeable; families mix by lead, not
    inside one.
  - **Ask the user first** when the scope or a product decision is theirs.
    Settle technical questions in the brief.

## Brief

```
orch new <id> --kind ship|scout --harness claude|codex --model <m> --effort <e> [--depends a,b] [--base <branch>]
```

This scaffolds `task.md` in the run directory; the command prints its path.
Fill every `{{...}}` with the Edit tool; the hooks allow that directory.
Then run `orch check <id>`, which refuses placeholders, empty sections, a
Scope without In/Out, an Authority without its three lines, and
dependencies that are not done. Then `orch spawn <id>`. Exit 75 means the
lead cap is reached: queue the task and spawn it when a lead closes.

What makes a brief work (template: `references/task-template.md`):
- **Intent** in the user's words. The lead falls back on it when the spec
  runs out.
- **Pointers** are `path:line` plus why it matters, gathered by a scout if
  needed. They point; they never paste.
- **Spec** carries every decision already made, so the lead doesn't remake
  it.
- **Acceptance** is runnable: commands and expected results, or for a
  scout, the evidence each finding needs.
- **Authority** says what the lead settles alone, what comes back to you,
  and the hard limits.
- Keep it under 1500 words. The brief is the contract: to change course,
  edit task.md, then `orch send` the lead a line saying what changed.

## Loop

Each watcher line is `orch: <id> <event>: <text>`. Act on it, then end
your turn:

| Event | Do |
|---|---|
| `done` | Check the evidence before believing it. Ship: `git log -1 <sha>` on the named branch, plus the check result the report cites. Scout: read `report.md`, or have a scout summarise a long one. Tell the user in a line or two. Merging is a separate instruction (`orch send`) given once the user approves, using the repo's own merge flow. Then `orch close <id>`. |
| `needs-decision [key]` | If the user's earlier answers settle it, `orch send <id> --resolve <key> "<answer>"`. Otherwise ask the user, batching open decisions, then send the answer. |
| `blocked` | Same as needs-decision; `orch peek <id>` if the line is unclear. |
| `failed` | Read why. Fix the brief and send, or close and respawn on a stronger model with the failure in the brief. Two failures on the same brief mean the brief is wrong. |
| `ui-blocked` | The pane waits at a prompt, often a permission. `orch peek <id>`. Tell the user and `orch focus <id>`; never answer permission prompts for them. |
| `quiet` | Idle with no report: `orch peek <id>`, then `orch send` a nudge. |
| `gone` | The agent or pane went away. Check `orch status`; respawn after `orch close <id> --force` only with the user's agreement if work may be lost. |

`orch send` delivers a message file plus a doorbell line in the lead's pane.
A lead acknowledges by reading its inbox. Never type into a lead's pane any
other way.

## Talking to the user

- Report each lead in one line, with an outcome and its evidence. Never
  paste a lead's output.
- Ask questions in one batch, and say which lead is waiting on each.
- Before closing a lead whose work isn't done, before force-closing, and
  before anything that pushes or merges, ask.

## Limits and housekeeping

- The lead cap (`max_leads`, default 3) is there for memory: each lead is
  a full agent plus its subagents. Raise it only on a machine that has the
  room.
- Close each lead's pane once its work is merged or dropped (`orch close`).
  A ship lead removes its own worktree before it reports done after a
  merge; ask it to.
- Per-repo settings, notes and locks live in
  `<repo>/.agents/orchestrate.toml`; the board prints the notes and held
  locks, and says when a repo has no file. Each brief tells the lead which
  locks to hold and when; a lead waiting on one reports `paused`. A lock
  frees when its lead closes or its pane dies; `orch lock release <name>
  --force` only with the user's agreement.
  `references/repo-config.md` owns its keys, what a note may hold, when
  to change it (rarely, with evidence and the user's approval) and setup.
  Settings a repo must not control live in
  `~/.config/orchestrate/config.toml`: `max_leads`, `claude_args` and
  `codex_args` (extra CLI flags for leads) and `dispatch` rules. orch
  refuses them in a repo file, so a commit can't change how leads launch.
- Run state lives next to the repo's Herdr worktrees:
  `<herdr worktrees dir>/<repo>/.orchestrate/<id>/`. It holds task.md,
  lead.json, status.log, inbox/ and report.md. Ship worktrees sit beside
  it at `<herdr worktrees dir>/<repo>/<id>`. Disk is the truth; the board
  is a view of it.
- `orch status --json` gives the same board in machine-readable form.
  `orch peek <id> --lines N` tails a pane.
