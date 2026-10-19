package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rankRepo is a committed repo of the given files, with rg required and no
// language servers.
func rankRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is not installed")
	}
	t.Setenv("ORCH_CACHE_DIR", t.TempDir())
	t.Setenv("ORCH_GOPLS", filepath.Join(t.TempDir(), "no-gopls"))
	t.Setenv("ORCH_TSLS", filepath.Join(t.TempDir(), "no-tsls"))
	code := t.TempDir()
	for name, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(code, name)), 0o755)
		os.WriteFile(filepath.Join(code, name), []byte(body), 0o644)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@e.com", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = code
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return code
}

func TestBriefRankingKeepsDocsBelowCodeAndCountsTextOnlyWhenRare(t *testing.T) {
	files := map[string]string{
		"svc/own.go":       "package svc\n",
		"svc/other.go":     "package svc\n\nvar rare_flag = 1\n",
		"docs/rare.md":     "the rare_flag is described here\n",
		"docs/features.md": "rare_flag and more\n",
	}
	for i := 0; i < 25; i++ { // a name with 25 hits is noise
		files[fmt.Sprintf("svc/common%02d.go", i)] = "package svc\n\nvar common_name = 1\n"
	}
	code := rankRepo(t, files)
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "no-jbcontext"))
	e := newCtxEnv(code, ctxOpts{})
	b := e.Briefs([]briefInput{{ID: "p", Title: "Svc Thing", Text: "Build it. Uses `rare_flag` and `common_name`.", Owns: []string{"svc/own.go"}}})[0]

	if len(b.Files) < 2 || !strings.HasPrefix(b.Files[0], "- `svc/own.go` (score 2: own)") {
		t.Fatalf("owned code must come first with its kinds:\n%s", strings.Join(b.Files, "\n"))
	}
	all := strings.Join(b.Files, "\n")
	if strings.Contains(all, "docs/") {
		t.Errorf("a doc is listed among the ranked files:\n%s", all)
	}
	if !strings.Contains(all, "- `svc/other.go` (score 1: text)") {
		t.Errorf("a rare name's text hit counts 1:\n%s", all)
	}
	if strings.Contains(all, "common") {
		t.Errorf("a name with more than 20 hits must count 0:\n%s", all)
	}
	docs := strings.Join(b.Docs, "\n")
	if !strings.Contains(docs, "docs/rare.md") || !strings.Contains(docs, "(score 1: text)") || strings.Contains(docs, "common") {
		t.Errorf("docs section:\n%s", docs)
	}
	if out := b.Render("", "package", dossierTotal); !strings.Contains(out, "### 1b. Docs that mention it\n") {
		t.Errorf("the docs section is not rendered:\n%s", out)
	}
}

func TestFileEvidenceScoresEachKindOnceWithAgreementBonus(t *testing.T) {
	for _, c := range []struct {
		kinds []string
		want  int
		list  string
	}{
		{[]string{"own"}, 2, "own"},
		{[]string{"text"}, 1, "text"},
		{[]string{"own", "sym"}, 6, "own+sym"},
		{[]string{"own", "jb", "sym"}, 8, "own+jb+sym"},
		{[]string{"jb", "text"}, 5, "jb+text"},
	} {
		ev := &fileEv{kinds: map[string]bool{}}
		for _, k := range c.kinds {
			ev.kinds[k] = true
		}
		if ev.score() != c.want || ev.kindList() != c.list {
			t.Errorf("%v: score %d (%s), want %d (%s)", c.kinds, ev.score(), ev.kindList(), c.want, c.list)
		}
	}
}

