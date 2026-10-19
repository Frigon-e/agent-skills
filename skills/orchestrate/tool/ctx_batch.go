package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// A batch is many lookups in one call: a plan of concept, symbol, text and
// read lookups, run in parallel from the cache, answered in plan order with
// each answer capped. It is what an agent uses instead of a dozen turns.

const (
	ctxPlanMax    = 25   // lookups the gate lets a planner ask for, per package
	ctxResultCaps = 1500 // characters kept of one lookup's answer in a batch
	ctxReadLines  = 60
)

// ctxLookup is one lookup of a plan. Kind is concept, symbol, text or read;
// the other fields are options of that kind, all optional. Why is a label for
// the reader and never part of any cache key.
type ctxLookup struct {
	Kind    string `json:"kind"`
	Query   string `json:"query"`
	Path    string `json:"path,omitempty"`
	From    int    `json:"from,omitempty"`
	To      int    `json:"to,omitempty"`
	Why     string `json:"why,omitempty"`
	Package string `json:"package,omitempty"`   // the gate's planner names one; ignored here
	Tests   string `json:"tests,omitempty"`     // none (default), include or only
	Gen     string `json:"generated,omitempty"` // generated files: none (default), include or only
	Max     int    `json:"max,omitempty"`       // alias of limit
	Limit   int    `json:"limit,omitempty"`     // page size
	Page    int    `json:"page,omitempty"`      // 1-based page of the list
	// text options (ripgrep)
	Fixed        bool    `json:"fixed,omitempty"`
	Word         bool    `json:"word,omitempty"`
	IgnoreCase   bool    `json:"ignore_case,omitempty"`
	Glob         strList `json:"glob,omitempty"`
	Type         string  `json:"type,omitempty"`
	FilesOnly    bool    `json:"files_only,omitempty"`
	Count        bool    `json:"count,omitempty"`
	Context      *int    `json:"context,omitempty"` // lines around a hit (default 2)
	Multiline    bool    `json:"multiline,omitempty"`
	OnlyMatching bool    `json:"only_matching,omitempty"`
	PerFile      int     `json:"per_file,omitempty"`
	All          bool    `json:"all,omitempty"` // also lockfiles, generated and vendored files
	// symbol options
	Refs    *int   `json:"refs,omitempty"` // callers listed (non-test; default 10)
	Impl    *bool  `json:"impl,omitempty"` // implementers listed (default true)
	DefKind string `json:"def_kind,omitempty"`
	In      string `json:"in,omitempty"`
}

// strList decodes either one string or a list of strings.
type strList []string

func (l *strList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*l = strList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

type ctxResult struct {
	Lookup ctxLookup `json:"lookup"`
	Head   string    `json:"head"`
	Output string    `json:"output"`
}

func (r ctxResult) String() string {
	return r.Head + "\n" + indent(r.Output, "    ")
}

func indent(s, pre string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pre + lines[i]
	}
	return strings.Join(lines, "\n")
}

// cutLines keeps whole lines of s within max characters.
func cutLines(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndex(s[:max], "\n")
	if cut < 0 { // one long line: keep its start
		return s[:max] + " ... (cut)"
	}
	return s[:cut] + "\n... (cut)"
}

// repoPath cleans a path and refuses one outside the repo.
func repoPath(p string) (string, bool) {
	p = filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimSpace(p), "./")))
	if p == "." || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// Batch runs the lookups. rootOf gives the TypeScript project root of lookup i
