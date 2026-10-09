# Model selection evidence

Why `route-work` sends short, checkable work to Haiku first and escalates on failure.

Source: oaustegard/claude-skills, `agent-routing` skill v2.4.0 (2026-10-07), github.com/oaustegard/claude-skills/tree/main/agent-routing. Reviewed 2026-10-07.

## What the source measured
- On a 14-repo seeded-bug repair battery, Haiku 5.5 solved 11/14 on the first try and 14/14 with a same-model informed retry, for $0.12 total. That is less than one fresh Sonnet 5.5 subagent spawn (about $0.17, mostly the cached prompt prefix of roughly 55K tokens).
- Sonnet 5.5 @ high matched Opus 5.5 @ high (24/28 each) at 0.43x the cost per completed task.
- The escalation decision belongs to whoever holds the verifier, never the worker.

## Prices (platform.claude.com/docs/en/about-claude/pricing, checked 2026-10-08)
Per MTok, input/output: Haiku 5.5 $0.10/$0.50 for prompts up to 100K tokens, $0.50/$2.50 over 100K (5x). Sonnet 5.5 $2/$10. Opus 5.5 $4/$20. Fable 5.1 $10/$50. The other models have no long-context premium.

So Haiku is 20x cheaper than Sonnet on short prompts but only 4x on long ones. A delegate's prompt is its whole context so far: the spawn prefix plus every file and test output it has read. A long Haiku run crosses 100K partway through and pays 5x from then on, before counting retries.

## Caveats
- One author, one battery of seeded-bug repair tasks. It does not separate tiers on harder work.
- A third-party model-choice guide (unsourced, seen 2026-10-08) cites Terminal-Bench edit-check-fix loops at Haiku 39% vs Sonnet 71%, and says Haiku stops early and skips checks under long prompts. Not verified here, but it's why multi-file inference and long edit-test-fix runs go to Sonnet.
- Haiku 5.5 on long generation (new modules, documents) is projected from price, not measured.
- Goal-shaped prompts where the deliverable must be discovered are better on Opus (one observed session, not a measurement).
- Prices and measurements go stale with each model generation. Recheck when models change.

## If Haiku-first misbehaves
Change the "Decisions settled, an exact plan for a short, single-concern change..." row in step 3 of SKILL.md to plain `implementer` (Sonnet).
