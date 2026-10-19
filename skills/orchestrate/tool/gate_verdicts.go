package main

import (
	"fmt"
	"strings"
)

// reuseItemsTask replaces the free "reuse" part of a group pass when the
// dossier holds a reuse checklist. It lives in the task, never in the shared
// prefix.
const reuseItemsTask = ` (3) reuse: the facts gathered by code include a Reuse checklist: every Builds bullet has an id; candidates are existing code found from its explicit search lines. Items without search lines have no searched candidates. A shared candidate is shown once and lists the other items it answers. Decide EVERY item from its candidates and give one entry per item id in "items" with a verdict: reuse (existing code already does the job; the package should call it, and building it again would duplicate it), extend (existing code does something near it; the new work is a variation or an addition at a named location, such as a case, a parameter, a sibling or a column: why must say where it goes), new (nothing comparable exists) or unclear. Set candidate to the existing code as path:line (null for new). Open a file only to confirm or reject a candidate; do not search for more than a few extra things. A thing the package is told to modify in place is not reuse.`

// itemMap groups items by package id.
func itemMap(items []gateItem) map[string][]gateItem {
	out := map[string][]gateItem{}
	for _, it := range items {
		out[it.Package] = append(out[it.Package], it)
	}
	return out
}

const retryItemsSchema = `{"packages":[{"id":"","items":[{"item":"<package>#<n>","verdict":"reuse|extend|new|unclear","candidate":"path:line or null","why":""}]}]}`

// retryItemsPass asks only for the items a group pass left without a verdict.
// It is of the same class as the group pass, so it shares the prefix; its
// text is the packages' text and the checklist of just the missing items.
func retryItemsPass(key, pkgShared, joined string, ids []string, missing []gateItem, dos *dossierSet) gatePass {
	seen := map[string]bool{}
	var pk, mid []string
	for _, it := range missing {
		mid = append(mid, it.ID)
		if !seen[it.Package] {
			seen[it.Package] = true
			pk = append(pk, it.Package)
		}
	}
	own := gatePackageOwn(joined) + "\n\nReuse checklist (only the items still open):\n" + dos.checklistFor(missing)
	task := fmt.Sprintf(`Your earlier answer left these items without a verdict: %s. For each, give a verdict from its candidates above: reuse (existing code already does the job; the package should call it), extend (existing code does something near it; the new work is a variation at a named location, which why must name), new (nothing comparable) or unclear. Set candidate to the existing code as path:line (null for new). Open a file only to confirm or reject a candidate. Give one entry in "packages" per package with its id and only the items listed. Packages in this pass: %s.`+budgetSentenceItems(gateTurnsBase), strings.Join(mid, ", "), strings.Join(pk, ", "))
	p := newGatePass(key+"#retry", "package", pkgShared, own, "GATE-GROUP-ITEMS", task, retryItemsSchema, applyItemsOnly(key+"#retry", pk, missing))
	p.budget = gateTurnsBase
	return p
}

// applyItemsOnly reads the reply of a coverage retry and fills only the items
// that are still empty.
func applyItemsOnly(name string, ids []string, missing []gateItem) func(string, *gateRecord, string, *spec) error {
	return func(raw string, rec *gateRecord, repo string, sp *spec) error {
		entries, ok := retryAnswers(raw, ids)
		if !ok {
			return fmt.Errorf("no item verdicts in the reply")
		}
		for _, e := range entries {
			var its []gateItem
			for _, it := range missing {
				if it.Package == e.ID {
					its = append(its, it)
				}
			}
			rec.applyVerdicts(name, repo, its, e.Items)
		}
		return nil
	}
}
