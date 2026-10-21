// Reminds the model to use `orch ctx` whenever it is about to run a raw search:
// rg, grep, egrep, fgrep, ag, fd, find, git grep, or pi's built-in grep/find/ls.
// The search still runs; the reminder is appended to its result. A pi build that
// cannot register a result hook blocks the first search in the session once.
import type { ExtensionAPI, ToolCallEvent, ToolResultEvent } from "@earendil-works/pi-coding-agent";
import { accessSync, constants, statSync } from "node:fs";
import { delimiter, join } from "node:path";

const marker = "orch ctx reminder";

const reminder =
	`${marker}: one orch ctx call replaces this search - ` +
	`orch ctx batch -q "find:Code that ..." -q "symbol:Name refs=3" -q "text:pattern glob=dir/**"\n` +
	`--compact shortens the output; sym takes a name, text a string, find a concept.`;

const shellSearches = new Set(["rg", "grep", "egrep", "fgrep", "ag", "fd", "find"]);
const builtinSearches = new Set(["grep", "find", "ls"]);

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
// argument that merely contains the word grep is not a search.
function segments(command: string): string[] {
	const out: string[] = [];
	let current = "";
	let quote = "";
	let escaped = false;
	for (const ch of command) {
		if (quote) {
			if (escaped) escaped = false;
			else if (quote === '"' && ch === "\\") escaped = true;
			else if (ch === quote) quote = "";
			continue;
		}
		if (ch === "'" || ch === '"') quote = ch;
		else if (";&|\n\r()".includes(ch)) {
			out.push(current);
			current = "";
		} else current += ch;
	}
	out.push(current);
	return out;
}

function segmentSearch(segment: string): boolean {
	const words = segment.trim().split(/\s+/).filter(Boolean);
	let at = 0;
	while (at < words.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[at] ?? "")) at += 1;
	const first = words[at];
	if (!first) return false;
	const word = first.replaceAll("\\", "").split("/").pop() ?? "";
	if (word === "git") return words[at + 1] === "grep";
	return shellSearches.has(word);
}

function commandSearch(command: string): boolean {
	return segments(command).some(segmentSearch);
}

function isSearch(toolName: string, input: Record<string, unknown>): boolean {
	if (builtinSearches.has(toolName)) return true;
	if (toolName !== "bash") return false;
	return commandSearch(String(input.command ?? ""));
}

export default function (pi: ExtensionAPI) {
	// The ctx-only gate shell already refuses everything but one orch ctx call.
	if (process.env.ORCH_CTX_ONLY_READY) return;
	if (!orchardOnPath()) return;

	let blocking = false;
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
	} catch {
		blocking = true;
	}
	if (!blocking) return;

	// No result hook available: remind by blocking the first search only, then
	// let every later search through so the retry always runs.
	let reminded = false;
	pi.on("tool_call", async (event: ToolCallEvent) => {
		if (reminded || !isSearch(event.toolName, event.input)) return undefined;
		reminded = true;
		return { block: true, reason: reminder };
	});
}
