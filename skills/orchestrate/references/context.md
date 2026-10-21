# Code facts with `orch ctx`

`orch ctx` answers questions about a repository from tools, with no model and
no run state. It works in any git repo, subject to backend availability and
trust. Use one `orch ctx batch` for independent lookups, then inspect source
with native Read/read. Use a single lookup when only one question is ready.
There is no blanket speed advantage over native tools. Never re-read what you
already read.
Agents read files with their native read tool (Pi `read`, Claude `Read`), using
offset and limit for part of a file. orch ctx is for finding things, not for
reading files; agents never use sed, cat, head or tail, the ctx read command,
or the batch read kind.
Here `orch` means `~/.agents/skills/orchestrate/scripts/orch`, called from the
repository root. When files are unknown, start with `ctx find`: it wraps
jbcontext, so jbcontext remains the first discovery step.
`orch ctx` alone shows the live state (index against HEAD, tools ready) and
the commands; `orch ctx <subcommand> --help` shows flags and examples.
`orch version`, `orch --version`, `-v` and `-V` print the bare build fingerprint
without repo/config/backend initialization. Direct Go builds print `devel`.
The launcher uses Go and `shasum` to check source, embedded-asset and toolchain
content on each invocation; unchanged content reuses its worktree-scoped binary.

## Pick the cheapest lookup

Start with the cheapest tool that can answer, and move up only when it cannot:

1. `text` when you know a string, a name, a route, a message: regex or literal
   matches in the reported scope, with status and exclusions. Narrow it with
   `--glob`,
   `--word`, `--type` before you widen it.
2. `sym` when you know the name of a function, type or method and want its
   definition, callers and implementers.
3. `find` (concept) only when you do not know any name to search for.
4. `batch` whenever you have two or more independent lookups.

When the tool guard blocks a raw grep or rg, one line of its reason is built
from the pattern you typed (`ORCH_TOOL_GUARD_HINT=pattern`; without it the
reason stays generic). One identifier such as `Normalize` or `Parser.Parse`
names `orch ctx sym`, a string, route or path such as `cannot find user` or
`/api/users` names `orch ctx text`, and a broad pattern names `orch ctx find`:
an alternation of three or more, a `.*` gap between words, four or more words,
a question, or a regex character class. Take the kind it names rather than
widening the same grep: `find` is for the case where no single name or string
is known yet, and `text` answers faster when one is.

The guard names `orch ctx` only when `orch --version` answers (exit 0 within
three seconds). When `orch` is on PATH and fails that probe, for example a
wrapper that cannot find the `tool` directory next to it, the guard treats it
as not installed: it names `fd`, `rg` or the built-in tools instead, does not
block a plain `rg`, and ends each search block with
`orch ctx is installed but broken here (<first line of the probe error>); use
the tools above and tell the user.` Pi probes once at startup; each Claude hook
probes at most once a minute through
`~/.local/state/orchestrate/toolguard-health.json`, which
`ORCH_TOOL_GUARD_STATE_DIR` moves. If you see that line, do not look for another
search tool: say the tool is broken and use the replacement the block names.

| You need | Command | Why this one |
|---|---|---|
| String matches within a stated scope | `orch ctx text <pattern> [--glob G] [--word] [--fixed] [--type T] [-l] [-c] [-C n]` | ripgrep: smart-case, skips lockfiles, minified, generated and vendored files; hits grouped by file, each with its enclosing function and configurable context (default 6 lines) |
| Exact or candidate-labelled definitions and available references | `orch ctx sym <Name\|pkg.Name\|Type.Method> [--refs N] [--impl=false] [--kind K] [--in path]` | gopls (Go) or the TypeScript server: doc comment, body preview, callers with their enclosing function, implementers, where it is wired |
| Candidates for a concept ("how are refunds issued") | `orch ctx find "<concept>" [-p path] [--limit N]` | Semantic search (jbcontext): bounded current-checkout excerpts, declarations and package size; not evidence of absence |
| Several of the above at once | `orch ctx batch <plan.json\|-> [-q "kind:query key=value"]` | One call, run in parallel, answered in plan order, a place shown once |
| A starting picture for a piece of work | `orch ctx brief <file\|-> [--owns a,b,c]` | Ranked files, interface sheet and look-alikes outside the owns; not an exhaustive inventory |
| Judging what code does | native `read` / `Read`, with offset and limit | Read the files you located; never use a shell command or ctx read for this |

