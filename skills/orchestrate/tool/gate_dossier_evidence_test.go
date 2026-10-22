package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDossierRelevantRegionBeyondFortyLines(t *testing.T) {
	lines := []string{"func dispatch() {"}
	for i := 1; i < 39; i++ {
		lines = append(lines, "  // setup")
	}
	lines = append(lines, "  runSelectedCommand()", "}")
	c := reuseCand{Path: "cmd/dispatch.go", Start: 1, End: 41, Total: 41, Lines: lines, Sig: lines[0], Via: []string{"runSelectedCommand"}}
	got := candidateText(1, c, true)
	if !strings.Contains(got, "runSelectedCommand()") {
		t.Fatalf("relevant region hidden: %s", got)
	}
}

func TestDossierPlannerSourcesDeduplicateAndKeepSelectors(t *testing.T) {
	code := rankRepo(t, map[string]string{"lib/dispatch.ts": "// CLI dispatch\nrunSelectedCommand();\n"})
	e := newCtxEnv(code, ctxOpts{})
	ds := newDossierSet(e, &spec{packages: []*specPackage{{id: "cli"}}}, map[string]string{"cli": "Command dispatch"})
	d := &ctxBrief{ID: "cli"}
	ds.docs["cli"] = d
	zero := 0
	ds.runLookups([]string{"cli"}, []lookup{{Package: "cli", ctxLookup: ctxLookup{Kind: "text", Query: "runSelectedCommand", Path: "lib/dispatch.ts", Fixed: true, Word: true, Glob: strList{"lib/*.ts"}, Context: &zero, Tests: "include", Gen: "include"}}, {Package: "cli", ctxLookup: ctxLookup{Kind: "read", Path: "lib/dispatch.ts", From: 2, To: 2}}})
	got := ds.text("cli")
	if strings.Count(got, "runSelectedCommand();") != 1 {
		t.Fatalf("planned body repeated: %s", got)
	}
	m := ds.manifest(nil)
	if len(m.Evidence) != 1 {
		t.Fatalf("expected one source, got %+v", m.Evidence)
	}
	for _, l := range m.Lookups {
		if err := l.Validate(evidenceEnvelope{Completeness: evidencePartial}); err != nil {
			t.Fatalf("invalid planner lookup %s: %v", l.ID, err)
		}
	}
	wantContains(t, d.Lookups[0].Scope, `"path":"lib/dispatch.ts"`, `"fixed":true`, `"word":true`, `"glob":["lib/*.ts"]`, `"tests":"include"`, `"generated":"include"`)
	_ = d.Render("## CLI", "package", 600)
	found := false
	for _, om := range d.Omissions {
		if om.EvidenceID == m.Evidence[0].ID {
			found = true
			wantContains(t, om.Expansion, "lib/dispatch.ts", "offset=2", "limit=1")
		}
	}
	if !found {
		t.Fatal("omitted planned body has no native expansion")
	}
}

func TestDossierPlannerScopeSelectorsArePartOfLookupIdentity(t *testing.T) {
	code := rankRepo(t, map[string]string{"lib/one.ts": "dispatch_selected();\n", "lib/two.go": "dispatch_selected()\n"})
	ds := newDossierSet(newCtxEnv(code, ctxOpts{}), &spec{packages: []*specPackage{{id: "cli"}}}, map[string]string{"cli": "Command dispatch"})
	ds.docs["cli"] = &ctxBrief{ID: "cli"}
	ds.runLookups([]string{"cli"}, []lookup{{Package: "cli", ctxLookup: ctxLookup{Kind: "text", Query: "dispatch_selected", Glob: strList{"lib/*.ts"}, Fixed: true}}, {Package: "cli", ctxLookup: ctxLookup{Kind: "text", Query: "dispatch_selected", Glob: strList{"lib/*.go"}, Fixed: true}}})
	lookups := ds.docs["cli"].Lookups
	if len(lookups) != 2 || lookups[0].ID == lookups[1].ID {
		t.Fatalf("different scoped requests collapsed: %+v", lookups)
	}
	wantContains(t, lookups[0].Scope, "lib/*.ts")
	wantContains(t, lookups[1].Scope, "lib/*.go")
}

