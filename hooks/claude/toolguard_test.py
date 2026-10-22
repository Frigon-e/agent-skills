#!/usr/bin/env python3
"""Runs every case in toolguard_cases.json through the real Claude hooks.

The hooks are shell scripts, so this drives them as Claude Code does: a
PreToolUse JSON event on stdin, exit 0 to allow, exit 2 with the reason on
stderr to block. PATH holds only stubs for the tools a situation names, so the
absence of orch is real and not an accident of the machine.

Run: python3 hooks/claude/toolguard_test.py
"""

import contextlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

ROOT = os.path.dirname(os.path.abspath(__file__))
CASES = os.path.join(ROOT, "toolguard_cases.json")
GUARD_MARKER = "orch tool guard"

# What the broken orch stub prints, so a test can look for it in a block.
BROKEN_DETAIL = "cd: /nonexistent/tool: No such file or directory"
BROKEN_LINE = (
    f"orch ctx is installed but broken here ({BROKEN_DETAIL}); "
    "use the tools above and tell the user."
)

SCRIPTS = [
    "deny-find-chains.sh",
    "deny-sed-inplace.sh",
    "deny-cat-head-tail.sh",
    "deny-grep-rg.sh",
    "deny-builtin-search.sh",
    "deny-big-read.sh",
]

# Which script owns a rule. Plain find shares deny-find-chains.sh, and the two
# rules for Claude's own search tools share deny-builtin-search.sh.
HOOK_FOR_RULE = {
    "find-chains": "deny-find-chains.sh",
    "find": "deny-find-chains.sh",
    "sed-inplace": "deny-sed-inplace.sh",
    "cat-head-tail": "deny-cat-head-tail.sh",
    "grep-rg": "deny-grep-rg.sh",
    "builtin-grep": "deny-builtin-search.sh",
    "builtin-glob": "deny-builtin-search.sh",
}

# The built-in search hook: a PreToolUse event for one of Claude's own search
# tools, with the harness-neutral tool name of the shared table.
BUILTIN_HOOK = "deny-builtin-search.sh"
BIG_READ_HOOK = "deny-big-read.sh"
BUILTIN_HOOK_TOOL = {"content-search": "Grep", "file-search": "Glob"}
BUILTIN_REPLACEMENT = {"builtin-grep": "orch ctx batch", "builtin-glob": "orch ctx batch"}

# The two built-in reasons, byte for byte.
GENERIC_BUILTIN = {
    "Grep": (
        "orch tool guard (deny-builtin-grep): the built-in Grep tool is blocked; "
        "orch ctx answers searches here.\n"
        'One orch ctx batch replaces it: orch ctx batch -q "text:pattern glob=dir/**" '
        '-q "symbol:Name refs=3" (a concept: find:...).'
    ),
    "Glob": (
        "orch tool guard (deny-builtin-glob): the built-in Glob tool is blocked; "
        "orch ctx answers file searches here.\n"
        'One orch ctx batch replaces it: orch ctx batch -q "find:Code that ..." '
        '-q "text:pattern glob=dir/**" (--compact shortens it).'
    ),
}
# The replacement a block must name, given the PATH situation and the rule.
REPLACEMENT = {
    "orch": {
        "find-chains": "orch ctx batch",
        "find": "orch ctx batch",
        "sed-inplace": "built-in Edit",
        "cat-head-tail": "built-in Read",
        "grep-rg": "orch ctx batch",
    },
    "orch broken": {
        "find-chains": "fd instead",
        "find": "fd instead",
        "sed-inplace": "built-in Edit",
        "cat-head-tail": "built-in Read",
        "grep-rg": "rg instead",
    },
    "fd-rg": {
        "find-chains": "fd instead",
        "find": "fd instead",
        "sed-inplace": "built-in Edit",
        "cat-head-tail": "built-in Read",
        "grep-rg": "rg instead",
    },
    "none": {
        "find-chains": "built-in Glob",
        "find": "built-in Glob",
        "sed-inplace": "built-in Edit",
        "cat-head-tail": "built-in Read",
        "grep-rg": "built-in Grep",
    },
}