## Output format

One format for every command. TOON (`name[n]{cols}:` plus rows) is used only for
short uniform tables: status, counts per file, the common files of a batch,
`help[n]:` hints. Hits are plain text because code is full of the delimiters a
table would have to quote. `--json` prints the data, the notes and the sources
for scripts.

```
text: 3 of 3 matches for "createGatewaySchema" in 1 file (glob web/apps/crm/src/**)
web/apps/crm/src/features/admin/sales/gateways.tsx
  60- /** What the connect form checks ... */
  61: export const createGatewaySchema = z
  97: resolver: zodResolver(createGatewaySchema),  [in function CreateGatewayForm]
sources: rg /opt/homebrew/bin/rg
```

- The first line counts the result ("3 of 3"). A path is printed once, then one
  row per line: `N:` a match, `N-` a context line, `[in func X]` the
  declaration that holds the match (read from the file, column-0 declarations;
  shown when it changes). Overlapping and adjacent ranges of a file are one hit.
- An empty answer is a sentence, not a blank: `text: 0 matches found in the
  whole repo`. Negative text results also state `searched_scope` and `excluded`.
  This is no match within that scope, not proof that behavior is absent.
- Anything left out is named with the flag that shows it, see Pagination.
- Test files are hidden by default and counted: `(+18 in tests;
  tests=include|only)`. `--tests only` lists only test files, `--tests include`
  both. One matcher everywhere: `_test.go`, `*.test.*`, `*.spec.*`,
  `__tests__`, `testdata`, `fixtures`. Works on `find`, `sym`, `text` and, as
  `tests`, on every lookup of a batch.
- `--compact` is the shortest answer (counts, signatures, first lines, no
  context or caller lists). `--full` raises finite display caps, not backend
  limits: default 1000 text matches/page, 600 columns and about 48000 characters
  per single answer or batch lookup, 280000 per batch. `--fields src` tags each
  file with the tool that found it.
- Each lookup has `status`: `ok` (usable result), `empty` (completed with no
  visible results), `partial` (usable data plus errors/incompleteness), or
  `failed` (no usable result). `count_kind` is `exact` (count completed within
  the stated scope), `lower_bound` (at least the reported number), or `unknown`
  (no authoritative total). An exact count need not mean all excerpts fit.
  Semantic candidates always have unknown counts and never establish absence.
- Errors are on stdout, structured under `--json`, with recovery hints. Usage
  errors exit 2. A failed single lookup exits 1; a batch where all lookups fail
  exits 1. A partial batch exits 0 with aggregate `status: partial` and retains
  successful results and each lookup's errors. A partial single lookup exits 0.
  Read status, count kind, notes, errors and cuts, not only the exit code.

### Pagination

`find`, `text` and `sym` (callers and implementers) take `--limit N` (page size)
and `--page P` (1-based); in a batch they are the per-lookup keys `limit` and
`page` (`-q "text:foo limit=6 page=2"`). `--max` aliases `--limit` for find/text;
`max` is also accepted for symbol batch lookups.
Defaults: find 4, text 5 hits, callers 10 (`--refs N` still sets the callers
page size), implementers 10. Pages are small on purpose: each hit is shown with
more code around it, and the footer names what the page left out. `--limit` or `--page` below 1 is a usage error
(exit 2). Explicit `limit`, `max` and `page` values above 2000 are also
usage errors.

Paging is stateless: page P is items `[(P-1)*N, P*N)` of a list sorted the same
way every time (text: path then line; callers and implementers: file then line;
find: jbcontext rank), so a page never depends on an earlier one. Semantic
pages filter visible candidates before slicing, but retrieval is bounded to a
2000 raw-rank window. Hidden or rejected candidates can consume that window;
use a narrower `-p path` or native checkout reads instead of treating a short
page as an exhaustive search.

