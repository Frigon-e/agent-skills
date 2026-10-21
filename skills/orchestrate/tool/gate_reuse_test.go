package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func specPkgFrom(t *testing.T, src, id string) *specPackage {
	t.Helper()
	sp := parseSpec(src)
	sp.validate()
	for _, p := range sp.packages {
		if p.id == id {
			return p
		}
	}
	t.Fatalf("no package %s", id)
	return nil
}

func TestBuildsBulletsBecomeItemsVerbatim(t *testing.T) {
	src := planHeader + "### api: Queue API\nkind: ship\nowns: internal/queue\n\nBody text.\n\n#### Builds\n- Code that queues a transfer on the panel, when a user submits one, producing a row.\n- Code that retries a failed send\n  with backoff, when the provider errors.\n  search: Code that retries a failed send\n  reuse: internal/store.go#Retry, internal/store.go:3\n* Code that lists queued transfers\n  reuse: internal/store.go\n\n#### Acceptance\n- Tests pass.\n" + planTail
	p := specPkgFrom(t, src, "api")
	items := packageItems(p)
	var got []string
	for _, it := range items {
		got = append(got, it.ID+" "+it.Text+" ["+strings.Join(it.Reuse, ", ")+"]")
	}
	want := []string{
		"api#1 Code that queues a transfer on the panel, when a user submits one, producing a row. []",
		"api#2 Code that retries a failed send with backoff, when the provider errors. [internal/store.go#Retry, internal/store.go:3]",
		"api#3 Code that lists queued transfers [internal/store.go]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(p.body, "Code that") || !strings.Contains(p.acceptance, "Tests pass") {
		t.Errorf("body %q acceptance %q", p.body, p.acceptance)
	}
	dir := t.TempDir()
	if err := writeItems(dir, map[string][]gateItem{"api": items}, []string{"api"}); err != nil {
		t.Fatal(err)
	}
	var back []gateItem
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, "items.json")), &back); err != nil || len(back) != 3 || back[2].ID != "api#3" || strings.Join(back[1].Reuse, ", ") != "internal/store.go#Retry, internal/store.go:3" || len(back[1].Queries) != 1 {
		t.Errorf("items.json: %v %v", back, err)
	}
	// the task a lead gets shows the Builds list under Spec
	task := specText(p)
	wantContains(t, task, "Body text.", "Builds:", "- api#1: Code that queues a transfer", "  reuse: internal/store.go#Retry, internal/store.go:3", "  search: Code that retries a failed send")
}

func TestBuildsParserHandlesEmptyLongAndNumberedBullets(t *testing.T) {
	if its, pr := parseBuilds(""); len(its) != 0 || len(pr) != 0 {
		t.Errorf("empty: %v %v", its, pr)
	}
	its, pr := parseBuilds("not a bullet\n1. Code that a\n2) Code that b\n")
	if len(its) != 2 || its[1].Text != "Code that b" || len(pr) != 0 {
		t.Errorf("numbered: %v %v", its, pr)
	}
	its, pr = parseBuilds("- " + strings.Repeat("x", 301))
	if len(pr) != 0 || len(its[0].Text) != 301 {
		t.Errorf("long bullet must remain verbatim: %v %v", its, pr)
	}
}

func TestShipPackageNeedsBuildsAndTooManyIsAWarning(t *testing.T) {
	mk := func(builds string) string {
		return planHeader + "### api: Queue API\nkind: ship\nowns: internal/queue\n\nBody.\n\n" + builds + "#### Acceptance\nTests pass.\n\n### look: Look around\nkind: scout\n\nBody.\n\n#### Acceptance\nA note.\n" + planTail
	}
	sp := parseSpec(mk(""))
	sp.validate()
	if len(sp.problems) != 0 {
		t.Errorf("missing Builds (a scout is exempt): %v", sp.problems)
	}
	sp = parseSpec(mk("#### Builds\n\n"))
	sp.validate()
	if len(sp.problems) != 0 {
		t.Errorf("empty Builds must not block: %v", sp.problems)
	}
	// the deterministic check names it too
	det := gateDeterministic(t.TempDir(), config{}, sp, mk(""))
	found := false
	for _, d := range det {
		found = found || strings.Contains(d, "note: package api has no Builds bullets; reuse checklist is skipped")
	}
	if !found {
		t.Errorf("deterministic: %v", det)
	}
	var chk config
	chk.Check = "x"
	var many strings.Builder
	many.WriteString("#### Builds\n")
	for i := 0; i < 9; i++ {
		many.WriteString(fmt.Sprintf("- Code that does thing %d\n", i))
	}
	sp = parseSpec(mk(many.String() + "\n"))
	sp.validate()
	if len(sp.problems) != 0 {
		t.Fatalf("9 bullets must still parse: %v", sp.problems)
	}
	var warn string
	for _, d := range gateDeterministic(t.TempDir(), chk, sp, "") {
		if strings.Contains(d, "9 Builds bullets") {
			warn = d
		}
	}
	if !strings.HasPrefix(warn, "note: ") || !strings.Contains(warn, "split the package") || hasError([]string{warn}) {
		t.Errorf("warning %q", warn)
	}
	// a reuse pointer must exist
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "have.go"), []byte("package x\n"), 0o644)
	sp = parseSpec(mk("#### Builds\n- Does a\n  reuse: have.go\n- Does b\n  reuse: nope.go\n- Does c\n  reuse: have.go:9\n\n"))
	sp.validate()
	det = gateDeterministic(repo, chk, sp, "")
	joined := strings.Join(det, "\n")
	if strings.Contains(joined, "bullet 1 ") || !strings.Contains(joined, "bullet 2: pointer nope.go") || !strings.Contains(joined, "bullet 3: pointer have.go:9 does not resolve") {
		t.Errorf("reuse tails:\n%s", joined)
	}
}

