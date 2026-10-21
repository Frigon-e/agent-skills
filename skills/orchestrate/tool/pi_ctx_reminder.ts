// Guards the model against five kinds of raw shell work and points it at the
// right tool instead. Each rule is a table entry with a name, a matcher and a
// reason: deny-find-chains (find -exec / find -delete / a search tool feeding a
// grep), deny-find (a find over a tree), deny-sed-inplace (sed -i, perl -pi),
// deny-cat-head-tail (a bare cat, head, tail or sed -n on one file) and
// deny-grep-rg (a plain grep, egrep, fgrep, ag or git grep over files or a
// directory, and rg too while orch is healthy). A block names orch ctx first
// when `orch` answers `--version`, then fd for a file search and rg for a
// content search, and the pi built-in tool last. An `orch` that is on PATH but
// cannot answer is treated as absent, and the block says it is broken.
// pi's own grep and find reach the tree without a shell, so two more rules
// refuse them the same way while orch answers; ls only lists a directory and
// stays, and with no usable orch the built-in searches are the fallback, so
// nothing is blocked and nothing is appended. orch ctx is pinned to a git
// repository root, so a built-in search that names a path outside the
// session's repository has no replacement and is allowed. A grep filtering
// another command's output and plain fd are not blocked; the note is appended
// to their result instead, and to a built-in search that does run.
import type { ExtensionAPI, ToolCallEvent, ToolResultEvent } from "@earendil-works/pi-coding-agent";
import { spawnSync } from "node:child_process";
import { accessSync, appendFileSync, constants, realpathSync, statSync } from "node:fs";
import { delimiter, dirname, isAbsolute, join, relative, resolve, sep } from "node:path";

const marker = "orch ctx reminder";
const guardMarker = "orch tool guard";

const reminder =
	`${marker}: one orch ctx call replaces this search - ` +
	`orch ctx batch -q "find:Code that ..." -q "symbol:Name refs=3" -q "text:pattern glob=dir/**"\n` +
	`--compact shortens the output; sym takes a name, text a string, find a concept.`;

const shellSearches = new Set(["rg", "grep", "egrep", "fgrep", "ag", "fd", "find"]);
const builtinSearches = new Set(["grep", "find", "ls"]);

type word = { text: string; quoted: boolean };
type segment = {
	words: word[];
	// stdin comes from an earlier command's pipe, so this only formats it.
	stdinPipe: boolean;
	// stdout feeds a later command, so this is one step of a pipeline.
	stdoutPipe: boolean;
	// A redirect writes a file, so the command is not a plain read.
	redirect: boolean;
};

// The replacements, in the order a block names them: our ctx system first,
// then the standalone search tools the user prefers, then pi's built-ins.
// `broken` carries the probe's error when orch is on PATH but does not answer,
// so a block can say why it stopped naming it.
type tools = { orch: boolean; fd: boolean; rg: boolean; broken: string };

type rule = {
	// The full name, and the short name ORCH_TOOL_GUARD_SKIP takes.
	name: string;
	id: string;
	match: (line: string, tools: tools) => boolean;
	reason: (tools: tools) => string;
	// A rule may add one line built from the pattern the model typed. Empty
	// when there is nothing to build it from.
	tuned?: (line: string, tools: tools) => string;
};

// A built-in tool call: pi's own tools reach the tree without a shell, so a
// rule for one matches the tool name and the arguments it carries.
type builtinCall = { name: string; input: Record<string, unknown> };

type builtinRule = {
	// The full name the reason shows, and the short name the log records.
	name: string;
	id: string;
	// The one ORCH_TOOL_GUARD_SKIP name that disables every built-in rule.
	group: string;
	match: (call: builtinCall) => boolean;
	// The reason is fixed: the rule only matches while orch answers.
	reason: string;
	// The kind of the pattern the model typed, for the log.
	kind: (input: Record<string, unknown>) => string;
	// One line built from the arguments the model typed, like a shell rule's.
	tuned: (input: Record<string, unknown>) => string;
};

// ORCH_TOOL_GUARD_HINT=pattern appends one line built from the blocked
// pattern, so the two messages can be A/B tested against each other. Unset or
// ORCH_TOOL_GUARD_HINT=generic keeps the generic text.
type hintMode = "generic" | "pattern";

function guardHintMode(): hintMode {
	return process.env.ORCH_TOOL_GUARD_HINT === "pattern" ? "pattern" : "generic";
}

