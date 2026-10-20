# Context and gate implementation plan

Date: 2026-10-19

Status: Updated implementation plan. Confirmed requirements: trust and completion fixes first, then the evidence contract and benchmark fixes; TOON for agent-facing information where practical; compact readable JSON for human questions and structured views; preserve --json, shared prompt caching and current model switching; add no mandatory agents. The exact new public status schema, requirement-ID syntax and optional adjudication configuration remain proposed implementation details. This document does not enable runtime behavior.

## Objective and limits

Make ctx results truthful about what was searched and shown. Make gate approval mean that the required review finished on the version it approves. Preserve Go, current model selection and fallback, native file reading, and support for Python, Go, TypeScript and mixed repositories. Keep changes small enough to review and release independently.

Success means the reproduced false passes, stale approvals, false absence reports, wrong-symbol resolution and authority-routing failures have regression tests; tested and integrated commits match; JSON and TOON carry equivalent selected information; benchmark denominators and unknown values are correct. Repository-specific vocabulary must not appear in core approval rules.

This plan does not require a new orchestrator, plugin framework, policy language, mandatory additional judge, or XML prompt format. Live model experiments follow scorer repair and use an explicit run budget. Existing private benchmark originals remain immutable.

## Plain-language gate behavior

The model reads code and makes judgments. Ordinary Go code then applies fixed rules to the recorded answers. This final function is what the earlier review called a deterministic reducer. Identical recorded inputs and policy produce the same status; the function performs no model calls, searches or writes.

The rule in plain English: **A pass means the required review finished, its answers meet the review rules, and it applies to the code version about to be used.** It does not mean Go has mathematically proved every model interpretation correct.

Example: a Python CSV exporter has three required checks. Ownership is valid. A reviewer finds an existing writer and recommends extending it. The public behavior for overwriting files is still undecided. The gate reports incomplete and names that decision and its owner. It does not discard the useful review, invent the overwrite policy, or accept a concluding “looks good.”

### Overall status and detail

Evaluate these rules in order. Keep findings, completion counts, failures and next actions in the report even when one status takes precedence.

| Condition | Status | Next action |
|---|---|---|
| Current subject cannot be established, or required system data is corrupt | error | Repair the specific system failure |
| The reviewed subject differs from the current subject | stale | Review the changed version |
| An accepted blocking finding exists about the current subject | fail | Resolve the named problem; unfinished checks remain listed |
| Required work remains unanswered, unsupported, unavailable, or beyond its budget | incomplete | Resume the named review, retrieve evidence, or ask the authorized decision-maker |
| All required checks have valid dispositions and no accepted blocker remains | pass | Proceed using the recorded subject |

Provider unavailability after fallbacks is incomplete, including when no reviewer answered, provided the subject and required-check records are valid. Optional diagnostics failing do not block approval. A required check is one identified by the task's versioned review policy, not every capability the tool happens to provide. An optional adjudicator being unavailable does not create a new requirement, but it cannot erase the unresolved required check that prompted escalation.

Malformed or missing model replies also leave required checks incomplete after bounded recovery. Reserve error for invalid system state or an unidentifiable subject, rather than using it for every unsuccessful model attempt.

Use a small reason enum for incomplete work: missing_answer, unsupported_claim, awaiting_decision, dependency_unavailable, budget_exhausted. Record the responsible actor and the next action. Do not add a new overall status for each reason.

Maintain separate fields for answer state, behavioral judgment and support. A nonempty verdict is not automatically a valid answer. A valid source location is not automatically a supported interpretation. A model confidence score is not a substitute for either.

A finding is accepted for gate purposes when its subject and references are valid, its required support fields are present, its author is an allowed assessor for that check, and the versioned policy permits that finding to block. Go can establish structural violations such as ownership collisions itself; semantic findings retain the responsible model's interpretation and are not presented as mechanically proved. Unsupported findings remain claims and leave the relevant required check incomplete. An accepted blocker stays active until a policy-authorized resolution explicitly addresses its ID and evidence. Record proposed, accepted, rejected and resolved dispositions with actor and reason; do not infer resolution from silence, a newer verdict or confidence. Conflicting claims without an accepted blocker remain incomplete. This preserves the status precedence above without treating model disagreement as automatic approval.

### Required checks without excessive process

Derive the default review list from existing spec sections and task kind. Do not require the author to hand-write a second checklist duplicating every Builds bullet. Every requested package review must explicitly answer scope, unresolved decisions and applicable reuse checks. Missing search lines do not exempt a feature from review.

Allow not_applicable only for an identified check, with a reason validated against the policy. For example, a pure spelling correction need not perform semantic reuse analysis. Missing tools, failed searches and exhausted budgets are not reasons to call a required check inapplicable. Preserve advisory findings without making every uncertain suggestion a blocker.

## Responsibilities across the workflow

| Work | During spec creation | During the gate | After implementation |
|---|---|---|---|
| Intended behavior and acceptance | Author and user establish it | Review for contradictions or consequential gaps | Run the acceptance checks |
| Ownership and dependencies | Author proposes them | Go validates the graph; reviewer checks feasibility | Go checks the actual change scope |
| Existing implementation | Author may gather useful evidence | Refresh evidence and assess reuse or extension | Verify the implemented choice |
| Product or public API decision | Resolve with the authorized owner | Return a precise new question if a gap is discovered | Do not silently change the agreement |
| Delegated technical choice | Author may settle it | Decision model may resolve it within recorded authority | Implement and test it |
| Evidence validity | Record source, version and limitations | Check subject, source hashes and coverage | Invalidate changed evidence |
| Approval | No final code-dependent approval | Go calculates readiness from explicit records | Test and integrate the exact candidate commit |

The authoring model and gate may use the same retrieval/evidence types. Reuse still-current observations to avoid repeated work, but treat author conclusions as claims that a reviewer may challenge. Only consequential new questions return to the user; delegated implementation choices stay with agents.

### Spec to gate: current and proposed flow

These are logical jobs, not separately installed agents or necessarily different models. Steps 4 and 5 can run concurrently. Optional authoring helpers belong to the brainstorm workflow; gate does not automatically launch them.

