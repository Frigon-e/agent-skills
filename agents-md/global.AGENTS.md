## Commits & PRs

- No AI attribution anywhere — no `Co-Authored-By` trailers, no "Generated with …" lines in commit messages, PR bodies, or code comments. Write them as the human developer would.

## Herdr

Before any `herdr` command, load the `herdr` skill (`herdr --skill` prints the same text) and follow it. My additions:

- Naming: agents `<role>` or `<role>-<topic>` (`reviewer`, `impl-auth`); numeric suffix only for identical parallel roles. `pane rename` each agent's pane to its agent name. Tabs by purpose (`agents`, `tests`, `logs`); workspaces `<repo>` or `<repo>-<task>`; worktree branches follow the repo's convention, else `<type>/<topic>`.
- Always pass `--timeout` with `--wait` / `wait`.
- Close the panes, tabs, and worktrees you created once their work is done; ask before `worktree remove --force`.

<!-- jbcontext-instructions-start -->
# Tools

## Semantic Code Search (jbcontext)

You have access to `jbcontext search` for searching the codebase semantically.
It finds code by meaning, not just keywords.

### Usage

```bash
jbcontext search "<detailed and descriptive query>"
jbcontext search -p <path> "<query>"  # <path> must be relative to the project root
```

### Query Tips

- Be descriptive: "Where is a function that validates user email addresses" > "email"
- Include context: "Find error handling middleware for HTTP requests with logging"
- Specify what you're looking for: "React component that renders a modal dialog"

### How to use it

- Start with `jbcontext search` before planning, editing, or exact search in unfamiliar code when you do not yet know the right file, subsystem, implementation, or related test.
- Use one focused natural-language query per search.
- Do not start with grep, ripgrep, or find when the search problem is still semantic or exploratory.
- Once you get a relevant hit, switch to direct file reads - needing another search is a sign to delegate to `context_explorer` instead of searching again yourself.

## Subagent: `context_explorer`

For broader or multi-step exploration, delegate to the `context_explorer` subagent
instead of searching inline. It is a read-only agent that runs several
`jbcontext search` queries in its own context, reads the promising files, and
returns concrete `file:line` references with inline code snippets and a
confidence note - so this thread stays uncluttered by intermediate search output
and does not have to re-read the same files.

### How to spawn it

- Spawn it with: `spawn_agent(agent_type="context_explorer", fork_turns="none", message="<intent>")`
- `spawn_agent` runs in the background - you can read a file you already know is relevant, check the environment, but don't perform exploration while it's running.
- Always call `wait_agent` once that known work (if any) is done, otherwise you never get the report.


## When to use `jbcontext search` CLI vs. `context_explorer` subagent

If you're confident the discovery is multi-step - mapping an unfamiliar
subsystem, or tracing across several files - spawn `context_explorer`
directly. Otherwise, run `jbcontext search` first; if the results are not enough, delegate to `context_explorer`.

<!-- jbcontext-instructions-end -->

## Subagents

- Route by context as well as cost. A spawn pays a fresh prompt prefix, but every token you keep is re-read on each later turn and dilutes attention, so long sessions should delegate more. Spawn one for output you'd mostly discard (searches, sizing, log digging), independent pieces that can run in parallel, or a check that needs fresh context. Browser and UI checks always go to a subagent (`ui-verifier` in Claude Code). Split by the context each piece needs, never by role (planner/implementer/tester); a change and its tests stay in one agent. For code changes in Claude Code, the `route-work` skill picks the model and escalation.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project. Do not repeat what the codebase already shows; point to the authoritative file or command instead. Prefer rewriting or pruning existing entries over appending new ones. When updating this file, preserve this bar for all agents and keep entries concise.