// ORCH_TOOL_GUARD_LOG=<path> appends one JSON line per block, for the A/B
// count: which harness, which rule, which hint, which pattern kind and the
// command. Off by default, and a guard never fails a call because of it.
function guardLog(harness: string, rule: string, hint: hintMode, kind: string, line: string): void {
	const path = process.env.ORCH_TOOL_GUARD_LOG;
	if (!path) return;
	const entry = { ts: new Date().toISOString(), harness, rule, hint, kind, command: line.slice(0, 200) };
	try {
		appendFileSync(path, `${JSON.stringify(entry)}\n`);
	} catch {
		// Not writable: the block still has to happen.
	}
}

// The probe's timeout in milliseconds. `orch --version` prints a fingerprint
// and starts nothing else, so a healthy wrapper answers at once; one that
// cannot find its tool directory fails at once too.
const probeTimeout = 3000;

// The first line of the probe's error, cut at 120 characters, so it fits in one
// line of a block.
function firstLine(text: string): string {
	for (const part of String(text).split("\n")) {
		const line = part.trim();
		if (line) return line.slice(0, 120);
	}
	return "";
}

// True when `orch --version` exits 0 within the timeout. A wrapper that is on
// PATH and cannot answer is not a replacement, and a block that points at it
// teaches the model to go around the guard instead.
function orchAnswers(): { ok: boolean; detail: string } {
	try {
		const done = spawnSync("orch", ["--version"], {
			timeout: probeTimeout,
			encoding: "utf8",
			killSignal: "SIGKILL",
		});
		if (done.error) return { ok: false, detail: firstLine(done.error.message) || "probe failed" };
		if (done.status === 0) return { ok: true, detail: "" };
		const detail =
			firstLine(done.stderr ?? "") || firstLine(done.stdout ?? "") || `orch --version exited ${done.status ?? "unknown"}`;
		return { ok: false, detail };
	} catch (error) {
		return { ok: false, detail: firstLine(String(error)) || "probe failed" };
	}
}

// ORCH_TOOL_GUARD_LOG also records a probe that found a broken orch, once per
// probe, so a session where the guard could not name its tool is visible next
// to the blocks.
function healthLog(detail: string): void {
	const path = process.env.ORCH_TOOL_GUARD_LOG;
	if (!path) return;
	const entry = { ts: new Date().toISOString(), harness: "pi", event: "orch-unhealthy", detail };
	try {
		appendFileSync(path, `${JSON.stringify(entry)}\n`);
	} catch {
		// Not writable: the block still has to happen.
	}
}

// True when an executable of this name is on PATH, so the reminder is harmless
// in repos and sessions that never installed the skill or the search tools.
function onPath(name: string): boolean {
	for (const dir of (process.env.PATH ?? "").split(delimiter)) {
		if (!dir) continue;
		const path = join(dir, name);
		try {
			if (!statSync(path).isFile()) continue;
			accessSync(path, constants.X_OK);
			return true;
		} catch {
			// Not this directory; keep looking.
		}
	}
	return false;
}

// Computed once: PATH does not change under a running session, and one probe
// per session is enough for a wrapper whose content does not change either.
function installed(): tools {
	const present = onPath("orch");
	let healthy = false;
	let detail = "";
	if (present) {
		const probe = orchAnswers();
		healthy = probe.ok;
		if (!healthy) {
			detail = probe.detail;
			healthLog(detail);
		}
	}
	return { orch: healthy, fd: onPath("fd"), rg: onPath("rg"), broken: detail };
}

// The one line a search block adds when orch is installed but cannot answer:
// the model is told the replacement it cannot use, instead of hunting for a
// loophole around the block.
function brokenLine(detail: string): string {
	return `orch ctx is installed but broken here (${detail}); use the tools above and tell the user.`;
}

// The three rules that name a search replacement add that line. The two that
// always name a built-in tool (edit, read) never do, because orch does not back
// them and a file edit or read still works.
function withBroken(reason: string, broken: string): string {
	return broken ? `${reason}\n${brokenLine(broken)}` : reason;
}