// (nil: found from the query). capChars caps each answer; 0 means ctxResultCaps.
func (e *ctxEnv) Batch(lks []ctxLookup, rootOf func(i int) string, capChars int) []ctxResult {
	if capChars <= 0 {
		capChars = ctxResultCaps
	}
	root := func(i int) string {
		if rootOf != nil {
			return rootOf(i)
		}
		return tsProjectRoot(e.Code, lks[i].Query, nil)
	}
	// Symbols in one batch, so one language server serves them all.
	goSeen := map[string]bool{}
	var goNames []string
	tsSeen := map[tsJob]bool{}
	var tsJobs []tsJob
	for i, l := range lks {
		q := strings.TrimSpace(l.Query)
		if strings.ToLower(strings.TrimSpace(l.Kind)) != "symbol" || !symIdent.MatchString(q) {
			continue
		}
		if !goSeen[q] {
			goSeen[q] = true
			goNames = append(goNames, q)
		}
		if r := root(i); r != "" && tsPlain.MatchString(q) {
			if j := (tsJob{r, q}); !tsSeen[j] {
				tsSeen[j] = true
				tsJobs = append(tsJobs, j)
			}
		}
	}
	var goFacts map[string]*symFact
	var tsFacts map[tsJob]*symFact
	if len(goNames) > 0 {
		goFacts, tsFacts = e.Symbols(goNames, tsJobs)
	}
	out := make([]ctxResult, len(lks))
	e.parallel(len(lks), func(i int) {
		l := lks[i]
		l.Kind = strings.ToLower(strings.TrimSpace(l.Kind))
		var res string
		what := l.Query
		switch l.Kind {
		case "concept":
			res = e.lookupConcept(l.Query, l.Path)
		case "symbol":
			q := strings.TrimSpace(l.Query)
			f := goFacts[q]
			if f == nil || len(f.Defs) == 0 {
				if t := tsFacts[tsJob{root(i), q}]; t != nil {
					f = t
				}
			}
			if f == nil {
				res = "no result (symbol lookup unavailable or failed)"
			} else {
				res = strings.Join(e.sheetLines(*f, sheetOpts{Callers: 6, Impls: true}), "\n")
			}
		case "text":
			res = e.lookupText(l.Query, l.Path)
		case "read":
			what = fmt.Sprintf("%s:%d-%d", l.Path, l.From, l.To)
			res = e.lookupRead(l.Path, l.From, l.To)
		default:
			res = "unknown kind"
		}
		head := fmt.Sprintf("- [%s] `%s`", l.Kind, short(flat(what), 80))
		if l.Why != "" {
			head += " - " + short(flat(l.Why), 80)
		}
		out[i] = ctxResult{Lookup: l, Head: head, Output: cutLines(strings.TrimRight(res, "\n"), capChars)}
	})
	return out
}

func (e *ctxEnv) lookupConcept(q, pathFilter string) string {
	q = short(flat(q), 300)
	hits, err := e.Concept(q, pathFilter, 3)
	if err != nil {
		return "no result (" + err.Error() + ")"
	}
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "%s:%d-%d\n", h.Path, h.Start, h.End)
		for _, l := range h.Snippet[:min(3, len(h.Snippet))] {
			b.WriteString("  " + short(l, 120) + "\n")
		}
	}
	if b.Len() == 0 {
		return "no hits"
	}
	return b.String()
}

func (e *ctxEnv) lookupText(q, path string) string {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 200 {
		return "no result (empty or too long query)"
	}
	if path != "" {
		p, ok := repoPath(path)
		if !ok {
			return "no result (path outside the repo)"
		}
		path = p
	}
	res, err := e.Text(rgQuery{Pattern: q, Path: path, AllFiles: true, CaseSensitive: true})
	if err != nil {
		return "no result (" + err.Error() + ")"
	}
	if len(res.Hits) == 0 {
		return "no matches"
	}
	var b strings.Builder
	for _, h := range res.Hits[:min(20, len(res.Hits))] {
		fmt.Fprintf(&b, "%s:%d: %s\n", h.Path, h.Line, short(h.Text, 160))
	}
	return b.String()
}

func (e *ctxEnv) lookupRead(path string, from, to int) string {
	p, ok := repoPath(path)
	if !ok {
		return "no result (path outside the repo)"
	}
	if real, err := filepath.EvalSymlinks(filepath.Join(e.Code, p)); err != nil || !strings.HasPrefix(real, evalOr(e.Code)+string(filepath.Separator)) {
		return "no result (file not found in the repo)"
	}
	ls := e.Lines(p)
	if len(ls) == 0 {
		return "no result (file not found or empty)"
	}
	from = max(from, 1)
	if to < from || to-from+1 > ctxReadLines {
		to = from + ctxReadLines - 1
	}
	to = min(to, len(ls))
	if from > to {
		return "no result (range past the end of the file)"
	}
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i, strings.TrimRight(ls[i-1], "\r"))
	}
	return b.String()
}
