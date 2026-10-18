package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The gate checks a spec before any lead starts. Deterministic checks cover
// what code can prove (pointers, acceptance commands). Cheap read-only model
// passes (pi, default Flash) then probe scope, open questions and design
// flaws. A model claim counts only when its evidence (`path:line`) exists in
// the repo, so a verifier cannot block or pass a spec by assertion. The
// orchestrator and the user make the final call; the gate removes noise.
const (
	gateTimeout   = 8 * time.Minute
	gateModelDef  = "openrouter/~deepseek/deepseek-flash-latest"
	gateStatePass = "pass"
)

type gateFinding struct {
	Severity string `json:"severity"` // blocker or concern
	Where    string `json:"where"`
	Claim    string `json:"claim"`
	Evidence string `json:"evidence"`
	Why      string `json:"why,omitempty"`
	Source   string `json:"source"`
	Verified bool   `json:"verified"`
}

type gateQuestion struct {
	Package    string `json:"package"`
	Question   string `json:"question"`
	AnsweredBy string `json:"answered_by,omitempty"`
	// Decisive is true when two reasonable answers would give behaviour a
	// user or a test could tell apart. Only decisive questions block.
	Decisive bool `json:"changes_result"`
}

func (q gateQuestion) blocks() bool { return q.Decisive && q.AnsweredBy == "" }

type gateRecord struct {
	Spec          string            `json:"spec"`
	Hash          string            `json:"hash"`
	At            int64             `json:"at"`
	Status        string            `json:"status"` // pass, fail or unverified
	Deterministic []string          `json:"deterministic,omitempty"`
	Findings      []gateFinding     `json:"findings,omitempty"`
	Questions     []gateQuestion    `json:"questions,omitempty"`
	Dropped       int               `json:"dropped_unsupported_claims"`
	PassErrors    map[string]string `json:"pass_errors,omitempty"`
}

func specHash(src []byte) string {
	sum := sha256.Sum256(src)
	return fmt.Sprintf("%x", sum[:8])
}

func gateDir(root, hash string) string { return filepath.Join(root, "gate", hash) }

// gatePassed reports whether the gate passed for exactly this spec text.
func gatePassed(root string, src []byte) bool {
	b, err := os.ReadFile(filepath.Join(gateDir(root, specHash(src)), "gate.json"))
	if err != nil {
		return false
	}
	var r gateRecord
	return json.Unmarshal(b, &r) == nil && r.Status == gateStatePass
}

var (
	pointerRe  = regexp.MustCompile("([A-Za-z0-9_./-]+\\.[A-Za-z0-9]+):([0-9]+)")
	backtickRe = regexp.MustCompile("`([^`\n]+)`")
	srcExtRe   = regexp.MustCompile(`\.(go|ts|tsx|js|jsx|py|rs|php|rb|java|kt|swift|c|h|cpp|cs|sql|sh|md|toml|json|ya?ml|html|css|vue|svelte|tmpl)$`)
)

// evidenceHolds reports whether "path:line" names a real line in repo.
func evidenceHolds(repo, ev string) bool {
	m := pointerRe.FindStringSubmatch(strings.TrimSpace(ev))
	if m == nil {
		return false
	}
	path := filepath.Join(repo, filepath.FromSlash(m[1]))
	if rel, err := filepath.Rel(repo, path); err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	n, _ := strconv.Atoi(m[2])
	b, err := os.ReadFile(path)
	if err != nil || n < 1 {
		return false
	}
	return n <= bytes.Count(b, []byte("\n"))+1
}