# Every example the task requires, with the decision it must get. Presence is
# checked here; the shared case file is what both harness tests execute.
REQUIRED = [
    ("find . -name '*.log' -exec rm {} \\;", "orch", "find-chains"),
    ("find . -name '*.ts' | grep -v node_modules", "orch", "find-chains"),
    ("find . -type f | xargs grep -l TODO", "orch", "find-chains"),
    ("sed -i 's/foo/bar/' file.txt", "orch", "sed-inplace"),
    ("sed -i.bak 's/foo/bar/' file.txt", "orch", "sed-inplace"),
    ("sed -i '' 's/foo/bar/' file.txt", "orch", "sed-inplace"),
    ("gsed -i 's/foo/bar/' file.txt", "orch", "sed-inplace"),
    ("sed --in-place 's/foo/bar/' file.txt", "orch", "sed-inplace"),
    ("cat notes.txt", "orch", "cat-head-tail"),
    ("head -n 20 notes.txt", "orch", "cat-head-tail"),
    ("tail -c 100 notes.txt", "orch", "cat-head-tail"),
    ("sudo cat /etc/hosts", "orch", "cat-head-tail"),
    ("sed 's/foo/bar/' file.txt", "orch", ""),
    ("git diff | sed 's/^/  /'", "orch", ""),
    ("tail -f /var/log/app.log", "orch", ""),
    ("cat file1 file2", "orch", ""),
    ("cat notes.txt | grep TODO", "orch", ""),
    ("head -n 5 file | wc -l", "orch", ""),
    ("cat notes.txt > copy.txt", "orch", ""),
    ('grep -rn "findUser" src', "orch", "grep-rg"),
    ("ls my-find-exec-project/", "orch", ""),
    ("grep -rn x src", "orch", "grep-rg"),
    ("rg Foo internal/", "orch", "grep-rg"),
    ("go test ./... | grep FAIL", "orch", ""),
]


# The deny-grep-rg reason, byte for byte.
GENERIC_ORCH_GREP = (
    "orch tool guard (deny-grep-rg): a plain grep, rg, ag or git grep over files is blocked.\n"
    'One orch ctx batch replaces it: orch ctx batch -q "text:pattern glob=dir/**" '
    '-q "symbol:Name refs=3" (a concept: find:...).'
)

def repository_root(start):
    """The git repository a path is in, by walking up, or ""."""
    path = os.path.abspath(start)
    while True:
        if os.path.exists(os.path.join(path, ".git")):
            return path
        parent = os.path.dirname(path)
        if parent == path:
            return ""
        path = parent


# The hook decides whether a path is inside the repository from its working
# directory, which Claude Code sets to the project. The tests say so explicitly
# so the absolute-path cases do not depend on where they were started from.
REPO_ROOT = repository_root(ROOT)


def builtin_hook(checker, tool, pattern, path, situation, extra=None, cwd=None):
    """One deny-builtin-search hook run, the way Claude Code drives it."""
    tool_input = {"pattern": pattern}
    if path:
        tool_input["path"] = path
    payload = json.dumps(
        {"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": tool_input}
    )
    return subprocess.run(
        [os.path.join(ROOT, BUILTIN_HOOK)],
        input=payload,
        text=True,
        capture_output=True,
        env=checker.env(situation, extra),
        cwd=cwd or REPO_ROOT or ROOT,
    )


def grep_hook(checker, command, situation, extra):
    """One grep-rg hook run, the way Claude Code drives it."""
    payload = json.dumps(
        {
            "hook_event_name": "PreToolUse",
            "tool_name": "Bash",
            "tool_input": {"command": command},
        }
    )
    return subprocess.run(
        [os.path.join(ROOT, "deny-grep-rg.sh")],
        input=payload,
        text=True,
        capture_output=True,
        env=checker.env(situation, extra),
    )


def check_generic_reasons(checker):
    """The deny-grep-rg reason, byte for byte, with orch, and without it."""
    failures = []
    done = grep_hook(checker, "grep -rn Normalize src", "orch", {})
    if done.returncode != 2 or done.stderr.strip() != GENERIC_ORCH_GREP:
        failures.append(f"the reason is not the fixed text byte for byte: {done.stderr.strip()!r}")
    for situation in ("fd-rg", "none"):
        done = grep_hook(checker, "grep -rn Normalize src", situation, {})
        if done.returncode != 2 or "orch ctx batch" in done.stderr:
            failures.append(f"{situation}: named orch ctx without orch on PATH: {done.stderr.strip()!r}")
    return failures


