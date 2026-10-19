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
func TestCtxOnlyExtensionAllowsOnlyOneOrchCtxCall(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	ext, err := writeCtxOnlyExtension(dir)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		`orch ctx batch -q "find:Code that saves a payment method" -q "symbol:Gateway refs=3" -q "text:foo glob=internal/**"`,
		`orch ctx text 'a|b(c)$' --compact --limit 3`,
		`orch ctx sym Service.saveMethod --refs 2`,
		`orch ctx find "Code that lists payment gateways"`,
	}
	blocked := []string{
		`grep -rn Gateway internal`,
		`ls internal`,
		`cd internal && orch ctx text x`,
		`orch ctx text x | head -3`,
		`orch ctx text x; ls`,
		`orch ctx text x > out.txt`,
		`orch ctx text x && rm -r .`,
		`orch ctx find "$(id)"`,
		"orch ctx find \"`id`\"",
		`orch ctx text *.go`,
		`orch ctx read a.go`,
		`orch ctx text "unterminated`,
		`orch ctxfoo`,
	}
	script := `
import ext from ` + string(mustJSON(t, ext)) + `;
let handler;
ext({ on: (_name, fn) => { handler = fn; } });
const out = {};
for (const c of JSON.parse(process.argv[1])) {
  const r = await handler({ toolName: "bash", input: { command: c } });
  out[c] = r && r.block ? r.reason : "";
}
const other = await handler({ toolName: "read", input: { path: "a.go" } });
out["<read tool>"] = other ? "blocked" : "";
console.log(JSON.stringify(out));
`
	all, _ := json.Marshal(append(append([]string{}, allowed...), blocked...))
	cmd := exec.Command(node, "--input-type=module", "-e", script, string(all))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got map[string]string
	if err := json.Unmarshal(out[strings.Index(string(out), "{"):], &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, c := range allowed {
		if got[c] != "" {
			t.Errorf("blocked an allowed call %q: %s", c, got[c])
		}
	}
	for _, c := range blocked {
		if !strings.Contains(got[c], "orch ctx batch") {
			t.Errorf("did not block %q (reason %q)", c, got[c])
		}
	}
	if got["<read tool>"] != "" {
		t.Error("the extension must only look at the bash tool")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCtxOnlyShellLimitsPiToReadAndTheExtension(t *testing.T) {
	h := setup(t)
	open, err := gateAgentPass(h.repo, "pi", gatePass{name: "a", shared: "s", task: "t", prompt: "st", ctxTool: true})
	if err != nil {
		t.Fatal(err)
	}
	only, err := gateAgentPass(h.repo, "pi", gatePass{name: "a", shared: "s", task: "t", prompt: "st", ctxTool: true, ctxOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(piPassTools(open), " "); got != "--tools read,grep,find,ls,bash" {
		t.Errorf("the open shell changed: %s", got)
	}
	want := "--tools read,bash -e " + filepath.Join(filepath.Dir(only.cli.Extension), "orch-ctx-only.ts")
	if got := strings.Join(piPassTools(only), " "); got != want {
		t.Errorf("ctx-only tools = %q, want %q", got, want)
	}
	if _, err := os.Stat(only.cli.Extension); err != nil {
		t.Errorf("extension file: %v", err)
	}
	if !strings.Contains(only.shared, ctxOnlyNotice) || strings.Contains(open.shared, ctxOnlyNotice) {
		t.Error("only the ctx-only pass is told its shell runs only orch ctx")
	}
	// Claude has its own permission list and is untouched.
	cl, err := gateAgentPass(h.repo, "claude", gatePass{name: "a", shared: "s", task: "t", prompt: "st", ctxTool: true, ctxOnly: true})
	if err != nil || cl.cli.Extension != "" || strings.Contains(cl.shared, ctxOnlyNotice) {
		t.Errorf("claude pass: ext %q err %v", cl.cli.Extension, err)
	}
}
