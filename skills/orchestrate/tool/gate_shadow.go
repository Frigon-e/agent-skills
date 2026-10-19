package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Shadow judges of the reuse checklist. They read each item's top candidates
// and real implementations, with author hints added only for these judges.
// Their answers are logged and compared, and never change what the gate decides.
//
//	decider  one decisions-API request per item (the Perplexity decider)
//	flash    one tool-less pi pass per package, all its items in one reply
//
// Rows go to <gatedir>/shadow/<experiment or "run">/items.jsonl.

const (
	shadowStateDefault = 48000 // characters of decider state when no limit was measured
	shadowBodyLines    = 80
	shadowTimeout      = 90 * time.Second
	shadowParallel     = 4
)

// shadowMode resolves --shadow over reuse_shadow: off, decider, flash or both.
// A comma list such as "decider,flash" means both.
func shadowMode(flagVal, cfgVal string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(flagVal))
	if v == "" {
		v = strings.ToLower(strings.TrimSpace(cfgVal))
	}
	if v == "" {
		return "off", nil
	}
	dec, fl := false, false
	for _, w := range strings.Split(v, ",") {
		switch strings.TrimSpace(w) {
		case "off", "none":
		case "decider":
			dec = true
		case "flash":
			fl = true
		case "both":
			dec, fl = true, true
		default:
			return "", fail("shadow judges are off, decider, flash or both, not %q", w)
		}
	}
	switch {
	case dec && fl:
		return "both", nil
	case dec:
		return "decider", nil
	case fl:
		return "flash", nil
	}
	return "off", nil
}