func TestBriefLookAlikesAreConceptChunksOfCodeOutsideTheOwns(t *testing.T) {
	// Each file holds the chunk its hit claims: hits that no longer read as
	// the file does are dropped before the look-alike rules run.
	chunk := "package svc\n\nfunc SvcThing() {}\nbody\n"
	files := map[string]string{
		"svc/own.go":       chunk,
		"svc/other.go":     chunk,
		"svc/x_test.go":    chunk,
		"api/openapi.json": chunk,
		"deploy/run.sh":    chunk,
		"svc/gen/x.pb.go":  chunk,
	}
	code := rankRepo(t, files)
	var results []string
	for _, p := range []string{"svc/own.go", "svc/other.go", "svc/x_test.go", "api/openapi.json", "deploy/run.sh", "svc/gen/x.pb.go"} {
		results = append(results, fmt.Sprintf(`{"result":{"sourcePosition":{"relativePath":"%s"}},"content":"\nfunc SvcThing() {}\nbody","contentStartLine":2}`, p))
	}
	for i := 0; i < 9; i++ { // more look-alikes than the cap
		results = append(results, fmt.Sprintf(`{"result":{"sourcePosition":{"relativePath":"svc/more%d.go"}},"content":"\nfunc SvcThingMore%d() {}","contentStartLine":2}`, i, i))
	}
	for i := 0; i < 9; i++ {
		os.WriteFile(filepath.Join(code, "svc", fmt.Sprintf("more%d.go", i)), []byte(fmt.Sprintf("package svc\n\nfunc SvcThingMore%d() {}\n", i)), 0o644)
	}
	dir := t.TempDir()
	jb := filepath.Join(dir, "jbcontext")
	os.WriteFile(jb, []byte("#!/bin/sh\ncat <<'EOF'\n{\"type\":\"search_result\",\"results\":["+strings.Join(results, ",")+"]}\nEOF\n"), 0o755)
	t.Setenv("ORCH_JBCONTEXT", jb)
	e := newCtxEnv(code, ctxOpts{})
	b := e.Briefs([]briefInput{{ID: "p", Title: "Svc Thing", Text: "Build the svc thing.\n\n#### Acceptance\n- the svc thing works\n", Owns: []string{"svc/own.go"}}})[0]

	like := strings.Join(b.Like, "\n")
	if !strings.Contains(like, "- `svc/other.go:2-4` in func SvcThing via concept \"") && !strings.Contains(like, "svc/other.go:") {
		t.Fatalf("a concept chunk of code outside the owns is missing (notes %v):\n%s", e.Notes(), like)
	}
	for _, bad := range []string{"svc/own.go", "x_test.go", "openapi.json", "run.sh", "x.pb.go"} {
		if strings.Contains(like, bad) {
			t.Errorf("%s must not be a look-alike:\n%s", bad, like)
		}
	}
	if !strings.Contains(like, "6 more cut") || len(b.LikeRows) != dossierLikeTree {
		t.Errorf("the cap of %d look-alikes per tree is not applied and said (%d rows):\n%s", dossierLikeTree, len(b.LikeRows), like)
	}
	if !strings.Contains(like, "func SvcThing() {}") {
		t.Errorf("no chunk lines shown:\n%s", like)
	}
}

func TestMissingNamesKeepIdentifiersRoutesAndPathsOnly(t *testing.T) {
	for n, want := range map[string]bool{
		"transfer_rows":       true,
		"Module.Webhooks":     true,
		"internal/gone.go":    true,
		"POST /api/transfers": true,
		"mod_*.go":            false,
		"charge.dispute.*":    false,
		"registry_test":       false,
		"a b":                 false,
	} {
		if got := missingWorthy(n); got != want {
			t.Errorf("missingWorthy(%q) = %v, want %v", n, got, want)
		}
	}
}

func TestBriefSheetKeepsOnlySymbolsThePackageDefinesExtendsOrCalls(t *testing.T) {
	pl := &briefPlan{in: briefInput{Owns: []string{"pay"}}, accept: []string{"Wired lists Named"}}
	def := func(where string, callers ...string) *symFact {
		return &symFact{Defs: []symDef{{Where: where, Callers: callers}}}
	}
	for name, c := range map[string]struct {
		f    *symFact
		want bool
	}{
		"Defined":   {def("pay/a.go:3"), true},
		"Called":    {def("store/q.go:3", "pay/b.go:9"), true},
		"Elsewhere": {def("config/c.go:3", "config/d.go:4"), false},
		"Named":     {def("config/c.go:3"), true},
	} {
		if got := pl.symKept(name, c.f); got != c.want {
			t.Errorf("symKept(%s) = %v, want %v", name, got, c.want)
		}
	}
}

