package main

import (
	"fmt"
	"strings"
	"testing"
)

const (
	score429     = `jbcontext failed: exit status 1: {"type":"error","message":"Exception during request to https://api.jetbrains.ai/user/v5/indexing/search with code 429: Limit reached. Please try again later","statusCode":429}`
	scoreLicense = `working-tree semantic search failed: jbcontext failed: exit status 1: {"type":"error","message":"JetBrains AI rejected license OZQIDX5ZGG: The exchange used the JetBrains Daemon (IDE/Toolbox) session (dev@example.com). Pick an option"}`
)

func scoreCand(name string, score int, lines int) reuseCand {
	c := reuseCand{EvidenceID: "evidence-" + name, Path: "internal/" + name + "/" + name + ".go", Start: 10, End: 10 + lines - 1, Name: name, In: "func " + name,
		Sig: "func " + name + "()", Doc: name + " does one thing.", Total: lines, Callers: 3, Score: score, Kinds: "jb+sym"}
	for i := 0; i < lines; i++ {
		c.Lines = append(c.Lines, fmt.Sprintf("\tstep%d := %s(%d) // body line of %s", i, name, i, name))
	}
	return c
}

// scoreFixture has the shape of the reported crm1-ui-shell dossier: concept
// queries over three scopes, exact-string counts, failed and unavailable
// lookups, items that share a candidate, and enough lookups to exhaust the
// 24 KiB budget before the scored sections when they are printed one by one.
func scoreFixture() *ctxBrief {
	d := &ctxBrief{ID: "crm", root: "/work/repo"}
	scopes := []string{".", "web/", "internal/"}
	var perItem [][]evidenceLookup
	for qi := 0; qi < 24; qi++ {
		q := fmt.Sprintf("Code that handles shell concern number %d for storefront pages", qi)
		for si, sc := range scopes {
			switch {
			case qi%6 == 5:
				d.Lookups = append(d.Lookups, dossierLookup(q, sc, "concept", 0, 4, fmt.Errorf("%s", score429), ""))
			case qi%6 == 4 && si == 0:
				d.Lookups = append(d.Lookups, dossierLookup(q, sc, "concept", 0, 4, fmt.Errorf("%s", scoreLicense), ""))
			default:
				d.Lookups = append(d.Lookups, dossierLookup(q, sc, "concept", si+1, 4, nil, ""))
			}
		}
	}
	for ni := 0; ni < 10; ni++ {
		d.Lookups = append(d.Lookups, dossierLookup(fmt.Sprintf("crm1-name%d", ni), ".", "text", 40+ni, 0, nil, ""))
	}
	d.Lookups = append(d.Lookups,
		dossierLookup("brandCSS", "web", "symbol", 0, 0, nil, "symbol backend returned no execution record"),
		dossierLookup("renderStatus", "web", "symbol", 0, 0, nil, "symbol backend returned no execution record"))
	for ii := 0; ii < 4; ii++ {
		var lks []evidenceLookup
		for qi := 0; qi < 2; qi++ {
			q := fmt.Sprintf("Code that serves item %d requirement %d", ii+1, qi)
			for _, sc := range scopes {
				lks = append(lks, dossierLookup(q, sc, "concept", 4, 4, nil, ""))
			}
		}
		perItem = append(perItem, lks)
	}
	a, b, c, dd, e := scoreCand("alpha", 9, 45), scoreCand("beta", 7, 30), scoreCand("gamma", 5, 30), scoreCand("delta", 3, 30), scoreCand("eps", 1, 30)
	d.ReuseItems = []itemCands{
		{Item: gateItem{ID: "crm#1", Text: "Provides the locale loader."}, Queries: []string{"q", "r"}, QueriesRun: 2, Lookups: perItem[0], Cands: []reuseCand{a, b}},
		{Item: gateItem{ID: "crm#2", Text: "Applies provider branding.", Reuse: []string{"internal/crm/branding.go#Branding"}}, Queries: []string{"q", "r"}, QueriesRun: 2, Lookups: perItem[1], Cands: []reuseCand{a, c, dd}},
		{Item: gateItem{ID: "crm#3", Text: "Shows region stock."}, Queries: []string{"q", "r"}, QueriesRun: 2, Lookups: perItem[2], Cands: []reuseCand{e}},
		{Item: gateItem{ID: "crm#4", Text: "Writes the audit trail."}, Queries: []string{"q", "r"}, QueriesRun: 2, Lookups: perItem[3]},
	}
	d.Files = []string{"- `internal/alpha/alpha.go` (score 5: jb+sym): defines Alpha", "- `web/beta.ts` (score 4: jb): concept search", "- `internal/gamma/gamma.go` (score 2: text): text `Gamma`"}
	d.interfaceSummary = []string{"- Alpha (go; exact)\n  `internal/alpha/alpha.go:10` [evidence-sig] match=exact signature: func Alpha(); callers: 3 (by_name=false); tests: 1"}
	d.Facts = []symFact{{Symbol: "Alpha", Defs: []symDef{{Where: "internal/alpha/alpha.go:10"}}}}
	d.Missing = []string{"- `Nonexistent`", "- `transfer_rows`"}
	return d
}