// Split a command line on unquoted separators, dropping quoted text, so an
// argument that merely contains the word grep is not a search. Each word also
// records whether it came from a quote, so a quoted word is never a command.
function segments(command: string): segment[] {
	const out: segment[] = [];
	let words: word[] = [];
	let text = "";
	let started = false;
	let quoted = false;
	let quote = "";
	let escaped = false;
	let stdinPipe = false;
	let redirect = false;

	const endWord = () => {
		if (!started) return;
		words.push({ text, quoted });
		text = "";
		started = false;
		quoted = false;
	};
	const endSegment = (pipeOut: boolean) => {
		endWord();
		if (words.length) out.push({ words, stdinPipe, stdoutPipe: pipeOut, redirect });
		words = [];
		stdinPipe = pipeOut;
		redirect = false;
	};

	for (const ch of command) {
		if (quote) {
			started = true;
			quoted = true;
			if (escaped) {
				escaped = false;
				text += ch;
			} else if (quote === '"' && ch === "\\") escaped = true;
			else if (ch === quote) quote = "";
			else text += ch;
			continue;
		}
		if (ch === "'" || ch === '"') {
			quote = ch;
			started = true;
			quoted = true;
			continue;
		}
		if (ch === ">" || ch === "<") {
			endWord();
			redirect = true;
			continue;
		}
		if (ch === "|") {
			endSegment(true);
			continue;
		}
		if (";&\n\r()".includes(ch)) {
			endSegment(false);
			continue;
		}
		if (ch === " " || ch === "\t") {
			endWord();
			continue;
		}
		text += ch;
		started = true;
	}
	endSegment(false);
	return out;
}

function base(word: string): string {
	return word.replaceAll("\\", "").split("/").pop() ?? "";
}

// sudo flags that take a separate value, so `sudo -u root cat f` still reads.
const sudoValue = /^-[ugpCDhTr]$/;

// The command a segment runs, with its arguments. Leading VAR=value prefixes,
// `sudo` and a path to the tool do not hide the command from a rule.
function command(segment: segment): { name: string; args: string[] } {
	const words = segment.words;
	let at = 0;
	while (at < words.length && !words[at].quoted && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[at].text)) at += 1;
	while (at < words.length && !words[at].quoted && base(words[at].text) === "sudo") {
		at += 1;
		while (at < words.length && !words[at].quoted && /^-[A-Za-z]/.test(words[at].text)) {
			const flag = words[at].text;
			at += 1;
			if (sudoValue.test(flag)) at += 1;
		}
		while (at < words.length && !words[at].quoted && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[at].text)) at += 1;
	}
	const first = words[at];
	if (!first || first.quoted) return { name: "", args: [] };
	return { name: base(first.text), args: words.slice(at + 1).map((w) => w.text) };
}

function segmentSearch(segment: segment): boolean {
	const { name, args } = command(segment);
	if (!name) return false;
	if (name === "git") return args[0] === "grep";
	if (shellSearches.has(name)) return true;
	if (name === "xargs") return args.some((arg) => arg === "git" || shellSearches.has(base(arg)));
	return false;
}

function commandSearch(command: string): boolean {
	return segments(command).some(segmentSearch);
}

function isSearch(toolName: string, input: Record<string, unknown>): boolean {
	if (builtinSearches.has(toolName)) return true;
	if (toolName !== "bash") return false;
	return commandSearch(String(input.command ?? ""));
}

// -i and its combined forms: -i, -i.bak, -pi, -ni, and --in-place.
const inPlace = /^-[A-Za-z]*i/;

function inPlaceEdit(args: string[]): boolean {
	return args.some((arg) => arg.startsWith("--in-place") || inPlace.test(arg));
}

// find runs a command on every match, or deletes: that is a chain.
const findChain = new Set(["-exec", "-execdir", "-ok", "-okdir", "-delete"]);
// A search tool feeding one of these is a search chain.
const searchSource = new Set(["find", "fd", "rg", "ag"]);
const searchSink = new Set(["grep", "rg", "ag"]);

function denyFindChains(line: string): boolean {
	const list = segments(line);
	for (const segment of list) {
		const { name, args } = command(segment);
		if (name === "find" && args.some((arg) => findChain.has(arg))) return true;
	}
	for (let at = 0; at < list.length; at += 1) {
		if (!list[at].stdoutPipe || !searchSource.has(command(list[at]).name)) continue;
		for (let next = at + 1; next < list.length && list[next].stdinPipe; next += 1) {
			const { name, args } = command(list[next]);
			const sink = name === "xargs" ? args.some((arg) => searchSink.has(base(arg)) || arg === "find") : searchSink.has(name);
			if (sink) return true;
			if (!list[next].stdoutPipe) break;
		}
	}
	return false;
}

// find over a tree is a file search by name, path or type, which fd or one
// ctx call answers. A find reading a pipe is formatting stdin, so it stays.
function denyFind(line: string): boolean {
	return segments(line).some((segment) => !segment.stdinPipe && command(segment).name === "find");
}

function denySedInplace(line: string): boolean {
	return segments(line).some((segment) => {
		const { name, args } = command(segment);
		if (name === "sed" || name === "gsed") return inPlaceEdit(args);
		if (name === "perl") return args.some((arg) => inPlace.test(arg));
		return false;
	});
}

