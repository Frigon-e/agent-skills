## Commits & PRs

- No AI attribution anywhere — no `Co-Authored-By` trailers, no "Generated with …" lines in commit messages, PR bodies, or code comments. Write them as the human developer would.

## Herdr

Before any `herdr` command, load the `herdr` skill (`herdr --skill` prints the same text) and follow it. My additions:

- Naming: agents `<role>` or `<role>-<topic>` (`reviewer`, `impl-auth`); numeric suffix only for identical parallel roles. `pane rename` each agent's pane to its agent name. Tabs by purpose (`agents`, `tests`, `logs`); workspaces `<repo>` or `<repo>-<task>`; worktree branches follow the repo's convention, else `<type>/<topic>`.
- Always pass `--timeout` with `--wait` / `wait`.
- Close the panes, tabs, and worktrees you created once their work is done; ask before `worktree remove --force`.

<!-- jbcontext-instructions-start -->
## Semantic code search (jbcontext)

- When you don't yet know which file or symbol is relevant, start with `jbcontext search "<descriptive natural-language query>"` (`-p <repo-relative-path>` to narrow) rather than grep/find.
- Once you have a relevant hit, read files directly.
- For multi-step discovery, delegate to the context-explorer subagent (`context-explorer` in Claude Code, `context_explorer` in Codex) instead of searching repeatedly yourself.
<!-- jbcontext-instructions-end -->

## Subagents

- Route by context as well as cost. A spawn pays a fresh prompt prefix, but every token you keep is re-read on each later turn and dilutes attention, so long sessions should delegate more. Spawn one for output you'd mostly discard (searches, sizing, log digging), independent pieces that can run in parallel, or a check that needs fresh context. Browser and UI checks always go to a subagent (`ui-verifier` in Claude Code). Split by the context each piece needs, never by role (planner/implementer/tester); a change and its tests stay in one agent. For code changes in Claude Code, the `route-work` skill picks the model and escalation.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project. Do not repeat what the codebase already shows; point to the authoritative file or command instead. Prefer rewriting or pruning existing entries over appending new ones. When updating this file, preserve this bar for all agents and keep entries concise.