func TestBriefDossierSummaryPreservesLegacySheet(t *testing.T) {
	code := rankRepo(t, map[string]string{"cli/main.py": "def dispatch_command(argv):\n    return argv[0]\n"})
	e := newCtxEnv(code, ctxOpts{})
	pl := e.prepareBrief(briefInput{ID: "cli", Text: "Use dispatch_command", Owns: []string{"cli/main.py"}})
	f := &symFact{Symbol: "dispatch_command", Lang: "python", Tier: "fixture", Defs: []symDef{{Where: "cli/main.py:1", Kind: "function", Match: "exact"}}}
	pl.goSyms = []string{"dispatch_command"}
	pl.facts["dispatch_command"] = f
	d := e.assemble(pl)
	var legacy strings.Builder
	e.symView(*f, symOpts{Compact: true}).write(&legacy, ctxFmt{Compact: true}, nil, 0, 0)
	if len(d.Sheet) != 1 || d.Sheet[0] != strings.TrimRight(legacy.String(), "\n") {
		t.Fatalf("legacy Sheet changed: %v", d.Sheet)
	}
	if len(d.interfaceSummary) != 1 || !strings.Contains(d.interfaceSummary[0], "signature: def dispatch_command") {
		t.Fatal("dossier has no native signature summary")
	}
}

func TestReuseQueriesCountCompletionNotScheduling(t *testing.T) {
	code := rankRepo(t, map[string]string{"lib/commands.go": "package lib\nfunc Dispatch() {}\n"})
	bin := filepath.Join(t.TempDir(), "jbcontext")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho unavailable >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCH_JBCONTEXT", bin)
	e := newCtxEnv(code, ctxOpts{})
	items := []gateItem{{ID: "cli#dispatch", Queries: []string{"Code that dispatches a selected subcommand"}}, {ID: "cli#none"}}
	res := e.reuseCandidates(items, []string{"cli/"}, "CLI")
	if res[0].QueriesRun != 0 {
		t.Fatal("failed query counted as completed")
	}
	failed := false
	for _, l := range res[0].Lookups {
		failed = failed || l.ExecutionStatus == lookupFailed
	}
	if !failed || res[1].Lookups[0].ExecutionStatus != lookupNotRequested {
		t.Fatalf("states collapsed: %+v", res)
	}
}