Every list with more says where it stands and which files it left out:
`showing 5-8 of 40; next: --page 3; left out: a.go (9), b.go (7), +4 more files`
(up to 5 files with their hit counts; for `find` the 8 ranked hits past the page
are looked up to name them). Inside a batch the hints use lookup keys:
`next: page=3`, `narrow with glob=/word=/tests=/path=` (find: `path=/tests=`, sym:
`in=/def_kind=/tests=`). Above 50 items narrowing comes first: `showing 1-5 of 80;
75 more; narrow with --glob/--word/--tests/-p, or --page 2`; after page 3 only
narrowing is suggested. A page after the end says `page 9 is past the end (40 hits, 8
pages)`. Check `count_kind` before using a total: failures, candidates and
unavailable references do not have exact counts. Callers retain bounded lists
(up to 50 per definition in the existing reference tiers), and symbol display
shows at most four definitions after path/kind filters. Narrow `--in` and
`--kind` for ambiguous symbols. `--full` raises page size (1000 for text, every
kept caller for sym), not the retained backend data.

Text retains at most 2000 visible excerpts while counting independently. A
completed count can still be exact beyond retention; an interrupted count can
be lower-bound or unknown. Narrow `--glob`/`-p` (batch `glob`/`path`), or inspect
the checkout with native Read/read. `--full` does not remove this retention cap.
Backend safety guards also remain: 16 MiB semantic JSON or LSP frames, 8 MiB
per rg record and 64 KiB diagnostics. A cut or failed backend requires a scoped
retry or native inspection, not an inference of absence.

### Text (`orch ctx text`)

Options: `--fixed/-F` literal, `--word/-w`, `--ignore-case/-i` (default is
smart-case), `--glob` (repeatable; `!` excludes), `--type/-t`, `-l` files only,
`-c` counts per file, `-C n` context lines (default 6; `-C 0` none),
`--multiline/-U`, `--only-matching/-o`, `--per-file N`, `--limit N` hits per
page (default 5; `--max` is the same), `--page P`, `--all` also lockfiles, generated, minified and vendored
files, but still honors gitignore and hidden-file rules. `--all` does not
include tests unless `--tests include|only` is also set. Leading whitespace is
trimmed and a line is cut at 300 characters (600 with `--full`).
Lines that read the same in several files print once:
`same line in 5 files: import x (a.ts:1, b.ts:2, c.ts:3, +2 more)`.

```
orch ctx text polar --glob 'internal/**' --word
```

### Symbols (`orch ctx sym`)

One block per definition: a header with the name, kind and place, the doc
comment, a body preview sized by kind, and reference information. Symbol facts
distinguish exact identity from candidates. Candidate headings explicitly say
`candidate (identity not verified)`; the JSON view retains the same heading in
`parts`. Backend-verified definitions use exact identity; declaration scans,
ambiguous bare methods and weaker name refinements remain candidates. Missing servers or failed references produce partial/failed results
with location-qualified errors, not zero callers. By-name matches are observed
name occurrences, not a call graph or proof of absence. Paths are printed once:
callers in the definition's file need none,
callers in other files sit under their path.

```
createGatewaySchema  const  web/apps/crm/src/features/admin/sales/gateways.tsx:61-77
What the connect form checks before the request; the API tests the key and has the last word.
  export const createGatewaySchema = z
    .object({
    ...
  (+8 lines; --full)
used by 2, none in tests:
  85  type CreateGatewayValues = z.infer<typeof createGatewaySchema>
  97  resolver: zodResolver(createGatewaySchema),  in CreateGatewayForm
```

- Body by kind: a function or method is its signature plus about 8 lines; a
  type, interface or struct is the full field or method list (a Go struct also
  gets a `methods:` line); a const or var is the whole declaration up to about
  15 lines. A cut says how many lines and `--full`.