def check_builtin_search(checker, workdir, cases):
    """The built-in search hook: the shared table, the two reasons byte for byte,
    the switches, the repository boundary and the fail-open cases. Returns the
    failures and the shared table's counts."""
    failures = []
    blocked = allowed = 0

    # The shared table: blocked while orch answers and the path is inside the
    # repository, allowed otherwise, and no retry escape on a block.
    for case in cases:
        tool = BUILTIN_HOOK_TOOL.get(case["tool"])
        if tool is None:
            failures.append(f"{case['name']}: unknown tool {case['tool']!r}")
            continue
        rule = (case.get("rule") or {}).get("claude", "")
        if not rule:
            allowed += 1
            done = builtin_hook(checker, tool, case["pattern"], case["path"], case["situation"])
            if done.returncode != 0 or done.stderr.strip():
                failures.append(f"{case['name']}: exit {done.returncode}, want 0: {done.stderr.strip()}")
            continue
        blocked += 1
        for attempt in ("first", "retry"):
            done = builtin_hook(checker, tool, case["pattern"], case["path"], case["situation"])
            label = case["name"] + (" (retry)" if attempt == "retry" else "")
            if done.returncode != 2:
                failures.append(f"{label}: exit {done.returncode}, want 2: {done.stderr.strip()}")
                continue
            reason = done.stderr.strip()
            if f"(deny-{rule})" not in reason:
                failures.append(f"{label}: reason does not name deny-{rule}: {reason}")
            if GUARD_MARKER not in reason:
                failures.append(f"{label}: reason does not name the guard: {reason}")
            if BUILTIN_REPLACEMENT[rule] not in reason:
                failures.append(f"{label}: reason must name {BUILTIN_REPLACEMENT[rule]!r}: {reason}")
            entries = [entry for entry in reason.splitlines() if entry.strip()]
            if len(entries) != 2:
                failures.append(f"{label}: reason is {len(entries)} lines, want 2: {reason}")
            if done.stdout:
                failures.append(f"{label}: block wrote to stdout: {done.stdout.strip()}")

    # The reason is the fixed text byte for byte.
    for tool, sample in (("Grep", "Gateway"), ("Glob", "*.go")):
        done = builtin_hook(checker, tool, sample, "internal", "orch")
        if done.returncode != 2 or done.stderr.strip() != GENERIC_BUILTIN[tool]:
            failures.append(
                f"{tool}: the reason is not the fixed text byte for byte: {done.stderr.strip()!r}"
            )

    # The switches: ORCH_TOOL_GUARD=off, the group name, and one rule's name.
    for extra, label in (
        ({"ORCH_TOOL_GUARD": "off"}, "ORCH_TOOL_GUARD=off"),
        ({"ORCH_TOOL_GUARD_SKIP": "builtin-search"}, "the builtin-search skip"),
    ):
        for tool, sample in (("Grep", "Gateway"), ("Glob", "*.go")):
            done = builtin_hook(checker, tool, sample, "internal", "orch", extra)
            if done.returncode != 0 or done.stderr.strip():
                failures.append(f"{label} did not disable {tool}: {done.stderr.strip()}")
    done = builtin_hook(checker, "Grep", "Gateway", "internal", "orch", {"ORCH_TOOL_GUARD_SKIP": "builtin-grep"})
    if done.returncode != 0:
        failures.append(f"skipping builtin-grep by short name did not work: {done.stderr.strip()}")
    done = builtin_hook(checker, "Glob", "*.go", "internal", "orch", {"ORCH_TOOL_GUARD_SKIP": "builtin-grep"})
    if done.returncode != 2 or "(deny-builtin-glob)" not in done.stderr:
        failures.append("skipping builtin-grep also disabled the glob rule")

    # The repository boundary: absolute inside is blocked, absolute outside is
    # allowed. The shared table carries the outside paths; this adds the inside
    # one, which needs this checkout's own root.
    if REPO_ROOT:
        inside = os.path.join(REPO_ROOT, "notes.md")
        for tool, sample in (("Grep", "Gateway"), ("Glob", "*.go")):
            done = builtin_hook(checker, tool, sample, inside, "orch")
            if done.returncode != 2:
                failures.append(f"{tool} {inside}: inside the repository but allowed: {done.stderr.strip()}")
    for outside in ("/private/var/folders/xy/T/orchestrate/notes.md", "/opt/orchestrate/notes.md"):
        for tool, sample in (("Grep", "Gateway"), ("Glob", "*.go")):
            done = builtin_hook(checker, tool, sample, outside, "orch")
            if done.returncode != 0 or done.stderr.strip():
                failures.append(f"{tool} {outside}: outside the repository but blocked: {done.stderr.strip()}")

    # Relative paths use the session directory, including a session outside git.
    repo = os.path.join(workdir, "boundary-repo")
    nested = os.path.join(repo, "nested")
    os.makedirs(nested)
    subprocess.run(["git", "init", "-q", repo], check=True, capture_output=True)
    os.symlink(workdir, os.path.join(repo, "outside-link"))
    inside_alias = os.path.join(workdir, "inside-link")
    os.symlink(repo, inside_alias)
    for cwd, target, want in (
        (nested, "", 2),
        (nested, "../inside", 2),
        (nested, "../../outside", 0),
        (nested, "../outside-link", 0),
        (nested, inside_alias, 2),
        (workdir, "", 0),
        (workdir, ".", 0),
        (workdir, "relative", 0),
    ):
        for tool, sample in (("Grep", "Gateway"), ("Glob", "*.go")):
            done = builtin_hook(checker, tool, sample, target, "orch", cwd=cwd)
            if done.returncode != want or (want == 0 and done.stderr.strip()):
                failures.append(f"{tool} {target!r} from {cwd}: exit {done.returncode}, want {want}: {done.stderr.strip()}")

    # Fail open on anything but a Grep or Glob call that carries a pattern.
    payloads = [
        ("no pattern", {"tool_name": "Grep", "tool_input": {"path": "internal"}}),
        ("empty pattern", {"tool_name": "Glob", "tool_input": {"pattern": "  "}}),
        ("another tool", {"tool_name": "Read", "tool_input": {"pattern": "Gateway"}}),
        ("a Bash call", {"tool_name": "Bash", "tool_input": {"command": "grep -rn Gateway internal"}}),
        ("a broken tool_input", {"tool_name": "Grep", "tool_input": "Gateway"}),
    ]
    for name, payload in payloads:
        done = subprocess.run(
            [os.path.join(ROOT, BUILTIN_HOOK)],
            input=json.dumps(payload),
            text=True,
            capture_output=True,
            env=checker.env("orch"),
            cwd=REPO_ROOT or ROOT,
        )
        if done.returncode != 0 or done.stderr.strip():
            failures.append(f"{name}: did not allow cleanly: {done.stderr.strip()}")
    return failures, blocked, allowed


