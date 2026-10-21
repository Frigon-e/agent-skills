package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The extension decides in pi, so the decision tables run the real file in node
// (which strips its types) against a stand-in for pi's extension API.
type reminderCase struct {
	Name string `json:"name"`
	Tool string `json:"tool"`
	// Blocks is the rule that must block the call: "", find-chains, find,
	// sed-inplace, cat-head-tail or grep-rg.
	Blocks string   `json:"-"`
	Needs  []string `json:"-"` // substrings the block reason must carry
	Remind bool     `json:"-"` // the note is appended to the result
	// Repeat marks the 3rd block in a row, whose reason carries one extra line.
	Repeat bool           `json:"-"`
	Input  map[string]any `json:"input"`
}

type reminderRun struct {
	Handlers []string `json:"handlers"`
	Out      map[string]struct {
		Block string `json:"block"`
		Added string `json:"added"`
		Kept  string `json:"kept"`
	} `json:"out"`
}

const (
	reminderMarker = "orch ctx reminder"
	guardMarker    = "orch tool guard"
	// repeatedBlock is the one extra line a reason carries from the 3rd block
	// in a row on.
	repeatedBlock = "Blocked 3 times: use the replacement above; this guard does not give way."
)

// runCtxReminder runs a case group with only an `orch` stand-in on PATH.
func runCtxReminder(t *testing.T, env []string, cases []reminderCase) reminderRun {
	t.Helper()
	return runCtxReminderWith(t, []string{"orch"}, env, cases)
}

