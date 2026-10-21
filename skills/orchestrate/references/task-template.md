---
id: @@ID@@
kind: @@KIND@@
repo: @@REPO@@
base: @@BASE@@
harness: @@HARNESS@@
model: @@MODEL@@
effort: @@EFFORT@@
depends_on: [@@DEPENDS@@]
---

# Intent

{{Why this task exists and what the user wants out of it, in their words where possible. One short paragraph. The lead re-reads this when the spec runs out.}}

# Scope

In: {{what this lead owns: features, files or directories, branches}}
Out: {{what it must not touch, and which other task owns it if any}}

# Pointers

{{Where to start reading, one per line: `path/file.ext:line` - why it matters; docs, ADRs, issues, earlier task reports. Pointers, not pasted content. To find more, search with `orch ctx` (find, sym, text, batch) before reading files.}}

# Spec

{{What to build or find out. For ship: behaviour, decisions already made, constraints, edge cases. For scout: the questions to answer and what a good answer looks like.}}

# Acceptance

{{How the lead proves it is done: commands to run and their expected result, tests that must exist, or for scout the evidence each finding needs. For Go tests in orch write `orch test-changed`, not a `-run` pattern; never ask for the full suite.}}

# Authority

May decide: {{technical choices the lead settles alone}}
Must escalate: {{choices that go back as needs-decision: product behaviour, compatibility, scope changes, anything irreversible}}
Never: {{hard limits: push, merge to the base branch, touch secrets, edit outside scope}}

# Report-back

@@REPORT_BACK@@
