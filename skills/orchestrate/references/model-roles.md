# Model roles and connector health

Machine settings go in `~/.config/orchestrate/config.toml` (or `ORCH_CONFIG`),
never repository config. Each role tries its ordered `use` list. An omitted
`enabled` means true for an explicitly configured role. An explicitly
configured empty list has no choices; `enabled = false` also wins over command
overrides. The optional `gate_adjudicator` role is disabled when absent, with
no inherited model choices, even when a command supplies a runner override.

```toml
[roles.gate]
use = [
  { harness = "pi", model = "openrouter/~deepseek/deepseek-flash-latest" },
  { harness = "claude", model = "haiku", effort = "low" },
]

[roles.spec_queries]
use = [{ harness = "claude", model = "haiku", effort = "low" }]

[roles.shadow_flash]
use = [{ harness = "pi", model = "openrouter/~deepseek/deepseek-flash-latest" }]

[roles.decider]
enabled = true
use = [{ harness = "api", provider = "openrouter", model = "perplexity/pplx-decider-v1.2-20261016" }]
```

The Decider is an HTTP API role, not a pi profile. It checks that connector's
credential, provider health, and available credit information. It does not
use pi's model allowlist or need the pi binary when an environment credential
exists. `triage_model`, if set, overrides the role's model for legacy configs.

OpenRouter credentials come from `ORCH_TRIAGE_KEY`, `OPENROUTER_API_KEY`, or
`pi auth print-bearer-token --provider openrouter`. Other API connectors use
`<PROVIDER>_API_KEY` or pi auth. An alternative decisions-compatible connector
uses `ORCH_<PROVIDER>_DECISIONS_URL`; once its endpoint and credential exist,
only the role entry's `provider` and `model` change. OpenRouter keeps the
legacy `ORCH_TRIAGE_URL` endpoint override. A connector must implement the
same typed questions/answers contract; this is not a chat-completions adapter.

## Defaults and overrides

On a Claude-only machine, gate and spec-query roles use `claude haiku low`.
Shadow Flash is off. The Decider is off unless an API key is independently
available. With both harnesses installed, gate/spec-query roles offer pi
DeepSeek Flash followed by Claude Haiku; shadow Flash offers pi Flash.
Choices are filtered by their connector health and credential readiness.
A missing role gets its defaults even when other roles are explicitly set.

`orch gate spec.md --runner pi:openrouter/~deepseek/deepseek-flash-latest,claude:haiku:low`
sets the run's ordered list. `ORCH_GATE_MODEL` prepends that pi model when
there is no `--runner`. `ORCH_PI` and `ORCH_CLAUDE` override headless binaries.
A profile marked `degraded = true` requires user approval and `--degraded`.
`site.runModel(role, prompt, ...modelOptions)` is the tool-less conversational
entry point for generation commands. API Decider calls use the API wrapper.

## Optional gate adjudication infrastructure

`gate_adjudicator` is separate from the main `gate` reviewer and the triage
`decider`. It may use the same model without sharing a conversation. Neither
`triage_model` nor `ORCH_GATE_MODEL` changes this role. Existing runner override,
health, allowlist and approved degraded-profile rules still apply through
`newRoleRun`. Gate reviewer command overrides do not reroute adjudication.

The role stays off until explicitly configured. This example is deliberately
disabled; enabling it is a separate calibration decision:

```toml
[roles.gate_adjudicator]
enabled = false
use = [{ harness = "claude", model = "haiku", effort = "low" }]

[roles.gate_adjudicator.budget]
requests = 24
input_tokens = 256000
output_tokens = 32768
seconds = 3600
repairs = 1
fallbacks = 1
output_reserve = 4096
# Optional dollar cap requires a reliable configured per-request estimate:
# max_usd = 1.0
# estimated_request_usd = 0.05
# reliable_estimate = true
```

Without a role budget table, adjudication has a local cap of 2 requests,
128000 input tokens, 8192 output tokens, 180 seconds, 1 repair, 1 fallback and
a 4096 output allowance per invocation. A supplied role table replaces these
local limits; required positive fields cannot be omitted. This local cap lives
inside the live whole-gate `RunBudget`, not a fresh run entitlement.

