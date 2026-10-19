package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Pagination of the lists `orch ctx` prints. It is stateless: page P of a list
// sorted the same way every time is the slice [(P-1)*N, P*N), so a page never
// depends on the pages before it.

const (
	ctxFindPage    = 4  // concept hits per page
	ctxTextPage    = 5  // text hits per page
	ctxListPage    = 10 // callers and implementers per page
	ctxNarrowAbove = 50 // above this many, narrowing comes before paging
	ctxPageSuggest = 3  // pages past this one are no longer suggested
)

// pageSize is the page size a lookup asks for: limit, else its max alias, else
// the default.
func (l ctxLookup) pageSize(def int) int {
	switch {
	case l.Limit > 0:
		return l.Limit
	case l.Max > 0:
		return l.Max
	}
	return def
}

func (l ctxLookup) pageNo() int { return max(l.Page, 1) }

// checkPaging rejects a limit, max or page below 1 that was given explicitly.
func checkPaging(a *app, visited map[string]bool, vals map[string]int) error {
	for _, k := range []string{"limit", "max", "page"} {
		if visited[k] && vals[k] < 1 {
			return ctxErr(a, fmt.Sprintf("--%s must be at least 1 (got %d)", k, vals[k]))
		}
	}
	return nil
}

// pageRange is the slice bounds of a page over total items; past is true when
// a page after the first starts beyond the list.
func pageRange(total, limit, page int) (lo, hi int, past bool) {
	lo = (page - 1) * limit
	if total == 0 {
		return 0, 0, false
	}
	if lo >= total {
		return total, total, page > 1
	}
	return lo, min(lo+limit, total), false
}

func pageCount(total, limit int) int { return (total + limit - 1) / limit }

func pastEnd(total, limit, page int, unit string) string {
	n := pageCount(total, limit)
	return fmt.Sprintf("page %d is past the end (%d %s, %d page%s)", page, total, unit, n, plural(n))
}

// pageFooter says which items are shown and how to get the rest. atLeast marks
// a total that is only a lower bound (a ranked list asked for P*N results).
func pageFooter(lo, hi, total, limit, page int, atLeast, batch bool, narrow string) string {
	if hi <= lo || hi <= 0 {
		return ""
	}
	if ctxTune.SlimFooter {
		return pageFooterSlim(lo, hi, total, page, atLeast, batch, narrow)
	}
	more := hi < total || atLeast
	if !more && page == 1 {
		return ""
	}
	of := strconv.Itoa(total)
	if atLeast {
		of = "at least " + of
	}
	s := fmt.Sprintf("showing %d-%d of %s", lo+1, hi, of)
	if !more {
		return s
	}
	next := fmt.Sprintf("--page %d", page+1)
	if batch {
		// Agents batch, so the footer speaks the key=value form of a -q lookup.
		next = fmt.Sprintf("page=%d", page+1)
		if n, ok := batchNarrow[narrow]; ok {
			narrow = n
		}
	}
	suggest := page <= ctxPageSuggest
	switch {
	case total > ctxNarrowAbove && !atLeast:
		s += fmt.Sprintf("; %d more; narrow with %s", total-hi, narrow)
		if suggest {
			s += ", or " + next
		}
	case total > ctxNarrowAbove:
		s += "; more; narrow with " + narrow
		if suggest {
			s += ", or " + next
		}
	case suggest:
		s += "; next: " + next
	default:
		s += "; narrow with " + narrow
	}
	return s
}

// sortLocs sorts "path:line" places by file, then line.
func sortLocs(locs []string) []string {
	out := append([]string(nil), locs...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, li := splitWhere(out[i])
		pj, lj := splitWhere(out[j])
		if pi != pj {
			return pi < pj
		}
		return li < lj
	})
	return out
}

// batchNarrow is each narrowing hint as the keys of a -q lookup.
var batchNarrow = map[string]string{
	narrowText: "glob=/word=/tests=/path=",
	narrowFind: "path=/tests=",
	narrowSym:  "in=/def_kind=/tests=",
}

const (
	narrowText = "--glob/--word/--tests/-p"
	narrowFind = "-p/--tests"
	narrowSym  = "--in/--kind/--tests"
)

// ctxRestFiles is how many files a footer names for the hits it left out.
const ctxRestFiles = 5

// ctxLeftOutMax is the most files the lean footer names; past it nothing is.
const ctxLeftOutMax = 8

// restNote names the files of the hits a page left out, with how many hits each
// holds, so the agent can tell from the footer whether the next page is worth a
// call.
func restNote(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	count := map[string]int{}
	var order []string
	for _, p := range paths {
		if count[p] == 0 {
			order = append(order, p)
		}
		count[p]++
	}
	if ctxTune.SlimFooter {
		// A list of files that fits is worth naming; the first five of two
		// hundred are not. The fullest files come first.
		if len(order) > ctxLeftOutMax {
			return ""
		}
		sort.SliceStable(order, func(i, j int) bool { return count[order[i]] > count[order[j]] })
	}
	var parts []string
	for _, p := range order[:min(ctxRestFiles, len(order))] {
		parts = append(parts, fmt.Sprintf("%s (%d)", p, count[p]))
	}
	if more := len(order) - ctxRestFiles; more > 0 {
		parts = append(parts, fmt.Sprintf("+%d more files", more))
	}
	sep := "; left out: "
	if ctxTune.SlimFooter {
		sep = " | left out: "
	}
	return sep + strings.Join(parts, ", ")
}

// slimNarrow is each narrowing hint as the slim footer writes it: the CLI flags
// and the lookup keys.
var slimNarrow = map[string][2]string{
	narrowText: {"--glob/-p", "glob=/path="},
	narrowFind: {"-p", "path="},
	narrowSym:  {"--in", "in="},
}

// pageFooterSlim is the footer without what the header already says (the
// counts): how to get more, how to narrow, in one line. The last page of a list
// says so; the only page of a list says nothing.
func pageFooterSlim(lo, hi, total, page int, atLeast, batch bool, narrow string) string {
	more := hi < total || atLeast
	if !more {
		if page == 1 {
			return ""
		}
		return "last page"
	}
	suggest := page <= ctxPageSuggest
	var parts []string
	if suggest {
		next := fmt.Sprintf("--page %d", page+1)
		if batch {
			next = fmt.Sprintf("page=%d", page+1)
		}
		parts = append(parts, "more: "+next)
	}
	if !suggest || total > ctxNarrowAbove {
		n := narrow
		if forms, ok := slimNarrow[narrow]; ok {
			n = forms[0]
			if batch {
				n = forms[1]
			}
		}
		parts = append(parts, "narrow: "+n)
	}
	return strings.Join(parts, " | ")
}
