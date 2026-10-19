package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func eq(id int, pkg, text string) enrichedQuestion {
	return enrichedQuestion{N: id, Q: gateQuestion{Package: pkg, Question: text, Decisive: true}}
}

func TestDedupeMergesNearDuplicatesAndKeepsDistinctQuestionsApart(t *testing.T) {
	items := []enrichedQuestion{
		eq(1, "api", "Should the refund endpoint return 404 or 410 when the order is already deleted?"),
		eq(2, "api", "Which table holds the session rows?"),
		eq(3, "api", "Does the refund endpoint return 404 or 410 when an order was already deleted, and which body does it send?"),
		// Different wording, same two identifiers: merged by the identifier rule.
		eq(4, "api", "Where is `settings.Key` resolved against `store/queries.sql` at start-up?"),
		eq(5, "api", "Is the lookup of `store/queries.sql` through `settings.Key` cached between requests, and for how long?"),
	}
	got := dedupeQuestions(items)
	if len(got) != 3 {
		t.Fatalf("%d cards, want 3: %+v", len(got), got)
	}
	if got[0].N != 3 || len(got[0].Merged) != 1 || !strings.Contains(got[0].Merged[0], "Should the refund endpoint") {
		t.Errorf("the longest wording should lead and list the variant: %+v", got[0])
	}
	if got[1].N != 2 || len(got[1].Merged) != 0 {
		t.Errorf("a distinct question was merged: %+v", got[1])
	}
	if got[2].N != 5 || len(got[2].Merged) != 1 {
		t.Errorf("identifier rule: %+v", got[2])
	}
	// The same wording in two packages is not merged: each package has its own card.
	other := []enrichedQuestion{eq(1, "a", "Which table holds the session rows?"), eq(2, "b", "Which table holds the session rows?")}
	if n := len(dedupeQuestions(other[:1])) + len(dedupeQuestions(other[1:])); n != 2 {
		t.Errorf("per-package dedupe: %d", n)
	}
}

func TestQuestionIdentifiersAndSpecHints(t *testing.T) {
	ids := questionIdentifiers("Does FRD-03 keep `retention_days` at 30 days, or read internal/crm/ledger/ and api.Scope(ctx)?")
	want := map[string]bool{"frd-03": true, "retention_days": true, "30 days": true, "internal/crm/ledger/": true, "api.scope": true}
	for _, id := range ids {
		delete(want, id)
	}
	if len(want) != 0 {
		t.Errorf("identifiers %v lack %v", ids, want)
	}
	lines := []string{
		"# Spec",
		"unrelated text about colours",
		"FRD-03 keeps the audit rows; `retention_days` is 30 days",
		"and nothing else here",
		"api.Scope is exported for crm1-support",
		"more filler",
	}
	hints := specHints(lines, []string{"frd-03", "retention_days", "30 days"})
	if len(hints) != 1 || hints[0].Line != 3 || !strings.Contains(hints[0].Text, "retention_days") {
		t.Errorf("hints = %+v", hints)
	}
	if h := specHints(lines, []string{"frd-03", "bogus_name", "other_name"}); len(h) != 0 {
		t.Errorf("a line holding 1 of 3 identifiers matched: %+v", h)
	}
	if h := specHints(lines, []string{"frd-03"}); len(h) != 0 {
		t.Errorf("one identifier is not enough: %+v", h)
	}
	if h := specHints(lines, questionIdentifiers("Which colour does the footer use?")); len(h) != 0 {
		t.Errorf("unrelated question matched: %+v", h)
	}
}