def read_hook(checker, payload, situation="orch", extra=None):
    """One deny-big-read hook run, the way Claude Code drives it."""
    return subprocess.run(
        [os.path.join(ROOT, BIG_READ_HOOK)],
        input=payload if isinstance(payload, str) else json.dumps(payload),
        text=True,
        capture_output=True,
        env=checker.env(situation, extra),
    )


def check_big_read(checker, workdir, cases):
    """The big-read hook: the shared table, the reason, the switches and the
    fail-open payloads. Returns the failures and the table's counts."""
    failures = []
    blocked = allowed = 0
    files = os.path.join(workdir, "big-read-files")
    os.makedirs(files)

    def event(path, extra=None):
        tool_input = {"file_path": path}
        tool_input.update(extra or {})
        return {"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": tool_input}

    big = os.path.join(files, "big.txt")
    for case in cases:
        path = os.path.join(files, case["file"])
        kind = case.get("kind", "file")
        if kind == "dir":
            os.makedirs(path, exist_ok=True)
        elif kind == "file" and not os.path.exists(path):
            with open(path, "wb") as handle:
                handle.write(b"x" * case["bytes"])
        payload = event(path, case["input"])
        if not case["block"]:
            allowed += 1
            done = read_hook(checker, payload, case["situation"])
            if done.returncode != 0 or done.stderr.strip():
                failures.append(f"{case['name']}: exit {done.returncode}, want 0: {done.stderr.strip()}")
            continue
        blocked += 1
        for attempt in ("first", "retry"):
            label = case["name"] + (" (retry)" if attempt == "retry" else "")
            done = read_hook(checker, payload, case["situation"])
            if done.returncode != 2:
                failures.append(f"{label}: exit {done.returncode}, want 2: {done.stderr.strip()}")
                continue
            reason = done.stderr.strip()
            lines = [entry for entry in reason.splitlines() if entry.strip()]
            if "(deny-big-read)" not in reason or GUARD_MARKER not in reason:
                failures.append(f"{label}: reason does not name the rule and the guard: {reason}")
            if not 2 <= len(lines) <= 5:
                failures.append(f"{label}: reason is {len(lines)} lines, want 2 to 5: {reason}")
            if f"{os.path.getsize(path) / 1024:.0f} KB" not in reason:
                failures.append(f"{label}: reason does not name the size in KB: {reason}")
            if "offset and limit" not in reason:
                failures.append(f"{label}: reason does not name offset and limit: {reason}")
            has_ctx = "orch ctx" in reason
            if (case["situation"] == "orch") != has_ctx:
                failures.append(f"{label}: orch ctx named wrongly for {case['situation']}: {reason}")
            if case["situation"] != "orch" and "rg -n" not in reason:
                failures.append(f"{label}: reason must name rg -n without a healthy orch: {reason}")
            if done.stdout:
                failures.append(f"{label}: block wrote to stdout: {done.stdout.strip()}")

    # A retry with a limit passes where the bare read was blocked.
    if read_hook(checker, event(big)).returncode != 2 or read_hook(checker, event(big, {"limit": 100})).returncode != 0:
        failures.append("the bare read was not blocked, or the retry with limit was not allowed")

    # The switches: ORCH_TOOL_GUARD=off, the short and the full rule name.
    for extra, label in (
        ({"ORCH_TOOL_GUARD": "off"}, "ORCH_TOOL_GUARD=off"),
        ({"ORCH_TOOL_GUARD_SKIP": "big-read"}, "the big-read skip"),
        ({"ORCH_TOOL_GUARD_SKIP": "deny-big-read"}, "the deny-big-read skip"),
        ({"ORCH_TOOL_GUARD_SKIP": "grep-rg, big-read"}, "a skip list"),
    ):
        done = read_hook(checker, event(big), "orch", extra)
        if done.returncode != 0 or done.stderr.strip():
            failures.append(f"{label} did not disable the rule: {done.stderr.strip()}")
    done = read_hook(checker, event(big), "orch", {"ORCH_TOOL_GUARD_SKIP": "builtin-search,grep-rg"})
    if done.returncode != 2:
        failures.append("another rule's skip also disabled big-read")

    # Fail open on anything but a Read of a path.
    payloads = [
        ("broken JSON", "not json"),
        ("a JSON list", "[]"),
        ("another tool", {"tool_name": "Grep", "tool_input": {"file_path": big}}),
        ("no tool name", {"tool_input": {"file_path": big}}),
        ("no tool input", {"tool_name": "Read"}),
        ("a broken tool_input", {"tool_name": "Read", "tool_input": big}),
        ("no file_path", {"tool_name": "Read", "tool_input": {}}),
        ("an empty file_path", {"tool_name": "Read", "tool_input": {"file_path": " "}}),
        ("a number as file_path", {"tool_name": "Read", "tool_input": {"file_path": 5}}),
        ("a NUL in file_path", {"tool_name": "Read", "tool_input": {"file_path": big + "\0"}}),
    ]
    for name, payload in payloads:
        done = read_hook(checker, payload)
        if done.returncode != 0 or done.stderr.strip():
            failures.append(f"{name}: did not allow cleanly: {done.stderr.strip()}")
    return failures, blocked, allowed


