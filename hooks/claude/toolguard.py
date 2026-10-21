#!/usr/bin/env python3
"""Shared guard for the Claude Code deny hooks in this folder.

This is a line for line port of the five rules in
skills/orchestrate/tool/pi_ctx_reminder.ts, which stays the source of truth:
deny-find-chains, deny-find, deny-sed-inplace, deny-cat-head-tail and
deny-grep-rg. `deny-find-chains.sh` owns the first two, because a plain find is
a file search and a find chain is its sharper form; the other three scripts own
one rule each.

Each script passes its group name as the only argument. The hook JSON arrives
on stdin, and a block is exit 2 with the reason on stderr, which is how Claude
Code's PreToolUse hooks hand a refusal back to the model.

The replacement text names a tool in the same order as pi: `orch ctx` first
when `orch` is on PATH, then fd for a file search and rg for a content search,
then the built-in Claude tools.
"""

import json
import os
import re
import sys
from datetime import datetime, timezone

GUARD_MARKER = "orch tool guard"

# The rule order matters: a find chain is the sharper name for a plain find.
SHELL_SEARCHES = frozenset({"rg", "grep", "egrep", "fgrep", "ag", "fd", "find"})

SUDO_VALUE = re.compile(r"^-[ugpCDhTr]$")
VAR_PREFIX = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=")
SUDO_FLAG = re.compile(r"^-[A-Za-z]")
IN_PLACE = re.compile(r"^-[A-Za-z]*i")
FOLLOW = re.compile(r"^-[A-Za-z]*[fF]")
RECURSIVE = re.compile(r"^-[A-Za-z]*[rR]")
SED_PRINT = re.compile(r"^[\d$,; ]*p$")

FIND_CHAIN = frozenset({"-exec", "-execdir", "-ok", "-okdir", "-delete"})
SEARCH_SOURCE = frozenset({"find", "fd", "rg", "ag"})
SEARCH_SINK = frozenset({"grep", "rg", "ag"})
PLAIN_SEARCH = frozenset({"grep", "egrep", "fgrep", "rg", "ag"})
RECURSIVE_BY_DEFAULT = frozenset({"rg", "ag"})
# What a blocked pattern asks for. The same three shapes are classified by
# skills/orchestrate/tool/pi_ctx_reminder.ts, so a change here needs the twin
# change there and a case in toolguard_cases.json.
IDENTIFIER_SHAPE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$")
QUESTION_WORDS = frozenset({"how", "what", "which", "where", "when", "why", "who"})
# A character class escape says regex, so the pattern is a mix and not a name.
CHAR_CLASS = re.compile(r"\\[wWdDsS]")
ESCAPE = re.compile(r"\\.")
WILDCARD = re.compile(r"\.\*|\.\+")
CONCEPT_STRIP = re.compile(r"[|^$*+?()\[\]{}]")
# A concept query already begins with this, so a pattern carrying it does not
# get it twice.
LEADING_CODE_THAT = re.compile(r"^code that ", re.IGNORECASE)
GREP_VALUE = frozenset(
    {
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
    }
)


def on_path(name):
    """True when an executable of this name is on PATH."""
    for directory in (os.environ.get("PATH") or "").split(os.pathsep):
        if not directory:
            continue
        path = os.path.join(directory, name)
        try:
            if os.path.isfile(path) and os.access(path, os.X_OK):
                return True
        except OSError:
            continue
    return False


def installed():
    """The replacements a block may name, computed once per hook run."""
    return {"orch": on_path("orch"), "fd": on_path("fd"), "rg": on_path("rg")}


