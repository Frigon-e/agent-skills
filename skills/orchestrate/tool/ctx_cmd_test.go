package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ctxHarness is a repo with a committed Go file, a jbcontext, a gopls and an
// rg stand-in, each logging its calls, and an app running in that repo.
type ctxHarness struct {
	*harness
	head                    string
	jbCalls, rgCalls, gCall string
	jbDir                   string
}

func newCtxHarness(t *testing.T) *ctxHarness {
	t.Helper()
	h := setup(t)
	c := &ctxHarness{harness: h}
	repo := h.repo
	os.MkdirAll(filepath.Join(repo, "pay"), 0o755)
	os.WriteFile(filepath.Join(repo, "pay", "pay.go"), []byte("package pay\n\ntype Gateway interface {\n\tStartCheckout(ctx int) error\n}\ntype Stripe struct{}\nfunc (s *Stripe) StartCheckout(ctx int) error { return nil }\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, "svc"), 0o755)
	os.WriteFile(filepath.Join(repo, "svc", "svc.go"), []byte("package svc\n\nimport \"pay\"\n\n// Handle starts a checkout.\nfunc Handle(g pay.Gateway) error {\n\treturn g.StartCheckout(1)\n}\n\nfunc Retry(g pay.Gateway) error { return Handle(g) }\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "pay", "pay_test.go"), []byte("package pay\n\nfunc TestPay(g Gateway) { g.StartCheckout(1) }\n"), 0o644)
	h.git("add", ".")
	h.git("commit", "-q", "-m", "pay")
	out, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	c.head = strings.TrimSpace(string(out))

	c.jbDir = t.TempDir()
	c.jbCalls = filepath.Join(c.jbDir, "calls.log")
	os.WriteFile(filepath.Join(c.jbDir, "rev"), []byte(c.head), 0o644)
	jb := filepath.Join(c.jbDir, "jbcontext")
	os.WriteFile(jb, []byte(`#!/bin/sh
echo "$*" >> "`+c.jbCalls+`"
case "$1" in
status) r=$(cat "`+c.jbDir+`/rev"); s=""; [ -n "$r" ] && s='{"revision":"'$r'","createdAt":1}'
	echo '{"type":"status_result","indices":[{"snapshots":['$s']}]}';;
index) echo `+c.head+` > "`+c.jbDir+`/rev";;
watch) if [ "$2" = status ]; then printf 'Watch daemon running (pid 7)\n  github.com/x/y  `+repo+`\n'; fi;;
search) cat <<'EOF'
{"type":"search_result","results":[{"result":{"sourcePosition":{"relativePath":"pay/pay.go"}},"content":"\ntype Gateway interface {\n\tStartCheckout(ctx int) error\n}","contentStartLine":3}]}
EOF
;;
esac
`), 0o755)
	t.Setenv("ORCH_JBCONTEXT", jb)

	c.gCall = filepath.Join(c.jbDir, "gopls.log")
	gopls := filepath.Join(c.jbDir, "gopls")
	os.WriteFile(gopls, []byte(`#!/bin/sh
echo "$*" >> "`+c.gCall+`"
C="`+evalOr(repo)+`"
case "$1 $2" in
"workspace_symbol StartCheckout")
	echo "$C/pay/pay.go:4:2-15 Gateway.StartCheckout Method"
	echo "$C/pay/pay.go:7:18-31 Stripe.StartCheckout Method";;
"references $C/pay/pay.go:4:2")
	echo "$C/pay/pay.go:4:2-15"
	for i in 7 1 2 3 4 5 6 8; do echo "$C/svc/svc.go:$i:5-18"; done
	echo "$C/pay/pay_test.go:3:30-43";;
"implementation $C/pay/pay.go:4:2") echo "$C/pay/pay.go:7:18-31";;
"references $C/pay/pay.go:7:18") echo "$C/svc/svc.go:30:5-18";;
esac
`), 0o755)
	t.Setenv("ORCH_GOPLS", gopls)

	c.rgCalls = filepath.Join(c.jbDir, "rg.log")
	rg := filepath.Join(c.jbDir, "rg")
	match := func(path string, n int, text string) string {
		return `{"type":"match","data":{"path":{"text":"` + path + `"},"lines":{"text":"` + text + `\n"},"line_number":` + fmt.Sprint(n) + `,"submatches":[{"match":{"text":"StartCheckout"}}]}}`
	}
	os.WriteFile(rg, []byte(`#!/bin/sh
echo "$*" >> "`+c.rgCalls+`"
case "$*" in
*zzzNone*) exit 1;;
*" -l "*) printf 'pay/pay.go\npay/pay_test.go\n';;
*" -c "*) printf 'pay/pay.go:2\npay/pay_test.go:1\n';;
*) cat <<'EOF'
`+match("pay/pay.go", 4, "\\tStartCheckout(ctx int) error")+`
`+match("pay/pay.go", 7, "func (s *Stripe) StartCheckout(ctx int) error { return nil }")+`
`+match("pay/pay_test.go", 3, "g.StartCheckout(1)")+`
{"type":"summary","data":{}}
EOF
;;
esac
`), 0o755)
	t.Setenv("ORCH_RG", rg)
	return c
}

func (c *ctxHarness) log(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func lastOutLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestCtxStatusReportsIndexWatchAndTools(t *testing.T) {
	c := newCtxHarness(t)
	out := c.must("ctx", "status")
	wantContains(t, out, "ctx_index: full", "index: fresh ("+sha7(c.head)+")", "watch: Watch daemon running (pid 7)", "watched: true",
		"tools[3]{name,path}:", "  ts_server,missing", "  gopls,", "  rg,")
	if l := lastOutLine(out); !strings.HasPrefix(l, "sources: ") || !strings.Contains(l, "jbcontext index "+sha7(c.head)) {
		t.Errorf("last line = %q", l)
	}
	var js struct {
		Result struct {
			Head       string `json:"head"`
			IndexFresh bool   `json:"index_fresh"`
			Watched    bool   `json:"watched"`
			WatchPID   string `json:"watch_pid"`
		} `json:"result"`
		Sources string `json:"sources"`
	}
	if err := json.Unmarshal([]byte(c.must("ctx", "status", "--json")), &js); err != nil {
		t.Fatal(err)
	}
	if js.Result.Head != c.head || !js.Result.IndexFresh || !js.Result.Watched || js.Result.WatchPID != "7" || js.Sources == "" {
		t.Errorf("json = %+v", js)
	}
	if exists(runRoot(c.repo)) {
		t.Error("ctx status created run state")
	}
	if strings.Contains(c.log(c.jbCalls), "index") {
		t.Errorf("status refreshed the index: %s", c.log(c.jbCalls))
	}
}

func TestCtxRefreshFollowsThePolicy(t *testing.T) {
	c := newCtxHarness(t)
	os.WriteFile(filepath.Join(c.jbDir, "rev"), []byte(strings.Repeat("c", 40)), 0o644)
	out := c.must("ctx", "refresh")
	wantContains(t, out, "refresh: index is current ("+sha7(c.head)+")")
	wantContains(t, c.log(c.jbCalls), "index --only-incremental")

	os.WriteFile(filepath.Join(c.jbDir, "rev"), nil, 0o644)
	os.Remove(c.jbCalls)
	cfg := filepath.Join(c.tmp, "config.toml")
	os.WriteFile(cfg, []byte("ctx_index = \"incremental\"\n"), 0o644)
	out = c.must("ctx", "refresh")
	if strings.Contains(c.log(c.jbCalls), "index") {
		t.Errorf("incremental built a first index: %s", c.log(c.jbCalls))
	}
	wantContains(t, out, "refresh: index is not current", "jbcontext has no index for this repo and ctx_index is incremental")
	c.must("ctx", "refresh", "--full")
	wantContains(t, c.log(c.jbCalls), "index --project-path")

	os.WriteFile(cfg, []byte("ctx_index = \"off\"\n"), 0o644)
	os.Remove(c.jbCalls)
	wantContains(t, c.must("ctx", "refresh"), "refresh: nothing to do (jbcontext is off (ctx_index = off))")
	if c.log(c.jbCalls) != "" {
		t.Errorf("off called jbcontext: %s", c.log(c.jbCalls))
	}
	os.WriteFile(cfg, []byte("ctx_index = \"sometimes\"\n"), 0o644)
	if code, out := c.run("ctx", "status"); code == 0 {
		t.Errorf("a bad ctx_index was accepted: %d %s", code, out)
	}
}

func TestCtxWatchIsAThinWrapper(t *testing.T) {
	c := newCtxHarness(t)
	c.must("ctx", "watch", "add")
	c.must("ctx", "watch", "remove", c.repo)
	log := c.log(c.jbCalls)
	wantContains(t, log, "watch add --project-path "+evalOr(c.repo), "watch remove --project-path "+c.repo)
	if code, _ := c.run("ctx", "watch", "start"); code == 0 {
		t.Error("an unknown watch verb was accepted")
	}
}

func TestCtxFindSymTextBatchAndBriefOutsideAnyRunState(t *testing.T) {
	c := newCtxHarness(t)

	find := c.must("ctx", "find", "checkout gateway", "-p", "pay", "--limit", "2")
	wantContains(t, find, "find: 1 of 1 hits for \"checkout gateway\" in 1 file (path pay)", "pay/pay.go\n", "  3: type Gateway interface {", "  4:  StartCheckout(ctx int) error", "  declares type Gateway", "  package pay declares 3 exported symbols in 1 file")
	wantContains(t, c.log(c.jbCalls), "--limit 10", "--path-filter pay", "--revision "+c.head)
	if !strings.HasPrefix(lastOutLine(find), "sources: jbcontext index "+sha7(c.head)) {
		t.Errorf("find sources: %q", lastOutLine(find))
	}

	sym := c.must("ctx", "sym", "StartCheckout")
	wantContains(t, sym, `sym: 2 definitions of "StartCheckout"`, "StartCheckout  method  pay/pay.go:4  candidate (identity not verified)\n", "\n   StartCheckout(ctx int) error\n", "used by 8, +1 in tests:\n",
		"  svc/svc.go\n", "    5  // Handle starts a checkout.\n", "showing 1-5 of 8; next: --page 2; left out: svc/svc.go (3)", "implemented by 1:\n  7  func (s *Stripe) StartCheckout(ctx int) error { return nil }\n",
		"StartCheckout  method  pay/pay.go:7  candidate (identity not verified)\n", "used by 1, none in tests\n")
	if strings.Contains(sym, "pay_test.go") {
		t.Errorf("default sym listed test references:\n%s", sym)
	}
	wantContains(t, lastOutLine(sym), "gopls ")
	wantContains(t, c.must("ctx", "sym", "NoSuchSymbol"), `sym: no symbol named "NoSuchSymbol" found`)

	text := c.must("ctx", "text", "StartCheckout", "-p", "pay", "--max", "1")
	wantContains(t, text, "text: 1 of 2 matches", "pay/pay.go\n", "  4: StartCheckout(ctx int) error  [in type Gateway]", "showing 1-1 of 2; next: --page 2")
	log := c.log(c.rgCalls)
	wantContains(t, log, "--json", "-e StartCheckout -- pay")
	c.must("ctx", "text", "Start.*Out", "--fixed")
	if lines := strings.Split(strings.TrimSpace(c.log(c.rgCalls)), "\n"); !strings.Contains(lines[len(lines)-1], "-F") {
		t.Errorf("--fixed did not search an exact string: %s", lines[len(lines)-1])
	}
	c.must("ctx", "text", "Start.*Out")
	if lines := strings.Split(strings.TrimSpace(c.log(c.rgCalls)), "\n"); strings.Contains(lines[len(lines)-1], "-F") {
		t.Errorf("the default pattern is not a regular expression: %s", lines[len(lines)-1])
	}
	wantContains(t, c.must("ctx", "text", "zzzNone"), "text: 0 matches found in the whole repo")
	code, out := c.run("ctx", "text", "x", "-p", "../outside")
	if code != 1 || !strings.Contains(out, "error: path ../outside is outside the repo") {
		t.Errorf("a path outside the repo: %d %s", code, out)
	}

	// A batch answers in plan order, from the cache the second time.
	plan := `{"lookups":[{"kind":"text","query":"StartCheckout","why":"callers"},{"kind":"symbol","query":"StartCheckout"},{"kind":"concept","query":"checkout gateway"},{"kind":"read","path":"pay/pay.go","from":3,"to":4},{"kind":"text","query":"x","path":"../outside"}]}`
	c.stdin.WriteString(plan)
	batch := c.must("ctx", "batch", "-")
	idx := func(s string) int { return strings.Index(batch, s) }
	if !(idx("#1 text") >= 0 && idx("#1 text") < idx("#2 sym") && idx("#2 sym") < idx("#3 find") && idx("#3 find") < idx("#4 read") && idx("#4 read") < idx("#5 text")) {
		t.Errorf("batch is not in plan order:\n%s", batch)
	}
	wantContains(t, batch, "batch: 5 lookups, 4 with results, 0 empty, 1 failed", `#1 text "StartCheckout" [callers]:`, "#5 text \"x\": error: path ../outside is outside the repo")
	jb, rg := c.log(c.jbCalls), c.log(c.rgCalls)
	c.stdin.WriteString(plan)
	if again := c.must("ctx", "batch", "-"); again != batch {
		t.Errorf("a cached batch differs:\n%s", again)
	}
	if c.log(c.rgCalls) != rg || strings.Count(c.log(c.jbCalls), "search") != strings.Count(jb, "search") {
		t.Error("the second batch called rg or jbcontext again")
	}
	// ... and a changed working tree is a different cache.
	os.WriteFile(filepath.Join(c.repo, "pay", "new.go"), []byte("package pay\n"), 0o644)
	c.stdin.WriteString(plan)
	c.must("ctx", "batch", "-")
	if c.log(c.rgCalls) == rg {
		t.Error("a change in the working tree was served from the old cache")
	}
	big := `{"lookups":[` + strings.Repeat(`{"kind":"text","query":"a"},`, 60) + `{"kind":"text","query":"a"}]}`
	c.stdin.WriteString(big)
	if code, out := c.run("ctx", "batch", "-"); code != 2 || !strings.Contains(out, "at most 50") {
		t.Errorf("a huge plan: %d %s", code, out)
	}
	c.stdin.WriteString("not a plan")
	if code, _ := c.run("ctx", "batch", "-"); code != 2 {
		t.Error("a plan that is not JSON was accepted")
	}

	c.stdin.WriteString("### api: Checkout\nBuild the StartCheckout flow in `pay/pay.go:7`; it adds a `zzzNone_table`.\n")
	brief := c.must("ctx", "brief", "-", "--owns", "pay")
	wantContains(t, brief, "brief: ", "files[", "  pay/pay.go,", "not_found[1]:", "  zzzNone_table")
	c.stdin.WriteString("Build the StartCheckout flow")
	var js struct {
		Result struct{ Files []string } `json:"result"`
	}
	if err := json.Unmarshal([]byte(c.must("ctx", "brief", "-", "--json")), &js); err != nil {
		t.Fatal(err)
	}
	if exists(runRoot(c.repo)) {
		t.Error("ctx commands created run state")
	}
}

func TestCtxPrivateTaskNeverUsesJbcontext(t *testing.T) {
	c := newCtxHarness(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("---\nid: p\nprivate: true\n---\nbody\n"), 0o644)
	t.Setenv("ORCH_TASK_DIR", dir)
	code, out := c.run("ctx", "find", "checkout")
	if code != 1 {
		t.Fatalf("private find exit %d, want 1: %s", code, out)
	}
	wantContains(t, out, "find: error: jbcontext is not used for private work")
	if c.log(c.jbCalls) != "" {
		t.Errorf("a private task reached jbcontext: %s", c.log(c.jbCalls))
	}
}

func TestGateCachesLiveUnderTheCtxCacheNotTheGateDir(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(1)
	h.sessionPi(gatePasses(nil))
	t.Setenv("HERDR_ENV", "")
	h.must("gate", "spec.md")
	if exists(filepath.Join(gdir, "enrich-cache")) {
		t.Error("the gate wrote an enrich-cache in its dir")
	}
}

func ctxSpec() string {
	return planHeader + "### checkout: Checkout flow\nkind: ship\nmodel: haiku\nowns: pay\n\nBuild the StartCheckout flow in `pay/pay.go:7`; it adds a `zzzNone_table`.\n\n#### Builds\n- Code that starts a hosted checkout for an invoice.\n\n#### Acceptance\nTests pass.\n\n" + planTail
}

func TestPlanBriefsGetFactsGatheredByCodeAndNoCtxSkipsThem(t *testing.T) {
	c := newCtxHarness(t)
	c.commitSpec(ctxSpec())
	c.must("plan", "spec.md", "--skip-gate")
	text := string(mustRead(t, filepath.Join(c.dir("checkout"), "task.md")))
	i, j, k := strings.Index(text, "\n# Spec\n"), strings.Index(text, "\n# Facts gathered by code\n"), strings.Index(text, "\n# Acceptance\n")
	if !(i > 0 && j > i && k > j) {
		t.Fatalf("the facts section is not between Spec and Acceptance (%d %d %d):\n%s", i, j, k, text)
	}
	wantContains(t, text, "### 1. Ranked files", "- `pay/pay.go` (score ", "- `zzzNone_table`", "Computed by code when this brief was written")
	if n := len(text[j:k]); n > taskBriefChars+800 {
		t.Errorf("the facts section is %d chars, want about %d at most", n, taskBriefChars)
	}
	if problems, _, err := checkTask(c.a.siteRoot(t), "checkout"); err != nil || len(problems) != 0 {
		t.Errorf("a brief with facts fails check: %v %v", problems, err)
	}

	c2 := newCtxHarness(t)
	c2.commitSpec(ctxSpec())
	c2.must("plan", "spec.md", "--skip-gate", "--no-ctx")
	if text := string(mustRead(t, filepath.Join(c2.dir("checkout"), "task.md"))); strings.Contains(text, "Facts gathered by code") {
		t.Errorf("--no-ctx still added the section:\n%s", text)
	}

	// Missing tools never fail the command.
	c3 := newCtxHarness(t)
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("ORCH_GOPLS", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("ORCH_RG", filepath.Join(t.TempDir(), "absent"))
	c3.commitSpec(ctxSpec())
	c3.must("plan", "spec.md", "--skip-gate")
	wantContains(t, string(mustRead(t, filepath.Join(c3.dir("checkout"), "task.md"))), "note: no concept search")

	// orch new has no task text to brief.
	c3.must("new", "plain", "--kind", "scout", "--harness", "claude", "--model", "sonnet", "--effort", "medium")
	if text := string(mustRead(t, filepath.Join(c3.dir("plain"), "task.md"))); strings.Contains(text, "Facts gathered by code") {
		t.Error("orch new added facts to a scaffold with placeholders")
	}
}

func TestDoctorReportsCtxReadiness(t *testing.T) {
	c := newCtxHarness(t)
	code, out := c.run("doctor")
	_ = code
	wantContains(t, out, "ctx: index fresh, watch on, gopls ok, ts server missing, rg ok")
	os.WriteFile(filepath.Join(c.jbDir, "rev"), []byte(strings.Repeat("d", 40)), 0o644)
	_, out = c.run("doctor")
	wantContains(t, out, "ctx: index stale, watch on", "(run: orch ctx refresh)")
	if strings.Contains(c.log(c.jbCalls), "index") {
		t.Error("doctor refreshed the index")
	}
}