func TestAcceptanceBulletsOwnsQueriesAndCodeTrees(t *testing.T) {
	text := "### p: P\n\nBuild.\n\n#### Acceptance\n- first thing works\n* `second` too\n1. third\n\n#### Other\n- not this\n"
	if got := strings.Join(acceptanceBullets(text), "|"); got != "first thing works|`second` too|third" {
		t.Errorf("acceptance bullets = %q", got)
	}
	if q := ownsQuery("internal/crm/payments/gateways/"); q != "payments gateways" {
		t.Errorf("ownsQuery = %q", q)
	}
	if q := ownsQuery("web/x.ts"); q != "web x" {
		t.Errorf("ownsQuery of a file = %q", q)
	}
	code := rankRepo(t, map[string]string{"internal/a.go": "package a\n", "internal/b.go": "package a\n", "web/c.ts": "x\n", "docs/d.go": "x\n", "api/o.json": "{}\n"})
	got := codeTreesFor(code, nil)
	if strings.Join(got, ",") != "internal/,web/" {
		t.Errorf("code trees = %v", got)
	}
}

// fakeJbResults is a jbcontext that answers every search with these chunks:
// path, first line, content.
func fakeJbResults(t *testing.T, chunks [][3]string) {
	t.Helper()
	var results []string
	for _, c := range chunks {
		results = append(results, fmt.Sprintf(`{"result":{"sourcePosition":{"relativePath":"%s"}},"content":"%s","contentStartLine":%s}`, c[0], c[2], c[1]))
	}
	jb := filepath.Join(t.TempDir(), "jbcontext")
	os.WriteFile(jb, []byte("#!/bin/sh\ncat <<'EOF'\n{\"type\":\"search_result\",\"results\":["+strings.Join(results, ",")+"]}\nEOF\n"), 0o755)
	t.Setenv("ORCH_JBCONTEXT", jb)
}

func likeBrief(code string) *ctxBrief {
	e := newCtxEnv(code, ctxOpts{})
	return e.Briefs([]briefInput{{ID: "p", Title: "Svc Thing", Text: "Build the svc thing.", Owns: []string{"svc/own.go"}}})[0]
}

func TestLookAlikesMergeEveryChunkOfAFileIntoOneEntry(t *testing.T) {
	code := rankRepo(t, map[string]string{"svc/own.go": "package svc\n", "svc/other.go": "package svc\n"})
	fakeJbResults(t, [][3]string{
		{"svc/other.go", "2", `\nfunc SvcThing() {}\nbody`},
		{"svc/other.go", "30", `\nfunc SvcSecond() {}\nbody`},
		{"svc/other.go", "50", `\nfunc SvcThird() {}`},
	})
	b := likeBrief(code)
	like := strings.Join(b.Like, "\n")
	if strings.Count(like, "svc/other.go") != 1 || len(b.LikeRows) != 1 {
		t.Fatalf("a file must be one entry:\n%s", like)
	}
	wantContains(t, like, "(lines 3-4, 31-32, 51)", "in func SvcThing, func SvcSecond, func SvcThird", "func SvcThing() {}")
}