```json
[
  {
    "step": 1, "stage": "Write the spec",
    "now": "Interactive author; brainstorm can add a facts scout and three design lenses.",
    "after": "Keep the author and optional design helpers. Record behavior, acceptance and decision authority."
  },
  {
    "step": 2, "stage": "Prepare search queries",
    "now": "Author supplies queries; spec_queries can propose missing queries on request.",
    "after": "Keep generation optional and batch missing queries. Missing queries never waive review."
  },
  {
    "step": 3, "stage": "Validate and assemble evidence",
    "now": "Go parses the spec, groups packages and builds dossiers. Plan mode optionally adds a planner per group.",
    "after": "Go also binds source versions, required checks and search limitations. Extra planning stays optional."
  },
  {
    "step": 4, "stage": "Review package groups",
    "now": "One model job per group handles scope, reuse and questions together.",
    "after": "Keep those jobs. Require explicit valid answers and evidence for every applicable check."
  },
  {
    "step": 5, "stage": "Challenge the whole design",
    "now": "advA challenges design rules and authority; advB challenges cross-package behavior and acceptance gaps.",
    "after": "Keep both jobs initially, with fresh conversations and shared cacheable spec context."
  },
  {
    "step": 6, "stage": "Resolve incomplete answers",
    "now": "Bounded retries; optional verdict repair and per-item or per-package shadow experiments.",
    "after": "Target missing evidence or invalid fields first. Optional adjudication handles remaining delegated disputes."
  },
  {
    "step": 7, "stage": "Calculate gate status",
    "now": "Go aggregates results, with the completion and trust gaps identified in the review.",
    "after": "Go checks versions, required answers and blockers. No approval-model call."
  },
  {
    "step": 8, "stage": "Resolve questions and revise",
    "now": "Author or user resolves questions; spec changes lead to another gate run.",
    "after": "Go renders compact JSON questions. Preserve answers and authority; invalidate approval when the subject changes."
  }
]
```

With three package groups, both the current ordinary gate and the proposed ordinary gate have five review jobs: three group reviewers and two adversaries. This excludes optional authoring helpers, query generation, plan mode, retries and explicitly enabled experiments. A job can make several provider requests. No new mandatory reviewer, router, formatter or final approval agent is introduced.

During spec creation, keep intended behavior, acceptance, ownership, dependencies, search/reuse hints and authority in the existing spec format. Add stable requirement IDs as proposed in B2, without a second manually maintained checklist. Go derives required checks from the spec and versioned task policy. Authors may propose an exemption but cannot silently remove a policy-required check. Product decisions reserved to the user remain unresolved until that owner answers. Technical decisions already delegated can be settled by the author or reviewer without a separate decision-model call.

## Decision models

A decision model is a good fit for interpreting whether code provides a complete implementation, a useful partial implementation, or unrelated behavior. It can compare conflicting findings, explain the remaining behavior, suggest the next lookup, and draft a clear question. Better context and questions are central to improving this work.

The model cannot waive required review, declare its own authority, ignore subject changes, or turn an invalid response into approval. Go owns those rules. A model may recommend a classification or resolution; acceptance still requires a valid response tied to the correct item and evidence. Semantic correctness remains an evaluation/review problem.

Use the existing role and runner configuration. Keep the current main reviewer. Evaluate the decision model in explicitly enabled, budgeted shadow experiments, then enable targeted adjudication only if calibrated results justify it. Shadow calls are off during ordinary runs. If enabled, a judge sees source excerpts and competing explanations, not just previous reviewers' labels.

Recommended response fields: item_id, item_revision, answer_state, judgment, evidence_ids, matching_behavior, missing_behavior, rationale, next_action, question_ids. Reuse judgments retain existing reuse/extend/new/unclear semantics. “New” means no comparable behavior was identified under adequate recorded searches, not a proof that no implementation exists anywhere.

Keep model replies in validated JSON initially, including transports without native structured-output support. Agent input and agent-directed status use TOON; human-directed status and questions use compact readable JSON. Model-output TOON can be a later adapter option after its parsing and repair behavior is measured; it is not needed to deliver TOON information to agents.

### Where each model runs and why

These are logical roles, not a requirement to deploy a different model for every row. Select actual models through configuration and measured task quality. A role may share a model with another role without sharing its conversation history.

| Stage | Executor | Input and output | Cost policy |
|---|---|---|---|
| Spec creation | Existing interactive authoring model | User intent and selected code evidence -> spec, delegated choices and unresolved questions | Use the conversation already underway; no extra writer or formatter call. Stronger reasoning is useful while consequential design remains open |
| Spec parsing, ownership, subject capture | Go | Spec and repository state -> exact validation records | No generative model call; stop invalid input before paid review |
| ctx retrieval and dossier assembly | Go plus configured search/LSP backends | Queries and checkout -> evidence packet | No generative reviewer call for ordinary retrieval or formatting. Semantic indexing/embedding services may still have their own cost |
| Missing query proposal | Existing spec_queries role, on demand | Selected requirement and known names -> proposed queries | Use only when useful queries are missing; batch the selected requirements, avoid a call per item |
| Main gate review | Configured economical gate model | One bounded packet per related package group -> scope, reuse, questions and support judgments | One review job per group, containing all its items. Tool turns are bounded; a job may involve multiple provider requests |
| Cross-package adversarial review | Existing configured gate reviewers | Shared contracts and relevant current evidence -> cross-package findings | Preserve existing two adversary passes during trust fixes. Test fewer or conditional passes only after scorer repair; do not add another mandatory layer |
| Missing evidence or malformed answer | Same review job where possible | Specific missing span or invalid fields -> targeted completion | Prefer Go validation and precise retrieval; at most one schema repair attempt initially. Never restart all completed groups for one bad answer |
| Consequential ambiguity or disagreement | Optional decision/adjudication model | Unresolved items, source excerpts, competing explanations and authority -> resolution or specific next action | Batch related unresolved items. No call on the ordinary resolved path. A stronger model is used only when the configured escalation policy requires it |
| Human question formatting | Go renders fields already produced by the author/reviewer | Question record -> compact readable JSON | No separate model for prose cleanup, serialization, deduplication or choosing format |
| Gate status and scheduling | Go | Validated answers, findings and current subject -> status and next actor | No model call to decide whether a response is complete or the review is stale |
| Implementation | Existing assigned lead model | Accepted task and evidence -> code and tests | Preserve current task/model routing; gate work does not add an implementation model |
| Acceptance and integration | Go invokes configured commands | Pinned candidate -> test results and integration record | No model on successful checks. Failures return evidence to the responsible lead |