// reuseRepo has two project roots: app/ with three functions the item names and zlib/ with one.
func reuseRepo(t *testing.T) (code string) {
	t.Helper()
	t.Setenv("ORCH_CACHE_DIR", t.TempDir())
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is not installed")
	}
	t.Setenv("ORCH_GOPLS", filepath.Join(t.TempDir(), "no-gopls"))
	t.Setenv("ORCH_TSLS", filepath.Join(t.TempDir(), "no-tsls"))
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "no-jbcontext"))
	code = evalOr(t.TempDir())
	writeTree(t, code, map[string]string{
		"app/go.mod":    "module example.com/app\n\ngo 1.23\n",
		"app/a.go":      "package app\n\n// Alpha sends a transfer.\nfunc Alpha() {}\n",
		"app/b.go":      "package app\n\nfunc Beta() {}\n",
		"app/c.go":      "package app\n\nfunc Gamma() {}\n",
		"app/a_test.go": "package app\n\nfunc TestAlpha() { Alpha() }\n",
		"zlib/go.mod":   "module example.com/zlib\n\ngo 1.23\n",
		"zlib/z.go":     "package zlib\n\nfunc Delta(x int) int {\n\treturn x\n}\n",
		"mine/go.mod":   "module example.com/mine\n\ngo 1.23\n",
		"mine/mine.go":  "package mine\n\nfunc Alpha() {}\n",
	})
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@e.com", "commit", "-q", "-m", "i"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = code
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return code
}

func TestCandidatesAreRankedByAgreementWithAQuotaPerRoot(t *testing.T) {
	code := reuseRepo(t)
	e := newCtxEnv(code, ctxOpts{})
	items := []gateItem{{ID: "p#1", Package: "p", Text: "Build a sender.", Queries: []string{"Code that sends using `Alpha`, `Beta`, `Gamma` and `Delta`."}, Kind: "builds"}}
	got := e.reuseCandidates(items, []string{"mine/"}, "sender")
	if len(got) != 1 || len(got[0].Cands) != reuseCands {
		t.Fatalf("candidates: %+v", got)
	}
	var paths []string
	perTree := map[string]int{}
	for _, c := range got[0].Cands {
		paths = append(paths, c.Path)
		perTree[c.Tree]++
		if inOwns(c.Path, []string{"mine/"}) || isTestPath(c.Path) {
			t.Errorf("an own or test file was offered: %s", c.Path)
		}
	}
	if perTree["app/"] > reuseRootQuota || perTree["zlib/"] != 1 {
		t.Errorf("quota per root: %v from %v", perTree, paths)
	}
	first := got[0].Cands[0]
	if first.Path != "app/a.go" || first.Name != "Alpha" || first.Doc != "Alpha sends a transfer." || first.Start != 4 || !strings.Contains(first.Kinds, "sym") {
		t.Errorf("first candidate: %+v", first)
	}
	again := e.reuseCandidates(items, []string{"mine/"}, "sender")
	if fmt.Sprint(again[0].Cands) != fmt.Sprint(got[0].Cands) {
		t.Error("ranking is not deterministic")
	}
}

func bigCands(n int) []itemCands {
	var out []itemCands
	for i := 1; i <= n; i++ {
		ic := itemCands{Item: gateItem{ID: fmt.Sprintf("p#%d", i), Package: "p", Text: fmt.Sprintf("item %d", i)}}
		for k := 1; k <= 3; k++ {
			c := reuseCand{Path: fmt.Sprintf("x/f%d_%d.go", i, k), Start: 1, End: 90, Name: "Fn", In: "func Fn", Sig: "func Fn()", Kinds: "sym", Score: 2, Total: 90}
			for l := 0; l < 80; l++ {
				c.Lines = append(c.Lines, fmt.Sprintf("\tline %d of the body with some text to take room", l))
			}
			ic.Cands = append(ic.Cands, c)
		}
		out = append(out, ic)
	}
	return out
}

func TestChecklistIsCappedPagedAndSaysWhatWasCut(t *testing.T) {
	small := renderChecklist(bigCands(2), nil, "")
	wantContains(t, small, "- `p#1` item 1", "1. `x/f1_1.go:1-90`", "func Fn", "| (50 more lines) omitted evidence-")
	if strings.Contains(small, "cut:") || strings.Contains(small, "More items") {
		t.Errorf("a small checklist was cut:\n%s", small)
	}
	// Larger bodies keep up to 40 relevant lines, with an explicit expansion.
	if strings.Count(small, "| \tline") != 2*3*reuseShowLines {
		t.Errorf("long bodies show %d lines", strings.Count(small, "| \tline"))
	}
	big := renderChecklist(bigCands(20), nil, "")
	wantContains(t, big, reuseMoreNotice, "- `p#13` item 13", "Recorded omissions")
	if i := strings.Index(big, reuseMoreNotice); i < 0 || strings.Index(big, "`p#12`") > i || strings.Index(big, "`p#13`") < i {
		t.Error("items 13 and later must come after the notice")
	}
	if len(big) > dossierTotal {
		t.Errorf("checklist is %d bytes, past its single overall budget", len(big))
	}
	only := renderChecklist(bigCands(5), map[string]bool{"p#2": true}, "")
	if strings.Contains(only, "`p#1`") || !strings.Contains(only, "`p#2`") {
		t.Errorf("restricting to an item:\n%s", only)
	}
}

func TestDossierHasTheChecklistAheadOfTheLookAlikes(t *testing.T) {
	code, src := dossierRepo(t)
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "no-jbcontext"))
	ds := buildDossier(t, code, src, t.TempDir())
	text := ds.text("api")
	i, j := strings.Index(text, "### 3a. Reuse checklist"), strings.Index(text, "### 3. Existing things")
	if i < 0 || j < i {
		t.Fatalf("checklist position %d, look-alikes %d:\n%s", i, j, text)
	}
	wantContains(t, text, "- `api#1` Code that queues a transfer and serves POST /api/transfers.")
	if len(ds.items["api"]) == 0 || len(ds.cands["api"]) != len(ds.items["api"]) {
		t.Errorf("items %v", ds.items)
	}
}