func sectionOf(t *testing.T, text, from string) string {
	t.Helper()
	i := strings.Index(text, from)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", from, text)
	}
	rest := text[i+len(from):]
	if j := strings.Index(rest, "\n###"); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.Index(rest, "\nCut for"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestDossierScoreFirstLeadsWithTheScoredPictureAndKeepsTheLookupLogOut(t *testing.T) {
	d := scoreFixture()
	out := d.Render("## Package crm", "package", dossierTotal)
	t.Logf("fixture dossier: %d lines, %d bytes (budget %d)\n%s", strings.Count(out, "\n"), len(out), dossierTotal, out)
	if len(out) > dossierTotal {
		t.Errorf("dossier is %d bytes, past the budget", len(out))
	}
	for _, bad := range []string{"lookup-", "completeness=", "status=", "count=", "never establish absence", "unresolved:", `{"type"`, "observation ", "statusCode", "OZQIDX5ZGG", "dev@example.com", "Recorded omissions"} {
		if strings.Contains(out, bad) {
			t.Errorf("the prompt still contains %q", bad)
		}
	}
	if n := strings.Count(out, "Absence of a hit is not proof of absence."); n != 1 {
		t.Errorf("the absence sentence appears %d times, want once", n)
	}
	wantContains(t, out, "dossier-manifest.json")
	pos := func(s string) int {
		i := strings.Index(out, s)
		if i < 0 {
			t.Fatalf("missing %q", s)
		}
		return i
	}
	if !(pos("### 1. Ranked files") < pos("### 2. Interface sheet") && pos("### 2. Interface sheet") < pos("### Candidates (unverified)") && pos("### Candidates (unverified)") < pos("### Search notes")) {
		t.Errorf("sections are not in the order ranked files, interface sheet, candidates, notes:\n%s", out)
	}
	if body := sectionOf(t, out, "### 1. Ranked files\n"); !strings.HasPrefix(body, "- `internal/alpha/alpha.go`") {
		t.Errorf("ranked files is empty or unordered:\n%s", body)
	}
	if body := sectionOf(t, out, "### 2. Interface sheet\n"); !strings.Contains(body, "Alpha") {
		t.Errorf("interface sheet is empty:\n%s", body)
	}
	// A candidate shared by two items is shown once and names the other item.
	if n := strings.Count(out, "evidence=evidence-alpha"); n != 1 {
		t.Errorf("shared candidate shown %d times, want once", n)
	}
	wantContains(t, out, "also for #2", "`crm#4` Writes the audit trail.\n  no eligible candidates in the searched scope", "author's pointer: `internal/crm/branding.go#Branding`")
	for _, name := range []string{"beta", "gamma", "delta", "eps"} {
		wantContains(t, out, "evidence=evidence-"+name)
	}
	notes := sectionOf(t, out, "### Search notes\n")
	if n := strings.Count(strings.TrimRight(notes, "\n"), "\n") + 2; n > 8 {
		t.Errorf("search notes are %d lines, want at most 8:\n%s", n, notes)
	}
	wantContains(t, notes, "Lookups: ", " failed, ", " unavailable", "`Nonexistent`", "`transfer_rows`")
	// One line per distinct failure reason, not per lookup or per scope.
	if n := strings.Count(notes, "429"); n != 1 {
		t.Errorf("the 429 reason is listed %d times, want once:\n%s", n, notes)
	}
	if n := strings.Count(notes, "rejected license"); n != 1 {
		t.Errorf("the license reason is listed %d times, want once:\n%s", n, notes)
	}
	if n := strings.Count(notes, "symbol backend returned no execution record"); n != 1 {
		t.Errorf("the unavailable reason is listed %d times, want once:\n%s", n, notes)
	}
}

func TestDossierScoreFirstOmitsSectionsWithNothingToSay(t *testing.T) {
	d := &ctxBrief{ID: "web", root: "/work/repo", Lookups: []evidenceLookup{dossierLookup("q", "web/", "concept", 2, 4, nil, "")}}
	out := d.Render("## Package web", "package", dossierTotal)
	for _, heading := range []string{"### 1. Ranked files", "### 1b. Docs", "### 2. Interface sheet", "### 3. Existing", "### 3b.", "### 5."} {
		if strings.Contains(out, heading) {
			t.Errorf("%q is rendered with nothing under it:\n%s", heading, out)
		}
	}
	// Whatever the budget, a heading has a line under it.
	full := scoreFixture()
	for total := 200; total <= dossierTotal; total += 97 {
		out := full.Render("## Package crm", "package", total)
		lines := strings.Split(out, "\n")
		for i, l := range lines {
			if !strings.HasPrefix(l, "### ") || strings.HasPrefix(l, "### Candidates") {
				continue
			}
			if i+1 >= len(lines) || lines[i+1] == "" || strings.HasPrefix(lines[i+1], "### ") {
				t.Fatalf("budget %d: %q has nothing under it:\n%s", total, l, out)
			}
		}
	}
}

func TestDossierScoreFirstBudgetCutsNotesBeforeCandidatesAndLowScoresFirst(t *testing.T) {
	full := scoreFixture()
	cands := map[string]int{"alpha": 9, "beta": 7, "gamma": 5, "delta": 3, "eps": 1}
	notesGoneAt, candGoneAt := 0, 0
	for total := dossierTotal; total >= 1500; total -= 40 {
		out := full.Render("## Package crm", "package", total)
		if !strings.Contains(out, "### Search notes") && notesGoneAt == 0 {
			notesGoneAt = total
			if !strings.Contains(out, "the search notes") {
				t.Errorf("budget %d: the cut notice does not say the notes were cut:\n%s", total, out)
			}
		}
		// The candidates still shown are always the best-scored ones.
		missingBest := 0
		for name, score := range cands {
			shown := strings.Contains(out, "evidence=evidence-"+name)
			if !shown && score > missingBest {
				missingBest = score
			}
		}
		for name, score := range cands {
			if strings.Contains(out, "evidence=evidence-"+name) && score < missingBest {
				t.Fatalf("budget %d: candidate %s (score %d) is shown while a candidate of score %d is cut:\n%s", total, name, score, missingBest, out)
			}
		}
		if missingBest > 0 && candGoneAt == 0 {
			candGoneAt = total
		}
		if n := strings.Count(out, "### Search notes"); n > 1 {
			t.Fatalf("budget %d: notes rendered more than once", total)
		}
		if len(out) > total {
			t.Fatalf("budget %d: dossier is %d bytes", total, len(out))
		}
	}
	if notesGoneAt == 0 || candGoneAt == 0 {
		t.Fatalf("the budget sweep never cut the notes (%d) and a candidate (%d)", notesGoneAt, candGoneAt)
	}
	if notesGoneAt < candGoneAt {
		t.Errorf("a candidate was cut at budget %d before the notes at %d", candGoneAt, notesGoneAt)
	}
	// The omission data stays complete for the manifest.
	out := full.Render("## Package crm", "package", candGoneAt-40)
	if len(full.Omissions) == 0 || strings.Count(out, "Cut for") != 1 {
		t.Errorf("omissions %d, cut lines %d:\n%s", len(full.Omissions), strings.Count(out, "Cut for"), out)
	}
	if len(out) > 0 && strings.Count(out, "Recorded omissions") != 0 {
		t.Errorf("the omission list is still printed in the prompt")
	}
}