- `used by 12 in 3 files, +5 in tests:` lists callers outside tests (up to
  `--refs`, default 10; `(+N more; --refs N or --full)` says what is left), each
  with its line and enclosing function. `-C n` adds n lines above each caller.
  `--refs 0` prints only the line with the counts. Test references stay a count;
  `--tests include|only` lists them under `tests:`.
- `implemented by N:` lists implementers of an interface method (`--impl=false`
  hides it). `wired:` follows the first caller one level up by name with rg (a
  pointer, not a call graph). Both are printed once even when several
  definitions share them.
- `--kind Method` and `--in path` filter definitions. `--compact` is the header,
  first doc line, signature and the counts line.

```
orch ctx sym StartCheckout --refs 3
```

### Concepts (`orch ctx find`)

Each chunk uses current-checkout code (up to 80 lines, 3 in compact mode),
merged per file, followed by
`declares ...` (the declarations in it) and the size of its package.

```
orch ctx find "webhook signature check" -p internal/ --tests include
```

### Batch (`orch ctx batch`)

An agent plan is JSON, `{"lookups":[{"kind":"concept|symbol|text","query":"...","why":"..."}]}`
(from a file or `-`), or inline, repeatable, with options as `key=value`:

```
orch ctx batch -q "symbol:StartCheckout refs=3" -q "text:polar tests=none glob=internal/**"
```

Options are validated per lookup kind, including explicitly set false/zero
values. Find: `path`, `tests`, `generated`, `max`, `limit`, `page`, `why`;
text: `fixed`, `word`, `ignore_case`, `glob` (repeatable), `type`, `files_only`,
`count`, `context`, `multiline`, `only_matching`, `per_file`, `all`, `path`,
`tests`, `generated`, `max`, `limit`, `page`, `why`; symbol: `refs`, `impl`,
`context`, `def_kind`, `in`, `tests`, `generated`, `max`, `limit`, `page`, `why`.
`orch ctx batch --help` lists the accepted keys. The script-only read kind is
described below. `why` is a label echoed in the lookup's header and is not part of any cache key. Quote a
query word that contains `=`. At most 50 lookups.

The batch removes redundancy across lookups:

- `common[n]{path,sources,lookups}:` lists a file found by several kinds of
  lookup once, with its sources (`jb+rg+sym`) and lookup numbers; it comes first.
- A line already printed by an earlier lookup is not printed again:
  `path also #2: 58-66`.
- One `notes[]` block and one `sources:` line per batch.
- Each answer is held to about 9000 characters and the batch to 70000; what was
  cut is said: `cut at about 9000 characters: 41 more lines (2 more files) not
  shown; --full raises the bounded cap, or narrow the lookup`, or `#6 ...: not
  shown, the batch budget of 70000 characters is spent`. Add `limit=N` or scope
  broad lookups; run a cut lookup alone when needed. `--full` raises per-lookup
  and total budgets to about 48000 and 280000 characters, not unlimited output.

### How much of a definition `sym` shows

A type is shown whole up to 120 lines, a function its signature and first 40
lines. A const or var reads in context: when it sits within 30 lines below the
start of its enclosing function it shows that function's first 30 lines;
otherwise 30 lines around it; the whole declaration is always inside, up to 60
lines. A `find` hit is shown with 6 lines of the checkout above and below it.

Put every independent lookup in one batch instead of one turn each.

### Script-only file reads

The `orch ctx read <path> --from N --to N` command and batch `read` kind remain
for scripts and the planner; agents use their native read tool instead. The
batch read options are `path`, `from`, and `to` (at most 60 lines). For scripts:

```sh
orch ctx read pay/pay.go --from 1 --to 40
orch ctx batch -q "read:pay/pay.go from=1 to=40"
```

These are script examples, not agent instructions.

## Languages and symbol tiers

`orch ctx` is not tied to one language or repo layout. Languages come from a
table (`tool/ctx_lang.go`): extensions, declaration forms, doc comment markers,
test and generated file patterns, entry-point names, the export rule and the
language servers to try. Built in: Go, TypeScript/JavaScript, Python, Rust,
Java, Kotlin, C/C++, Ruby, PHP and shell.