The user configuration, `~/.config/orchestrate/config.toml`, also accepts an
optional `[gate_budget]` table. Repository files cannot set it. Each omitted
key keeps its default; an explicit zero disables repairs or fallbacks:

```toml
[gate_budget]
requests = 2000
input_tokens = 300000000
output_tokens = 64000000
seconds = 14400
repairs = 12
fallbacks = 24
output_reserve = 4096
# Optional, only with a reliable configured estimate:
# max_usd = 10.0
# estimated_request_usd = 0.01
# reliable_estimate = true
```

Defaults preserve the existing normal path: for each main pass, four route
invocations times its existing hard turn allowance (`ceil(1.4 * soft_turns)`)
plus two answer turns, two additional invocations per optional planner, and
two for optional adjudication. The total input allowance is that request count
times 600000 (four UTF-8 bytes per 150000-token context allowance); output is
that count times 32768. The total wall cap is 45 minutes times the number of
main passes plus 180 seconds. Repairs default to twice the number of main passes plus one, fallbacks to
three times that number plus one, and the per-invocation
output reserve to 4096. These are generous safety limits, not a guarantee that
an opaque harness uses exactly the configured turn count. Existing per-job
45-minute deadlines, stall rules, soft/hard turns, prefixes and warm-up order
remain unchanged.

First attempts, schema repairs, coverage/stall retries and provider fallbacks
admit against this same object. Pane stop/answer prompts also require admission.
Concurrent invocations reserve input/output allowance atomically. Admission
uses the complete prepared prompt, including the shared prefix and selected
dossier, not only the changing suffix. Reported usage includes cached input,
tool-loop requests and output before a reply can be accepted. Retry sessions
retain earlier usage; a fresh conversation or compaction cannot reset spend.
An opaque invocation without request telemetry costs at least one request and
leaves the request counter explicitly incomplete. Missing token/cost reports
stay unknown, never measured zero. Unknown input/output consumes its admitted
allowance as an estimate; estimated allowance and reported tokens are separate.
Token enforcement remains labeled `reported_usage_and_reserved_allowance`:
unreported provider usage cannot be claimed as an exact hard measurement.
Dollar enforcement requires the reliable configured per-request estimate;
estimated and reported cost remain separate. Request, token and wall limits do
not need a dollar estimate. `gate.json` and v2 status expose the same spent,
remaining and unknown budget state.

An unresolved batch gets one initial invocation and at most one schema repair,
from that same run budget. Provider errors and valid uncertainty are not
resubmitted until another model agrees. A later explicitly requested batch or
route switch retains the counters and charges a fallback when its route
changes. Exhaustion records an incomplete answer with `budget_exhausted` and
an owner/reviewer next action. Only the unfinished required checks become
incomplete; completed groups are retained and are not restarted. Raise the
user-level budget, resume the named review, or ask its recorded owner.

Only unanswered technical questions with recorded delegation to
`gate_adjudicator`, an allowed assessor, and targeted evidence retrieval are
eligible. No source text or model reply can grant that authority. The current
legacy consequential-question adapter grants no delegation, so simply enabling
the role does not authorize user-owned decisions. Future typed producers must
record the check owner, delegates, assessor and question basis. Fully resolved,
disabled and user-owned paths make no packet-formatting, adjudication or shadow
call. The existing reviewer or authorized owner remains responsible otherwise.

Packets carry the unresolved check only, exact source excerpts with their
recorded hashes, lookup coverage/omissions, earlier claims and allowed actions.
Related unresolved items in one package share a batch; completed items are
excluded, and separate package batches consume the same run budget.
Paths without excerpts, stale hashes and unsupported exact column selections
fail closed. Chat runners receive TOON through the shared output boundary,
with stable instructions before changing evidence and repairs. Decisions API
runners retain native object state/questions and use the existing decisions
endpoint, never chat completions. The current finite-choice endpoint can select
a complete recorded behavioral proposal, retaining its explanation, evidence
and matching/missing behavior. It can also decline or defer. If no complete
proposal exists, that connector cannot supply the required support fields and
records incomplete without a call; it never invents an explanation or embeds
JSON chat inside a typed choice. An eligible later configured conversational
route may be selected before any model call.

