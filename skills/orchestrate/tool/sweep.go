package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// `orch sweep <topic>` settles a pile of open questions without trusting any
// single model. Several passes (different models, fresh context each) answer
// every question card on their own, in files under <topic>/passes/<pass>/.
// This command compares them with code: an answer only counts when it picks
// one of the card's option ids and cites evidence that really exists; passes
// that agree settle a question, passes that differ go to a final pass, and
// questions in a human-only area always go to the user.
//
//	<topic>/questions/<key>.md          cards (see references/question-file.md)
//	<topic>/passes/<pass>/<key>.json    one answer per pass and question
//	<topic>/dropped.json                optional: questions collect dropped
//	<topic>/sweep/for-user/<key>.md     written here: cards for `orch ask`
type sweepAnswer struct {
	Choice     string   `json:"choice"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason"`
	Evidence   []string `json:"evidence"`
	Owner      string   `json:"owner"` // agent (default), user or an outside expert such as accountant
}

type sweepDropped struct {
	Question    string `json:"question"`
	Reason      string `json:"reason"`
	DuplicateOf string `json:"duplicate_of,omitempty"` // a card key
	AnsweredBy  string `json:"answered_by,omitempty"`  // path:line of the ADR or code
}

type sweepPass struct {
	name    string
	ans     sweepAnswer
	checked int // evidence lines that resolve
	urls    int
	bad     []string // evidence that does not resolve
}

// sweepEvidence counts what an answer's evidence is worth. roots maps a name
// to a directory outside the repo (a competitor's checkout); evidence reads
// "<name>/path:line" for those and "path:line" for the repo itself.
func sweepEvidence(repo string, roots map[string]string, ev []string) (ok, urls int, bad []string) {
	for _, e := range ev {
		e = strings.TrimSpace(e)
		switch {
		case strings.HasPrefix(e, "https://") || strings.HasPrefix(e, "http://"):
			urls++
			continue
		}
		base, rel := repo, e
		if name, rest, found := strings.Cut(e, "/"); found {
			if dir, isRoot := roots[name]; isRoot {
				base, rel = dir, rest
			}
		}
		if evidenceHolds(base, rel) {
			ok++
		} else {
			bad = append(bad, e)
		}
	}
	return ok, urls, bad
}

type sweepResult struct {
	key, question, status, why string
	passes                     []sweepPass
	card                       *card
	choice                     string
}