func TestRouteQuestionTagsAndOwnerWinsOnConflict(t *testing.T) {
	may := []string{"template wording", "test layout"}
	for _, c := range []struct{ q, tag string }{
		{"Which refund policy applies to a partly shipped order?", "owner"},
		{"Is the retention period for tickets 30 or 90 days?", "owner"},
		{"What wording does the customer see when consent is withdrawn?", "owner"},
		{"Where should the new test layout put fixtures?", "lead-may-decide"},
		{"Should the template wording say shipped or sent?", "owner"}, // "wording" is an owner word
		{"Does the template use a 2-column wrapper?", "unclear"},
		{"Where do the helper files go for the test layout?", "lead-may-decide"},
		{"Which index backs the lookup?", "unclear"},
	} {
		if tag, _ := routeQuestion(c.q, may); tag != c.tag {
			t.Errorf("%q tagged %s, want %s", c.q, tag, c.tag)
		}
	}
	// Both an owner word and a May-decide phrase: the owner decides.
	if tag, _ := routeQuestion("Is the test layout for the price tests the same?", may); tag != "owner" {
		t.Errorf("conflict tagged %s", tag)
	}
	sp := parseSpec(threePkgSpec())
	if got := mayDecidePhrases(sp); len(got) != 0 {
		t.Errorf("single-word phrases should not count: %v", got)
	}
	sp.sections["Authority"] = "May decide: names, internal structure, `store/queries.go`, test layout"
	if got := strings.Join(mayDecidePhrases(sp), ";"); got != "internal structure;store/queries.go;test layout" {
		t.Errorf("phrases = %q", got)
	}
}

