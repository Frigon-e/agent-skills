package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Enrichment makes the gate's question cards easier to settle, with no model
// call: near-duplicate questions are merged, spec lines that may already
// answer one are attached, jbcontext (semantic code search) adds code that may
// answer it, and a routing tag advises who should decide. Every question stays;
// the tag and the hints are advice, never a verdict.

const (
	enrichSimilar    = 0.6 // token Jaccard at which two questions in a package merge
	enrichSpecShare  = 0.6 // share of a question's identifiers a spec line must hold
	enrichMaxHints   = 3
	enrichSnippet    = ctxSnippet
	enrichParallel   = 8
	enrichQueryChars = 300
	enrichMaxFails   = 3 // failed searches after which the rest are skipped
)

// gateOwnerWords marks a question the owner should decide: money, prices,
// tax, refunds, legal, privacy, retention, consent, GDPR, permissions, roles,
// security, wording or naming users see, default behaviour and policy. Words
// match from the start of a word, so "refund" also finds "refunds".
var gateOwnerWords = []string{
	"money", "pric", "tax", "refund", "legal", "privacy", "retention", "consent", "gdpr",
	"permission", "role", "security", "user-visible", "user visible", "wording",
	"shown to users", "shown to the user", "shown to customers", "shown to the customer",
	"default behaviour", "default behavior", "policy", "policies",
}

var ownerRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join(escapeAll(gateOwnerWords), "|") + `)`)

func escapeAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = regexp.QuoteMeta(s)
	}
	return out
}

var enrichStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the is are was were be been being do does did of to in on at for from by with as and or not no
		it its this that these those which what whom who how when where whether should would could can may might must will shall
		if then than so such into onto per via vs there their they them we us our you your i or any each every both either
		one more most other same also only just still yet`) {
		enrichStop[w] = true
	}
}

type specHint struct {
	Line int
	Text string
}

type enrichedQuestion struct {
	N       int // the question's card number in the gate
	Q       gateQuestion
	Merged  []string // other wordings merged into this card
	Tag     string
	Why     string
	Spec    []specHint
	Code    []codeHit
	Facts   []symFact
	Card    string // file name without .md
	Blocker bool
}

type enrichResult struct {
	Total, Cards         int
	Owner, Lead, Unclear int
	SpecHints, CodeHits  int
	SymbolFacts          int
	Minor                []enrichedQuestion
	Items                []enrichedQuestion
	Notes                []string
}

func (r *enrichResult) summary() string {
	return fmt.Sprintf("%d -> %d after merging; owner %d, lead-may-decide %d, unclear %d; spec hints %d; code hits %d; symbol facts %d",
		r.Total, r.Cards, r.Owner, r.Lead, r.Unclear, r.SpecHints, r.CodeHits, r.SymbolFacts)
}

// tokens are the words of a question that carry meaning: lowercase, without
// punctuation or stop words.
func questionTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range regexp.MustCompile(`[a-z0-9_]+`).FindAllString(strings.ToLower(s), -1) {
		if len(w) > 1 && !enrichStop[w] {
			out[w] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

var (
	idBacktick = regexp.MustCompile("`([^`\n]{2,60})`")
	idCode     = regexp.MustCompile(`\b[A-Z]{2,6}-\d{1,4}\b`)
	idSnake    = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
	idCamel    = regexp.MustCompile(`\b(?:[A-Z][a-z0-9]+){2,}\b|\b[a-z]+(?:[A-Z][a-z0-9]*)+\b`)
	idDotted   = regexp.MustCompile(`\b[a-z][A-Za-z0-9]*\.[A-Z][A-Za-z0-9]*\b`)
	idPath     = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+/?`)
	idUnit     = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s?(?:ms|seconds?|secs?|minutes?|mins?|hours?|days?|mb|kb|gb)\b`)
)

// questionIdentifiers lists what a question names exactly: backticked spans,
// snake_case and CamelCase words, ids like FRD-03, dotted calls, file paths
// and numbers with units. They are lowercased and de-duplicated.
func questionIdentifiers(q string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if len(s) >= 2 && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, m := range idBacktick.FindAllStringSubmatch(q, -1) {
		span := m[1]
		if i := strings.Index(span, "("); i > 0 {
			span = span[:i]
		}
		if !strings.Contains(span, " ") {
			add(span)
		}
	}
	for _, re := range []*regexp.Regexp{idCode, idSnake, idCamel, idDotted, idUnit} {
		for _, m := range re.FindAllString(q, -1) {
			add(m)
		}
	}
	for _, m := range idPath.FindAllString(q, -1) {
		m = strings.Trim(m, ".")
		last := m[strings.LastIndex(strings.TrimSuffix(m, "/"), "/")+1:]
		if len(m) >= 5 && (strings.Contains(last, ".") || strings.HasSuffix(m, "/") || strings.Count(m, "/") >= 2) {
			add(m)
		}
	}
	return out
}