Both adapters normalize to `reviewAnswer`, `reviewQuestion` and
`reviewResolution`. Subject and attempt identity come from the trusted request.
Earlier judgments remain recorded; a later answer never wins by arrival order.
Material conflicts stay incomplete and an accepted blocker requires an explicit
evidence-linked resolution, including a finding-to-resolution link. The pure
reducer is unchanged. `adjudication.json` beside the gate run records packets,
raw replies, execution/usage, normalized results and the shared budget.
Native decisions aggregate usage and served model are retained separately when
reported; aggregate tokens are not guessed into an input/output split. The
explicit `recordAdjudicationShadow` experiment hook owns a separate budget and
records later outcomes without applying any reply to production. It is not
called by ordinary runs, and adds no default shadow traffic. Cache availability
changes neither authority nor reduction of identical recorded answers.

## Connector lanes

- `claude`: Claude subscription.
- `pi:<provider>`: first segment of a pi model id, for example `pi:openai`,
  `pi:anthropic`, or `pi:openrouter`.
- `pi:openrouter:anthropic`: OpenRouter model ids containing `anthropic/`.
- `pi`: legacy parent covering every pi provider and the API Decider's lane.
- `pi-anthropic`: legacy alias of direct `pi:anthropic`, not OpenRouter Anthropic.

A lane is limited if its own entry or a parent entry is limited. An OpenRouter
limit does not disable direct OpenAI/Anthropic connectors or Claude. A `pi`
parent limit does disable all pi connectors and the OpenRouter API Decider.
Unqualified legacy pi models retain lane `pi` because they do not identify a
provider. `orch health` lists granular lanes, including saved provider lanes.

```sh
orch health set pi:openrouter limited --for 3h
orch health set pi:openrouter:anthropic limited --for 3h
orch health set pi:anthropic ok
```

`allowed_models` remains keyed by conversational harness `claude`/`pi`.
`allowed_when_limited` keeps those harness keys and adds optional `lane` for
a connector-specific condition. Without `lane`, the condition uses the legacy
harness parent.

```toml
[allowed_models]
pi = ["openrouter/~deepseek/deepseek-flash-latest"]
claude = ["sonnet"]

[[allowed_when_limited]]
harness = "pi"
lane = "pi:openrouter"
allow = { claude = ["haiku"] }
```

## Doctor and keys

Doctor prints per-role choices, binaries, and per-provider credential state.
OpenRouter uses a free read-only `GET /api/v1/key`. A returned, nonempty
Anthropic-only `allowed_models` or `allowed_providers` restriction scopes an
empty key to `pi:openrouter:anthropic`. If the endpoint provides no such
proof, doctor says `unknown scope` and attributes empty credit to
`pi:openrouter`. Labels and configured model names are not scope evidence.
The actual inspected endpoint omitted model/provider restriction fields,
returned `budgets: []` and `effective_budget: null`, and could not prove a scope.

Null `limit_remaining` means no reported cap, not zero. A failed HTTP probe
is reported as a diagnostic, not evidence of exhausted credit. OpenAI and
Anthropic only check credential presence through pi's read-only
`auth check --json --no-refresh` or provider key variables. No secrets are
printed. No readiness or fallback path writes `health.json` automatically.

## Failure and triage behavior

Provider failures (credit, usage, rate limit, auth, missing binary, server
errors) exclude the affected connector lane for that role's run. A pass gets
one retry on the next usable connector. The exact manual hint is printed:
`orch health set <lane> limited --for 3h`. Timeouts/stalls are not provider-down
and keep the existing pass-limit retry behavior.

With no usable gate runner, deterministic checks, dossiers, items and the gate
record are still written; a normal gate run has state `incomplete` when its
subject and required system records are valid. `pass_runners`, the
report, and compare output show actual pass runners.