Project roots come from manifests, never directory names: `go.mod`/`go.work`,
`package.json`/`tsconfig.json`/`pnpm-workspace.yaml`/`deno.json`, `pyproject.toml`
and friends, `Cargo.toml`, `pom.xml`/`build.gradle(.kts)`, `Gemfile`,
`composer.json`, `CMakeLists.txt`. Nested roots are separate, and a language
server gets one workspace per root. A parent is a single root only when it
declares its children (`go.work`, `package.json` workspaces or
`pnpm-workspace.yaml`, `[tool.uv.workspace]`, Cargo `[workspace]`, Gradle
`include`, Maven `<modules>`). With no manifest, the top directories with the
most code files stand in. Text and concept search always run at the git root.

A symbol is answered by the best tier available, and `sources:` names it:

1. A language server: gopls (Go), the native TypeScript 7 server, Pyrefly for
   Python (`pyrefly lsp --indexing-mode lazy-blocking`), rust-analyzer, clangd,
   jdtls. Definitions come from `workspace/symbol` when the server advertises
   it, else from the declaration scan; references and implementations come from
   the server.
2. A declaration scan with rg: a name is a symbol only if some declaration form
   of a language matches it (`def X`, `class X`, `func X`, `fn X`, ...). Callers
   are word matches outside the definition, printed `(by name)`: a pointer, not
   a call graph. A bare word that no declaration matches is not a symbol.
3. Plain text (`orch ctx text`), as an explicit scoped fallback. A missing
   language server does not become a successful empty symbol search: useful
   declaration/name candidates can survive with partial status and errors;
   without usable data the symbol lookup fails. Install the external server
   named in the hint, narrow the lookup, or inspect source with native Read/read.

Python servers are tried in order: Pyrefly, basedpyright, pyright, `ty server`,
pylsp, then the declaration scan. Override with `ORCH_PYLS` (a command line).
Discovery order: the env override, the project's `.venv`/`venv` in normal mode
(trusted repos only), then PATH. Every resolved server must pass
the language-server trust policy below. Install
Pyrefly with `uv tool install pyrefly`, `pip install pyrefly` or run it with
`uvx pyrefly`. `orch ctx status` and `orch doctor` list the detected languages
and, for each, the server found or the install hint; a missing server never
fails them.

Add or change a language in the user config with `[[ctx_langs]]`; a known `id`
overrides only the fields set:

```toml
[[ctx_langs]]
id = "zig"
extensions = [".zig"]
decl = ['^\s*(?:pub\s+)?fn\s+{sym}\b']   # {sym} is the escaped name
test = ['_test\.zig$']
lsp_bin = "zls"
lsp_hint = "install zls"
```

Other keys: `doc` (comment markers), `generated`, `entry`, `export`, `lsp_args`,
`lsp_fallbacks` (`"command args"` strings), `manifests`.

## Writing a concept query

When you do not know where code lives, search one behavior at a time in plain
language: `Code that <action> on <entity>, when <trigger>, producing <result>`.
Include only what you know. Do not invent symbols, frameworks or paths. Read the
best hits, adopt the repo's own names, then use `sym` / `text` for exact lookups.

Drop any slot that does not apply; do not force the whole template.
- Good (full): `Code that saves a payment method for a customer, when a checkout completes, producing a stored method row`.
- Good (short): `Code that retries failed payments`.
- Bad (noun list): `payment checkout method customer store`.

`orch ctx find --help`, the home view, the plan-arm planner prompt and the gate's
per-item checklist queries (`Code that implements <item>`) use the same form.
A short noun-list query, or an answer with at most one hit, gets one line:
`tip: describe the behavior: "Code that <action> <entity> ..." (one behavior per
query)`. It is shown once per batch and never for a query that starts with `Code
that` or has six or more words. Queries you type are never rewritten.

## Generated files and ignored files