func TestVerdictsAreParsedNormalisedAndSalvaged(t *testing.T) {
	raw := `{"packages":[{"id":"p","items":[{"item":"p#1","verdict":"Duplicate","candidate":"internal/store.go:3","why":"same"},{"item":"2","verdict":"extend","candidate":"internal/store.go:3","why":"add a column in Put"},{"item":"p#3","verdict":"new","candidate":null,"why":"nothing"},{"item":"p#4","verdict":"maybe"}]}]}`
	var r itemsReply
	if err := extractJSON(raw, &r); err != nil || len(r.Packages[0].Items) != 4 {
		t.Fatalf("decode: %v %+v", err, r)
	}
	items := []gateItem{{ID: "p#1", Package: "p", Text: "one"}, {ID: "p#2", Package: "p", Text: "two"}, {ID: "p#3", Package: "p", Text: "three"}, {ID: "p#4", Package: "p", Text: "four"}}
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "internal"), 0o755)
	os.WriteFile(filepath.Join(repo, "internal", "store.go"), []byte("package internal\n\nfunc Put() {}\n"), 0o644)
	rec := &gateRecord{}
	rec.ensureItems(items)
	rec.applyVerdicts("g", repo, items, r.Packages[0].Items)
	vs := map[string]string{}
	for _, it := range rec.Items {
		vs[it.ID] = it.Verdict
	}
	if vs["p#1"] != verdictReuse || vs["p#2"] != verdictExtend || vs["p#3"] != verdictNew || vs["p#4"] != "" {
		t.Errorf("verdicts %v", vs)
	}
	if len(rec.Findings) != 2 || rec.Findings[0].Source != "reuse" || rec.Findings[0].Severity != "concern" || rec.Findings[1].Source != "extend" || rec.Findings[1].Severity != "note" || !strings.Contains(rec.Findings[1].Why, "column") {
		t.Errorf("findings %+v", rec.Findings)
	}
	if m := rec.missingItems(items); len(m) != 1 || m[0].ID != "p#4" {
		t.Errorf("missing %v", m)
	}
	// salvage: a reply cut off in the middle still gives the complete items
	cut := `{"packages":[{"id":"p","will_change":[],"items":[{"item":"p#1","verdict":"new","candidate":null,"why":"x"},{"item":"p#2","verdict":"ne`
	es, ok := salvageEntries(cut, []string{"p"})
	if !ok {
		t.Fatalf("salvage failed")
	}
	_ = es
	if strings.Count(rec.coverageLine(), "extend 1") != 1 {
		t.Errorf("coverage line %q", rec.coverageLine())
	}
}

func verdictRepo(t *testing.T, h *harness) string {
	t.Helper()
	os.MkdirAll(filepath.Join(h.repo, "internal"), 0o755)
	writeFiles(t, h.repo, map[string]string{"internal/store.go": "package internal\n\nfunc Put() {}\n"})
	h.commitSpec(planHeader + "### api: API\nkind: ship\nowns: internal/api.go\n\nBuild the API.\n#### Builds\n- Build api.\n- Add the POST /api/put endpoint\n#### Acceptance\nTests pass.\n" + planTail)
	return filepath.Join(filepath.Dir(h.dir("x")), "gate", specHash(mustRead(t, filepath.Join(h.repo, "spec.md"))))
}

func scriptPi(t *testing.T, h *harness, groupReply, retryReply string) string {
	t.Helper()
	dir := filepath.Join(h.tmp, "scriptpi")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "group.json"), []byte(groupReply), 0o644)
	os.WriteFile(filepath.Join(dir, "retry.json"), []byte(retryReply), 0o644)
	script := "#!/bin/sh\n" + fakeCtxOnlyActivation + `echo "$*" >> "` + dir + `/calls.log"
case "$*" in
*GATE-GROUP-ITEMS*) cat "` + dir + `/retry.json";;
*GATE-GROUP*) cat "` + dir + `/group.json";;
*) echo '{"findings":[]}';;
esac
`
	os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o755)
	t.Setenv("ORCH_PI", filepath.Join(dir, "pi"))
	t.Setenv("HERDR_ENV", "")
	return dir
}

func TestGroupVerdictsAreOffByDefaultAndTheRetryAsksOnlyForMissingItems(t *testing.T) {
	h := setup(t)
	gdir := verdictRepo(t, h)
	group := `{"packages":[{"id":"api","will_change":[],"claims":[],"questions":[],"reuse":[],"items":[{"item":"api#1","verdict":"reuse","candidate":"internal/store.go:3","why":"Put exists","matching_behavior":"Put stores the record","support_assessment":{"schema_version":1,"state":"supported","rationale":"The existing Put function handles the requested storage behavior"}}]}]}`
	retry := `{"packages":[{"id":"api","items":[{"item":"api#2","verdict":"extend","candidate":"internal/store.go:3","why":"add the route beside Put","matching_behavior":"Put supplies storage","missing_behavior":"Add the POST route beside Put","support_assessment":{"schema_version":1,"state":"supported","rationale":"Storage exists but the requested HTTP route must be added"}}]}]}`
	dir := scriptPi(t, h, cleanPackageReply, retry)

	// Off by default: no verdict is asked for and no retry runs.
	out := h.must("gate", "spec.md")
	calls := string(mustRead(t, filepath.Join(dir, "calls.log")))
	if strings.Contains(calls, "GATE-GROUP-ITEMS") || strings.Contains(calls, "answer every item") || strings.Contains(out, "items:") {
		t.Errorf("verdicts leaked into a default run:\n%s\n%s", out, calls)
	}
	wantContains(t, string(mustRead(t, filepath.Join(gdir, "items.json"))), `"id": "api#1"`, `"id": "api#2"`)

	os.Remove(filepath.Join(dir, "calls.log"))
	scriptPi(t, h, group, retry)
	out = h.must("gate", "spec.md", "--verdicts")
	calls = string(mustRead(t, filepath.Join(dir, "calls.log")))
	if n := strings.Count(calls, "GATE-GROUP-ITEMS"); n != 1 {
		t.Fatalf("retry passes = %d:\n%s", n, calls)
	}
	wantContains(t, calls, "answer every item", "without a verdict: api#2.")
	for _, l := range strings.Split(calls, "\n") {
		if strings.Contains(l, "GATE-GROUP-ITEMS") && strings.Contains(l, "without a verdict: api#1") {
			t.Errorf("the retry asked for a covered item: %s", l)
		}
	}
	wantContains(t, out, "items: 2, with a verdict 2 (reuse 1, extend 1, new 0, unclear 0), no verdict 0")
	rec := readRecord(t, gdir)
	if len(rec.Items) != 2 || rec.Items[0].Verdict != "reuse" || rec.Items[1].Verdict != "extend" || !strings.HasSuffix(rec.Items[1].Pass, "#retry") {
		t.Errorf("items %+v", rec.Items)
	}
	src := map[string]string{}
	for _, f := range rec.Findings {
		src[f.Source] = f.Severity
	}
	if src["reuse"] != "concern" || src["extend"] != "note" {
		t.Errorf("finding kinds %v", src)
	}

	// A retry that still leaves an item out prevents approval.
	scriptPi(t, h, group, `{"packages":[{"id":"api","items":[]}]}`)
	code, out := h.run("gate", "spec.md", "--verdicts")
	if code != 1 {
		t.Fatalf("missing verdict passed: %d %s", code, out)
	}
	view := wantGateState(t, out, "incomplete")
	wantContains(t, view.Coverage, "no verdict 1")
	wantContains(t, view.PassErrors["group:api"], "1 items had no verdict", "api#2")
	if rec := readRecord(t, gdir); len(rec.Reasons) == 0 {
		t.Fatal("missing verdict has no reason")
	}
}