// jbOrderFake is a jbcontext stand-in for `search`. The query "q<k>" gets one hit
// in lib/f<k>.go after <dir>/delay.<k> seconds (0.05 when absent), or an error
// when <dir>/fail.<k> exists, and answers 429 to the first <dir>/limit calls. With <dir>/onefile every query hits lib/f0.go, q0
// at line 1 and any other at line 2. It logs the number of searches running at
// once to conc.log and the order the answers left in done.log.
func jbOrderFake(t *testing.T, n int, slots int) (code, dir string) {
	t.Helper()
	files := map[string]string{}
	for k := 0; k < n; k++ {
		files[fmt.Sprintf("lib/f%d.go", k)] = "package lib\nfunc F() {}\n"
	}
	code = rankRepo(t, files)
	dir = t.TempDir()
	bin := filepath.Join(dir, "jbcontext")
	script := `#!/bin/sh
D="` + dir + `"
[ "$1" = search ] || exit 0
for last; do :; done
k=${last#q}
echo "$$" >> "$D/calls.log"
if [ -f "$D/limit" ] && [ "$(wc -l < "$D/calls.log" | tr -d ' ')" -le "$(cat "$D/limit")" ]; then
  echo "Limit reached. Please try again later (HTTP 429)" >&2
  exit 1
fi
mkdir "$D/run.$$"
n=$(ls "$D" | grep -c '^run\.')
echo "$n" >> "$D/conc.log"
sleep "$(cat "$D/delay.$k" 2>/dev/null || echo 0.05)"
echo "$k" >> "$D/done.log"
rmdir "$D/run.$$"
if [ -f "$D/fail.$k" ]; then echo "jbcontext exploded" >&2; exit 1; fi
f=$k; c='package lib'; l=1
if [ -f "$D/onefile" ]; then
  f=0
  if [ "$k" != 0 ]; then c='func F() {}'; l=2; fi
fi
printf '{"type":"search_result","results":[{"result":{"sourcePosition":{"relativePath":"lib/f%s.go"}},"content":"%s\\n","contentStartLine":%s}]}\n' "$f" "$c" "$l"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCH_JBCONTEXT", bin)
	t.Setenv("ORCH_JB_SLOTS", fmt.Sprint(slots))
	t.Setenv("ORCH_JB_BACKOFF_MS", "10")
	return code, dir
}

func conceptJobs(n int) []dossierJob {
	var jobs []dossierJob
	for k := 0; k < n; k++ {
		jobs = append(jobs, dossierJob{name: fmt.Sprintf("q%d", k)})
	}
	return jobs
}

func TestRunJobsReturnsAnswersInJobOrderWhateverFinishesFirst(t *testing.T) {
	code, dir := jbOrderFake(t, 10, 4)
	for k := 0; k < 10; k++ { // the earlier the job, the later its answer
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("delay.%d", k)), []byte(fmt.Sprintf("0.%d", 9-k)), 0o644)
	}
	outs := newCtxEnv(code, ctxOpts{}).runJobs(conceptJobs(10))
	for k, o := range outs {
		if o.err != nil || len(o.hits) != 1 || o.hits[0].Path != fmt.Sprintf("lib/f%d.go", k) {
			t.Fatalf("job %d got %+v", k, o)
		}
	}
	done := jbRateLines(dir, "done.log")
	if len(done) != 10 || strings.Join(done, " ") == "0 1 2 3 4 5 6 7 8 9" {
		t.Fatalf("the searches did not finish out of order, so the test proves nothing: %v", done)
	}
}

func TestRunJobsNeverRunsMoreSearchesThanTheSlotCap(t *testing.T) {
	for _, slots := range []int{2, 7} {
		t.Run(fmt.Sprint(slots), func(t *testing.T) {
			code, dir := jbOrderFake(t, 24, slots)
			os.WriteFile(filepath.Join(dir, "delay.0"), []byte("0.2"), 0o644)
			newCtxEnv(code, ctxOpts{}).runJobs(conceptJobs(24))
			if got := jbRateMax(dir); got > slots || got < min(slots, 2) {
				t.Fatalf("most searches at once = %d, cap %d", got, slots)
			}
		})
	}
}

func TestRunJobsReportsAFailedLookupInItsOwnPlace(t *testing.T) {
	code, dir := jbOrderFake(t, 6, 3)
	os.WriteFile(filepath.Join(dir, "fail.3"), nil, 0o644)
	outs := newCtxEnv(code, ctxOpts{}).runJobs(conceptJobs(6))
	for k, o := range outs {
		switch {
		case k == 3 && (o.err == nil || len(o.hits) != 0):
			t.Fatalf("job 3 should have failed: %+v", o)
		case k != 3 && (o.err != nil || len(o.hits) != 1 || o.hits[0].Path != fmt.Sprintf("lib/f%d.go", k)):
			t.Fatalf("job %d got %+v", k, o)
		}
	}
}

func TestRunJobsAsksAgainForSearchesTheServiceRefused(t *testing.T) {
	code, dir := jbOrderFake(t, 8, 4)
	os.WriteFile(filepath.Join(dir, "limit"), []byte("12"), 0o644)
	waits := 0
	old := dossierCooldownWait
	dossierCooldownWait = func(bin string) {
		waits++
		jbCoolMu.Lock()
		delete(jbCoolUntil, bin)
		jbCoolMu.Unlock()
	}
	defer func() { dossierCooldownWait = old }()
	outs := newCtxEnv(code, ctxOpts{}).runJobs(conceptJobs(8))
	for k, o := range outs {
		if o.err != nil || len(o.hits) != 1 || o.hits[0].Path != fmt.Sprintf("lib/f%d.go", k) {
			t.Fatalf("job %d got %+v", k, o)
		}
	}
	if waits == 0 {
		t.Fatal("no search was refused, so the test proves nothing")
	}
}

func TestReuseCandidatesAreTheSameWhicheverSearchFinishesFirst(t *testing.T) {
	var runs []string
	for _, firstIsSlow := range []bool{true, false} {
		code, dir := jbOrderFake(t, 1, 4)
		os.WriteFile(filepath.Join(dir, "onefile"), nil, 0o644)
		slow, fast := "0", "1"
		if !firstIsSlow {
			slow, fast = fast, slow
		}
		os.WriteFile(filepath.Join(dir, "delay."+slow), []byte("0.5"), 0o644)
		os.WriteFile(filepath.Join(dir, "delay."+fast), []byte("0.01"), 0o644)
		items := []gateItem{{ID: "p#1", Package: "p", Text: "Build it.", Queries: []string{"q0", "q1"}, Kind: "builds"}}
		got := newCtxEnv(code, ctxOpts{}).reuseCandidates(items, []string{"mine/"}, "it")
		if len(got) != 1 || len(got[0].Cands) != 2 || got[0].Cands[0].MatchLine != 1 || got[0].Cands[1].MatchLine != 2 {
			t.Fatalf("candidates are not in query order: %+v", got)
		}
		runs = append(runs, fmt.Sprint(got[0].Cands, got[0].QueriesRun))
	}
	if runs[0] != runs[1] {
		t.Fatalf("the candidates changed with the finishing order:\n%s\n%s", runs[0], runs[1])
	}
}

func TestDossierLookupsCarryWhenTheyStartedAndHowLongTheyRan(t *testing.T) {
	code, dir := jbOrderFake(t, 2, 4)
	os.WriteFile(filepath.Join(dir, "delay.1"), []byte("0.3"), 0o644)
	e := newCtxEnv(code, ctxOpts{})
	items := []gateItem{{ID: "p#1", Package: "p", Text: "Build it.", Queries: []string{"q0", "q1"}, Kind: "builds"}}
	ds := newDossierSet(e, &spec{packages: []*specPackage{{id: "p"}}}, map[string]string{"p": "Build it."})
	ds.docs["p"] = &ctxBrief{ID: "p"}
	ds.cands["p"] = e.reuseCandidates(items, []string{"mine/"}, "it")
	b, err := json.Marshal(ds.manifest(nil))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Lookups      []struct{ ID, Unit string }
		LookupTiming []struct {
			ID        string `json:"id"`
			StartedAt string `json:"started_at"`
			ElapsedMS *int64 `json:"elapsed_ms"`
		} `json:"lookup_timing"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, l := range m.Lookups {
		if l.Unit == "concept" {
			ids[l.ID] = true
		}
	}
	slowest := int64(0)
	for _, tm := range m.LookupTiming {
		if !ids[tm.ID] || tm.StartedAt == "" || tm.ElapsedMS == nil {
			t.Fatalf("bad timing entry %+v", tm)
		}
		delete(ids, tm.ID)
		slowest = max(slowest, *tm.ElapsedMS)
	}
	if len(ids) != 0 || slowest < 300 {
		t.Fatalf("lookups without timing: %v; slowest %d ms", ids, slowest)
	}
	if strings.Contains(string(b), `"Timing"`) {
		t.Fatal("the timing leaked into the lookup records")
	}
}