// shadowRow is one judge's answer for one item.
type shadowRow struct {
	Item       string             `json:"item"`
	Package    string             `json:"package"`
	Judge      string             `json:"judge"` // decider or flash
	Verdict    string             `json:"verdict,omitempty"`
	Answers    map[string]string  `json:"answers,omitempty"`
	Confidence map[string]float64 `json:"confidence,omitempty"`
	Best       string             `json:"best_candidate,omitempty"`
	At         string             `json:"best_at,omitempty"`
	How        string             `json:"how_implemented,omitempty"`
	Seconds    float64            `json:"seconds"`
	Tokens     int                `json:"tokens,omitempty"`
	Cost       float64            `json:"cost_usd,omitempty"`
	CostKnown  bool               `json:"cost_known,omitempty"`
	StateChars int                `json:"state_chars,omitempty"`
	Cut        string             `json:"cut,omitempty"`
	Model      string             `json:"model,omitempty"`
	Skipped    string             `json:"skipped,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// judgeVerdict maps shadow classes and legacy answers to internal verdicts.
func judgeVerdict(wd string) string {
	switch strings.ToLower(strings.TrimSpace(wd)) {
	case "duplicate", "yes", "reuse":
		return verdictReuse
	case "variation_site", "partly", "extend":
		return verdictExtend
	case "none", "no", "new":
		return verdictNew
	}
	return ""
}

func shadowDir(gdir, name string) string {
	if name == "" {
		name = "run"
	}
	return filepath.Join(gdir, "shadow", name)
}

// shadowLimit is the largest decider state in characters: ORCH_SHADOW_STATE_MAX,
// else the limit `orch gate probe-decider` measured, else a conservative default.
func shadowLimit(root string) (int, string) {
	if v, err := strconv.Atoi(os.Getenv("ORCH_SHADOW_STATE_MAX")); err == nil && v > 0 {
		return v, "ORCH_SHADOW_STATE_MAX"
	}
	if b, err := os.ReadFile(filepath.Join(root, "gate", "decider-limit.json")); err == nil {
		var l struct {
			Max int `json:"max_state_chars"`
		}
		if json.Unmarshal(b, &l) == nil && l.Max > 0 {
			return l.Max * 9 / 10, "measured " + strconv.Itoa(l.Max)
		}
	}
	return shadowStateDefault, "default (no limit measured; run `orch gate probe-decider`)"
}

var deciderQuestionOrder = []string{"trigger_and_inputs", "outputs_and_effects", "relation", "best_candidate", "placement"}

func deciderQuestions(n int, cands ...reuseCand) map[string]any {
	best := map[string]any{"none": "no candidate decides it"}
	for i := 1; i <= n; i++ {
		text := fmt.Sprintf("candidate %d", i)
		if i <= len(cands) && cands[i-1].Hint {
			text += " (author hint)"
		}
		best[strconv.Itoa(i)] = text
	}
	return map[string]any{
		"trigger_and_inputs": map[string]any{"type": "choice", "instructions": "Is the best candidate triggered by, and fed, the same things the item describes? Judge from the code shown, not from names.",
			"criteria": map[string]any{"same": "same", "different": "different", "cannot_tell": "cannot tell from this evidence"}},
		"outputs_and_effects": map[string]any{"type": "choice", "instructions": "Does it produce the same results and side effects (writes, events, calls)?",
			"criteria": map[string]any{"same": "same", "partly": "partly", "different": "different", "cannot_tell": "cannot tell from this evidence"}},
		"relation": map[string]any{"type": "choice", "instructions": "Compare the item with the best-matching candidate by what the code does (trigger, inputs, outputs, side effects). Names and file locations are not evidence.",
			"criteria": map[string]any{
				"duplicate":      "a candidate already does this same job: same trigger and inputs, same outputs and side effects. Building the item would be a second implementation.",
				"variation_site": "a candidate does something close but not the same, and the item would be a small variation of it, would live next to it, or would call it.",
				"none":           "no candidate changes how the item is built. Generic infrastructure (router, logger, HTTP client) does not count. This is a normal answer."}},
		"best_candidate": map[string]any{"type": "choice", "instructions": "The candidate that decides your answer to `relation`; none if relation is none.", "criteria": best},
		"placement": map[string]any{"type": "choice", "instructions": "If the item is built, where should its code live, given the best candidate? existing_location = inside or next to the best candidate's file or function; new_location = new code of its own.",
			"criteria": map[string]any{"existing_location": "inside or next to the best candidate's file or function", "new_location": "new code of its own", "unknown": "cannot tell from this evidence"}},
	}
}

// deriveDuplicate is diagnostic only: it never overrides the judge's class.
func deriveDuplicate(answers map[string]string) {
	a, b := answers["trigger_and_inputs"], answers["outputs_and_effects"]
	answers["derived"] = "not_duplicate"
	if a == "cannot_tell" && b == "cannot_tell" {
		answers["derived"] = ""
	} else if a == "same" && b == "same" {
		answers["derived"] = "duplicate"
	}
	answers["consistent"] = "no"
	if (answers["derived"] == "duplicate") == (answers["relation"] == "duplicate") {
		answers["consistent"] = "yes"
	}
}

func bestAt(best string, cands []reuseCand) string {
	if n, err := strconv.Atoi(best); err == nil && n > 0 && n <= len(cands) {
		return cands[n-1].at()
	}
	return ""
}

// candidates copies before merging so the dossier and group pass stay unchanged.
func (s *shadowRun) candidates(ic itemCands) itemCands {
	ic.Cands = append([]reuseCand{}, ic.Cands...)
	if s.dos.e != nil {
		for _, c := range s.dos.e.hintCandidates(ic.Item.Reuse) {
			found := false
			for i := range ic.Cands {
				if ic.Cands[i].at() == c.at() {
					ic.Cands[i].Hint, found = true, true
					break
				}
			}
			if !found {
				ic.Cands = append(ic.Cands, c)
			}
		}
	}
	ic.Cands = ic.Cands[:min(6, len(ic.Cands))]
	return ic
}

// shadowState builds the decider's state for one item: the item, the package's
// purpose and the real implementation of each candidate. The state is cut to fit
// max characters (bodies first, then the last candidates) and the cut is named.
func shadowState(ic itemCands, purpose string, max int) (map[string]any, int, string) {
	build := func(nlines, ncands int) map[string]any {
		var cs []map[string]any
		for i, c := range ic.Cands[:min(ncands, len(ic.Cands))] {
			ls := c.Lines[:min(nlines, len(c.Lines))]
			cs = append(cs, map[string]any{"index": i + 1, "path": c.Path, "range": c.at(), "signature": c.Sig, "doc": c.Doc, "callers": callersText(c),
				"implementation": strings.Join(ls, "\n"), "implementation_lines": len(ls), "total_lines": c.Total, "author_hint": c.Hint})
		}
		return map[string]any{"item": ic.Item.Text, "item_id": ic.Item.ID, "package_purpose": purpose, "candidates": cs}
	}
	size := func(m map[string]any) int { b, _ := json.Marshal(m); return len(b) }
	for _, nc := range []int{len(ic.Cands), 2, 1} {
		if nc > len(ic.Cands) {
			continue
		}
		for _, nl := range []int{shadowBodyLines, 40, 15, 0} {
			st := build(nl, nc)
			if n := size(st); n <= max {
				var cut []string
				if nc < len(ic.Cands) {
					cut = append(cut, fmt.Sprintf("%d candidate(s) dropped", len(ic.Cands)-nc))
				}
				if nl < shadowBodyLines {
					cut = append(cut, fmt.Sprintf("bodies cut to %d lines", nl))
				}
				return st, n, strings.Join(cut, ", ")
			}
		}
	}
	st := build(0, 1)
	return st, size(st), "state over the limit even without bodies"
}

type shadowUsage struct {
	Cost   float64 `json:"cost"`
	Tokens int     `json:"total_tokens"`
}

// postDecisions posts one decisions request with typed questions.
func postDecisions(model string, state, questions map[string]any) (map[string]decAnswer, string, shadowUsage, error) {
	return postDecisionsWith(profile{Harness: "api", Provider: "openrouter", Model: model}, state, questions)
}

func postDecisionsWith(p profile, state, questions map[string]any) (map[string]decAnswer, string, shadowUsage, error) {
	var u shadowUsage
	key, err := providerKey(p.Provider)
	if err != nil {
		return nil, "", u, err
	}
	body := decisionBody(p.Model, state, questions)
	endpoint := envOr("ORCH_TRIAGE_URL", triageURLDefault)
	if p.Provider != "openrouter" {
		endpoint = os.Getenv("ORCH_" + strings.ToUpper(strings.ReplaceAll(p.Provider, "-", "_")) + "_DECISIONS_URL")
		if endpoint == "" {
			return nil, "", u, fmt.Errorf("%s decisions connector has no endpoint", p.Provider)
		}
	}
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", u, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: shadowTimeout}).Do(req)
	if err != nil {
		return nil, "", u, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, "", u, fmt.Errorf("decisions API returned %d: %s", resp.StatusCode, short(strings.TrimSpace(string(raw)), 200))
	}
	var r struct {
		Model   string               `json:"model"`
		Answers map[string]decAnswer `json:"answers"`
		Usage   shadowUsage          `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", u, fmt.Errorf("decisions API reply is not JSON: %v", err)
	}
	return r.Answers, r.Model, r.Usage, nil
}

