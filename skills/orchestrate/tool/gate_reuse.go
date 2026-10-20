package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The reuse checklist: for each item a package builds, the existing code that
// might already do the job, found by code with no model. Evidence is counted
// the way the dossier counts it (concept hit 2, symbol definition 2, rare text
// hit 1, two kinds agreeing +2); the package's own files, tests, generated
// files and docs never count. Matched source previews and precise expansion
// routes let a reviewer assess candidates without treating retrieval as proof.

const (
	reuseCands      = 3  // default candidates per query in small packages
	reuseBodyBudget = 24 // bodies per package, divided over its queries
	reuseMinCands   = 2
	reuseMaxCands   = 5
	reuseRootQuota  = 2  // ... at most this many from one project root
	reuseShowLines  = 40 // body lines shown for a candidate
	reuseKeepLines  = 80 // body lines kept for the shadow judges
	reuseNameLimit  = 6  // identifier-like names looked up per item
	reuseMoreNotice = "More items (13 and later): treat them exactly like the items above and decide every one."
)

// reuseCand is one candidate: a declaration (or, failing that, a place) of
// existing code.
type reuseCand struct {
	EvidenceID   string   `json:"evidence_id,omitempty"`
	MatchLine    int      `json:"match_line,omitempty"`
	MatchEnd     int      `json:"match_end,omitempty"`
	PreviewStart int      `json:"preview_start,omitempty"`
	Expansion    string   `json:"expansion,omitempty"`
	Hint         bool     `json:"hint,omitempty"`
	Path         string   `json:"path"`
	Start        int      `json:"start"`
	End          int      `json:"end"`
	Name         string   `json:"name,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	In           string   `json:"in,omitempty"`
	Sig          string   `json:"signature"`
	Doc          string   `json:"doc,omitempty"`
	Lines        []string `json:"-"` // up to reuseKeepLines lines of the declaration
	Total        int      `json:"lines"`
	Callers      int      `json:"callers"`
	ByName       bool     `json:"callers_by_name,omitempty"`
	Score        int      `json:"score"`
	Kinds        string   `json:"kinds"`
	Via          []string `json:"via,omitempty"`
	Tree         string   `json:"-"`
}

func (c reuseCand) at() string {
	if c.End > c.Start {
		return fmt.Sprintf("%s:%d-%d", c.Path, c.Start, c.End)
	}
	return fmt.Sprintf("%s:%d", c.Path, c.Start)
}

type itemCands struct {
	Item       gateItem
	Cands      []reuseCand
	Queries    []string
	QueriesRun int
	Lookups    []evidenceLookup `json:"lookups,omitempty"`
}

// itemEv is the evidence for one file of one item.
type itemEv struct {
	fileEv
	line, start, end int
	prio             int // 3 symbol definition, 2 concept chunk, 1 text hit
	agree            map[string]bool
	body             []string
	sym              *symDef
}

// itemNames are the identifier-like names an item mentions.
func itemNames(text string) []string {
	var out []string
	for _, s := range goSymbols(text) {
		out = addUniq(out, s, reuseNameLimit)
	}
	for _, s := range genericSymbols(text) {
		out = addUniq(out, s, reuseNameLimit)
	}
	for _, s := range tsSymbols(text, nil) {
		out = addUniq(out, s, reuseNameLimit)
	}
	return out
}

// declLineFor finds the declaration that holds line ln, or the first one in
// ln..end when the line is in a chunk; 0 when there is none.
func declLineFor(ls []string, ln, end int, path string) int {
	l := langOf(path)
	if l == nil || ln < 1 || ln > len(ls) {
		return 0
	}
	for i := ln; i <= min(max(end, ln), min(ln+60, len(ls))); i++ {
		if k, _, _ := l.declOfLine(ls[i-1]); k != "" {
			return i
		}
	}
	hit := indentOf(ls[ln-1])
	for i := ln - 2; i >= 0 && ln-i <= 800; i-- {
		if strings.TrimSpace(ls[i]) == "" {
			continue
		}
		k, _, top := l.declOfLine(ls[i])
		if k == "" || (top && indentOf(ls[i]) > 0) {
			continue
		}
		if c := indentOf(ls[i]); c < hit || (c == 0 && l.BraceEnd) {
			return i + 1
		}
	}
	return 0
}

// reuseCandidates finds the candidates of each item. owns are the files the
// package creates or changes; title is its purpose.
func (e *ctxEnv) reuseCandidates(items []gateItem, owns []string, _ string) []itemCands {
	var queries []gateItem
	var owners []int
	out := make([]itemCands, len(items))
	for i, it := range items {
		out[i] = itemCands{Item: it, Queries: it.Queries}
		if len(it.Queries) == 0 {
			zero := 0
			out[i].Lookups = []evidenceLookup{{SchemaVersion: evidenceSchemaVersion, ID: "lookup-unrequested-" + subjectDigest([]byte(it.ID))[:20], Scope: ".", Unit: "concept", ExecutionStatus: lookupNotRequested, Completeness: evidenceUnknown, CountKind: evidenceCountUnknown, Counts: lookupCounts{Requested: &zero}}}
		}
		for _, q := range it.Queries {
			queries = append(queries, gateItem{ID: it.ID, Package: it.Package, Text: q, Queries: []string{q}})
			owners = append(owners, i)
		}
	}
	for j, ic := range e.reuseQueryCandidates(queries, owns, candidatesPerQuery(len(queries))) {
		i := owners[j]
		out[i].QueriesRun += ic.QueriesRun
		out[i].Lookups = append(out[i].Lookups, ic.Lookups...)
		for _, c := range ic.Cands {
			seen := false
			for _, prev := range out[i].Cands {
				seen = seen || prev.at() == c.at()
			}
			if !seen {
				out[i].Cands = append(out[i].Cands, c)
			}
		}
	}
	return out
}

func candidatesPerQuery(n int) int {
	if n <= reuseBodyBudget/reuseCands {
		return reuseCands
	}
	return max(reuseMinCands, min(reuseMaxCands, reuseBodyBudget/max(1, n)))
}

func (e *ctxEnv) reuseQueryCandidates(items []gateItem, owns []string, limit int) []itemCands {
	trees := orderTrees(codeTreesFor(e.Code, owns), owns)
	if len(trees) == 0 {
		trees = []string{""}
	}
	ownL := map[string]bool{}
	for _, id := range ownsLangs(e.Code, owns) {
		ownL[id] = true
	}
	res := make([]itemCands, len(items))
	names := make([][]string, len(items))
	// Names first: one symbol batch for all items, so one server serves them.
	goSeen := map[string]bool{}
	var goNames []string
	tsSeen := map[tsJob]bool{}
	var tsJobs []tsJob
	tsRoots := make([]string, len(items))
	for i, it := range items {
		res[i].Item = it
		names[i] = itemNames(it.Text)
		tsRoots[i] = tsProjectRoot(e.Code, it.Text, owns)
		for _, n := range names[i] {
			if !goSeen[n] {
				goSeen[n] = true
				goNames = append(goNames, n)
			}
			if tsRoots[i] != "" && tsPlain.MatchString(n) {
				if j := (tsJob{tsRoots[i], n}); !tsSeen[j] {
					tsSeen[j] = true
					tsJobs = append(tsJobs, j)
				}
			}
		}
		res[i].Queries = it.Queries
	}
	var goFacts map[string]*symFact
	var tsFacts map[tsJob]*symFact
	if len(goNames) > 0 {
		goFacts, tsFacts = e.Symbols(goNames, tsJobs)
	}
	evs := make([]map[string]*itemEv, len(items))
	for i := range evs {
		evs[i] = map[string]*itemEv{}
	}
	var mu sync.Mutex
	note := func(i int, path, kind, via string, prio, line, start, end int, q string, body []string, sym *symDef) {
		if path == "" || e.Skip[path] || inOwns(path, owns) || !isCodePath(path) || !lookAlikePath(path, ownL) || e.isGenerated(path) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		ev := evs[i][path]
		if ev == nil {
			ev = &itemEv{fileEv: fileEv{kinds: map[string]bool{}}, agree: map[string]bool{}}
			evs[i][path] = ev
		}
		ev.kinds[kind] = true
		ev.agree[q] = true
		ev.why = addUniq(ev.why, via, 3)
		if prio > ev.prio {
			ev.prio, ev.line, ev.start, ev.end, ev.body, ev.sym = prio, line, start, end, body, sym
		}
	}
	for i := range items {
		for _, n := range names[i] {
			f := goFacts[n]
			if f == nil || len(f.Defs) == 0 {
				if t := tsFacts[tsJob{tsRoots[i], n}]; t != nil {
					f = t
				}
			}
			res[i].Lookups = append(res[i].Lookups, dossierSymbolLookup(n, orDefault(tsRoots[i], "."), f))
			if f == nil {
				continue
			}
			for k := range f.Defs {
				d := f.Defs[k]
				p, ln := splitWhere(d.Where)
				note(i, p, "sym", "symbol `"+n+"`", 3, ln, ln, ln, n, nil, &d)
			}
		}
	}
	// Text hits of rare names, and concept searches per project root.
	type job struct {
		i     int
		name  string
		tree  string
		isTxt bool
	}
	var jobs []job
	jbReady := e.jbMissing() == ""
	for i := range items {
		for _, n := range names[i] {
			jobs = append(jobs, job{i, n, "", true})
		}
		for _, q := range res[i].Queries {
			for _, t := range trees {
				if jbReady {
					jobs = append(jobs, job{i, q, t, false})
				} else {
					res[i].Lookups = append(res[i].Lookups, dossierLookup(q, t, "concept", 0, dossierConceptHits, nil, e.jbMissing()))
				}
			}
		}
	}
	e.parallel(len(jobs), func(k int) {
		j := jobs[k]
		if j.isTxt {
			r, err := e.Text(rgQuery{Pattern: j.name, Fixed: true, PerFile: 3, AllFiles: true, CaseSensitive: true})
			mu.Lock()
			res[j.i].Lookups = append(res[j.i].Lookups, dossierTextLookup(j.name, ".", r, err))
			mu.Unlock()
			if err != nil || len(r.Hits) == 0 || len(r.Hits) > dossierRare {
				return
			}
			for _, h := range r.Hits {
				note(j.i, h.Path, "text", "text `"+short(j.name, 40)+"`", 1, h.Line, h.Line, h.Line, j.name, nil, nil)
			}
			return
		}
		hits, err := e.Concept(j.name, j.tree, dossierConceptHits)
		mu.Lock()
		res[j.i].Lookups = append(res[j.i].Lookups, dossierLookup(j.name, j.tree, "concept", len(hits), dossierConceptHits, err, ""))
		mu.Unlock()
		if err != nil {
			return
		}
		for _, h := range hits {
			body := h.Body
			if len(body) == 0 {
				body = h.Snippet
			}
			note(j.i, h.Path, "jb", "concept \""+short(j.name, 40)+"\"", 2, h.Start, h.Start, h.End, j.name, body, nil)
		}
	})
	for i := range items {
		sort.Slice(res[i].Lookups, func(a, b int) bool { return res[i].Lookups[a].ID < res[i].Lookups[b].ID })
		// A query succeeds only after every requested scoped execution completes.
		for _, q := range res[i].Queries {
			complete := true
			for _, lk := range res[i].Lookups {
				if lk.Unit == "concept" && lk.Query == q && lk.ExecutionStatus != lookupSucceeded {
					complete = false
				}
			}
			if complete {
				res[i].QueriesRun++
			}
		}
		type scored struct {
			path  string
			ev    *itemEv
			score int
			tree  string
		}
		var all []scored
		for p, ev := range evs[i] {
			all = append(all, scored{p, ev, ev.score(), treeOf(trees, p)})
		}
		sort.Slice(all, func(a, b int) bool {
			if all[a].score != all[b].score {
				return all[a].score > all[b].score
			}
			if len(all[a].ev.agree) != len(all[b].ev.agree) {
				return len(all[a].ev.agree) > len(all[b].ev.agree)
			}
			return all[a].path < all[b].path
		})
		perTree := map[string]int{}
		for _, s := range all {
			if len(res[i].Cands) >= limit {
				break
			}
			if perTree[s.tree] >= reuseRootQuota {
				continue
			}
			perTree[s.tree]++
			c := e.candidateAt(s.path, s.ev)
			c.Score, c.Kinds, c.Via, c.Tree = s.score, s.ev.kindList(), s.ev.why, s.tree
			res[i].Cands = append(res[i].Cands, c)
		}
	}
	return res
}

// candidateAt builds the candidate for a file from its best evidence.
func (e *ctxEnv) candidateAt(path string, ev *itemEv) reuseCand {
	ls := e.Lines(path)
	c := reuseCand{Path: path, Start: ev.line, End: ev.end, MatchLine: ev.line, MatchEnd: max(ev.line, ev.end)}
	if d := declLineFor(ls, ev.line, ev.end, path); d > 0 {
		body, more := bodyPreview(ls, d, reuseKeepLines)
		c.Start, c.Total, c.Lines = d, len(body)+more, body
		c.End = d + c.Total - 1
		l := langOf(path)
		k, n, _ := l.declOfLine(ls[d-1])
		c.Kind, c.Name = k, n
		c.In = strings.TrimSpace(k + " " + n)
		c.Sig = short(strings.TrimSpace(ls[d-1]), 200)
		if doc := docBlock(ls, d, path); len(doc) > 0 {
			c.Doc = short(doc[0], 160)
		}
	} else {
		body := ev.body
		if len(body) == 0 && ev.line >= 1 && ev.line <= len(ls) {
			body = ls[ev.line-1 : min(len(ls), ev.line+9)]
		}
		c.Lines, c.Total = body[:min(len(body), reuseKeepLines)], len(body)
		if len(body) > 0 {
			c.Sig = short(strings.TrimSpace(body[0]), 200)
		}
		c.End = c.Start + max(c.Total-1, 0)
	}
	// Preview the matched region, not just the declaration's opening lines.
	c.PreviewStart = c.Start
	if c.Total > reuseShowLines && c.MatchLine > c.Start+reuseShowLines/2 {
		c.PreviewStart = max(c.Start, c.MatchLine-reuseShowLines/2)
		end := min(c.End, c.PreviewStart+reuseShowLines-1)
		if c.PreviewStart >= 1 && end <= len(ls) {
			c.Lines = append([]string{}, ls[c.PreviewStart-1:end]...)
		}
	}
	fromLine := min(len(ls), max(0, c.Start-1))
	toLine := min(len(ls), max(fromLine, c.End))
	c.EvidenceID = dossierSourceID(path, c.Start, c.End, strings.Join(ls[fromLine:toLine], "\n"))
	from := max(c.Start, c.MatchLine-3)
	c.Expansion = dossierRead(path, from, min(c.End, min(from+reuseShowLines-1, max(c.MatchEnd, c.MatchLine+3))))
	name := c.Name
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case ev.sym != nil && ev.sym.Func:
		c.Callers, c.ByName = ev.sym.Total, ev.sym.ByName
	case name != "":
		if r, err := e.Text(rgQuery{Pattern: name, Fixed: true, Word: true, CaseSensitive: true}); err == nil {
			for _, h := range r.Hits {
				if h.Path == path && h.Line >= c.Start && h.Line <= c.End || isTestPath(h.Path) {
					continue
				}
				c.Callers++
			}
			c.ByName = true
		}
	}
	return c
}

// hintCandidates resolves author pointers without changing the searched evidence.
func (e *ctxEnv) hintCandidates(ptrs []string) []reuseCand {
	var out []reuseCand
	for _, ptr := range ptrs {
		m := pointerRe.FindStringSubmatch(ptr)
		if m == nil {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(m[1]))
		if !ctxWithinRoot(e.Code, path) {
			continue
		}
		ls := e.Lines(path)
		line, end := 0, 0
		if name := m[4]; name != "" {
			l := langOf(path)
			if l == nil || len(l.Decl) == 0 {
				continue
			}
			base := name[strings.LastIndex(name, ".")+1:]
			re := regexp.MustCompile(declPattern(l, base))
			for i, text := range ls {
				if !re.MatchString(text) {
					continue
				}
				_, declared, _ := l.declOfLine(text)
				owner := strings.TrimSuffix(name, "."+base)
				if !strings.Contains(name, ".") || declared == name || strings.HasSuffix(enclosing(ls, i+1, path), " "+owner) {
					line, end = i+1, i+1
					break
				}
			}
		} else {
			line, _ = strconv.Atoi(m[2])
			end = line
			if m[3] != "" {
				end, _ = strconv.Atoi(m[3])
			}
		}
		if line < 1 || end < line || end > len(ls) {
			continue
		}
		c := e.candidateAt(path, &itemEv{line: line, end: end})
		c.Hint = true
		seen := false
		for _, prev := range out {
			seen = seen || prev.at() == c.at()
		}
		if !seen {
			out = append(out, c)
		}
	}
	return out
}

func callersText(c reuseCand) string {
	s := fmt.Sprintf("%d", c.Callers)
	if c.ByName {
		s += " by name"
	}
	return s
}

// candidateText writes one candidate; full shows its matched preview (up to
// 40 lines), otherwise only metadata and the native expansion route.
func candidateText(n int, c reuseCand, full bool) string {
	var b strings.Builder
	if c.EvidenceID == "" {
		c.EvidenceID = dossierSourceID(c.Path, c.Start, c.End, strings.Join(c.Lines, "\n"))
	}
	if c.Expansion == "" {
		c.Expansion = dossierRead(c.Path, c.Start, max(c.Start, c.End))
	}
	head := fmt.Sprintf("  %d. `%s` evidence=%s (candidate, unverified)", n, c.at(), c.EvidenceID)
	if c.In != "" {
		head += " " + c.In
	}
	fmt.Fprintf(&b, "%s [%s, score %d] callers: %s\n", head, c.Kinds, c.Score, callersText(c))
	if c.Sig != "" {
		b.WriteString("     signature: " + c.Sig + "\n")
	}
	if c.Doc != "" {
		b.WriteString("     doc: " + c.Doc + "\n")
	}
	if !full {
		fmt.Fprintf(&b, "     omitted %s: body budget; expand: %s\n", c.EvidenceID, c.Expansion)
		return b.String()
	}
	shown := c.Lines
	more := c.Total - len(shown)
	if len(shown) > reuseShowLines {
		shown = shown[:reuseShowLines]
	}
	// When only a manually supplied preview is available, prefer a matching line.
	if len(c.Lines) > reuseShowLines {
		for i, line := range c.Lines {
			for _, via := range c.Via {
				if strings.Contains(line, via) {
					from := max(0, i-reuseShowLines/2)
					shown = c.Lines[from:min(len(c.Lines), from+reuseShowLines)]
					break
				}
			}
		}
		more = c.Total - len(shown)
	}
	if c.PreviewStart > 0 {
		fmt.Fprintf(&b, "     preview lines %d-%d\n", c.PreviewStart, c.PreviewStart+len(shown)-1)
	}
	for _, l := range shown {
		b.WriteString("     | " + l + "\n")
	}
	if more > 0 {
		fmt.Fprintf(&b, "     | (%d more lines) omitted %s; expand: %s\n", more, c.EvidenceID, c.Expansion)
	}
	return b.String()
}

func itemText(ic itemCands, full bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- `%s` %s\n", ic.Item.ID, ic.Item.Text)
	for _, p := range ic.Item.Reuse {
		fmt.Fprintf(&b, "  author's pointer: `%s`\n", p)
	}
	if len(ic.Queries) == 0 {
		b.WriteString("  search: not_requested (no explicit queries; unresolved work)\n")
	}
	for _, lk := range ic.Lookups {
		b.WriteString(lookupObservation(lk) + "\n")
	}
	if len(ic.Cands) == 0 && len(ic.Queries) > 0 {
		if ic.QueriesRun == len(ic.Queries) {
			b.WriteString("  (completed search: no eligible candidates in the searched scope; not proof of absence)\n")
		} else {
			b.WriteString("  (unresolved: requested search did not complete; no negative conclusion)\n")
		}
	}
	for i, c := range ic.Cands {
		b.WriteString(candidateText(i+1, c, full))
	}
	return b.String()
}

// renderChecklist writes the checklist of the items in ics (restricted to
// the ids in only when it is not nil), capped, saying what was cut.
func renderChecklist(ics []itemCands, only map[string]bool) string {
	var selected []itemCands
	for _, ic := range ics {
		if only == nil || only[ic.Item.ID] {
			selected = append(selected, ic)
		}
	}
	if len(selected) == 0 {
		return ""
	}
	return (&ctxBrief{ReuseItems: selected}).Render("", "package", dossierTotal)
}
