---
name: route-work
description: Decides how a coding task gets done and by which model. Triage unclear scope with a cheap Haiku scout, judge how much design work is still open, then either do it yourself or hand a concrete plan to a cheaper implementer agent, and verify the result. Use this for any request to implement, fix, change, refactor, or add something in a codebase (bug reports, feature requests, "make X do Y", tickets, multi-part changes), especially when the scope isn't clear yet or the work could be split up. Not needed for pure questions, explanations, or reviews where no code changes.
---

# Route work by what's still undecided

The goal is to spend strong-model attention only on decisions and cheap-model tokens on finding and typing. What sets the right model isn't how big the task sounds. It's **how much judgment remains** once the code is understood. A large feature with a precise plan is mechanical; a two-line fix with an unclear root cause isn't.

Roles (a **delegate** is any subagent doing the work; the **plan** is what you hand it):
- **You (the main session):** judgment. Root causes, design decisions, plans, final verification.
- **`context-explorer` (Haiku):** finding. In `scope` mode it reports change sites, blast radius, tests, risk flags and open questions. It gathers evidence; it doesn't decide.
- **`implementer` (Sonnet, medium effort by default):** executing a plan. Pass `model: "haiku"` for short, checkable plans (see step 3).
- **`fable-planner` (Fable, read-only):** last-resort diagnosis and plan when you and the delegates are stuck. Rare and expensive; see step 4.
- **`lens-reviewer` (Sonnet):** checks a change through one narrow lens (spec, callers, security, …), reporting only high-confidence issues of that class. Used for risky changes, not routine ones.

Tell the user the route in one line when you pick it (for example "Scope is clear and small, so fixing directly" or "Triaging first; the request spans billing and export"), so they can redirect early.

## 1. Is triage needed?

Skip it when you already know where the change goes: the user named the file or function, it's in context, or one quick search pins it down. A scout pass on a known small fix only adds latency.

Triage when the request describes behavior rather than location ("exports sometimes drop rows"), spans areas you haven't seen, or you can't yet say which files change. For requests touching several independent areas, run one scout per area in parallel.

Prompt the scout with the request in the user's words plus anything you already know:

```
Mode: scope
Goal: <the change request, verbatim where possible>
Scope: <directory or files, if known>
Known: <facts you already have, so it doesn't re-find them>
```

## 2. Check the evidence, then judge

Treat the scout's report as a map: open the top 1–3 change sites yourself before deciding. That usually takes a few reads and tells you whether the "small" guess is real. Reading a report can't show what the scout missed, so when completeness matters (every caller, every config site), spot-check one area yourself or split it into narrower scouts whose coverage you can check.

Then answer:
- **Open decisions:** is the root cause known? Is the intended behavior unambiguous? Are there design choices left (data shape, algorithm, error semantics, API)?
- **Risk:** auth or security, persistence or migrations, concurrency, money, public APIs, external I/O.

Ask the user only when an open decision is truly theirs (product behavior, compatibility tradeoffs). Settle technical ones yourself.

## 3. Route

| Situation | Route |
|---|---|
| Change is obvious once located, roughly ≤20–30 lines | **Do it yourself.** Writing a plan for a delegate would cost more than the change. |
| Open design decisions or unclear root cause | **Settle them yourself first** (investigate, decide, write the plan), then route the plan using the rows below. If deciding and doing can't be separated (exploratory debugging, delicate algorithms), implement it yourself. |
| Decisions settled, an exact plan for a short, single-concern change, and a test or check that can verify it | **`implementer` with `model: "haiku"`**, escalating per step 4 if it fails. Independent parts can go to parallel implementers if they touch different files. |
| Purely mechanical (renames, boilerplate, repetitive edits across many files) | **`implementer` with `model: "haiku"`.** |
| Decisions settled, but nothing mechanical can verify the result (UI, prose, config with no tests), or the work needs inference across several files or a long edit-test-fix run | **`implementer`** (Sonnet). |
| Settled but high-risk | **`implementer`** (Sonnet), then lens reviews (step 5). |