// specHints finds spec places that hold at least enrichSpecShare of the
// question's identifiers (and at least two). It looks at windows of three
// lines, because the spec wraps its prose, and reports the first line of the
// window that holds one. The result is a hint only.
func specHints(lines []string, ids []string) []specHint {
	if len(ids) < 2 {
		return nil
	}
	need := max(2, int(math.Ceil(enrichSpecShare*float64(len(ids)))))
	lower := make([]string, len(lines))
	for i, l := range lines {
		lower[i] = strings.ToLower(l)
	}
	type cand struct{ at, n int }
	var cands []cand
	for i := range lines {
		win := strings.Join(lower[i:min(i+3, len(lines))], "\n")
		n := 0
		for _, id := range ids {
			if strings.Contains(win, id) {
				n++
			}
		}
		if n >= need {
			cands = append(cands, cand{i, n})
		}
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].n > cands[b].n })
	var out []specHint
	var taken []int
	for _, c := range cands {
		near := false
		for _, t := range taken {
			if c.at-t < 3 && t-c.at < 3 {
				near = true
			}
		}
		if near {
			continue
		}
		taken = append(taken, c.at)
		line := c.at
		for k := c.at; k < min(c.at+3, len(lines)); k++ {
			for _, id := range ids {
				if strings.Contains(lower[k], id) {
					line = k
					goto found
				}
			}
		}
	found:
		out = append(out, specHint{Line: line + 1, Text: short(strings.TrimSpace(lines[line]), 160)})
		if len(out) == enrichMaxHints {
			break
		}
	}
	return out
}

// routeQuestion advises who should decide. An owner word wins over an
// Authority "May decide" phrase; with neither the question is unclear.
func routeQuestion(q string, mayDecide []string) (tag, why string) {
	if m := ownerRe.FindString(q); m != "" {
		return "owner", "mentions " + strings.ToLower(m)
	}
	low := strings.ToLower(q)
	for _, p := range mayDecide {
		if phraseIn(low, p) {
			return "lead-may-decide", "the spec's Authority lets a lead decide: " + p
		}
	}
	return "unclear", "no routing rule matched"
}

// phraseIn reports whether every word of phrase occurs in text, ignoring a
// plural s, so "query shapes" finds "the query shape".
func phraseIn(text, phrase string) bool {
	words := strings.Fields(phrase)
	if len(words) == 0 {
		return false
	}
	if len(words) == 1 {
		return strings.Contains(text, phrase)
	}
	for _, w := range words {
		if !strings.Contains(text, strings.TrimSuffix(w, "s")) {
			return false
		}
	}
	return true
}

// mayDecidePhrases are the phrases of the Authority "May decide:" line that
// can match a question: two or more words, or an identifier.
func mayDecidePhrases(sp *spec) []string {
	var out []string
	for _, p := range strings.Split(labelled(sp.sections["Authority"], "May decide"), ",") {
		p = strings.ToLower(strings.Trim(strings.TrimSpace(p), "`.;"))
		if len(strings.Fields(p)) >= 2 || strings.ContainsAny(p, "_./") {
			out = append(out, p)
		}
	}
	return out
}

// dedupeQuestions clusters one package's questions whose token sets overlap
// by enrichSimilar or more, or that name the same two or more identifiers. The
// longest wording of a cluster leads (the earliest on a tie).
func dedupeQuestions(items []enrichedQuestion) []enrichedQuestion {
	n := len(items)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	toks := make([]map[string]bool, n)
	ids := make([]string, n)
	for i, it := range items {
		toks[i] = questionTokens(it.Q.Question)
		if idl := questionIdentifiers(it.Q.Question); len(idl) >= 2 {
			sort.Strings(idl)
			ids[i] = strings.Join(idl, "|")
		}
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if jaccard(toks[i], toks[j]) >= enrichSimilar || (ids[i] != "" && ids[i] == ids[j]) {
				if a, b := find(i), find(j); a != b {
					parent[max(a, b)] = min(a, b)
				}
			}
		}
	}
	members := map[int][]int{}
	var roots []int
	for i := range items {
		r := find(i)
		if _, ok := members[r]; !ok {
			roots = append(roots, r)
		}
		members[r] = append(members[r], i)
	}
	var out []enrichedQuestion
	for _, r := range roots {
		lead := members[r][0]
		for _, i := range members[r] {
			if len(items[i].Q.Question) > len(items[lead].Q.Question) {
				lead = i
			}
		}
		it := items[lead]
		for _, i := range members[r] {
			if i != lead {
				it.Merged = append(it.Merged, strings.Join(strings.Fields(items[i].Q.Question), " "))
			}
		}
		out = append(out, it)
	}
	return out
}