func readyCLIPacket() *gateQuestionPacket {
	return &gateQuestionPacket{Goal: "Make command dispatch predictable", KnownFacts: []string{"The parser accepts --help and rejects an empty command."}, Unknown: "Which empty-command behavior should ship?", Options: []gatePacketOption{{"a", "Print help", "Shows available commands", "More stdout output"}, {"b", "Return an error", "Scripts detect missing input", "Users must request help separately"}}, Recommendation: "a", Rationale: "The owner wants discoverable commands", Authority: "Owner decides user-visible defaults", NextAction: "Owner selects an option, then the lead adds CLI acceptance tests"}
}

func TestDossierLookupExecutionStatesAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name string
		lk   evidenceLookup
		want string
	}{
		{"failed", dossierLookup("dispatch", "cli", "concept", 0, 4, fmt.Errorf("index unavailable"), ""), "failed status=failed"},
		{"empty", dossierLookup("dispatch", "cli", "text", 0, 0, nil, ""), "succeeded status=empty"},
		{"unavailable", dossierLookup("dispatch", "cli", "concept", 0, 4, nil, "backend missing"), "unavailable completeness=unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.lk.Validate(evidenceEnvelope{Completeness: evidenceUnknown}); err != nil {
				t.Fatal(err)
			}
			got := itemText(itemCands{Item: gateItem{ID: "cli#dispatch"}, Queries: []string{"dispatch"}, Lookups: []evidenceLookup{tc.lk}}, false, "")
			wantContains(t, got, tc.want, "scope=cli")
		})
	}
	l := dossierLookup("dispatch", "cli", "concept", 0, 4, nil, "")
	if l.ExecutionStatus != lookupSucceeded || l.CountKind != evidenceCountUnknown || l.Completeness != evidencePartial {
		t.Fatalf("semantic window manufactured absence: %+v", l)
	}
}

