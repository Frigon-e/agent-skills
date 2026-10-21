// Guards the model against four kinds of raw shell work and points it at the
// right tool instead. Each rule is a table entry with a name, a matcher and a
// reason: deny-find-chains (find -exec / find -delete / a search tool feeding a
// grep), deny-sed-inplace (sed -i, perl -pi), deny-cat-head-tail (a bare cat,
// head, tail or sed -n on one file) and deny-grep-rg (a plain grep, egrep,
// fgrep, rg, ag or git grep over files or a directory). A block names orch ctx
// first when `orch` is on PATH and the pi built-in tool otherwise.
// A grep filtering another command's output, plain find and plain fd are not
// blocked; the note is appended to their result instead, and to pi's built-in
// grep/find/ls, which are never blocked.
import type { ExtensionAPI, ToolCallEvent, ToolResultEvent } from "@earendil-works/pi-coding-agent";
import { accessSync, constants, statSync } from "node:fs";
import { delimiter, join } from "node:path";

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

type rule = {
	// deny-find-chains, and the short name ORCH_TOOL_GUARD_SKIP takes.
	name: string;
	id: string;
	match: (line: string) => boolean;
	reason: (orch: boolean) => string;
};

// True when an `orch` executable is on PATH, so the reminder is harmless in
// repos and sessions that never installed the skill.
function orchardOnPath(): boolean {
	for (const dir of (process.env.PATH ?? "").split(delimiter)) {
		if (!dir) continue;
		const path = join(dir, "orch");
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

// A grep with a file or directory argument, or one that recurses, searches
// files. One reading stdin is a filter, so it stays allowed.
function denyGrepRg(line: string): boolean {
	return segments(line).some((segment) => {
		const { name, args } = command(segment);
		if (!name) return false;
		if (name === "git") return args[0] === "grep" && !segment.stdinPipe;
		if (!plainSearch.has(name)) return false;
		if (searchTargets(args).length > 0) return true;
		const recurses =
			recursiveByDefault.has(name) || args.some((arg) => arg.startsWith("--recursive") || recursive.test(arg));
		return recurses && !segment.stdinPipe;
	});
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

// Two or three short lines: what is blocked, and the tool that replaces it.
const rules: rule[] = [
	{
		name: "deny-find-chains",
		id: "find-chains",
		match: denyFindChains,
		reason: (orch) =>
			`${guardMarker} (deny-find-chains): find -exec, -delete and find | grep chains are blocked.\n` +
			(orch
				? `One orch ctx batch replaces them: orch ctx batch -q "find:Code that ..." -q "symbol:Name refs=3" -q "text:pattern glob=dir/**" (--compact shortens it).`
				: `Use the built-in grep and find tools; orch ctx is not installed.`),
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
		reason: (orch) =>
			`${guardMarker} (deny-grep-rg): a plain grep, rg, ag or git grep over files is blocked.\n` +
			(orch
				? `One orch ctx batch replaces it: orch ctx batch -q "text:pattern glob=dir/**" -q "symbol:Name refs=3" (a concept: find:...).`
				: `Use the built-in grep tool; orch ctx is not installed.`),
	},
	{
		name: "deny-cat-head-tail",
		id: "cat-head-tail",
		match: denyCatHeadTail,
		reason: (orch) =>
			`${guardMarker} (deny-cat-head-tail): a bare cat, head, tail or sed -n floods the context.\n` +
			(orch
				? `Use the built-in read tool with offset and limit; for a function or type body prefer orch ctx sym Name.`
				: `Use the built-in read tool with offset and limit; orch ctx is not installed.`),
	},
];

// ORCH_TOOL_GUARD_SKIP=find-chains,sed-inplace,cat-head-tail disables single
// rules by short name or by full name.
function activeRules(): rule[] {
	const raw = process.env.ORCH_TOOL_GUARD_SKIP ?? "";
	const skip = new Set(
		raw
			.split(",")
			.map((name) => name.trim())
			.filter(Boolean),
	);
	return rules.filter((rule) => !skip.has(rule.id) && !skip.has(rule.name));
}

export default function (pi: ExtensionAPI) {
	// The ctx-only gate shell already refuses everything but one orch ctx call,
	// and has its own guard, so this one has to stay silent there.
	if (process.env.ORCH_CTX_ONLY_READY) return;
	const orch = orchardOnPath();
	const guard = process.env.ORCH_TOOL_GUARD !== "off";
	const active = guard ? activeRules() : [];

	// A blocked command line runs the next time it is asked for, and is never
	// blocked again: the tool may truly need it, and a block never repeats.
	const allowedAfterBlock = new Set<string>();

	let remindersByResult = false;
	if (orch) {
		try {
			pi.on("tool_result", async (event: ToolResultEvent) => {
				if (!isSearch(event.toolName, event.input)) return undefined;
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

	pi.on("tool_call", async (event: ToolCallEvent) => {
		const input = event.input as Record<string, unknown>;
		const line = event.toolName === "bash" ? String(input.command ?? "").trim() : "";
		if (!line) return undefined;
		if (allowedAfterBlock.has(line)) return undefined;
		for (const rule of active) {
			if (!rule.match(line)) continue;
			allowedAfterBlock.add(line);
			return { block: true, reason: rule.reason(orch) };
		}
		if (!orch || !guard || remindersByResult || fallbackReminded) return undefined;
		if (event.toolName !== "bash" || !commandSearch(line)) return undefined;
		fallbackReminded = true;
		return { block: true, reason: reminder };
	});
}
