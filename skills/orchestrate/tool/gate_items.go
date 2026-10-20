package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Items are the Builds bullets, with stable ids (<package>#<n>). Only explicit
// search lines retrieve candidates; the deliverable text is for humans.

const (
	itemPageMain  = 12 // items before "more items"
	verdictReuse  = "reuse"
	verdictExtend = "extend"
	verdictNew    = "new"
	verdictUnclr  = "unclear"
)

type gateItem struct {
	ID      string   `json:"id"`
	Package string   `json:"package"`
	Text    string   `json:"text"`
	Kind    string   `json:"kind"`
	Queries []string `json:"queries,omitempty"`
	Reuse   []string `json:"reuse,omitempty"`
}

// UnmarshalJSON also reads old experiment files whose reuse field was a string.
func (it *gateItem) UnmarshalJSON(b []byte) error {
	type plain gateItem
	var raw struct {
		*plain
		Reuse json.RawMessage `json:"reuse"`
	}
	raw.plain = (*plain)(it)
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw.Reuse) == 0 {
		return nil
	}
	var legacy string
	if json.Unmarshal(raw.Reuse, &legacy) == nil {
		it.Reuse = splitList(legacy)
		return nil
	}
	return json.Unmarshal(raw.Reuse, &it.Reuse)
}

type buildItem struct {
	Text    string
	Queries []string
	Reuse   []string
}

const buildsMaxBullets = 8 // more than this is a warning: split the package

// parseBuilds reads the lines of a Builds section. A bullet continues on the
// following indented lines. It returns the bullets and the problems found.
func parseBuilds(text string) (items []buildItem, problems []string) {
	var cur *buildItem
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if m := bulletRe.FindStringSubmatch(l); m != nil {
			items = append(items, buildItem{Text: m[1]})
			cur = &items[len(items)-1]
		} else if cur != nil && len(l) > len(strings.TrimLeft(l, " \t")) {
			switch {
			case strings.HasPrefix(t, "search:"):
				if q := strings.TrimSpace(strings.TrimPrefix(t, "search:")); q != "" {
					cur.Queries = append(cur.Queries, q)
				}
			case strings.HasPrefix(t, "reuse:"):
				cur.Reuse = append(cur.Reuse, splitList(strings.TrimPrefix(t, "reuse:"))...)
			case t != "":
				cur.Text += " " + t
			}
		}
	}
	return items, problems
}

// packageItems lists the items of a package: its Builds bullets, verbatim.
func packageItems(p *specPackage) []gateItem {
	var out []gateItem
	for i, b := range p.builds {
		out = append(out, gateItem{ID: fmt.Sprintf("%s#%d", p.id, i+1), Package: p.id, Text: b.Text, Kind: "builds", Queries: b.Queries, Reuse: b.Reuse})
	}
	return out
}

func writeItems(dir string, items map[string][]gateItem, order []string) error {
	var all []gateItem
	for _, id := range order {
		all = append(all, items[id]...)
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "items.json"), append(b, '\n'), 0o644)
}

// ---- verdicts ----

// flexStr decodes a JSON string, null or any other value into a string.
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexStr(s)
		return nil
	}
	if t := strings.TrimSpace(string(b)); t != "null" {
		*f = flexStr(t)
	} else {
		*f = ""
	}
	return nil
}

// itemVerdict is the model's answer for one item.
type itemVerdict struct {
	Item      string  `json:"item"`
	Verdict   string  `json:"verdict"`
	Candidate flexStr `json:"candidate"`
	Why       string  `json:"why"`
	fields    map[string]json.RawMessage
}

func (v *itemVerdict) UnmarshalJSON(b []byte) error {
	type plain itemVerdict
	if err := json.Unmarshal(b, (*plain)(v)); err != nil {
		return err
	}
	return json.Unmarshal(b, &v.fields)
}

// gateItemResult is one item in gate.json: its verdict and where it came from.
type gateItemResult struct {
	ID        string `json:"id"`
	Package   string `json:"package"`
	Text      string `json:"text"`
	Verdict   string `json:"verdict"` // reuse, extend, new, unclear, or "" for none
	Candidate string `json:"candidate,omitempty"`
	Why       string `json:"why,omitempty"`
	Pass      string `json:"source_pass,omitempty"`
	Verified  bool   `json:"verified,omitempty"` // the candidate's evidence holds
}