func TestLookAlikesHaveAQuotaPerTreeAndTheOwnsTreeComesFirst(t *testing.T) {
	files := map[string]string{"svc/own.go": "package svc\n", "web/a.ts": "x\n", "svc/z.go": "package svc\n"}
	var chunks [][3]string
	for i := 0; i < 6; i++ { // the web results come first and are more numerous
		files[fmt.Sprintf("web/p%d.ts", i)] = "x\n"
		chunks = append(chunks, [3]string{fmt.Sprintf("web/p%d.ts", i), "2", fmt.Sprintf(`\nexport function svcThing%d() {}`, i)})
	}
	for i := 0; i < 6; i++ {
		files[fmt.Sprintf("svc/q%d.go", i)] = "package svc\n"
		chunks = append(chunks, [3]string{fmt.Sprintf("svc/q%d.go", i), "2", fmt.Sprintf(`\nfunc SvcThing%d() {}`, i)})
	}
	code := rankRepo(t, files)
	fakeJbResults(t, chunks)
	b := likeBrief(code)
	var svc, web int
	firstWeb, lastSvc := -1, -1
	for i, r := range b.LikeRows {
		if strings.HasPrefix(r.At, "svc/") {
			svc++
			lastSvc = i
		} else if strings.HasPrefix(r.At, "web/") {
			web++
			if firstWeb < 0 {
				firstWeb = i
			}
		}
	}
	if svc != dossierLikeTree || web != dossierLikeTree || lastSvc > firstWeb {
		t.Errorf("want %d per tree with svc/ first, got svc %d web %d:\n%s", dossierLikeTree, svc, web, strings.Join(b.Like, "\n"))
	}
	if !strings.Contains(strings.Join(b.Like, "\n"), "4 more cut") {
		t.Errorf("the cut is not said:\n%s", strings.Join(b.Like, "\n"))
	}
}

func TestLookAlikesNeedAWordInCommonWithThePackageAndEmptyTreesAreSaid(t *testing.T) {
	code := rankRepo(t, map[string]string{"svc/own.go": "package svc\n", "svc/near.go": "package svc\n", "svc/far.go": "package svc\n", "web/a.ts": "x\n"})
	fakeJbResults(t, [][3]string{
		{"svc/near.go", "2", `\nfunc NewSvcClient() {}`},
		{"svc/far.go", "2", `\nfunc BananaSplit() {}`},
		{"web/a.ts", "2", `\nexport const banana = 1`},
	})
	b := likeBrief(code)
	like := strings.Join(b.Like, "\n")
	wantContains(t, like, "svc/near.go", "(no similar code found in web/)")
	if strings.Contains(like, "far.go") || strings.Contains(like, "web/a.ts:") {
		t.Errorf("a chunk sharing no word with the package survived:\n%s", like)
	}
	// camelCase is split: SvcClient shares "svc".
	if got := wordsOf("NewSvcClient HTTPServer", nil); !got["svc"] || !got["client"] || !got["http"] || !got["server"] {
		t.Errorf("wordsOf = %v", got)
	}
}

func TestConceptQueriesAreShortKeyPhrasesNotSpecSentences(t *testing.T) {
	pl := &briefPlan{in: briefInput{Title: "Gateway v1, Stripe App, bank debits and PayPal through Stripe", Owns: []string{"pay/registry.go", "pay/fake/", "pay/stripe/"}},
		goSyms: []string{"Gateway", "Registry"}, facts: map[string]*symFact{},
		builds: []string{"Build the `Gateway` interface for v1: off-session charge of a saved method, refund (full and partial), setup session for a new method (hosted), list and detach methods"},
		accept: []string{"Refund of a partial amount returns the remaining balance to the customer and records an event in the ledger"}}
	pl.makeQueries()
	if len(pl.concepts) < 4 {
		t.Fatalf("queries = %q", pl.concepts)
	}
	for _, q := range pl.concepts {
		if n := len(strings.Fields(q)); n > 11 {
			t.Errorf("query %q has %d words", q, n)
		}
	}
	if !strings.HasPrefix(pl.concepts[0], "Gateway Registry registry") && !strings.HasPrefix(pl.concepts[0], "Gateway Registry") {
		t.Errorf("the first query is names and purpose: %q", pl.concepts[0])
	}
	if strings.Contains(strings.Join(pl.concepts, "|"), "off-session charge of a saved method") {
		t.Errorf("a spec sentence was sent verbatim: %q", pl.concepts)
	}
	if !contains(pl.concepts, "pay fake") || !contains(pl.concepts, "pay stripe") {
		t.Errorf("one query per owned directory is missing: %q", pl.concepts)
	}
}

func contains(l []string, s string) bool { return containsStr(l, s) }