// orderedDeciderQuestions preserves the behavioral checks before the class on the wire.
// Other decisions callers retain their existing question maps.
type orderedDeciderQuestions map[string]any

func (qs orderedDeciderQuestions) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range deciderQuestionOrder {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(key)
		v, err := json.Marshal(qs[key])
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func decisionBody(model string, state, questions map[string]any) []byte {
	var qs any = questions
	if _, ok := questions["trigger_and_inputs"]; ok {
		qs = orderedDeciderQuestions(questions)
	}
	b, _ := json.Marshal(map[string]any{"model": model, "state": state, "questions": qs})
	return b
}

// shadowRun is everything the judges need.
type shadowRun struct {
	dir     string // the gate dir (shadow/<name> goes under it)
	name    string // experiment name or ""
	root    string // the orchestrate state root
	code    string
	model   string // decider model
	flash   string // pi model of the flash judge
	cache   string
	mode    string
	dos     *dossierSet
	sp      *spec
	post    func(model string, state, qs map[string]any) (map[string]decAnswer, string, shadowUsage, error)
	runPass func(p gatePass, sessionDir string) (string, error)
}

func (s *shadowRun) purpose(id string) string {
	for _, p := range s.sp.packages {
		if p.id == id {
			return short(p.title+": "+flat(p.body), 600)
		}
	}
	return ""
}

// run executes the judges and writes items.jsonl; it returns the rows and a
// one-line summary. Failures are rows with an error, never a failed gate.
func (s *shadowRun) run() ([]shadowRow, string) {
	var rows []shadowRow
	var mu sync.Mutex
	add := func(r shadowRow) { mu.Lock(); rows = append(rows, r); mu.Unlock() }
	var ids []string
	for _, p := range s.sp.packages {
		if len(s.dos.cands[p.id]) > 0 {
			ids = append(ids, p.id)
		}
	}
	limit, _ := shadowLimit(s.root)
	var wg sync.WaitGroup
	if s.mode == "decider" || s.mode == "both" {
		sem := make(chan struct{}, shadowParallel)
		for _, id := range ids {
			for _, ic := range s.dos.cands[id] {
				wg.Add(1)
				go func(id string, ic itemCands) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					add(s.decide(id, ic, limit))
				}(id, ic)
			}
		}
	}
	if s.mode == "flash" || s.mode == "both" {
		for _, id := range ids {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				for _, r := range s.flashPackage(id) {
					add(r)
				}
			}(id)
		}
	}
	wg.Wait()
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Judge != rows[j].Judge {
			return rows[i].Judge < rows[j].Judge
		}
		return rows[i].Item < rows[j].Item
	})
	dir := shadowDir(s.dir, s.name)
	_ = os.MkdirAll(dir, 0o755)
	var b bytes.Buffer
	for _, r := range rows {
		j, _ := json.Marshal(r)
		b.Write(append(j, '\n'))
	}
	_ = os.WriteFile(filepath.Join(dir, "items.jsonl"), b.Bytes(), 0o644)
	return rows, shadowSummary(s.mode, rows)
}

