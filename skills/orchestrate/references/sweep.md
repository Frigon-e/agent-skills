# Question sweep

Use a sweep when many open questions (from ADRs, specs, notes, earlier runs)
need answers and the person should see only the ones that are really theirs.
No single model is trusted: several passes answer on their own, and
`orch sweep` compares them with code.

## Stages

| Stage | Who | Output |
|---|---|---|
| 1 Collect | one Flash scout | cards in `<topic>/questions/`, and `dropped.json` for every question it merged or skipped |
| 2 Evidence | Flash scouts, one per area, in parallel | `<topic>/evidence/<area>.md`: facts with `path:line` or a link, no recommendations |
| 3 Answer | two passes with fresh context: `sonnet`, and one Flash pass (`flash`) | `<topic>/passes/<pass>/<key>.json` |
| 4 Compare | `orch sweep <topic>` (code, no model) | settled, disagree, recheck, user |
| 5 Settle | one `settle` pass (Opus or Sonnet) on the disagreements only | `<topic>/passes/settle/<key>.json` |
| 6 You | `orch ask --cards <topic>/sweep/for-user` | one page of the real owner questions |

Why Flash writes no recommendations: weighing options is judgment, and Flash
is for gathering and for settled work. Finding what the code, an ADR or a
competitor does is gathering, and each fact can be checked.

## Answer file

```json
{"choice": "script", "confidence": 0.8, "reason": "one or two sentences",
 "evidence": ["docs/adr/0007.md:12", "polar/server/checkout.ts:88"],
 "owner": "agent"}
```

- `choice` must be an option id on the card. A free-text card cannot be
  compared and goes to the user.
- `evidence` is `path:line` in the repo, or `<root>/path:line` for another
  checkout named in `--roots polar=/path,stripe=/path`. Code checks that
  the line exists. An https link cannot be checked, so it counts only with
  `--accept-urls`.
- `owner` is `agent` (the default), `user`, or an outside expert such as
  `accountant`. Any pass that says it is not `agent` sends the question to you.

## What `orch sweep` decides

- **settled**: at least `--min-passes` (default 2) passes picked the same
  option, each with evidence that exists. Nothing else settles a question.
- **final**: the passes differed and the pass named by `--final` (default
  `settle`) answered with real evidence.
- **disagree**: the passes differed and there is no valid final answer. Run
  the final pass on these.
- **recheck**: an answer is missing, picks a non-option, or has evidence that
  does not resolve. Rerun that pass.
- **user**: the card's `area:` is in `human_only` (repo config), a pass named
  another owner, or the card is free-text. `sweep/for-user/<key>.md` is the
  card plus a `## Passes` section with each pass's choice, reason and evidence.

`dropped.json` is checked too: each dropped question must name the card it
duplicates (`duplicate_of`) or the evidence that already answers it
(`answered_by`). A question dropped with neither is listed.

## Spec outline

```
### collect: Gather every open question
kind: scout
model: flash
Write one card per distinct open question in <topic>/questions/. Give each
card an `area:` (for example billing, tax, legal, product, infra). List every
merged or skipped question in <topic>/dropped.json with duplicate_of or
answered_by. Do not answer any question.

### evidence-billing: Evidence for the billing area
kind: scout
model: flash
depends: collect
For each billing card write the facts that bear on it: what the MVP code
does, what the ADRs say, what Polar, Stripe, WHMCS and Paymenter do. Every
fact carries `path:line` or a link. Do not recommend.

### answer-billing-sonnet: Answer the billing cards
kind: scout
model: sonnet
depends: evidence-billing
Read only the cards and evidence/billing.md. Write one answer file per card
to passes/sonnet/. Do not read other passes.
```

Repeat evidence and answer packages for each area. The Flash answer pass
reads the same evidence file and writes to `passes/flash/`. A brief cannot
stop a lead from reading another pass's folder, so name only its own files in
Pointers and treat agreement as evidence only when the evidence is real.

## Rules that keep it honest

- Money, tax and legal areas go in `human_only`. Two models agreeing on an
  invoicing rule is not an accountant's answer.
- Both answer passes read the same evidence file, so a wrong fact can fool
  both. That is why every fact needs a line a script can open.
- `orch sweep` writes nothing to `questions/answered/`. Move a card there
  yourself once the person has answered it.
- Cost: one Flash collect, about six Flash evidence scouts, one Sonnet and one
  Flash pass per area, and Opus only on disagreements. Group areas so each
  Sonnet pass reads one evidence file.
