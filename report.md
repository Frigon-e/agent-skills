# Paid ctx-gate experiments

## Status

Phase 1: pre-registration only. No paid model request has started. Approval is required through decision `manifest` before any paid call.

- Branch: `orch/paid-experiments`.
- Code baseline: `8a062d2c2f8d15c0916e9bd87ba6cc58378e7c1b`.
- Manifest: [`checks/manifest.json`](checks/manifest.json).
- Paid ledger: [`checks/ledger.jsonl`](checks/ledger.jsonl), currently empty.
- Verified experiment spend so far: **$0.00**. No gate, shadow, adjudicator, classifier, decision API, model scout, or model-based web-search call was made.
- Estimated planned spend: **$1.04**. This is a planning estimate, not measured cost.
- Sum of enforced per-run ceilings: **$3.36** across 28 planned runs.
- Absolute user cap: **$5.00**, including failed calls and repairs. Admission cutoff: $4.50. Stop starting experiments at $4.00.

This report is not a completion claim. Paid execution, results, final live-store verification and final acceptance remain pending.

## Why these experiments

The manifest selects experiments 2 and 6 from the implementation plan, in that order:

| Experiment | Design | Planned runs | Estimate | Sum of run ceilings |
|---|---|---:|---:|---:|
| E2: equivalent representation | Fixed evidence as hybrid text, TOON and compact JSON; fixture packet and real CRM rendering-seam packet; two trials per cell | 12 | $0.24 | $0.96 |
| E6: current gate route smoke | Default Flash and configured GLM; CRM, Go, Python and TypeScript; two trials per cell | 16 | $0.80 | $2.40 |
| Total | No extra trials or paid fallback routes | 28 | $1.04 | $3.36 |

E2 is an isolated reply test. It cannot establish a complete-workflow format win. Its constructed hybrid retains identical fields but is not described as an exact historical gate prompt.

E6 runs real gate commands with `--ctx-shell` at its current `open` default. Fixture runs include both adversaries. CRM uses one explicitly recorded experiment group against the full exact real spec, so its group result cannot authorize planning or stand for a complete CRM review. Both routes use OpenRouter; this is not a provider-independence test. The output ceiling is an experiment safety constraint, not a changed default.

Experiments 3, 4 and 5 are deferred, not negative results. A default-versus-lean profile test would confound excerpts and formatting. A universal-prefix comparison needs dedicated cache/workflow controls. The production legacy question adapter does not delegate technical decisions to the optional adjudicator, so merely enabling that role would not test an authorized adjudication workflow.

## Evidence checked before approval

1. Read the implementation plan's ordered experiments, benchmark/experiment documentation, deterministic before/after proof, and the C1, C2, C3, B5b and F2 delivery reports.
2. The requested `c3-hardening/report.md` is absent. Its task directory contains task/status/inbox metadata, not a delivered report. This is an evidence gap, not a reason to invent conclusions.
3. `orch doctor` reports OpenRouter, OpenAI and Anthropic credential readiness. The default gate route is `pi openrouter/~deepseek/deepseek-flash-latest`. Claude Haiku is currently disallowed, so it is not a fallback in this plan.
4. `orch models` confirms that Flash and `openrouter/~z-ai/glm-latest` are allowed configured routes. No allowlist, role or default was changed.
5. `pi --list-models deepseek` advertises a 943,718-token maximum output for default Flash. A turn budget and an after-the-fact dollar total alone are not a sufficient protection for a $5 experiment.
6. An unpaid fetch of `https://openrouter.ai/api/v1/models` reports Flash input/output prices of $0.07/$0.50 per million tokens, and GLM $0.079/$1.40. Both advertise a 1,048,576-token context. These are proposed bounds/estimates, not actual usage measurements. Revalidate them before dispatch.
7. The current integrated code already has a shared gate RunBudget. Its documentation explicitly labels token enforcement `reported_usage_and_reserved_allowance`; opaque tool-loop usage is reconciled after an invocation. Do not mistake that for a provider-level prepaid dollar cap.
8. The real CRM spec is the copied, byte-exact `b5811b75d2b7e626023014fdcb13eb02e65377e359019cff150a7aec927158f4` object. The proposed current source is committed CRM HEAD `a87b826d30db1c1d07a5adaab02f53a39475dec9`, not the user's five uncommitted working-tree changes. Rendering evidence uses committed blob `3531e14ca4d92c220b99ca0330a3116ffb13ddfb`. No current correctness is inferred from old keys.
9. The Go/Python/TypeScript fixture bodies all trim without case conversion. These give unambiguous complete-reuse and partial-extension controls. Contradiction, insufficient evidence, authority, same-name and failed-provider cases are declared in the manifest before paid output.

`orch doctor` also reports no semantic index in the experiment worktree and no TypeScript language server. This is known degraded retrieval readiness, not a silent clean result. No backend was installed or default changed. The run report must retain partial/unknown instrumentation instead of treating it as an absence finding.

## Required spending guard

Paid execution remains blocked until approval **and** a no-spend transport preflight pass.

