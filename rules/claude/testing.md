---
paths:
  - "**/tests/**"
  - "**/test/**"
  - "**/__tests__/**"
  - "**/spec/**"
  - "**/e2e/**"
  - "**/*.{test,spec}.*"
  - "**/test_*.py"
  - "**/*_test.{py,go}"
  - "**/*_spec.rb"
  - "**/*{Test,Tests}.{java,kt,cs,swift}"
---

# Writing and changing tests

A test is worth keeping only if a realistic bug would make it fail. Fewer, stronger tests beat more, weaker ones.
The author of a test never reviews it: use the `test-reviewer` agent.

## Gate (answer before adding a test; a missing answer means do not add it)

1. **Behavior.** What observable behavior or contract does it protect? (public API, HTTP response, DB row, rendered UI, exit code)
2. **Name the break.** What specific production change makes it fail (wrong constant, wrong branch, missing side effect, empty return, missing validation)? Is that change a bug or a decision?
3. **Existing coverage.** Why does current coverage not already catch it? Name the existing test you searched. Extend a table case instead of duplicating.
4. **Seam.** Does it need a production export, flag or hook that no production caller needs? If so, move the test to the real boundary.
5. **Level.** Is this the narrowest test that can catch the break?

## Rules

- Assert outcomes at a public seam, not call counts, private state, log text or source greps.
- Expected values come from a literal, a worked example or the spec, never from the code under test.
- Mock only at system boundaries. Never mock the subject or modules this repo owns. A mock earns no assertions: if the only check is that a mock was called, or its return value decides the outcome, unmock it or delete the test.
- Prefer real in-process collaborators (temp dir, in-memory DB, test server).
- Failure-path tests assert the specific error or status/exit code, not "does not throw" or message text alone.
- Boundaries: for each comparison against a constant, cover just below, on and above it. For each normalizing call (trim, sort, round, default), include a case whose answer changes if the call is removed.
- No sleeps; wait on a condition. No wall-clock, locale or unseeded randomness.
- One test, one behavior. The name states the capability, not the internal call.
- Skip constructors, getters, constants, trivial forwarding and change-detectors (`expect(MAX_RETRIES).toBe(5)`), unless they validate, normalize, default or have side effects.
- Write one test at a time. Watch it fail for the right reason (not a typo or setup error) before writing code. A test that passes immediately proves nothing: fix the test.
- Finish with a mental mutation pass: for each realistic mutation of the code you touched, name the test that fails. If none, add one or say the behavior is unprotected.
- Regression test: red on the parent commit, green on the fix (revert the fix, run, confirm red, restore, run, confirm green).
- Critical modules: a diff-scoped mutation run (Stryker, mutmut, PIT, mewt). Surviving mutants map to missing assertions. Do not treat the score as pass/fail on its own.

## Existing tests are frozen; a failing test is information

You usually cannot ask the user (subagents have no question tool, and long runs should not stall).

1. Assume the code is wrong. Investigate before touching any expected value.
2. If the task statement explicitly says this behavior should now differ, update the affected assertion to assert the new behavior exactly. Never write a rewrite that passes both old and new.
3. Anything else is a surprise: leave the test failing, continue with independent work, and report it.
4. Test-change log: every changed or removed existing assertion goes in your final report and the PR description (file:line, old, new, which sentence of the task justifies it). If you cannot point to one, do not make the change.
5. Surprises: for each, give the test name, failing assertion, whether you think the code or the test is wrong, evidence, and what you tried.
6. A test that fails on baseline is a possible product bug. Fix the code, do not delete the test.
7. Never loosen, skip, delete, raise timeouts, add retries or update snapshots just to get green.
8. If you cannot write a faithful test, say why in the report and still write the strongest test the requirement allows.
