package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitBuildQueriesAndPointers(t *testing.T) {
	text := "- Saves a payment method after checkout; a replay\n  refreshes its details.\n  search: Code that saves a payment method\n  search: Code that refreshes display details\n  reuse: store.go#Save, data.sql:2-4\n- Lists gateways.\n"
	got, problems := parseBuilds(text)
	if len(problems) != 0 || len(got) != 2 || got[0].Text != "Saves a payment method after checkout; a replay refreshes its details." || len(got[0].Queries) != 2 || len(got[0].Reuse) != 2 || len(got[1].Queries) != 0 {
		t.Fatalf("%+v %v", got, problems)
	}
	var old gateItem
	if err := json.Unmarshal([]byte(`{"id":"p#1","reuse":"store.go:3"}`), &old); err != nil || len(old.Reuse) != 1 {
		t.Fatalf("legacy items: %+v %v", old, err)
	}
	p := &specPackage{id: "p", body: "Body", builds: got}
	task := specText(p)
	wantContains(t, task, "store.go#Save, data.sql:2-4", "search: Code that refreshes display details")
}

func TestItemsAreNeverInferredAndNoQueriesMeansNoSearch(t *testing.T) {
	p := &specPackage{id: "p", body: "Builds a queue", acceptance: "- Add a sender"}
	if len(packageItems(p)) != 0 {
		t.Fatal("inferred items")
	}
	code := reuseRepo(t)
	e := newCtxEnv(code, ctxOpts{})
	got := e.reuseCandidates([]gateItem{{ID: "p#1", Package: "p", Text: "Build `Alpha`"}}, nil, "")
	if len(got) != 1 || len(got[0].Cands) != 0 || len(got[0].Queries) != 0 {
		t.Fatalf("unrequested search: %+v", got)
	}
}

func TestSymbolAndRangePointers(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{
		"store.go": "package store\ntype Store struct{}\nfunc Save() {}\nfunc (s *Store) Save() {}\nvar called = Save\n",
		"data.odd": "the Token lives here\n",
		"view.ts":  "export class View {\n  render() { return 1; }\n}\n",
	})
	for _, ptr := range []string{"store.go#Save", "store.go#Store.Save", "store.go:2-4", "view.ts#View.render"} {
		note, err := checkPointer(repo, ptr)
		if err != nil || note != "" {
			t.Errorf("%s: %s %v", ptr, note, err)
		}
	}
	for _, ptr := range []string{"store.go#Absent", "store.go#Wrong.Save", "store.go:9", "store.go:4-2", "missing.go#Save", "../outside.go:1"} {
		_, err := checkPointer(repo, ptr)
		if err == nil || !strings.Contains(err.Error(), ptr) {
			t.Errorf("%s: %v", ptr, err)
		}
	}
	note, err := checkPointer(repo, "data.odd#Token")
	if err != nil || !strings.Contains(note, "word match") {
		t.Fatalf("fallback %s %v", note, err)
	}
	files := specFiles(repo, "store.go#Store.Save, view.ts#View.render, store.go:2")
	if strings.Join(files, ",") != "store.go,view.ts" {
		t.Fatal(files)
	}
}

func TestAdaptiveCandidatesAndDeduplicatedBodies(t *testing.T) {
	for n, want := range map[int]int{0: 3, 1: 3, 8: 3, 9: 2, 20: 2} {
		if got := candidatesPerQuery(n); got != want {
			t.Errorf("%d: %d want %d", n, got, want)
		}
	}
	c := reuseCand{Path: "x.go", Start: 2, End: 3, Lines: []string{"func Save() {", "}"}, Total: 2}
	ics := []itemCands{{Item: gateItem{ID: "p#1", Text: "one"}, Cands: []reuseCand{c}}, {Item: gateItem{ID: "p#2", Text: "two"}, Cands: []reuseCand{c}}, {Item: gateItem{ID: "p#5", Text: "five"}, Cands: []reuseCand{c}}}
	got := renderChecklist(ics, nil)
	if strings.Count(got, "| func Save()") != 1 {
		t.Fatal(got)
	}
	wantContains(t, got, "also for #2, #5", "candidate `x.go:2-3` shown above")
}