Use temporary configuration and test-only execution tooling, not production edits. Keep Pi, the configured provider/model IDs, and the gate's open-shell setting. A temporary model override caps output at 4,096. A local accounting proxy must verify that cap on actual serialized requests against a fake upstream before any real request. It must serialize upstream calls, pin allowed models, reject unbounded billable options, sync admission records, and reserve the whole conservative next-request ceiling before dispatch.

At the observed prices, full advertised context plus 4,096 output tokens costs at most $0.07544832 for Flash and $0.088572928 for GLM, before any extra fee. Proposed conservative reservations are $0.08 and $0.10. A reliable complete fee/usage bound is a precondition, not an assumed fact. Run ceilings are $0.08 for E2 and $0.15 for E6. The proxy must refuse a new request if the remaining run/global allowance cannot cover the whole reservation, even when normal replies cost much less.

Provider-reported charges and request IDs must reconcile with actual generation usage where available. Preserve Pi session/catalog totals and `gate.json` pass totals separately; do not relabel a catalog estimate as a verified provider charge. An unreconciled timeout, crash, missing cost, unexpected fee or bypass-capable model workflow stops further spending and requires a new decision. No paid fallback, retries outside a declared run allowance, background classifier, default change or additional experiment is approved by unused budget.

The temporary guard is not implemented or verified yet. If this proves infeasible without a production/default change, stop and escalate rather than relax the cap.

## Scoring and interpretation

Use the existing deterministic reducer/evidence validation, `orch gate compare`, and isolated-store `orch bench record/compare/report/verify`. Do not generate a supporting rationale, lookup proof or authority on behalf of a model that omitted it. An adapter may carry trusted envelope identity, not invent model support. Historical answer keys do not map automatically onto current CRM code.

For every experiment, report false approvals, false blockers, incorrect absence, missed reuse/extension, support quality, present/valid/supported coverage, all-in cost per supported correct decision, isolated and complete-command latency, and downstream acceptance or an explicit `unknown/not measured`. The manifest defines denominators. Missing and unsupported replies stay in the full population. A cap-exhausted incomplete review is not silently scored as a supported blocker.

Predeclared trial screens are zero deterministic false passes, zero model false approvals, at most 10% false blockers, at least 90% coverage, at least 95% support quality, at most $0.10 per correct decision and at most 600 seconds per complete measured command. Trust failures veto aggregate improvements. Two trials per cell are too small to establish safety or route independence, even if all are clean. Numeric screens do not authorize a default change.

## Live bench store protection

The live store was opened only for read-only discovery and a copy. Every benchmark command after discovery points `ORCH_BENCH_DIR` at the private copy; paid recordings will use temporary storage as well.

- Live store: `~/.local/share/orchestrate/bench`.
- Initial listing: 568 entries, including 466 regular files, 23 runs and 2 keys.
- Initial listing SHA256: `e2f6647ced1ea8ac251436b083d8374ef620580a608070b3f90a87a026c29f1c`.
- Listing fields: relative path, type, mode, size, nanosecond mtime and regular-file SHA256.
- Private listing/copy location: recorded in the task directory at `checks/work-dir.txt`. Store contents, private source/spec/keys and raw model transcripts will not be committed.
- Phase-1 after-listing comparison: PASS, byte-identical to the initial listing, including all 466 file hashes, sizes, modes and mtimes.
- Final listing and byte-for-byte comparison: pending after execution or cancellation. Never modify the source to make a comparison pass.

## Verification

A CPU-locked background preflight runs the required repository checks and deterministic fixture/manifest selection:

```sh
cd skills/orchestrate/tool
go build ./...
go vet ./...
go test -p 2 -count=1 -run 'Fixture|Manifest' .
```

Outcome: **PASS**, exit 0. Build and vet emitted no failures. The focused fixture/manifest suite passed in **17.960 seconds**. The task acquired and released the CPU lock successfully. This command made no model calls and did not run the full test suite.

Evidence: background task `b9b8d4023`, output `.pi/tasks/cfaff92d-0a36-4cc7-9074-22d8e4108bf0-58502/b9b8d4023.output`.

Independent no-spend manifest checks passed:

- The integrated `experimentManifest.ValidateRunnable` accepted the manifest when run from a temporary stdlib-only harness.
- Exact decimal arithmetic verified all 28 runs, the $1.04 estimate, and the $3.36 sum of run ceilings below the $4.00/$4.50/$5.00 stop/admission/hard limits.

Successful manifest validation grants neither spending authority nor a default change.

## Results and recommendations

| Experiment | Result | Current recommendation |
|---|---|---|
| E2 format packets | Not run; approval and safe transport pending | Keep current TOON direction; no representation winner claimed |
| E6 route smoke | Not run; approval and safe transport pending | Keep current default Flash route; no independence claim |
| E3 excerpts, E4 prefixes, E5 adjudication | Deferred | Keep current excerpt/prefix behavior and adjudication disabled |
| Question classifier | Outside the selected experiments | Keep current classifier defaults; no promotion |

Ledger total: **$0.00**. No paid evidence exists yet.
