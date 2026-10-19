package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Per-item part of `orch gate compare`: the verdict of each judge for each
// item (the group pass, the shadow decider, the shadow flash judge) next to a
// reference answer key, their agreement and the precision and recall of each
// arm's reuse findings against the key.

// refEntry is one line of a reference file: a strong model's verdict on one
// item. Verdict values: duplicate (or reuse), extend, new, unclear.
type refEntry struct {
	Package   string `json:"package"`
	Item      string `json:"item"` // an item id or the item's text
	Verdict   string `json:"verdict"`
	Candidate string `json:"candidate"`
	Note      string `json:"note"`
}

func loadReference(path string) ([]refEntry, error) {
	b, err := os.ReadFile(expandHome(path))
	if err != nil {
		return nil, err
	}
	var out []refEntry
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("reference %s: %v", path, err)
	}
	for i := range out {
		if normVerdict(out[i].Verdict) == "" {
			return nil, fmt.Errorf("reference %s: entry %d has verdict %q; use duplicate, reuse, extend, new or unclear", path, i+1, out[i].Verdict)
		}
	}
	return out, nil
}

// wordShare is the share of the words of the shorter text that the other has.
func wordShare(a, b string) float64 {
	ta, tb := questionTokens(a), questionTokens(b)
	small := min(len(ta), len(tb))
	if small == 0 {
		return 0
	}
	n := 0
	for w := range ta {
		if tb[w] {
			n++
		}
	}
	return float64(n) / float64(small)
}

const itemMatchShare = 0.6

// matchText finds the item whose text best matches text (the share of words
// agreeing at least itemMatchShare); pkg limits the candidates when set.
func matchText(items []gateItem, pkg, text string) (string, bool) {
	best, bestID := itemMatchShare, ""
	for _, it := range items {
		if pkg != "" && it.Package != pkg {
			continue
		}
		if s := wordShare(it.Text, text); s >= best {
			best, bestID = s, it.ID
		}
	}
	return bestID, bestID != ""
}

// resolveReference maps item ids to reference entries, matching an entry by
// id or by word overlap with the item text.
func resolveReference(ref []refEntry, items []gateItem) map[string]refEntry {
	out := map[string]refEntry{}
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
	}
	for _, r := range ref {
		id := itemKey(r.Package, r.Item)
		if !ids[id] {
			var ok bool
			if id, ok = matchText(items, r.Package, r.Item); !ok {
				if r.Candidate == "" {
					continue
				}
				id = "location:" + r.Candidate
			}
		}
		if previous, dup := out[id]; !dup {
			out[id] = r
		} else if r.Candidate != "" && previous.Candidate != r.Candidate {
			out["location:"+r.Candidate] = r
		}
	}
	return out
}

// armData is what compare knows about one experiment's items.
type armData struct {
	name      string
	locations map[string]map[string]map[string]bool // judge -> verdict -> locations
	checklist map[string][]itemCands
	stats     string
	items     []gateItem
	group     map[string]string
	judges    map[string]map[string]string // judge -> item -> verdict
	rows      []shadowRow
	pass      map[string]float64         // reuse-finding positives by verdict kind, not used when group is set
	finding   map[string]map[string]bool // verdict kind -> item ids, from the findings of a run without item results
}

func readItemsFile(dir string) []gateItem {
	b, err := os.ReadFile(filepath.Join(dir, "items.json"))
	if err != nil {
		return nil
	}
	var its []gateItem
	_ = json.Unmarshal(b, &its)
	return its
}