func shadowSummary(mode string, rows []shadowRow) string {
	n := map[string]int{}
	cost := map[string]float64{}
	secs := map[string]float64{}
	errs := 0
	for _, r := range rows {
		n[r.Judge]++
		cost[r.Judge] += r.Cost
		secs[r.Judge] += r.Seconds
		if r.Error != "" {
			errs++
		}
	}
	return fmt.Sprintf("shadow %s: decider %d rows ($%.4f, %.0fs), flash %d rows ($%.4f, %.0fs), %d errors; record only", mode, n["decider"], cost["decider"], secs["decider"], n["flash"], cost["flash"], secs["flash"], errs)
}

func (s *shadowRun) decide(pkg string, ic itemCands, limit int) shadowRow {
	ic = s.candidates(ic)
	row := shadowRow{Item: ic.Item.ID, Package: pkg, Judge: "decider", Model: s.model}
	if len(ic.Cands) == 0 {
		row.Skipped = "no candidates"
		return row
	}
	st, n, cut := shadowState(ic, s.purpose(pkg), limit)
	row.StateChars, row.Cut = n, cut
	start := time.Now()
	ans, served, usage, err := s.post(s.model, st, deciderQuestions(len(ic.Cands), ic.Cands...))
	row.Seconds = time.Since(start).Seconds()
	if served != "" {
		row.Model = served
	}
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Tokens, row.Cost, row.CostKnown = usage.Tokens, usage.Cost, usage.Cost > 0
	row.Answers, row.Confidence = map[string]string{}, map[string]float64{}
	for k, a := range ans {
		row.Answers[k] = a.Choice
		row.Confidence[k] = a.Confidence
	}
	row.Best = row.Answers["best_candidate"]
	row.At = bestAt(row.Best, ic.Cands)
	row.Verdict = judgeVerdict(row.Answers["relation"])
	if row.Verdict == "" {
		row.Verdict = judgeVerdict(row.Answers["would_duplicate"])
	}
	deriveDuplicate(row.Answers)
	if row.Answers["how_implemented"] == "" {
		row.How = "skipped: the decisions API types orch knows are choice, score and noul"
	}
	return row
}

const flashJudgeHead = `You decide whether new work duplicates existing code. You have NO tools: use only the evidence below. Names and file locations are not evidence; judge by what the code shown does. If the lines shown do not let you see the behaviour, say so and answer the weaker class. For each item, in this order: say what the best candidate does (one sentence); whether trigger and inputs match; whether outputs and side effects match; what the item needs that the candidate lacks; then the class. Classes: duplicate = the same job, so building it again would be a second implementation (needs both matches). variation_site = close but not the same; the item would be a small variation of it, sit next to it, or call it. none = nothing existing changes how this is built (generic infrastructure such as a router, a logger or an HTTP client does not count); none is a normal answer, do not stretch. Reply with ONLY one JSON object: {"items":[{"item":"<id>","best_candidate":"1|2|..|none","candidate_does":"","trigger_inputs":"same|different|cannot_tell","outputs_effects":"same|partly|different|cannot_tell","lacks":"","class":"duplicate|variation_site|none","placement":"existing_location|new_location|unknown","confidence":0.0}]}.`

