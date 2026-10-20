package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func count(s, sub string) int { return strings.Count(s, sub) }

// section is the part of out from the line that starts with from up to the
// line that starts with to ("" for the end).
func section(out, from, to string) string {
	i := strings.Index(out, from)
	if i < 0 {
		return ""
	}
	out = out[i:]
	if to != "" {
		if j := strings.Index(out[len(from):], "\n"+to); j >= 0 {
			out = out[:len(from)+j+1]
		}
	}
	return out
}

func TestIsTestPathMatchesTestFilesAndNotLookalikes(t *testing.T) {
	for p, want := range map[string]bool{
		"pay/pay_test.go":             true,
		"web/a.test.ts":               true,
		"web/a.test.tsx":              true,
		"web/a.spec.js":               true,
		"web/__tests__/a.ts":          true,
		"internal/testdata/in.json":   true,
		"web/fixtures/user.json":      true,
		"pay/pay.go":                  false,
		"pay/contest.go":              false,
		"pay/latest/x.go":             false,
		"pay/testing.go":              false,
		"web/fixtures_helper.ts":      false,
		"web/spec.ts":                 false,
		"internal/testdata_loader.go": false,
	} {
		if got := isTestPath(p); got != want {
			t.Errorf("isTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestEnclosingNamesTheDeclarationAroundALine(t *testing.T) {
	goSrc := strings.Split("package pay\n\n// Pay pays.\nfunc (s *Stripe) Pay(x int) error {\n\tif x > 0 {\n\t\treturn nil\n\t}\n\treturn nil\n}\n\ntype Gateway interface {\n\tPay(x int) error\n}\n", "\n")
	tsSrc := strings.Split("import { z } from 'zod'\n\nexport const schema = z.object({\n  name: z.string(),\n})\n\nexport function Form() {\n  const x = 1\n  return x\n}\n", "\n")
	pySrc := strings.Split("class Cart:\n    def total(self):\n        return 1\n", "\n")
	for _, c := range []struct {
		name string
		src  []string
		path string
		ln   int
		want string
	}{
		{"go method body", goSrc, "pay.go", 6, "method Stripe.Pay"},
		{"go interface member", goSrc, "pay.go", 12, "type Gateway"},
		{"the declaration line itself", goSrc, "pay.go", 4, ""},
		{"before any declaration", goSrc, "pay.go", 1, ""},
		{"ts const object", tsSrc, "a.ts", 4, "const schema"},
		{"ts function body", tsSrc, "a.tsx", 8, "function Form"},
		{"python method", pySrc, "cart.py", 3, "def total"},
		{"a language it does not read", goSrc, "pay.rb", 6, ""},
		{"a line past the end", goSrc, "pay.go", 99, ""},
	} {
		if got := enclosing(c.src, c.ln, c.path); got != c.want {
			t.Errorf("%s: enclosing = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBodyPreviewEndsWhereTheBracketsCloseAndFollowsChains(t *testing.T) {
	src := strings.Split("const a = 1\nexport const schema = z\n  .object({\n    name,\n  })\n  .refine((v) => v, {\n    x,\n  })\n\nconst after = 2\nfunc f() {\n\tbody()\n}\n", "\n")
	if body, more := bodyPreview(src, 1, 10); len(body) != 1 || more != 0 {
		t.Errorf("a single statement: %q +%d", body, more)
	}
	body, more := bodyPreview(src, 2, 10)
	if len(body) != 7 || more != 0 || !strings.Contains(body[6], "})") {
		t.Errorf("a chained call: %d lines +%d: %q", len(body), more, body)
	}
	if body, more = bodyPreview(src, 2, 4); len(body) != 4 || more != 3 {
		t.Errorf("a cut preview: %d lines +%d", len(body), more)
	}
	if body, _ = bodyPreview(src, 11, 10); len(body) != 3 {
		t.Errorf("a function: %q", body)
	}
}

func TestParseInlineLookupReadsKindQueryAndOptions(t *testing.T) {
	l, err := parseInlineLookup("text:polar tests=none glob=internal/** glob=!vendor/** word=true context=0 why=callers")
	if err != nil {
		t.Fatal(err)
	}
	if l.Kind != "text" || l.Query != "polar" || l.Tests != "none" || strings.Join(l.Glob, "|") != "internal/**|!vendor/**" || !l.Word || l.Context == nil || *l.Context != 0 || l.Why != "callers" {
		t.Errorf("parsed = %+v", l)
	}
	l, err = parseInlineLookup(`symbol:StartCheckout refs=3 impl=false def_kind=Method in=pay`)
	if err != nil || l.Refs == nil || *l.Refs != 3 || l.Impl == nil || *l.Impl || l.DefKind != "Method" || l.In != "pay" {
		t.Errorf("symbol options = %+v %v", l, err)
	}
	l, _ = parseInlineLookup(`text:"a=b" 'two words'`)
	if l.Query != "a=b two words" {
		t.Errorf("a quoted token that looks like an option: %q", l.Query)
	}
	l, _ = parseInlineLookup("read:pay/pay.go from=3 to=9")
	if l.Path != "pay/pay.go" || l.From != 3 || l.To != 9 || l.Query != "" {
		t.Errorf("read = %+v", l)
	}
	for _, bad := range []string{"no kind here", "text:x bogus=1", "text:x refs=many", "text:x word=maybe"} {
		if _, err := parseInlineLookup(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestOverlappingRangesMergeIntoOneHit(t *testing.T) {
	p := &ctxPart{}
	for _, n := range []int{5, 6, 7, 8, 9, 3, 4, 5, 6, 7} { // two ranges that overlap on 5-7
		p.add("a.go", ctxLine{N: n, Text: fmt.Sprint(n), Match: n == 6 || n == 4})
	}
	p.add("a.go", ctxLine{N: 20, Text: "far", Match: true})
	p.normalize()
	if got := len(p.Files[0].Lines); got != 8 { // 3-9 and 20
		t.Errorf("lines after the merge = %d", got)
	}
	if p.blocks() != 2 {
		t.Errorf("hits = %d, want the merged range and the far line", p.blocks())
	}
	if l := p.Files[0].Lines[3]; l.N != 6 || !l.Match {
		t.Errorf("a line that is a match once and context once must stay a match: %+v", l)
	}
}

func TestViewCutSaysWhatWasLeftOutAndTheFlag(t *testing.T) {
	v := &ctxView{Kind: "find", Query: "q", Unit: "hits"}
	for i := 1; i <= 100; i++ {
		v.part(0).add("a.go", ctxLine{N: i, Text: strings.Repeat("x", 20), Match: true})
	}
	v.Total, v.Shown = 1, 1
	var b strings.Builder
	v.write(&b, ctxFmt{}, nil, 0, 400)
	out := b.String()
	if !strings.Contains(out, "cut at about 400 characters: ") || !strings.Contains(out, "--full raises the bounded cap") {
		t.Errorf("no cut note:\n%s", out)
	}
	if shown := count(out, "x\n") + count(out, "xxxx\n") - 0; shown == 0 || count(out, ": xxxxxxxxxxxxxxxxxxxx") >= 100 {
		t.Errorf("the cap did not apply:\n%s", out)
	}
	b.Reset()
	v.write(&b, ctxFmt{}, nil, 0, 0)
	if strings.Contains(b.String(), "cut at") || count(b.String(), ": xxxxxxxxxxxxxxxxxxxx") != 100 {
		t.Errorf("a view with no limit was cut:\n%s", b.String())
	}
}

func TestBatchBudgetStopsLaterAnswersWithASentence(t *testing.T) {
	var views []*ctxView
	for i := 0; i < 12; i++ {
		v := &ctxView{Kind: "find", Query: fmt.Sprintf("q%d", i), Unit: "hits", Total: 1, Shown: 1}
		for n := 1; n <= 80; n++ {
			v.part(0).add(fmt.Sprintf("f%d.go", i), ctxLine{N: n, Text: strings.Repeat("y", 100), Match: true})
		}
		views = append(views, v)
	}
	out := renderBatch(views, ctxFmt{})
	if !strings.Contains(out, "not shown, the batch budget of 70000 characters is spent; run it alone or use --full") {
		t.Errorf("no budget sentence:\n%s", out[:min(len(out), 400)])
	}
	if len(out) > ctxBatchBudget+ctxViewCap {
		t.Errorf("the batch is %d characters, over its budget of %d", len(out), ctxBatchBudget)
	}
	if full := renderBatch(views, ctxFmt{Full: true}); strings.Contains(full, "not shown") || strings.Contains(full, "cut at") {
		t.Error("--full still cut the batch")
	}
}

func TestSameLineInSeveralFilesIsPrintedOnce(t *testing.T) {
	v := &ctxView{Kind: "text", Query: "zod", Unit: "matches", Total: 5, Shown: 5}
	for i := 1; i <= 5; i++ {
		v.part(0).add(fmt.Sprintf("web/f%d.ts", i), ctxLine{N: i, Text: "import { z } from 'zod'", Match: true})
	}
	v.part(0).add("web/f1.ts", ctxLine{N: 9, Text: "const only = z.string()", Match: true})
	var b strings.Builder
	v.write(&b, ctxFmt{}, nil, 0, 0)
	out := b.String()
	wantContains(t, out, "same line in 5 files: import { z } from 'zod' (web/f1.ts:1, web/f2.ts:2, web/f3.ts:3, +2 more)", "web/f1.ts\n  9: const only = z.string()")
	if count(out, "import { z }") != 1 || strings.Contains(out, "web/f4.ts\n") {
		t.Errorf("the repeated line was printed per file:\n%s", out)
	}
}

func TestConceptViewHidesTestFilesAndCountsThemUnlessAsked(t *testing.T) {
	c := newCtxHarness(t)
	e := newCtxEnv(c.repo, ctxOpts{})
	hits := []codeHit{
		{Path: "pay/pay.go", Start: 3, End: 5, Snippet: []string{"type Gateway interface {", "\tStartCheckout(ctx int) error", "}"}},
		{Path: "pay/pay_test.go", Start: 3, End: 3, Snippet: []string{"func TestPay(g Gateway) { g.StartCheckout(1) }"}},
	}
	none := e.conceptView("q", "", "none", hits, ctxFmt{})
	if none.Total != 1 || none.Hidden != 1 || strings.Contains(none.header(0), "pay_test.go") || !strings.Contains(none.header(0), "(+1 in tests; tests=include|only)") {
		t.Errorf("tests=none: %s total=%d hidden=%d", none.header(0), none.Total, none.Hidden)
	}
	only := e.conceptView("q", "", "only", hits, ctxFmt{})
	if only.Total != 1 || len(only.Parts[0].Files) != 1 || only.Parts[0].Files[0].Path != "pay/pay_test.go" {
		t.Errorf("tests=only: %+v", only.Parts[0].Files)
	}
	inc := e.conceptView("q", "", "include", hits, ctxFmt{})
	if inc.Total != 2 || strings.Contains(inc.header(0), "in tests") {
		t.Errorf("tests=include: %s", inc.header(0))
	}
}

func TestCtxTestsFlagOnSymTextAndBatch(t *testing.T) {
	c := newCtxHarness(t)

	text := c.must("ctx", "text", "StartCheckout")
	wantContains(t, text, "text: 2 of 2 matches", "(+1 in tests; tests=include|only)")
	if strings.Contains(text, "pay_test.go") {
		t.Errorf("tests are hidden by default:\n%s", text)
	}
	only := c.must("ctx", "text", "StartCheckout", "--tests", "only")
	wantContains(t, only, "text: 1 of 1 matches", "pay/pay_test.go\n")
	if strings.Contains(only, "pay/pay.go") {
		t.Errorf("--tests only listed a source file:\n%s", only)
	}
	inc := c.must("ctx", "text", "StartCheckout", "--tests", "include")
	wantContains(t, inc, "text: 3 of 3 matches", "pay/pay.go\n", "pay/pay_test.go\n")

	sym := c.must("ctx", "sym", "StartCheckout", "--tests", "only")
	wantContains(t, sym, "in 1 tests (used by 8 outside tests):\n", "  pay/pay_test.go\n    3  func TestPay(g Gateway) { g.StartCheckout(1) }")
	if strings.Contains(sym, "used by 8,") || strings.Contains(sym, "svc/svc.go") {
		t.Errorf("--tests only listed callers:\n%s", sym)
	}
	wantContains(t, c.must("ctx", "sym", "StartCheckout", "--tests", "include"), "used by 8, +1 in tests:\n", "tests:\n  pay/pay_test.go\n")

	c.stdin.WriteString(`{"lookups":[{"kind":"text","query":"StartCheckout"},{"kind":"text","query":"StartCheckout","tests":"only"},{"kind":"symbol","query":"StartCheckout","tests":"only"}]}`)
	batch := c.must("ctx", "batch", "-")
	wantContains(t, batch, `#1 text "StartCheckout": 2 of 2 matches`, `#2 text "StartCheckout": 1 of 1 matches`, "in 1 tests (used by 8 outside tests):")

	code, out := c.run("ctx", "text", "x", "--tests", "maybe")
	if code != 2 || !strings.Contains(out, "error: unknown value for --tests: maybe") || !strings.Contains(out, "valid values: none, include, only") {
		t.Errorf("a bad --tests: %d %s", code, out)
	}
}

func TestCtxSymbolOptionsShapeTheAnswer(t *testing.T) {
	c := newCtxHarness(t)
	def := c.must("ctx", "sym", "StartCheckout")
	wantContains(t, def, "used by 8, +1 in tests:\n  svc/svc.go\n", "implemented by 1:\n",
		"\nwired: StartCheckout is called in func Handle (svc/svc.go:7); by name (rg), Handle is referenced at pay/pay.go:4 in type Gateway")
	if count(def, "svc/svc.go\n") != 1 || strings.Contains(def, "callers of") {
		t.Errorf("a path is printed once and there is no callers header:\n%s", def)
	}

	counts := c.must("ctx", "sym", "StartCheckout", "--refs", "0")
	wantContains(t, counts, "used by 8, +1 in tests (--refs N lists them)")
	if strings.Contains(counts, "svc/svc.go") {
		t.Errorf("--refs 0 still listed callers:\n%s", counts)
	}
	two := c.must("ctx", "sym", "StartCheckout", "--refs", "2")
	wantContains(t, two, "used by 8, +1 in tests:\n", "showing 1-2 of 8; next: --page 2")
	if strings.Contains(two, "    8  }") || strings.Contains(two, "    7  return g.StartCheckout(1)  in Handle") || !strings.Contains(two, "    2  ") || !strings.Contains(two, "    1  package svc") {
		t.Errorf("--refs 2 listed more than two callers:\n%s", two)
	}
	if noImpl := c.must("ctx", "sym", "StartCheckout", "--impl=false"); strings.Contains(noImpl, "implemented by") {
		t.Errorf("--impl=false listed implementers:\n%s", noImpl)
	}
	wantContains(t, c.must("ctx", "sym", "StartCheckout", "--kind", "Function"), "0 of 2 definitions match")
	wantContains(t, c.must("ctx", "sym", "StartCheckout", "--in", "svc"), "0 of 2 definitions match")
	only := c.must("ctx", "sym", "StartCheckout", "--in", "pay", "--kind", "method")
	wantContains(t, only, "2 definitions of")

	compact := c.must("ctx", "sym", "StartCheckout", "--compact")
	wantContains(t, compact, "StartCheckout  method  pay/pay.go:4  candidate (identity not verified)\n", "used by 8, +1 in tests (--refs N lists them)")
	if strings.Contains(compact, "svc/svc.go") || strings.Contains(compact, "implemented by") {
		t.Errorf("--compact listed callers:\n%s", compact)
	}
	if len(compact)*10 >= len(def)*7 {
		t.Errorf("compact (%d) is not at least 30%% shorter than default (%d)", len(compact), len(def))
	}
}

func TestCtxBatchDedupesAcrossLookupsAndCrossReferences(t *testing.T) {
	c := newCtxHarness(t)
	c.stdin.WriteString(`{"lookups":[
{"kind":"text","query":"StartCheckout","tests":"include"},
{"kind":"symbol","query":"StartCheckout"},
{"kind":"concept","query":"checkout gateway"},
{"kind":"text","query":"StartCheckout","why":"again"}]}`)
	out := c.must("ctx", "batch", "-")

	wantContains(t, out, "batch: 4 lookups, 4 with results, 0 empty, 0 failed", "common[", "  pay/pay.go,jb+rg+sym,1-4")
	if i, j := strings.Index(out, "common["), strings.Index(out, "#1 "); i < 0 || i > j {
		t.Errorf("the cross-referenced files must come first:\n%s", out)
	}
	// #1 prints pay/pay.go lines 2-9 once even though its two hits' context overlaps.
	one := section(out, "#1 text", "#2 ")
	for _, n := range []string{"5", "6"} {
		if count(one, "\n  "+n+"-") != 1 {
			t.Errorf("line %s of the overlapping context is not printed exactly once in #1:\n%s", n, one)
		}
	}
	wantContains(t, section(out, "#2 sym", "#3 "), "pay/pay.go also #1: ")
	wantContains(t, section(out, "#3 find", "#4 "), "pay/pay.go also #1: 3-5")
	four := section(out, "#4 text", "")
	wantContains(t, four, `#4 text "StartCheckout" [again]: 2 of 2 matches`, "pay/pay.go also #1: ")
	if strings.Contains(four, "  4: ") {
		t.Errorf("#4 printed a line #1 already showed:\n%s", four)
	}
	if count(out, "\nsources: ") != 1 {
		t.Errorf("a batch carries one sources line:\n%s", out)
	}
	if count(out, "notes[") > 1 {
		t.Errorf("a batch carries one notes block:\n%s", out)
	}
}

func TestCtxBatchInlineLookupsAndWhyLabel(t *testing.T) {
	c := newCtxHarness(t)
	out := c.must("ctx", "batch", "-q", "text:StartCheckout glob=pay/** why=callers", "-q", "symbol:StartCheckout refs=1 impl=false")
	wantContains(t, out, `#1 text "StartCheckout" [callers]: `, "#2 sym \"StartCheckout\": 2 definitions", "used by 8, +1 in tests:\n", "showing 1-1 of 8; next: page=2")
	if strings.Contains(out, "implemented by") {
		t.Errorf("impl=false in an inline lookup was ignored:\n%s", out)
	}
	wantContains(t, c.log(c.rgCalls), "-g pay/**")
	calls := c.log(c.rgCalls)

	// The label is not part of the cache key.
	again := c.must("ctx", "batch", "-q", "text:StartCheckout glob=pay/** why=other")
	wantContains(t, again, `#1 text "StartCheckout" [other]: `)
	if c.log(c.rgCalls) != calls {
		t.Errorf("a different why ran rg again:\n%s", c.log(c.rgCalls))
	}

	// A plan from stdin and inline lookups run together, plan first.
	c.stdin.WriteString(`{"lookups":[{"kind":"read","path":"pay/pay.go","from":1,"to":2}]}`)
	both := c.must("ctx", "batch", "-", "-q", "text:StartCheckout")
	if !(strings.Index(both, "#1 read") >= 0 && strings.Index(both, "#1 read") < strings.Index(both, "#2 text")) {
		t.Errorf("plan and inline lookups:\n%s", both)
	}

	code, bad := c.run("ctx", "batch", "-q", "text:x bogus=1")
	if code != 2 || !strings.Contains(bad, `unknown option "bogus"`) || !strings.Contains(bad, "valid options: tests, generated, max, limit, page, fixed, word") {
		t.Errorf("an unknown inline option: %d %s", code, bad)
	}
	code, bad = c.run("ctx", "batch", "-q", "wat:x")
	if code != 2 || !strings.Contains(bad, "unknown kind") || !strings.Contains(bad, "valid kinds: concept, symbol, text, read") {
		t.Errorf("an unknown kind: %d %s", code, bad)
	}
}

func TestCtxTextOptionsReachRipgrep(t *testing.T) {
	c := newCtxHarness(t)
	last := func() string {
		lines := strings.Split(strings.TrimSpace(c.log(c.rgCalls)), "\n")
		return lines[len(lines)-1]
	}
	c.must("ctx", "text", "StartCheckout", "--fixed", "--word", "--ignore-case", "--glob", "pay/**", "--glob", "!pay/gen/**", "--type", "go", "--per-file", "2")
	got := last()
	wantContains(t, got, "-S", "--json", "-F", "-w", "-i", "-g pay/**", "-g !pay/gen/**", "-t go", "-m 2", "-g !*.lock", "-g !**/vendor/**", "-g !go.sum", "-e StartCheckout --")
	if strings.Index(got, "-g !*.lock") > strings.Index(got, "-g pay/**") {
		t.Errorf("the default excludes must come before the caller's globs: %s", got)
	}
	c.must("ctx", "text", "x", "--all", "-U")
	got = last()
	wantContains(t, got, "-U")
	if strings.Contains(got, "-g !*.lock") {
		t.Errorf("--all still skipped lockfiles: %s", got)
	}
	c.must("ctx", "text", "x", "-l")
	wantContains(t, last(), " -l ")
	if files := c.must("ctx", "text", "x", "-l", "--tests", "include"); !strings.Contains(files, "files[2]:\n  pay/pay.go\n  pay/pay_test.go") || !strings.Contains(files, "2 of 2 files") {
		t.Errorf("files only:\n%s", files)
	}
	wantContains(t, c.must("ctx", "text", "StartCheckout", "--fields", "src", "-p", "pay"), "pay/pay.go (rg)\n")
	counts := c.must("ctx", "text", "x", "-c")
	wantContains(t, counts, "counts[1]{path,count}:\n  pay/pay.go,2", "(+1 in tests; tests=include|only)")
}

// realRG skips the test when ripgrep is not installed.
func realRG(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is not installed")
	}
	return p
}

func TestCtxTextWithRipgrepKeepsItsSemanticsAndShapesHits(t *testing.T) {
	c := newCtxHarness(t)
	t.Setenv("ORCH_RG", realRG(t))
	long := "var Wide = \"" + strings.Repeat("w", 400) + " StartCheckout\"\n"
	for path, body := range map[string]string{
		"pay/lower.go":     "package pay\n\nfunc lower() { startcheckout() }\n",
		"pay/wide.go":      "package pay\n\n" + long,
		"pay/only_test.go": "package pay\n\nfunc TestOnly() { uniqueOnlyInTests() }\n",
		"vendor/v/v.go":    "package v\n\nvar _ = StartCheckout\n",
		"go.sum":           "StartCheckout h1:abc\n",
		"web/page.tsx":     "export function Page() {\n  return StartCheckout\n}\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(c.repo, path)), 0o755)
		os.WriteFile(filepath.Join(c.repo, path), []byte(body), 0o644)
	}

	// smart-case: a lowercase pattern finds both spellings, a capitalised one only its own.
	smart := c.must("ctx", "text", "startcheckout", "--tests", "include")
	wantContains(t, smart, "pay/lower.go\n", "pay/pay.go\n")
	cs := c.must("ctx", "text", "StartCheckout")
	if strings.Contains(cs, "pay/lower.go") {
		t.Errorf("a capitalised pattern matched a lowercase spelling:\n%s", cs)
	}
	wantContains(t, c.must("ctx", "text", "StartCheckout", "--ignore-case"), "pay/lower.go\n")

	// default excludes, and --all.
	if strings.Contains(cs, "vendor/") || strings.Contains(cs, "go.sum") {
		t.Errorf("vendored and lock files must be skipped by default:\n%s", cs)
	}
	wantContains(t, c.must("ctx", "text", "StartCheckout", "--all", "--limit", "20"), "vendor/v/v.go\n", "go.sum\n")

	// word and glob.
	wantContains(t, c.must("ctx", "text", "Start", "--word"), "text: 0 matches found in the whole repo")
	web := c.must("ctx", "text", "StartCheckout", "--word", "--glob", "web/**")
	wantContains(t, web, "text: 1 of 1 matches", "(glob web/**)", "web/page.tsx\n", "  2: return StartCheckout  [in function Page]")
	if count(web, "web/page.tsx") != 1 {
		t.Errorf("a path must be printed once, not once per line:\n%s", web)
	}
	wantContains(t, c.must("ctx", "text", "StartCheckout", "--glob", "nowhere/**"), "text: 0 matches found in the whole repo (glob nowhere/**)")

	// context: two lines by default, none with -C 0, a wider window with -C 3.
	def := c.must("ctx", "text", "StartCheckout", "--word", "--glob", "web/page.tsx")
	wantContains(t, def, "  1- export function Page() {", "  3- }")
	if zero := c.must("ctx", "text", "StartCheckout", "--word", "--glob", "web/page.tsx", "-C", "0"); strings.Contains(zero, "  1- ") || strings.Contains(zero, "  3- ") {
		t.Errorf("-C 0 printed context:\n%s", zero)
	}

	// only matching, multiline.
	om := c.must("ctx", "text", "Start[A-Z]\\w+", "--only-matching", "--glob", "web/**")
	wantContains(t, om, "  2: StartCheckout  [in function Page]")
	ml := c.must("ctx", "text", `Gateway interface \{\n\s+Start`, "--multiline", "--glob", "pay/pay.go")
	wantContains(t, ml, "  3: type Gateway interface {", "  4: StartCheckout(ctx int) error")

	// counts and files.
	wantContains(t, c.must("ctx", "text", "StartCheckout", "-c", "--glob", "pay/pay.go"), "counts[1]{path,count}:\n  pay/pay.go,2")

	// a long line is cut at 300 characters, 600 with --full, and says so with an ellipsis.
	wide := c.must("ctx", "text", "StartCheckout", "--glob", "pay/wide.go", "-C", "0")
	if l := section(wide, "pay/wide.go", ""); !strings.Contains(l, "…") || strings.Contains(l, strings.Repeat("w", 301)) || !strings.Contains(l, strings.Repeat("w", 250)) {
		t.Errorf("the wide line is not cut at about 300 characters:\n%s", wide)
	}
	if full := c.must("ctx", "text", "StartCheckout", "--glob", "pay/wide.go", "-C", "0", "--full"); !strings.Contains(full, strings.Repeat("w", 380)) {
		t.Errorf("--full did not lift the line width:\n%s", full)
	}

	// matches only in test files: the zero sentence says where the hits went.
	wantContains(t, c.must("ctx", "text", "uniqueOnlyInTests"), "text: 0 matches outside tests in the whole repo (+1 in tests; tests=include|only)")
	wantContains(t, c.must("ctx", "text", "uniqueOnlyInTests", "--tests", "only"), "text: 1 of 1 matches", "pay/only_test.go\n")
	wantContains(t, c.must("ctx", "text", "neverAppearsAnywhere"), "text: 0 matches found in the whole repo")

	// per-file and total caps say how many were left out and the flag that shows them.
	body := "package pay\n"
	for i := 0; i < 5; i++ {
		body += fmt.Sprintf("var m%d = \"marker\"\n", i)
	}
	os.WriteFile(filepath.Join(c.repo, "pay", "many.go"), []byte(body), 0o644)
	pf := c.must("ctx", "text", "marker", "--per-file", "2", "-C", "0")
	wantContains(t, pf, "text: 2 of 2 matches")
	mx := c.must("ctx", "text", "marker", "--max", "3", "-C", "0")
	wantContains(t, mx, "text: 3 of 5 matches", "showing 1-3 of 5; next: --page 2")
}

func TestCtxErrorsAreStructuredOnStdoutWithExitCode2(t *testing.T) {
	c := newCtxHarness(t)
	for _, tc := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"ctx", "bogus"}, []string{"error: unknown subcommand bogus", "help: valid subcommands: status, refresh, watch, find, sym, text, batch, brief"}},
		{[]string{"ctx", "text", "x", "--bogus"}, []string{"error: unknown flag --bogus for `ctx text`", "valid flags for `ctx text`: ", "--glob", "--word", "--tests"}},
		{[]string{"ctx", "text", "x", "--fields", "nope"}, []string{"error: unknown field nope", "valid fields: src"}},
		{[]string{"ctx", "text"}, []string{"error: ctx text needs one pattern"}},
		{[]string{"ctx", "sym"}, []string{"error: ctx sym needs one symbol name"}},
		{[]string{"ctx", "batch"}, []string{"error: ctx batch needs a plan"}},
		{[]string{"ctx", "watch", "start"}, []string{"error: ctx watch needs add or remove"}},
	} {
		code, out := c.run(tc.args...)
		if code != 2 {
			t.Errorf("%v exited %d, want 2:\n%s", tc.args, code, out)
		}
		wantContains(t, out, tc.wants...)
	}
	// A plan with one bad unit of work is a partial batch, not a failure.
	c.stdin.WriteString(`{"lookups":[{"kind":"text","query":"StartCheckout"},{"kind":"read","path":"pay/missing.go"},{"kind":"text","query":"zzzNone"}]}`)
	out := c.must("ctx", "batch", "-")
	wantContains(t, out, "batch: 3 lookups, 1 with results, 1 empty, 1 failed", "#2 read \"pay/missing.go\": error: file pay/missing.go not found in the repo", "#3 text \"zzzNone\": 0 matches found in the whole repo")
}

func TestCtxHomeShowsLiveStateAndHelpAndVersionAreCheap(t *testing.T) {
	c := newCtxHarness(t)
	home := c.must("ctx")
	wantContains(t, home, "repo: ", "head: "+sha7(c.head), "index: fresh ("+sha7(c.head)+")", "tools: ", "help[5]:", "orch ctx text <pattern>")
	if c.log(c.jbCalls) != "" && strings.Contains(c.log(c.jbCalls), "index") {
		t.Errorf("the home view refreshed the index: %s", c.log(c.jbCalls))
	}
	if out := c.must("ctx", "--version"); out != ctxVersion+"\n" {
		t.Errorf("--version = %q", out)
	}
	for name, h := range ctxHelps {
		if n := len(h.examples); n < 2 || n > 3 {
			t.Errorf("ctx %s --help has %d examples, want 2 or 3", name, n)
		}
		out := c.must("ctx", name, "--help")
		wantContains(t, out, "usage: "+h.usage, fmt.Sprintf("examples[%d]:", len(h.examples)))
	}
	wantContains(t, c.must("ctx", "text", "--help"), "flags[", "--glob,", "--word,", "--tests,none,")
}

func TestSymViewSizesBodyByKindAndPrintsPathsOnce(t *testing.T) {
	c := newCtxHarness(t)
	var fn strings.Builder
	fn.WriteString("package pay\n\n// Big does a lot.\nfunc Big() {\n")
	for i := 1; i <= 18; i++ {
		fmt.Fprintf(&fn, "\tstep%d()\n", i)
	}
	fn.WriteString("}\n\ntype Rec struct {\n\tA int\n\tB int\n\tC int\n\tD int\n\tE int\n}\n\nconst Limit = 5\n\nfunc Use() { Big() }\n")
	os.WriteFile(filepath.Join(c.repo, "pay", "big.go"), []byte(fn.String()), 0o644)
	e := newCtxEnv(c.repo, ctxOpts{})
	render := func(f symFact, o symOpts) string {
		var b strings.Builder
		e.symView(f, o).write(&b, ctxFmt{}, nil, 0, 0)
		return b.String()
	}

	big := symFact{Symbol: "Big", Lang: "go", Defs: []symDef{{Where: "pay/big.go:4", Kind: "Function", Func: true, Total: 3,
		Callers: []string{"pay/big.go:22", "svc/svc.go:7", "svc/svc.go:10"}}}}
	out := render(big, symOpts{Refs: 10})
	wantContains(t, out, "Big  func  pay/big.go:4-23\n", "Big does a lot.\n", "  func Big() {\n   step1()\n") // doc, no label
	if count(out, "step") != 19 || strings.Contains(out, "--full") {
		t.Errorf("a function of 20 lines is shown whole (18 steps and the caller line):\n%s", out)
	}
	var huge strings.Builder
	huge.WriteString("package pay\n\nfunc Huge() {\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&huge, "\tmore%d()\n", i)
	}
	huge.WriteString("}\n")
	os.WriteFile(filepath.Join(c.repo, "pay", "huge.go"), []byte(huge.String()), 0o644)
	cut := render(symFact{Symbol: "Huge", Lang: "go", Defs: []symDef{{Where: "pay/huge.go:3", Kind: "Function", Func: true}}}, symOpts{})
	if count(cut, "more") != 39 || !strings.Contains(cut, "  (+22 lines; --full)\n") {
		t.Errorf("a function shows its signature and 39 lines, then says how many it cut:\n%s", cut)
	}
	if strings.Contains(out, "4:") || strings.Contains(out, "  5  step") {
		t.Errorf("body lines are numbered:\n%s", out)
	}
	wantContains(t, out, "used by 3 in 2 files, none in tests:\n  22  step18()  in Big\n", "  svc/svc.go\n    7  return g.StartCheckout(1)  in Handle\n")
	if count(out, "pay/big.go") != 1 {
		t.Errorf("the home file's path appears once (the header), callers there need none:\n%s", out)
	}
	if full := render(big, symOpts{Refs: 10, Full: true}); count(full, "step") != 19 || strings.Contains(full, "--full") {
		t.Errorf("--full keeps the whole body:\n%s", full)
	}
	if ctxN := render(big, symOpts{Refs: 10, Context: 1}); !strings.Contains(ctxN, "  21- step17()\n") {
		t.Errorf("-C 1 adds the line above a caller:\n%s", ctxN)
	}
	if noCtx := render(big, symOpts{Refs: 10}); strings.Contains(noCtx, "- ") {
		t.Errorf("a caller's context line appears without -C:\n%s", noCtx)
	}

	rec := render(symFact{Symbol: "Rec", Lang: "go", Defs: []symDef{{Where: "pay/big.go:25", Kind: "Struct"}}}, symOpts{})
	wantContains(t, rec, "Rec  type  pay/big.go:25-31\n", "   A int\n", "   E int\n", "  }\n")
	if strings.Contains(rec, "--full") {
		t.Errorf("a struct is listed in full:\n%s", rec)
	}
	lim := render(symFact{Symbol: "Limit", Lang: "go", Defs: []symDef{{Where: "pay/big.go:33", Kind: "Constant"}}}, symOpts{})
	wantContains(t, lim, "Limit  const  pay/big.go:33 (lines 18-36)\n", "  const Limit = 5\n")
}

func TestSymViewDoesNotRepeatCallSitesAcrossDefinitions(t *testing.T) {
	c := newCtxHarness(t)
	e := newCtxEnv(c.repo, ctxOpts{})
	callers := []string{"svc/svc.go:7"}
	f := symFact{Symbol: "StartCheckout", Lang: "go", Defs: []symDef{
		{Where: "pay/pay.go:4", Kind: "Method", Func: true, Total: 1, Callers: callers},
		{Where: "pay/pay.go:7", Kind: "Method", Func: true, Total: 1, Callers: callers},
	}}
	var b strings.Builder
	e.symView(f, symOpts{Refs: 10}).write(&b, ctxFmt{}, nil, 0, 0)
	out := b.String()
	if count(out, "7  return g.StartCheckout(1)  in Handle") != 1 || count(out, "wired: ") != 1 || count(out, "used by 1, none in tests (same call sites as above)") != 1 {
		t.Errorf("call sites repeated:\n%s", out)
	}
}

func TestConstReadsInItsFunctionOrAmongItsNeighbours(t *testing.T) {
	c := newCtxHarness(t)
	var src strings.Builder
	src.WriteString("package pay\n\nfunc Setup() {\n\tconst retries = 3\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&src, "\tstep%d()\n", i)
	}
	src.WriteString("\tconst late = 9\n}\n")
	os.WriteFile(filepath.Join(c.repo, "pay", "dyn.go"), []byte(src.String()), 0o644)
	e := newCtxEnv(c.repo, ctxOpts{})
	render := func(name string, line int) string {
		var b strings.Builder
		f := symFact{Symbol: name, Lang: "go", Defs: []symDef{{Where: fmt.Sprintf("pay/dyn.go:%d", line), Kind: "Constant"}}}
		e.symView(f, symOpts{}).write(&b, ctxFmt{}, nil, 0, 0)
		return b.String()
	}
	// Line 4 is 1 line below the function start: the function's first 30 lines.
	near := render("retries", 4)
	wantContains(t, near, "retries  const  pay/dyn.go:4 (lines 3-32)\n", "  func Setup() {\n", "   const retries = 3\n", "   step28()\n")
	if strings.Contains(near, "step29()") {
		t.Errorf("the window is 30 lines:\n%s", near)
	}
	// Line 65 is 62 lines into the function: the lines around it (to the end of the file), never the function's start.
	far := render("late", 65)
	wantContains(t, far, "late  const  pay/dyn.go:65 (lines 50-67)", "   const late = 9\n")
	if strings.Contains(far, "func Setup") {
		t.Errorf("a const far into a function does not pull in its start:\n%s", far)
	}
}

func TestDefBodyKeepsDocOutOfTheLinesItCounts(t *testing.T) {
	lines := func(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }
	eq := func(got, want []string) bool { return strings.Join(got, "|") == strings.Join(want, "|") }

	// Go: the position is the signature; the code runs to the closing brace.
	goSrc := lines("// A does x.\nfunc A() {\n\tone()\n}\n\nfunc B() {}\n")
	doc, body, start, more := defBody(goSrc, 2, "a.go", 40)
	if len(doc) != 0 || start != 2 || more != 0 || !eq(body, goSrc[1:4]) {
		t.Errorf("go: doc %v start %d body %v more %d", doc, start, body, more)
	}
	// A position that points at the comment above: the comment is doc, the
	// code starts at the signature, and the comment does not use up the lines.
	doc, body, start, more = defBody(goSrc, 1, "a.go", 2)
	if !eq(doc, []string{"A does x."}) || start != 2 || more != 1 || !eq(body, goSrc[1:3]) {
		t.Errorf("comment position: doc %v start %d body %v more %d", doc, start, body, more)
	}

	// Python: the docstring is doc, the signature spans two lines, and the
	// function ends where the indentation falls back.
	py := lines("class X:\n    def f(self, a,\n          b):\n        \"\"\"Does f.\n\n        More.\n        \"\"\"\n        x = 1\n        return x\n\n    def g(self):\n        pass\n")
	doc, body, start, more = defBody(py, 2, "x.py", 40)
	wantBody := []string{py[1], py[2], py[7], py[8]}
	if !eq(doc, []string{"Does f.", "More."}) || start != 2 || more != 0 || !eq(body, wantBody) {
		t.Errorf("python: doc %v start %d body %v more %d", doc, start, body, more)
	}
	// The cap counts code lines only.
	long := "def f():\n    \"\"\"Doc.\"\"\"\n"
	for i := 1; i <= 50; i++ {
		long += fmt.Sprintf("    s%d()\n", i)
	}
	doc, body, _, more = defBody(lines(long), 1, "x.py", 40)
	if !eq(doc, []string{"Doc."}) || len(body) != 40 || more != 11 {
		t.Errorf("python cap: doc %v, %d code lines, %d more (want 40 and 11: the signature and 50 calls)", doc, len(body), more)
	}
	// Ruby keeps its closing end.
	rb := lines("def f\n  1\nend\n\ndef g\nend\n")
	if _, body, _, _ = defBody(rb, 1, "x.rb", 40); !eq(body, rb[0:3]) {
		t.Errorf("ruby: %v", body)
	}
}

func TestDefinitionsInAFileThatCannotBeReadDoNotPanic(t *testing.T) {
	c := newCtxHarness(t)
	e := newCtxEnv(c.repo, ctxOpts{})
	// The position names a file that is missing, a line past the end and no line.
	for _, where := range []string{"pay/missing.go:55", "pay/pay.go:900", "pay/pay.go"} {
		for _, d := range []symDef{{Where: where, Kind: "Constant"}, {Where: where, Kind: "Function", Func: true}, {Where: where, Kind: "Struct"}} {
			var b strings.Builder
			e.symView(symFact{Symbol: "X", Lang: "go", Defs: []symDef{d}}, symOpts{}).write(&b, ctxFmt{}, nil, 0, 0)
			if !strings.Contains(b.String(), "X ") {
				t.Errorf("%s %s: no header:\n%s", d.Kind, where, b.String())
			}
		}
	}
	if s, e2 := constWindow(nil, 55, "a.go"); e2 >= s {
		t.Errorf("an empty file gives a window of %d-%d", s, e2)
	}
}

func TestACommentUnderAClosedFunctionBelongsToNoFunction(t *testing.T) {
	ls := strings.Split("func A() {\n\tx()\n}\n\n// B does y.\n// More.\nfunc B() {\n\ty()\n}\n", "\n")
	if got := enclosing(ls, 5, "a.go"); got != "" {
		t.Errorf("a doc comment at column 0 is in %q, want no function", got)
	}
	if got := enclosing(ls, 6, "a.go"); got != "" {
		t.Errorf("the second comment line is in %q, want no function", got)
	}
	if got := enclosing(ls, 2, "a.go"); got != "func A" {
		t.Errorf("a statement in A is in %q", got)
	}
	if got := enclosing(ls, 8, "a.go"); got != "func B" {
		t.Errorf("a statement in B is in %q", got)
	}
}

func TestLeanConstShowsItsDeclarationOrItsFunctionNotItsNeighbours(t *testing.T) {
	var src strings.Builder
	src.WriteString("package pay\n\nvar ErrA = errors.New(\"a\")\nvar ErrB = errors.New(\"b\")\nvar ErrC = errors.New(\"c\")\n\nfunc Setup() {\n\tconst retries = 3\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&src, "\tstep%d()\n", i)
	}
	src.WriteString("}\n")
	ls := strings.Split(src.String(), "\n")
	if s, e := leanConstWindow(ls, 4, "a.go"); s != 4 || e != 4 {
		t.Errorf("a top-level var is its own line: %d-%d", s, e)
	}
	if s, e := leanConstWindow(ls, 8, "a.go"); s != 7 || e != 21 {
		t.Errorf("a const near its function's start shows 15 lines from the function: %d-%d", s, e)
	}
	if s, e := leanConstWindow(nil, 8, "a.go"); e >= s {
		t.Errorf("an empty file gives an empty window: %d-%d", s, e)
	}
}