// normVerdict maps a verdict word to one of the four values, "" if unknown.
func normVerdict(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "reuse", "duplicate":
		return verdictReuse
	case "extend", "variation":
		return verdictExtend
	case "new":
		return verdictNew
	case "unclear", "unknown":
		return verdictUnclr
	}
	return ""
}

// itemKey makes "api#2", "2" and "#2" the same item of package pkg.
func itemKey(pkg, id string) string {
	id = strings.TrimSpace(id)
	if _, err := strconv.Atoi(strings.TrimPrefix(id, "#")); err == nil {
		return pkg + "#" + strings.TrimPrefix(id, "#")
	}
	return id
}

// ensureItems adds an empty result for each item not yet in the record.
func (r *gateRecord) ensureItems(items []gateItem) {
	have := map[string]bool{}
	for _, it := range r.Items {
		have[it.ID] = true
	}
	for _, it := range items {
		if !have[it.ID] {
			r.Items = append(r.Items, gateItemResult{ID: it.ID, Package: it.Package, Text: it.Text})
		}
	}
}

// itemFindings turns the verdicts of one package into results and findings.
// reuse stays a "reuse" finding (a concern); extend is an "extend" finding of
// severity note that names where the variation goes. An item that already has
// a supported verdict is left alone, so a retry only fills gaps.
func (r *gateRecord) applyVerdicts(pass, repo string, items []gateItem, vs []itemVerdict) {
	byID := map[string]itemVerdict{}
	for _, v := range vs {
		if len(items) == 0 {
			break
		}
		byID[itemKey(items[0].Package, v.Item)] = v
	}
	for _, it := range items {
		v, ok := byID[it.ID]
		verdict := normVerdict(v.Verdict)
		if !ok || verdict == "" {
			continue
		}
		for i := range r.Items {
			res := &r.Items[i]
			if res.ID != it.ID || res.covered() {
				continue
			}
			res.Verdict, res.Candidate, res.Why, res.Pass = verdict, strings.TrimSpace(string(v.Candidate)), v.Why, pass
			res.Verified = false
			if res.Candidate == "null" {
				res.Candidate = ""
			}
			switch verdict {
			case verdictReuse, verdictExtend:
				res.Verified = evidenceHolds(repo, res.Candidate)
				f := gateFinding{Severity: "concern", Where: "package " + it.Package, Evidence: res.Candidate, Why: v.Why, Source: "reuse", Verified: res.Verified}
				f.Claim = it.Text + " may duplicate " + res.Candidate
				if verdict == verdictExtend {
					f.Severity, f.Source = "note", "extend"
					f.Claim = it.Text + " is a variation of " + res.Candidate
				}
				if !f.Verified {
					r.Dropped++
				}
				r.Findings = append(r.Findings, f)
			}
		}
	}
}

// covered requires a valid candidate for reuse/extend, not just a verdict word.
func (r gateItemResult) covered() bool {
	switch r.Verdict {
	case verdictReuse, verdictExtend:
		return r.Verified
	case verdictNew, verdictUnclr:
		return true
	}
	return false
}

// missingItems lists the items that still need a supported verdict.
func (r *gateRecord) missingItems(items []gateItem) []gateItem {
	done := map[string]bool{}
	for _, res := range r.Items {
		if res.covered() {
			done[res.ID] = true
		}
	}
	var out []gateItem
	for _, it := range items {
		if !done[it.ID] {
			out = append(out, it)
		}
	}
	return out
}

// coverageLine is the summary shown on stdout, in the report and by compare.
func (r *gateRecord) coverageLine() string {
	if len(r.Items) == 0 {
		return ""
	}
	n := map[string]int{}
	none := 0
	for _, it := range r.Items {
		if !it.covered() {
			none++
		} else {
			n[it.Verdict]++
		}
	}
	return fmt.Sprintf("items: %d, with a verdict %d (reuse %d, extend %d, new %d, unclear %d), no verdict %d",
		len(r.Items), len(r.Items)-none, n[verdictReuse], n[verdictExtend], n[verdictNew], n[verdictUnclr], none)
}

// itemEntry is the part of a group reply the verdict pass reads back.
type itemsReply struct {
	Packages []groupEntry `json:"packages"`
}

// retryAnswers decodes a coverage retry; only a complete JSON reply can
// establish coverage.
func retryAnswers(raw string) ([]groupEntry, bool) {
	var r itemsReply
	if err := extractJSON(raw, &r); err == nil {
		return r.Packages, true
	}
	// A fragment can be retained for diagnosis by the group decoder, but
	// cannot establish review completion or item coverage.
	return nil, false
}