// gateDeterministic is every check that needs no model.
func gateDeterministic(repo string, cfg config, sp *spec, src string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range pointerRe.FindAllStringSubmatch(src, -1) {
		path := m[1]
		if seen[m[0]] || !(strings.Contains(path, "/") || srcExtRe.MatchString(path)) || strings.Contains(path, "://") {
			continue
		}
		seen[m[0]] = true
		if !evidenceHolds(repo, m[0]) {
			out = append(out, fmt.Sprintf("pointer %s does not resolve to a line in the repo", m[0]))
		}
	}
	for _, p := range sp.packages {
		for _, m := range backtickRe.FindAllStringSubmatch(p.acceptance, -1) {
			first := strings.Fields(m[1])
			if len(first) == 0 {
				continue
			}
			cmd := first[0]
			if strings.Contains(cmd, "/") {
				if _, err := os.Stat(filepath.Join(repo, cmd)); err != nil {
					out = append(out, fmt.Sprintf("package %s: acceptance command %q is not in the repo", p.id, cmd))
				}
			} else if _, err := exec.LookPath(cmd); err != nil {
				out = append(out, fmt.Sprintf("package %s: acceptance command %q is not on PATH", p.id, cmd))
			}
		}
	}
	if cfg.Check == "" {
		out = append(out, "no `check` command in the repo config, so `orch verify` cannot pass any lead's work")
	}
	return out
}

const gateHeader = `You are a read-only verifier for a software spec. The repository is the current directory. Never edit anything. Open files with your tools before you claim anything about them. Cite evidence as "path:line" for a line you actually read. A claim without real evidence is discarded. Reply with ONLY one JSON object and no other text.

`

func gatePrompt(tag, task, schema, specText string) string {
	return gateHeader + tag + "\n" + task + "\nJSON schema: " + schema + "\n\nSPEC:\n" + specText
}

// runPi runs one headless read-only pi pass and returns its text.
func runPi(repo, model, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, envOr("ORCH_PI", "pi"), "-p", "--no-session", "--no-extensions", "--no-skills",
		"--no-context-files", "--tools", "read,grep,find,ls", "--model", model, prompt)
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("timed out after %s", gateTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("pi failed: %v: %s", err, short(strings.TrimSpace(stderr.String()), 200))
	}
	return string(out), nil
}

// extractJSON decodes the first JSON object in text into v.
func extractJSON(text string, v any) error {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return fmt.Errorf("no JSON object in the reply")
	}
	return json.Unmarshal([]byte(text[i:j+1]), v)
}

type gatePass struct {
	name, prompt string
	apply        func(raw string, rec *gateRecord, repo string, sp *spec) error
}