class Checker:
    def __init__(self, cases, situations, paths, state_root):
        self.cases = cases
        self.situations = situations
        self.paths = paths
        self.state_root = state_root
        self.blocked = 0
        self.allowed = 0
        self.failures = []

    def fail(self, case, message):
        self.failures.append(f"{case}: {message}")

    def run_case(self, case, hook):
        payload = json.dumps(
            {
                "hook_event_name": "PreToolUse",
                "tool_name": "Bash",
                "tool_input": {"command": case["command"]},
            }
        )
        return subprocess.run(
            [os.path.join(ROOT, hook)],
            input=payload,
            text=True,
            capture_output=True,
            env=self.env(case["situation"]),
        )

    def env(self, situation, extra=None):
        # A probe cache per situation, so a healthy orch, a broken one and no
        # orch at all never share one answer. Tests never touch the real one.
        state = os.path.join(self.state_root, situation)
        env = {
            "PATH": self.paths[situation],
            "HOME": os.environ.get("HOME", ""),
            "ORCH_TOOL_GUARD_STATE_DIR": state,
        }
        env.update(extra or {})
        return env

    def test(self):
        for case in self.cases:
            rule = case["rule"]
            if rule:
                self.blocked += 1
                self.expect_block(case, rule)
            else:
                self.allowed += 1
                self.expect_allow(case)

    def expect_block(self, case, rule):
        hook = HOOK_FOR_RULE.get(rule)
        if hook is None:
            self.fail(case["name"], f"unknown rule {rule!r}")
            return
        first = self.run_case(case, hook)
        # No retry escape: the same command is blocked every time.
        second = self.run_case(case, hook)
        for attempt in (first, second):
            label = case["name"] + " (retry)" if attempt is second else case["name"]
            if attempt.returncode != 2:
                self.fail(label, f"exit {attempt.returncode}, want 2: {attempt.stderr.strip()}")
                continue
            reason = attempt.stderr.strip()
            if f"(deny-{rule})" not in reason:
                self.fail(label, f"reason does not name deny-{rule}: {reason}")
            if GUARD_MARKER not in reason:
                self.fail(label, f"reason does not name the guard: {reason}")
            lines = [line for line in reason.splitlines() if line.strip()]
            if not 2 <= len(lines) <= 3:
                self.fail(label, f"reason is {len(lines)} lines, want 2 or 3: {reason}")
            want = REPLACEMENT[case["situation"]][rule]
            if want not in reason:
                self.fail(label, f"reason must name {want!r}: {reason}")
            if attempt.stdout:
                self.fail(label, f"block wrote to stdout: {attempt.stdout.strip()}")

    def expect_allow(self, case):
        for hook in SCRIPTS:
            done = self.run_case(case, hook)
            if done.returncode != 0:
                self.fail(
                    case["name"],
                    f"{hook} exited {done.returncode}: {done.stderr.strip()}",
                )
            elif done.stderr.strip():
                self.fail(case["name"], f"{hook} wrote to stderr: {done.stderr.strip()}")