The main reviewer already performs decisions such as reuse versus extension. A separate decision model is only an escalation path for cases that remain unresolved; it is not a compulsory step after every reviewer. Do not ask a model to route every model call. Go can route by configured role, capabilities, validated failure reason and remaining budget.

Keep gate adjudication configuration separate from orchestration triage even if both use the same provider/model. An optional gate_adjudicator role can reuse the existing runner mechanism; adding it must not silently change the current decider or triage behavior.

### Decision-model setup and contract

A specialized decision model is an alternative to a conversational model for an optional adjudication job. It is not an alternative implementation of the deterministic reducer. Both model types make fallible semantic judgments. The Go reducer is ordinary code and must never call either model. A tool-less decision API also cannot replace a code-exploring reviewer unless an independent experiment establishes equivalent required coverage; that replacement is outside this rollout.

Keep configuration in the existing machine-level configuration and runner/connector boundaries; do not put provider names, credentials or model choices into project specs. Preserve current ordered choices, command overrides, environment precedence, disabled-role behavior, connector health filtering and approved degraded-profile behavior. A new optional gate role must default to disabled when absent rather than inherit an enabled default. It may point to the same provider/model as another role without changing that role or sharing its conversation.

The following is a human-readable description of the proposed setup, not an existing CLI or configuration schema:

```json
{
  "gate_review": {"role": "gate", "routing": "existing configuration and overrides"},
  "gate_adjudication": {
    "enabled": false,
    "role": "gate_adjudicator",
    "trigger": "unresolved delegated judgment after targeted evidence retrieval",
    "model_choices": "ordered existing connector/model routes",
    "batching": "related unresolved items within context and run budgets",
    "initial_attempt_limit": 1,
    "schema_repair_limit": 1,
    "budget": "shared with the gate run"
  },
  "gate_status": {"executor": "Go", "model_calls": 0}
}
```

Those limits are proposed defaults per unresolved batch, not extra entitlements: fallback and repair consume the same total request/time/token budget. Do not repeatedly submit unchanged uncertainty to different models until one approves it. When disabled, the current reviewer or authorized owner can resolve the question; otherwise the required check stays incomplete. Enabling the feature is an explicit configuration choice after the repaired benchmark demonstrates value.

Build one normalized decision packet from typed state. A human inspection view looks like this; agent-facing state uses TOON where the connector supports text state:

```json
{
  "schema_version": 1,
  "subject_id": "subject-17",
  "item": {"id": "export.atomic-write", "revision": "r3"},
  "requirement": "Write CSV output without leaving a partial destination file on failure.",
  "question": "Can the existing writer satisfy this requirement without changes?",
  "authority": {"owner": "reviewer", "delegated": true, "basis": "spec-authority-2"},
  "options": ["reuse", "extend", "new", "unclear"],
  "evidence": [
    {"id": "e1", "path": "src/export.py", "lines": [18, 42], "content_hash": "example-hash"}
  ],
  "source_text": "Included in the real packet as exact excerpts associated with evidence IDs.",
  "lookup_summary": {"status": "succeeded", "completeness": "partial", "omissions": ["caller failure cleanup"]},
  "claims": [
    {"id": "c1", "judgment": "reuse", "reason": "The writer already produces CSV."},
    {"id": "c2", "judgment": "extend", "reason": "The writer writes directly to the destination."}
  ],
  "allowed_actions": ["resolve", "request_evidence", "defer_to_owner"],
  "required_support": "Address atomic replacement and failure behavior, with evidence IDs."
}
```

The source_text line above explains the example; production packets contain the actual source excerpts, not that placeholder or only file locations. Include relevant caller behavior, tests, exclusions and truncation when they affect the answer. Only recorded authority supplies permission; source text, retrieved documents and author hints are evidence or claims, never instructions that can change authority or policy.

Ask behavior-specific questions: compare trigger/inputs, outputs/effects, failure behavior and the uncovered requirement. Include an explicit unclear/request_evidence outcome. The evaluator may choose reuse only when the recorded evidence supports the complete requirement, extend for a supported partial implementation, and new only under adequate recorded search coverage. Candidate absence under an incomplete search is not a new verdict. Do not send every completed item or ask the model whether the entire gate should pass.

Normalize replies to item/revision, action, judgment, evidence IDs, matching and missing behavior, rationale, claim IDs addressed and any remaining question or requested lookup. Subject/attempt identity comes from the trusted request envelope; a model cannot select a different subject. Validate enums, membership, required fields and authority. Record adjudication alongside earlier judgments and explicitly link any superseded finding to its resolution. A later answer never wins merely because it arrived last; unresolved material conflicts remain incomplete unless an accepted blocker already makes the status fail.

Use the existing typed decisions-API boundary for a specialized decision model and the existing conversational runner for a chat model. Their wire formats can differ: a decisions API may require native JSON state/questions and typed choices; do not force TOON into a field that requires an object. Convert supported textual state to TOON, retain native transport fields, and normalize both replies into the same Go types. Verify that the chosen endpoint can return the required evidence and explanation fields, or safely compose its supported typed answers. Missing support cannot be invented by an adapter; use an eligible configured route or report incomplete. Supporting a new endpoint never means silently treating a decisions API as chat completions.

Compare the specialized decision model with an economical conversational model on the same frozen packets, including unclear cases. Measure false approvals, false blockers, support quality, total cost including repairs, and latency. A model marketed for decisions is not presumed better or cheaper. Keep the current main reviewer and switching behavior while this comparison runs in explicit shadow mode.