func TestVerdictsDoNotChangeTheSharedPrefix(t *testing.T) {
	code, src := dossierRepo(t)
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "no-jbcontext"))
	build := func(verdicts bool) []gatePass {
		ds := buildDossier(t, code, src, t.TempDir())
		ds.verdicts = verdicts
		sp := parseSpec(src)
		sp.validate()
		return buildGatePasses(code, src, "spec.md", sp, map[string][]string{}, ds, false, nil)
	}
	off, on := build(false), build(true)
	if len(off) != len(on) {
		t.Fatal("pass counts differ")
	}
	for i := range off {
		if off[i].shared != on[i].shared {
			t.Errorf("pass %s: the shared prefix changed", off[i].name)
		}
	}
	var go1, go2 gatePass
	for i := range off {
		if off[i].class == "package" {
			go1, go2 = off[i], on[i]
		}
	}
	if strings.Contains(go1.task, "answer every item") || !strings.Contains(go2.task, "answer every item") || !strings.Contains(go2.task, "extend (existing code does something near it") || len(go1.items) != 0 || go2.retry == nil {
		t.Errorf("group task off:\n%s\non:\n%s", go1.task, go2.task)
	}
}

// ---- shadow judges ----

func TestShadowModeFlagOverridesConfig(t *testing.T) {
	cases := []struct{ flag, cfg, want string }{{"", "", "off"}, {"", "decider", "decider"}, {"flash", "decider", "flash"}, {"decider,flash", "", "both"}, {"both", "off", "both"}, {"off", "both", "off"}}
	for _, c := range cases {
		if got, err := shadowMode(c.flag, c.cfg); err != nil || got != c.want {
			t.Errorf("shadowMode(%q,%q) = %q %v, want %q", c.flag, c.cfg, got, err, c.want)
		}
	}
	if _, err := shadowMode("sometimes", ""); err == nil {
		t.Error("a bad mode was accepted")
	}
}

func shadowFixture(t *testing.T) (*dossierSet, *spec) {
	t.Helper()
	code := reuseRepo(t)
	src := planHeader + "### p: Sender\nkind: ship\nowns: mine/\n\nBuild a sender with `Alpha` and `Delta`.\n\n#### Builds\n- Sends a transfer.\n  search: Code that sends a transfer using `Alpha` and `Delta`.\n\n#### Acceptance\nTests pass.\n" + planTail
	sp := parseSpec(src)
	sp.validate()
	ds := newDossierSet(newCtxEnv(code, ctxOpts{}), sp, packageBlocks(sp, src))
	ds.build([]string{"p"})
	return ds, sp
}

func TestShadowJudgesRecordAndNeverDecide(t *testing.T) {
	ds, sp := shadowFixture(t)
	if len(ds.cands["p"]) == 0 || len(ds.cands["p"][0].Cands) == 0 {
		t.Fatalf("no candidates: %+v", ds.cands)
	}
	var seen []map[string]any
	post := func(model string, state, qs map[string]any) (map[string]decAnswer, string, shadowUsage, error) {
		seen = append(seen, state)
		for _, q := range deciderQuestionOrder {
			if _, ok := qs[q]; !ok {
				t.Errorf("question %s missing", q)
			}
		}
		return map[string]decAnswer{"trigger_and_inputs": {Choice: "same", Confidence: 0.8}, "outputs_and_effects": {Choice: "partly"}, "relation": {Choice: "variation_site", Confidence: 0.7},
			"best_candidate": {Choice: "1"}, "placement": {Choice: "existing_location"}}, "dec-1", shadowUsage{Cost: 0.002, Tokens: 900}, nil
	}
	flashJSON := `{"items":[{"item":"p#1","candidate_does":"sends","trigger_inputs":"same","outputs_effects":"same","lacks":"nothing","class":"duplicate","best_candidate":"1","placement":"existing_location","confidence":0.9}]}`
	var prompts []string
	run := func(mode string) ([]shadowRow, string) {
		gdir := t.TempDir()
		prompts = nil
		sr := &shadowRun{dir: gdir, root: t.TempDir(), model: "dec", flash: "fl", mode: mode, dos: ds, sp: sp, post: post,
			runPass: func(p gatePass, sess string) (string, error) {
				if !p.noTools {
					t.Error("the flash judge got tools")
				}
				prompts = append(prompts, p.prompt)
				return flashJSON, nil
			}}
		rows, sum := sr.run()
		if mode != "off" {
			if b := mustRead(t, filepath.Join(gdir, "shadow", "run", "items.jsonl")); strings.Count(string(b), "\n") != len(rows) {
				t.Errorf("items.jsonl has %d lines for %d rows", strings.Count(string(b), "\n"), len(rows))
			}
		}
		return rows, sum
	}
	count := func(rows []shadowRow, j string) (n int) {
		for _, r := range rows {
			if r.Judge == j && r.Error == "" && r.Skipped == "" {
				n++
			}
		}
		return
	}
	nItems := len(ds.cands["p"])
	rows, _ := run("decider")
	if count(rows, "decider") != nItems || count(rows, "flash") != 0 || len(prompts) != 0 {
		t.Errorf("decider mode: %d decider rows, %d flash, %d prompts", count(rows, "decider"), count(rows, "flash"), len(prompts))
	}
	r := rows[0]
	if r.Verdict != verdictExtend || r.Best != "1" || r.Answers["placement"] != "existing_location" || r.Cost != 0.002 || r.StateChars == 0 || r.Model != "dec-1" {
		t.Errorf("decider row %+v", r)
	}
	cand := seen[0]["candidates"].([]map[string]any)[0]
	if !strings.Contains(fmt.Sprint(cand["implementation"]), "func ") || cand["path"] == "" {
		t.Errorf("the decider state lacks the implementation: %v", cand)
	}
	rows, _ = run("flash")
	if count(rows, "flash") == 0 || count(rows, "decider") != 0 || len(prompts) != 1 || !strings.Contains(prompts[0], "NO tools") {
		t.Errorf("flash mode: %v prompts %d", rows, len(prompts))
	}
	for _, r := range rows {
		if r.Item == "p#1" && r.Verdict != verdictReuse {
			t.Errorf("flash verdict %q", r.Verdict)
		}
	}
	rows, sum := run("both")
	if count(rows, "flash") == 0 || count(rows, "decider") != nItems || !strings.Contains(sum, "record only") {
		t.Errorf("both mode: %s", sum)
	}
}

