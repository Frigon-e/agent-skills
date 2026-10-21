package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// A brief is what code can establish about a piece of work before anyone
// reads it: which files matter and why (ranked by how many independent lookups
// agree), the exact interfaces it touches, existing code that looks like what
// it builds, and names that exist nowhere. It is built from the task text, an
// optional owns list and the lookups of the layer, with no model. The text can
// be a spec package, a task.md or a question.

const (
	dossierOwnsCap     = 40 // files taken from the owns
	dossierFiles       = 12 // ranked files kept
	dossierRgHits      = 8  // rg hits kept per name
	dossierNames       = 20 // names looked up with rg
	dossierSyms        = 8  // symbols per language looked up
	dossierBuilds      = 6  // "builds" lines used as concept queries
	dossierConcept     = 300
	dossierConceptHits = 4  // chunks kept per concept query
	dossierAccept      = 6  // acceptance bullets used as concept queries
	dossierLike        = 8  // look-alikes kept
	dossierLikeTree    = 4  // look-alikes kept per code tree
	dossierDocs        = 5  // docs listed
	dossierRare        = 20 // a name with more hits than this is noise
	dossierTextLike    = 5  // a name with more hits than this is not shown as a look-alike

	// One 24 KiB budget replaces the former independent brief/checklist/planner
	// caps (about 60k bytes before overshoot). It leaves room for execution metadata
	// without crowding out matched source regions in a normal package.
	dossierTotal = 24 * 1024
)

type briefInput struct {
	ID     string
	Title  string
	Text   string   // the whole task text: pointers, symbols and names are read from it
	Body   string   // the part that says what is built; Text when empty
	Builds []string // the Builds bullets of a spec package: concept queries as written
	Owns   []string // files or directories the work owns
}

var (
	dossierPtrRe = regexp.MustCompile(`([A-Za-z0-9_@.][A-Za-z0-9_@./-]*\.[A-Za-z0-9]{1,5})(?::(\d+)|#([A-Za-z_][A-Za-z0-9_.]*))`)
	endpointRe   = regexp.MustCompile(`\b(?:GET|POST|PUT|PATCH|DELETE)\s+/[A-Za-z0-9_/{}:.-]+`)
	snakeRe      = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
)

// inOwns reports whether path is one of the owns entries or below one.
func inOwns(path string, owns []string) bool {
	for _, o := range owns {
		o = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(o), "./"), "/")
		if o != "" && (path == o || strings.HasPrefix(path, o+"/")) {
			return true
		}
	}
	return false
}

// ownedFiles lists the existing files of the owns entries: a file as itself,
// a directory by its tracked files, up to dossierOwnsCap.
func (e *ctxEnv) ownedFiles(owns []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(f string) {
		if len(out) < dossierOwnsCap && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, o := range owns {
		o = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(o), "./"), "/")
		if o == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(e.Code, o)); err == nil && !st.IsDir() {
			add(o)
			continue
		}
		for _, t := range e.files() {
			if strings.HasPrefix(t, o+"/") {
				add(t)
			}
		}
	}
	return out
}

type pointer struct {
	path, sym string
	exists    bool
}

// pointers are the `path:line` and `path#Symbol` references in text.
func (e *ctxEnv) pointers(text string) []pointer {
	var out []pointer
	seen := map[string]bool{}
	for _, m := range dossierPtrRe.FindAllStringSubmatch(text, -1) {
		path := strings.TrimPrefix(m[1], "./")
		if e.Skip[path] || !strings.Contains(path, ".") {
			continue
		}
		key := path + "#" + m[3]
		if seen[key] {
			continue
		}
		seen[key] = true
		st, err := os.Stat(filepath.Join(e.Code, path))
		out = append(out, pointer{path: path, sym: m[3], exists: err == nil && !st.IsDir()})
	}
	return out
}

// briefPlan is what is looked up for one brief and what came back.
type briefPlan struct {
	in       briefInput
	owned    []string
	ptrs     []pointer
	goSyms   []string
	tsSyms   []string
	tsRoot   string
	names    []string // looked up with rg
	files    []string // file names looked up among the tracked files
	concepts []string
	trees    []string // code trees concept searches are narrowed to ("" when the repo has none)

	facts   map[string]*symFact // by symbol, the language that found it
	rg      map[string][]rgHit
	concept map[string][]codeHit // by query + "\x00" + tree
	accept  []string             // acceptance bullets
	builds  []string             // the lines that say what is built
	qnames  []string             // names the concept queries use
	purpose string               // the short purpose phrase of the queries
	lookups []evidenceLookup
}