// fakeJbcontext installs a jbcontext stand-in that logs each query; it answers
// with one code chunk and one chunk of the spec, or fails when <dir>/fail exists.
func fakeJbcontext(t *testing.T) (bin, calls, failFlag string) {
	t.Helper()
	dir := t.TempDir()
	bin, calls, failFlag = filepath.Join(dir, "jbcontext"), filepath.Join(dir, "calls.log"), filepath.Join(dir, "fail")
	script := `#!/bin/sh
echo "$*" >> "` + calls + `"
[ -f "` + failFlag + `" ] && { echo "not logged in" >&2; exit 1; }
cat <<'EOF'
{"type":"search_result","results":[
{"result":{"scoredText":{"similarity":0.9},"sourcePosition":{"relativePath":"internal/store.go","startOffset":0,"endOffset":9}},"content":"\nfunc Put() {}\nline2\nline3\nline4\nline5\nline6\nline7\nline8","contentStartLine":2},
{"result":{"sourcePosition":{"relativePath":"spec.md"}},"content":"spec text","contentStartLine":1}]}
EOF
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls, failFlag
}

// enrichFixture writes a gate dir with the given questions and returns what
// enrichGate needs.
func enrichFixture(t *testing.T, qs []gateQuestion) (dir, code, src string, sp *spec) {
	t.Helper()
	if os.Getenv("ORCH_CACHE_DIR") == "" {
		t.Setenv("ORCH_CACHE_DIR", t.TempDir())
	}
	dir, code = t.TempDir(), t.TempDir()
	// The fake jbcontext hit is for this file; a hit for a file the checkout
	// lacks is dropped.
	if err := os.MkdirAll(filepath.Join(code, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := "package internal\n\nfunc Put() {}\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n"
	if err := os.WriteFile(filepath.Join(code, "internal", "store.go"), []byte(store), 0o644); err != nil {
		t.Fatal(err)
	}
	src = threePkgSpec()
	rec := gateRecord{Spec: "spec.md", Questions: qs}
	b, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(dir, "gate.json"), b, 0o644)
	os.WriteFile(filepath.Join(dir, "report.md"), []byte("# Gate report\n\nStatus: **fail**\n"), 0o644)
	return dir, code, src, parseSpec(src)
}

func fixtureQuestions() []gateQuestion {
	return []gateQuestion{
		{Package: "api", Question: "Should the refund endpoint return 404 or 410 when the order is already deleted?", Decisive: true},
		{Package: "api", Question: "Which table holds the session rows? Is `sessions` in internal/store.go?", Decisive: true},
		{Package: "api", Question: "Does the refund endpoint return 404 or 410 when an order was already deleted, and with which body?", Decisive: true},
		{Package: "store", Question: "Where does the helper live for the lookup?", Decisive: true},
		{Package: "store", Question: "Minor thing about names?", Decisive: false},
		{Package: "store", Question: "Answered one", Decisive: true, AnsweredBy: "internal/store.go:3"},
	}
}

func TestEnrichRewritesCardsWithTagsHintsAndAnIndex(t *testing.T) {
	bin, calls, _ := fakeJbcontext(t)
	t.Setenv("ORCH_JBCONTEXT", bin)
	dir, code, src, sp := enrichFixture(t, fixtureQuestions())
	res, err := enrichGate(dir, code, src, "spec.md", sp)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 4 || res.Cards != 3 || res.Owner != 1 || res.Unclear != 2 || res.CodeHits != 3 || len(res.Minor) != 1 {
		t.Fatalf("result = %+v", res)
	}
	wantSummary := "4 -> 3 after merging; owner 1, lead-may-decide 0, unclear 2; spec hints 0; code hits 3; symbol facts 0"
	if res.summary() != wantSummary {
		t.Errorf("summary %q, want %q", res.summary(), wantSummary)
	}

	qdir := filepath.Join(dir, "questions")
	entries, _ := filepath.Glob(filepath.Join(qdir, "*.md"))
	if len(entries) != 4 { // 3 cards and INDEX.md; the merged duplicate has no card of its own
		t.Fatalf("files = %v", entries)
	}
	card := string(mustRead(t, filepath.Join(qdir, "q3-api.md")))
	wantContains(t, card, "(+1 similar)", "## Merged similar questions", "Should the refund endpoint", "## Tag\nowner (mentions refund)", "## Possibly relevant code",
		"- `internal/store.go:3-10`", "func Put() {}")
	if strings.Contains(card, "spec text") || strings.Contains(card, "line7") {
		t.Errorf("a spec hit or more than %d snippet lines leaked:\n%s", enrichSnippet, card)
	}
	if exists(filepath.Join(qdir, "q1-api.md")) {
		t.Error("the merged duplicate kept its own card")
	}
	for _, f := range entries {
		if filepath.Base(f) == "INDEX.md" {
			continue
		}
		c, err := parseCard(string(mustRead(t, f)))
		if err != nil || c.question == "" || !strings.Contains(c.context, "Raised by the spec gate") {
			t.Errorf("%s is not a valid card: %v %+v", f, err, c)
		}
	}
	index := string(mustRead(t, filepath.Join(qdir, "INDEX.md")))
	wantContains(t, index, wantSummary, "## api", "| q3-api | owner |", "(+1 similar)", "## Minor open questions", "store [unclear]: Minor thing about names?")
	wantContains(t, string(mustRead(t, filepath.Join(dir, "report.md"))), "## Question cards\n"+wantSummary)

	// A rerun reads the same input, stacks nothing, and the cache avoids new searches.
	before := string(mustRead(t, calls))
	if _, err := enrichGate(dir, code, src, "spec.md", sp); err != nil {
		t.Fatal(err)
	}
	if after := string(mustRead(t, calls)); after != before {
		t.Errorf("a rerun called jbcontext again:\n%s", after)
	}
	if n := strings.Count(string(mustRead(t, filepath.Join(dir, "report.md"))), "## Question cards"); n != 1 {
		t.Errorf("report has %d question sections", n)
	}
	if n := strings.Count(before, "search --limit 3 --json-output"); n != 3 {
		t.Errorf("%d searches, want one per card (3)", n)
	}
}

func TestEnrichWithoutAWorkingJbcontextStillWritesValidCards(t *testing.T) {
	check := func(t *testing.T, want string) {
		t.Helper()
		dir, code, src, sp := enrichFixture(t, fixtureQuestions())
		res, err := enrichGate(dir, code, src, "spec.md", sp)
		if err != nil {
			t.Fatal(err)
		}
		if res.CodeHits != 0 || len(res.Notes) == 0 || !strings.Contains(strings.Join(res.Notes, "|"), want) {
			t.Fatalf("notes %v hits %d, want a note with %q", res.Notes, res.CodeHits, want)
		}
		wantContains(t, string(mustRead(t, filepath.Join(dir, "questions", "INDEX.md"))), "Note: ")
		c, err := parseCard(string(mustRead(t, filepath.Join(dir, "questions", "q2-api.md"))))
		if err != nil || c.question == "" {
			t.Fatalf("card invalid: %v", err)
		}
	}
	t.Run("missing", func(t *testing.T) {
		t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "absent"))
		check(t, "was not found")
	})
	t.Run("not logged in", func(t *testing.T) {
		bin, _, failFlag := fakeJbcontext(t)
		os.WriteFile(failFlag, nil, 0o644)
		t.Setenv("ORCH_JBCONTEXT", bin)
		check(t, "not logged in")
	})
}

func TestEnrichWithoutAGateJSONIsAnError(t *testing.T) {
	_, err := enrichGate(t.TempDir(), t.TempDir(), "", "spec.md", parseSpec(threePkgSpec()))
	if err == nil || !strings.Contains(err.Error(), "run `orch gate` first") {
		t.Errorf("err = %v", err)
	}
}

func TestGateEnrichesItsCardsAndAskStillParsesThem(t *testing.T) {
	h := setup(t)
	os.MkdirAll(filepath.Join(h.repo, "internal"), 0o755)
	os.WriteFile(filepath.Join(h.repo, "internal", "store.go"), []byte("package internal\n\nfunc Put() {}\n"), 0o644)
	h.commitSpec(threePkgSpec())
	bin, _, _ := fakeJbcontext(t)
	t.Setenv("ORCH_JBCONTEXT", bin)
	h.fakePi(gatePasses(map[string]string{"package-api": `{"id":"api","questions":[{"question":"What refund policy applies to a deleted order?","answered_by":"","changes_result":true}]}`}))
	code, out := h.run("gate", "spec.md")
	if code != 1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	wantContains(t, out, "questions: 1 need a decision", "question cards: 1 -> 1 after merging; owner 1, lead-may-decide 0, unclear 0; spec hints 0; code hits 1; symbol facts 0")
	gdir := filepath.Join(runRoot(h.repo), "gate", specHash(mustRead(t, filepath.Join(h.repo, "spec.md"))))
	wantContains(t, string(mustRead(t, filepath.Join(gdir, "questions", "q1-api.md"))), "## Tag\nowner (mentions refund)")
	ask := h.must("ask", "--cards", filepath.Join(gdir, "questions"))
	wantContains(t, ask, "questions: 1")
	if strings.Contains(ask, "INDEX") || strings.Contains(ask, "skipped") {
		t.Errorf("ask tripped over the index:\n%s", ask)
	}

	// `orch gate enrich` redoes it from gate.json; a spec never gated is an error.
	wantContains(t, h.must("gate", "enrich", "spec.md"), "question cards: 1 -> 1 after merging", "INDEX.md")
	h.commitSpec(strings.Replace(threePkgSpec(), "Users queue transfers.", "Users queue transfers fast.", 1))
	if code, out := h.run("gate", "enrich", "spec.md"); code != 1 || !strings.Contains(out, "run `orch gate` first") {
		t.Errorf("enrich without a gate run = %d %s", code, out)
	}
}

func TestGoSymbolsKeepsGoLookingNamesOnly(t *testing.T) {
	for _, c := range []struct {
		q    string
		want string
	}{
		{"Does Gateway.StartCheckout return an error, and what does payments.NewGateway do?", "Gateway.StartCheckout,payments.NewGateway"},
		{"Why is StartCheckout called twice, and does OrderService retry?", "StartCheckout,OrderService"},
		{"Does `newOrders(ctx)` wire `cart.Deps` and `account_id`?", "newOrders,cart.Deps"},
		{"Is invoice_line_id set in internal/crm/store/queries.sql, or in orders.sql.go and the `invoices` table?", ""},
		{"Is GitHub or PostgreSQL used, and who owns the Stripe sync?", ""},
		{"Do Alpha.Beta, Gamma.Delta, Epsilon.Zeta and Eta.Theta all agree?", "Alpha.Beta,Gamma.Delta,Epsilon.Zeta"}, // capped at 3
		{"Is `Gateway.StartCheckout` the same as StartCheckout?", "Gateway.StartCheckout"},                           // the bare name adds nothing
	} {
		if got := strings.Join(goSymbols(c.q), ","); got != c.want {
			t.Errorf("%q -> %q, want %q", c.q, got, c.want)
		}
	}
}

// goplsFixture makes a checkout with an interface and an implementation and a
// gopls stand-in that answers for it, logging every call.
func goplsFixture(t *testing.T) (code, calls, bin string) {
	t.Helper()
	code, dir := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(code, "pay"), 0o755)
	os.WriteFile(filepath.Join(code, "pay", "pay.go"), []byte("package pay\n\ntype Gateway interface {\n\tStartCheckout(ctx int) error\n}\ntype Stripe struct{}\nfunc (s *Stripe) StartCheckout(ctx int) error { return nil }\n"), 0o644)
	bin, calls = filepath.Join(dir, "gopls"), filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
echo "$*" >> "` + calls + `"
[ -f "` + dir + `/sleep" ] && exec sleep 5
C="` + code + `"
case "$1 $2" in
"workspace_symbol StartCheckout")
	echo "$C/pay/pay.go:4:2-15 Gateway.StartCheckout Method"
	echo "$C/pay/pay.go:7:18-31 Stripe.StartCheckout Method"
	echo "$C/pay/pay_test.go:9:6-23 TestStartCheckout Function"
	echo "$C/other/o.go:3:6-19 Other.StartCheckout Method";;
"workspace_symbol NoSuchThing") ;;
"workspace_symbol pay.Build")
	echo "$C/pay/b.go:3:6-11 pay.Build Function"
	echo "$C/other/b.go:3:6-11 other.Build Function";;
"references $C/pay/b.go:3:6") echo "$C/svc/svc.go:40:2-7";;
"references $C/pay/pay.go:4:2")
	echo "$C/pay/pay.go:4:2-15"
	echo "$C/svc/svc.go:10:5-18"
	echo "$C/svc/svc.go:20:5-18"
	echo "$C/pay/pay_test.go:30:3-16";;
"implementation $C/pay/pay.go:4:2") echo "$C/pay/pay.go:7:18-31";;
"references $C/pay/pay.go:7:18") echo "$C/svc/svc.go:30:5-18";;
esac
`
	os.WriteFile(bin, []byte(script), 0o755)
	return code, calls, bin
}