func TestShadowStateIsCutUnderTheLimitAndTheRequestIsNotSent(t *testing.T) {
	ds, sp := shadowFixture(t)
	ic := ds.cands["p"][0]
	for i := range ic.Cands {
		ic.Cands[i].Lines = nil
		for l := 0; l < 80; l++ {
			ic.Cands[i].Lines = append(ic.Cands[i].Lines, strings.Repeat("x", 100))
		}
	}
	st, n, cut := shadowState(ic, "purpose", 3000)
	if n > 3000 || cut == "" {
		t.Errorf("state %d chars, cut %q", n, cut)
	}
	_ = st
	if _, n, cut := shadowState(ic, "purpose", 1<<20); cut != "" || n < len(ic.Cands)*80*100 {
		t.Errorf("an uncut state was reported cut: %d %q", n, cut)
	}
	body, line := deciderRequest(ds, sp, "m", "first", 40000)
	if !strings.Contains(string(body), `"questions"`) || !strings.Contains(string(body), `"state"`) || !strings.Contains(line, "state") {
		t.Errorf("request %s %s", body, line)
	}
	if b, _ := deciderRequest(ds, sp, "m", "zz#9", 40000); b != nil {
		t.Error("an unknown item gave a request")
	}
}

func TestPostDecisionsSendsTypedQuestionsAndReadsUsage(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "no", 401)
			return
		}
		w.Write([]byte(`{"model":"served","answers":{"relation":{"type":"choice","choice":"none","confidence":0.9}},"usage":{"cost":0.01,"total_tokens":5}}`))
	}))
	defer srv.Close()
	t.Setenv("ORCH_TRIAGE_URL", srv.URL)
	t.Setenv("ORCH_TRIAGE_KEY", "k")
	ans, model, u, err := postDecisions("m", map[string]any{"item": "x"}, deciderQuestions(2))
	if err != nil || model != "served" || ans["relation"].Choice != "none" || u.Tokens != 5 || got["model"] != "m" {
		t.Fatalf("%v %v %v %v %v", ans, model, u, err, got)
	}
	qs := got["questions"].(map[string]any)
	if best := qs["best_candidate"].(map[string]any)["criteria"].(map[string]any); len(best) != 3 {
		t.Errorf("best_candidate criteria %v", best)
	}
	if got, _ := probeDecider(t.TempDir(), "m", func(string, map[string]any, map[string]any) (map[string]decAnswer, string, shadowUsage, error) {
		return nil, "", shadowUsage{}, nil
	}); got != 256000 {
		t.Errorf("probe with an API that takes everything = %d", got)
	}
}

// ---- compare ----

func TestCompareShowsItemColumnsAgreementAndReferenceScores(t *testing.T) {
	items := []gateItem{{ID: "p#1", Package: "p", Text: "Build a retry queue for failed sends"}, {ID: "p#2", Package: "p", Text: "Add the gateway webhook table"}, {ID: "p#3", Package: "p", Text: "Create the settings page"}}
	a := &armData{name: "a", items: items, group: map[string]string{"p#1": "reuse", "p#2": "new", "p#3": "new"}, judges: map[string]map[string]string{"decider": {"p#1": "reuse", "p#2": "extend", "p#3": "new"}, "flash": {"p#1": "extend", "p#2": "extend"}}, finding: map[string]map[string]bool{},
		rows: []shadowRow{{Judge: "decider", Cost: 0.01, Seconds: 2, Tokens: 100}, {Judge: "flash", Cost: 0.001, Seconds: 1}}}
	b := &armData{name: "b", items: items, group: map[string]string{}, judges: map[string]map[string]string{}, finding: map[string]map[string]bool{"reuse": {"p#1": true, "p#3": true}}}
	a.locations = map[string]map[string]map[string]bool{}
	b.locations = map[string]map[string]map[string]bool{}
	a.addLocation("group", "reuse", "a.go:1")
	a.addLocation("decider", "reuse", "a.go:1")
	a.addLocation("decider", "extend", "b.go:2")
	b.addLocation("group", "reuse", "a.go:1")
	b.addLocation("group", "reuse", "c.go:3")
	refFile := filepath.Join(t.TempDir(), "ref.json")
	os.WriteFile(refFile, []byte(`[{"package":"p","item":"p#1","verdict":"duplicate","candidate":"a.go:1"},{"package":"p","item":"Add the gateway webhook table","verdict":"extend","candidate":"b.go:2","note":"add a column"},{"package":"p","item":"unrelated words entirely here","verdict":"new"}]`), 0o644)
	entries, err := loadReference(refFile)
	if err != nil || len(entries) != 3 {
		t.Fatalf("%v %v", entries, err)
	}
	ref := resolveReference(entries, items)
	if len(ref) != 3 || len(referenceIssues(ref)) != 3 {
		t.Fatalf("legacy references without revisions/maps must remain in the denominator, not fuzzy-match: %v", ref)
	}
	var sb strings.Builder
	printItemComparison(&sb, []*armData{a, b}, ref, true)
	out := sb.String()
	wantContains(t, out,
		`p#1 "Build a retry queue for failed sends" | A [group=reuse decider=reuse flash=extend] | B [group=- decider=- flash=-] | reference=-`,
		"legacy raw agreement A.group ~ A.decider: 2/3 (66%;",
		"legacy raw agreement A.decider ~ A.flash: 1/2 (50%;",
		`p#2 "Add the gateway webhook table": new vs extend`,
		"dependent quality unavailable",
		"answer coverage 0/3 (0%)",
		"judge A.decider: 1 rows",
		"legacy reference reuse, A.group (locations, not behavior quality): 1 found, 1 in the reference, 1 match; precision 100%, recall 100%",
		"legacy reference reuse, B.group (locations, not behavior quality): 2 found, 1 in the reference, 1 match; precision 50%, recall 100%",
		"legacy reference extend, A.decider (locations, not behavior quality): 1 found, 1 in the reference, 1 match; precision 100%, recall 100%")
	if _, err := loadReference(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Error("a missing reference file was accepted")
	}
	os.WriteFile(refFile, []byte(`[{"package":"p","item":"x","verdict":"maybe"}]`), 0o644)
	if _, err := loadReference(refFile); err == nil {
		t.Error("an unknown reference verdict was accepted")
	}
}