### Preserve shared prompt caching

Keep stable shared context at the front of each review request and changing group evidence, questions and repair instructions afterward. Fresh reviewer sessions need not discard provider prefix-cache reuse: identical effective prefixes can be reused where the configured provider supports it. Cache behavior depends on the model/provider, tools, prompt bytes, lifetime and routing; a shared local file alone does not guarantee a hit. Measure reported cache reads and writes rather than assume them.

The current gate has two prefix classes: adversaries share the full numbered spec; package reviewers share the non-package spec sections plus the package index, followed by their group bodies and dossier. Preserve these classes during the trust fixes. Treat a full-spec prefix for every group as a separate benchmark candidate, not a prerequisite for the evidence contract. Retain the existing warm-up scheduling until measurements justify changing it.

Reuse stable source material, not an ever-growing transcript of unrelated reviews. Cached tokens still occupy context. Keep each group's changing suffix relevant, and give independent adversaries fresh conversations. Render shared TOON once per subject and format version with deterministic ordering; keep per-attempt timestamps and IDs outside the shared prefix. A spec or relevant code change invalidates approval even when part of its prompt remains cacheable.

Keep three kinds of reuse separate: provider prompt caching reduces repeated input processing; evidence reuse avoids repeating still-valid lookups; approval reuse authorizes work only for the bound subject and accepted policy. A cache hit proves neither evidence freshness nor review completion. Correctness must be identical with caching unavailable or disabled. Switching models may reuse validated evidence, but must not assume provider cache transfer or carry an incompatible conversation transcript. Fresh attempts receive the stable prefix plus current unresolved state and evidence.

Budget the complete request: shared prefix, selected dossier, tool results and response reserve. Do not budget only the uncached suffix. When a route cannot fit the packet, apply the same deterministic evidence selection and explicit omissions or choose another eligible configured route; do not silently drop requirements or authority. After the bounded repair attempt, resume from explicit records in a fresh session only under the remaining run budget. Compaction or a fresh conversation never resets completion, freshness or spend accounting.

Acceptance: byte-identical shared inputs produce identical prefixes across group order and scheduling; attempt IDs/timestamps do not disturb the prefix; changed authoritative inputs change the relevant prompt bytes; each independent reviewer starts without previous reviewers' transcript; fake runners verify preserved routing and warm-up behavior. Benchmarks separately label cold and warm runs, prefix class/digest, provider/model route, cache settings, known cache reads/writes, total input and suffix sizes, total cost and full-run latency. Unsupported counters are unknown. Compare a universal full-spec prefix against the current two classes only with equivalent required coverage and controlled evidence.

### Normal path and escalation budget

For G package groups, the current baseline is G main review jobs plus the existing two adversary jobs, with bounded tool turns. That is G+2 jobs, not G+2 guaranteed API requests. A fully resolved run adds zero adjudication jobs, zero question-formatting jobs and zero production shadow jobs.

An unresolved technical question first requests the specific missing evidence. Reuse still-valid observations within the same subject. If judgment remains unresolved, the question is within delegated authority and optional adjudication is enabled, send only that unresolved packet to the configured adjudicator. Otherwise keep the question with its authorized owner. A product decision reserved to the user goes directly to the user as JSON rather than paying another model to guess the preference.

Configure a shared run budget covering requests, input/output allowance, elapsed time, repairs and model fallbacks. Set per-job limits inside it. Do not reset counters on retries or a provider switch. Stop with incomplete and a useful next action when a required review exhausts its budget. Hard token/request/time limits still work when dollar cost is unavailable; dollar enforcement requires a reliable configured estimate and must distinguish estimates from reported usage.

Report authoring, evidence/indexing, review, adversary, repair, adjudication and implementation costs separately where measured. Missing costs remain unknown. The review comparison must include follow-up calls and repairs, not just the first prompt. Choose the cheapest model route that meets the calibrated quality target for that role; nominal price alone does not establish cost effectiveness.

After benchmark repairs, compare the current G+2 baseline against a candidate G+1 cross-package review and carefully scoped risk-based scheduling. Changing required review coverage is a separate measured policy change; the initial trust fixes preserve it. Evaluate fewer calls, smaller packets and targeted adjudication independently.

## Information formats

Use one selected result object and two serializers:

```text
Go result or external JSON result
  -> validate and normalize
  -> select fields, excerpts and budget once
  -> TOON for agent information
  -> compact readable JSON for human questions and system views
  -> JSON for --json and machine use
```

For orch-owned commands, serialize the underlying Go value rather than launching the CLI to parse its JSON output. For external tools that offer JSON, consume the structured response, check errors and completeness, and encode the normalized result as TOON. Preserve unknown values, IDs, numeric types, ordering and omission information. Conversion cannot supply evidence that the input never recorded.

Apply TOON to the agent-facing versions of status, lookup results, scope/exclusion metadata, candidate inventories, evidence references, findings, decision questions, coverage and benchmark summaries. Render human-facing versions as compact readable JSON. Use nested objects or lists when rows are not uniform. Do not manufacture a dense table for arbitrary prose.

Compact readable JSON means concise fields, two-space indentation for the outer object, and short scalar option objects kept on one line when they fit. It does not mean minifying an entire question onto one line. Use plain-language names and values; show the question, material context, choices, recommendation and required actor before deep provenance. Keep IDs that identify the decision, and provide explicit detail access for long evidence or logs. The renderer selects known fields mechanically and never rewrites meaning with a model.

Example human question view:

```json
{
  "id": "export.overwrite",
  "question": "What should happen if the output file already exists?",
  "context": "The exporter currently replaces the existing file.",
  "options": [
    {"id": "refuse", "effect": "Keep the file and report an error."},
    {"id": "replace", "effect": "Replace the file with the new export."}
  ],
  "recommendation": "refuse",
  "reason": "Avoid accidental loss of an existing export.",
  "answer_needed_from": "owner"
}
```