def segments(command):
    """Split a command line on unquoted separators, keeping quote awareness.

    A word records whether it came from a quote, so a quoted word is never a
    command. A segment records a pipe on either side and a redirect, because a
    pipeline formats or forwards input and a redirect writes a file.
    """
    out = []
    words = []
    text = ""
    started = False
    quoted = False
    quote = ""
    escaped = False
    stdin_pipe = False
    redirect = False

    def end_word():
        nonlocal text, started, quoted
        if not started:
            return
        words.append((text, quoted))
        text = ""
        started = False
        quoted = False

    def end_segment(pipe_out):
        nonlocal words, stdin_pipe, redirect
        end_word()
        if words:
            out.append(
                {
                    "words": words,
                    "stdin": stdin_pipe,
                    "stdout": pipe_out,
                    "redirect": redirect,
                }
            )
        words = []
        stdin_pipe = pipe_out
        redirect = False

    for ch in command:
        if quote:
            started = True
            quoted = True
            if escaped:
                escaped = False
                text += ch
            elif quote == '"' and ch == "\\":
                escaped = True
            elif ch == quote:
                quote = ""
            else:
                text += ch
            continue
        if ch in "'\"":
            quote = ch
            started = True
            quoted = True
            continue
        if ch in "><":
            end_word()
            redirect = True
            continue
        if ch == "|":
            end_segment(True)
            continue
        if ch in ";&\n\r()":
            end_segment(False)
            continue
        if ch in " \t":
            end_word()
            continue
        text += ch
        started = True
    end_segment(False)
    return out


def base(word):
    return word.replace("\\", "").split("/")[-1]


def command(segment):
    """The command a segment runs, with its arguments.

    Leading VAR=value prefixes, `sudo` and a path to the tool do not hide the
    command from a rule.
    """
    words = segment["words"]
    at = 0
    total = len(words)
    while at < total and not words[at][1] and VAR_PREFIX.match(words[at][0]):
        at += 1
    while at < total and not words[at][1] and base(words[at][0]) == "sudo":
        at += 1
        while at < total and not words[at][1] and SUDO_FLAG.match(words[at][0]):
            flag = words[at][0]
            at += 1
            if SUDO_VALUE.match(flag):
                at += 1
        while at < total and not words[at][1] and VAR_PREFIX.match(words[at][0]):
            at += 1
    if at >= total:
        return "", []
    first = words[at]
    if first[1]:
        return "", []
    return base(first[0]), [word[0] for word in words[at + 1 :]]


def in_place_edit(args):
    return any(arg.startswith("--in-place") or IN_PLACE.match(arg) for arg in args)


def deny_find_chains(line):
    """find running a command, or deleting, and a search tool feeding a grep."""
    listing = segments(line)
    for segment in listing:
        name, args = command(segment)
        if name == "find" and any(arg in FIND_CHAIN for arg in args):
            return True
    for at, segment in enumerate(listing):
        if not segment["stdout"] or command(segment)[0] not in SEARCH_SOURCE:
            continue
        nxt = at + 1
        while nxt < len(listing) and listing[nxt]["stdin"]:
            name, args = command(listing[nxt])
            if name == "xargs":
                sink = any(base(arg) in SEARCH_SINK or arg == "find" for arg in args)
            else:
                sink = name in SEARCH_SINK
            if sink:
                return True
            if not listing[nxt]["stdout"]:
                break
            nxt += 1
    return False


def deny_find(line):
    """find over a tree is a file search; a find reading a pipe formats stdin."""
    return any(not segment["stdin"] and command(segment)[0] == "find" for segment in segments(line))


def deny_sed_inplace(line):
    for segment in segments(line):
        name, args = command(segment)
        if name in ("sed", "gsed") and in_place_edit(args):
            return True
        if name == "perl" and any(IN_PLACE.match(arg) for arg in args):
            return True
    return False


def plain_files(args):
    """cat takes no counted flags, so every other word names a file."""
    return [arg for arg in args if arg != "-" and not arg.startswith("-")]


def count_files(args):
    out = []
    at = 0
    while at < len(args):
        arg = args[at]
        if arg in ("-n", "-c", "-s", "--lines", "--bytes"):
            at += 2
            continue
        if arg.startswith("-") or arg == "-":
            at += 1
            continue
        out.append(arg)
        at += 1
    return out


def sed_files(args):
    """The file words of a sed, which follow its script."""
    out = []
    scripted = False
    at = 0
    while at < len(args):
        arg = args[at]
        if arg.startswith("-") and arg != "-":
            if arg in ("-e", "-f", "--expression", "--file"):
                scripted = True
                at += 1
            at += 1
            continue
        if not scripted:
            scripted = True
            at += 1
            continue
        out.append(arg)
        at += 1
    return out