func cmdSweep(a *app, args []string) error {
	fs := a.flags("sweep")
	rootsFlag := fs.String("roots", "", "evidence outside the repo as name=dir[,name=dir]; cite it as name/path:line")
	minPasses := fs.Int("min-passes", 2, "answers needed before a question can settle")
	final := fs.String("final", "settle", "name of the pass that breaks a disagreement")
	acceptURLs := fs.Bool("accept-urls", false, "let an https link count as evidence (it cannot be checked by code)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *minPasses < 2 {
		return &exitError{code: 2, msg: "usage: orch sweep <topic-dir> [--roots name=dir,...] [--min-passes N>=2] [--final PASS] [--accept-urls]"}
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	topic := pos[0]
	roots := map[string]string{}
	for _, kv := range splitList(*rootsFlag) {
		name, dir, ok := strings.Cut(kv, "=")
		if !ok || name == "" || dir == "" {
			return fail("--roots %q must be name=dir", kv)
		}
		roots[name] = expandHome(dir)
	}
	cards, err := filepath.Glob(filepath.Join(topic, "questions", "*.md"))
	if err != nil || len(cards) == 0 {
		return fail("no cards in %s", filepath.Join(topic, "questions"))
	}
	sort.Strings(cards)
	passDirs, _ := filepath.Glob(filepath.Join(topic, "passes", "*"))
	humanOnly := map[string]bool{}
	for _, h := range s.cfg.HumanOnly {
		humanOnly[strings.ToLower(strings.TrimSpace(h))] = true
	}

	var results []sweepResult
	for _, path := range cards {
		key := strings.TrimSuffix(filepath.Base(path), ".md")
		src, _ := os.ReadFile(path)
		c, cerr := parseCard(string(src))
		r := sweepResult{key: key, card: c}
		if cerr != nil {
			r.status, r.why = "user", "the card does not parse ("+cerr.Error()+")"
			results = append(results, r)
			continue
		}
		r.question = c.question
		results = append(results, a.judgeQuestion(&r, s.repo, roots, topic, passDirs, humanOnly, *minPasses, *final, *acceptURLs))
	}

	forUser := filepath.Join(topic, "sweep", "for-user")
	_ = os.RemoveAll(forUser)
	groups := map[string][]sweepResult{}
	for _, r := range results {
		groups[r.status] = append(groups[r.status], r)
		if r.status == "user" {
			if err := writeUserCard(forUser, r, cards); err != nil {
				return err
			}
		}
	}
	order := []struct{ status, title string }{
		{"settled", "settled: the passes agree, with evidence that exists"},
		{"final", "settled by the final pass after a disagreement"},
		{"disagree", "disagree: run the final pass (a stronger model) on these"},
		{"recheck", "recheck: an answer is missing, invalid or has no real evidence"},
		{"user", "user: only a person decides these (written to sweep/for-user)"},
	}
	for _, g := range order {
		fmt.Fprintf(a.out, "%s[%d]:\n", g.title, len(groups[g.status]))
		for _, r := range groups[g.status] {
			line := r.key
			if r.choice != "" {
				line += " -> " + r.choice
			}
			if r.why != "" {
				line += " (" + r.why + ")"
			}
			fmt.Fprintf(a.out, "  %s\n", toonValue(line))
		}
	}
	for _, issue := range checkDropped(topic, cards, s.repo, roots, *acceptURLs) {
		fmt.Fprintf(a.out, "dropped: %s\n", issue)
	}
	if n := len(groups["user"]); n > 0 {
		fmt.Fprintf(a.out, "next: orch ask --cards %s\n", forUser)
	}
	return nil
}

func (a *app) judgeQuestion(r *sweepResult, repo string, roots map[string]string, topic string, passDirs []string, humanOnly map[string]bool, minPasses int, final string, acceptURLs bool) sweepResult {
	c := r.card
	options := map[string]bool{}
	for _, o := range c.options {
		options[o.id] = true
	}
	var invalid []string
	for _, d := range passDirs {
		name := filepath.Base(d)
		b, err := os.ReadFile(filepath.Join(d, r.key+".json"))
		if err != nil {
			continue
		}
		var ans sweepAnswer
		if json.Unmarshal(b, &ans) != nil {
			invalid = append(invalid, name+": not JSON")
			continue
		}
		p := sweepPass{name: name, ans: ans}
		p.checked, p.urls, p.bad = sweepEvidence(repo, roots, ans.Evidence)
		r.passes = append(r.passes, p)
	}
	supported := func(p sweepPass) bool { return p.checked > 0 || (acceptURLs && p.urls > 0) }

	// A question the user alone decides never settles on a model's say-so.
	area := strings.ToLower(strings.TrimSpace(c.area))
	for _, p := range r.passes {
		if o := strings.ToLower(strings.TrimSpace(p.ans.Owner)); o != "" && o != "agent" {
			r.status, r.why = "user", fmt.Sprintf("pass %s says the owner is %s", p.name, o)
			return *r
		}
	}
	if humanOnly[area] {
		r.status, r.why = "user", "area "+area+" is human-only"
		return *r
	}
	if len(c.options) == 0 {
		r.status, r.why = "user", "a free-text question has no options to compare"
		return *r
	}

	var voting []sweepPass
	for _, p := range r.passes {
		if p.name == final {
			continue
		}
		switch {
		case !options[p.ans.Choice]:
			invalid = append(invalid, fmt.Sprintf("%s picked %q, which is not an option", p.name, p.ans.Choice))
		case len(p.bad) > 0:
			invalid = append(invalid, p.name+": evidence does not resolve: "+strings.Join(p.bad, ", "))
		case !supported(p):
			invalid = append(invalid, p.name+": no evidence")
		default:
			voting = append(voting, p)
		}
	}
	if len(invalid) > 0 || len(voting) < minPasses {
		r.status = "recheck"
		r.why = strings.Join(invalid, "; ")
		if r.why == "" {
			r.why = fmt.Sprintf("%d of %d passes answered", len(voting), minPasses)
		}
		return *r
	}
	agree := true
	for _, p := range voting {
		if p.ans.Choice != voting[0].ans.Choice {
			agree = false
		}
	}
	if agree {
		r.status, r.choice = "settled", voting[0].ans.Choice
		return *r
	}
	for _, p := range r.passes {
		if p.name == final && options[p.ans.Choice] && len(p.bad) == 0 && supported(p) {
			r.status, r.choice, r.why = "final", p.ans.Choice, "final pass "+final
			return *r
		}
	}
	var picks []string
	for _, p := range voting {
		picks = append(picks, p.name+"="+p.ans.Choice)
	}
	r.status, r.why = "disagree", strings.Join(picks, ", ")
	return *r
}

// oneLine makes model output safe to put in a card: one line, no backticks,
// so it can never start a heading or an include, or close a code span.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "'")), " ")
	return short(s, max)
}