func TestDossierOneBudgetIncludesEverySectionAndHasExpansionPaths(t *testing.T) {
	d := &ctxBrief{ID: "cli", Files: []string{strings.Repeat("ranked ", 100)}, Docs: []string{strings.Repeat("docs ", 200)}, Sheet: []string{strings.Repeat("signature ", 200)}, Like: []string{strings.Repeat("candidate ", 200)}, Planned: []string{strings.Repeat("planner ", 1000)}, ReuseItems: bigCands(4)}
	for _, budget := range []int{500, 2000, 6000} {
		got := d.Render("## CLI", "package", budget)
		if len(got) > budget {
			t.Fatalf("budget %d produced %d bytes", budget, len(got))
		}
		if len(d.Omissions) == 0 {
			t.Fatal("missing omission manifest")
		}
		for _, om := range d.Omissions {
			if om.EvidenceID == "" || !strings.Contains(om.Expansion, "Read path=") {
				t.Fatalf("no stable expansion: %+v", om)
			}
		}
		again := d.Render("## CLI", "package", budget)
		if again != got {
			t.Fatal("render changed without new evidence")
		}
	}
}

func TestDossierSharedBodyPreservesDistinctRequirementRelationships(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"lib/cli.py": "def dispatch_command(argv):\n    return argv[0]\n"})
	e := newCtxEnv(root, ctxOpts{})
	c := e.candidateAt("lib/cli.py", &itemEv{line: 2, end: 2})
	ics := []itemCands{{Item: gateItem{ID: "cli#parse", Revision: "one"}, Cands: []reuseCand{c}}, {Item: gateItem{ID: "cli#run", Revision: "two"}, Cands: []reuseCand{c}}}
	d := &ctxBrief{ReuseItems: ics}
	ds := &dossierSet{e: e, docs: map[string]*ctxBrief{"cli": d}, cands: map[string][]itemCands{"cli": ics}}
	got := ds.text("cli")
	wantContains(t, got, "cli#parse", "cli#run", c.EvidenceID, "also for #run", "also: `lib/cli.py:1` (listed under #parse)")
	if strings.Count(got, "| def dispatch_command") != 1 {
		t.Fatalf("body repeated: %s", got)
	}
	m := ds.manifest(nil)
	if len(m.Evidence) != 1 || len(m.Requirements) != 2 || m.Requirements[0].EvidenceIDs[0] != m.Requirements[1].EvidenceIDs[0] {
		t.Fatalf("relationships lost: %+v", m)
	}
}

func TestDossierManifestMergesRenderedObservationsAcrossPackages(t *testing.T) {
	code := rankRepo(t, map[string]string{"lib/cli.py": "def dispatch(argv):\n    return '" + strings.Repeat("x", 6000) + "'\n"})
	e := newCtxEnv(code, ctxOpts{})
	c := e.candidateAt("lib/cli.py", &itemEv{line: 1, end: 2})
	a := &ctxBrief{ReuseItems: []itemCands{{Item: gateItem{ID: "a#dispatch"}, Cands: []reuseCand{c}}}}
	b := &ctxBrief{ReuseItems: []itemCands{{Item: gateItem{ID: "b#dispatch"}, Cands: []reuseCand{c}}}}
	for i := 0; i < 300; i++ {
		a.Files = append(a.Files, strings.Repeat("metadata ", 20))
	}
	ds := &dossierSet{e: e, docs: map[string]*ctxBrief{"a": a, "b": b}, cands: map[string][]itemCands{"a": a.ReuseItems, "b": b.ReuseItems}}
	m := ds.manifest(nil)
	if a.Shown[c.EvidenceID] || !b.Shown[c.EvidenceID] {
		t.Fatal("fixture did not exercise a later rendered source")
	}
	if len(m.Evidence) != 1 || m.Evidence[0].Rendered == nil || !*m.Evidence[0].Rendered {
		t.Fatalf("first omission erased later rendering: %+v", m.Evidence)
	}
}

