package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
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

// briefRun is Briefs in halves: briefsPrepare plans every lookup, symbols runs
// the symbol lookups, and finish builds the briefs from the answers. The symbol
// lookups and the jobs may run at the same time between prepare and finish.
type briefRun struct {
	e       *ctxEnv
	plans   []*briefPlan
	goNames []string
	tsJobs  []tsJob
	jobs    []dossierJob
	owner   []*briefPlan // the plan of jobs[i]
	jbReady bool

	goFacts    map[string]*symFact
	tsFacts    map[tsJob]*symFact
	symStarted time.Time
	symElapsed time.Duration
}

func (e *ctxEnv) briefsPrepare(ins []briefInput) *briefRun {
	r := &briefRun{e: e, jbReady: e.jbMissing() == ""}
	for _, in := range ins {
		r.plans = append(r.plans, e.prepareBrief(in))
	}
	goSeen := map[string]bool{}
	tsSeen := map[tsJob]bool{}
	for _, pl := range r.plans {
		for _, s := range pl.goSyms {
			if !goSeen[s] {
				goSeen[s] = true
				r.goNames = append(r.goNames, s)
			}
		}
		for _, s := range pl.tsSyms {
			if j := (tsJob{pl.tsRoot, s}); !tsSeen[j] {
				tsSeen[j] = true
				r.tsJobs = append(r.tsJobs, j)
			}
		}
	}
	// The concept queries do not depend on the symbols (only the names they
	// report do), so the jobs are known now; finish sets the queries again.
	for _, pl := range r.plans {
		pl.makeQueries()
		for _, n := range pl.names {
			r.jobs = append(r.jobs, dossierJob{text: true, name: n})
			r.owner = append(r.owner, pl)
		}
		if r.jbReady {
			for _, q := range pl.concepts {
				for _, tr := range pl.trees {
					r.jobs = append(r.jobs, dossierJob{name: q, tree: tr})
					r.owner = append(r.owner, pl)
				}
			}
		}
	}
	return r
}

// symbols looks up the symbols of all inputs in one batch, so one language
// server serves them all.
func (r *briefRun) symbols() {
	r.symStarted = time.Now()
	r.goFacts, r.tsFacts = r.e.Symbols(r.goNames, r.tsJobs)
	r.symElapsed = time.Since(r.symStarted)
}

// finish builds one brief per input from the symbol facts and the answers of the
// jobs, which are in the order of r.jobs.
func (r *briefRun) finish(outs []jobOut) []*ctxBrief {
	e := r.e
	for _, pl := range r.plans {
		for _, s := range pl.goSyms {
			pl.lookups = append(pl.lookups, dossierSymbolLookup(s, ".", r.goFacts[s]).timed(r.symStarted, r.symElapsed))
			if f := r.goFacts[s]; f != nil && len(f.Defs) > 0 {
				pl.facts[s] = f
			}
		}
		for _, s := range pl.tsSyms {
			pl.lookups = append(pl.lookups, dossierSymbolLookup(s, pl.tsRoot, r.tsFacts[tsJob{pl.tsRoot, s}]).timed(r.symStarted, r.symElapsed))
			if f := r.tsFacts[tsJob{pl.tsRoot, s}]; f != nil && len(f.Defs) > 0 && pl.facts[s] == nil {
				pl.facts[s] = f
			}
		}
	}
	for _, pl := range r.plans {
		pl.makeQueries()
	}
	if !r.jbReady {
		e.note(e.jbMissing())
		for _, pl := range r.plans {
			for _, q := range pl.concepts {
				for _, tree := range pl.trees {
					pl.lookups = append(pl.lookups, dossierLookup(q, tree, "concept", 0, dossierConceptHits, nil, e.jbMissing()))
				}
			}
		}
	}
	for i, j := range r.jobs {
		pl, o := r.owner[i], outs[i]
		if j.text {
			pl.lookups = append(pl.lookups, dossierTextLookup(j.name, ".", o.text, o.err).timed(o.started, o.elapsed))
			if o.err != nil {
				e.note(o.err.Error())
				continue
			}
			pl.rg[j.name] = o.text.Hits
			continue
		}
		pl.lookups = append(pl.lookups, dossierLookup(j.name, j.tree, "concept", len(o.hits), dossierConceptHits, o.err, "").timed(o.started, o.elapsed))
		if o.err != nil {
			e.note(o.err.Error())
			continue
		}
		pl.concept[j.name+"\x00"+j.tree] = o.hits
	}
	out := make([]*ctxBrief, len(r.plans))
	for i, pl := range r.plans {
		out[i] = e.assemble(pl)
	}
	return out
}