func readShadowRows(gdir, name string) []shadowRow {
	b, err := os.ReadFile(filepath.Join(shadowDir(gdir, name), "items.jsonl"))
	if err != nil {
		return nil
	}
	var out []shadowRow
	for _, l := range strings.Split(string(b), "\n") {
		var r shadowRow
		if l != "" && json.Unmarshal([]byte(l), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func loadArm(gdir, name, dir string, rec *gateRecord) *armData {
	a := &armData{name: name, items: readItemsFile(dir), group: map[string]string{}, judges: map[string]map[string]string{}, finding: map[string]map[string]bool{}}
	for _, r := range rec.Items {
		if r.Verdict != "" {
			a.group[r.ID] = r.Verdict
		}
	}
	if len(a.items) == 0 {
		for _, r := range rec.Items {
			a.items = append(a.items, gateItem{ID: r.ID, Package: r.Package, Text: r.Text})
		}
	}
	a.locations = map[string]map[string]map[string]bool{}
	b, _ := os.ReadFile(filepath.Join(dir, "checklist.json"))
	_ = json.Unmarshal(b, &a.checklist)
	a.stats = checklistStats(dir, a.checklist)
	if a.checklist == nil {
		a.checklist = legacyChecklist(dir)
	}
	for _, r := range rec.Items {
		a.addLocation("group", r.Verdict, r.Candidate)
	}
	for _, f := range rec.Findings {
		kind := map[string]string{"reuse": verdictReuse, "extend": verdictExtend}[f.Source]
		a.addLocation("group", kind, f.Evidence)
	}
	a.rows = readShadowRows(gdir, name)
	for _, r := range a.rows {
		if r.Verdict == "" {
			continue
		}
		if a.judges[r.Judge] == nil {
			a.judges[r.Judge] = map[string]string{}
		}
		a.judges[r.Judge][r.Item] = r.Verdict
		if r.At != "" {
			a.addLocation(r.Judge, r.Verdict, r.At)
		} else if n, err := strconv.Atoi(r.Best); err == nil {
			for _, ic := range a.checklist[r.Package] {
				if ic.Item.ID == r.Item && n > 0 && n <= len(ic.Cands) {
					a.addLocation(r.Judge, r.Verdict, ic.Cands[n-1].at())
				}
			}
		}
	}
	// A run without item results (the group pass gave no verdicts) still has
	// reuse and extend findings: match their claims to items by words.
	if len(a.group) == 0 {
		for _, f := range rec.Findings {
			kind := map[string]string{"reuse": verdictReuse, "extend": verdictExtend}[f.Source]
			if kind == "" {
				continue
			}
			if id, ok := matchText(a.items, strings.TrimPrefix(f.Where, "package "), f.Claim); ok {
				if a.finding[kind] == nil {
					a.finding[kind] = map[string]bool{}
				}
				a.finding[kind][id] = true
			}
		}
	}
	return a
}

// positives are the item ids an arm judged as kind: the group pass's verdicts
// or, failing those, its findings.
func (a *armData) positives(judge, kind string) map[string]bool {
	out := map[string]bool{}
	switch judge {
	case "group":
		if len(a.group) > 0 {
			for id, v := range a.group {
				if v == kind {
					out[id] = true
				}
			}
			return out
		}
		return a.finding[kind]
	default:
		for id, v := range a.judges[judge] {
			if v == kind {
				out[id] = true
			}
		}
	}
	return out
}

func (a *armData) verdict(judge, id string) string {
	if judge == "group" {
		if v, ok := a.group[id]; ok {
			return v
		}
		for _, k := range []string{verdictReuse, verdictExtend} {
			if a.finding[k][id] {
				return k
			}
		}
		return ""
	}
	return a.judges[judge][id]
}

func pct(n, d int) string {
	if d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d%%", n*100/d)
}

func printItemComparison(w io.Writer, arms []*armData, ref map[string]refEntry, hasRef bool) {
	for i, a := range arms {
		fmt.Fprintf(w, "checklist %s: %s\n", string(rune('A'+i)), orDefault(a.stats, "not recorded"))
	}
	itemsByID := map[string]gateItem{}
	var order []string
	for _, a := range arms {
		for _, it := range a.items {
			if _, ok := itemsByID[it.ID]; !ok {
				itemsByID[it.ID] = it
				order = append(order, it.ID)
			}
		}
	}
	if len(order) == 0 {
		return
	}
	judges := []string{"group", "decider", "flash"}
	cell := func(a *armData, id string) string {
		var parts []string
		for _, j := range judges {
			parts = append(parts, j+"="+orDefault(a.verdict(j, id), "-"))
		}
		return strings.Join(parts, " ")
	}
	fmt.Fprintf(w, "per-item verdicts (%d items; group = the group pass, decider and flash = shadow judges, record only):\n", len(order))
	for _, id := range order {
		line := fmt.Sprintf("  %s \"%s\"", id, short(flat(itemsByID[id].Text), 40))
		for i, a := range arms {
			line += fmt.Sprintf(" | %s [%s]", string(rune('A'+i)), cell(a, id))
		}
		if hasRef {
			line += " | reference=" + orDefault(normVerdict(ref[id].Verdict), "-")
		}
		fmt.Fprintln(w, line)
	}
	// Agreement between each pair of judges.
	type judge struct {
		label string
		get   func(id string) string
	}
	var js []judge
	for i, a := range arms {
		for _, j := range judges {
			a, j := a, j
			js = append(js, judge{string(rune('A'+i)) + "." + j, func(id string) string { return a.verdict(j, id) }})
		}
	}
	if hasRef {
		js = append(js, judge{"reference", func(id string) string { return normVerdict(ref[id].Verdict) }})
	}
	sameArm := func(x, y string) bool { return x[0] == y[0] && len(x) > 1 && len(y) > 1 && x[1] == '.' && y[1] == '.' }
	for i := range js {
		for k := i + 1; k < len(js); k++ {
			x, y := js[i], js[k]
			related := sameArm(x.label, y.label) || y.label == "reference" || (x.label[2:] == y.label[min(2, len(y.label)):] && x.label[0] != y.label[0])
			if !related {
				continue
			}
			both, agree := 0, 0
			var dis []string
			for _, id := range order {
				vx, vy := x.get(id), y.get(id)
				if vx == "" || vy == "" {
					continue
				}
				both++
				if vx == vy {
					agree++
				} else if len(dis) < 10 {
					dis = append(dis, fmt.Sprintf("    %s \"%s\": %s vs %s", id, short(flat(itemsByID[id].Text), 80), vx, vy))
				}
			}
			if both == 0 {
				continue
			}
			fmt.Fprintf(w, "agreement %s ~ %s: %d/%d (%s)\n", x.label, y.label, agree, both, pct(agree, both))
			for _, d := range dis {
				fmt.Fprintln(w, d)
			}
		}
	}
	// Cost and time of each judge.
	for i, a := range arms {
		for _, j := range []string{"decider", "flash"} {
			n, errs := 0, 0
			var cost, secs float64
			tokens := 0
			for _, r := range a.rows {
				if r.Judge != j {
					continue
				}
				n++
				cost += r.Cost
				secs += r.Seconds
				tokens += r.Tokens
				if r.Error != "" {
					errs++
				}
			}
			if n > 0 {
				fmt.Fprintf(w, "judge %s.%s: %d rows, %d errors, $%.4f, %.0fs, %d tokens\n", string(rune('A'+i)), j, n, errs, cost, secs, tokens)
			}
		}
	}
	if !hasRef {
		return
	}
	printKeyView(w, arms, ref)
	printLocationScores(w, arms, ref, judges)
}

// Score locations, not item ids: Builds bullets can change between arms.
func printLocationScores(w io.Writer, arms []*armData, ref map[string]refEntry, judges []string) {
	for _, kind := range []string{verdictReuse, verdictExtend} {
		refPos := map[string]bool{}
		for _, r := range ref {
			if normVerdict(r.Verdict) == kind && r.Candidate != "" {
				refPos[r.Candidate] = true
			}
		}
		for i, a := range arms {
			for _, j := range judges {
				pos := a.locations[j][kind]
				if len(pos) == 0 && len(refPos) == 0 {
					continue
				}
				score := scoreLocations(a, ref, j, kind)
				tp := score.Match
				if len(pos) == 0 && j != "group" {
					continue
				}
				fmt.Fprintf(w, "reference %s, %s.%s (locations): %d found, %d in the reference, %d match; precision %s, recall %s\n", kind, string(rune('A'+i)), j, len(pos), len(refPos), tp, pct(tp, len(pos)), pct(tp, len(refPos)))
			}
		}
	}
}