Every spawn pays for a large cached prompt prefix, so prefer one implementer over several tiny ones, and don't move short, checkable work up a tier "to be safe". Keep Haiku delegates short: a Haiku prompt over 100K tokens costs 5x, and its context grows with every file it reads. Evidence and caveats: `references/model-selection.md`.

## 4. Writing a plan worth delegating

The implementer's quality ceiling is the plan's specificity. It's told to stop rather than guess, so gaps come back as "blocked" instead of silent mistakes. Cover the gaps up front:

```
Goal: <one sentence, the user-visible outcome>
Change: `path/file.py` `func`: <exactly what changes>; `path/other.py`: <…>
Behavior: <inputs → outputs, including the edge cases you decided: empty, negative, missing, concurrent…>
Don't change: <adjacent code or behavior that must stay as is>
Unverified: <claims from notes, tickets or docs you haven't checked against the code; omit if none>
Tests: add <what> in `tests/…`; run `<command>`
```

If you can't fill in **Change** and **Behavior** concretely, the decisions aren't settled yet. Go back to step 3's second row.

When an implementer reports **blocked**, make the call it surfaced and either re-delegate with the answer or finish it yourself if that's quicker.

### When a delegate fails
You hold the verifier, so you make the escalation call, never the delegate. A failure means the tests you ran fail, not that the implementer says it's done.
1. **First failure:** re-delegate once to the **same model** with the failing output and what you learned (an informed retry). Often that's enough.
2. **Second failure on Haiku:** move up to `implementer` on Sonnet, with the failure history in the plan.
3. **Second failure on Sonnet, or the failures show a design gap:** stop delegating and take over the problem yourself, going back to step 3's second row.
4. **Still stuck (last resort):** call `fable-planner` only if you investigated yourself and the root cause or design is still unclear, a fix has failed with evidence, and the task is worth the cost. Skip it if this session already runs on Fable. Brief it in its input format, check its diagnosis against the code, then hand its plan to a Sonnet `implementer`. If it reports `Needs user`, ask the user; if low confidence, run the experiments it names first. One planner call per problem; a second means the problem needs the user.

## 5. Verify

- Implementers run tests and add a regression test. Check their report shows real test output, not just "done", then **rerun the tests yourself**. Your run is the verifier that decides whether to escalate.
- **Read the final diff yourself.** It's cheap and catches drift from the plan, scope creep, and edits to tests that make them pass rather than check the behavior.
- For routine changes, stop there. The implementer's tests plus your diff read already cover them, and a reviewer mostly adds cost.

### Lens reviews (risky or delegated-and-large changes)
For auth or security, persistence, concurrency, money, shared-code signature changes, or large delegated diffs, run two or three narrow `lens-reviewer` agents in parallel, one lens each, and verify each finding against the code yourself. Lens table, prompts and handling: `references/lens-reviews.md`. Skip this for routine changes.

## On Codex

Codex spawns its custom agents (`~/.codex/agents`) in place of the roles above; triage, plan format, escalation and verification stay the same:
- `context-explorer` → `context_explorer` (read-only).
- `implementer` → `plan_implementer`. It has one configured model, so the Haiku and Sonnet rows both route to it.
- `lens-reviewer` → `full_reviewer` for risky changes, `fast_reviewer` to re-check a narrow fix; `silent_failure_hunter` and `test_coverage_analyst` as extra lenses.
- `fable-planner` → none. In an orchestrate lead, report `needs-decision` with the evidence; otherwise ask the user.

## Pitfalls
- Delegating a change smaller than its plan.
- Triaging a location you already know.
- Letting the scout's size guess make the decision without opening the code.
- Handing a delegate a task with open design questions and a vague plan. The cheap model will make those decisions quietly and confidently.
- Spawning many scouts for one small area. One scout per independent area is enough.
- Taking an implementer's "done" as the verdict instead of rerunning the tests.
- Escalating to a bigger model without feeding it the failing output.
- Fanning out many small spawns when one implementer would do.
- Calling `fable-planner` before investigating yourself, or for a problem that is merely tedious. It is for stuck, not for hard-but-clear.