func addUniq(list []string, s string, max int) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	if len(list) >= max {
		return list
	}
	return append(list, s)
}

func (e *ctxEnv) prepareBrief(in briefInput) *briefPlan {
	pl := &briefPlan{in: in, facts: map[string]*symFact{}, rg: map[string][]rgHit{}, concept: map[string][]codeHit{}}
	block := in.Text
	pl.owned = e.ownedFiles(in.Owns)
	pl.ptrs = e.pointers(block)
	lines := strings.Split(block, "\n")
	for _, l := range lines {
		for _, s := range goSymbols(l) {
			pl.goSyms = addUniq(pl.goSyms, s, dossierSyms)
		}
	}
	// Names that might be symbols in any language; the declaration tier decides.
	for _, l := range lines {
		for _, s := range genericSymbols(l) {
			pl.goSyms = addUniq(pl.goSyms, s, dossierSyms+8)
		}
	}
	pl.tsRoot = tsProjectRoot(e.Code, block, in.Owns)
	if pl.tsRoot != "" {
		for _, l := range lines {
			for _, s := range tsSymbols(l, nil) {
				pl.tsSyms = addUniq(pl.tsSyms, s, dossierSyms)
			}
		}
	}
	for _, ptr := range pl.ptrs {
		if ptr.sym == "" || !ptr.exists {
			continue
		}
		if langIDOf(ptr.path) == "typescript" && pl.tsRoot != "" {
			if tsPlain.MatchString(ptr.sym) {
				pl.tsSyms = addUniq(pl.tsSyms, ptr.sym, dossierSyms+4)
			}
		} else if langIDOf(ptr.path) != "" {
			pl.goSyms = addUniq(pl.goSyms, ptr.sym, dossierSyms+4)
		}
	}
	// Names to find by text: backticked names, endpoints, snake_case names, then the symbols.
	for _, m := range idBacktick.FindAllStringSubmatch(block, -1) {
		span := strings.TrimSpace(m[1])
		switch {
		case dossierPtrRe.MatchString(span) && !strings.Contains(span, " "):
			continue
		case strings.ContainsAny(span, "*?") || (strings.HasSuffix(span, "_test") && !strings.ContainsAny(span, "./")):
			continue // a glob or a bare test-file name is not a thing to find
		case fileExtRe.MatchString(span) && !strings.Contains(span, " "):
			pl.files = addUniq(pl.files, filepath.Base(span), dossierNames)
		case len(span) >= 3 && !strings.ContainsAny(span, " \t"):
			pl.names = addUniq(pl.names, span, dossierNames)
		}
	}
	for _, s := range endpointRe.FindAllString(block, -1) {
		pl.names = addUniq(pl.names, strings.TrimRight(s, ".,;:)"), dossierNames)
	}
	for _, s := range snakeRe.FindAllString(block, -1) {
		if len(s) >= 5 {
			pl.names = addUniq(pl.names, s, dossierNames)
		}
	}
	for _, s := range append(append([]string{}, pl.goSyms...), pl.tsSyms...) {
		pl.names = addUniq(pl.names, s, dossierNames)
	}
	pl.builds = in.Builds[:min(len(in.Builds), dossierBuilds)]
	pl.accept = acceptanceBullets(block)
	pl.trees = codeTreesFor(e.Code, in.Owns)
	return pl
}

