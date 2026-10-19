package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestKeyViewSeparatesWhatTheJudgesSawFromHowTheyAgreed(t *testing.T) {
	a := &armData{
		group: map[string]string{},
		items: []gateItem{
			{ID: "p#1", Package: "p"}, {ID: "p#2", Package: "p", Reuse: []string{"b/y.go#Sym"}},
			{ID: "p#3", Package: "p"}, {ID: "p#4", Package: "p"},
		},
		checklist: map[string][]itemCands{"p": {
			{Item: gateItem{ID: "p#1"}, Cands: []reuseCand{{Path: "a/x.go", Start: 12, End: 15}}},
			{Item: gateItem{ID: "p#3"}, Cands: []reuseCand{{Path: "other/q.go", Start: 1, End: 4}}},
		}},
		judges: map[string]map[string]string{"decider": {"p#1": verdictReuse, "p#2": verdictExtend, "p#3": verdictNew, "p#4": verdictNew}},
	}
	ref := map[string]refEntry{
		"p#1":               {Package: "p", Verdict: "duplicate", Candidate: "a/x.go:10-20"}, // a candidate overlaps it
		"p#2":               {Package: "p", Verdict: "extend", Candidate: "b/y.go:5-9"},      // only an author hint names the file
		"p#3":               {Package: "p", Verdict: "duplicate", Candidate: "c/z.go:1-3"},   // never shown
		"p#4":               {Package: "p", Verdict: "new"},                                  // no target to see
		"p#5":               {Package: "p", Verdict: "unclear", Candidate: "d/w.go:1"},       // unknown classes are left out
		"location:e/v.go:1": {Package: "p", Verdict: "duplicate", Candidate: "a/x.go:14"},    // a key item no checklist row matches by id
	}
	v := keyViewOf(a, ref)
	if got := v.Seen["duplicate"]; got != (seenCount{Total: 3, Seen: 2}) {
		t.Errorf("duplicates seen = %+v", got)
	}
	if got := v.Seen["variation_site"]; got != (seenCount{Total: 1, Seen: 1, Hint: 1}) {
		t.Errorf("variation sites seen = %+v", got)
	}
	want := keyAgree{Agree: 3, Both: 4, SeenAgree: 2, SeenBoth: 2, UnseenAgree: 0, UnseenBoth: 1}
	if got := v.Agree["decider"]; got != want {
		t.Errorf("decider agreement = %+v, want %+v", got, want)
	}
	if _, ok := v.Agree["flash"]; ok {
		t.Error("a judge with no verdicts has no agreement row")
	}
	var out bytes.Buffer
	printKeyView(&out, []*armData{a}, ref)
	for _, part := range []string{"seen by the judges, arm A: duplicate 2/3 (66%), variation_site 1/1 (100%)", "agreement with the key, A.decider: 3/4 (75%); targets seen 2/2 (100%), not seen 0/1 (0%)"} {
		if !strings.Contains(out.String(), part) {
			t.Errorf("output lacks %q:\n%s", part, out.String())
		}
	}
}
