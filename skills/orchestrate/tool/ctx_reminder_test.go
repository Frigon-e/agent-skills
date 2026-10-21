package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The extension decides in pi, so the decision table runs the real file in node
// (which strips its types) against a stand-in for pi's extension API.
type reminderCase struct {
	Name   string         `json:"name"`
	Tool   string         `json:"tool"`
	Input  map[string]any `json:"input"`
	Remind bool           `json:"-"`
}

type reminderRun struct {
	Handlers []string `json:"handlers"`
	Out      map[string]struct {
		Block string `json:"block"`
		Added string `json:"added"`
		Kept  string `json:"kept"`
	} `json:"out"`
}

const reminderMarker = "orch ctx reminder"

// runCtxReminder copies the real extension into a temp dir, puts an `orch`
// stand-in on PATH and runs the cases through the loaded extension in node.
func runCtxReminder(t *testing.T, env []string, cases []reminderCase) reminderRun {
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
	if err := os.WriteFile(filepath.Join(bin, "orch"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
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

func TestCtxReminderDecisionTable(t *testing.T) {
	bash := func(command string) map[string]any { return map[string]any{"command": command} }
	cases := []reminderCase{
		// Searches get the reminder.
		{Name: "rg", Tool: "bash", Input: bash(`rg -n Gateway internal`), Remind: true},
		{Name: "grep -r", Tool: "bash", Input: bash(`grep -rn Gateway internal`), Remind: true},
		{Name: "egrep", Tool: "bash", Input: bash(`egrep -n Gateway internal`), Remind: true},
		{Name: "fgrep", Tool: "bash", Input: bash(`fgrep -n Gateway internal`), Remind: true},
		{Name: "ag", Tool: "bash", Input: bash(`ag Gateway internal`), Remind: true},
		{Name: "fd", Tool: "bash", Input: bash(`fd gateway`), Remind: true},
		{Name: "find", Tool: "bash", Input: bash(`find . -name "*.go"`), Remind: true},
		{Name: "absolute rg", Tool: "bash", Input: bash(`/usr/bin/rg -n Gateway .`), Remind: true},
		{Name: "env prefix", Tool: "bash", Input: bash(`RG_OPTS=1 rg Gateway .`), Remind: true},
		{Name: "cd && rg", Tool: "bash", Input: bash(`cd internal && rg -n Gateway .`), Remind: true},
		{Name: "pipe grep", Tool: "bash", Input: bash(`cat file | grep -n Gateway`), Remind: true},
		{Name: "git grep", Tool: "bash", Input: bash(`git grep -n Gateway`), Remind: true},
		{Name: "builtin grep", Tool: "grep", Input: map[string]any{"pattern": "Gateway"}, Remind: true},
		{Name: "builtin find", Tool: "find", Input: map[string]any{"pattern": "*.go"}, Remind: true},
		{Name: "builtin ls", Tool: "ls", Input: map[string]any{"path": "."}, Remind: true},
		// Everything else stays quiet.
		{Name: "orch ctx text", Tool: "bash", Input: bash(`orch ctx text Gateway`), Remind: false},
		{Name: "orch ctx find", Tool: "bash", Input: bash(`orch ctx find "Code that saves a card"`), Remind: false},
		{Name: "orch ctx batch quoted grep", Tool: "bash", Input: bash(`orch ctx batch -q "text:grep glob=internal/**"`), Remind: false},
		{Name: "go test", Tool: "bash", Input: bash(`go test ./...`), Remind: false},
		{Name: "plain ls", Tool: "bash", Input: bash(`ls`), Remind: false},
		{Name: "echo quoted rg", Tool: "bash", Input: bash(`echo "rg"`), Remind: false},
		{Name: "cat pipe head", Tool: "bash", Input: bash(`cat file | head -5`), Remind: false},
		{Name: "read tool", Tool: "read", Input: map[string]any{"path": "a.go"}, Remind: false},
	}
	got := runCtxReminder(t, nil, cases)
	if len(got.Handlers) != 1 || got.Handlers[0] != "tool_result" {
		t.Fatalf("non-blocking reminder expected only a tool_result hook, got %v", got.Handlers)
	}
	reminded, quiet := 0, 0
	for _, c := range cases {
		res, ok := got.Out[c.Name]
		if !ok {
			t.Fatalf("missing result for %q", c.Name)
		}
		if res.Kept != "ORIGINAL" {
			t.Errorf("%q lost the original result content: %q", c.Name, res.Kept)
		}
		if res.Block != "" {
			t.Errorf("%q was blocked although the result hook exists: %s", c.Name, res.Block)
		}
		if c.Remind {
			reminded++
			if !strings.Contains(res.Added, reminderMarker) || !strings.Contains(res.Added, "orch ctx batch") {
				t.Errorf("%q did not get the reminder: %q", c.Name, res.Added)
			}
			if strings.Count(res.Added, "\n") != 1 {
				t.Errorf("%q reminder is not two lines: %q", c.Name, res.Added)
			}
		} else {
			quiet++
			if res.Added != "" {
				t.Errorf("%q got a reminder it should not have: %q", c.Name, res.Added)
			}
		}
	}
	t.Logf("decision table: %d reminded, %d quiet", reminded, quiet)
}

func TestCtxReminderFallbackBlocksOnceThenAllowsRetry(t *testing.T) {
	bash := func(command string) map[string]any { return map[string]any{"command": command} }
	cases := []reminderCase{
		{Name: "go test first", Tool: "bash", Input: bash(`go test ./...`)},
		{Name: "plain ls", Tool: "bash", Input: bash(`ls`)},
		{Name: "first rg", Tool: "bash", Input: bash(`rg -n Gateway .`)},
		{Name: "same rg retry", Tool: "bash", Input: bash(`rg -n Gateway .`)},
		{Name: "other search", Tool: "bash", Input: bash(`git grep -n Gateway`)},
	}
	got := runCtxReminder(t, []string{"NO_RESULT_HOOK=1"}, cases)
	if len(got.Handlers) != 1 || got.Handlers[0] != "tool_call" {
		t.Fatalf("fallback expected only a tool_call hook, got %v", got.Handlers)
	}
	if reason := got.Out["go test first"].Block; reason != "" {
		t.Errorf("a non-search was blocked: %s", reason)
	}
	if reason := got.Out["plain ls"].Block; reason != "" {
		t.Errorf("plain ls was blocked: %s", reason)
	}
	first := got.Out["first rg"].Block
	if !strings.Contains(first, reminderMarker) || !strings.Contains(first, "orch ctx batch") {
		t.Fatalf("the first search was not blocked with the reminder: %q", first)
	}
	for _, name := range []string{"same rg retry", "other search"} {
		if reason := got.Out[name].Block; reason != "" {
			t.Errorf("%q was blocked after the one reminder: %s", name, reason)
		}
	}
}

func TestCtxReminderInactiveWithoutOrchOrInCtxOnlyShell(t *testing.T) {
	bash := func(command string) map[string]any { return map[string]any{"command": command} }
	cases := []reminderCase{{Name: "rg", Tool: "bash", Input: bash(`rg -n Gateway .`)}}
	t.Run("no orch on PATH", func(t *testing.T) {
		got := runCtxReminder(t, []string{"PATH=" + filepath.Join(t.TempDir(), "empty")}, cases)
		if len(got.Handlers) != 0 || got.Out["rg"].Added != "" || got.Out["rg"].Block != "" {
			t.Fatalf("extension acted without orch on PATH: %v %+v", got.Handlers, got.Out["rg"])
		}
	})
	t.Run("ctx-only gate shell", func(t *testing.T) {
		got := runCtxReminder(t, []string{"ORCH_CTX_ONLY_READY=/tmp/ready"}, cases)
		if len(got.Handlers) != 0 || got.Out["rg"].Added != "" || got.Out["rg"].Block != "" {
			t.Fatalf("extension acted inside the ctx-only shell: %v %+v", got.Handlers, got.Out["rg"])
		}
	})
}