// enrichGate rewrites the question cards of a gate run in dir and writes
// questions/INDEX.md. It reads the questions from gate.json, so a rerun
// starts from the same input. code is the checkout to search, src the spec
// text and rel its repo-relative path.
func enrichGate(dir, code, src, rel string, sp *spec) (*enrichResult, error) {
	b, err := os.ReadFile(filepath.Join(dir, "gate.json"))
	if err != nil {
		return nil, fmt.Errorf("no gate.json in %s: run `orch gate` first", dir)
	}
	var rec gateRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("gate.json: %v", err)
	}
	res := &enrichResult{}
	lines := specLines(src)
	may := mayDecidePhrases(sp)

	// Card numbers follow the order of the blocking questions, as writeGate numbers them.
	byPkg := map[string][]enrichedQuestion{}
	var pkgs []string
	var minor []enrichedQuestion
	n := 0
	for _, q := range rec.Questions {
		if q.blocks() {
			n++
			if _, ok := byPkg[q.Package]; !ok {
				pkgs = append(pkgs, q.Package)
			}
			byPkg[q.Package] = append(byPkg[q.Package], enrichedQuestion{N: n, Q: q, Blocker: true})
		} else if q.AnsweredBy == "" {
			minor = append(minor, enrichedQuestion{Q: q})
		}
	}
	res.Total = n
	var items []enrichedQuestion
	for _, p := range pkgs {
		items = append(items, dedupeQuestions(byPkg[p])...)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].N < items[j].N })
	res.Cards = len(items)
	for _, set := range [][]enrichedQuestion{items, minor} {
		for i := range set {
			it := &set[i]
			it.Tag, it.Why = routeQuestion(it.Q.Question, may)
			it.Spec = specHints(lines, questionIdentifiers(it.Q.Question))
		}
	}
	for i := range items {
		items[i].Card = cardKey(items[i].N, items[i].Q.Package)
	}

	// Code search: one jbcontext call per card, a few at a time.
	cfg, _ := loadConfig(code)
	env := newCtxEnv(code, ctxOpts{Cfg: cfg, Skip: []string{rel}})
	if why := env.jbMissing(); why != "" {
		if env.NoJB {
			res.Notes = append(res.Notes, why)
		} else {
			res.Notes = append(res.Notes, "no code hints: "+env.JB+" was not found")
		}
	} else {
		var mu sync.Mutex
		fails := 0
		sem := make(chan struct{}, enrichParallel)
		var wg sync.WaitGroup
		for i := range items {
			wg.Add(1)
			sem <- struct{}{}
			go func(it *enrichedQuestion) {
				defer wg.Done()
				defer func() { <-sem }()
				mu.Lock()
				stop := fails >= enrichMaxFails
				mu.Unlock()
				if stop {
					return
				}
				hits, err := env.Concept(short(strings.Join(strings.Fields(it.Q.Question), " "), enrichQueryChars), "", 3)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if fails++; fails == 1 {
						res.Notes = append(res.Notes, "no code hints for some questions: "+err.Error())
					}
					return
				}
				for _, h := range hits {
					if len(it.Code) < enrichMaxHints {
						it.Code = append(it.Code, h)
					}
				}
			}(&items[i])
		}
		wg.Wait()
		if fails >= enrichMaxFails {
			res.Notes = append(res.Notes, fmt.Sprintf("code search stopped after %d failures", fails))
		}
		res.Notes = append(res.Notes, env.Notes()...)
	}
	var note string
	res.SymbolFacts, note = addSymbolFacts(items, env.CacheDir, code)
	if note != "" {
		res.Notes = append(res.Notes, note)
	}
	nTS, note := addTSFacts(items, env.CacheDir, code, sp)
	res.SymbolFacts += nTS
	if note != "" {
		res.Notes = append(res.Notes, note)
	}
	for _, it := range items {
		switch it.Tag {
		case "owner":
			res.Owner++
		case "lead-may-decide":
			res.Lead++
		default:
			res.Unclear++
		}
		if len(it.Spec) > 0 {
			res.SpecHints++
		}
		if len(it.Code) > 0 {
			res.CodeHits++
		}
	}
	res.Items, res.Minor = items, minor

	qdir := filepath.Join(dir, "questions")
	_ = os.RemoveAll(qdir)
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return nil, err
	}
	for _, it := range items {
		if err := os.WriteFile(filepath.Join(qdir, it.Card+".md"), []byte(enrichedCard(it, rel)), 0o644); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(qdir, "INDEX.md"), []byte(enrichedIndex(res, rel)), 0o644); err != nil {
		return nil, err
	}
	if err := addReportSection(filepath.Join(dir, "report.md"), res); err != nil {
		return nil, err
	}
	return res, nil
}