def sed_prints(args):
    if any(arg == "-n" or re.match(r"^-[A-Za-z]*n$", arg) for arg in args):
        return True
    return any(SED_PRINT.match(arg) for arg in args)


def search_targets(args):
    """The file and directory arguments of a grep, which follow its pattern."""
    out = []
    patterned = False
    at = 0
    while at < len(args):
        arg = args[at]
        if arg.startswith("-") and arg != "-":
            if arg in GREP_VALUE:
                at += 1
            at += 1
            continue
        if not patterned:
            patterned = True
            at += 1
            continue
        out.append(arg)
        at += 1
    return out


def search_pattern(args):
    """The pattern of a grep: the -e/--regexp value, else its first non-flag
    word."""
    at = 0
    total = len(args)
    while at < total:
        arg = args[at]
        if arg in ("-e", "--regexp"):
            return args[at + 1] if at + 1 < total else ""
        if arg.startswith("-") and arg != "-":
            if arg in GREP_VALUE:
                at += 1
            at += 1
            continue
        return arg
    return ""


def branches(pattern):
    """Alternation branches, ignoring an escaped pipe."""
    count = 1
    at = 0
    total = len(pattern)
    while at < total:
        if pattern[at] == "\\":
            at += 2
            continue
        if pattern[at] == "|":
            count += 1
        at += 1
    return count


def wildcard_between_words(pattern):
    """.* or .+ with words on both sides: the model is describing a gap."""
    for marker in (".*", ".+"):
        at = pattern.find(marker)
        if at > 0 and at + len(marker) < len(pattern):
            return True
    return False


def pattern_kind(pattern):
    """Three kinds by shape alone, the same in the pi guard: concept is a broad
    query, symbol one identifier, text anything else (a string, a route, a path,
    a short regex with simple anchors only)."""
    trimmed = pattern.strip()
    if not trimmed:
        return "text"
    words = trimmed.split()
    if branches(trimmed) >= 3:
        return "concept"
    if wildcard_between_words(trimmed):
        return "concept"
    if len(words) >= 4:
        return "concept"
    if "?" in trimmed or words[0].lower() in QUESTION_WORDS:
        return "concept"
    if CHAR_CLASS.search(trimmed):
        return "concept"
    if IDENTIFIER_SHAPE.match(trimmed):
        return "symbol"
    return "text"


def search_dir(args):
    """The directory a grep searches, which the text line scopes with --glob: a
    file names its own directory, and no target or `.` searches the root."""
    targets = search_targets(args)
    if not targets:
        return ""
    first = targets[0].rstrip("/")
    if not first or first == ".":
        return ""
    parts = first.split("/")
    if "." in parts[-1]:
        return "/".join(parts[:-1])
    return first


def shell_quoted(text):
    """One shell word, safe to paste into a command."""
    return "'" + text.replace("'", "'\\''") + "'"


def concept_words(pattern):
    """The concept phrase: the model's own words with the regex dropped."""
    cleaned = ESCAPE.sub(" ", pattern)
    cleaned = WILDCARD.sub(" ", cleaned)
    cleaned = CONCEPT_STRIP.sub(" ", cleaned)
    return LEADING_CODE_THAT.sub("", " ".join(cleaned.split()))


def pattern_hint(segment):
    """The one line a pattern hint adds: the ctx query that fits what was typed.
    Empty when the grep has no pattern to build it from."""
    name, args = command(segment)
    if name == "git":
        args = args[1:]
    pattern = search_pattern(args)
    if not pattern:
        return ""
    kind = pattern_kind(pattern)
    if kind == "symbol":
        return f"Use orch ctx sym {pattern} --refs 3."
    words = concept_words(pattern)
    if kind == "concept" and words:
        phrase = words.replace('"', "'")
        return (
            f'Use orch ctx find "Code that {phrase}" '
            "(a broader search; use it when no name or string is known)."
        )
    where = search_dir(args)
    if where:
        return f"Use orch ctx text {shell_quoted(pattern)} --glob {shell_quoted(where + '/**')}."
    return f"Use orch ctx text {shell_quoted(pattern)}."