Choose the audience explicitly at the caller/UI boundary, not by guessing from a terminal or changing a user's existing --json contract. Proposed human-view selection is additive; a final CLI flag name is an implementation detail to document. Where a human view selects fewer fields, compare encodings of that same view rather than claiming it is byte-for-byte the full machine record. Required uncertainty, alternatives and unresolved decisions must survive selection.

Source code remains faithful source text. A strict TOON result encodes excerpt text using the encoder's string rules; native Read/read output remains native. Human-facing generated question and status records use readable JSON, with source references or correctly escaped excerpt strings. Explanatory documentation may remain prose. File bytes and line indentation must survive extraction unchanged.

Evaluate the Go implementation at https://github.com/toon-format/toon-go, pin an appropriate release, and run its relevant reference fixtures before adoption. Confirm supported Go version and numeric behavior. IDs and hashes are strings. Do not pass arbitrary JSON numbers through float64 and silently change them. Prefer one shared field definition over independently maintained JSON and TOON tags.

Preserve established --json schemas during the initial fixes. Add explicit schema_version on new versioned records; make incompatible CLI schema changes opt-in/versioned until consumers are migrated. Default TOON selection and --json must describe the same selected data. A full/raw view is a separate selection option, not a side effect of choosing JSON.

## Portability requirements

The core understands repositories, paths, ranges, requirements, ownership, evidence, questions and checks. It does not understand CRM entities or assume every project has an HTTP server, database, UI, migration folder or Go package layout.

Extend the existing boundaries rather than introduce a plugin framework:

- Language support stays in ctx_lang.go and existing gopls/TypeScript/generic LSP paths. Report definition/reference/implementation capability explicitly. A declaration guess never silently becomes an exact semantic reference.
- Project checks come from repository configuration and the spec. Python, Go and TypeScript select their own commands; the core does not hard-code pytest, go test, npm, frameworks, or directory names as universal requirements.
- Model routing stays in runners.go and existing role configuration. The same review contract applies across providers and harnesses. Record the actual selected route, attempts, prompt version and fallback; missing optional provider features must not redefine completion.
- Subject identity describes source/spec content. Execution identity additionally describes model, prompt, tool, policy, retrieval settings and budgets. Switching models can reuse current source observations; it creates a separately attributable review attempt. Reusing approval depends on subject and accepted policy, including any required reviewer role.
- Context budgets adapt to configured model limits and project size. Character/byte counts remain honestly labeled when token counts are unavailable. Provider token usage, when available, is recorded separately.
- Add pure Python, pure Go, pure TypeScript and mixed-root fixtures from the first fixes. Use generic CLI, parser, library and file-processing behavior. CRM is a real-world regression suite, never the sole key or acceptance criterion.

## Phase A Fix trust and completion

Implement these as small changes with their regression tests. Changes that touch gate.go are sequential. Do not delay the fixes for the larger schema or renderer redesign.

### A1 Require complete valid review responses

Files: tool/gate.go, tool/gate_reply.go, tool/gate_items.go, tool/gate_verdicts.go and corresponding tests.

- Validate required field presence and types, expected package/item membership and duplicates before applying responses. Unknown IDs and malformed enums are errors, not silently ignored entries.
- Distinguish omitted/null required arrays from explicitly empty arrays where empty is valid. Do not trust a completion boolean without actual coverage validation.
- Preserve salvaged findings for diagnosis; a salvaged fragment cannot establish completed review.
- Invalid candidate references leave the item unsupported and eligible for repair. Retry exhaustion or missing required answers prevents pass.
- Use the current bounded retry infrastructure with one explicit schema repair attempt as the initial proposed limit. Keep a total review budget; repair is not an unbounded new conversation.
- Verify future-file ownership conflicts against the spec graph rather than requiring the file to exist.
- Until the new status schema lands, use the existing unverified status plus precise reasons for incomplete work.

Acceptance: fake package-ID-only and {} adversary replies cannot pass; explicit complete empty findings can pass a genuinely clean fixture; invalid candidates are not covered; duplicate/unknown IDs fail validation; a future file in another package's owns blocks appropriately; downstream-owner wiring retains its documented treatment.

### A2 Bind approval to the reviewed subject

Files: tool/gate_checkout.go, tool/gate.go, tool/plan.go, shared fingerprint helpers extracted as needed; reuse relevant bench_subject.go concepts without making approval depend on benchmark archives.

- Record repository identity, spec content hash, commit/tree and dirty/untracked content fingerprint. Include deletions, modes and symlink targets where relevant. Porcelain filenames alone are insufficient.
- Capture review policy and context settings affecting approval. Recheck the subject at review completion and when planning consumes approval.
- Keep declared runtime artifacts and caches outside the reviewed source set to avoid self-invalidation. Do not exclude arbitrary source files merely to make fingerprints stable.
- Unknown/unbound legacy approvals remain readable but cannot authorize planning. Preserve history and request a fresh review; never synthesize historical identity.
- Begin with conservative whole-subject invalidation. Selective evidence invalidation is deferred.

Acceptance: unchanged subject reuses approval; body-only, untracked-content, deletion and mode changes invalidate it; identical spec on another repo cannot reuse it; review-generated artifacts do not invalidate it; an unreadable fingerprint prevents approval; legacy reports still render.

### A3 Test and integrate the same commit

Files: tool/verify.go, tool/integration.go, tool/integration_test.go and relevant orchestration tests.

- Require a clean candidate for the path that integrates. Pin HEAD before scope and acceptance checks.
- Recheck HEAD and working-tree state after checks, and integrate the recorded SHA.
- Continue post-merge checks. Bind the integrated marker to the tested candidate and integration result.
- Retain preliminary working-tree validation through the existing no-merge path, with output that does not imply integration approval.

Acceptance: untracked implementation cannot be silently omitted; a check that writes source or advances HEAD cannot authorize merging another version; clean candidate succeeds; merge conflicts and failed post-merge checks retain existing recovery behavior.

### A4 Make retrieval truthful

Files: tool/ctx_search.go, tool/ctx_render.go, tool/ctx_views.go, tool/ctx_decl.go, tool/ctx_symbols.go, tool/ctx_gopls.go, tool/cmd_ctx.go and focused tests.

