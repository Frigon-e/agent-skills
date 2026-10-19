package main

import (
	"fmt"
	"io"
	"strings"
)

// What the judges were shown of the reference key, and how often they agree
// with it. A key item with a target location is "seen" when a candidate of the
// item's checklist (the dossier's reuse rows) points at that location, or an
// author reuse hint names its file. Agreement with the key is split into items
// seen and not seen, which tells retrieval (was the answer on the page?) from
// judgement (did the judge pick it?).

// seenCount counts key items with a target location. Hint counts the ones seen
// only through an author hint on the same file.
type seenCount struct {
	Total int `json:"total"`
	Seen  int `json:"seen"`
	Hint  int `json:"hint"`
}

// keyAgree counts one judge's verdicts that equal the key's class. The seen
// and unseen counts cover the key items that have a target location.
type keyAgree struct {
	Agree       int `json:"agree"`
	Both        int `json:"both"`
	SeenAgree   int `json:"seen_agree"`
	SeenBoth    int `json:"seen_both"`
	UnseenAgree int `json:"unseen_agree"`
	UnseenBoth  int `json:"unseen_both"`
}

type keyView struct {
	Seen  map[string]seenCount `json:"seen"`  // by class: duplicate, variation_site
	Agree map[string]keyAgree  `json:"agree"` // by judge: group, decider, flash
}

// keySeen reports whether an arm's judges were shown the key's target of an
// item, and whether only through an author hint.
func (a *armData) keySeen(id string, r refEntry) (seen, viaHint bool) {
	if r.Candidate == "" {
		return false, false
	}
	var rows []itemCands
	if strings.HasPrefix(id, "location:") {
		for _, cs := range a.checklist {
			rows = append(rows, cs...)
		}
	} else {
		rows = append(rows, a.checklist[r.Package]...)
		if len(rows) == 0 {
			for _, cs := range a.checklist {
				rows = append(rows, cs...)
			}
		}
	}
	for _, ic := range rows {
		if ic.Item.ID != id && !strings.HasPrefix(id, "location:") {
			continue
		}
		for _, c := range ic.Cands {
			if sameLocation(c.at(), r.Candidate) {
				return true, false
			}
		}
	}
	want := pointerRe.FindStringSubmatch(r.Candidate)
	for _, it := range a.items {
		if it.ID != id && !strings.HasPrefix(id, "location:") {
			continue
		}
		for _, h := range it.Reuse {
			if got := pointerRe.FindStringSubmatch(h); got != nil && want != nil && got[1] == want[1] {
				return true, true
			}
		}
	}
	return false, false
}

func keyViewOf(a *armData, ref map[string]refEntry) keyView {
	v := keyView{Seen: map[string]seenCount{}, Agree: map[string]keyAgree{}}
	for id, r := range ref {
		class := benchClass(r.Verdict)
		if class == "" || class == "unknown" {
			continue
		}
		seen, hint := a.keySeen(id, r)
		hasTarget := r.Candidate != "" && (class == "duplicate" || class == "variation_site")
		if hasTarget {
			c := v.Seen[class]
			c.Total++
			if seen {
				c.Seen++
				if hint {
					c.Hint++
				}
			}
			v.Seen[class] = c
		}
		for _, j := range []string{"group", "decider", "flash"} {
			got := a.verdict(j, id)
			if got == "" {
				continue
			}
			k := v.Agree[j]
			k.Both++
			same := got == normVerdict(r.Verdict)
			if same {
				k.Agree++
			}
			if hasTarget {
				if seen {
					k.SeenBoth++
					if same {
						k.SeenAgree++
					}
				} else {
					k.UnseenBoth++
					if same {
						k.UnseenAgree++
					}
				}
			}
			v.Agree[j] = k
		}
	}
	return v
}

func printKeyView(w io.Writer, arms []*armData, ref map[string]refEntry) {
	for i, a := range arms {
		v := keyViewOf(a, ref)
		label := string(rune('A' + i))
		var total, seen int
		var parts []string
		for _, class := range []string{"duplicate", "variation_site"} {
			c := v.Seen[class]
			total += c.Total
			seen += c.Seen
			parts = append(parts, fmt.Sprintf("%s %d/%d (%s)", class, c.Seen, c.Total, pct(c.Seen, c.Total)))
		}
		fmt.Fprintf(w, "seen by the judges, arm %s: %s; all %d/%d (%s) of the key's targets were among the candidates or hints shown\n", label, strings.Join(parts, ", "), seen, total, pct(seen, total))
		for _, j := range []string{"group", "decider", "flash"} {
			k, ok := v.Agree[j]
			if !ok {
				continue
			}
			fmt.Fprintf(w, "agreement with the key, %s.%s: %d/%d (%s); targets seen %d/%d (%s), not seen %d/%d (%s)\n", label, j, k.Agree, k.Both, pct(k.Agree, k.Both),
				k.SeenAgree, k.SeenBoth, pct(k.SeenAgree, k.SeenBoth), k.UnseenAgree, k.UnseenBoth, pct(k.UnseenAgree, k.UnseenBoth))
		}
	}
}