def stubs(directory, names, broken=()):
    """A stand-in executable per name. A broken one writes an error to stderr,
    records the probe when ORCH_PROBE_LOG is set, and exits 1, which is what a
    wrapper that cannot find its tool directory does."""
    os.makedirs(directory, exist_ok=True)
    for name in names:
        path = os.path.join(directory, name)
        if name in broken:
            body = (
                "#!/bin/sh\n"
                'if [ -n "$ORCH_PROBE_LOG" ]; then echo probe >> "$ORCH_PROBE_LOG"; fi\n'
                f'echo "{BROKEN_DETAIL}" >&2\n'
                "exit 1\n"
            )
        else:
            body = "#!/bin/sh\nexit 0\n"
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(body)
        os.chmod(path, 0o700)


def support_bin(directory):
    """A PATH with only what the shell wrappers themselves need."""
    os.makedirs(directory, exist_ok=True)
    for name in ("bash", "python3", "dirname"):
        found = shutil.which(name)
        if not found:
            raise SystemExit(f"toolguard_test: {name} is not installed")
        link = os.path.join(directory, name)
        with contextlib.suppress(FileExistsError):
            os.symlink(found, link)


def shell_checks():
    failures = []
    bash = shutil.which("bash")
    shellcheck = shutil.which("shellcheck")
    for script in SCRIPTS:
        path = os.path.join(ROOT, script)
        if not os.access(path, os.X_OK):
            failures.append(f"{script}: not executable")
        if bash:
            done = subprocess.run([bash, "-n", path], text=True, capture_output=True)
            if done.returncode != 0:
                failures.append(f"{script}: bash -n: {done.stderr.strip()}")
        if shellcheck:
            done = subprocess.run(
                [shellcheck, "-x", path], text=True, capture_output=True
            )
            if done.returncode != 0:
                failures.append(f"{script}: shellcheck: {done.stdout.strip()}")
    return failures


def required_cases(cases):
    found = {(case["command"], case["situation"]): case["rule"] for case in cases}
    failures = []
    for command, situation, rule in REQUIRED:
        key = (command, situation)
        if key not in found:
            failures.append(f"required case {command!r} ({situation}) is missing")
        elif found[key] != rule:
            failures.append(
                f"required case {command!r} ({situation}) has rule {found[key]!r}, want {rule!r}"
            )
    return failures