- Stream/filter matches before retaining excerpts; count independently. If exhaustive counting is not completed, label a lower bound and partial search rather than invent a total.
- Render current checkout spans from semantic locations. Validate/relocate source ranges and refuse to silently mix indexed and checkout code.
- Preserve qualified symbol identity; label weaker fallback results as candidates. Apply path/kind filters before result caps and use path-component-aware scope checks.
- Validate batch options per lookup kind before dependencies run. Share those definitions with help and JSON plan validation.
- Return usage/runtime failures distinctly from successful empty searches. Preserve successful portions of partial batches; expose aggregate status and per-query errors. Define all-failed batches as failure.
- Propagate malformed backend streams, missing servers and search failures as failures/partial data, never zero matches.
- Make semantic pagination operate over visible results or report raw-rank limitations explicitly. Correct --all and --full descriptions/behavior; report any unavoidable backend cap and recovery route.
- Negative text results state searched scope and exclusions. Semantic candidates never establish absence.

Acceptance: >2,000 excluded hits cannot hide a production match as zero; unchanged function header with changed body renders current code; wrong receiver cannot resolve as exact; fifth scoped definition remains discoverable; irrelevant lookup options fail; malformed backend data cannot appear empty; missing servers are not zero references; pagination and generated/test filters remain consistent.

### A5 Preserve question identity and authority

Files: tool/gate_enrich.go, tool/gate_enrich_test.go, relevant question rendering and documentation.

- Only exact normalized duplicates collapse. Related questions may cluster visually but retain their IDs, evidence needs, authority and answer state.
- Route every member independently; a cluster displays the most restrictive unresolved authority and never loses member actions.
- Replace categorical absence statements with what the review actually established. Preserve original questions and answer history.
- Keep product choices with their declared owner; permit delegated technical choices without asking the user again.

Acceptance: retention plus test-layout questions remain independent; a longer question cannot lower another question's authority; a spec hint cannot coexist with an unsupported claim that no answer exists; answering one member leaves other questions open.

### A6 Repair launcher and essential CLI guidance

Files: scripts/orch, tool/main.go, tool/ctx_cli.go, tool/ctx_guide.go and associated tests/docs.

- Include pi_ctx_only.ts and every embedded asset in rebuild invalidation. Test with a temporary cached binary.
- Add inexpensive top-level version aliases without requiring repo state or model/backend initialization.
- Remove blanket claims that ctx proves absence or is universally faster. Recommend batches for independent lookups and native reads for source inspection.
- Document exact-current behavior for caps, failures and fallbacks; do not defer correcting misleading prompts until Phase B.

Acceptance: changing only an embedded policy triggers rebuild; version works outside a repo; instructions and help agree with the repaired behavior.

## Phase B Introduce the evidence contract and TOON output

### B1 Define versioned data shared by retrieval and review

Proposed new files: tool/evidence.go and tool/gate_state.go with their tests. Integrate through existing ctx and gate structs; avoid a rewrite.

Minimum shared records:

- Subject: source/spec identity and freshness basis.
- Lookup: ID, query, scope, exclusions, requested/succeeded/failed counts, execution status, completeness and count kind.
- Evidence: ID, subject, source kind, path/range, content hash and origin lookup. Track separately what was selected/rendered/read.
- Item: stable ID, content revision, package, requirement and applicable review checks.
- Review answer: item/revision, answer state, judgment, evidence IDs, rationale and missing information.
- Question: stable ID, linked item, options/impact, authority, answer and answering actor.
- Finding/resolution: stable ID, linked check, disposition, support, assessor, authority basis and explicit resolution/supersession links.
- Execution: model/harness route, prompt/tool/policy versions, attempts and usage availability.

Use complete/partial/unknown for completeness and exact/lower_bound/unknown for counts. Search execution distinguishes not_requested, succeeded, failed and unavailable. Avoid duplicating every value on every row; shared envelope fields apply unless overridden.

Replace the overloaded verified meaning with locator_valid and an explicit support assessment naming its assessor/method. A valid locator alone never promotes a model claim to established fact. Rendering a source span proves it was delivered, not that the model understood it.

Acceptance: JSON round trips preserve state; a not-requested search differs from successful empty and failed search; stale evidence cannot attach to a current answer without revalidation; unsupported model claims remain visible as unresolved work.

### B2 Stabilize requirement identity

Files: tool/gate_items.go, tool/spec.go, tool/spec_compare.go, tool/bench_objects.go and spec-format documentation/tests.

Recommended spec extension: optional indented id: on a Builds bullet. Use package plus explicit ID as its stable identity; display ordinals for convenience only. Track a separate content revision that includes requirement text and associated search/reuse declarations. Do not attempt semantic cosmetic-edit detection initially.

- Reordering explicit IDs preserves identity.
- Editing content changes revision and invalidates its previous answer.
- Splits/merges get new IDs with explicit mapping/supersedes metadata.
- Legacy package#ordinal IDs remain readable and are scoped to the exact spec/itemset. They cannot silently carry approval across versions.
- Offer a non-mutating migration preview. Ambiguous historical mappings require explicit mappings rather than fuzzy automatic acceptance.

Acceptance: reordering, editing, duplicate IDs, splits and merges behave predictably; two requirements sharing a source function remain separate; old specs still parse; old answers are not reattached by coincidence.

### B3 Render agent TOON and human JSON through a shared output boundary

Proposed file: tool/agent_output.go with encoder/selection tests. Integrate first with ctx, then dossier/questions/gate, then benchmark summaries.

- Adopt and pin a tested conforming Go encoder. Reuse JSON field names and exact values.
- Build the selected view once; feed it to TOON for agents or compact readable JSON for humans. Preserve types, empty states, errors, source ranges and omission metadata. Preserve the established --json machine contract separately from additive human-view selection.
- Put compact task/status/coverage information first. Put long evidence behind stable IDs and precise expansion instructions. Keep large text out of uniform metadata tables.
- Keep stdout structured and diagnostics on stderr. Preserve the selected output format for error responses.
- Serialize actual source strings safely. For native reads, preserve the existing tool path instead of prescribing orch ctx read to agents.
- Keep current machine consumers compatible during migration; document any versioned result-envelope changes.