When the Decider is off, `orch spawn <id> --auto` prints one guidance message,
starts no lead, and invents no tier. Supply `--auto --tier settled|standard|planning`
to select a dispatch rule without triage. The first allowed, non-limited,
approved profile wins. `--apply` still controls launch. A hand-chosen
`orch spawn <id> --harness H --model M [--effort E]` also bypasses triage;
effort defaults to the task's effort, or high for an unresolved auto task.

Quick shadow mode records `skipped: decider off`, leaves the tier empty, and
proceeds. Quick enforce mode fails closed and gives the manual-profile/tier
message. Triage shadow also records unavailable skips rather than fake ratings.

## Claude execution boundary

Headless Claude uses `claude -p`, Read/Grep/Glob, non-interactive `dontAsk`
permissions and disabled slash commands. The existing strict empty server
configuration excludes ambient servers; the gate runs no ctx server and
loads no generated adapter. Tool-less tasks still have no tools. Inside
Herdr the gate uses `herdr agent start --kind claude` and a dedicated
session/transcript.

`--ctx-tool on|off` defaults to on. `CtxCLIFor` supplies a PATH shim,
`ORCH_CTX_ROOT`, harness-specific tools and a notice placed first in the
shared prefix. Agents call `orch ctx find|sym|text|batch` through the shell.
The notice bytes are shared across passes of a class; dossiers remain in the
per-pass task. Pi adds `bash`; Claude exposes Bash with the ctx command
permission pattern supplied by `CtxCLIFor`. Task A's installed-harness probe
also allowed `cat file` as a safe read, so the pattern is not an exclusive
shell-command allowlist. Its stricter requested cat refusal remains an
integration limitation for the parent to resolve. Off keeps the original read/search
tools without adding the notice or ctx environment. Tool-less planner and
shadow passes stay tool-less even with ctx enabled.

`--context-files on|off` defaults to off. Pi gets `--no-context-files` when
off, and omits it when on. Claude gets
`--settings '{"claudeMdExcludes":["**"]}'` when off, and omits it when on.
This controls CLAUDE.md discovery without `--bare`, whose documented auth
restriction would break subscription users. It is the nearest Claude
equivalent, not a promise that every harness loads AGENTS.md identically.
Gate/experiment records, reports and compare identify both switch values.

Before any model pass, the gate fingerprints Git status (NUL-separated,
including every non-ignored untracked file), the diff against HEAD, HEAD
identity, and content hashes of dirty/untracked paths. After every runner
attempt, including planner/coverage retries and shadow Flash, it compares
against the same baseline. A changed checkout discards model findings,
questions and item verdicts, cancels remaining passes, sets state to `stale`,
and warns in stdout/report. An unidentifiable subject or corrupt required
system data takes precedence as `error`. `tree_changed` lists paths;
`tree_changed_passes` lists passes in flight, not a proven writer. Failure to
compute the check also fails closed. No files are automatically reverted.
A checkout already dirty before the baseline is allowed if unchanged.

The check does not cover ignored files, paths outside the checkout, or edits
reverted before a check. Keep gate state artifacts outside the code checkout
or ignored, since the scan deliberately does not exempt non-ignored orch
artifacts. Pi has no shell command allowlist: it can read outside the repo,
write ignored/outside files, or use the network. Its protections are the
read-only prompt, pinned read-only orch ctx commands, and the post-pass
check. These do not constitute a sandbox.

Fake tests cover argv, environment exports, prompt/cache bytes, context
switches, transcripts and mutations under both harnesses. Opt-in installed
Claude tests use isolated loopback mocks and dummy credentials:

```sh
ORCH_TEST_RUNNER_CLAUDE=1 go test -v -run 'TestInstalledClaude(RefusesMutatingTools|ContextFilesToggle)'
```

The original mutation test verifies tools-off Bash/Edit/Write refusal. The
new test measures whether a CLAUDE.md sentinel reaches the mock model for
each context-files setting. CLI-only command permission verification belongs
to the ctx CLI implementation's installed-harness tests. No paid traffic is
needed for these experiments.
