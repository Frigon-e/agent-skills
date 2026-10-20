# Experiment pre-registration and promotion

`tool/experiment_manifest.go` defines schema version 1 and a fail-closed
`Validate` / `ValidateRunnable` preflight. This is an inert record and validator.
There is no experiment command, paid runner, automatic adjudicator or promotion
switch. A valid record is necessary, not sufficient, for an approved paid run.
Never infer permission to spend or change defaults from successful validation.

## Required record

All fields below except `paid` are required for shadow and live records. Rates
are fractions from 0 to 1. Threshold pointers distinguish an explicit zero from
omission.

- `schema_version`: exactly `1`.
- `baseline`: immutable baseline/configuration and subject identifier.
- `held_out_cases`: unique `id`, immutable `subject` (including answer-key
  identity), and `category`. Include at least one each of
  `contradictory_judgments`, `insufficient_evidence`, `authority_boundaries`, and
  `provider_failures`. Categories may contain several cases. Cases must not be
  used for tuning. Obtain independent adjudication for ambiguous answer keys.
- `trial_count`: a positive predeclared number of trials.
- `maximum_spend`: uppercase three-letter `currency` and integer `micros`
  (millionths of that currency). A paid experiment needs a positive cap. An
  unpaid experiment may explicitly declare zero. Missing caps are never valid.
- `promotion_criteria`: `deterministic_false_passes` must be explicitly zero;
  `max_false_approval_rate`, `max_false_blocker_rate`, `min_coverage`, and
  `min_support_quality` must be predeclared; `max_cost_micros` and
  `max_latency_millis` must be positive per-decision limits. Cost uses the same
  currency as the total cap. Trial uncertainty and denominators must accompany
  results. These are declarations, not measurements or proof of safety.
- `model_routes`: nonempty records with explicit `role`, `provider`, and `model`.
  Distinct routes can have the same role. Record any route or provider changes.
- `cache_settings`: `mode` is `cold`, `warm`, or `both`; `settings` records
  provider cache configuration, including explicitly disabled caching. Measure
  cold and warm runs separately, with cache writes and repairs included in cost.
- `mode`: `shadow` (observational only) or `live` (affects decisions).
- `paid`: whether the experiment incurs model/provider charges. Default false
  does not relax any other pre-registration requirement.

The validator checks record structure, required categories and numeric bounds.
It cannot verify that identifiers are immutable, a baseline is suitable, cases
are held out, a provider really is free, spend stayed under the cap, or declared
promotion criteria actually passed. External approval and result review remain
mandatory; this code executes nothing.

## Ordered experiments

1. Run deterministic retrieval and trust fixtures first.
2. Compare the current hybrid representation, TOON and compact JSON using the
   same selected evidence, ordering and budget. TOON remains the default
   direction; a serialization comparison must not change evidence selection.
3. Compare excerpts and progressive expansion separately from serialization.
4. Compare current prefix classes with a universal full-spec prefix, keeping
   required coverage and evidence fixed. Include cold/warm costs and full-run
   latency, not only isolated reply tokens.
5. Compare fixed unresolved packets across the current reviewer, an economical
   conversational adjudicator and a specialized decision model. Evaluate both
   isolated replies and complete workflows.
6. Repeat across at least two configured routes and several project types
   before claiming model independence. Include a real non-domain-specific suite
   alongside existing real suites. Fake fixtures do not replace real evaluations.

Primary outcomes: false approval, false blocker, incorrect absence, missed
reuse/extension, citation/support quality, answer coverage and downstream
acceptance. Secondary outcomes: total cost per correct decision, full task
latency, calls, duplicate reads, measured tokens, and context/output sizes.
Always report variation across trials, explicit comparable-subject denominators,
exclusions, provider failures and unknown instrumentation.

## Promotion rules

- Require **zero false passes** in deterministic trust fixtures.
- Predeclare acceptable false-approval and false-blocker rates, minimum coverage
  and support quality, and cost/latency limits for the chosen sample.
- An aggregate improvement cannot offset a failed trust criterion.
- An inconclusive trial keeps current defaults. A small clean sample is not
  evidence that the system is safe in all settings; report uncertainty.
- Do not enable adjudication or reduce mandatory reviewer coverage until the
  declared criteria pass and approval is recorded.

## Absence coverage records

A review answer may cite additive `lookup_ids` for search coverage. Each ID must
name a known lookup of the same current subject. A zero-result search has no
source evidence row. A `new` assessment can instead cite that completed search,
with explicit support and rationale still required. Reuse/extension claims still
need evidence. The trusted check's `search_scope` declares the whole coverage
target (`.` for repository-wide gate checks); the search must match that target
after path normalization. Missing scope, partial/narrowed coverage, lower-bound
counts, omitted windows, provider failures and unavailable tools cannot approve
absence. These records do not themselves grant product-decision authority.

## Offline fixture selection

From `skills/orchestrate/tool`:

```sh
go build ./... && go test -p 2 -count=1 -run 'Fixture|Manifest' .
```

`testdata/fixtures/{go,py,ts}` hold generic file-name parsing libraries, same-name
symbols in separate modules, test-only, generated-path and vendored matches.
`mixed` has independent Python processor and TypeScript CLI roots. Test helpers
copy fixtures to committed temporary repositories and use local fixed-output
search transcripts, fake semantic backends, fake model runners and pipe-backed
LSPs. Trust controls include complete reuse, partial reuse, new behavior with no
candidate, unsupported claims, invalid candidates, malformed replies, owner-only
product decisions, future-file ownership, stale subjects and dirty integration.
No installed language server, live index, model, network or credentials are
required. No optional real-backend smoke test is included, so missing tools do
not silently turn into fixture passes. Any future real-backend smoke test must
be separate and explicitly skip with its unavailable-backend reason.