Acceptance: decoded TOON equals the selected JSON object for quotes, delimiters, Unicode, multiline code, null, false, empty collections and numeric boundaries; repeated IDs remain strings; source indentation survives; truncation means the same thing in both formats; structured errors remain parseable; human questions are valid compact readable JSON and retain every option, required decision and material uncertainty; rendering makes no model requests.

### B4 Build dossiers and question packets from evidence

Files: tool/ctx_brief.go, tool/ctx_brief_rank.go, tool/gate_dossier.go, tool/gate_reuse.go, tool/gate_enrich.go and corresponding tests.

- Replace the “exact; trust these” envelope with observations, candidates and unresolved work.
- Show not-searched, failed and completed-empty item searches separately. Count successful execution after completion.
- Use one overall dossier budget and a recorded omission list. Prioritize relevant matched regions and signatures over only function starts.
- Deduplicate source bodies by evidence ID while preserving every requirement relationship. Do not deduplicate decisions by shared locations.
- Emit a compact question packet: goal, known facts/evidence, exact unknown, options and consequences, recommendation with rationale, authority and next action. Use TOON when addressed to an agent and readable compact JSON when addressed to a human. Reuse fields from the original review answer instead of paying for a question-formatting model. Use project vocabulary; do not invent repository-specific concepts.
- Expand evidence by the smallest useful native read or ctx lookup. Preserve all scope selectors in suggested commands.
- Extend adversary exploration to bounded relevant incoming and outgoing references, registrations/configuration and tests. Record coverage gaps rather than imposing a forward-import-only stopping rule.

Acceptance: zero-query and failed-query items are visibly distinct; a 41-line function does not hide its relevant region behind a shorter preview; the total budget is enforced; every omitted body has an expansion path; a shared function can support several distinct items; a non-CRM CLI receives useful questions and context.

### B5 Centralize gate policy and decision-model packets

Files: tool/gate_state.go, tool/gate.go, tool/gate_items.go, tool/gate_verdicts.go, tool/gate_shadow.go, tool/runners.go and tests.

- Implement a pure ReduceReview(subject, requiredChecks, answers, findings, policy) result using the status rules above. Separate I/O and model execution from this function.
- Validate evidence IDs, subject, item revision, required fields and authorized decisions before reduction. Interpretive support remains attributable to its reviewer.
- Bind a new-behavior search to the trusted check's `search_query`, or the Go-derived `Code that implements <requirement>` default, never a reviewer-selected query. A lookup-only producer must set that binding in Go; no production producer is wired yet. Lookup-only coverage requires complete exact zero results for every cited lookup. Exclusions are exact labels: by default only `tests`, `generated` and `vendor`, or an explicit trusted `allowed_search_exclusions` list; `everything` never qualifies. A lookup scope may contain the check scope using path components, not lexical prefixes. The legacy item-reply path targets the whole repository (`.`), so a narrower lookup cannot cover it; omitted check `search_scope` also means `.`.
- Give the main reviewer and optional decision model the same schema and evidence contract. The adjudicator receives the relevant unresolved subset, not the whole group by default. Record separate judgments and explicit finding dispositions rather than overwrite a prior one.
- Keep shadow evaluation explicitly opt-in with its own experiment budget. A targeted adjudication setting can later handle contradictory/consequential judgments with a shared run budget and separate role configuration; ordinary resolved reviews add no decider calls.
- Preserve runner choice and fallbacks. Log each attempt and actual model; fallback cannot waive required evidence or reset the overall review budget.
- Implement the disabled-by-default optional role, normalized packet and connector capability checks described above. Preserve the existing decider/triage role and explicit routing semantics. Keep native decisions-API transport separate from conversational generation.
- Preserve the two existing shared prefix classes, independent sessions and warm-up scheduling. Build stable renderings once, budget the full request and record cache usage honestly.
- Include current status, reasons, completion and next actors in TOON and JSON. Only an accepted current pass authorizes plan.

Acceptance: table-driven and permutation tests cover every status precedence and missing-answer combination; replay produces identical output; malformed replies cannot improve status; optional failures do not block otherwise complete work; required failures cannot pass; changing providers/harnesses leaves the contract and reducer unchanged; fake-runner call counts prove the resolved path has no adjudicator or formatter call; retries/fallbacks cannot reset budget accounting; production shadow and adjudication default off. Also test existing override/disabled-role precedence, unchanged triage routing, unknown evidence IDs, unsupported connector fields, user-owned decisions, conflicting judgments, explicit blocker resolution and cache-prefix stability. Cache disabled and cache-hit runs with identical recorded answers must reduce to the same status.

## Phase C Repair benchmarks before choosing defaults

### C1 Correct identity, classes and missing data

Files: tool/gate_compare_items.go, tool/gate_compare_seen.go, tool/gate_compare.go, tool/bench.go, tool/bench_report.go, tool/bench_report_subjects.go and tests.

- Preserve key-item identity independently of source location. Support multiple acceptable evidence targets per behavior.
- Match exact stable ID/revision first. Cross-version comparisons require explicit validated mappings; ambiguous/unmatched populations are reported and affected quality metrics are unavailable.
- Score explicit reuse/extend/new/unclear judgments. Do not infer full duplication from a generic reuse finding. Keep historical heuristic scores labeled as legacy.
- Distinguish candidate retrieved, author hint present, relevant span rendered and support judged. Use the rendered evidence manifest for visibility; a same-file hint does not prove the target body was shown.
- Report answer coverage alongside conditional accuracy. Missing/unsupported answers remain in coverage denominators. Avoid scoring an invalid candidate as a valid positive.
- Preserve unknown instrumentation. Recover historical total reads only from actual total-read counters; do not infer range breakdowns. No checklist means visibility unknown.
- Restrict comparative rankings to compatible evaluation populations and known cost coverage. Do not rank one-group and eleven-pass runs as directly comparable.