func cmdGate(a *app, args []string) error {
	pos, err := parseArgs(a.flags("gate"), args)
	if err != nil {
		return err
	}
	if err := exactArgs(pos, 1, "gate <spec>"); err != nil {
		return err
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	specPath := expandHome(pos[0])
	if !filepath.IsAbs(specPath) {
		specPath = filepath.Join(a.cwd, specPath)
	}
	srcB, err := os.ReadFile(specPath)
	if err != nil {
		return fail("spec %s: %v", pos[0], err)
	}
	src := string(srcB)
	sp := parseSpec(src)
	sp.validate()
	if n := len(sp.problems); n > 0 {
		fmt.Fprintf(a.out, "spec has %d format problem(s); run `orch plan --dry-run` and fix them first\n", n)
		return &exitError{code: 1}
	}
	hash := specHash(srcB)
	dir := gateDir(s.root, hash)
	rec := &gateRecord{Spec: pos[0], Hash: hash, At: a.now().Unix(), PassErrors: map[string]string{}}
	rec.Deterministic = gateDeterministic(s.repo, s.cfg, sp, src)

	model := envOr("ORCH_GATE_MODEL", gateModelDef)
	if err := s.cfg.modelAllowed("pi", model, a.now()); err != nil {
		return fail("the gate model: %v", err)
	}
	if e, limited := loadHealth(a.now())["pi"]; limited {
		rec.PassErrors["all"] = "pi is " + e.String() + ": the model passes did not run"
	} else {
		passes := []gatePass{
			{"scope", gatePrompt("GATE-SCOPE", `For each package say which files its work will really have to change, found by reading the code. Give claims about the code with evidence.`,
				`{"packages":[{"id":"","will_change":["path"],"claims":[{"claim":"","evidence":"path:line"}]}]}`, src), applyScope},
			{"questions", gatePrompt("GATE-QUESTIONS", `List the questions a developer would have to answer before building each package. Mark a question answered_by "path:line" when the code or the spec already answers it; leave answered_by empty when it needs a decision. Set changes_result to true ONLY if two reasonable answers would give behaviour that a user or a test could tell apart; style, naming, placement and other details a developer can settle alone are false.`,
				`{"questions":[{"package":"","question":"","answered_by":"","changes_result":false}]}`, src), applyQuestions},
			{"adversary-a", gatePrompt("GATE-ADVERSARY-A", `Attack the Design rules and the Authority section. Find a rule that the code contradicts, a failure case no package covers, or an unsafe default. Report only what you can prove.`,
				`{"findings":[{"severity":"blocker|concern","where":"","claim":"","evidence":"path:line","why":""}]}`, src), applyFindings("adversary-a")},
			{"adversary-b", gatePrompt("GATE-ADVERSARY-B", `Assume the packages are built exactly as written and the acceptance commands pass. What still breaks for a user, or between two packages? Report only what you can prove.`,
				`{"findings":[{"severity":"blocker|concern","where":"","claim":"","evidence":"path:line","why":""}]}`, src), applyFindings("adversary-b")},
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, p := range passes {
			wg.Add(1)
			go func(p gatePass) {
				defer wg.Done()
				out, err := runPi(s.repo, model, p.prompt)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					err = p.apply(out, rec, s.repo, sp)
				}
				if err != nil {
					rec.PassErrors[p.name] = err.Error()
				}
			}(p)
		}
		wg.Wait()
	}

	blockers, unanswered := 0, 0
	for _, f := range rec.Findings {
		if f.Verified && f.Severity == "blocker" {
			blockers++
		}
	}
	for _, q := range rec.Questions {
		if q.blocks() {
			unanswered++
		}
	}
	switch {
	case hasError(rec.Deterministic), blockers > 0, unanswered > 0:
		rec.Status = "fail"
	case len(rec.PassErrors) > 0:
		rec.Status = "unverified"
	default:
		rec.Status = gateStatePass
	}
	if err := writeGate(dir, rec); err != nil {
		return err
	}

	fmt.Fprintf(a.out, "gate: %s\nspec: %s (version %s)\nreport: %s\n", rec.Status, toonValue(pos[0]), hash, filepath.Join(dir, "report.md"))
	concerns, unverified := 0, 0
	for _, f := range rec.Findings {
		switch {
		case !f.Verified:
			unverified++
		case f.Severity == "concern":
			concerns++
		}
	}
	fmt.Fprintf(a.out, "deterministic: %d issue(s)\nblockers: %d verified\nconcerns: %d verified, %d unverified\nquestions: %d need a decision\ndropped: %d claim(s) without real evidence\n",
		len(rec.Deterministic), blockers, concerns, unverified, unanswered, rec.Dropped)
	for name, msg := range rec.PassErrors {
		fmt.Fprintf(a.out, "pass_failed: %s - %s\n", name, toonValue(msg))
	}
	if unanswered > 0 {
		fmt.Fprintf(a.out, "help[1]:\n  orch ask --cards %s\n", filepath.Join(dir, "questions"))
	}
	if rec.Status != gateStatePass {
		return &exitError{code: 1}
	}
	return nil
}

func hasError(det []string) bool {
	for _, d := range det {
		if !strings.HasPrefix(d, "no `check` command") {
			return true
		}
	}
	return false
}

func applyScope(raw string, rec *gateRecord, repo string, sp *spec) error {
	var r struct {
		Packages []struct {
			ID         string   `json:"id"`
			WillChange []string `json:"will_change"`
			Claims     []struct{ Claim, Evidence string }
		} `json:"packages"`
	}
	if err := extractJSON(raw, &r); err != nil {
		return err
	}
	owner := map[string]string{} // a path owned by exactly one package
	for _, p := range sp.packages {
		for _, o := range p.owns {
			owner[o] = p.id
		}
	}
	for _, rp := range r.Packages {
		var pkg *specPackage
		for _, p := range sp.packages {
			if p.id == rp.ID {
				pkg = p
			}
		}
		if pkg == nil {
			continue
		}
		for _, c := range rp.Claims {
			if !evidenceHolds(repo, c.Evidence) {
				rec.Dropped++
			}
		}
		for _, f := range rp.WillChange {
			if ownedPath(pkg.owns, f) {
				continue
			}
			sev := "concern"
			for o, id := range owner {
				if id != pkg.id && ownedPath([]string{o}, f) {
					sev = "blocker"
				}
			}
			rec.Findings = append(rec.Findings, gateFinding{Severity: sev, Where: "package " + pkg.id,
				Claim: "the work must change " + f + ", which the package does not own", Evidence: f + ":1", Source: "scope",
				Verified: evidenceHolds(repo, f+":1")})
		}
	}
	return nil
}

func applyQuestions(raw string, rec *gateRecord, repo string, sp *spec) error {
	var r struct {
		Questions []gateQuestion `json:"questions"`
	}
	if err := extractJSON(raw, &r); err != nil {
		return err
	}
	for _, q := range r.Questions {
		if strings.TrimSpace(q.Question) == "" {
			continue
		}
		if q.AnsweredBy != "" && !evidenceHolds(repo, q.AnsweredBy) {
			q.AnsweredBy = "" // an answer the code does not contain is no answer
			rec.Dropped++
		}
		rec.Questions = append(rec.Questions, q)
	}
	return nil
}

func applyFindings(source string) func(string, *gateRecord, string, *spec) error {
	return func(raw string, rec *gateRecord, repo string, sp *spec) error {
		var r struct {
			Findings []gateFinding `json:"findings"`
		}
		if err := extractJSON(raw, &r); err != nil {
			return err
		}
		for _, f := range r.Findings {
			if f.Severity != "blocker" && f.Severity != "concern" {
				f.Severity = "concern"
			}
			f.Source = source
			f.Verified = evidenceHolds(repo, f.Evidence)
			if !f.Verified {
				rec.Dropped++
			}
			rec.Findings = append(rec.Findings, f)
		}
		return nil
	}
}

func writeGate(dir string, rec *gateRecord) error {
	qdir := filepath.Join(dir, "questions")
	_ = os.RemoveAll(qdir)
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return err
	}
	n := 0
	for _, q := range rec.Questions {
		if !q.blocks() {
			continue
		}
		n++
		key := fmt.Sprintf("q%d", n)
		if id := slug(q.Package); id != "" {
			key = fmt.Sprintf("q%d-%s", n, id)
		}
		card := fmt.Sprintf("---\nquestion: %s\n---\n## Context\nRaised by the spec gate for package `%s`. No file in the repo answers it.\n",
			strings.Join(strings.Fields(q.Question), " "), q.Package)
		if err := os.WriteFile(filepath.Join(qdir, key+".md"), []byte(card), 0o644); err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "gate.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "# Gate report: %s\n\nStatus: **%s** (spec version %s)\n\n", rec.Spec, rec.Status, rec.Hash)
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&r, "## %s\n", title)
		for _, l := range lines {
			fmt.Fprintf(&r, "- %s\n", l)
		}
		r.WriteString("\n")
	}
	section("Deterministic issues", rec.Deterministic)
	var verified, unverified []string
	for _, f := range rec.Findings {
		line := fmt.Sprintf("[%s, %s] %s: %s (%s) %s", f.Severity, f.Source, f.Where, f.Claim, f.Evidence, f.Why)
		if f.Verified {
			verified = append(verified, line)
		} else {
			unverified = append(unverified, line)
		}
	}
	section("Findings with real evidence", verified)
	section("Findings without real evidence (not counted)", unverified)
	var qs, minor []string
	for _, q := range rec.Questions {
		switch {
		case q.blocks():
			qs = append(qs, fmt.Sprintf("%s: %s", q.Package, q.Question))
		case q.AnsweredBy == "":
			minor = append(minor, fmt.Sprintf("%s: %s", q.Package, q.Question))
		}
	}
	section("Questions that need a decision", qs)
	section("Minor open questions (a lead may settle these; not blocking)", minor)
	var errs []string
	for name, msg := range rec.PassErrors {
		errs = append(errs, name+": "+msg)
	}
	section("Passes that did not run", errs)
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(r.String()), 0o644)
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	return out
}