A file is generated when its path matches the language table or the universal
list (`.generated.`, `/generated/`, `/gen/`, `openapi`, `swagger`, `/vendor/`,
`/node_modules/`, `/dist/`, `.min.`), or one of its first five lines has a marker
(`Code generated ... DO NOT EDIT`, `auto-generated`, `autogenerated`,
`@generated`, `This file was generated`, `DO NOT EDIT`), or `.gitattributes` sets
`linguist-generated`. One function decides this for `find`, `text`, `batch`, `sym`
callers, the dossier's look-alikes and the checklist candidates; header checks
read only the first lines and are cached per run.

Generated files are hidden and counted like tests: `(+3 in generated; --generated
include|only)`; the flag is `--generated` on `find`, `text` and `sym` and
`generated=` in a batch lookup; `text --all` includes them. Totals and pages
exclude what is hidden. A symbol's definition may live in a generated file (sqlc
store methods): it is shown, labelled `generated`, because it is the real
definition; generated files are still not listed as callers, look-alikes or reuse
candidates.

Ignored files: no `orch ctx` path passes `-u`, `--no-ignore` or `--hidden` to
rg, so `.gitignore` is honoured; the file lists come from `git ls-files --cached
--others --exclude-standard` (tracked files plus untracked ones git does not
ignore). jbcontext indexes a git revision (`index --revision`, default HEAD), so an ignored untracked file is not in the index; its `watch` overlay of uncommitted changes is not documented to skip ignored files, and I did not run it on a fixture. Every `find` hit is therefore checked with `git check-ignore` and an ignored path is dropped.

## When not to use it

- To understand a function's behaviour, to decide anything, or to check a claim
  that depends on a condition the tools cannot see: read the code.
- `find` returns ranked semantic candidates, never proof of absence. A missing
  hit can reflect ranking, filtering, a cap or stale/missing index data.
- A successful empty `text` result means no pattern match in the stated scope
  and exclusions. Check status and count kind, then broaden scope or inspect
  source if the claim needs more evidence. Symbols can be candidates; missing
  servers and reference failures are not evidence of zero callers.
- No retrieval result alone establishes that a behavior does not exist.

## Staleness

Every answer ends with a `sources:` line naming what answered: the jbcontext
index revision against HEAD, the gopls or TypeScript server used, rg. A note
`jbcontext index is at <sha7>, working tree is at <sha7>[ with uncommitted
changes]: hits may be stale` means `find` may miss new code. Displayed semantic
spans are validated or uniquely relocated against the checkout; ambiguous or
invalid spans are rejected with partial/error status. Text reads the checkout;
symbol coverage still depends on server readiness and the selected tier.
Results are cached per HEAD and working-tree fingerprint under
`~/.cache/orchestrate/ctx/`; incomplete/error results are not cached as
successful search facts. This is not a guarantee of exhaustive server coverage.
`orch ctx status` shows the index, the watch daemon and
the tools; `orch ctx refresh` makes the index current. `orch ctx watch add`
keeps an overlay for uncommitted changes (the daemon is never started by orch).

## Read-only tools for agents

Pin retrieval with an absolute git root:

```sh
ORCH_CTX_ROOT=/absolute/repo ORCH_CTX_READONLY=1 orch ctx find "Code that retries failed payments"
ORCH_CTX_ROOT=/absolute/repo orch ctx --readonly sym StartCheckout
```

`ORCH_CTX_ROOT` itself implies read-only. The flag and `ORCH_CTX_READONLY=1`
require it. The caller's working directory cannot change the root. Agent
lookups are `find`, `sym`, `text`, `refs` (an alias of `sym`) and `batch`.
The CLI also permits `read` for scripts; agents use their native read tool.
`refresh`, `status`, `watch` and `brief` are refused. Index builds
and refreshes are disabled, including automatic ones on `find`. An existing
stale index can still answer, with a staleness note. Traversal and symlinks
outside the root cannot be read, and a batch-plan file must be inside the root.
Result caches and CLI shims stay under the ctx cache.

Agents use the plain CLI through the harness shell tool. No server, schema or
adapter files are generated. `CtxCLIFor(harness, code)` returns:

```go
type CtxCLI struct {
    Env    []string
    Tools  []string
    Notice string
    Extension string // pi guard path
    Ready string     // gate invocation's activation acknowledgement
}
```