func TestGoplsFactsSplitCallSitesFromTestsAndListImplementations(t *testing.T) {
	code, calls, bin := goplsFixture(t)
	t.Setenv("ORCH_GOPLS", bin)
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "absent"))
	qs := []gateQuestion{
		{Package: "api", Question: "Does Gateway.StartCheckout retry, and is NoSuchThing involved? Also `invoice_line_id` and internal/crm/store/queries.sql.", Decisive: true},
		{Package: "api", Question: "Who else calls StartCheckout?", Decisive: true},
	}
	dir, _, src, sp := enrichFixture(t, qs)
	res, err := enrichGate(dir, code, src, "spec.md", sp)
	if err != nil {
		t.Fatal(err)
	}
	if res.SymbolFacts != 2 {
		t.Fatalf("symbol facts = %d, notes %v", res.SymbolFacts, res.Notes)
	}
	card := string(mustRead(t, filepath.Join(dir, "questions", "q1-api.md")))
	wantContains(t, card, "## Exact symbol facts",
		"- `Gateway.StartCheckout` (go Method) is defined at `pay/pay.go:4`; 2 callers outside tests: `svc/svc.go:10`, `svc/svc.go:20`; 1 test references; implemented at `pay/pay.go:7`",
		"- no symbol named `NoSuchThing` found (go)")
	for _, not := range []string{"Stripe.StartCheckout", "other/o.go", "TestStartCheckout", "invoice_line_id"} {
		if strings.Contains(card[strings.Index(card, "## Exact symbol facts"):], not) {
			t.Errorf("the facts hold %q:\n%s", not, card)
		}
	}
	// The bare name matches every exact definition: the interface method and the implementation, not the test or the lookalike.
	card2 := string(mustRead(t, filepath.Join(dir, "questions", "q2-api.md")))
	wantContains(t, card2, "`StartCheckout` (go Method) is defined at `pay/pay.go:4`", "`StartCheckout` (go Method) is defined at `pay/pay.go:7`; 1 callers outside tests: `svc/svc.go:30`; 0 test references")
	wantContains(t, string(mustRead(t, filepath.Join(dir, "questions", "INDEX.md"))), "| go facts |", "| yes |")
	wantContains(t, res.summary(), "symbol facts 2")

	// A lowercase first part is a package: only definitions in a directory of that name count.
	pkgDir, _, _, _ := enrichFixture(t, []gateQuestion{{Package: "api", Question: "Does pay.Build set the field?", Decisive: true}})
	if _, err := enrichGate(pkgDir, code, src, "spec.md", sp); err != nil {
		t.Fatal(err)
	}
	pkgCard := string(mustRead(t, filepath.Join(pkgDir, "questions", "q1-api.md")))
	wantContains(t, pkgCard, "`pay.Build` (go Function) is defined at `pay/b.go:3`; 1 callers outside tests: `svc/svc.go:40`; 0 test references")
	if strings.Contains(pkgCard, "other/b.go") {
		t.Errorf("a definition in another package leaked:\n%s", pkgCard)
	}

	// A rerun is served from the cache: gopls is not called again.
	before := string(mustRead(t, calls))
	if _, err := enrichGate(dir, code, src, "spec.md", sp); err != nil {
		t.Fatal(err)
	}
	if after := string(mustRead(t, calls)); after != before {
		t.Errorf("a rerun called gopls again:\n%s", after)
	}
	if n, m := strings.Count(before, "workspace_symbol StartCheckout"), strings.Count(before, "workspace_symbol NoSuchThing"); n < 1 || n > 2 || m != 1 {
		t.Errorf("workspace_symbol calls: StartCheckout %d (once, or twice when two symbols race), NoSuchThing %d (once)", n, m)
	}
}

