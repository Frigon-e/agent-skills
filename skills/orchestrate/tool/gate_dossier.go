package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The gate's use of the retrieval layer: a dossier per spec package, built by
// code with the brief builder and put after the package text of a group pass.
// With `--dossier plan` a tool-less planner pass asks for further lookups,
// which the layer's batch executor runs and which become section 5.

const dossierTitle = "Facts gathered by code (exact; trust these, open files only to check what they do not cover)"

// dossierSentence is added to the task of a group pass that has a dossier.
const dossierSentence = " Use the facts gathered by code that follow the package text: they were computed by tools, so do not re-derive them. Section 3b lists files you may not edit because other packages own them, or nothing in the spec does: list one in will_change only when this package cannot meet its Builds without editing it, and not when its owner uses this package and wires it in. When existing code outside this package does most of an item's job, prefer extracting the shared piece (into this package's owns, or an existing shared package) to rewriting it, say where it should live, and name the owner if the extraction needs an edit there."

// dossierSet holds the dossier of each package asked for.
type dossierSet struct {
	e        *ctxEnv
	blocks   map[string]string
	sp       *spec
	docs     map[string]*ctxBrief
	verdicts bool                   // the group pass is asked for a verdict per item (--verdicts)
	items    map[string][]gateItem  // the items of each package
	cands    map[string][]itemCands // ... and their candidates
}

func newDossierSet(e *ctxEnv, sp *spec, blocks map[string]string) *dossierSet {
	return &dossierSet{e: e, sp: sp, blocks: blocks, docs: map[string]*ctxBrief{}, items: map[string][]gateItem{}, cands: map[string][]itemCands{}}
}

// build computes the dossiers of the packages with these ids.
func (d *dossierSet) build(ids []string) {
	var ins []briefInput
	for _, id := range ids {
		for _, p := range d.sp.packages {
			if p.id == id && d.docs[id] == nil {
				ins = append(ins, briefInput{ID: id, Title: p.title, Text: d.blocks[id], Body: p.body, Builds: buildTexts(p), Owns: p.owns})
			}
		}
	}
	for _, b := range d.e.Briefs(ins) {
		d.docs[b.ID] = b
	}
	for _, in := range ins {
		var p *specPackage
		for _, q := range d.sp.packages {
			if q.id == in.ID {
				p = q
			}
		}
		items := packageItems(p)
		d.items[in.ID] = items
		if len(items) > 0 {
			d.cands[in.ID] = d.e.reuseCandidates(items, p.owns, p.title)
			d.docs[in.ID].Reuse = renderChecklist(d.cands[in.ID], nil)
		}
		d.docs[in.ID].Foreign = d.foreign(p)
	}
}

// hasItems reports whether any of the packages has items.
func (d *dossierSet) hasItems(ids []string) bool {
	for _, id := range ids {
		if len(d.items[id]) > 0 && d.docs[id] != nil {
			return true
		}
	}
	return false
}

// itemsOf lists the items of the packages, in package order.
func (d *dossierSet) itemsOf(ids []string) []gateItem {
	var out []gateItem
	for _, id := range ids {
		out = append(out, d.items[id]...)
	}
	return out
}

// checklistFor is the reuse checklist of just these items, for a coverage retry.
func (d *dossierSet) checklistFor(items []gateItem) string {
	only := map[string]bool{}
	for _, it := range items {
		only[it.ID] = true
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.Package] {
			continue
		}
		seen[it.Package] = true
		if t := renderChecklist(d.cands[it.Package], only); t != "" {
			b.WriteString("Package " + it.Package + ":\n" + t)
		}
	}
	return b.String()
}

func (d *dossierSet) text(id string) string {
	return d.docs[id].Render("## Package "+id, "package", dossierTotal)
}

// section is the text added after the package blocks of a group pass.
func (s *dossierSet) section(ids []string) string {
	var parts []string
	for _, id := range ids {
		if s.docs[id] != nil {
			parts = append(parts, s.text(id))
		}
	}
	return "\n\n" + dossierTitle + ":\n" + strings.Join(parts, "\n")
}