// flags of head and tail: -f, -F and --follow keep watching, so they are not a
// plain read of one file.
const follow = /^-[A-Za-z]*[fF]/;

// cat takes no counted flags, so every word that is not a flag names a file.
function plainFiles(args: string[]): string[] {
	return args.filter((arg) => arg !== "-" && !arg.startsWith("-"));
}

function countFiles(args: string[]): string[] {
	const out: string[] = [];
	for (let at = 0; at < args.length; at += 1) {
		const arg = args[at];
		if (arg === "-n" || arg === "-c" || arg === "-s" || arg === "--lines" || arg === "--bytes") {
			at += 1;
			continue;
		}
		if (arg.startsWith("-") || arg === "-") continue;
		out.push(arg);
	}
	return out;
}

// sed prints a line range to stdout when it has -n or a script that only
// prints, and names the file as the first word after the script.
function sedFiles(args: string[]): string[] {
	const out: string[] = [];
	let scripted = false;
	for (let at = 0; at < args.length; at += 1) {
		const arg = args[at];
		if (arg.startsWith("-") && arg !== "-") {
			if (arg === "-e" || arg === "-f" || arg === "--expression" || arg === "--file") {
				scripted = true;
				at += 1;
			}
			continue;
		}
		if (!scripted) {
			scripted = true;
			continue;
		}
		out.push(arg);
	}
	return out;
}

function sedPrints(args: string[]): boolean {
	if (args.some((arg) => arg === "-n" || /^-[A-Za-z]*n$/.test(arg))) return true;
	return args.some((arg) => /^[\d$,; ]*p$/.test(arg));
}

const plainSearch = new Set(["grep", "egrep", "fgrep", "rg", "ag"]);
// rg and ag search the tree under the current directory without a flag.
const recursiveByDefault = new Set(["rg", "ag"]);
const recursive = /^-[A-Za-z]*[rR]/;
// grep flags whose value is the pattern, a type, a glob or a count, not a path.
const grepValue = new Set([
	"-e",
	"-f",
	"-A",
	"-B",
	"-C",
	"-m",
	"-t",
	"-g",
	"-j",
	"--regexp",
	"--file",
	"--type",
	"--glob",
	"--max-count",
	"--after-context",
	"--before-context",
	"--context",
]);

// The file and directory arguments of a grep, which follow its pattern.
function searchTargets(args: string[]): string[] {
	const out: string[] = [];
	let patterned = false;
	for (let at = 0; at < args.length; at += 1) {
		const arg = args[at];
		if (arg.startsWith("-") && arg !== "-") {
			if (grepValue.has(arg)) at += 1;
			continue;
		}
		if (!patterned) {
			patterned = true;
			continue;
		}
		out.push(arg);
	}
	return out;
}

// The pattern of a grep: the value of -e or --regexp, else its first word that
// is not a flag.
function searchPattern(args: string[]): string {
	for (let at = 0; at < args.length; at += 1) {
		const arg = args[at];
		if (arg === "-e" || arg === "--regexp") return args[at + 1] ?? "";
		if (arg.startsWith("-") && arg !== "-") {
			if (grepValue.has(arg)) at += 1;
			continue;
		}
		return arg;
	}
	return "";
}

// What a blocked pattern asks for. The same three shapes are classified by
// hooks/claude/toolguard.py, so a change here needs the twin change there and
// a case in hooks/claude/toolguard_cases.json.
type searchKind = "symbol" | "text" | "concept";

// One identifier and nothing else: letters, digits, _ and . only.
const identifierShape = /^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$/;
const questionWords = new Set(["how", "what", "which", "where", "when", "why", "who"]);
// A character class escape says regex, so the pattern is a mix and not a name.
const charClass = /\\[wWdDsS]/;

// Alternation branches, ignoring an escaped pipe.
function branches(pattern: string): number {
	let count = 1;
	for (let at = 0; at < pattern.length; at += 1) {
		if (pattern[at] === "\\") {
			at += 1;
			continue;
		}
		if (pattern[at] === "|") count += 1;
	}
	return count;
}

// .* or .+ with words on both sides: the model is describing a gap, not a
// string.
function wildcardBetweenWords(pattern: string): boolean {
	for (const marker of [".*", ".+"]) {
		const at = pattern.indexOf(marker);
		if (at > 0 && at + marker.length < pattern.length) return true;
	}
	return false;
}