func TestGoplsMissingOrSlowLeavesValidCardsAndANote(t *testing.T) {
	qs := []gateQuestion{{Package: "api", Question: "Does Gateway.StartCheckout retry?", Decisive: true}}
	check := func(t *testing.T, want string) {
		t.Helper()
		dir, code, src, sp := enrichFixture(t, qs)
		res, err := enrichGate(dir, code, src, "spec.md", sp)
		if err != nil {
			t.Fatal(err)
		}
		if res.SymbolFacts != 0 || !strings.Contains(strings.Join(res.Notes, "|"), want) {
			t.Fatalf("facts %d notes %v, want a note with %q", res.SymbolFacts, res.Notes, want)
		}
		wantContains(t, string(mustRead(t, filepath.Join(dir, "questions", "INDEX.md"))), "Note: ")
		if c, err := parseCard(string(mustRead(t, filepath.Join(dir, "questions", "q1-api.md")))); err != nil || c.question == "" {
			t.Fatalf("card invalid: %v", err)
		}
	}
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "absent"))
	t.Run("missing", func(t *testing.T) {
		t.Setenv("ORCH_GOPLS", filepath.Join(t.TempDir(), "absent"))
		check(t, "was not found")
	})
	t.Run("timeout", func(t *testing.T) {
		_, _, bin := goplsFixture(t)
		os.WriteFile(filepath.Join(filepath.Dir(bin), "sleep"), nil, 0o644)
		t.Setenv("ORCH_GOPLS", bin)
		old := goplsLimit
		goplsLimit = 150 * time.Millisecond
		t.Cleanup(func() { goplsLimit = old })
		check(t, "timed out")
	})
}