func TestSpecQueriesParserGenerationAndSelection(t *testing.T) {
	dir := t.TempDir()
	src := planHeader + "### api: API\nkind: ship\nowns: api/\n\nBody.\n#### Builds\n- Saves methods.\n  search: Code that saves methods\n- Lists gateways.\n  reuse: store.go#List\n#### Acceptance\nTests.\n\n### legacy: Old\nkind: ship\nowns: old/\n\nMakes old things.\n#### Acceptance\nTests old things.\n" + planTail
	if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errs strings.Builder
	a := &app{cwd: dir, out: &out, errw: &errs}
	run := func(args ...string) (int, string) {
		out.Reset()
		errs.Reset()
		code := a.run(append([]string{"spec", "queries", "spec.md"}, args...))
		return code, out.String() + errs.String()
	}
	if code, s := run("--package", "api"); code != 0 || !strings.Contains(s, "api#2: Lists gateways.") {
		t.Fatalf("%d %s", code, s)
	}
	if code, s := run("--item", "api#2", "--json"); code != 0 || !strings.Contains(s, `"reuse":["store.go#List"]`) || strings.Contains(s, "api#1") {
		t.Fatalf("%d %s", code, s)
	}
	if code, s := run("--generate"); code != 2 || !strings.Contains(s, "explicit --all") {
		t.Fatalf("%d %s", code, s)
	}
	if code, s := run("--item", "api#9"); code == 0 || !strings.Contains(s, "no matching") {
		t.Fatalf("%d %s", code, s)
	}
	if code, s := run("--package", "api", "--generate", "--dry-run"); code != 0 || strings.Contains(s, "- Saves methods.") || !strings.Contains(s, "- Lists gateways.") {
		t.Fatalf("%d %s", code, s)
	}
	old := queryModelRunner
	defer func() { queryModelRunner = old }()
	calls := 0
	queryModelRunner = func(role, prompt string) (string, error) {
		calls++
		if role != "spec_queries" || !strings.Contains(prompt, "Makes old things.") {
			t.Fatalf("%s %s", role, prompt)
		}
		return "#### Builds\n- Makes old things.", nil
	}
	if code, s := run("--package", "legacy", "--generate"); code != 0 || !strings.Contains(s, "#### Builds") || calls != 1 {
		t.Fatalf("%d %s calls %d", code, s, calls)
	}
	if code, s := run("--item", "api#1", "--generate"); code != 0 || !strings.Contains(s, "No selected items") || calls != 1 {
		t.Fatalf("%d %s calls %d", code, s, calls)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "spec.md")); err != nil || string(b) != src {
		t.Fatal("spec edited", err)
	}
}

func TestMultipleQueriesUnionCandidatesWithoutSearchingProse(t *testing.T) {
	code := reuseRepo(t)
	e := newCtxEnv(code, ctxOpts{})
	items := []gateItem{{ID: "p#1", Package: "p", Text: "Use `Gamma`.", Queries: []string{"Code that invokes `Alpha`", "Code that invokes `Delta`"}}}
	got := e.reuseCandidates(items, []string{"mine/"}, "")
	paths := map[string]bool{}
	for _, c := range got[0].Cands {
		paths[c.Path] = true
	}
	if !paths["app/a.go"] || !paths["zlib/z.go"] || paths["app/c.go"] {
		t.Fatalf("candidates from wrong source: %+v", got)
	}
}

func TestMissingBuildsOnlyWarnsAndScoutsAreExempt(t *testing.T) {
	sp := parseSpec(planHeader + "### api: API\nkind: ship\nowns: api/\n\nBuild API.\n#### Acceptance\nTests.\n\n### look: Look\nkind: scout\n\nLook around.\n#### Acceptance\nNotes.\n" + planTail)
	sp.validate()
	if len(sp.problems) != 0 {
		t.Fatal(sp.problems)
	}
	var cfg config
	cfg.Check = "test"
	got := gateDeterministic(t.TempDir(), cfg, sp, "")
	if len(got) != 1 || hasError(got) || !strings.Contains(got[0], "reuse checklist is skipped for this package") {
		t.Fatalf("%v", got)
	}
}

func TestChecklistMetricsAndLegacyShadowLocations(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"dossier/p.md": "### 3a. Reuse checklist\n- `p#1` Saves.\n  1. `store.go:10-20` func Save\n     | func Save() {}\n- `p#2` Lists.\n  candidate `store.go:10-20` shown above\n### 3. Existing things\n"})
	legacy := legacyChecklist(dir)
	if len(legacy["p"]) != 2 || legacy["p"][0].Cands[0].at() != "store.go:10-20" {
		t.Fatalf("%+v", legacy)
	}
	legacy["p"][0].Item.Queries = []string{"Code that saves"}
	legacy["p"][0].QueriesRun = 1
	legacy["p"][1].Cands = legacy["p"][0].Cands
	got := checklistStats(dir, legacy)
	if got != "items with queries 1; queries run 1; distinct candidate locations 1; bodies shown 1" {
		t.Fatal(got)
	}
	entries := []refEntry{{Package: "p", Item: "old#1", Verdict: "duplicate", Candidate: "store.go:12"}, {Package: "p", Item: "old#2", Verdict: "duplicate", Candidate: "store.go:30"}}
	if ref := resolveReference(entries, []gateItem{{ID: "p#1", Package: "p", Text: "Different wording"}}); len(ref) != 2 {
		t.Fatal(ref)
	}
}

func TestReferenceScoresLocationsAcrossDifferentItems(t *testing.T) {
	a := &armData{locations: map[string]map[string]map[string]bool{}}
	a.addLocation("group", verdictReuse, "store.go:10-20")
	a.addLocation("group", verdictReuse, "store.go:15")
	var out strings.Builder
	printLocationScores(&out, []*armData{a}, map[string]refEntry{"old#7": {Verdict: "duplicate", Candidate: "store.go:12"}}, []string{"group"})
	wantContains(t, out.String(), "1 found, 1 in the reference, 1 match; precision 100%, recall 100%")
}