// runCtxReminderWith copies the real extension into a temp dir, puts a stub for
// each named tool on PATH and runs the cases through the loaded extension in
// node. PATH is only that temp dir, so the real system tools never leak in and
// the case group decides which replacements the guard can name.
func runCtxReminderWith(t *testing.T, onPath []string, env []string, cases []reminderCase) reminderRun {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	ext := filepath.Join(dir, "orch-ctx-reminder.ts")
	if err := os.WriteFile(ext, mustRead(t, "pi_ctx_reminder.ts"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range onPath {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `
import ext from ` + string(mustJSON(t, ext)) + `;
const handlers = {};
ext({ on: (name, fn) => {
  if (name === "tool_result" && process.env.NO_RESULT_HOOK) throw new Error("unsupported event");
  handlers[name] = fn;
} });
const out = {};
for (const c of JSON.parse(process.argv[1])) {
  const call = { type: "tool_call", toolCallId: "t1", toolName: c.tool, input: c.input };
  const blocked = handlers.tool_call ? await handlers.tool_call(call) : undefined;
  const raw = { type: "tool_result", toolCallId: "t1", toolName: c.tool, input: c.input, content: [{ type: "text", text: "ORIGINAL" }], isError: false };
  const res = handlers.tool_result ? await handlers.tool_result(raw) : undefined;
  out[c.name] = {
    block: blocked && blocked.block ? blocked.reason : "",
    added: res ? res.content.slice(1).map((p) => p.text).join("") : "",
    kept: res ? res.content[0].text : "ORIGINAL",
  };
}
console.log(JSON.stringify({ handlers: Object.keys(handlers).sort(), out }));
`
	cmd := exec.Command(node, "--input-type=module", "-e", script, string(mustJSON(t, cases)))
	cmd.Env = agentEnv(nil, append([]string{"PATH=" + bin, "ORCH_CTX_ONLY_READY="}, env...))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, raw)
	}
	var got reminderRun
	if err := json.Unmarshal(raw[strings.Index(string(raw), "{"):], &got); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	return got
}

func bash(command string) map[string]any { return map[string]any{"command": command} }

// checkReminder asserts the whole table: which calls are blocked, by which
// rule, with which replacement, and which get the note appended.
func checkReminder(t *testing.T, got reminderRun, cases []reminderCase) (reminded, quiet, blocked int) {
	t.Helper()
	for _, c := range cases {
		res, ok := got.Out[c.Name]
		if !ok {
			t.Fatalf("missing result for %q", c.Name)
		}
		if res.Kept != "ORIGINAL" {
			t.Errorf("%q lost the original result content: %q", c.Name, res.Kept)
		}
		if c.Blocks == "" {
			if res.Block != "" {
				t.Errorf("%q was blocked but should be allowed: %s", c.Name, res.Block)
			}
		} else {
			blocked++
			if want := "deny-" + c.Blocks; !strings.Contains(res.Block, want) {
				t.Errorf("%q must be blocked by %s: %s", c.Name, want, res.Block)
			}
			if !strings.Contains(res.Block, guardMarker) {
				t.Errorf("%q block reason does not name the guard: %q", c.Name, res.Block)
			}
			for _, need := range c.Needs {
				if !strings.Contains(res.Block, need) {
					t.Errorf("%q block reason must name %q: %q", c.Name, need, res.Block)
				}
			}
			// A reason is two or three short lines; the 3rd block in a row adds
			// the repeated-block line.
			limit := 3
			if strings.Contains(res.Block, repeatedBlock) {
				limit = 4
			}
			if lines := strings.Count(res.Block, "\n") + 1; lines > limit {
				t.Errorf("%q block reason is longer than %d lines: %q", c.Name, limit, res.Block)
			}
			if c.Repeat && !strings.Contains(res.Block, repeatedBlock) {
				t.Errorf("%q is the 3rd block in a row but the reason lacks %q: %q", c.Name, repeatedBlock, res.Block)
			}
		}
		// A blocked call never reaches its result hook in pi, so the note is
		// only checked on the calls that run.
		switch {
		case c.Blocks != "":
		case c.Remind:
			reminded++
			if !strings.Contains(res.Added, reminderMarker) || !strings.Contains(res.Added, "orch ctx batch") {
				t.Errorf("%q did not get the reminder: %q", c.Name, res.Added)
			}
			if strings.Count(res.Added, "\n") != 1 {
				t.Errorf("%q reminder is not two lines: %q", c.Name, res.Added)
			}
		default:
			quiet++
			if res.Added != "" {
				t.Errorf("%q got a reminder it should not have: %q", c.Name, res.Added)
			}
		}
	}
	return reminded, quiet, blocked
}

// The shape of every decision: whose the call is, and whether it is blocked.
func TestCtxReminderDecisionTable(t *testing.T) {
	orch := []string{"orch ctx batch"}
	cases := []reminderCase{
		// A plain grep or rg over files belongs to deny-grep-rg.
		{Name: "rg", Tool: "bash", Input: bash(`rg -n Gateway internal`), Blocks: "grep-rg", Needs: orch, Remind: true},
		{Name: "grep -r", Tool: "bash", Input: bash(`grep -rn Gateway internal`), Blocks: "grep-rg", Needs: orch, Remind: true},
		{Name: "egrep", Tool: "bash", Input: bash(`egrep -n Gateway internal`), Blocks: "grep-rg", Needs: orch, Remind: true},
		{Name: "fgrep", Tool: "bash", Input: bash(`fgrep -n Gateway internal`), Blocks: "grep-rg", Needs: orch, Remind: true},
		{Name: "ag", Tool: "bash", Input: bash(`ag Gateway internal`), Blocks: "grep-rg", Needs: orch, Remind: true},
		{Name: "absolute rg", Tool: "bash", Input: bash(`/usr/bin/rg -n Gateway .`), Blocks: "grep-rg", Needs: orch, Remind: true},
		{Name: "env prefix", Tool: "bash", Input: bash(`RG_OPTS=1 rg Gateway .`), Blocks: "grep-rg", Needs: orch, Remind: true},
		{Name: "cd && rg", Tool: "bash", Input: bash(`cd internal && rg -n Gateway .`), Blocks: "grep-rg", Needs: orch, Remind: true},
		{Name: "git grep", Tool: "bash", Input: bash(`git grep -n Gateway`), Blocks: "grep-rg", Needs: orch, Remind: true},
		// fd only gets the note; a plain find is now a file search to redirect,
		// and a grep fed by a pipe filters.
		{Name: "fd", Tool: "bash", Input: bash(`fd gateway`), Remind: true},
		{Name: "find", Tool: "bash", Input: bash(`find . -name "*.go"`), Blocks: "find", Needs: orch},
		{Name: "find by type", Tool: "bash", Input: bash(`find src -type f`), Blocks: "find", Needs: orch},
		{Name: "pipe find", Tool: "bash", Input: bash(`cat list.txt | find . -name "*.ts"`), Remind: true},
		{Name: "pipe grep", Tool: "bash", Input: bash(`cat file | grep -n Gateway`), Remind: true},
		{Name: "builtin grep", Tool: "grep", Input: map[string]any{"pattern": "Gateway"}, Remind: true},
		{Name: "builtin find", Tool: "find", Input: map[string]any{"pattern": "*.go"}, Remind: true},
		{Name: "builtin ls", Tool: "ls", Input: map[string]any{"path": "."}, Remind: true},
		// Everything else stays quiet.
		{Name: "orch ctx text", Tool: "bash", Input: bash(`orch ctx text Gateway`)},
		{Name: "orch ctx find", Tool: "bash", Input: bash(`orch ctx find "Code that saves a card"`)},
		{Name: "orch ctx batch quoted grep", Tool: "bash", Input: bash(`orch ctx batch -q "text:grep glob=internal/**"`)},
		{Name: "go test", Tool: "bash", Input: bash(`go test ./...`)},
		{Name: "plain ls", Tool: "bash", Input: bash(`ls`)},
		{Name: "echo quoted rg", Tool: "bash", Input: bash(`echo "rg"`)},
		{Name: "cat pipe head", Tool: "bash", Input: bash(`cat file | head -5`)},
		{Name: "read tool", Tool: "read", Input: map[string]any{"path": "a.go"}},
	}
	got := runCtxReminder(t, nil, cases)
	if len(got.Handlers) != 2 || got.Handlers[0] != "tool_call" || got.Handlers[1] != "tool_result" {
		t.Fatalf("expected the guard and the reminder hooks, got %v", got.Handlers)
	}
	reminded, quiet, blocked := checkReminder(t, got, cases)
	t.Logf("base table: %d reminded, %d quiet, %d blocked", reminded, quiet, blocked)
}

// deny-find-chains: find running a command, and a search tool feeding a grep.
func TestCtxReminderGuardDenyFindChains(t *testing.T) {
	orch := []string{"orch ctx batch"}
	cases := []reminderCase{
		// Blocks.
		{Name: "find -exec", Tool: "bash", Input: bash(`find . -name "*.go" -exec rm {} \;`), Blocks: "find-chains", Needs: orch},
		{Name: "find -delete", Tool: "bash", Input: bash(`find . -name "*.tmp" -delete`), Blocks: "find-chains", Needs: orch},
		{Name: "find | grep", Tool: "bash", Input: bash(`find . -name "*.go" | grep Gateway`), Blocks: "find-chains", Needs: orch},
		{Name: "find | xargs grep", Tool: "bash", Input: bash(`find . -type f | xargs grep -n Gateway`), Blocks: "find-chains", Needs: orch},
		{Name: "fd | grep", Tool: "bash", Input: bash(`fd -e go | grep Gateway`), Blocks: "find-chains", Needs: orch},
		{Name: "rg | grep", Tool: "bash", Input: bash(`rg -n Gateway . | grep -v test`), Blocks: "find-chains", Needs: orch},
		{Name: "ag | xargs grep", Tool: "bash", Input: bash(`ag Gateway | xargs grep -n foo`), Blocks: "find-chains", Needs: orch},
		{Name: "rg | xargs grep", Tool: "bash", Input: bash(`rg -n Gateway . | xargs grep foo`), Blocks: "find-chains", Needs: orch},
		{Name: "find | sort | grep", Tool: "bash", Input: bash(`find . -name "*.go" | sort | grep hand`), Blocks: "find-chains", Needs: orch},
		{Name: "sudo find -exec", Tool: "bash", Input: bash(`sudo find . -name core -exec rm {} \;`), Blocks: "find-chains", Needs: orch},
		{Name: "cd && find | grep", Tool: "bash", Input: bash(`cd internal && find . -name "*.go" | grep Gateway`), Blocks: "find-chains", Needs: orch},
		{Name: "/usr/bin/find -exec", Tool: "bash", Input: bash(`/usr/bin/find . -name x -exec ls {} \;`), Blocks: "find-chains", Needs: orch},
		// Does not touch: a path or a quoted word that merely contains find or
		// grep, and a pipeline that formats another command. Plain find now
		// belongs to deny-find, which blocks it.
		{Name: "plain find", Tool: "bash", Input: bash(`find . -name "*.md" -type f`), Blocks: "find", Needs: orch},
		{Name: "plain find | head", Tool: "bash", Input: bash(`find . -name "*.go" | head -20`), Blocks: "find", Needs: orch},
		{Name: "path with find -exec", Tool: "bash", Input: bash(`ls my-find-exec-project/`)},
		{Name: "find -name quoted grep", Tool: "bash", Input: bash(`find . -name "grep*"`), Blocks: "find", Needs: orch},
		{Name: "pipe rg quoted find -exec", Tool: "bash", Input: bash(`cat file | rg -n "find -exec"`), Remind: true},
		{Name: "echo quoted find -exec", Tool: "bash", Input: bash(`echo "find -exec rm"`)},
	}
	got := runCtxReminder(t, nil, cases)
	reminded, quiet, blocked := checkReminder(t, got, cases)
	t.Logf("deny-find-chains: %d blocked, %d allowed, %d reminded", blocked, quiet, reminded)
}

// deny-find: find over a tree is a file search by name, path or type, so one
// ctx batch owns it; a find reading a pipe only formats stdin.
func TestCtxReminderGuardDenyFind(t *testing.T) {
	orch := []string{"orch ctx batch"}
	cases := []reminderCase{
		{Name: "find by name", Tool: "bash", Input: bash(`find . -name "*.go"`), Blocks: "find", Needs: orch},
		{Name: "find by type", Tool: "bash", Input: bash(`find src -type f`), Blocks: "find", Needs: orch},
		{Name: "find path and glob", Tool: "bash", Input: bash(`find internal -name "*.ts"`), Blocks: "find", Needs: orch},
		{Name: "bare find", Tool: "bash", Input: bash(`find`), Blocks: "find", Needs: orch},
		{Name: "sudo find", Tool: "bash", Input: bash(`sudo find /var -type f`), Blocks: "find", Needs: orch},
		{Name: "cd && find", Tool: "bash", Input: bash(`cd internal && find . -name "*.ts"`), Blocks: "find", Needs: orch},
		{Name: "absolute find", Tool: "bash", Input: bash(`/usr/bin/find . -name x`), Blocks: "find", Needs: orch},
		// find -exec is a chain, so the more specific rule names it.
		{Name: "find -exec", Tool: "bash", Input: bash(`find . -name x -exec ls {} \;`), Blocks: "find-chains", Needs: orch},
		// Does not touch: find reading a pipe, the find tool, fd and a quoted word.
		{Name: "find reads a pipe", Tool: "bash", Input: bash(`printf x | find . -name "*.ts"`), Remind: true},
		{Name: "builtin find", Tool: "find", Input: map[string]any{"pattern": "*.go"}, Remind: true},
		{Name: "fd", Tool: "bash", Input: bash(`fd -e ts gateway`), Remind: true},
		{Name: "echo quoted find", Tool: "bash", Input: bash(`echo "find"`)},
	}
	got := runCtxReminder(t, nil, cases)
	reminded, quiet, blocked := checkReminder(t, got, cases)
	t.Logf("deny-find: %d blocked, %d allowed, %d reminded", blocked, quiet, reminded)
}

// The replacement order: our ctx system first, then fd for a file search and rg
// for a content search, then pi's built-in tools. PATH holds only the stub
// executables of the case group, so the absence of fd and rg is real.
func TestCtxReminderReplacementOrder(t *testing.T) {
	batch := "orch ctx batch"
	t.Run("orch installed", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "find by name", Tool: "bash", Input: bash(`find . -name '*.ts'`), Blocks: "find", Needs: []string{batch, "text:pattern"}},
			{Name: "find by type", Tool: "bash", Input: bash(`find src -type f`), Blocks: "find", Needs: []string{batch}},
			{Name: "find in a pipe", Tool: "bash", Input: bash(`cat list.txt | find . -name '*.ts'`), Remind: true},
			{Name: "grep recursive", Tool: "bash", Input: bash(`grep -rn x src`), Blocks: "grep-rg", Needs: []string{batch, "text:pattern"}},
			{Name: "rg tree", Tool: "bash", Input: bash(`rg x src`), Blocks: "grep-rg", Needs: []string{batch}},
			{Name: "ag tree", Tool: "bash", Input: bash(`ag x src`), Blocks: "grep-rg", Needs: []string{batch}},
			{Name: "git grep", Tool: "bash", Input: bash(`git grep -n x`), Blocks: "grep-rg", Needs: []string{batch}},
		}
		got := runCtxReminderWith(t, []string{"orch", "fd", "rg"}, nil, cases)
		reminded, quiet, blocked := checkReminder(t, got, cases)
		t.Logf("orch installed: %d blocked, %d allowed, %d reminded", blocked, quiet, reminded)
		t.Run("find names no fd", func(t *testing.T) {
			if strings.Contains(got.Out["find by name"].Block, "fd") {
				t.Errorf("orch is installed but the reason named fd: %q", got.Out["find by name"].Block)
			}
		})
	})
	t.Run("no orch, fd and rg installed", func(t *testing.T) {
		fd := "fd -e ts pattern dir"
		rg := "rg -n pattern dir"
		cases := []reminderCase{
			{Name: "find by name", Tool: "bash", Input: bash(`find . -name '*.ts'`), Blocks: "find", Needs: []string{"fd instead", fd, "fd -x cmd"}},
			{Name: "find by type", Tool: "bash", Input: bash(`find src -type f`), Blocks: "find", Needs: []string{fd}},
			{Name: "find in a pipe", Tool: "bash", Input: bash(`cat list.txt | find . -name '*.ts'`)},
			{Name: "grep recursive", Tool: "bash", Input: bash(`grep -rn x src`), Blocks: "grep-rg", Needs: []string{"rg instead", rg}},
			{Name: "rg tree", Tool: "bash", Input: bash(`rg x src`)},
			{Name: "ag tree", Tool: "bash", Input: bash(`ag x src`), Blocks: "grep-rg", Needs: []string{rg}},
			{Name: "git grep", Tool: "bash", Input: bash(`git grep -n x`), Blocks: "grep-rg", Needs: []string{rg}},
		}
		got := runCtxReminderWith(t, []string{"fd", "rg"}, nil, cases)
		reminded, quiet, blocked := checkReminder(t, got, cases)
		t.Logf("no orch, fd+rg: %d blocked, %d allowed, %d reminded", blocked, quiet, reminded)
		for _, name := range []string{"find by name", "grep recursive", "ag tree", "git grep"} {
			if reason := got.Out[name].Block; strings.Contains(reason, batch) {
				t.Errorf("%q named orch ctx although orch is not on PATH: %q", name, reason)
			}
		}
	})
	t.Run("no orch, no fd, no rg", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "find by name", Tool: "bash", Input: bash(`find . -name '*.ts'`), Blocks: "find", Needs: []string{"built-in find"}},
			{Name: "find by type", Tool: "bash", Input: bash(`find src -type f`), Blocks: "find", Needs: []string{"built-in find"}},
			{Name: "find in a pipe", Tool: "bash", Input: bash(`cat list.txt | find . -name '*.ts'`)},
			{Name: "grep recursive", Tool: "bash", Input: bash(`grep -rn x src`), Blocks: "grep-rg", Needs: []string{"built-in grep"}},
			{Name: "rg tree", Tool: "bash", Input: bash(`rg x src`)},
			{Name: "ag tree", Tool: "bash", Input: bash(`ag x src`), Blocks: "grep-rg", Needs: []string{"built-in grep"}},
			{Name: "git grep", Tool: "bash", Input: bash(`git grep -n x`), Blocks: "grep-rg", Needs: []string{"built-in grep"}},
		}
		got := runCtxReminderWith(t, nil, nil, cases)
		reminded, quiet, blocked := checkReminder(t, got, cases)
		t.Logf("no orch, no fd, no rg: %d blocked, %d allowed, %d reminded", blocked, quiet, reminded)
		for _, name := range []string{"find by name", "grep recursive"} {
			if reason := got.Out[name].Block; strings.Contains(reason, batch) || strings.Contains(reason, "fd -e ts") || strings.Contains(reason, "rg -n") {
				t.Errorf("%q named a tool that is not on PATH: %q", name, reason)
			}
		}
	})
}

