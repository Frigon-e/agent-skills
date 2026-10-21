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

ROOT = os.path.dirname(os.path.abspath(__file__))
CASES = os.path.join(ROOT, "toolguard_cases.json")
GUARD_MARKER = "orch tool guard"

SCRIPTS = [
    "deny-find-chains.sh",
    "deny-sed-inplace.sh",
    "deny-cat-head-tail.sh",
    "deny-grep-rg.sh",
]

# Which script owns a rule. Plain find shares deny-find-chains.sh.
HOOK_FOR_RULE = {
    "find-chains": "deny-find-chains.sh",
    "find": "deny-find-chains.sh",
    "sed-inplace": "deny-sed-inplace.sh",
    "cat-head-tail": "deny-cat-head-tail.sh",
    "grep-rg": "deny-grep-rg.sh",
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


# The line a kind renders, and the generic reason that must survive byte for
# byte when the pattern hint is off.
KIND_MARKERS = {"symbol": "orch ctx sym", "text": "orch ctx text", "concept": "orch ctx find"}

GENERIC_ORCH_GREP = (
    "orch tool guard (deny-grep-rg): a plain grep, rg, ag or git grep over files is blocked.\n"
    'One orch ctx batch replaces it: orch ctx batch -q "text:pattern glob=dir/**" '
    '-q "symbol:Name refs=3" (a concept: find:...).'
)

# The three examples the task names, with the whole tailored line they must add.
TAILORED = [
    (
        'grep -rn "refund.*failed" src',
        'Use orch ctx find "Code that refund failed" '
        "(a broader search; use it when no name or string is known).",
    ),
    ("grep -rn Normalize src", "Use orch ctx sym Normalize --refs 3."),
    (
        'rg "cannot find user" internal/',
        "Use orch ctx text 'cannot find user' --glob 'internal/**'.",
    ),
]


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


def check_pattern_hints(checker, kinds, workdir):
    """The classifier table and the one line ORCH_TOOL_GUARD_HINT=pattern adds.

    Every patternKinds case runs through the real deny-grep-rg hook with a stub
    orch on PATH, so pattern extraction, the kind and the line it renders are
    checked together. Returns the failures and the count per kind.
    """
    failures = []
    counts = {"symbol": 0, "text": 0, "concept": 0}
    pattern = {"ORCH_TOOL_GUARD_HINT": "pattern"}
    for case in kinds:
        command = f"grep -rn '{case['pattern']}' src"
        label = f"pattern kind {case['pattern']!r}"
        wanted = case["kind"]
        if wanted not in KIND_MARKERS:
            failures.append(f"{label}: unknown kind {wanted!r}")
            continue
        done = grep_hook(checker, command, "orch", pattern)
        if done.returncode != 2:
            failures.append(f"{label}: exit {done.returncode}, want 2: {done.stderr.strip()}")
            continue
        counts[wanted] += 1
        reason = done.stderr.strip()
        for kind, marker in KIND_MARKERS.items():
            if (marker in reason) != (kind == wanted):
                failures.append(f"{label}: must render the {wanted} line and no other: {reason}")
                break
        lines = [line for line in reason.splitlines() if line.strip()]
        if len(lines) != 3:
            failures.append(f"{label}: the tailored line must be one extra line, got {len(lines)}: {reason}")
    # At least eight cases per kind, so neither implementation can be right by
    # accident on a thin table.
    for wanted, count in counts.items():
        if count < 8:
            failures.append(f"only {count} classification cases for {wanted}, want 8 or more")

    # The three named examples, whole line.
    for command, line in TAILORED:
        done = grep_hook(checker, command, "orch", pattern)
        reason = done.stderr.strip()
        if line not in reason.splitlines():
            failures.append(f"{command}: missing the tailored line {line!r}: {reason}")

    # generic and unset keep today's text byte for byte.
    for extra in ({"ORCH_TOOL_GUARD_HINT": "generic"}, {}):
        done = grep_hook(checker, "grep -rn Normalize src", "orch", extra)
        label = "generic" if extra else "unset"
        if done.returncode != 2 or done.stderr.strip() != GENERIC_ORCH_GREP:
            failures.append(f"{label}: the reason is not the old text byte for byte: {done.stderr.strip()!r}")
    # Without orch, the pattern hint has nothing to name, so both modes agree.
    for situation in ("fd-rg", "none"):
        for extra in (pattern, {}):
            done = grep_hook(checker, "grep -rn Normalize src", situation, extra)
            lines = [line for line in done.stderr.strip().splitlines() if line.strip()]
            if done.returncode != 2 or len(lines) != 2:
                failures.append(f"{situation}: a pattern hint without orch added a line: {done.stderr.strip()!r}")
            if any(marker in done.stderr for marker in KIND_MARKERS.values()):
                failures.append(f"{situation}: named orch ctx without orch on PATH: {done.stderr.strip()!r}")

    # The A/B log: one JSON line per block, off unless asked for.
    log = os.path.join(workdir, "guard.log")
    long_command = "grep -rn " + "A" * 300 + " src"
    for command in ("grep -rn Normalize src", long_command):
        grep_hook(checker, command, "orch", dict(pattern, ORCH_TOOL_GUARD_LOG=log))
    # An allowed call writes nothing.
    grep_hook(checker, "orch ctx text Normalize", "orch", dict(pattern, ORCH_TOOL_GUARD_LOG=log))
    try:
        with open(log, encoding="utf-8") as handle:
            entries = [json.loads(line) for line in handle if line.strip()]
    except OSError as error:
        failures.append(f"the log was not written: {error}")
        entries = []
    if len(entries) != 2:
        failures.append(f"the log must hold one line per block, got {len(entries)}")
    for entry in entries:
        if sorted(entry) != ["command", "harness", "hint", "kind", "rule", "ts"]:
            failures.append(f"the log line has the wrong fields: {sorted(entry)}")
        if entry.get("harness") != "claude" or entry.get("rule") != "grep-rg":
            failures.append(f"the log line names the wrong block: {entry}")
        if entry.get("hint") != "pattern" or entry.get("kind") != "symbol":
            failures.append(f"the log line names the wrong hint or kind: {entry}")
        if len(entry.get("command", "")) > 200:
            failures.append("the log command is not cut at 200 chars")
    return failures, counts


class Checker:
    def __init__(self, cases, situations, paths):
        self.cases = cases
        self.situations = situations
        self.paths = paths
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
        env = {"PATH": self.paths[situation], "HOME": os.environ.get("HOME", "")}
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


def stubs(directory, names):
    os.makedirs(directory, exist_ok=True)
    for name in names:
        path = os.path.join(directory, name)
        with open(path, "w", encoding="utf-8") as handle:
            handle.write("#!/bin/sh\nexit 0\n")
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


def main():
    with open(CASES, encoding="utf-8") as handle:
        data = json.load(handle)
    cases = data["cases"]
    situations = data["pathSituations"]

    workdir = tempfile.mkdtemp(prefix="toolguard-")
    kind_counts = {}
    try:
        support = os.path.join(workdir, "support")
        support_bin(support)
        paths = {}
        for situation, spec in situations.items():
            stubs(os.path.join(workdir, situation), spec["tools"])
            paths[situation] = os.path.join(workdir, situation) + os.pathsep + support

        checker = Checker(cases, situations, paths)
        failures = []
        failures.extend(shell_checks())
        failures.extend(required_cases(cases))
        checker.test()
        failures.extend(checker.failures)
        failures.extend(check_switches(checker))
        failures.extend(check_non_bash(checker))
        kind_failures, kind_counts = check_pattern_hints(checker, data["patternKinds"], workdir)
        failures.extend(kind_failures)
    finally:
        shutil.rmtree(workdir, ignore_errors=True)

    for failure in failures:
        print(f"FAIL {failure}", file=sys.stderr)
    print(
        f"{len(cases)} cases: {checker.blocked} blocked, {checker.allowed} allowed; "
        f"{len(SCRIPTS)} scripts checked with bash -n"
        + (" and shellcheck" if shutil.which("shellcheck") else "")
        + "; pattern kinds: "
        + ", ".join(f"{kind} {count}" for kind, count in kind_counts.items())
        + f"; {len(failures)} failure(s)"
    )
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