Append `Env` to the agent process environment, add `Tools` to its allowlist,
and put `Notice` at the very top of the shared read-only prompt. `Env` pins
`ORCH_CTX_ROOT`, sets `ORCH_CTX_READONLY=1`, and prepends a cache directory to
PATH. Its `orch` symlink targets the current binary's path (`os.Executable()`),
not its inode: an atomic rebuild at that path still works without regenerating
the shim. Shim creation is idempotent; a conflicting entry is an error.

Keep Claude's native `Read,Grep,Glob` in both the tool and allowed-tool lists,
add `Bash` to the tool list, and add `Bash(orch ctx:*)` to `--allowedTools` with
`dontAsk` permission handling. Pi defaults to `--ctx-shell ctx-only` with ctx on:
only native `read` and guarded bash. It starts with
`--no-builtin-tools --tools +read`: read is active, while bash stays registered
but inactive. After its command guard is installed, the extension enables bash
and acknowledges startup. A hard `--tools read` allowlist would exclude bash
from the registry and prevent activation.
A missing acknowledgement or extension-write error fails the pass, without an
open-shell fallback. `--ctx-shell open` keeps Pi's native `read,grep,find,ls`
and appends unrestricted `bash`. Claude ignores `--ctx-shell`; ctx off keeps the
original native tools. `CtxCLI.Tools` contains only the extra shell entry.
Agents use native Read/read with offset and limit for files; never the ctx read
command, batch read kind, sed, cat, head or tail.

Installed Claude 2.1.303 refused the chaining, redirection, write and
substitution probes, but **also allowed `cat file`** as a safe read command.
This pattern is not an exclusive shell-command allowlist. No deny rules are
added to hide that behavior. In the outside-repo dummy-file probe, Claude
refused shell cat but permitted native Read with offset=1 and limit=1 on that
same file. Thus a Claude pass can read outside the checkout through native
Read, even where its safe-shell check refuses cat. This does not prove that
every outside path or every shell read is allowed or refused. `ORCH_CTX_ROOT`
constrains orch, not native Read; it is not a file-confidentiality boundary.

Pi's default ctx-only shell accepts only one `orch ctx find|sym|text|batch|refs`
call, without pipes, redirects or unrelated commands. `--ctx-shell open` opts
out and permits arbitrary shell programs, including writes and network access.
Native read is not root-confined in either mode. The prompt and post-pass
unchanged check remain separate layers, not an operating-system sandbox;
ignored files and outside paths are not covered by that check. Gate records,
reports and compare show `ctx_shell`; missing historical fields display `open`.

`skills/orchestrate/tool/pi_ctx_reminder.ts` is a user-level pi extension (the
orchestrator installs it under `~/.pi/agent/extensions/`). It carries five
small deny rules, each a table entry with a name, a matcher and a reason:
`deny-find-chains` (find `-exec`/`-delete`, and a search tool feeding a grep),
`deny-find` (a plain `find` over a tree), `deny-sed-inplace` (`sed -i`,
`perl -pi`), `deny-cat-head-tail` (one bare `cat`, `head`, `tail` or `sed -n`
on a file) and `deny-grep-rg` (a plain `grep`, `egrep`, `fgrep`, `ag` or
`git grep` over files or a directory, and `rg` too while `orch` is installed).
A `tool_call` block names the replacement in order: one `orch ctx` call when
`orch` is on PATH, otherwise `fd` for a file search and `rg` for a content
search, and the built-in `edit`, `read`, `grep` or `find` when those are
missing too; the reason is two or three lines. Reading a file is the
exception: `deny-cat-head-tail` always names the built-in `read` with offset
and limit (a section or the whole file), never `orch ctx`, whether or not
`orch` is installed. Allowed: a grep or rg filtering another command's
output, a read that feeds a pipe or a redirect, `tail -f`, several files to
`cat`, a `find` reading a pipe, and plain `fd`. Plain `fd`
and pi's built-in `grep`, `find` and `ls`, which are never blocked, keep the
appended reminder that names one `orch ctx batch` call. A blocked command line
is blocked every time and does not run on a retry; from the third block in a
row the reason adds `Blocked 3 times: use the replacement above; this guard
does not give way.` `ORCH_TOOL_GUARD=off` turns the guard off and
`ORCH_TOOL_GUARD_SKIP=find-chains,find,sed-inplace,cat-head-tail,grep-rg`
disables rules by name; the reminder stays either way. A pi build without the
`tool_result` hook blocks the first search of the session once instead. It
stays silent inside the ctx-only gate shell. Its decision tables run in node:
`go test -run 'CtxReminder' .` in `tool`.