func TestGateCompareCommandReadsItemsShadowRowsAndReference(t *testing.T) {
	h := setup(t)
	gdir := verdictRepo(t, h)
	items := []gateItem{{ID: "api#1", Package: "api", Text: "Build api."}, {ID: "api#2", Package: "api", Text: "Add the POST /api/put endpoint"}}
	for i, name := range []string{"e1", "e2"} {
		dir := filepath.Join(gdir, "experiments", name)
		os.MkdirAll(dir, 0o755)
		rec := gateRecord{Spec: "spec.md", Experiment: name, State: "experiment", PassSeconds: map[string]int{}, PassTokens: map[string]passTokens{}}
		if i == 0 {
			rec.Items = []gateItemResult{{ID: "api#1", Package: "api", Text: "Build api.", Verdict: "reuse", Candidate: "internal/store.go:3", Verified: true}, {ID: "api#2", Package: "api", Text: items[1].Text, Verdict: "extend", Candidate: "internal/store.go:3", Verified: true}}
		}
		b, _ := json.Marshal(rec)
		os.WriteFile(filepath.Join(dir, "gate.json"), b, 0o644)
		ib, _ := json.Marshal(items)
		os.WriteFile(filepath.Join(dir, "items.json"), ib, 0o644)
		sd := shadowDir(gdir, name)
		os.MkdirAll(sd, 0o755)
		row, _ := json.Marshal(shadowRow{Item: "api#1", Package: "api", Judge: "decider", Verdict: "reuse", Cost: 0.002, Seconds: 3})
		os.WriteFile(filepath.Join(sd, "items.jsonl"), append(row, '\n'), 0o644)
	}
	ref := filepath.Join(h.tmp, "ref.json")
	os.WriteFile(ref, []byte(`[{"package":"api","item":"api#1","verdict":"duplicate","candidate":"internal/store.go:3"}]`), 0o644)
	out := h.must("gate", "compare", "spec.md", "e1", "e2", "--reference", ref)
	wantContains(t, out, "coverage A: items: 2, with a verdict 2", "per-item verdicts (2 items", `api#1 "Build api." | A [group=reuse decider=reuse flash=-] | B [group=- decider=reuse flash=-] | reference=-`,
		"reference A: 1 entries, 0 mapped to items; 1 unmapped/ambiguous", "judge B.decider: 1 rows", "legacy reference reuse, A.group (locations, not behavior quality): 1 found, 1 in the reference, 1 match; precision 100%, recall 100%")
	if code, out := h.run("gate", "compare", "spec.md", "e1", "e2", "--reference", filepath.Join(h.tmp, "nope.json")); code == 0 {
		t.Errorf("a missing reference file passed: %s", out)
	}
}