// Briefs builds one brief per input. The symbols of all inputs go in one batch,
// so one language server serves them all.
func (e *ctxEnv) Briefs(ins []briefInput) []*ctxBrief {
	var plans []*briefPlan
	for _, in := range ins {
		plans = append(plans, e.prepareBrief(in))
	}
	goSeen := map[string]bool{}
	var goNames []string
	tsSeen := map[tsJob]bool{}
	var tsJobs []tsJob
	for _, pl := range plans {
		for _, s := range pl.goSyms {
			if !goSeen[s] {
				goSeen[s] = true
				goNames = append(goNames, s)
			}
		}
		for _, s := range pl.tsSyms {
			if j := (tsJob{pl.tsRoot, s}); !tsSeen[j] {
				tsSeen[j] = true
				tsJobs = append(tsJobs, j)
			}
		}
	}
	goFacts, tsFacts := e.Symbols(goNames, tsJobs)
	for _, pl := range plans {
		for _, s := range pl.goSyms {
			pl.lookups = append(pl.lookups, dossierSymbolLookup(s, ".", goFacts[s]))
			if f := goFacts[s]; f != nil && len(f.Defs) > 0 {
				pl.facts[s] = f
			}
		}
		for _, s := range pl.tsSyms {
			pl.lookups = append(pl.lookups, dossierSymbolLookup(s, pl.tsRoot, tsFacts[tsJob{pl.tsRoot, s}]))
			if f := tsFacts[tsJob{pl.tsRoot, s}]; f != nil && len(f.Defs) > 0 && pl.facts[s] == nil {
				pl.facts[s] = f
			}
		}
	}
	for _, pl := range plans {
		pl.makeQueries()
	}
	// Text and concept lookups of every input, a few at a time.
	type job struct {
		pl   *briefPlan
		name string
		text bool
	}
	var jobs []job
	jbReady := e.jbMissing() == ""
	for _, pl := range plans {
		for _, n := range pl.names {
			jobs = append(jobs, job{pl, n, true})
		}
		if jbReady {
			for _, q := range pl.concepts {
				for _, tr := range pl.trees {
					jobs = append(jobs, job{pl, q + "\x00" + tr, false})
				}
			}
		}
	}
	if !jbReady {
		e.note(e.jbMissing())
		for _, pl := range plans {
			for _, q := range pl.concepts {
				for _, tree := range pl.trees {
					pl.lookups = append(pl.lookups, dossierLookup(q, tree, "concept", 0, dossierConceptHits, nil, e.jbMissing()))
				}
			}
		}
	}
	var mu sync.Mutex
	e.parallel(len(jobs), func(i int) {
		j := jobs[i]
		if j.text {
			res, err := e.Text(rgQuery{Pattern: j.name, Fixed: true, PerFile: 3, AllFiles: true, CaseSensitive: true})
			hits := res.Hits
			mu.Lock()
			j.pl.lookups = append(j.pl.lookups, dossierTextLookup(j.name, ".", res, err))
			mu.Unlock()
			if err != nil {
				e.note(err.Error())
				return
			}
			mu.Lock()
			j.pl.rg[j.name] = hits
			mu.Unlock()
			return
		}
		q, tree, _ := strings.Cut(j.name, "\x00")
		hits, err := e.Concept(q, tree, dossierConceptHits)
		mu.Lock()
		j.pl.lookups = append(j.pl.lookups, dossierLookup(q, tree, "concept", len(hits), dossierConceptHits, err, ""))
		mu.Unlock()
		if err != nil {
			e.note(err.Error())
			return
		}
		mu.Lock()
		j.pl.concept[j.name] = hits
		mu.Unlock()
	})
	out := make([]*ctxBrief, len(plans))
	for i, pl := range plans {
		out[i] = e.assemble(pl)
	}
	return out
}

type briefFile struct {
	Path  string
	Score int
	Why   []string
}

type briefLike struct {
	At, In, Via string
	Snippet     []string
}

type briefNativeLike struct {
	text   string
	source evidenceRecord
}

type ctxBrief struct {
	// Render-only views keep legacy --json Sheet/Like/LikeRows unchanged.
	root                              string
	interfaceSummary                  []string
	nativeLike                        map[string]briefNativeLike
	plannedShown                      map[string][]string
	ID                                string
	Files, Docs, Sheet, Like, Missing []string
	Planned                           []string // lookups run for the brief afterwards, if any
	PlannedIDs                        []string
	Reuse                             string // the reuse checklist of a gate dossier, "" for none
	Foreign                           string // files the brief names that other packages own (gate dossier), "" for none
	Lookups                           []evidenceLookup
	ReuseItems                        []itemCands
	Omissions                         []dossierOmission
	Evidence                          []evidenceRecord
	Shown                             map[string]bool

	// The same content as data, for the TOON view of `orch ctx brief`.
	FileRows []briefFile
	Facts    []symFact
	LikeRows []briefLike
	Names    []string
}

// capEntries keeps whole entries while they fit in max characters; the rest
// is counted in a last line.
func capEntries(entries []string, max int) string {
	if len(entries) == 0 {
		return "(none)\n"
	}
	var b strings.Builder
	for i, en := range entries {
		if b.Len()+len(en)+1 > max {
			fmt.Fprintf(&b, "- ... %d more omitted\n", len(entries)-i)
			break
		}
		b.WriteString(en + "\n")
	}
	return b.String()
}

// docLine is the first line of the comment block right above line ln (1-based).
func docLine(lines []string, ln int) string {
	top := -1
	for i := ln - 2; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*") {
			top = i
			continue
		}
		break
	}
	if top < 0 {
		return ""
	}
	for i := top; i < ln-1; i++ {
		t := strings.TrimSpace(lines[i])
		t = strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(t, "/**"), "/*"), "/*"))
		t = strings.TrimSpace(strings.TrimSuffix(t, "*/"))
		if t != "" && t != "*" {
			return short(t, 140)
		}
	}
	return ""
}