Harness verification uses only local mock endpoints, never a paid model:

```sh
cd tool
ORCH_TEST_CTX_HARNESSES=1 go test -count=1 -v -run '^TestCtxInstalledHarnesses$' .
```

This opt-in test builds orch and starts the installed Pi and Claude headlessly
with isolated homes and dummy credentials. The mock forces shell calls, checks
that plain `orch ctx find x` works through the shim, and probes Claude's command
permissions for chaining, redirection, unrelated commands and substitution.
It also checks native tool availability and outside-repo shell/native reads,
including a native Read with offset=1 and limit=1. It records the actual tool
results, not just model claims. Ordinary tests do
not launch either harness.

## Language-server trust

Language servers are executable programs, not passive repository data. A
server found inside an inspected checkout is skipped unless that repository is
explicitly trusted. The real binary path is resolved with `EvalSymlinks`, so an
env override or PATH entry that points through a symlink into the checkout is
also subject to the policy. These checks also guard the execution boundary.

Read-only mode never honors trust: it skips project `LocalDirs` (such as
`node_modules/.bin` and `.venv/bin`) and does not select a TypeScript server
from the checkout's pnpm store. It uses only env overrides or PATH servers
whose real binary is outside the checkout, otherwise the declaration/rg tier.
TypeScript PATH discovery uses `tsgo`; a native server named `tsc` needs an
explicit `ORCH_TSLS` override because generic `tsc` is usually not an LSP.
A skipped TypeScript server adds the note `ts server in the repo not run in
read-only mode`, once per response.

Normal mode implicitly trusts only the repository containing the installed
skill (`ORCH_SKILL_DIR`). Other git repositories need an explicit user-config
opt-in:

```toml
ctx_trust = ["/absolute/repo"]
```

`ctx_deny` wins over every trust source. Git entries cover linked worktrees by
the common Git directory. Non-git directory entries match directly, including
symlink equivalence; trust is not recursive approval for unrelated child
repositories. Read-only mode never runs repo-local servers, even in a trusted
or installed-skill repository. In trusted normal mode, TypeScript auto-pick
looks for the pnpm store in the checkout and other worktrees of its repository,
so a fresh worktree can use that server without `ORCH_TSLS`. `orch doctor`
reports `ts server found in the repo, not run (...)` when a server is skipped.
This does not sandbox an external server or prevent it from running project
plugins.

## Config (user config)

Path: `$XDG_CONFIG_HOME/orchestrate/config.toml`, default
`~/.config/orchestrate/config.toml`; `ORCH_CONFIG` overrides it.

- `ctx_index`: `full` (default) builds a missing index (15 minutes at most; a
  one-line notice says so) and refreshes a stale one; `incremental` only
  refreshes an existing index; `off` never calls jbcontext.
- `ctx_deny`: repos (paths) that never use jbcontext and never run repo-local
  language servers (their worktrees included). Tasks with `private: true` never
  use jbcontext either.
- `ctx_trust`: explicit repository/directory opt-ins for repo-local language
  servers in normal mode, for example `ctx_trust = ["/absolute/repo"]`. Only
  the installed-skill repository is implicitly trusted. `ctx_deny` wins;
  read-only mode never permits these servers.
- Overrides for tests and odd installs: `ORCH_JBCONTEXT`, `ORCH_GOPLS`,
  `ORCH_TSLS` (the TypeScript 7 server, `tsc --lsp --stdio`), `ORCH_PYLS` (the
  Python server command), `ORCH_RG`,
  `ORCH_CACHE_DIR`.
