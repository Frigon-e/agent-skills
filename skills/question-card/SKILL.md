---
name: question-card
description: The format for asking the user a decision - context first, a recommendation, 2-3 options with gains and costs, in ASD-STE100-style Simplified Technical English and the project's own terms. Use whenever you ask the user to choose between options, report a needs-decision as an orchestrate lead, batch questions as an orchestrator, or the user says they don't understand a question and asks for a re-pitch.
---

# Question card

The user decides fast when a question is short, has context, and uses the
project's own words. Every decision you ask for uses this card.

## The card

1. **Context** (2-3 sentences). What we are doing, and what caused this
   question. The user may have been away; do not assume they saw the work.
2. **Question** (1 sentence). Write it as a choice.
3. **Recommendation.** Your pick and one reason.
4. **Options** (2-3, recommendation first). For each: what it is, what you
   gain, what it costs.
5. **Change later:** easy or hard. Hard means more care from the user.
6. **Next:** what happens after the user picks.

A yes-or-no question needs only 1, 2, 3 and 6.

## Language

Write in the style of ASD-STE100 Simplified Technical English:

- One idea per sentence. Procedure sentences: 20 words or fewer.
  Descriptive sentences: 25 words or fewer.
- Active voice. Say who does the action.
- One word, one meaning. Do not use a synonym for a term you used already.
- Simple verbs in simple tenses. No "-ing" verb forms, no phrasal verbs
  ("set up" → "make", "find out" → "find").
- Use articles ("the", "a"). Do not drop words to save space.
- Facts and numbers, not adjectives ("3,000 lines", not "very long").

This is STE style, not full STE: you do not have the STE dictionary.

**Use the project's terms.** The project's own words are the "technical
names" that STE allows. Find them in `docs/GLOSSARY-MAP.md` when the repo
has one; it points to the family glossary and to each product's glossary.
Use a term exactly as the glossary does. A word can mean different things
in two products; say which product you mean. When a term is not in a
glossary, define it in a few words the first time you use it.

## Where the card goes

| Situation | Surface |
|---|---|
| One yes-or-no question | Plain chat |
| 2+ options, or several questions at once | A Lavish page (below), or chat when Lavish is not available |
| The choice is about structure, flow or UI | A Lavish page with a diagram or UI mock |
| Orchestrate lead | The card goes in the needs-decision report (see the lead protocol) |

**Lavish page:** follow `lavish-axi playbook input` (one submit per
question). Add a diagram when the question is about how parts connect or
how data moves (`lavish-axi playbook diagram`, inline SVG). Add a UI mock
when the question is about a screen, in the project's own design system.
Do not add visuals to a question that text answers well. Never run
`lavish-axi share`: it publishes to a third-party site.

## When the user does not understand

The user says "I don't understand" or "re-pitch that". Do not repeat the
same text. Write the card again with:

- more context: where we are and how we got here;
- fewer terms, and a definition for each term you keep;
- one example or one picture of the difference between the options.

## Example

> **Context:** Leads send questions to the orchestrator. Today the
> orchestrator shows them to you as text in chat.
>
> **Question:** Does a script make the question page, or does the
> orchestrator write it?
>
> **Recommendation:** A script. Every page then looks the same and costs
> no tokens.
>
> - **A script.** Gain: the same layout every time; we can test it.
>   Cost: a fixed layout; a diagram needs its own file.
> - **The orchestrator.** Gain: each page can have its own layout.
>   Cost: tokens in the orchestrator's context, every time.
>
> **Change later:** easy. **Next:** I add the `orch board` command.