// writeUserCard copies a card for `orch ask` and adds what each pass said, so
// the user sees the evidence and any disagreement next to the question.
func writeUserCard(dir string, r sweepResult, cardPaths []string) error {
	var src []byte
	for _, p := range cardPaths {
		if strings.TrimSuffix(filepath.Base(p), ".md") == r.key {
			src, _ = os.ReadFile(p)
		}
	}
	var b strings.Builder
	b.Write(src)
	b.WriteString("\n\n## Passes\n\n")
	fmt.Fprintf(&b, "Why this is yours: %s.\n\n", oneLine(r.why, 300))
	for _, p := range r.passes {
		fmt.Fprintf(&b, "- **%s** chose `%s` (confidence %.2f): %s\n", oneLine(p.name, 40), oneLine(p.ans.Choice, 40), p.ans.Confidence, oneLine(p.ans.Reason, 600))
		if len(p.ans.Evidence) > 0 {
			ev := make([]string, len(p.ans.Evidence))
			for i, e := range p.ans.Evidence {
				ev[i] = oneLine(e, 120)
			}
			fmt.Fprintf(&b, "  Evidence: %s\n", strings.Join(ev, ", "))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, r.key+".md"), []byte(b.String()), 0o644)
}

// checkDropped holds the collect step to account: a dropped question must
// name the card it duplicates, or the evidence that already answers it.
func checkDropped(topic string, cardPaths []string, repo string, roots map[string]string, acceptURLs bool) []string {
	b, err := os.ReadFile(filepath.Join(topic, "dropped.json"))
	if err != nil {
		return nil
	}
	var list []sweepDropped
	if json.Unmarshal(b, &list) != nil {
		return []string{"dropped.json is not a JSON list"}
	}
	keys := map[string]bool{}
	for _, p := range cardPaths {
		keys[strings.TrimSuffix(filepath.Base(p), ".md")] = true
	}
	var out []string
	for _, d := range list {
		switch {
		case d.DuplicateOf != "" && !keys[d.DuplicateOf]:
			out = append(out, fmt.Sprintf("%q is marked a duplicate of %q, which is not a card", short(d.Question, 50), d.DuplicateOf))
		case d.DuplicateOf == "" && d.AnsweredBy == "":
			out = append(out, fmt.Sprintf("%q was dropped with no duplicate_of or answered_by", short(d.Question, 50)))
		case d.AnsweredBy != "":
			if ok, urls, _ := sweepEvidence(repo, roots, []string{d.AnsweredBy}); ok == 0 && !(acceptURLs && urls > 0) {
				out = append(out, fmt.Sprintf("%q says %s answers it, but that does not resolve", short(d.Question, 50), d.AnsweredBy))
			}
		}
	}
	return out
}