// Three kinds by shape alone: concept is a broad query, symbol one identifier,
// text anything else (a string, a route, a path, a short regex with simple
// anchors only).
function patternKind(pattern: string): searchKind {
	const trimmed = pattern.trim();
	if (!trimmed) return "text";
	const words = trimmed.split(/\s+/);
	if (branches(trimmed) >= 3) return "concept";
	if (wildcardBetweenWords(trimmed)) return "concept";
	if (words.length >= 4) return "concept";
	if (trimmed.includes("?") || questionWords.has(words[0].toLowerCase())) return "concept";
	if (charClass.test(trimmed)) return "concept";
	if (identifierShape.test(trimmed)) return "symbol";
	return "text";
}

// The directory a grep searches, which the text line scopes with --glob: a
// file names its own directory, and no target or `.` searches the root.
function searchDir(args: string[]): string {
	const targets = searchTargets(args);
	if (!targets.length) return "";
	const first = targets[0].replace(/\/+$/, "");
	if (!first || first === ".") return "";
	const parts = first.split("/");
	const last = parts[parts.length - 1] ?? "";
	if (last.includes(".")) return parts.slice(0, -1).join("/");
	return first;
}

// One shell word, safe to paste into a command.
function shellQuoted(text: string): string {
	return `'${text.replaceAll("'", "'\\''")}'`;
}

// A concept query already begins with this, so a pattern that carries it does
// not get it twice.
const leadingCodeThat = /^code that\s+/i;

// The concept phrase: the model's own words with the regex syntax dropped.
function conceptWords(pattern: string): string {
	const words = pattern
		.replaceAll(/\\./g, " ")
		.replaceAll(/\.\*|\.\+/g, " ")
		.replaceAll(/[|^$*+?()[\]{}]/g, " ")
		.replaceAll(/\s+/g, " ")
		.trim();
	return words.replace(leadingCodeThat, "");
}