func TestEnrichPacketsReuseReviewFieldsAndGateReadiness(t *testing.T) {
	for _, tc := range []struct {
		name    string
		packet  *gateQuestionPacket
		failure string
		want    string
	}{{"ready", readyCLIPacket(), "", "ready"}, {"incomplete", nil, "", "missing_tradeoffs"}, {"failed", readyCLIPacket(), "Code search failed: backend exited", "failed_retrieval"}} {
		t.Run(tc.name, func(t *testing.T) {
			q := gateQuestion{Package: "cli", Question: "Which empty-command behavior?", Decisive: true, Packet: tc.packet}
			src := enrichedCard(enrichedQuestion{Q: q, Tag: "owner", CodeReview: tc.failure}, "spec.md")
			r := checkCardReady(src)
			if r.Status != tc.want {
				t.Fatalf("ready=%+v card=%s", r, src)
			}
			if tc.packet != nil {
				wantContains(t, src, tc.packet.Goal, tc.packet.Rationale, tc.packet.NextAction)
			}
		})
	}
}

func TestDossierAdversaryCoveragePersistsWithoutChangingTheReview(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{`{"findings":[]}`, "coverage: not reported"}, {`{"findings":[],"coverage_gaps":[]}`, "coverage: reviewer reported no further gaps"}, {`{"findings":[],"coverage_gaps":["incoming registrations not checked: turn budget"]}`, "reported coverage gap: incoming registrations not checked"}} {
		calls := 0
		apply := func(raw string, rec *gateRecord, repo string, sp *spec) error {
			calls++
			if raw != tc.raw {
				t.Fatal("coverage wrapper changed the review reply")
			}
			return nil
		}
		p := newGatePass("adversary-a", "adversary", "stable spec", "body", "TAG", "Inspect"+adversaryScope([]string{"cmd/main.go"}), `{"findings":[]}`, apply)
		p = gateEvidenceContract(p)
		p = gateEvidenceContract(p)
		if strings.Contains(p.task, "only when one of these imports") {
			t.Fatal("forward-only exploration rule survived")
		}
		rec := &gateRecord{}
		if err := p.apply(tc.raw, rec, "", nil); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || len(rec.Notes) != 1 || !strings.Contains(rec.Notes[0], "adversary-a "+tc.want) {
			t.Fatalf("coverage lost or wrapper repeated: %v calls=%d", rec.Notes, calls)
		}
	}
}

func TestDossierStablePrefixClassesIgnoreGroupOrder(t *testing.T) {
	shared := "same byte-identical spec and package index"
	for _, class := range []string{"adversary", "group"} {
		a := newGatePass("first", class, shared, "group B", "TAG", "task B", `{"questions":[]}`, nil)
		b := newGatePass("second", class, shared, "group A", "TAG", "task A", `{"questions":[]}`, nil)
		a = gateEvidenceContract(a)
		b = gateEvidenceContract(b)
		originalShared, _ := gatePromptParts(shared, "group B", "TAG", "task B", `{"questions":[]}`)
		if a.shared != originalShared {
			t.Fatal("evidence hook changed original shared prefix bytes")
		}
		if a.shared != b.shared || !strings.HasPrefix(a.prompt, a.shared) || !strings.HasPrefix(b.prompt, b.shared) {
			t.Fatal("per-group state contaminated shared prefix")
		}
		if class == "adversary" {
			wantContains(t, a.task, "incoming AND outgoing", "registrations/configuration", "tests", "coverage_gaps")
		}
	}
}