def check_switches(checker):
    """ORCH_TOOL_GUARD=off and ORCH_TOOL_GUARD_SKIP are the only escapes."""
    failures = []
    sample = {
        "find-chains": ("deny-find-chains.sh", "fd -e go | grep Gateway"),
        "find": ("deny-find-chains.sh", "find . -name '*.ts'"),
        "sed-inplace": ("deny-sed-inplace.sh", "sed -i 's/a/b/' file.txt"),
        "cat-head-tail": ("deny-cat-head-tail.sh", "cat notes.txt"),
        "grep-rg": ("deny-grep-rg.sh", "grep -rn x src"),
    }

    def run(rule, situation, extra):
        hook, command = sample[rule]
        payload = json.dumps(
            {
                "hook_event_name": "PreToolUse",
                "tool_name": "Bash",
                "tool_input": {"command": command},
            }
        )
        return subprocess.run(
            [os.path.join(ROOT, hook)],
            input=payload,
            text=True,
            capture_output=True,
            env=checker.env(situation, extra),
        )

    for rule in sample:
        done = run(rule, "orch", {"ORCH_TOOL_GUARD": "off"})
        if done.returncode != 0:
            failures.append(f"ORCH_TOOL_GUARD=off did not disable {rule}: {done.stderr.strip()}")

    skip_four = "find-chains,sed-inplace,cat-head-tail,grep-rg"
    for rule in skip_four.split(","):
        done = run(rule, "orch", {"ORCH_TOOL_GUARD_SKIP": skip_four})
        if done.returncode != 0:
            failures.append(f"the four-name skip did not disable {rule}: {done.stderr.strip()}")
    # Plain find is a separate rule id, so the four-name skip leaves it on.
    done = run("find", "orch", {"ORCH_TOOL_GUARD_SKIP": skip_four})
    if done.returncode != 2 or "(deny-find)" not in done.stderr:
        failures.append("the four-name skip disabled plain find, which is its own rule")

    done = run("find", "orch", {"ORCH_TOOL_GUARD_SKIP": "find-chains"})
    if done.returncode != 2 or "(deny-find)" not in done.stderr:
        failures.append("skipping find-chains also disabled plain find")
    done = run("find-chains", "orch", {"ORCH_TOOL_GUARD_SKIP": "find-chains"})
    if done.returncode != 0:
        failures.append("skipping find-chains did not disable it")
    done = run("find", "orch", {"ORCH_TOOL_GUARD_SKIP": "find"})
    if done.returncode != 0:
        failures.append("skipping find did not disable plain find")
    done = run("cat-head-tail", "orch", {"ORCH_TOOL_GUARD_SKIP": "deny-cat-head-tail"})
    if done.returncode != 0:
        failures.append("skipping deny-cat-head-tail by full name did not work")
    return failures


def check_non_bash(checker):
    """A non-Bash event, a missing command and broken JSON must all allow."""
    failures = []
    payloads = [
        ("non-Bash tool", {"tool_name": "Read", "tool_input": {"command": "grep -rn x src"}}),
        ("missing command", {"tool_name": "Bash", "tool_input": {}}),
        ("empty command", {"tool_name": "Bash", "tool_input": {"command": "  "}}),
    ]
    for script in SCRIPTS:
        for name, payload in payloads:
            done = subprocess.run(
                [os.path.join(ROOT, script)],
                input=json.dumps(payload),
                text=True,
                capture_output=True,
                env=checker.env("orch"),
            )
            if done.returncode != 0 or done.stderr.strip():
                failures.append(f"{script}: {name} did not allow cleanly: {done.stderr.strip()}")
        done = subprocess.run(
            [os.path.join(ROOT, script)],
            input="not json",
            text=True,
            capture_output=True,
            env=checker.env("orch"),
        )
        if done.returncode != 0 or done.stderr.strip():
            failures.append(f"{script}: broken stdin did not allow cleanly: {done.stderr.strip()}")
    return failures


def probe_count(path):
    """How many probes the broken stub has recorded."""
    try:
        with open(path, encoding="utf-8") as handle:
            return len([line for line in handle if line.strip()])
    except OSError:
        return 0