// Briefs builds one brief per input. The symbols of all inputs go in one batch,
// so one language server serves them all, and run beside the searches.
func (e *ctxEnv) Briefs(ins []briefInput) []*ctxBrief {
	r := e.briefsPrepare(ins)
	var sym sync.WaitGroup
	sym.Add(1)
	go func() {
		defer sym.Done()
		r.symbols()
	}()
	outs := e.runJobs(r.jobs)
	sym.Wait()
	return r.finish(outs)
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

// itemRef is the short name of a checklist item: "#2" for "crm1-ui-shell#2".
func itemRef(id string) string {
	if i := strings.LastIndex(id, "#"); i >= 0 {
		return id[i:]
	}
	return id
}

// candID is the evidence id of a candidate, computed when it carries none.
func candID(c reuseCand) string {
	if c.EvidenceID != "" {
		return c.EvidenceID
	}
	return dossierSourceID(c.Path, c.Start, c.End, strings.Join(c.Lines, "\n"))
}

// itemHead is a checklist item as the prompt shows it: its text, the author's
// pointers and, when nothing was found, one short status line. The lookups that
// ran for it are not listed; they are in the manifest.
func itemHead(ic itemCands) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- `%s` %s\n", ic.Item.ID, ic.Item.Text)
	for _, p := range ic.Item.Reuse {
		fmt.Fprintf(&b, "  author's pointer: `%s`\n", p)
	}
	switch {
	case len(ic.Queries) == 0:
		b.WriteString("  search: not_requested (no explicit queries; unresolved work)\n")
	case len(ic.Cands) > 0:
	case ic.QueriesRun == len(ic.Queries):
		b.WriteString("  no eligible candidates in the searched scope\n")
	default:
		b.WriteString("  search did not complete; no negative conclusion\n")
	}
	return b.String()
}

const (
	dossierIntro       = "Absence of a hit is not proof of absence. Complete lookup records (ids, status, counts, scopes) and the omission list are in dossier-manifest.json."
	searchNotesMaxWays = 4 // failure reasons listed in the search notes
	searchNotesNames   = 12
)

var (
	noteJSONMessage = regexp.MustCompile(`\{[^{}]*"message":\s*"((?:[^"\\]|\\.)*)"[^{}]*\}?`)
	noteLeadIn      = regexp.MustCompile(`^(?:working-tree semantic search failed: )?(?:jbcontext failed: (?:exit status \d+: )?)?`)
	noteEmail       = regexp.MustCompile(`\s*\([^()]*@[^()]*\)`)
	noteLicense     = regexp.MustCompile(`(license) [A-Z0-9]{6,}`)
)

// noteReason is a failure reason for the search notes: the message inside a
// JSON error body, without license ids, e-mail addresses or machine paths, cut
// at its first sentence. Two failures with the same cause give the same text.
func noteReason(msg, root string) string {
	s := shortPathText(failureMissing.ReplaceAllString(strings.TrimSpace(msg), "$1 not found"), root)
	s = noteJSONMessage.ReplaceAllString(s, "$1")
	if i := strings.Index(s, `{"`); i >= 0 {
		s = strings.TrimRight(s[:i], ": ")
	}
	for _, rw := range failureKnown {
		s = rw.re.ReplaceAllString(s, rw.to)
	}
	s = noteLeadIn.ReplaceAllString(s, "")
	s = noteEmail.ReplaceAllString(s, "")
	s = noteLicense.ReplaceAllString(s, "$1")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i]
	}
	return short(strings.Join(strings.Fields(s), " "), 110)
}