const flashJudgeExamples = `Worked example 1: duplicate
Item e#1: On bell press, save the bell ID and emit rang.
Candidate 1:
onBellPress((id: string) => {
  const row = {
    id,
  };
  bells.save(row);
  events.emit("rang", id);
});
{"items":[{"item":"e#1","best_candidate":"1","candidate_does":"Saves the pressed bell and emits rang.","trigger_inputs":"same","outputs_effects":"same","lacks":"nothing","class":"duplicate","placement":"existing_location","confidence":0.9}]}

Worked example 2: variation_site
Item e#2: On bell press, save its ID, emit rang, and log its colour.
Candidate 1:
onBellPress((id: string) => {
  const row = {
    id,
  };
  bells.save(row);
  events.emit("rang", id);
});
{"items":[{"item":"e#2","best_candidate":"1","candidate_does":"Saves the pressed bell and emits rang.","trigger_inputs":"same","outputs_effects":"partly","lacks":"colour logging","class":"variation_site","placement":"existing_location","confidence":0.9}]}

Worked example 3: none
Item e#3: Each dawn, prune expired lantern leases.
Candidate 1:
function fetchText(url: string) {
  const headers = {Accept: "text/plain"};
  return fetch(url, {headers})
    .then(r => r.text())
    .then(text => text.trim());
}
{"items":[{"item":"e#3","best_candidate":"none","candidate_does":"Fetches trimmed text over HTTP.","trigger_inputs":"different","outputs_effects":"different","lacks":"lease expiry and deletion","class":"none","placement":"new_location","confidence":0.9}]}`

// flashEvidence is the text of one item with its candidates.
func flashEvidence(ic itemCands) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Item %s\n%s\n", ic.Item.ID, ic.Item.Text)
	for i, c := range ic.Cands {
		label := ""
		if c.Hint {
			label = " (author hint)"
		}
		fmt.Fprintf(&b, "Candidate %d%s: %s %s callers %s\nsignature: %s\ndoc: %s\n", i+1, label, c.at(), c.In, callersText(c), c.Sig, c.Doc)
		ls := c.Lines[:min(shadowBodyLines, len(c.Lines))]
		b.WriteString(strings.Join(ls, "\n") + "\n")
		if c.Total > len(ls) {
			fmt.Fprintf(&b, "(%d more lines)\n", c.Total-len(ls))
		}
	}
	if len(ic.Cands) == 0 {
		b.WriteString("(no candidates)\n")
	}
	return b.String()
}

func (s *shadowRun) flashPackage(pkg string) []shadowRow {
	ics := s.dos.cands[pkg]
	var rows []shadowRow
	var with []itemCands
	for _, ic := range ics {
		ic = s.candidates(ic)
		if len(ic.Cands) == 0 {
			rows = append(rows, shadowRow{Item: ic.Item.ID, Package: pkg, Judge: "flash", Skipped: "no candidates"})
			continue
		}
		with = append(with, ic)
	}
	if len(with) == 0 {
		return rows
	}
	var ev strings.Builder
	for _, ic := range with {
		ev.WriteString(flashEvidence(ic) + "\n")
	}
	prompt := flashJudgeHead + "\n\n" + flashJudgeExamples + "\n\nPackage " + pkg + ": " + s.purpose(pkg) + "\n\n" + ev.String()
	p := gatePass{name: "shadow-" + pkg, class: "shadow", prompt: prompt, noTools: true}
	sess := filepath.Join(s.dir, "sessions", passFile(p.name))
	start := time.Now()
	out, err := s.runPass(p, sess)
	secs := time.Since(start).Seconds()
	tok, have := sessionTokens(sess)
	per := float64(len(with))
	base := func(ic itemCands) shadowRow {
		r := shadowRow{Item: ic.Item.ID, Package: pkg, Judge: "flash", Model: s.flash, Seconds: secs / per, StateChars: len(prompt) / len(with)}
		if have {
			r.Tokens = (tok.Input + tok.CacheRead + tok.CacheWrite + tok.Output) / len(with)
			r.Cost, r.CostKnown = tok.Cost/per, tok.NoCost == 0
		}
		return r
	}
	var reply struct {
		Items []struct {
			Item           string  `json:"item"`
			CandidateDoes  string  `json:"candidate_does"`
			TriggerInputs  string  `json:"trigger_inputs"`
			OutputsEffects string  `json:"outputs_effects"`
			Lacks          string  `json:"lacks"`
			Class          string  `json:"class"`
			Placement      string  `json:"placement"`
			How            string  `json:"how_implemented"`
			Similar        string  `json:"similar_implementation"`
			WouldDuplicate string  `json:"would_duplicate"`
			Best           flexStr `json:"best_candidate"`
			Where          string  `json:"where_to_implement"`
			Confidence     float64 `json:"confidence"`
		} `json:"items"`
	}
	if err == nil {
		err = extractJSON(out, &reply)
	}
	got := map[string]int{}
	for i, it := range reply.Items {
		got[itemKey(pkg, it.Item)] = i
	}
	for _, ic := range with {
		r := base(ic)
		i, ok := got[ic.Item.ID]
		switch {
		case err != nil:
			r.Error = err.Error()
		case !ok:
			r.Error = "no answer for this item in the reply"
		default:
			it := reply.Items[i]
			r.Answers = map[string]string{"similar_implementation": it.Similar, "would_duplicate": it.WouldDuplicate, "best_candidate": string(it.Best), "where_to_implement": it.Where}
			r.Confidence = map[string]float64{"overall": it.Confidence}
			r.Best = string(it.Best)
			r.At = bestAt(r.Best, ic.Cands)
			r.Answers["trigger_and_inputs"] = it.TriggerInputs
			r.Answers["outputs_and_effects"] = it.OutputsEffects
			r.Answers["relation"] = it.Class
			r.Answers["placement"] = orDefault(it.Placement, it.Where)
			r.Answers["lacks"] = it.Lacks
			r.How = orDefault(it.CandidateDoes, it.How)
			r.Verdict = judgeVerdict(orDefault(it.Class, it.WouldDuplicate))
			deriveDuplicate(r.Answers)
		}
		rows = append(rows, r)
	}
	if err == nil {
		dropPassFiles(s.dir, p.name, false)
	}
	return rows
}