// The one line a pattern hint adds: the ctx query that fits what was typed.
// Empty when the grep has no pattern to build it from.
function patternHint(segment: segment): string {
	const { name, args } = command(segment);
	const pattern = searchPattern(name === "git" ? args.slice(1) : args);
	if (!pattern) return "";
	const kind = patternKind(pattern);
	if (kind === "symbol") return `Use orch ctx sym ${pattern} --refs 3.`;
	const dir = searchDir(name === "git" ? args.slice(1) : args);
	const words = conceptWords(pattern);
	if (kind === "concept" && words) {
		const phrase = `Code that ${words}`.replaceAll(/([\\$`"])/g, "\\$1");
		return (
			`Use orch ctx find "${phrase}" (a broader search; use it when no name or string is known).`
		);
	}
	return textHint(pattern, dir);
}

// The literal-string line, in the batch form the rest of the guard shows: the
// pattern in single quotes inside the query, the query one double-quoted shell
// word. A pattern or directory that cannot sit in both keeps the single command.
function textHint(pattern: string, dir: string): string {
	if (!pattern.includes("'") && !(dir && /[\s'"\\$`]/.test(dir))) {
		const escaped = pattern.replaceAll(/([\\$`"])/g, "\\$1");
		return `Use orch ctx batch -q "text:'${escaped}'${dir ? ` glob=${dir}/**` : ""}".`;
	}
	return dir
		? `Use orch ctx text --glob ${shellQuoted(`${dir}/**`)} -- ${shellQuoted(pattern)}.`
		: `Use orch ctx text -- ${shellQuoted(pattern)}.`;
}

// The root of the git repository the session runs in, found by walking up from
// the working directory (a worktree root has a .git file rather than a
// directory, so any entry counts), or "" outside one. Resolved once: the working
// directory does not change under a session.
let repoRoot: string | undefined;
function repositoryRoot(): string {
	if (repoRoot !== undefined) return repoRoot;
	repoRoot = "";
	let dir = process.cwd();
	for (;;) {
		try {
			statSync(join(dir, ".git"));
			repoRoot = realpathSync(dir);
			break;
		} catch {
			// No repository here; keep walking up.
		}
		const parent = dirname(dir);
		if (parent === dir) break;
		dir = parent;
	}
	return repoRoot;
}

// Resolve relative and missing paths from the session directory. orch ctx is
// pinned to a git repository root, so searches outside it have no ctx answer.
function insideRepository(path: string): boolean {
	const root = repositoryRoot();
	if (!root) return false;
	let resolved = resolve(path.trim() || ".");
	try {
		resolved = realpathSync(resolved);
	} catch {
		// Missing targets keep their lexical scope.
	}
	const rel = relative(root, resolved);
	return rel !== ".." && !rel.startsWith(`..${sep}`) && !isAbsolute(rel);
}

// The directory a built-in search starts from, scoped the way the shell hint
// scopes it: a file names its own directory, and no path or `.` is the root.
function builtinDir(input: Record<string, unknown>): string {
	const path = String(input.path ?? "").trim().replace(/\/+$/, "");
	if (!path || path === ".") return "";
	const parts = path.split("/");
	const last = parts[parts.length - 1] ?? "";
	if (last.includes(".")) return parts.slice(0, -1).join("/");
	return path;
}

// The name or glob a built-in find asks for. The installed pi passes `pattern`;
// `name` and `glob` are read too so a build that names the argument otherwise
// still gets a hint instead of a generic reason.
function builtinFindPattern(input: Record<string, unknown>): string {
	for (const key of ["pattern", "name", "glob"]) {
		const value = String(input[key] ?? "").trim();
		if (value) return value;
	}
	return "";
}

// The one ctx line a built-in search's pattern lands on, in the batch syntax the
// other hints use: a name is a symbol, a broad phrase is a concept, and a string
// is a text lookup scoped by the directory the model named.
function builtinHint(kind: searchKind, pattern: string, dir: string): string {
	if (kind === "symbol") return `Use orch ctx batch -q "symbol:${pattern} refs=3".`;
	const words = conceptWords(pattern);
	if (kind === "concept" && words) {
		const query = `find:Code that ${words}`.replaceAll(/([\\$`"])/g, "\\$1");
		return `Use orch ctx batch -q "${query}".`;
	}
	return textHint(pattern, dir);
}

// The built-in grep's pattern picks one of the same three kinds as the shell
// rule, so a name, a string and a broad phrase each get their own query.
function builtinGrepHint(input: Record<string, unknown>): string {
	const pattern = String(input.pattern ?? "").trim();
	if (!pattern) return "";
	return builtinHint(patternKind(pattern), pattern, builtinDir(input));
}

// The built-in find's name or glob argument is a file name, and no ctx kind
// lists files by name: a concept asks for a concept search, and anything else is
// a text lookup scoped by the directory, which is what `glob=` is for.
function builtinFindHint(input: Record<string, unknown>): string {
	const pattern = builtinFindPattern(input);
	if (!pattern) return "";
	const kind = patternKind(pattern) === "concept" ? "concept" : "text";
	return builtinHint(kind, pattern, builtinDir(input));
}

// The line the log records for a built-in call: the tool and the arguments the
// model typed, in the shape a shell command would have.
function builtinLogLine(tool: string, input: Record<string, unknown>): string {
	const parts = [tool];
	for (const key of ["pattern", "name", "path", "glob"]) {
		const value = String(input[key] ?? "").trim();
		if (value) parts.push(`${key}=${value}`);
	}
	return parts.join(" ");
}

// The grep segment a block names, so a hint is built from the pattern the
// model typed and not from a generic example.
function grepSegment(line: string, tools: tools): segment | undefined {
	return segments(line).find((segment) => grepSearches(segment, tools));
}

// One segment is a raw content search over files.
function grepSearches(segment: segment, tools: tools): boolean {
	const { name, args } = command(segment);
	if (!name) return false;
	if (name === "git") return args[0] === "grep" && !segment.stdinPipe;
	if (name === "rg" && !tools.orch) return false;
	if (!plainSearch.has(name)) return false;
	if (searchTargets(args).length > 0) return true;
	const recurses =
		recursiveByDefault.has(name) || args.some((arg) => arg.startsWith("--recursive") || recursive.test(arg));
	return recurses && !segment.stdinPipe;
}

// The kind of the blocked grep's pattern, for the log.
function grepKind(line: string, tools: tools): string {
	const segment = grepSegment(line, tools);
	if (!segment) return "";
	const { name, args } = command(segment);
	const pattern = searchPattern(name === "git" ? args.slice(1) : args);
	return pattern ? patternKind(pattern) : "";
}

// A grep with a file or directory argument, or one that recurses, searches
// files. One reading stdin is a filter, so it stays allowed. With no orch, rg
// is the preferred grep rather than a raw search, so rg itself stays allowed.
function denyGrepRg(line: string, tools: tools): boolean {
	return segments(line).some((segment) => grepSearches(segment, tools));
}

function denyCatHeadTail(line: string): boolean {
	return segments(line).some((segment) => {
		// A pipeline formats or takes its input elsewhere, and a redirect
		// writes a file, so neither is a bare read of one file.
		if (segment.stdinPipe || segment.stdoutPipe || segment.redirect) return false;
		const { name, args } = command(segment);
		if (name === "cat") return plainFiles(args).length === 1;
		if (name === "head" || name === "tail") {
			if (args.some((arg) => follow.test(arg) || arg.startsWith("--follow"))) return false;
			return countFiles(args).length === 1;
		}
		if (name === "sed") {
			if (inPlaceEdit(args)) return false;
			return sedPrints(args) && sedFiles(args).length === 1;
		}
		return false;
	});
}

// Two or three short lines: what is blocked, and the tool that replaces it. A
// search rule adds one more line while orch is broken, and the 3rd block in a
// row one more again.
const rules: rule[] = [
	{
		name: "deny-find-chains",
		id: "find-chains",
		match: denyFindChains,
		reason: ({ orch, fd, broken }) =>
			withBroken(
				`${guardMarker} (deny-find-chains): find -exec, -delete and find | grep chains are blocked.\n` +
					(orch
						? `One orch ctx batch replaces them: orch ctx batch -q "find:Code that ..." -q "symbol:Name refs=3" -q "text:pattern glob=dir/**" (--compact shortens it).`
						: fd
							? `Use fd instead (e.g. fd -e ts pattern dir, and fd -x cmd replaces find -exec).`
							: `Use the built-in grep and find tools; orch ctx and fd are not installed.`),
				broken,
			),
	},
	{
		name: "deny-find",
		id: "find",
		match: denyFind,
		reason: ({ orch, fd, broken }) =>
			withBroken(
				`${guardMarker} (deny-find): find over a tree is blocked.\n` +
					(orch
						? `One orch ctx batch replaces it: orch ctx batch -q "find:Code that ..." -q "text:pattern glob=dir/**" (--compact shortens it).`
						: fd
							? `Use fd instead (e.g. fd -e ts pattern dir, and fd -x cmd replaces find -exec).`
							: `Use the built-in find tool; orch ctx and fd are not installed.`),
				broken,
			),
	},
	{
		name: "deny-sed-inplace",
		id: "sed-inplace",
		match: denySedInplace,
		reason: () =>
			`${guardMarker} (deny-sed-inplace): sed -i and perl -pi rewrite a file in place.\n` +
			`Use the built-in edit tool; orch ctx is read-only and never replaces an edit.`,
	},
	{
		name: "deny-grep-rg",
		id: "grep-rg",
		match: denyGrepRg,
		reason: ({ orch, rg, broken }) =>
			withBroken(
				`${guardMarker} (deny-grep-rg): a plain grep${orch ? ", rg" : ""}, ag or git grep over files is blocked.\n` +
					(orch
						? `One orch ctx batch replaces it: orch ctx batch -q "text:pattern glob=dir/**" -q "symbol:Name refs=3" (a concept: find:...).`
						: rg
							? `Use rg instead (e.g. rg -n pattern dir).`
							: `Use the built-in grep tool; orch ctx and rg are not installed.`),
				broken,
			),
		tuned: (line, tools) => {
			const segment = grepSegment(line, tools);
			return segment ? patternHint(segment) : "";
		},
	},
	{
		name: "deny-cat-head-tail",
		id: "cat-head-tail",
		match: denyCatHeadTail,
		reason: () =>
			`${guardMarker} (deny-cat-head-tail): a bare cat, head, tail or sed -n floods the context.\n` +
			`Use the built-in read tool with offset and limit (a section or the whole file).`,
	},
];

// pi's built-in grep and find walk the tree without a shell, so a shell rule
// alone leaves them as the way around the guard. While orch answers they are
// refused like the shell rules and point at the same ctx query; with no usable
// orch they are the fallback, so they are never blocked then. `ls` lists a
// directory instead of searching, so it has no rule and stays allowed.
const builtinRules: builtinRule[] = [
	{
		name: "deny-builtin-grep",
		id: "builtin-grep",
		group: "builtin-search",
		match: (call) => call.name === "grep",
		reason:
			`${guardMarker} (deny-builtin-grep): the built-in grep tool is blocked; orch ctx answers searches here.\n` +
			`One orch ctx batch replaces it: orch ctx batch -q "text:pattern glob=dir/**" -q "symbol:Name refs=3" (a concept: find:...).`,
		kind: (input) => {
			const pattern = String(input.pattern ?? "").trim();
			return pattern ? patternKind(pattern) : "";
		},
		tuned: builtinGrepHint,
	},
	{
		name: "deny-builtin-find",
		id: "builtin-find",
		group: "builtin-search",
		match: (call) => call.name === "find",
		reason:
			`${guardMarker} (deny-builtin-find): the built-in find tool is blocked; orch ctx answers file searches here.\n` +
			`One orch ctx batch replaces it: orch ctx batch -q "find:Code that ..." -q "text:pattern glob=dir/**" (--compact shortens it).`,
		kind: (input) => {
			const pattern = builtinFindPattern(input);
			return pattern ? patternKind(pattern) : "";
		},
		tuned: builtinFindHint,
	},
];

// The rules and the built-in rules ORCH_TOOL_GUARD_SKIP disables, by short name,
// by full name, or by group (`builtin-search` for both built-in rules at once).
function skipped(): Set<string> {
	const raw = process.env.ORCH_TOOL_GUARD_SKIP ?? "";
	return new Set(
		raw
			.split(",")
			.map((name) => name.trim())
			.filter(Boolean),
	);
}

// ORCH_TOOL_GUARD_SKIP=find-chains,find,sed-inplace,cat-head-tail disables
// single rules by short name or by full name.
function activeRules(): rule[] {
	const skip = skipped();
	return rules.filter((rule) => !skip.has(rule.id) && !skip.has(rule.name));
}

function activeBuiltinRules(): builtinRule[] {
	const skip = skipped();
	return builtinRules.filter((rule) => !skip.has(rule.id) && !skip.has(rule.name) && !skip.has(rule.group));
}

export default function (pi: ExtensionAPI) {
	// The ctx-only gate shell already refuses everything but one orch ctx call,
	// and has its own guard, so this one has to stay silent there.
	if (process.env.ORCH_CTX_ONLY_READY) return;
	const ready = installed();
	const guard = process.env.ORCH_TOOL_GUARD !== "off";
	const active = guard ? activeRules() : [];
	const activeBuiltins = guard ? activeBuiltinRules() : [];

	// A blocked command stays blocked, so the model learns not to use it. From
	// the third block in a row the reason also says the guard will not give way.
	// Any call the guard lets through ends the run.
	const giveWay = `\nBlocked 3 times: use the replacement above; this guard does not give way.`;
	let blocksInRow = 0;

	let remindersByResult = false;
	if (ready.orch) {
		try {
			pi.on("tool_result", async (event: ToolResultEvent) => {
				if (!isSearch(event.toolName, event.input)) return undefined;
				// A search outside the repository has no orch ctx replacement, so it
				// gets no note either.
				if (!insideRepository(String(event.input.path ?? ""))) return undefined;
				const content = event.content ?? [];
				if (content.some((part) => part.type === "text" && part.text.includes(marker))) return undefined;
				const next = [...content, { type: "text" as const, text: reminder }];
				return event.structuredContent === undefined
					? { content: next }
					: { content: next, structuredContent: event.structuredContent };
			});
			remindersByResult = true;
		} catch {
			remindersByResult = false;
		}
	}

	// No result hook available: remind by blocking the first shell search only,
	// then let every later search through so the retry always runs.
	let fallbackReminded = false;

	// The reason a block carries: the rule's own text, the tailored line when the
	// hint mode asks for one, and the give-way line from the third block in a row.
	const blockReason = (reason: string, tuned: string) =>
		reason + (tuned ? `\n${tuned}` : "") + (blocksInRow >= 3 ? giveWay : "");

	pi.on("tool_call", async (event: ToolCallEvent) => {
		const input = event.input as Record<string, unknown>;
		// The built-in searches are the way around the shell rules, so while orch
		// answers they are refused the same way. With no usable orch they are the
		// fallback, so they run and the shell rules point at them instead.
		const builtin = ready.orch
			? activeBuiltins.find(
					(rule) => rule.match({ name: event.toolName, input }) && insideRepository(String(input.path ?? "")),
				)
			: undefined;
		if (builtin) {
			blocksInRow += 1;
			const mode = guardHintMode();
			guardLog("pi", builtin.id, mode, builtin.kind(input), builtinLogLine(event.toolName, input));
			return { block: true, reason: blockReason(builtin.reason, mode === "pattern" ? builtin.tuned(input) : "") };
		}
		const line = event.toolName === "bash" ? String(input.command ?? "").trim() : "";
		if (!line) {
			blocksInRow = 0;
			return undefined;
		}
		for (const rule of active) {
			if (!rule.match(line, ready)) continue;
			blocksInRow += 1;
			const mode = guardHintMode();
			// The pattern hint is one extra line, and only when the reason can
			// name the tool it points at.
			const tuned = mode === "pattern" && ready.orch && rule.tuned ? rule.tuned(line, ready) : "";
			guardLog("pi", rule.id, mode, rule.id === "grep-rg" ? grepKind(line, ready) : "", line);
			return { block: true, reason: blockReason(rule.reason(ready), tuned) };
		}
		blocksInRow = 0;
		if (!ready.orch || !guard || remindersByResult || fallbackReminded) return undefined;
		if (event.toolName !== "bash" || !commandSearch(line)) return undefined;
		fallbackReminded = true;
		return { block: true, reason: reminder };
	});
}