// searchNotes is the closing block of the dossier: how the lookups ended, one
// line per distinct failure reason, and the names that exist nowhere. At most
// 8 lines, "" when there is nothing to say.
func searchNotes(lks []evidenceLookup, missing []string, root string) string {
	var run, ok, partial, failed, unavailable int
	type group struct {
		status, reason string
		n              int
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, l := range lks {
		switch l.ExecutionStatus {
		case lookupNotRequested:
			continue
		case lookupFailed:
			failed++
		case lookupUnavailable:
			unavailable++
		default:
			if l.Status == "partial" {
				partial++
			} else {
				ok++
			}
		}
		run++
		if len(l.Errors) == 0 {
			continue
		}
		reason := noteReason(strings.Join(l.Errors, "; "), root)
		key := string(l.ExecutionStatus) + "\x00" + reason
		g := byKey[key]
		if g == nil {
			g = &group{status: string(l.ExecutionStatus), reason: reason}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.n++
	}
	var names []string
	for _, m := range missing {
		if m = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m), "- ")); m != "" {
			names = append(names, m)
		}
	}
	if run == 0 && len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Search notes\n")
	if run > 0 {
		fmt.Fprintf(&b, "Lookups: %d run, %d ok, %d partial, %d failed, %d unavailable.\n", run, ok, partial, failed, unavailable)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].n != groups[j].n {
			return groups[i].n > groups[j].n
		}
		return groups[i].reason < groups[j].reason
	})
	for i, g := range groups {
		if i == searchNotesMaxWays-1 && len(groups) > searchNotesMaxWays {
			fmt.Fprintf(&b, "%d more failure reasons; see the manifest.\n", len(groups)-i)
			break
		}
		noun := "lookups"
		if g.n == 1 {
			noun = "lookup"
		}
		fmt.Fprintf(&b, "%d %s %s: %s\n", g.n, noun, g.status, g.reason)
	}
	if len(names) > 0 {
		more := ""
		if len(names) > searchNotesNames {
			more, names = fmt.Sprintf(" (+%d more)", len(names)-searchNotesNames), names[:searchNotesNames]
		}
		fmt.Fprintf(&b, "Names looked up that were not found: %s%s.\n", strings.Join(names, ", "), more)
	}
	return b.String()
}