def grep_searches(segment, tools):
    """One segment is a raw content search over files."""
    name, args = command(segment)
    if not name:
        return False
    if name == "git":
        return bool(args) and args[0] == "grep" and not segment["stdin"]
    if name == "rg" and not tools["orch"]:
        return False
    if name not in PLAIN_SEARCH:
        return False
    if search_targets(args):
        return True
    recurses = name in RECURSIVE_BY_DEFAULT or any(
        arg.startswith("--recursive") or RECURSIVE.match(arg) for arg in args
    )
    return recurses and not segment["stdin"]


def grep_segment(line, tools):
    """The grep segment a block names, so the hint is built from the model's own
    pattern and not from a generic example."""
    for segment in segments(line):
        if grep_searches(segment, tools):
            return segment
    return None


def grep_kind(line, tools):
    """The kind of the blocked grep's pattern, for the log."""
    segment = grep_segment(line, tools)
    if segment is None:
        return ""
    name, args = command(segment)
    if name == "git":
        args = args[1:]
    pattern = search_pattern(args)
    return pattern_kind(pattern) if pattern else ""


def grep_tuned(line, tools):
    """The one line a deny-grep-rg block adds in pattern mode."""
    segment = grep_segment(line, tools)
    return pattern_hint(segment) if segment else ""


def deny_grep_rg(line, tools):
    """A grep with a file or directory argument, or one that recurses, searches
    files. One reading stdin is a filter. With no orch, rg is the preferred grep
    rather than a raw search, so rg itself stays allowed."""
    return any(grep_searches(segment, tools) for segment in segments(line))


def hint_mode():
    """ORCH_TOOL_GUARD_HINT=pattern appends one line built from the blocked
    pattern, so the two messages can be A/B tested. Unset or generic keeps the
    generic text."""
    return "pattern" if os.environ.get("ORCH_TOOL_GUARD_HINT") == "pattern" else "generic"


def guard_log(rule, hint, kind, line):
    """ORCH_TOOL_GUARD_LOG=<path> appends one JSON line per block, for the A/B
    count. Off by default, and a guard never fails a call because of it."""
    path = os.environ.get("ORCH_TOOL_GUARD_LOG")
    if not path:
        return
    entry = {
        "ts": datetime.now(timezone.utc).isoformat(),
        "harness": "claude",
        "rule": rule,
        "hint": hint,
        "kind": kind,
        "command": line[:200],
    }
    try:
        with open(path, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(entry) + "\n")
    except OSError:
        pass


def deny_cat_head_tail(line):
    for segment in segments(line):
        if segment["stdin"] or segment["stdout"] or segment["redirect"]:
            continue
        name, args = command(segment)
        if name == "cat":
            if len(plain_files(args)) == 1:
                return True
            continue
        if name in ("head", "tail"):
            if any(FOLLOW.match(arg) or arg.startswith("--follow") for arg in args):
                continue
            if len(count_files(args)) == 1:
                return True
            continue
        if name == "sed":
            if in_place_edit(args):
                continue
            if sed_prints(args) and len(sed_files(args)) == 1:
                return True
    return False


def file_search_reason(tools, with_symbol, none_text):
    """The line a find rule names: orch ctx, then fd, then the built-in Glob."""
    if tools["orch"]:
        queries = 'orch ctx batch -q "find:Code that ..."'
        if with_symbol:
            queries += ' -q "symbol:Name refs=3"'
        queries += ' -q "text:pattern glob=dir/**"'
        return f"One orch ctx batch replaces it: {queries} (--compact shortens it)."
    if tools["fd"]:
        return "Use fd instead (e.g. fd -e ts pattern dir, and fd -x cmd replaces find -exec)."
    return none_text