func TestGateShadowFlagRecordsRowsAndLeavesTheVerdictAlone(t *testing.T) {
	h := setup(t)
	t.Setenv("ORCH_GOPLS", filepath.Join(h.tmp, "no-gopls"))
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(h.tmp, "no-jbcontext"))
	os.MkdirAll(filepath.Join(h.repo, "internal"), 0o755)
	writeFiles(t, h.repo, map[string]string{"internal/store.go": "package internal\n\n// PutRecord stores a record.\nfunc PutRecord() {}\n"})
	h.commitSpec(planHeader + "### api: API\nkind: ship\nowns: internal/api.go\n\nAdds a helper.\n#### Builds\n- Builds the API.\n- Adds the PutRecord helper.\n  search: Code that stores records with `PutRecord`\n  reuse: internal/store.go#PutRecord\n#### Acceptance\nTests pass.\n" + planTail)
	gdir := filepath.Join(filepath.Dir(h.dir("x")), "gate", specHash(mustRead(t, filepath.Join(h.repo, "spec.md"))))
	h.fakePi(gatePasses(nil))
	t.Setenv("HERDR_ENV", "")
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.Write([]byte(`{"model":"d","answers":{"trigger_and_inputs":{"choice":"same","confidence":0.9},"outputs_and_effects":{"choice":"same","confidence":0.9},"relation":{"choice":"duplicate","confidence":0.9},"best_candidate":{"choice":"1","confidence":0.9},"placement":{"choice":"existing_location","confidence":0.9}}}`))
	}))
	defer srv.Close()
	t.Setenv("ORCH_TRIAGE_URL", srv.URL)
	t.Setenv("ORCH_TRIAGE_KEY", "k")

	base := h.must("gate", "spec.md")
	if posts != 0 || exists(filepath.Join(gdir, "shadow")) {
		t.Fatalf("the default run called the decider (%d) or wrote shadow rows", posts)
	}
	out := h.must("gate", "spec.md", "--shadow", "decider")
	if posts == 0 {
		t.Fatalf("no decider request:\n%s", out)
	}
	wantContains(t, out, "shadow decider: decider 2 rows", "record only")
	var row shadowRow
	lines := strings.Split(strings.TrimSpace(string(mustRead(t, filepath.Join(gdir, "shadow", "run", "items.jsonl")))), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"skipped":"no candidates"`) {
		t.Errorf("rows: %v", lines)
	}
	if err := json.Unmarshal([]byte(lines[1]), &row); err != nil || row.Judge != "decider" || row.Verdict != "reuse" || row.Item != "api#2" {
		t.Errorf("row %+v %v", row, err)
	}
	if strings.Split(base, "\n")[0] != strings.Split(out, "\n")[0] {
		t.Errorf("the shadow changed the verdict: %q vs %q", strings.Split(base, "\n")[0], strings.Split(out, "\n")[0])
	}
	if rec := readRecord(t, gdir); rec.Shadow == "" {
		t.Error("gate.json does not mention the shadow run")
	}
	// the dry run prints a request without sending it
	before := posts
	dry := h.must("gate", "spec.md", "--dry-run", "--dump-decider", "first")
	wantContains(t, dry, "decider request, NOT sent", `"state"`, `"implementation"`, "PutRecord", `"author_hint": true`, "candidate 1 (author hint)", `"relation"`, `"trigger_and_inputs"`, `"outputs_and_effects"`, `"placement"`)
	if posts != before {
		t.Error("--dump-decider sent a request")
	}
}

func TestJudgeVerdictClassesAndLegacyWords(t *testing.T) {
	for word, want := range map[string]string{
		"duplicate": verdictReuse, "variation_site": verdictExtend, "none": verdictNew,
		"yes": verdictReuse, "partly": verdictExtend, "no": verdictNew,
		"reuse": verdictReuse, "extend": verdictExtend, "new": verdictNew,
		" Duplicate ": verdictReuse, "other": "", "unknown": "", "": "",
	} {
		if got := judgeVerdict(word); got != want {
			t.Errorf("judgeVerdict(%q) = %q, want %q", word, got, want)
		}
	}
}

func TestDeciderQuestionSchemaAndWireOrder(t *testing.T) {
	want := []string{"trigger_and_inputs", "outputs_and_effects", "relation", "best_candidate", "placement"}
	for _, n := range []int{0, 1, 4, 6} {
		cs := make([]reuseCand, n)
		if n > 0 {
			cs[n-1].Hint = true
		}
		qs := deciderQuestions(n, cs...)
		if len(qs) != len(want) {
			t.Fatalf("questions: %v", qs)
		}
		for _, key := range want {
			if q, ok := qs[key].(map[string]any); !ok || q["type"] != "choice" {
				t.Errorf("question %s: %v", key, qs[key])
			}
		}
		best := qs["best_candidate"].(map[string]any)["criteria"].(map[string]any)
		if len(best) != n+1 || best["none"] != "no candidate decides it" {
			t.Errorf("best criteria: %v", best)
		}
		for i := 1; i <= n; i++ {
			text := fmt.Sprintf("candidate %d", i)
			if i == n {
				text += " (author hint)"
			}
			if best[fmt.Sprint(i)] != text {
				t.Errorf("candidate %d: %v", i, best)
			}
		}
		relation := qs["relation"].(map[string]any)["criteria"].(map[string]any)
		if len(relation) != 3 || relation["duplicate"] == nil || relation["variation_site"] == nil || relation["none"] == nil {
			t.Errorf("relation classes: %v", relation)
		}
		// Decode sequentially, rather than into a map, to check actual wire order.
		body := decisionBody("fake", nil, qs)
		var req struct{ Questions json.RawMessage }
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		d := json.NewDecoder(strings.NewReader(string(req.Questions)))
		d.Token()
		for _, key := range want {
			tok, err := d.Token()
			if err != nil || tok != key {
				t.Fatalf("wire order: got %v (%v), want %s: %s", tok, err, key, body)
			}
			var value any
			if err := d.Decode(&value); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestDerivedDuplicateIsDiagnostic(t *testing.T) {
	for _, tc := range []struct{ trigger, outputs, relation, derived, consistent string }{
		{"same", "same", "duplicate", "duplicate", "yes"},
		{"same", "same", "variation_site", "duplicate", "no"},
		{"same", "partly", "variation_site", "not_duplicate", "yes"},
		{"different", "same", "duplicate", "not_duplicate", "no"},
		{"cannot_tell", "same", "none", "not_duplicate", "yes"},
		{"same", "cannot_tell", "duplicate", "not_duplicate", "no"},
		{"cannot_tell", "cannot_tell", "duplicate", "", "no"},
		{"cannot_tell", "cannot_tell", "none", "", "yes"},
	} {
		a := map[string]string{"trigger_and_inputs": tc.trigger, "outputs_and_effects": tc.outputs, "relation": tc.relation}
		deriveDuplicate(a)
		if a["derived"] != tc.derived || a["consistent"] != tc.consistent || a["relation"] != tc.relation {
			t.Errorf("%+v: %v", tc, a)
		}
	}
	// A contradictory derived result must not override the recorded verdict.
	ic := itemCands{Item: gateItem{ID: "p#1"}, Cands: []reuseCand{{Path: "x.go", Start: 1}}}
	s := &shadowRun{dos: &dossierSet{}, sp: &spec{}, post: func(string, map[string]any, map[string]any) (map[string]decAnswer, string, shadowUsage, error) {
		return map[string]decAnswer{"trigger_and_inputs": {Choice: "same"}, "outputs_and_effects": {Choice: "same"}, "relation": {Choice: "none"}}, "fake", shadowUsage{}, nil
	}}
	r := s.decide("p", ic, 40000)
	if r.Verdict != verdictNew || r.Answers["consistent"] != "no" {
		t.Fatalf("derived check changed verdict: %+v", r)
	}
}

func TestHintCandidatesResolveOwnersAndMergeOnlyForShadows(t *testing.T) {
	code := reuseRepo(t)
	writeTree(t, code, map[string]string{
		"app/methods.go": "package app\ntype Birch struct{}\ntype Cedar struct{}\nfunc (b *Birch) Grow() {\n println(1)\n}\nfunc (c *Cedar) Grow() {\n println(2)\n}\n",
		"app/types.ts":   "class Birch {\n  grow() {\n    return 1;\n  }\n}\nclass Cedar {\n  grow() {\n    return 2;\n  }\n}\n",
	})
	e := newCtxEnv(code, ctxOpts{})
	ptrs := []string{"app/a.go#Alpha", "app/methods.go#Cedar.Grow", "app/types.ts#Cedar.grow", "app/b.go:3", "app/b.go:3-3", "missing.go#No", "app/a.go#No", "app/methods.go#Maple.Grow", "app/a.go:999", "app/a.go:4-2", "../outside.go:1", "app/a.go"}
	cs := e.hintCandidates(ptrs)
	if len(cs) != 4 {
		t.Fatalf("resolved hints: %+v", cs)
	}
	for i, want := range []string{"app/a.go:4", "app/methods.go:7-9", "app/types.ts:6-10", "app/b.go:3"} {
		if cs[i].at() != want || !cs[i].Hint || len(cs[i].Lines) == 0 {
			t.Errorf("hint %d = %+v, want %s with body", i, cs[i], want)
		}
	}
	original := cs[0]
	original.Hint = false
	ic := itemCands{Item: gateItem{ID: "p#1", Reuse: ptrs}, Cands: []reuseCand{original}}
	s := &shadowRun{dos: &dossierSet{e: e}, sp: &spec{}}
	merged := s.candidates(ic)
	if len(merged.Cands) != 4 || !merged.Cands[0].Hint || ic.Cands[0].Hint || len(ic.Cands) != 1 {
		t.Fatalf("merge mutated searched candidates or failed dedupe: %+v / %+v", ic, merged)
	}
	st, _, _ := shadowState(merged, "", 40000)
	for _, c := range st["candidates"].([]map[string]any) {
		if c["author_hint"] != true {
			t.Errorf("unlabelled state hint: %v", c)
		}
	}
	wantContains(t, flashEvidence(merged), "Candidate 1 (author hint): app/a.go:4", "Candidate 4 (author hint): app/b.go:3")
	// A hint alone is sufficient to show an item in a dry-run request.
	s.dos.cands = map[string][]itemCands{"p": {{Item: ic.Item}}}
	sp := &spec{packages: []*specPackage{{id: "p"}}}
	body, _ := deciderRequest(s.dos, sp, "fake", "first", 40000)
	wantContains(t, string(body), `"author_hint": true`, "author hint", "Cedar")
	// Search order wins; hints append after it and the shadow total is at most six.
	many := itemCands{Item: ic.Item}
	for i := 1; i <= 8; i++ {
		many.Cands = append(many.Cands, reuseCand{Path: "other.go", Start: i})
	}
	capped := s.candidates(many)
	if len(capped.Cands) != 6 || capped.Cands[5].Start != 6 || len(many.Cands) != 8 {
		t.Errorf("shadow cap/order: %+v", capped)
	}
}

func TestFlashPromptAndNewAndLegacyReplies(t *testing.T) {
	if len(flashJudgeExamples) > 1800 {
		t.Errorf("worked examples exceed budget: %d", len(flashJudgeExamples))
	}
	if strings.Count(flashJudgeExamples, "Candidate 1:") != 3 || strings.Count(flashJudgeExamples, `{"items":[`) != 3 {
		t.Error("three worked examples with JSON answers required")
	}
	for _, tc := range []struct{ name, reply, how, verdict, derived, consistent string }{
		{"new", `{"items":[{"item":"p#1","best_candidate":2,"candidate_does":"Saves bells.","trigger_inputs":"same","outputs_effects":"same","lacks":"nothing","class":"variation_site","placement":"existing_location","confidence":0.8}]}`, "Saves bells.", verdictExtend, "duplicate", "no"},
		{"old", `{"items":[{"item":"p#1","best_candidate":"2","how_implemented":"Old explanation.","similar_implementation":"similar","would_duplicate":"yes","where_to_implement":"existing_location","confidence":0.7}]}`, "Old explanation.", verdictReuse, "not_duplicate", "yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := reuseRepo(t)
			e := newCtxEnv(code, ctxOpts{})
			ic := itemCands{Item: gateItem{ID: "p#1", Reuse: []string{"app/b.go#Beta"}}, Cands: []reuseCand{{Path: "app/a.go", Start: 4}}}
			dos := &dossierSet{e: e, cands: map[string][]itemCands{"p": {ic}}}
			s := &shadowRun{dir: t.TempDir(), dos: dos, sp: &spec{}, runPass: func(p gatePass, sess string) (string, error) {
				if !p.noTools || !strings.HasPrefix(p.prompt, flashJudgeHead+"\n\n"+flashJudgeExamples) {
					t.Errorf("wrong flash prompt or tools: %+v", p)
				}
				wantContains(t, p.prompt, "Candidate 2 (author hint): app/b.go:3", "Worked example 1: duplicate", "Worked example 2: variation_site", "Worked example 3: none")
				return tc.reply, nil
			}}
			rows := s.flashPackage("p")
			if len(rows) != 1 {
				t.Fatalf("rows: %+v", rows)
			}
			r := rows[0]
			if r.Error != "" || r.Verdict != tc.verdict || r.How != tc.how || r.At != "app/b.go:3" || r.Best != "2" || r.Answers["placement"] != "existing_location" || r.Answers["derived"] != tc.derived || r.Answers["consistent"] != tc.consistent {
				t.Fatalf("parsed row: %+v", r)
			}
			if len(dos.cands["p"][0].Cands) != 1 {
				t.Error("flash changed dossier candidates")
			}
		})
	}
}

func TestComparePrefersBestAtAndFallsBackForLegacyRows(t *testing.T) {
	gdir := t.TempDir()
	dir := filepath.Join(gdir, "experiments", "e")
	os.MkdirAll(dir, 0o755)
	checklist := map[string][]itemCands{"p": {{Item: gateItem{ID: "p#1"}, Cands: []reuseCand{{Path: "searched.go", Start: 3}}}}}
	b, _ := json.Marshal(checklist)
	os.WriteFile(filepath.Join(dir, "checklist.json"), b, 0o644)
	sd := shadowDir(gdir, "e")
	os.MkdirAll(sd, 0o755)
	var rows strings.Builder
	for _, row := range []shadowRow{
		{Item: "p#1", Package: "p", Judge: "decider", Verdict: verdictReuse, Best: "1", At: "hint.go:7-10"},
		{Item: "p#1", Package: "p", Judge: "flash", Verdict: verdictExtend, Best: "1"},
	} {
		b, _ := json.Marshal(row)
		rows.Write(b)
		rows.WriteByte('\n')
	}
	os.WriteFile(filepath.Join(sd, "items.jsonl"), []byte(rows.String()), 0o644)
	a := loadArm(gdir, "e", dir, &gateRecord{})
	if !a.locations["decider"][verdictReuse]["hint.go:7-10"] || a.locations["decider"][verdictReuse]["searched.go:3"] || !a.locations["flash"][verdictExtend]["searched.go:3"] {
		t.Fatalf("compare locations: %+v", a.locations)
	}
}