func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func enrichedCard(it enrichedQuestion, rel string) string {
	var b strings.Builder
	q := flat(it.Q.Question)
	if len(it.Merged) > 0 {
		q += fmt.Sprintf(" (+%d similar)", len(it.Merged))
	}
	fmt.Fprintf(&b, "---\nquestion: %s\n---\n## Context\nRaised by the spec gate for package `%s`. No file in the repo answers it.\n", q, it.Q.Package)
	fmt.Fprintf(&b, "\n## Tag\n%s (%s). This is advice; the card is yours to settle.\n", it.Tag, it.Why)
	if len(it.Merged) > 0 {
		b.WriteString("\n## Merged similar questions\n")
		for _, m := range it.Merged {
			fmt.Fprintf(&b, "- %s\n", m)
		}
	}
	if len(it.Spec) > 0 {
		b.WriteString("\n## The spec may already answer this\nA hint only; read the lines before relying on them.\n")
		for _, h := range it.Spec {
			fmt.Fprintf(&b, "- `%s:%d`: %s\n", rel, h.Line, h.Text)
		}
	}
	if len(it.Facts) > 0 {
		b.WriteString("\n## Exact symbol facts\n")
		for _, l := range factLines(it.Facts) {
			b.WriteString(l + "\n")
		}
	}
	if len(it.Code) > 0 {
		b.WriteString("\n## Possibly relevant code\n")
		for _, h := range it.Code {
			fmt.Fprintf(&b, "- `%s:%d-%d`\n\n```\n%s\n```\n", h.Path, h.Start, h.End, strings.Join(h.Snippet, "\n"))
		}
	}
	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "-"
}

func cell(s string) string { return strings.ReplaceAll(s, "|", "/") }

func enrichedIndex(res *enrichResult, rel string) string {
	var b strings.Builder
	b.WriteString("# Gate questions\n\n")
	fmt.Fprintf(&b, "%s\n\n", res.summary())
	for _, n := range res.Notes {
		fmt.Fprintf(&b, "Note: %s\n\n", n)
	}
	var pkgs []string
	byPkg := map[string][]enrichedQuestion{}
	for _, it := range res.Items {
		if _, ok := byPkg[it.Q.Package]; !ok {
			pkgs = append(pkgs, it.Q.Package)
		}
		byPkg[it.Q.Package] = append(byPkg[it.Q.Package], it)
	}
	rank := map[string]int{"owner": 0, "unclear": 1, "lead-may-decide": 2}
	for _, p := range pkgs {
		set := byPkg[p]
		sort.SliceStable(set, func(i, j int) bool { return rank[set[i].Tag] < rank[set[j].Tag] })
		fmt.Fprintf(&b, "## %s\n\n| card | tag | question | spec hint | code hits | go facts |\n|---|---|---|---|---|---|\n", p)
		for _, it := range set {
			q := short(flat(it.Q.Question), 100)
			if len(it.Merged) > 0 {
				q += fmt.Sprintf(" (+%d similar)", len(it.Merged))
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", it.Card, it.Tag, cell(q), yesNo(len(it.Spec) > 0), yesNo(len(it.Code) > 0), yesNo(len(it.Facts) > 0))
		}
		b.WriteString("\n")
	}
	if len(res.Minor) > 0 {
		fmt.Fprintf(&b, "## Minor open questions (not blocking, no cards)\n\n")
		for _, it := range res.Minor {
			fmt.Fprintf(&b, "- %s [%s]: %s\n", it.Q.Package, it.Tag, short(flat(it.Q.Question), 140))
		}
	}
	return b.String()
}

// addReportSection puts the question summary in report.md, replacing an
// earlier one so a rerun does not stack them.
func addReportSection(path string, res *enrichResult) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(b)
	if i := strings.Index(text, "## Question cards\n"); i >= 0 {
		text = strings.TrimRight(text[:i], "\n") + "\n\n"
	}
	text += "## Question cards\n" + res.summary() + "\n"
	for _, n := range res.Notes {
		text += "Note: " + n + "\n"
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