// Render has one byte budget for ALL sections, including headings, source,
// docs, checklist and expansion instructions. No section receives a second cap.
//
// The dossier leads with what the scoring established (ranked files, interface
// sheet, the checklist with its candidates) and ends with a short note on the
// searches. Selection runs in that order of value, so a tight budget cuts the
// search notes, then the lowest-scored candidates, then the source previews; the
// lookup records are not part of the markdown at all. The complete omission
// list is persisted in dossier-manifest.json.
func (d *ctxBrief) Render(heading, noun string, total int) string {
	// Selection tiers, first to last: the scored picture and its structure, the
	// candidate lines (best score first), the search notes, then look-alikes and
	// planner results, and last the source previews.
	const (
		tierCore = iota
		tierCand
		tierNotes
		tierExtra
		tierBody
	)
	type entry struct {
		text, id, expansion string
		body                bool
		tier, score         int
		sec                 string // the section whose heading this entry needs
		head                bool   // this entry is the heading of sec
		dep                 int    // 1 + the index of the entry this one needs, 0 for none
		kind                string // what a cut of this entry is called in the cut line
	}
	var entries []entry
	headOf := map[string]int{}
	add := func(text string) { entries = append(entries, entry{text: text + "\n"}) }
	section := func(sec, text string) {
		headOf[sec] = len(entries)
		entries = append(entries, entry{text: text + "\n", sec: sec, head: true})
	}
	in := func(sec, text string) { entries = append(entries, entry{text: text + "\n", sec: sec}) }

	add(heading)
	add(dossierIntro)
	if len(d.Files) > 0 {
		section("files", "### 1. Ranked files")
		for _, text := range d.Files {
			in("files", text)
		}
	}
	if len(d.Docs) > 0 {
		section("docs", "### 1b. Docs that mention it")
		for _, text := range d.Docs {
			in("docs", text)
		}
	}
	sheet := d.Sheet
	if d.interfaceSummary != nil {
		sheet = d.interfaceSummary
	}
	if len(sheet) > 0 {
		section("sheet", "### 2. Interface sheet")
		for i, text := range sheet {
			path, start := "", 1
			if i < len(d.Facts) && len(d.Facts[i].Defs) > 0 {
				path, start = splitWhere(d.Facts[i].Defs[0].Where)
			}
			entries = append(entries, entry{text: text + "\n", id: fmt.Sprintf("signature-summary:%d", i), expansion: dossierRead(path, start, start+ctxSnippet-1), sec: "sheet"})
		}
	}
	if len(d.ReuseItems) > 0 || d.Reuse != "" {
		add("### Candidates (unverified)")
		add("### 3a. Reuse checklist (each requirement remains a separate decision)")
	}
	// A candidate is shown once, under the first item that has it, with the
	// other items it also serves; those items point back to it.
	firstItem := map[string]int{}
	alsoFor := map[string][]string{}
	for ri, ic := range d.ReuseItems {
		for _, c := range ic.Cands {
			id := candID(c)
			if _, ok := firstItem[id]; !ok {
				firstItem[id] = ri
			} else if firstItem[id] != ri {
				alsoFor[id] = append(alsoFor[id], itemRef(ic.Item.ID))
			}
		}
	}
	seen := map[string]bool{}
	headerAt := map[string]int{}
	for ri, ic := range d.ReuseItems {
		if ri == itemPageMain {
			add(reuseMoreNotice)
		}
		add(strings.TrimRight(itemHead(ic), "\n"))
		for i, c := range ic.Cands {
			c.EvidenceID = candID(c)
			if c.Expansion == "" {
				c.Expansion = dossierRead(c.Path, c.Start, max(c.Start, c.End))
			}
			if seen[c.EvidenceID] {
				if h, ok := headerAt[c.EvidenceID]; ok {
					entries = append(entries, entry{text: fmt.Sprintf("  also: `%s` (listed under %s)\n", c.at(), itemRef(d.ReuseItems[firstItem[c.EvidenceID]].Item.ID)), tier: tierCand, score: c.Score, dep: h + 1, kind: "cand"})
				}
				continue
			}
			seen[c.EvidenceID] = true
			plain := candidateText(i+1, c, false)
			if at := strings.Index(plain, "     omitted "); at >= 0 {
				plain = plain[:at]
			}
			header := plain
			if also := alsoFor[c.EvidenceID]; len(also) > 0 {
				header = strings.Replace(header, "\n", " also for "+strings.Join(also, ", ")+"\n", 1)
			}
			headerAt[c.EvidenceID] = len(entries)
			entries = append(entries, entry{text: header, id: c.EvidenceID, expansion: c.Expansion, tier: tierCand, score: c.Score, kind: "cand"})
			entries = append(entries, entry{text: strings.TrimPrefix(candidateText(i+1, c, true), plain) + "\n", id: c.EvidenceID, expansion: c.Expansion, body: true, tier: tierBody, score: c.Score, dep: headerAt[c.EvidenceID] + 1, kind: "body"})
		}
	}
	if len(d.ReuseItems) == 0 && d.Reuse != "" {
		add(d.Reuse)
	}
	if len(d.Like) > 0 {
		section("like", "### 3. Existing things that look like what this "+noun+" builds (outside its owns)")
		for _, text := range d.Like {
			native, ok := d.nativeLike[text]
			if !ok {
				entries = append(entries, entry{text: text + "\n", sec: "like", tier: tierExtra, kind: "other"})
				continue
			} // notes/cut notices have no source body
			er := native.source
			expand := dossierRead(er.Path, er.Range.StartLine, er.Range.EndLine)
			if seen[er.ID] {
				in("like", "- source "+er.ID+" referenced above or in omissions; expand: "+expand)
				continue
			}
			seen[er.ID] = true
			entries = append(entries, entry{text: native.text + "\n", id: er.ID, expansion: expand, body: true, sec: "like", tier: tierExtra, kind: "other"})
		}
	}
	if d.Foreign != "" {
		section("foreign", "### 3b. Not in our workspace")
		in("foreign", strings.TrimRight(d.Foreign, "\n"))
	}
	if d.Planned != nil {
		section("planned", "### 5. Lookups the planner asked for")
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
			entries = append(entries, entry{text: text + "\n", id: id, expansion: expand, body: true, sec: "planned", tier: tierExtra, kind: "other"})
		}
	}
	add("Coverage gaps: candidate relevance is unverified. Expand incoming/outgoing references, registrations/configuration and tests where material; record any uncompleted checks.")
	var all []evidenceLookup
	seenLookup := map[string]bool{}
	for _, lks := range append([][]evidenceLookup{d.Lookups}, func() [][]evidenceLookup {
		var out [][]evidenceLookup
		for _, ic := range d.ReuseItems {
			out = append(out, ic.Lookups)
		}
		return out
	}()...) {
		for _, l := range lks {
			if !seenLookup[l.ID] {
				seenLookup[l.ID] = true
				all = append(all, l)
			}
		}
	}
	if notes := searchNotes(all, d.Missing, d.root); notes != "" {
		entries = append(entries, entry{text: notes, id: "search-notes", expansion: "Read path=\"dossier-manifest.json\"", tier: tierNotes, kind: "notes"})
	}
	d.Omissions = nil
	d.Shown = map[string]bool{}
	// Reserve room for the one-line cut notice before selecting. Metadata that
	// itself exceeds the budget remains in the persisted manifest, never cut mid-body.
	reserve := min(total/3, 400)
	limit := max(0, total-reserve)
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ea, eb := entries[order[a]], entries[order[b]]
		if ea.tier != eb.tier {
			return ea.tier < eb.tier
		}
		return ea.score > eb.score
	})
	kept := make([]bool, len(entries))
	headKept := map[string]bool{}
	used := 0
	// Candidates and previews are cut strictly by score: once one does not fit,
	// no lower-scored one of its kind takes the room it left.
	stopped := map[string]bool{}
	for _, i := range order {
		en := entries[i]
		if en.head || (en.dep > 0 && !kept[en.dep-1]) || (en.kind != "" && stopped[en.kind]) {
			continue
		}
		cost := len(en.text)
		if en.sec != "" && !headKept[en.sec] {
			cost += len(entries[headOf[en.sec]].text)
		}
		if used+cost > limit {
			if en.kind == "cand" || en.kind == "body" {
				stopped[en.kind] = true
			}
			continue
		}
		kept[i], used = true, used+cost
		if en.sec != "" {
			headKept[en.sec] = true
		}
	}
	var b strings.Builder
	cut := map[string]int{}
	cutAny := 0
	omit := func(om dossierOmission) {
		for _, o := range d.Omissions {
			if o.EvidenceID == om.EvidenceID && o.Reason == om.Reason {
				return
			}
		}
		d.Omissions = append(d.Omissions, om)
	}
	for i, en := range entries {
		if en.head {
			if headKept[en.sec] {
				b.WriteString(en.text)
			}
			continue
		}
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
				omit(dossierOmission{en.id, "partial body preview", en.expansion})
			}
			continue
		}
		if en.dep == 0 && en.id == "" && en.kind == "" && en.tier == tierCore && en.sec == "" {
			continue // structure lines carry no evidence; the manifest keeps the data
		}
		cutAny++
		if en.kind != "" {
			cut[en.kind]++
		}
		found := false
		for _, er := range d.Evidence {
			for _, id := range d.plannedShown[en.id] {
				if er.ID == id && er.Range != nil {
					found = true
					omit(dossierOmission{er.ID, "overall dossier byte budget", dossierRead(er.Path, er.Range.StartLine, er.Range.EndLine)})
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
		omit(dossierOmission{id, "overall dossier byte budget", expand})
	}
	if cutAny > 0 {
		var what []string
		if cut["notes"] > 0 {
			what = append(what, "the search notes")
		}
		if n := cut["cand"]; n > 0 {
			what = append(what, fmt.Sprintf("%d lower-scored candidate line(s)", n))
		}
		if n := cut["body"]; n > 0 {
			what = append(what, fmt.Sprintf("%d source preview(s)", n))
		}
		if n := cut["other"]; n > 0 {
			what = append(what, fmt.Sprintf("%d look-alike or planner entr(ies)", n))
		}
		if len(what) == 0 {
			what = []string{fmt.Sprintf("%d entries", cutAny)}
		}
		footer := fmt.Sprintf("Cut for the %d-byte budget: %s. Omission list and expansions: Read path=\"dossier-manifest.json\".\n", total, strings.Join(what, ", "))
		if b.Len()+len(footer) <= total {
			b.WriteString(footer)
		}
	}
	return b.String()
}