func (s *dossierSet) sizes() map[string]int {
	out := map[string]int{}
	for id := range s.docs {
		out[id] = len(s.text(id))
	}
	return out
}

// write saves each package's dossier under dir/dossier/<package>.md.
func (s *dossierSet) write(dir string) error {
	if _, err := s.writeFiles(filepath.Join(dir, "dossier")); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.cands, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "checklist.json"), append(b, '\n'), 0o644)
}

// writeFiles saves each package's dossier as dir/<package>.md and returns the
// paths, sorted.
func (s *dossierSet) writeFiles(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var paths []string
	for id := range s.docs {
		p := filepath.Join(dir, passFile(id)+".md")
		if err := os.WriteFile(p, []byte(s.text(id)), 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// dossierSummary is the size line shown on stdout, in the report and in a dry run.
func dossierSummary(mode string, sizes map[string]int) string {
	total := 0
	for _, n := range sizes {
		total += n
	}
	return fmt.Sprintf("%s, %d package(s), %d chars (~%d tokens)", mode, len(sizes), total, (total+3)/4)
}

// ---- the planner arm ----

// lookup is one request of the planner: a layer lookup for a package.
type lookup struct {
	Package string `json:"package"`
	ctxLookup
}

const planSchema = `{"lookups":[{"package":"id","kind":"concept|symbol|text|read","query":"","path":"","from":0,"to":0,"why":""}]}`

func planTask(ids []string) string {
	return `You plan the lookups that code will run for you before the real review. You have NO tools in this pass and must not guess about the code: ask. For each package under review, list up to ` + fmt.Sprint(ctxPlanMax) + ` lookups that would let a reviewer answer what the package needs to know: what exists already, where, and how it is called. Kinds: concept (query is a natural-language code search), symbol (query is a Go or TypeScript symbol such as pkg.Func or Type.Method; you get its definition, callers and implementers), text (query is an exact string or regular expression searched in the whole repo; path optionally restricts it to a file or directory), read (path with from and to, at most ` + fmt.Sprint(ctxReadLines) + ` lines). Set package to the package id and why to one short phrase.` + conceptQueryRule + ` Packages in this pass: ` + strings.Join(ids, ", ") + `.`
}

// decodeLookups reads the planner's reply; a reply that is not valid JSON is
// salvaged object by object. It reports false when no lookup could be read.
func decodeLookups(raw string) ([]lookup, bool) {
	var r struct {
		Lookups []lookup `json:"lookups"`
	}
	if err := extractJSON(raw, &r); err == nil {
		return r.Lookups, true
	}
	for _, m := range regexp.MustCompile(`\{[^{}]*\}`).FindAllString(raw, -1) {
		var l lookup
		if json.Unmarshal([]byte(m), &l) == nil && l.Kind != "" {
			r.Lookups = append(r.Lookups, l)
		}
	}
	return r.Lookups, len(r.Lookups) > 0
}

// runLookups runs the planner's lookups for the packages ids through the
// layer's batch executor and stores their output as section 5 of each package's
// dossier. Duplicates and lookups for other packages are dropped.
func (s *dossierSet) runLookups(ids []string, lks []lookup) {
	per := map[string][]lookup{}
	for _, id := range ids {
		per[id] = nil
	}
	seen := map[string]bool{}
	for _, l := range lks {
		l.Kind = strings.ToLower(strings.TrimSpace(l.Kind))
		k := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d", l.Package, l.Kind, l.Query, l.Path, l.From, l.To)
		if _, ok := per[l.Package]; !ok || seen[k] || len(per[l.Package]) >= ctxPlanMax {
			continue
		}
		seen[k] = true
		per[l.Package] = append(per[l.Package], l)
	}
	root := map[string]string{}
	for _, p := range s.sp.packages {
		root[p.id] = tsProjectRoot(s.e.Code, s.blocks[p.id], p.owns)
	}
	type item struct{ id string }
	var items []item
	var batch []ctxLookup
	for _, id := range ids {
		for _, l := range per[id] {
			items = append(items, item{id})
			batch = append(batch, l.ctxLookup)
		}
	}
	res := s.e.Batch(batch, func(i int) string { return root[items[i].id] }, ctxResultCaps)
	for _, id := range ids {
		d := s.docs[id]
		if d == nil {
			continue
		}
		d.Planned = []string{}
		for i, it := range items {
			if it.id == id {
				d.Planned = append(d.Planned, res[i].String())
			}
		}
		if len(d.Planned) == 0 {
			d.Planned = append(d.Planned, "(the planner asked for nothing for this package)")
		}
	}
}

func buildTexts(p *specPackage) []string {
	var out []string
	for _, b := range p.builds {
		out = append(out, b.Text)
	}
	return out
}

// The relation of an owning package to the package under review.
const (
	relDownstream = "which uses this package" // it depends on pkg: it wires pkg's exports in
	relUpstream   = "which this package uses" // pkg depends on it: it should already provide this
	relUnrelated  = "unrelated to this package"
)

// ownerOf is the package that owns file: the one with the longest matching
// owns entry, or nil when none does.
func (sp *spec) ownerOf(file string) *specPackage {
	var best *specPackage
	n := -1
	for _, p := range sp.packages {
		for _, o := range p.owns {
			if ownedPath([]string{o}, file) && len(strings.TrimSuffix(o, "/")) > n {
				best, n = p, len(strings.TrimSuffix(o, "/"))
			}
		}
	}
	return best
}

// dependsOn reports whether a depends on b, directly or through other packages.
func (sp *spec) dependsOn(a, b *specPackage) bool {
	seen := map[string]bool{}
	var walk func(p *specPackage) bool
	walk = func(p *specPackage) bool {
		for _, d := range p.deps {
			if d == b.id {
				return true
			}
			if seen[d] {
				continue
			}
			seen[d] = true
			for _, q := range sp.packages {
				if q.id == d && walk(q) {
					return true
				}
			}
		}
		return false
	}
	return walk(a)
}

// relation says how the owner relates to the package under review.
func (sp *spec) relation(pkg, owner *specPackage) string {
	switch {
	case sp.dependsOn(owner, pkg):
		return relDownstream
	case sp.dependsOn(pkg, owner):
		return relUpstream
	}
	return relUnrelated
}

var changeNoteRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)

// changePath is the path in a model's will_change entry, without a trailing
// remark such as "(new)" or "(new review queue, block list)".
func changePath(f string) string {
	return strings.Trim(changeNoteRe.ReplaceAllString(strings.TrimSpace(f), ""), "` ")
}

// foreign is section 3b of a package's dossier: the files the dossier names
// that sit outside the package's owns, with who owns them and how that package
// relates to this one, so a change there is read as wiring by its owner and not
// as a change this package has to make.
func (d *dossierSet) foreign(pkg *specPackage) string {
	doc := d.docs[pkg.id]
	if doc == nil {
		return ""
	}
	var paths []string
	for _, r := range doc.FileRows {
		paths = append(paths, r.Path)
	}
	for _, r := range doc.LikeRows {
		p, _ := splitWhere(r.At)
		paths = append(paths, p)
	}
	for _, ic := range d.cands[pkg.id] {
		for _, c := range ic.Cands {
			paths = append(paths, c.Path)
		}
	}
	byOwner := map[string][]string{}
	seen := map[string]bool{}
	var order []string
	for _, p := range paths {
		if p == "" || seen[p] || isTestPath(p) || ownedPath(pkg.owns, p) {
			continue
		}
		seen[p] = true
		label := "owned by no package (existing code)"
		if o := d.sp.ownerOf(p); o != nil && o.id != pkg.id {
			label = "owned by " + o.id + ", " + d.sp.relation(pkg, o)
		}
		if _, ok := byOwner[label]; !ok {
			order = append(order, label)
		}
		byOwner[label] = append(byOwner[label], p)
	}
	if len(order) == 0 {
		return ""
	}
	var b strings.Builder
	sort.Strings(order)
	for _, label := range order {
		ps := byOwner[label]
		sort.Strings(ps)
		more := ""
		if len(ps) > 8 {
			more, ps = fmt.Sprintf(", +%d more", len(ps)-8), ps[:8]
		}
		b.WriteString("- " + label + ": " + strings.Join(ps, ", ") + more + "\n")
	}
	return b.String()
}