Acceptance: original 29-item scoped key retains both duplicate items despite shared ranges; partial reuse is not forced into duplicate; missing range counters do not become zero reads; author hints do not count as rendered bodies; ambiguous mappings suppress dependent quality claims; unverified answers affect coverage explicitly; incompatible subjects cannot produce an unlabeled winner.

### C2 Rescore without rewriting historical evidence

Files: tool/bench_migrate.go, tool/bench_verify.go, report commands and benchmark documentation/tests.

- Add scorer/profile schema versions. Preserve original runs, keys, prompts and scores.
- Produce new score artifacts with scorer version, population/mapping IDs, denominator, exclusions and warnings.
- Keep legacy unbound subjects and missing measurements as unknown. A new report must not manufacture reproducibility.
- Validate an isolated copy of the benchmark store before any explicit live rescore operation. No automatic key rewriting or import recipe execution.

Acceptance: migration/reporting is idempotent; original object hashes remain unchanged; reports distinguish old/new scores; the source store is not mutated by read-only comparisons; legacy data remains inspectable without implying current approval.

### C3 Evaluate portability and formats separately

Add small fixture repositories for Go, Python, TypeScript and mixed roots. Cover same-name symbols, stale bodies, hidden/excluded matches, absent LSPs, partial reuse, complete reuse, new behavior, future-file ownership, malformed replies, unauthorized questions and dirty integration. Use fake backends/models for deterministic tests; optional real-backend smoke tests are separate and explicitly skipped when unavailable.

Build current-subject behavioral answer keys with independent adjudication for ambiguous cases. Keep CRM among the real suites, alongside at least one non-CRM project. Do not copy CRM package names or source paths into core scoring rules.

Experiments, in order:

1. Validate repaired retrieval and reducer against deterministic fixtures.
2. Compare same evidence/ordering/budget as current hybrid, TOON and compact JSON. TOON is the chosen default direction; this test tunes representation and identifies exceptional payloads rather than reopening the preference without evidence.
3. Compare excerpts and progressive expansion separately from serialization.
4. Compare current prefix classes against a universal full-spec prefix, controlling required coverage and evidence. Measure cold and warm runs separately and include cache writes, repairs and full-run latency in cost comparisons.
5. Compare unresolved packets handled by the current reviewer, an economical conversational adjudicator and a specialized decision model. Keep the packet contents fixed; compare the complete workflows as well as the isolated adjudication responses.
6. Repeat a small matrix across at least two configured model routes and several project types before claiming model independence.

Primary outcomes: false approval, false blocker, incorrect absence, missed reuse/extension, citation/support quality, answer coverage and downstream acceptance. Secondary outcomes: total cost per correct decision, complete task latency, calls, read duplication, token counts when measured, and context/output sizes. Report variation across trials and unknown instrumentation.

Before paid experiments, record the baseline, held-out cases, trial count, maximum spend and promotion criteria in the experiment manifest. Include contradictory judgments, insufficient evidence, authority boundaries and provider failures in the held-out set. Require zero false passes in deterministic trust fixtures; for model evaluations, predeclare acceptable false-approval/false-blocker rates, minimum coverage and support quality, and cost/latency limits for the chosen sample. Report uncertainty rather than claim that a small clean sample proves safety. An aggregate score improvement cannot compensate for a failed trust criterion. Do not enable adjudication or reduce mandatory reviewer coverage until those declared criteria pass. An inconclusive trial keeps current defaults.

Acceptance: metrics have explicit denominators and comparable subjects; no claimed format win comes from different evidence; role/provider changes are recorded; fixtures and at least one real non-CRM suite can run without modifying gate policy.

## Execution and rollout

Deliver A1 through A6 first, with each fix and its tests together. Shared gate files require sequential integration. Some ctx and verification work can proceed independently after ownership is assigned; avoid concurrent edits to gate.go or shared schema files. Phase B follows, then Phase C. Do not enable new automatic adjudication based on the old quality scores.

During implementation, run the focused regression tests for each changed behavior. At phase boundaries, run go test ./... and go vet ./... from skills/orchestrate/tool. Use a targeted race run when changing parallel retrieval, shared caches or cancellation. Keep paid model experiments outside the deterministic suite and give them a separate explicit budget.

Compatibility rules:

- Existing reports and benchmark originals stay readable.
- Old approvals without a valid subject cannot authorize new work.
- CLI --json stays available. Breaking output changes use a versioned transition.
- Existing model/harness configuration remains the selection mechanism.
- No new mandatory agents. Optional gate adjudication defaults off and never changes orchestration triage by implication.
- Stable prompt prefixes and fresh independent reviewer sessions are preserved; cache availability never affects correctness.
- Current language adapters remain usable with honest degraded-capability output.
- Keep explicit user bypasses visible and auditable; never silently convert a bypass into a review pass.

Recommended first implementation slice: A1 with the reproduced empty-response, unsupported-verdict and future-file-ownership regressions. A2 follows immediately so a genuinely complete pass cannot be reused against different code.

## Deferred decisions and ideas

Recommended defaults remain proposals until accepted: the precise five-state public schema, the optional Builds id: syntax, and eventual decider promotion from shadow to targeted adjudication. The trust fixes do not require automatic decider promotion or XML.

Defer selective evidence-cache invalidation until conservative subject binding works; defer graph-ranking changes until retrieval coverage is measured correctly; defer TOON model replies until output validation is reliable; defer XML wrappers until a format-boundary experiment shows a benefit; defer OPA or a plugin framework unless actual policy/configuration requirements exceed small Go types and functions.

## References

- [AXI](../../axi/SKILL.md)
- [Current context interface](context.md)
- [Current specification format](spec-format.md)
- [Current model roles and connector behavior](model-roles.md)
- [Benchmark storage and provenance](bench.md)
- [TOON specification](https://toonformat.dev/reference/spec)
- [TOON Go implementation](https://github.com/toon-format/toon-go)
- [Agent tool design and evaluation](https://www.anthropic.com/engineering/writing-tools-for-agents)
- [Provider prompt caching and prefix matching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
