package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The gate's use of the retrieval layer: a dossier per spec package, built by
// code with the brief builder and put after the package text of a group pass.
// With `--dossier plan` a tool-less planner pass asks for further lookups,
// which the layer's batch executor runs and which become section 5.

const dossierTitle = "Evidence dossier: observations, unverified candidates, and unresolved work"

// dossierSentence is added to the task of a group pass that has a dossier.
const dossierSentence = " Use the evidence dossier after the package text. Observations report bounded tool execution, not semantic proof. Candidates require review; unresolved work and omitted bodies need targeted expansion. Section 3b lists files you may not edit because other packages own them, or nothing in the spec does: list one in will_change only when this package cannot meet its Builds without editing it, and not when its owner uses this package and wires it in. When existing code outside this package does most of an item's job, prefer extracting the shared piece (into this package's owns, or an existing shared package) to rewriting it, say where it should live, and name the owner if the extraction needs an edit there."

// dossierSet holds the dossier of each package asked for.
type dossierSet struct {
	e        *ctxEnv
	subject  *gateSubject
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

// dossierJob is one text or concept search of the dossier. A text job looks
// name up in the tracked files; a concept job searches query within tree.
type dossierJob struct {
	text       bool
	name, tree string
}

// jobOut is what a job returned and when it ran.
type jobOut struct {
	text    rgResult
	hits    []codeHit
	err     error
	started time.Time
	elapsed time.Duration
}

const (
	// dossierPoolWidth is the most concept searches the dossier keeps in flight,
	// below the slot cap: the service answers 429 when it is asked too fast, and
	// a refused search is a lookup the dossier loses. Measured on the CRM spec
	// (4 packages): width 4 had no refusal, 6 had 3, 8 had 25, 16 had 64.
	dossierPoolWidth = 5
	// dossierRateRounds is how many times the searches the service refused are
	// asked again, after the cooldown, one half as wide each time.
	dossierRateRounds = 2
)

// dossierCooldownWait waits until jbcontext is out of its cooldown, at most as
// long as the cooldown plus a margin. A variable so a test need not wait.
var dossierCooldownWait = func(bin string) {
	for end := time.Now().Add(jbCooldown + 5*time.Second); jbCooling(bin) && time.Now().Before(end); {
		time.Sleep(250 * time.Millisecond)
	}
}

// runJobs runs the jobs and returns their answers in job order, whatever the
// order they finished in. The text searches run a few at a time; the concept
// searches run as one pool, as wide as the slot cap allows up to
// dossierPoolWidth, at the same time. A concept search the service refused for
// rate is asked again after the cooldown; its timing is that of the last ask.
func (e *ctxEnv) runJobs(jobs []dossierJob) []jobOut {
	outs := make([]jobOut, len(jobs))
	var texts, concepts []int
	for i, j := range jobs {
		if j.text {
			texts = append(texts, i)
		} else {
			concepts = append(concepts, i)
		}
	}
	run := func(i int) {
		j := jobs[i]
		o := &outs[i]
		o.started = time.Now()
		if j.text {
			o.text, o.err = e.Text(rgQuery{Pattern: j.name, Fixed: true, PerFile: 3, AllFiles: true, CaseSensitive: true})
		} else {
			o.hits, o.err = e.Concept(j.name, j.tree, dossierConceptHits)
		}
		o.elapsed = time.Since(o.started)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		e.parallel(len(texts), func(k int) { run(texts[k]) })
	}()
	width := min(jbSlotCount(), dossierPoolWidth)
	go func() {
		defer wg.Done()
		fanOut(width, len(concepts), func(k int) { run(concepts[k]) })
		for round := 1; round <= dossierRateRounds; round++ {
			var again []int
			for _, i := range concepts {
				if errors.Is(outs[i].err, errJBRateLimited) {
					again = append(again, i)
				}
			}
			if len(again) == 0 {
				return
			}
			dossierCooldownWait(e.JB)
			fanOut(max(1, width>>round), len(again), func(k int) { run(again[k]) })
		}
	}()
	wg.Wait()
	return outs
}

// build computes the dossiers of the packages with these ids. Every search of
// every package is planned first, then the symbol lookups, the text searches and
// the concept searches all run at once, and only then are the answers put
// together, package by package, in the order the searches were planned.
func (d *dossierSet) build(ids []string) {
	var ins []briefInput
	for _, id := range ids {
		for _, p := range d.sp.packages {
			if p.id == id && d.docs[id] == nil {
				ins = append(ins, briefInput{ID: id, Title: p.title, Text: d.blocks[id], Body: p.body, Builds: buildTexts(p), Owns: p.owns})
			}
		}
	}
	br := d.e.briefsPrepare(ins)
	pkgs := make([]*specPackage, len(ins))
	rps := make([]*reusePlan, len(ins))
	jobs := append([]dossierJob{}, br.jobs...)
	cut := []int{len(jobs)} // cut[i+1] is where the jobs of ins[i] end
	for i, in := range ins {
		for _, q := range d.sp.packages {
			if q.id == in.ID {
				pkgs[i] = q
			}
		}
		items := packageItems(pkgs[i])
		d.items[in.ID] = items
		if len(items) > 0 {
			rps[i] = d.e.planReuse(items, pkgs[i].owns)
			jobs = append(jobs, rps[i].run.jobs...)
		}
		cut = append(cut, len(jobs))
	}
	// One lane for the symbol lookups, in the order they ran one after the other
	// before, beside the searches.
	var lane sync.WaitGroup
	lane.Add(1)
	go func() {
		defer lane.Done()
		br.symbols()
		for _, rp := range rps {
			if rp != nil {
				rp.run.symbols()
			}
		}
	}()
	outs := d.e.runJobs(jobs)
	lane.Wait()
	for _, b := range br.finish(outs[:len(br.jobs)]) {
		d.docs[b.ID] = b
	}
	for i, in := range ins {
		if rps[i] != nil {
			d.cands[in.ID] = rps[i].finish(outs[cut[i]:cut[i+1]])
			d.docs[in.ID].ReuseItems = d.cands[in.ID]
			d.docs[in.ID].Reuse = renderChecklist(d.cands[in.ID], nil, d.e.Code)
		}
		d.docs[in.ID].Foreign = d.foreign(pkgs[i])
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
		if t := renderChecklist(d.cands[it.Package], only, d.e.Code); t != "" {
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
	if err := os.WriteFile(filepath.Join(dir, "checklist.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	b, err = json.MarshalIndent(s.manifest(nil), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "dossier-manifest.json"), append(b, '\n'), 0o644)
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
		k := l.Package + "\x00" + subjectJSONDigest(l.ctxLookup)
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
	// Keep structured status until after recording it. The old Batch string
	// API could not distinguish failed, unknown and completed-empty lookups.
	views := s.lookupViews(batch, func(i int) string { return root[items[i].id] })
	for _, id := range ids {
		d := s.docs[id]
		if d == nil {
			continue
		}
		d.Planned = []string{}
		d.PlannedIDs = nil
		d.plannedShown = map[string][]string{}
		seenBodies := map[string]bool{}
		for _, er := range d.Evidence {
			seenBodies[er.ID] = true
		}
		for _, ic := range s.cands[id] {
			for _, c := range ic.Cands {
				seenBodies[c.EvidenceID] = true
			}
		}
		for i, it := range items {
			if it.id == id {
				v := views[i]
				v.finishTruth()
				lk := batch[i]
				var runErr error
				if v.Err != "" {
					runErr = fmt.Errorf("%s", v.Err)
				}
				l := dossierLookup(orDefault(lk.Query, lk.Path), orDefault(lk.Path, "."), lk.Kind, v.Total, 0, runErr, "")
				l.ID = "lookup-" + subjectJSONDigest([]any{lk, root[id], v.Status, v.CountKind, v.Total, v.Err})[:20]
				l.Scope = string(mustDossierJSON(struct {
					Lookup ctxLookup `json:"lookup"`
					Root   string    `json:"project_root"`
				}{lk, root[id]}))
				l.Status = v.Status
				l.CountKind = evidenceCountKind(v.CountKind)
				if v.CountKind == "unknown" {
					l.Counts.Total = nil
				}
				if v.Status == "partial" {
					l.Completeness = evidencePartial
				}
				if l.CountKind != evidenceCountExact {
					l.Completeness = evidencePartial
					if l.Status == "empty" || l.Status == "ok" {
						l.Status = "partial"
					}
				}
				if v.Cut != "" {
					l.Omissions = append(l.Omissions, v.Cut)
				}
				d.Lookups = append(d.Lookups, l)
				d.PlannedIDs = append(d.PlannedIDs, l.ID)
				for _, part := range v.Parts {
					for fi := range part.Files {
						file := &part.Files[fi]
						if len(file.Lines) == 0 {
							continue
						}
						start, end := file.Lines[0].N, file.Lines[len(file.Lines)-1].N
						if start > 0 && end >= start {
							er := dossierSource(s.e, file.Path, start, end)
							er.LookupID = l.ID
							d.Evidence = append(d.Evidence, er)
							if seenBodies[er.ID] {
								file.Lines = nil
								file.Notes = append(file.Notes, "source "+er.ID+" referenced elsewhere or in omissions; expand: "+dossierRead(er.Path, start, end))
							} else {
								seenBodies[er.ID] = true
								d.plannedShown[l.ID] = append(d.plannedShown[l.ID], er.ID)
							}
						}
					}
				}
				var out strings.Builder
				v.write(&out, ctxFmt{SchemaVersion: 2}, nil, 0, 0)
				what := lk.Query
				if lk.Kind == "read" {
					what = fmt.Sprintf("%s:%d-%d", lk.Path, lk.From, lk.To)
				}
				d.Planned = append(d.Planned, plannedEntry(lk.Kind, what, lk.Why, out.String()))
			}
		}
		if len(d.Planned) == 0 {
			d.Planned = append(d.Planned, "(the planner asked for nothing for this package)")
		}
	}
}

// plannedEntry is one section 5 entry: the planner's request, then the lookup
// output. The lookup record itself is in the manifest.
func plannedEntry(kind, what, why, out string) string {
	return fmt.Sprintf("- [%s] `%s` - %s\n%s", kind, what, why, out)
}

// ctxLookup contains only JSON value types. Keep the original selectors, not
// a shortened prose query, on every expansion route.
func mustDossierJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Errorf("internal dossier selector serialization: %w", err))
	}
	return b
}

func (s *dossierSet) lookupViews(batch []ctxLookup, root func(int) string) []*ctxView {
	var names []string
	var jobs []tsJob
	for i, l := range batch {
		if l.Kind == "symbol" && l.validate() == nil {
			names = addUniq(names, l.Query, len(batch))
			if r := root(i); r != "" && tsPlain.MatchString(l.Query) {
				jobs = append(jobs, tsJob{r, l.Query})
			}
		}
	}
	goFacts, tsFacts := s.e.Symbols(names, jobs)
	views := make([]*ctxView, len(batch))
	f := ctxFmt{SchemaVersion: 2}
	s.e.parallel(len(batch), func(i int) {
		l := batch[i]
		if err := l.validate(); err != nil {
			views[i] = &ctxView{Kind: l.Kind, Query: l.Query, Err: err.Error()}
			views[i].finishTruth()
			return
		}
		switch l.Kind {
		case "concept":
			views[i] = s.e.conceptLookup(l, f)
		case "text":
			views[i] = s.e.textLookup(l, f)
		case "read":
			views[i] = s.e.readLookup(l, f)
		case "symbol":
			fact := goFacts[l.Query]
			if fact == nil || len(fact.Defs) == 0 {
				if t := tsFacts[tsJob{root(i), l.Query}]; t != nil {
					fact = t
				}
			}
			if fact == nil {
				views[i] = &ctxView{Kind: "sym", Query: l.Query, Err: "symbol lookup unavailable or failed"}
			} else {
				views[i] = s.e.symView(*fact, l.symOpts(f))
			}
		}
		views[i].Why = l.Why
		views[i].finishTruth()
	})
	return views
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

// dossierLookup preserves B1 execution and completeness independently. The
// legacy semantic API has unknown global counts, even after execution completes.
func dossierLookup(query, scope, unit string, n, limit int, err error, unavailable string) evidenceLookup {
	zero, one := 0, 1
	l := evidenceLookup{SchemaVersion: evidenceSchemaVersion, ID: "lookup-" + subjectJSONDigest([]any{query, scope, unit, n, fmt.Sprint(err), unavailable})[:20], Query: query, Scope: orDefault(scope, "."), Unit: unit, Exclusions: []string{"spec input excluded; candidate selection separately excludes tests, generated files and owns"}, CountKind: evidenceCountUnknown, Completeness: evidenceUnknown, ExecutionStatus: lookupNotRequested, Counts: lookupCounts{Requested: &zero}}
	if unavailable != "" {
		l.ExecutionStatus = lookupUnavailable
		l.Counts.Requested = &one
		l.Errors = []string{unavailable}
		return l
	}
	l.Counts = lookupCounts{Requested: &one, Succeeded: &one, Failed: &zero, Total: &n}
	l.CountKind = evidenceCountExact
	l.Completeness = evidenceComplete
	l.ExecutionStatus = lookupSucceeded
	l.Status = "ok"
	if n == 0 {
		l.Status = "empty"
	}
	if limit > 0 && n >= limit {
		l.CountKind = evidenceCountLowerBound
		l.Completeness = evidencePartial
		l.Status = "partial"
		l.Omissions = []string{"bounded result page; narrow the same scope and retry"}
	}
	if unit == "concept" {
		// The legacy semantic API returns a filtered index window, not an
		// exhaustive repository count. Preserve A4's unknown completeness.
		l.Status = "partial"
		l.CountKind = evidenceCountUnknown
		l.Completeness = evidencePartial
		l.Counts.Total = nil
		l.Omissions = []string{fmt.Sprintf("completed bounded window: %d visible candidate(s); candidates never establish absence; expand lookup kind=concept query=%q path=%q tests=none generated=none", n, query, orDefault(scope, "."))}
	}
	if err != nil {
		l.ExecutionStatus = lookupFailed
		l.Status = "failed"
		l.Completeness = evidenceUnknown
		l.CountKind = evidenceCountUnknown
		l.Counts.Succeeded = &zero
		l.Counts.Failed = &one
		l.Counts.Total = nil
		l.Errors = []string{err.Error()}
	}
	return l
}

func dossierSymbolLookup(query, scope string, f *symFact) evidenceLookup {
	if f == nil {
		return dossierLookup(query, scope, "symbol", 0, 0, nil, "symbol backend returned no execution record")
	}
	var err error
	if len(f.Errors) > 0 {
		err = fmt.Errorf("%s", strings.Join(f.Errors, "; "))
	}
	l := dossierLookup(query, scope, "symbol", len(f.Defs), 0, err, "")
	l.ID = "lookup-" + subjectJSONDigest([]any{query, scope, "symbol", f})[:20]
	if err == nil {
		for _, d := range f.Defs {
			if d.Match != "exact" || d.RefsUnknown || len(d.Errors) > 0 {
				l.Status = "partial"
				l.Completeness = evidencePartial
				l.CountKind = evidenceCountLowerBound
				l.Omissions = append(l.Omissions, "candidate identity or references remain unresolved; expand the same scoped symbol lookup")
			}
		}
	}
	return l
}

func dossierTextLookup(query, scope string, r rgResult, err error) evidenceLookup {
	l := dossierLookup(query, scope, "text", r.Total, 0, err, "")
	l.Scope = string(mustDossierJSON(map[string]any{"path": orDefault(scope, "."), "fixed": true, "case_sensitive": true, "per_file": 3, "all_files": true}))
	if err == nil && (!r.Complete || r.Limited) {
		l.Status = "partial"
		l.Completeness = evidencePartial
		l.CountKind = evidenceCountLowerBound
		l.Omissions = []string{fmt.Sprintf("text retention/backend cap; expand lookup kind=text query=%q path=%q with the same case/word/glob selectors", query, orDefault(scope, "."))}
	}
	l.ID = "lookup-" + subjectJSONDigest([]any{query, scope, "text", r, fmt.Sprint(err)})[:20]
	return l
}

var (
	failureTemp    = regexp.MustCompile(`(^|[\s:(])(?:/private)?/(?:var/folders/[^/\s]+/[^/\s]+/T|tmp)`)
	failureMissing = regexp.MustCompile(`\S*/([^\s/]+?)(?: was)? not found`)
	failureKnown   = []struct {
		re *regexp.Regexp
		to string
	}{
		{regexp.MustCompile(`^(?:<repo>|<tmp>|~): `), ""},
		{regexp.MustCompile(`^no TypeScript facts: `), ""},
		{regexp.MustCompile(`no native TypeScript 7 server found(?: \([^)]*node_modules[^)]*\))?`), "typescript@7 server not found"},
		{regexp.MustCompile(`; set (ORCH_[A-Z0-9_]+) to one`), " (set $1)"},
	}
)

// lookupLine is the compact one-line observation of one lookup: its id,
// execution and completeness state, count and count kind, query and scope.
// Empty fields are dropped and machine-specific paths are shortened.
func lookupLine(l evidenceLookup, root string) string {
	count := "unknown"
	if l.Counts.Total != nil {
		count = fmt.Sprint(*l.Counts.Total)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  observation %s: %s", l.ID, l.ExecutionStatus)
	if l.Status != "" {
		fmt.Fprintf(&b, " status=%s", l.Status)
	}
	fmt.Fprintf(&b, " completeness=%s count=%s (%s)", l.Completeness, count, l.CountKind)
	if l.Query != "" {
		fmt.Fprintf(&b, " query=%q", l.Query)
	}
	fmt.Fprintf(&b, " scope=%s", shortScope(l.Scope, root))
	for _, om := range l.Omissions {
		fmt.Fprintf(&b, "\n    unresolved: %s", shortPathText(om, root))
	}
	return b.String()
}

// lookupReasonGroups names each distinct failure reason once, with the queries
// of the lookups it affected. Reasons are compared after path normalisation, so
// one missing backend is one line instead of one line per query.
func lookupReasonGroups(lks []evidenceLookup, root string) string {
	type failureGroup struct {
		status, reason string
		queries        []string
	}
	var groups []*failureGroup
	byReason := map[string]*failureGroup{}
	for _, l := range lks {
		if len(l.Errors) == 0 {
			continue
		}
		reason := shortFailure(strings.Join(l.Errors, "; "), root)
		key := string(l.ExecutionStatus) + "\x00" + reason
		g := byReason[key]
		if g == nil {
			g = &failureGroup{status: string(l.ExecutionStatus), reason: reason}
			byReason[key] = g
			groups = append(groups, g)
		}
		g.queries = append(g.queries, l.Query)
	}
	lines := make([]string, 0, len(groups))
	for _, g := range groups {
		noun := "lookups"
		if len(g.queries) == 1 {
			noun = "lookup"
		}
		line := fmt.Sprintf("%d %s %s: %s", len(g.queries), noun, g.status, g.reason)
		if len(g.queries) > 0 {
			quoted := make([]string, 0, len(g.queries))
			for _, q := range g.queries {
				quoted = append(quoted, fmt.Sprintf("%q", q))
			}
			line += "; affected queries: " + strings.Join(quoted, ", ")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// shortScope renders a scope selector in short form: the path of a JSON
// selector, or the path itself, without the machine-specific prefix.
func shortScope(scope, root string) string {
	s := strings.TrimSpace(scope)
	if strings.HasPrefix(s, "{") {
		var v struct {
			Path        *string         `json:"path"`
			ProjectRoot *string         `json:"project_root"`
			Lookup      json.RawMessage `json:"lookup"`
		}
		if json.Unmarshal([]byte(s), &v) == nil && (v.Path != nil || v.ProjectRoot != nil || v.Lookup != nil) {
			s = "."
			if v.Path != nil && *v.Path != "" {
				s = *v.Path
			} else if v.ProjectRoot != nil && *v.ProjectRoot != "" {
				s = *v.ProjectRoot
			}
		}
	}
	if s == "" {
		s = "."
	}
	return shortPathText(s, root)
}

// shortFailure renders a backend failure without the paths that only say where
// this checkout or its tools happen to live.
func shortFailure(msg, root string) string {
	s := failureMissing.ReplaceAllString(strings.TrimSpace(msg), "$1 not found")
	s = shortPathText(s, root)
	for _, rw := range failureKnown {
		s = rw.re.ReplaceAllString(s, rw.to)
	}
	return short(strings.Join(strings.Fields(s), " "), 200)
}

// shortPathText replaces the checkout root, a home prefix and a temp prefix, so
// the same path renders the same way on any machine.
func shortPathText(s, root string) string {
	if root != "" {
		s = strings.ReplaceAll(s, root, "<repo>")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home, "~")
	}
	return failureTemp.ReplaceAllString(s, "$1<tmp>")
}

func dossierSource(e *ctxEnv, path string, start, end int) evidenceRecord {
	ls := e.Lines(path)
	from := min(len(ls), max(0, start-1))
	to := min(len(ls), max(from, end))
	body := strings.Join(ls[from:to], "\n")
	id := dossierSourceID(path, start, end, body)
	valid := start >= 1 && end >= start && end <= len(ls)
	complete := evidenceComplete
	if !valid {
		complete = evidenceUnknown
	}
	kind := evidenceSourceCode
	if isTestPath(path) {
		kind = evidenceSourceTest
	} else if isDocPath(path) {
		kind = evidenceSourceDocument
	}
	return evidenceRecord{SchemaVersion: evidenceSchemaVersion, ID: id, Completeness: complete, SourceKind: kind, Language: langIDOf(path), Path: path, Range: &evidenceRange{StartLine: start, EndLine: end}, ContentHash: subjectDigest([]byte(body)), LookupID: "lookup-" + id, Selected: &valid, LocatorValid: &valid}
}

func dossierSourceID(path string, start, end int, body string) string {
	return "evidence-" + subjectJSONDigest([]any{path, start, end, subjectDigest([]byte(body))})[:20]
}
func dossierRead(path string, start, end int) string {
	return fmt.Sprintf("Read path=%q offset=%d limit=%d", path, max(1, start), max(1, end-start+1))
}

type dossierOmission struct {
	EvidenceID string `json:"evidence_id"`
	Reason     string `json:"reason"`
	Expansion  string `json:"expansion"`
}
type dossierRequirement struct {
	ItemID      string   `json:"item_id"`
	Revision    string   `json:"revision"`
	EvidenceIDs []string `json:"evidence_ids"`
	LookupIDs   []string `json:"lookup_ids"`
}

// Required is governed by the existing --verdicts switch, not by retrieval
// availability. These are reducer inputs/relationships, never decisions.
type dossierRequiredCheck struct {
	ID           string   `json:"id"`
	ItemID       string   `json:"item_id"`
	ItemRevision string   `json:"item_revision"`
	Kind         string   `json:"kind"`
	Required     bool     `json:"required"`
	LookupIDs    []string `json:"lookup_ids"`
}
type dossierManifest struct {
	RequiredChecks []dossierRequiredCheck `json:"required_checks"`
	evidencePacket
	Budget       int                  `json:"budget_bytes"`
	Requirements []dossierRequirement `json:"requirements"`
	Omissions    []dossierOmission    `json:"omissions"`
	// LookupTiming has one entry for each lookup that ran a search: when it
	// started and how long it took. A symbol lookup carries the time of the
	// batch it was answered in.
	LookupTiming []lookupTiming `json:"lookup_timing,omitempty"`
}

func (s *dossierSet) manifest(subject *gateSubject) *dossierManifest {
	if subject == nil {
		subject = s.subject
	}
	m := &dossierManifest{evidencePacket: evidencePacket{evidenceEnvelope: evidenceEnvelope{SchemaVersion: evidenceSchemaVersion, Subject: subject, Completeness: evidencePartial}}, Budget: dossierTotal}
	ids := make([]string, 0, len(s.docs))
	for id := range s.docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen, lookups := map[string]int{}, map[string]bool{}
	for _, id := range ids {
		d := s.docs[id]
		_ = s.text(id)
		for _, l := range d.Lookups {
			if !lookups[l.ID] {
				lookups[l.ID] = true
				m.Lookups = append(m.Lookups, l)
			}
		}
		for _, ic := range s.cands[id] {
			r := dossierRequirement{ItemID: ic.Item.ID, Revision: ic.Item.Revision}
			for _, l := range ic.Lookups {
				r.LookupIDs = append(r.LookupIDs, l.ID)
				if !lookups[l.ID] {
					lookups[l.ID] = true
					m.Lookups = append(m.Lookups, l)
				}
			}
			for _, c := range ic.Cands {
				r.EvidenceIDs = append(r.EvidenceIDs, c.EvidenceID)
				if n := seen[c.EvidenceID]; n > 0 {
					if d.Shown[c.EvidenceID] {
						yes := true
						m.Evidence[n-1].Rendered = &yes
					}
					continue
				}
				er := dossierSource(s.e, c.Path, c.Start, c.End)
				er.ID = c.EvidenceID
				er.LookupID = "lookup-" + c.EvidenceID
				// Source selection is not a claim that a reviewer read the body.
				var readErr error
				if !*er.LocatorValid {
					readErr = fmt.Errorf("source path/range could not be read")
				}
				l := dossierLookup(dossierRead(c.Path, c.Start, c.End), c.Path, "source_read", 1, 0, readErr, "")
				l.ID = er.LookupID
				if !lookups[l.ID] {
					lookups[l.ID] = true
					m.Lookups = append(m.Lookups, l)
				}
				shown := d.Shown[c.EvidenceID]
				er.Rendered = &shown
				if c.Total > len(c.Lines) || c.Total > reuseShowLines {
					er.Completeness = evidencePartial
				}
				if subject != nil {
					er.SubjectID = subject.ID
				}
				seen[er.ID] = len(m.Evidence) + 1
				m.Evidence = append(m.Evidence, er)
			}
			m.Requirements = append(m.Requirements, r)
			m.RequiredChecks = append(m.RequiredChecks, dossierRequiredCheck{ID: "reuse:" + ic.Item.ID, ItemID: ic.Item.ID, ItemRevision: ic.Item.Revision, Kind: "reuse", Required: s.verdicts, LookupIDs: append([]string{}, r.LookupIDs...)})
		}
		for _, er := range d.Evidence {
			if n := seen[er.ID]; n > 0 {
				if d.Shown[er.ID] {
					yes := true
					m.Evidence[n-1].Rendered = &yes
				}
				continue
			}
			seen[er.ID] = len(m.Evidence) + 1
			if subject != nil {
				er.SubjectID = subject.ID
			}
			shown := d.Shown[er.ID]
			er.Rendered = &shown
			l := dossierLookup(dossierRead(er.Path, er.Range.StartLine, er.Range.EndLine), er.Path, "source_read", 1, 0, nil, "")
			l.ID = er.LookupID
			if !lookups[l.ID] {
				lookups[l.ID] = true
				m.Lookups = append(m.Lookups, l)
			}
			m.Evidence = append(m.Evidence, er)
		}
		m.Omissions = append(m.Omissions, d.Omissions...)
	}
	for _, l := range m.Lookups {
		if l.Timing != nil {
			m.LookupTiming = append(m.LookupTiming, *l.Timing)
		}
	}
	return m
}