func splitWhere(where string) (string, int) {
	i := strings.LastIndex(where, ":")
	if i < 0 {
		return where, 0
	}
	var n int
	fmt.Sscanf(where[i+1:], "%d", &n)
	return where[:i], n
}

func quoteList(ss []string) string {
	var out []string
	for _, s := range ss {
		out = append(out, "`"+s+"`")
	}
	return strings.Join(out, ", ")
}

// Render has one byte budget for ALL sections, including headings, source,
// docs, checklist and expansion instructions. No section receives a second cap.
// The complete omission list is also persisted in dossier-manifest.json.
func (d *ctxBrief) Render(heading, noun string, total int) string {
	type entry struct {
		text, id, expansion string
		body                bool
	}
	var entries []entry
	add := func(text string) { entries = append(entries, entry{text: text + "\n"}) }
	add(heading)
	add("### Observations (bounded tool results, not semantic proof)")
	// Failures are grouped once per reason, and the exclusions sentence is
	// printed once, so a missing backend costs one line per distinct failure
	// instead of one long line per query.
	exclusionsShown, absenceShown := false, false
	lookupNotes := func(lks []evidenceLookup) {
		if len(lks) == 0 {
			return
		}
		if !exclusionsShown {
			if line := lookupExclusionsLine(lks); line != "" {
				add(line)
				exclusionsShown = true
			}
		}
		if !absenceShown && lookupUncertainAny(lks) {
			add(lookupAbsenceLine)
			absenceShown = true
		}
	}
	lookupNotes(d.Lookups)
	if reasons := lookupReasonGroups(d.Lookups, d.root); reasons != "" {
		add(reasons)
	}
	for _, lk := range d.Lookups {
		add(lookupLine(lk, d.root))
	}
	add("### Candidates (unverified)")
	add("### 3a. Reuse checklist (each requirement remains a separate decision)")
	var reuseLookups []evidenceLookup
	for _, ic := range d.ReuseItems {
		reuseLookups = append(reuseLookups, ic.Lookups...)
	}
	lookupNotes(reuseLookups)
	seen := map[string]bool{}
	for ri, ic := range d.ReuseItems {
		if ri == itemPageMain {
			add(reuseMoreNotice)
		}
		cs := ic.Cands
		ic.Cands = nil
		meta := itemText(ic, false, d.root)
		if len(cs) > 0 {
			meta = strings.ReplaceAll(meta, "  (unresolved: requested search did not complete; no negative conclusion)\n", "")
			meta = strings.ReplaceAll(meta, "  (completed search: no eligible candidates in the searched scope; not proof of absence)\n", "")
		}
		add(meta)
		for i, c := range cs {
			if c.EvidenceID == "" {
				c.EvidenceID = dossierSourceID(c.Path, c.Start, c.End, strings.Join(c.Lines, "\n"))
			}
			if c.Expansion == "" {
				c.Expansion = dossierRead(c.Path, c.Start, max(c.Start, c.End))
			}
			if seen[c.EvidenceID] {
				add(fmt.Sprintf("  candidate %s `%s` referenced above or in omissions, also supports requirement %s (decide independently); expand: %s", c.EvidenceID, c.at(), ic.Item.ID, c.Expansion))
				continue
			}
			seen[c.EvidenceID] = true
			header := candidateText(i+1, c, false)
			if at := strings.Index(header, "     omitted "); at >= 0 {
				header = header[:at]
			}
			add(header)
			body := strings.TrimPrefix(candidateText(i+1, c, true), header)
			entries = append(entries, entry{body, c.EvidenceID, c.Expansion, true})
		}
	}
	if len(d.ReuseItems) == 0 && d.Reuse != "" {
		add(d.Reuse)
	}
	add("### 2. Interface sheet")
	sheet := d.Sheet
	if d.interfaceSummary != nil {
		sheet = d.interfaceSummary
	}
	for i, text := range sheet {
		path, start := "", 1
		if i < len(d.Facts) && len(d.Facts[i].Defs) > 0 {
			path, start = splitWhere(d.Facts[i].Defs[0].Where)
		}
		id := fmt.Sprintf("signature-summary:%d", i)
		entries = append(entries, entry{text + "\n", id, dossierRead(path, start, start+ctxSnippet-1), false})
	}
	add("### 1. Ranked files")
	for _, text := range d.Files {
		add(text)
	}
	add("### 1b. Docs that mention it")
	for _, text := range d.Docs {
		add(text)
	}
	add("### 3. Existing things that look like what this " + noun + " builds (outside its owns)")
	for _, text := range d.Like {
		native, ok := d.nativeLike[text]
		if !ok {
			add(text)
			continue
		} // notes/cut notices have no source body
		er := native.source
		expand := dossierRead(er.Path, er.Range.StartLine, er.Range.EndLine)
		if seen[er.ID] {
			add("- source " + er.ID + " referenced above or in omissions; expand: " + expand)
			continue
		}
		seen[er.ID] = true
		entries = append(entries, entry{native.text + "\n", er.ID, expand, true})
	}
	add("### 3b. Not in our workspace")
	if d.Foreign != "" {
		add(d.Foreign)
	}
	add("### Unresolved work")
	add("### 4. Names not found in completed bounded lookups (not proof of absence)")
	for _, text := range d.Missing {
		add(text)
	}
	if d.Planned != nil {
		add("### 5. Lookups the planner asked for")
		byID := map[string]evidenceLookup{}
		for _, l := range d.Lookups {
			byID[l.ID] = l
		}
		planned := make([]evidenceLookup, 0, len(d.PlannedIDs))
		for _, id := range d.PlannedIDs {
			if l, ok := byID[id]; ok {
				planned = append(planned, l)
			}
		}
		lookupNotes(planned)
		if reasons := lookupReasonGroups(planned, d.root); reasons != "" {
			add(reasons)
		}
		for i, text := range d.Planned {
			id := "entry-" + subjectDigest([]byte(text))[:20]
			if i < len(d.PlannedIDs) {
				id = d.PlannedIDs[i]
			}
			expand := "Read path=\"dossier-manifest.json\""
			for _, l := range d.Lookups {
				if l.ID == id {
					expand = "ctx lookup with original selectors: " + l.Scope
					break
				}
			}
			entries = append(entries, entry{text + "\n", id, expand, true})
		}
	}
	add("Coverage gaps: candidate relevance is unverified. Expand incoming/outgoing references, registrations/configuration and tests where material; record any uncompleted checks.")
	d.Omissions = nil
	d.Shown = map[string]bool{}
	// Reserve a compact omission index before selecting source. Metadata that
	// itself exceeds the budget remains in the persisted manifest, never cut mid-body.
	reserve := min(total/3, 1024)
	limit := max(0, total-reserve)
	// Keep requirement relationships, signatures and execution states ahead of
	// bodies. Select first, then emit in the original stable section order.
	kept := make([]bool, len(entries))
	used := 0
	for pass := 0; pass < 2; pass++ {
		for i, en := range entries {
			if en.body != (pass == 1) {
				continue
			}
			if used+len(en.text) <= limit {
				kept[i] = true
				used += len(en.text)
			}
		}
	}
	var b strings.Builder
	for i, en := range entries {
		if kept[i] {
			b.WriteString(en.text)
			if strings.HasPrefix(en.id, "signature-summary:") {
				for _, er := range d.Evidence {
					if strings.Contains(en.text, er.ID) {
						d.Shown[er.ID] = true
					}
				}
			}
			if en.body {
				d.Shown[en.id] = true
				for _, id := range d.plannedShown[en.id] {
					d.Shown[id] = true
				}
			}
			if en.body && strings.Contains(en.text, "omitted ") {
				d.Omissions = append(d.Omissions, dossierOmission{en.id, "partial body preview", en.expansion})
			}
			continue
		}
		found := false
		for _, er := range d.Evidence {
			for _, id := range d.plannedShown[en.id] {
				if er.ID == id && er.Range != nil {
					found = true
					d.Omissions = append(d.Omissions, dossierOmission{er.ID, "overall dossier byte budget", dossierRead(er.Path, er.Range.StartLine, er.Range.EndLine)})
				}
			}
		}
		if found {
			continue
		}
		id := en.id
		if id == "" {
			id = "entry-" + subjectDigest([]byte(en.text))[:20]
		}
		expand := en.expansion
		if expand == "" {
			expand = "Read path=\"dossier-manifest.json\" (same package and lookup scope)"
		}
		d.Omissions = append(d.Omissions, dossierOmission{id, "overall dossier byte budget", expand})
	}
	if len(d.Omissions) > 0 {
		footer := fmt.Sprintf("### Recorded omissions (%d); overall budget=%d bytes\n", len(d.Omissions), total)
		for _, om := range d.Omissions {
			line := fmt.Sprintf("- %s: %s; expand: %s\n", om.EvidenceID, om.Reason, om.Expansion)
			if len(footer)+len(line)+100 <= reserve {
				footer += line
			}
		}
		footer += "Complete omission list and scope selectors: Read path=\"dossier-manifest.json\".\n"
		if b.Len()+len(footer) <= total {
			b.WriteString(footer)
		}
	}
	return b.String()
}