// deny-sed-inplace: sed -i and perl -pi rewrite a file, so the edit tool owns it.
func TestCtxReminderGuardDenySedInplace(t *testing.T) {
	edit := []string{"edit"}
	cases := []reminderCase{
		{Name: "sed -i", Tool: "bash", Input: bash(`sed -i 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "sed -i.bak", Tool: "bash", Input: bash(`sed -i.bak 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "sed -i BSD", Tool: "bash", Input: bash(`sed -i '' 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "gsed -i", Tool: "bash", Input: bash(`gsed -i 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "sed --in-place", Tool: "bash", Input: bash(`sed --in-place 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "perl -pi", Tool: "bash", Input: bash(`perl -pi -e 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "perl -i -pe", Tool: "bash", Input: bash(`perl -i -pe 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "absolute sed -i", Tool: "bash", Input: bash(`/usr/bin/sed -i 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "cd && sed -i", Tool: "bash", Input: bash(`cd internal && sed -i 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: edit},
		{Name: "sudo sed -i", Tool: "bash", Input: bash(`sudo sed -i 's/a/b/' /etc/hosts`), Blocks: "sed-inplace", Needs: edit},
		// Does not touch: sed that prints to stdout, a piped transform, grep -i.
		{Name: "sed print to stdout", Tool: "bash", Input: bash(`sed 's/a/b/' file.go`)},
		{Name: "piped sed transform", Tool: "bash", Input: bash(`git diff | sed 's/^/  /'`)},
		{Name: "grep -i", Tool: "bash", Input: bash(`grep -rn -i Gateway internal`), Blocks: "grep-rg", Needs: []string{"orch ctx batch"}},
	}
	got := runCtxReminder(t, nil, cases)
	reminded, quiet, blocked := checkReminder(t, got, cases)
	t.Logf("deny-sed-inplace: %d blocked, %d allowed, %d reminded", blocked, quiet, reminded)
}

// deny-cat-head-tail: one bare file read belongs to the read tool.
func TestCtxReminderGuardDenyCatHeadTail(t *testing.T) {
	read := []string{"read", "orch ctx sym"}
	cases := []reminderCase{
		{Name: "cat one file", Tool: "bash", Input: bash(`cat notes.txt`), Blocks: "cat-head-tail", Needs: read},
		{Name: "head -n 20", Tool: "bash", Input: bash(`head -n 20 notes.txt`), Blocks: "cat-head-tail", Needs: read},
		{Name: "tail -c 100", Tool: "bash", Input: bash(`tail -c 100 notes.txt`), Blocks: "cat-head-tail", Needs: read},
		{Name: "sed -n range", Tool: "bash", Input: bash(`sed -n '10,20p' notes.txt`), Blocks: "cat-head-tail", Needs: read},
		{Name: "sudo cat", Tool: "bash", Input: bash(`sudo cat /etc/hosts`), Blocks: "cat-head-tail", Needs: read},
		{Name: "cat -n one file", Tool: "bash", Input: bash(`cat -n notes.txt`), Blocks: "cat-head-tail", Needs: read},
		{Name: "absolute cat", Tool: "bash", Input: bash(`/bin/cat notes.txt`), Blocks: "cat-head-tail", Needs: read},
		{Name: "head bare", Tool: "bash", Input: bash(`head notes.txt`), Blocks: "cat-head-tail", Needs: read},
		// Does not touch: follow, several files, a pipe on either side, a
		// redirect, and reading from another command's output.
		{Name: "tail -f", Tool: "bash", Input: bash(`tail -f /var/log/app.log`)},
		{Name: "tail --follow", Tool: "bash", Input: bash(`tail --follow notes.txt`)},
		{Name: "cat a b", Tool: "bash", Input: bash(`cat a.go b.go`)},
		{Name: "cat f | grep TODO", Tool: "bash", Input: bash(`cat f.go | grep TODO`), Remind: true},
		{Name: "head | wc -l", Tool: "bash", Input: bash(`head -n 5 f.go | wc -l`)},
		{Name: "cat redirect copy", Tool: "bash", Input: bash(`cat f.go > copy.go`)},
		{Name: "cat heredoc", Tool: "bash", Input: bash(`cat > f.go <<EOF`)},
		{Name: "go test | tail", Tool: "bash", Input: bash(`go test ./... | tail -n 20`)},
		{Name: "git log | head", Tool: "bash", Input: bash(`git log | head`)},
		{Name: "cat stdin", Tool: "bash", Input: bash(`cat`)},
		{Name: "wc -l file", Tool: "bash", Input: bash(`wc -l notes.txt`)},
	}
	got := runCtxReminder(t, nil, cases)
	reminded, quiet, blocked := checkReminder(t, got, cases)
	t.Logf("deny-cat-head-tail: %d blocked, %d allowed, %d reminded", blocked, quiet, reminded)
}

// deny-grep-rg: a grep or rg over files is a search; one reading stdin filters.
func TestCtxReminderGuardDenyGrepRg(t *testing.T) {
	orch := []string{"orch ctx batch"}
	cases := []reminderCase{
		{Name: "grep dir", Tool: "bash", Input: bash(`grep -rn "findUser" src`), Blocks: "grep-rg", Needs: orch},
		{Name: "rg dir", Tool: "bash", Input: bash(`rg Foo internal/`), Blocks: "grep-rg", Needs: orch},
		{Name: "grep file", Tool: "bash", Input: bash(`grep -n Gateway internal/gateway.go`), Blocks: "grep-rg", Needs: orch},
		{Name: "grep -rn dot", Tool: "bash", Input: bash(`grep -rn Gateway .`), Blocks: "grep-rg", Needs: orch},
		{Name: "egrep dir", Tool: "bash", Input: bash(`egrep -n Gateway internal`), Blocks: "grep-rg", Needs: orch},
		{Name: "fgrep file", Tool: "bash", Input: bash(`fgrep -n Gateway internal/gateway.go`), Blocks: "grep-rg", Needs: orch},
		{Name: "ag dir", Tool: "bash", Input: bash(`ag Gateway internal`), Blocks: "grep-rg", Needs: orch},
		{Name: "git grep", Tool: "bash", Input: bash(`git grep -n Gateway`), Blocks: "grep-rg", Needs: orch},
		{Name: "grep -r no path", Tool: "bash", Input: bash(`grep -r Gateway`), Blocks: "grep-rg", Needs: orch},
		{Name: "rg no path", Tool: "bash", Input: bash(`rg -n Gateway`), Blocks: "grep-rg", Needs: orch},
		{Name: "sudo grep", Tool: "bash", Input: bash(`sudo grep -rn Gateway /etc`), Blocks: "grep-rg", Needs: orch},
		{Name: "cd && rg", Tool: "bash", Input: bash(`cd internal && rg -n Gateway .`), Blocks: "grep-rg", Needs: orch},
		{Name: "env prefix rg", Tool: "bash", Input: bash(`RG_OPTS=1 rg Gateway .`), Blocks: "grep-rg", Needs: orch},
		{Name: "absolute rg", Tool: "bash", Input: bash(`/usr/bin/rg -n Gateway .`), Blocks: "grep-rg", Needs: orch},
		{Name: "rg glob flag", Tool: "bash", Input: bash(`rg -g "*.go" Gateway`), Blocks: "grep-rg", Needs: orch},
		// Does not touch: a grep filtering piped input, grep -q as a condition,
		// a grep reading stdin, and a quoted word.
		{Name: "go test | grep FAIL", Tool: "bash", Input: bash(`go test ./... | grep FAIL`), Remind: true},
		{Name: "git log | grep fix", Tool: "bash", Input: bash(`git log | grep fix`), Remind: true},
		{Name: "ps aux | grep x", Tool: "bash", Input: bash(`ps aux | grep x`), Remind: true},
		{Name: "go test | grep -q", Tool: "bash", Input: bash(`go test ./... | grep -q FAIL`), Remind: true},
		{Name: "cat f | rg TODO", Tool: "bash", Input: bash(`cat f.go | rg TODO`), Remind: true},
		{Name: "grep stdin", Tool: "bash", Input: bash(`grep foo`), Remind: true},
		{Name: "echo quoted grep", Tool: "bash", Input: bash(`echo "grep"`)},
		{Name: "orch ctx text grep", Tool: "bash", Input: bash(`orch ctx text grep --glob internal/**`)},
	}
	got := runCtxReminder(t, nil, cases)
	reminded, quiet, blocked := checkReminder(t, got, cases)
	t.Logf("deny-grep-rg: %d blocked, %d allowed, %d reminded", blocked, quiet, reminded)
}

// Without orch on PATH every rule still blocks, pointing at the built-ins, and
// no note is appended because there is nothing to name. rg is the exception:
// with no orch it is the preferred grep, so it is never blocked.
func TestCtxReminderSilentInCtxOnlyShellAndGuardsWithoutOrch(t *testing.T) {
	t.Run("no orch on PATH", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "find | grep", Tool: "bash", Input: bash(`find . -name "*.go" | grep Gateway`), Blocks: "find-chains", Needs: []string{"grep", "find"}},
			{Name: "sed -i", Tool: "bash", Input: bash(`sed -i 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: []string{"edit"}},
			{Name: "cat one file", Tool: "bash", Input: bash(`cat notes.txt`), Blocks: "cat-head-tail", Needs: []string{"read", "offset"}},
			{Name: "rg Foo internal/", Tool: "bash", Input: bash(`rg Foo internal/`)},
			{Name: "find by name", Tool: "bash", Input: bash(`find . -name "*.ts"`), Blocks: "find", Needs: []string{"built-in find"}},
			{Name: "fd", Tool: "bash", Input: bash(`fd gateway`)},
			{Name: "go test | tail", Tool: "bash", Input: bash(`go test ./... | tail -n 20`)},
		}
		got := runCtxReminderWith(t, nil, nil, cases)
		if len(got.Handlers) != 1 || got.Handlers[0] != "tool_call" {
			t.Fatalf("without orch only the guard hook is expected, got %v", got.Handlers)
		}
		checkReminder(t, got, cases)
		if strings.Contains(got.Out["find | grep"].Block, "orch ctx batch") {
			t.Errorf("a block reason named orch ctx although orch is not on PATH: %q", got.Out["find | grep"].Block)
		}
	})
	t.Run("ctx-only gate shell", func(t *testing.T) {
		cases := []reminderCase{{Name: "rg", Tool: "bash", Input: bash(`rg -n Gateway .`)}}
		got := runCtxReminder(t, []string{"ORCH_CTX_ONLY_READY=/tmp/ready"}, cases)
		if len(got.Handlers) != 0 || got.Out["rg"].Added != "" || got.Out["rg"].Block != "" {
			t.Fatalf("extension acted inside the ctx-only shell: %v %+v", got.Handlers, got.Out["rg"])
		}
	})
}

// A blocked command stays blocked on every retry, so the model learns not to
// use it; the 3rd block in a row adds one line saying the guard will not give
// way, and an allowed call ends the run. ORCH_TOOL_GUARD=off or a skip by name
// are the human's escapes.
func TestCtxReminderLoopProtectionAndSwitches(t *testing.T) {
	orch := []string{"orch ctx batch"}
	t.Run("same command stays blocked", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "grep dir", Tool: "bash", Input: bash(`rg Foo internal/`), Blocks: "grep-rg", Needs: orch},
			{Name: "grep dir retry", Tool: "bash", Input: bash(`rg Foo internal/`), Blocks: "grep-rg", Needs: orch},
			{Name: "grep dir third", Tool: "bash", Input: bash(`rg Foo internal/`), Blocks: "grep-rg", Needs: orch, Repeat: true},
			{Name: "orch ctx call", Tool: "bash", Input: bash(`orch ctx text Gateway`)},
			{Name: "cat one file", Tool: "bash", Input: bash(`cat notes.txt`), Blocks: "cat-head-tail", Needs: []string{"read"}},
		}
		got := runCtxReminder(t, nil, cases)
		checkReminder(t, got, cases)
		if strings.Contains(got.Out["cat one file"].Block, repeatedBlock) {
			t.Errorf("an allowed call did not end the run of blocks: %q", got.Out["cat one file"].Block)
		}
	})
	t.Run("ORCH_TOOL_GUARD=off", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "find | grep", Tool: "bash", Input: bash(`find . -name "*.go" | grep Gateway`), Remind: true},
			{Name: "sed -i", Tool: "bash", Input: bash(`sed -i 's/a/b/' file.go`)},
			{Name: "cat one file", Tool: "bash", Input: bash(`cat notes.txt`)},
			{Name: "plain rg", Tool: "bash", Input: bash(`rg -n Gateway .`), Remind: true},
		}
		got := runCtxReminder(t, []string{"ORCH_TOOL_GUARD=off"}, cases)
		checkReminder(t, got, cases)
	})
	t.Run("skip grep-rg by short name", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "rg Foo internal/", Tool: "bash", Input: bash(`rg Foo internal/`), Remind: true},
			{Name: "cat one file", Tool: "bash", Input: bash(`cat notes.txt`), Blocks: "cat-head-tail", Needs: []string{"read"}},
		}
		got := runCtxReminder(t, []string{"ORCH_TOOL_GUARD_SKIP=grep-rg"}, cases)
		checkReminder(t, got, cases)
	})
	t.Run("skip cat-head-tail by full name", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "cat one file", Tool: "bash", Input: bash(`cat notes.txt`)},
			{Name: "sed -i", Tool: "bash", Input: bash(`sed -i 's/a/b/' file.go`), Blocks: "sed-inplace", Needs: []string{"edit"}},
		}
		got := runCtxReminder(t, []string{"ORCH_TOOL_GUARD_SKIP=deny-cat-head-tail"}, cases)
		checkReminder(t, got, cases)
	})
	t.Run("skip find by short name", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "find by name", Tool: "bash", Input: bash(`find . -name "*.ts"`), Remind: true},
			{Name: "cat one file", Tool: "bash", Input: bash(`cat notes.txt`), Blocks: "cat-head-tail", Needs: []string{"read"}},
		}
		got := runCtxReminder(t, []string{"ORCH_TOOL_GUARD_SKIP=find"}, cases)
		checkReminder(t, got, cases)
	})
	t.Run("skip find by full name", func(t *testing.T) {
		cases := []reminderCase{
			{Name: "find by name", Tool: "bash", Input: bash(`find . -name "*.ts"`), Remind: true},
			{Name: "rg Foo internal/", Tool: "bash", Input: bash(`rg Foo internal/`), Blocks: "grep-rg", Needs: []string{"orch ctx batch"}},
		}
		got := runCtxReminder(t, []string{"ORCH_TOOL_GUARD_SKIP=deny-find"}, cases)
		checkReminder(t, got, cases)
	})
}

// A pi build without the tool_result hook falls back to blocking the first
// search once, so the note still reaches the model, and the retry always runs.
func TestCtxReminderFallbackBlocksOnceThenAllowsRetry(t *testing.T) {
	cases := []reminderCase{
		{Name: "go test first", Tool: "bash", Input: bash(`go test ./...`)},
		{Name: "plain ls", Tool: "bash", Input: bash(`ls`)},
		{Name: "first rg", Tool: "bash", Input: bash(`rg -n Gateway .`), Blocks: "grep-rg", Needs: []string{"orch ctx batch"}},
		{Name: "same rg retry", Tool: "bash", Input: bash(`rg -n Gateway .`)},
		{Name: "first fd", Tool: "bash", Input: bash(`fd gateway`)},
		{Name: "same fd retry", Tool: "bash", Input: bash(`fd gateway`)},
		{Name: "later fd", Tool: "bash", Input: bash(`fd gateway.go`)},
	}
	got := runCtxReminder(t, []string{"NO_RESULT_HOOK=1"}, cases)
	if len(got.Handlers) != 1 || got.Handlers[0] != "tool_call" {
		t.Fatalf("fallback expected only a tool_call hook, got %v", got.Handlers)
	}
	blocked := 0
	for _, name := range []string{"go test first", "plain ls"} {
		if reason := got.Out[name].Block; reason != "" {
			t.Errorf("%q was blocked: %s", name, reason)
		}
	}
	first := got.Out["first rg"].Block
	if !strings.Contains(first, "deny-grep-rg") || !strings.Contains(first, "orch ctx batch") {
		t.Fatalf("the first search was not blocked by the guard: %q", first)
	}
	blocked++
	// A blocked command stays blocked, so the retry does not run.
	if retry := got.Out["same rg retry"].Block; !strings.Contains(retry, "deny-grep-rg") {
		t.Errorf("the retry of a blocked search was not blocked: %s", retry)
	}
	blocked++
	firstFD := got.Out["first fd"].Block
	if !strings.Contains(firstFD, reminderMarker) || !strings.Contains(firstFD, "orch ctx batch") {
		t.Fatalf("the fallback did not block the first search with the reminder: %q", firstFD)
	}
	blocked++
	for _, name := range []string{"same fd retry", "later fd"} {
		if reason := got.Out[name].Block; reason != "" {
			t.Errorf("%q was blocked after the one reminder: %s", name, reason)
		}
	}
	t.Logf("fallback: %d blocked, 4 allowed", blocked)
}

// sharedToolguardFile is one decision table for both harness guards. The Claude
// hooks in hooks/claude run every case through their own test; this runs the
// same file through the pi extension so the two implementations cannot drift.
// Rule names and replacement tools differ by harness, so only blocked vs
// allowed and the rule that blocks are compared.
type sharedToolguardFile struct {
	PathSituations map[string]struct {
		Tools []string `json:"tools"`
	} `json:"pathSituations"`
	Cases []struct {
		Name      string `json:"name"`
		Situation string `json:"situation"`
		Rule      string `json:"rule"`
		Command   string `json:"command"`
	} `json:"cases"`
}

func TestCtxReminderSharedToolguardCases(t *testing.T) {
	path := filepath.Join("..", "..", "..", "hooks", "claude", "toolguard_cases.json")
	var file sharedToolguardFile
	if err := json.Unmarshal(mustRead(t, path), &file); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if len(file.Cases) == 0 || len(file.PathSituations) == 0 {
		t.Fatalf("%s has no cases or no PATH situations", path)
	}
	for situation, spec := range file.PathSituations {
		t.Run(situation, func(t *testing.T) {
			var cases []reminderCase
			for _, c := range file.Cases {
				if c.Situation != situation {
					continue
				}
				cases = append(cases, reminderCase{Name: c.Name, Tool: "bash", Input: bash(c.Command)})
			}
			if len(cases) == 0 {
				t.Fatalf("no cases for PATH situation %q", situation)
			}
			got := runCtxReminderWith(t, spec.Tools, nil, cases)
			blocked := 0
			for _, c := range file.Cases {
				if c.Situation != situation {
					continue
				}
				reason := got.Out[c.Name].Block
				if c.Rule == "" {
					if reason != "" {
						t.Errorf("%q is allowed in the shared cases but pi blocked it: %s", c.Name, reason)
					}
					continue
				}
				blocked++
				// The parens keep deny-find from matching deny-find-chains.
				if want := "(deny-" + c.Rule + ")"; !strings.Contains(reason, want) {
					t.Errorf("%q must be blocked by %s in pi: %s", c.Name, want, reason)
				}
			}
			t.Logf("shared cases, %s PATH: %d cases, %d must block", situation, len(cases), blocked)
		})
	}
}