// probeDecider finds the largest state the decisions API accepts by posting
// states of growing size with one trivial question, and records it. It makes
// real, paid requests: it only runs from `orch gate probe-decider`.
func probeDecider(root, model string, post func(string, map[string]any, map[string]any) (map[string]decAnswer, string, shadowUsage, error)) (int, error) {
	q := map[string]any{"ok": map[string]any{"type": "noul", "instructions": "Is the state non-empty?", "criteria": map[string]any{"true": "yes", "false": "no"}}}
	best := 0
	for _, n := range []int{8000, 16000, 32000, 64000, 128000, 256000} {
		if _, _, _, err := post(model, map[string]any{"filler": strings.Repeat("x", n)}, q); err != nil {
			if best == 0 {
				return 0, err
			}
			break
		}
		best = n
	}
	b, _ := json.Marshal(map[string]any{"max_state_chars": best, "model": model, "at": time.Now().Unix()})
	_ = os.MkdirAll(filepath.Join(root, "gate"), 0o755)
	return best, os.WriteFile(filepath.Join(root, "gate", "decider-limit.json"), b, 0o644)
}

func cmdGateProbeDecider(a *app, args []string) error {
	if _, err := parseArgs(a.flags("gate"), args); err != nil {
		return err
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	runner, _ := s.newRoleRun("decider", modelOptions{})
	choices, _ := runner.available()
	if len(choices) == 0 {
		fmt.Fprintln(a.out, runner.unavailable())
		return nil
	}
	n, err := probeDecider(s.root, choices[0].Model, runner.postDecisions)
	if err != nil {
		return fail("probe: %v", err)
	}
	fmt.Fprintf(a.out, "decider accepts a state of at least %d characters; recorded in %s\n", n, filepath.Join(s.root, "gate", "decider-limit.json"))
	return nil
}

// deciderRequest is the request body for one item, for --dump-decider.
func deciderRequest(dos *dossierSet, sp *spec, model, id string, limit int) ([]byte, string) {
	s := &shadowRun{dos: dos, sp: sp}
	for _, p := range sp.packages {
		for _, ic := range dos.cands[p.id] {
			ic = s.candidates(ic)
			if len(ic.Cands) > 0 && (id == "first" || id == ic.Item.ID) {
				st, n, cut := shadowState(ic, s.purpose(p.id), limit)
				var b bytes.Buffer
				_ = json.Indent(&b, decisionBody(model, st, deciderQuestions(len(ic.Cands), ic.Cands...)), "", "  ")
				return b.Bytes(), fmt.Sprintf("item %s: state %d characters (limit %d)%s", ic.Item.ID, n, limit, map[bool]string{true: "; cut: " + cut, false: ""}[cut != ""])
			}
		}
	}
	return nil, ""
}