def check_orch_health(checker, workdir):
    """A broken orch is not a replacement: a search block says so, rg stays
    allowed, and the probe is cached for a minute."""
    failures = []
    probes = os.path.join(workdir, "broken-probes.log")
    # A state directory of its own, so the cases above cannot have warmed it.
    state = os.path.join(workdir, "health-state")
    cache = os.path.join(state, "toolguard-health.json")
    extra = {
        "ORCH_PROBE_LOG": probes,
        "ORCH_TOOL_GUARD_STATE_DIR": state,
    }

    # Two blocked greps: both name rg and the broken line, and the cache means
    # one probe served both calls.
    for attempt in ("first", "second"):
        done = grep_hook(checker, "grep -rn x src", "orch broken", extra)
        reason = done.stderr.strip()
        if done.returncode != 2:
            failures.append(f"broken orch {attempt} grep: exit {done.returncode}, want 2: {reason}")
            continue
        if BROKEN_LINE not in reason.splitlines():
            failures.append(f"broken orch {attempt} grep: no broken line: {reason!r}")
        if "orch ctx batch" in reason:
            failures.append(f"broken orch {attempt} grep: named orch ctx: {reason!r}")
        if ", rg, ag" in reason:
            failures.append(f"broken orch {attempt} grep: named rg as the preferred grep: {reason!r}")
        if "rg instead" not in reason:
            failures.append(f"broken orch {attempt} grep: did not name rg: {reason!r}")
    if probe_count(probes) != 1:
        failures.append(f"two calls within the minute probed {probe_count(probes)} times, want 1")

    # rg is the preferred grep when orch cannot answer, so it is not blocked.
    done = grep_hook(checker, "rg Foo internal/", "orch broken", extra)
    if done.returncode != 0 or done.stderr.strip():
        failures.append(f"rg was not allowed with a broken orch: {done.stderr.strip()}")

    # A stale cache is re-probed, so a wrapper fixed a minute ago works again.
    os.makedirs(state, exist_ok=True)
    with open(cache, "w", encoding="utf-8") as handle:
        json.dump({"ts": time.time() - 120, "ok": True, "detail": ""}, handle)
    done = grep_hook(checker, "grep -rn x src", "orch broken", extra)
    if BROKEN_LINE not in done.stderr:
        failures.append("a stale cache was trusted instead of re-probed")
    if probe_count(probes) != 2:
        failures.append(f"a stale cache probed {probe_count(probes)} times in all, want 2")

    # A fresh cache is trusted: no probe, and the healthy text comes back.
    with open(cache, "w", encoding="utf-8") as handle:
        json.dump({"ts": time.time(), "ok": True, "detail": ""}, handle)
    done = grep_hook(checker, "grep -rn x src", "orch broken", extra)
    reason = done.stderr.strip()
    if "orch ctx batch" not in reason or "broken here" in reason:
        failures.append(f"a fresh healthy cache was not trusted: {reason!r}")
    if probe_count(probes) != 2:
        failures.append("a fresh cache still probed")

    # The detail is the first line of the probe's error, cut at 120 characters.
    long_first = "x" * 200
    broken_dir = os.path.join(workdir, "long-broken")
    os.makedirs(broken_dir, exist_ok=True)
    stub = os.path.join(broken_dir, "orch")
    with open(stub, "w", encoding="utf-8") as handle:
        handle.write('#!/bin/sh\necho "' + long_first + '" >&2\necho "second line" >&2\nexit 1\n')
    os.chmod(stub, 0o700)
    long_env = {
        "ORCH_TOOL_GUARD_STATE_DIR": os.path.join(workdir, "long-state"),
        "PATH": broken_dir + os.pathsep + checker.paths["orch broken"],
    }
    done = grep_hook(checker, "grep -rn x src", "orch broken", long_env)
    reason = done.stderr.strip()
    if f"({long_first[:120]})" not in reason:
        failures.append(f"the detail is not the first line cut at 120 characters: {reason!r}")
    if "second line" in reason:
        failures.append(f"the detail is not one line: {reason!r}")
    return failures


def main():
    with open(CASES, encoding="utf-8") as handle:
        data = json.load(handle)
    cases = data["cases"]
    situations = data["pathSituations"]

    workdir = tempfile.mkdtemp(prefix="toolguard-")
    try:
        support = os.path.join(workdir, "support")
        support_bin(support)
        paths = {}
        for situation, spec in situations.items():
            stubs(os.path.join(workdir, situation), spec["tools"], spec.get("broken", ()))
            paths[situation] = os.path.join(workdir, situation) + os.pathsep + support

        state_root = os.path.join(workdir, "state")
        checker = Checker(cases, situations, paths, state_root)
        failures = []
        failures.extend(shell_checks())
        failures.extend(required_cases(cases))
        checker.test()
        failures.extend(checker.failures)
        failures.extend(check_switches(checker))
        failures.extend(check_non_bash(checker))
        failures.extend(check_orch_health(checker, workdir))
        builtin_failures, builtin_blocked, builtin_allowed = check_builtin_search(
            checker, workdir, data["builtinCases"]
        )
        failures.extend(builtin_failures)
        read_failures, read_blocked, read_allowed = check_big_read(checker, workdir, data["readCases"])
        failures.extend(read_failures)
        failures.extend(check_generic_reasons(checker))
    finally:
        shutil.rmtree(workdir, ignore_errors=True)

    for failure in failures:
        print(f"FAIL {failure}", file=sys.stderr)
    print(
        f"{len(cases)} cases: {checker.blocked} blocked, {checker.allowed} allowed; "
        f"{builtin_blocked + builtin_allowed} built-in cases: {builtin_blocked} blocked, "
        f"{builtin_allowed} allowed; "
        f"{read_blocked + read_allowed} read cases: {read_blocked} blocked, {read_allowed} allowed; "
        f"{len(SCRIPTS)} scripts checked with bash -n"
        + (" and shellcheck" if shutil.which("shellcheck") else "")
        + f"; {len(failures)} failure(s)"
    )
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