def rules():
    return [
        {
            "script": "find-chains",
            "name": "deny-find-chains",
            "id": "find-chains",
            "match": lambda line, tools: deny_find_chains(line),
            "reason": lambda tools: (
                f"{GUARD_MARKER} (deny-find-chains): find -exec, -delete and find | grep chains are blocked.\n"
                + file_search_reason(
                    tools,
                    True,
                    "Use the built-in Glob and Grep tools; orch ctx and fd are not installed.",
                )
            ),
        },
        {
            "script": "find-chains",
            "name": "deny-find",
            "id": "find",
            "match": lambda line, tools: deny_find(line),
            "reason": lambda tools: (
                f"{GUARD_MARKER} (deny-find): find over a tree is blocked.\n"
                + file_search_reason(tools, False, "Use the built-in Glob tool; orch ctx and fd are not installed.")
            ),
        },
        {
            "script": "sed-inplace",
            "name": "deny-sed-inplace",
            "id": "sed-inplace",
            "match": lambda line, tools: deny_sed_inplace(line),
            "reason": lambda tools: (
                f"{GUARD_MARKER} (deny-sed-inplace): sed -i and perl -pi rewrite a file in place.\n"
                "Use the built-in Edit tool; orch ctx is read-only and never replaces an edit."
            ),
        },
        {
            "script": "grep-rg",
            "name": "deny-grep-rg",
            "id": "grep-rg",
            "match": deny_grep_rg,
            "tuned": grep_tuned,
            "reason": lambda tools: (
                f"{GUARD_MARKER} (deny-grep-rg): a plain grep"
                + (", rg" if tools["orch"] else "")
                + ", ag or git grep over files is blocked.\n"
                + (
                    'One orch ctx batch replaces it: orch ctx batch -q "text:pattern glob=dir/**" '
                    '-q "symbol:Name refs=3" (a concept: find:...).'
                    if tools["orch"]
                    else "Use rg instead (e.g. rg -n pattern dir)."
                    if tools["rg"]
                    else "Use the built-in Grep tool; orch ctx and rg are not installed."
                )
            ),
        },
        {
            "script": "cat-head-tail",
            "name": "deny-cat-head-tail",
            "id": "cat-head-tail",
            "match": lambda line, tools: deny_cat_head_tail(line),
            "reason": lambda tools: (
                f"{GUARD_MARKER} (deny-cat-head-tail): a bare cat, head, tail or sed -n floods the context.\n"
                "Use the built-in Read tool with offset and limit (a section or the whole file)."
            ),
        },
    ]


def active_rules(script):
    """ORCH_TOOL_GUARD=off disables every rule; ORCH_TOOL_GUARD_SKIP disables
    single rules by short name or by full name."""
    if os.environ.get("ORCH_TOOL_GUARD") == "off":
        return []
    skip = {name.strip() for name in (os.environ.get("ORCH_TOOL_GUARD_SKIP") or "").split(",") if name.strip()}
    return [
        rule
        for rule in rules()
        if rule["script"] == script and rule["id"] not in skip and rule["name"] not in skip
    ]


def main(argv):
    script = argv[1] if len(argv) > 1 else ""
    try:
        event = json.load(sys.stdin)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return 0
    if not isinstance(event, dict):
        return 0
    tool = event.get("tool_name")
    if tool is not None and tool != "Bash":
        return 0
    tool_input = event.get("tool_input")
    if not isinstance(tool_input, dict):
        return 0
    raw = tool_input.get("command")
    if not isinstance(raw, str):
        return 0
    line = raw.strip()
    if not line:
        return 0
    active = active_rules(script)
    if not active:
        return 0
    tools = installed()
    for rule in active:
        if rule["match"](line, tools):
            mode = hint_mode()
            # The pattern hint is one extra line, and only when the reason can
            # name the tool it points at.
            tuned = ""
            if mode == "pattern" and tools["orch"] and rule.get("tuned"):
                tuned = rule["tuned"](line, tools)
            kind = grep_kind(line, tools) if rule["id"] == "grep-rg" else ""
            guard_log(rule["id"], mode, kind, line)
            reason = rule["reason"](tools) + ("\n" + tuned if tuned else "")
            sys.stderr.write(reason + "\n")
            return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