func TestGateDossierCLIHasUsefulPythonAndTypeScriptContextWithoutFormatterCalls(t *testing.T) {
	for _, lang := range []string{"py", "ts"} {
		t.Run(lang, func(t *testing.T) {
			h := setup(t)
			t.Setenv("HERDR_ENV", "")
			t.Setenv("ORCH_GOPLS", filepath.Join(h.tmp, "no-gopls"))
			t.Setenv("ORCH_TSLS", filepath.Join(h.tmp, "no-tsls"))
			path := "lib/dispatch." + lang
			body := "def dispatch_command(argv):\n    return argv[0]\n"
			snippet := `def dispatch_command(argv):\n    return argv[0]`
			if lang == "ts" {
				body = "export function dispatch_command(argv: string[]): string {\n  return argv[0];\n}\n"
				snippet = `export function dispatch_command(argv: string[]): string {\n  return argv[0];\n}`
			}
			writeTree(t, h.repo, map[string]string{path: body, "cli/main." + lang: body})
			src := planHeader + "### cli: Command dispatcher\nkind: ship\nowns: cli/main." + lang + "\n\nExpose `dispatch_command` from " + path + " to command-line users.\n#### Builds\n- Dispatch the selected subcommand.\n  id: dispatch\n  search: Code that dispatches the selected command using `dispatch_command`\n#### Acceptance\nTests pass.\n" + planTail
			h.commitSpec(src)
			fakeJbResults(t, [][3]string{{path, "1", snippet}})
			q := gateQuestion{Question: "Which default behavior should `dispatch_command` use when a command is missing?", Decisive: true, Packet: readyCLIPacket()}
			answer, _ := json.Marshal(map[string]any{"id": "cli", "will_change": []string{}, "claims": []any{}, "questions": []gateQuestion{q}, "reuse": []any{}})
			dir := h.fakePi(gatePasses(map[string]string{"package-cli": string(answer)}))
			code, out := h.run("gate", "spec.md", "--dossier", "code")
			if code != 1 {
				t.Fatalf("gate exit=%d %s", code, out)
			}
			gdir := filepath.Join(runRoot(h.repo), "gate", specHash([]byte(src)))
			rec := readRecord(t, gdir)
			wantContains(t, strings.Join(rec.Notes, "\n"), "adversary-a coverage: not reported", "adversary-b coverage: not reported")
			if rec.DossierManifest == nil {
				t.Fatal("manifest not persisted in gate.json")
			}
			wantLang := "python"
			if lang == "ts" {
				wantLang = "typescript"
			}
			foundLang := false
			for _, er := range rec.DossierManifest.Evidence {
				foundLang = foundLang || er.Language == wantLang
			}
			if !foundLang {
				t.Fatalf("no %s source binding in manifest", wantLang)
			}
			if err := rec.DossierManifest.evidencePacket.Validate(); err != nil {
				t.Fatalf("invalid shared evidence packet: %v", err)
			}
			text := string(mustRead(t, filepath.Join(gdir, "dossier", "cli.md")))
			wantContains(t, text, "cli#dispatch", path, "dispatch_command", "unverified")
			if len(text) > dossierTotal {
				t.Fatal("CLI dossier over budget")
			}
			packetJSON := mustRead(t, filepath.Join(gdir, "questions", "packets.json"))
			toon := mustRead(t, filepath.Join(gdir, "questions", "packets.toon"))
			if fmt.Sprint(decodedOutput(t, toon, outputAgent)) != fmt.Sprint(decodedOutput(t, packetJSON, outputHuman)) {
				t.Fatal("human/agent packet selection differs")
			}
			var before bytes.Buffer
			before.Write(mustRead(t, filepath.Join(dir, "calls.log")))
			ask := h.must("ask", "--cards", filepath.Join(gdir, "questions"))
			wantContains(t, ask, "questions: 1", "Print help", "Return an error")
			h.must("gate", "enrich", "spec.md")
			if before.String() != string(mustRead(t, filepath.Join(dir, "calls.log"))) {
				t.Fatal("rendering/enrichment made a formatter model request")
			}
			cache, _ := os.ReadFile(filepath.Join(dir, "cache.log"))
			if len(strings.Fields(string(cache))) > 3 {
				t.Fatal("unexpected additional model calls")
			}
		})
	}
}

func TestDossierZeroQueriesNotCompletedEmpty(t *testing.T) {
	got := itemText(itemCands{Item: gateItem{ID: "cli#dispatch", Text: "Dispatch the selected command"}}, true, "")
	if !strings.Contains(got, "not_requested") || strings.Contains(got, "no candidates found by code") {
		t.Fatalf("zero-query item claims search: %s", got)
	}
}
