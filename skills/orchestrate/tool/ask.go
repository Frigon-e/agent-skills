package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

//go:embed ask.html.tmpl
var askTmplSrc string

var askTmpl = template.Must(template.New("ask").Parse(askTmplSrc))

const maxIncludeBytes = 1 << 20

var (
	includeRe = regexp.MustCompile(`^include: (\S+)(?: (.+))?$`)
	optionRe  = regexp.MustCompile(`^### +([a-z0-9][a-z0-9-]{0,40}): +(\S.*)$`)
)

type askOption struct {
	ID, Label   string
	Body        template.HTML
	Recommended bool
}

type askExtra struct {
	Title string
	Body  template.HTML
}

type askQuestion struct {
	ID, Lead, Label, Resolve string // Label is the key, or a placeholder when keyless
	Question, Change         string
	Warning                  string
	Context, Next            template.HTML
	Extra                    []askExtra
	Options                  []askOption
}

type askPage struct {
	Questions []askQuestion
	Mermaid   bool
}

// card is a parsed question-card file, before includes are rendered.
type card struct {
	question, recommend, change string
	context, next               string
	extra                       []struct{ title, body string }
	options                     []struct{ id, label, body string }
}

// fenceTracker tells whether a line sits inside a fenced code block.
type fenceTracker struct{ fence string }

func (f *fenceTracker) inside(line string) bool {
	t := strings.TrimSpace(line)
	if f.fence != "" {
		if strings.HasPrefix(t, f.fence) {
			f.fence = ""
		}
		return true
	}
	for _, m := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, m) {
			f.fence = m
			return true
		}
	}
	return false
}

func parseCard(src string) (*card, error) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("front matter must start with ---")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("front matter has no closing ---")
	}
	c := &card{}
	for _, l := range lines[1:end] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return nil, fmt.Errorf("front matter line %q is not key: value", l)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		switch strings.TrimSpace(k) {
		case "question":
			c.question = v
		case "recommend":
			c.recommend = v
		case "change":
			c.change = v
		}
	}
	if c.question == "" {
		return nil, fmt.Errorf("front matter needs a question")
	}
	if c.change != "" && c.change != "easy" && c.change != "hard" {
		return nil, fmt.Errorf("change must be easy or hard, got %q", c.change)
	}
	// Split the rest into "## " sections, keeping fenced code intact.
	var name string
	var body []string
	var ft fenceTracker
	flush := func() {
		text := strings.TrimSpace(strings.Join(body, "\n"))
		switch strings.ToLower(name) {
		case "":
		case "context":
			c.context = text
		case "options":
			c.options = nil
			c.parseOptions(text)
		case "next":
			c.next = text
		default:
			c.extra = append(c.extra, struct{ title, body string }{name, text})
		}
		body = nil
	}
	for _, l := range lines[end+1:] {
		if !ft.inside(l) && strings.HasPrefix(l, "## ") {
			flush()
			name = strings.TrimSpace(l[3:])
			continue
		}
		body = append(body, l)
	}
	flush()
	return c, c.validate()
}

func (c *card) parseOptions(text string) {
	var ft fenceTracker
	for _, l := range strings.Split(text, "\n") {
		if !ft.inside(l) && strings.HasPrefix(l, "### ") {
			if m := optionRe.FindStringSubmatch(l); m != nil {
				c.options = append(c.options, struct{ id, label, body string }{m[1], strings.TrimSpace(m[2]), ""})
			} else {
				c.options = append(c.options, struct{ id, label, body string }{"", l, ""})
			}
			continue
		}
		if n := len(c.options); n > 0 {
			c.options[n-1].body += l + "\n"
		}
	}
}

func (c *card) validate() error {
	seen := map[string]bool{}
	for _, o := range c.options {
		if o.id == "" {
			return fmt.Errorf("option heading %q must be \"### id: label\" with a lowercase slug id", o.label)
		}
		if seen[o.id] {
			return fmt.Errorf("duplicate option id %q", o.id)
		}
		seen[o.id] = true
	}
	if len(c.options) == 1 {
		return fmt.Errorf("one option is not a choice: give two or more, or none for a free-text question")
	}
	if c.recommend != "" && !seen[c.recommend] {
		return fmt.Errorf("recommend names %q, which is not an option", c.recommend)
	}
	return nil
}

// renderer turns card text into HTML for one question, resolving includes.
type renderer struct {
	lead, key, cardPath string
	taskDir, qDir       string // taskDir is symlink-resolved
	rawTaskDir          string
	filesDir            string
	mermaid             bool
}

// Raw HTML in markdown is not emitted. Hard wraps keep a card's short lines
// (Gain:, Cost:) on their own lines, as the lead wrote them.
var md = goldmark.New(goldmark.WithRendererOptions(gmhtml.WithHardWraps()))

func renderMarkdown(src string) (string, error) {
	var b bytes.Buffer
	if err := md.Convert([]byte(src), &b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// body renders markdown, replacing include lines with the rendered file.
func (r *renderer) body(text string) (template.HTML, error) {
	var out, chunk strings.Builder
	flush := func() error {
		if strings.TrimSpace(chunk.String()) == "" {
			chunk.Reset()
			return nil
		}
		s, err := renderMarkdown(chunk.String())
		chunk.Reset()
		out.WriteString(s)
		return err
	}
	var ft fenceTracker
	for _, l := range strings.Split(text, "\n") {
		if m := includeRe.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil && !ft.inside(l) {
			if err := flush(); err != nil {
				return "", err
			}
			s, err := r.include(m[1], strings.TrimSpace(m[2]))
			if err != nil {
				return "", err
			}
			out.WriteString(s)
			out.WriteString("\n")
			continue
		}
		chunk.WriteString(l + "\n")
	}
	if err := flush(); err != nil {
		return "", err
	}
	// #nosec: markdown went through goldmark without raw HTML, and HTML includes went through htmlPolicy.
	return template.HTML(out.String()), nil
}

func (r *renderer) include(rel, alt string) (string, error) {
	inside := func(p string) bool {
		return p == r.taskDir || strings.HasPrefix(p, r.taskDir+string(filepath.Separator))
	}
	// Check the lexical path first so a missing outside file is still "outside".
	if lex := filepath.Join(r.qDir, rel); !inside(lex) && !inside(r.resolveDir(lex)) {
		return "", fmt.Errorf("%s: include %s is outside the task directory", r.cardPath, rel)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(r.qDir, rel))
	if err != nil {
		return "", fmt.Errorf("%s: include %s: file not found", r.cardPath, rel)
	}
	if !inside(real) {
		return "", fmt.Errorf("%s: include %s is outside the task directory", r.cardPath, rel)
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s: include %s is not a regular file", r.cardPath, rel)
	}
	if st.Size() > maxIncludeBytes {
		return "", fmt.Errorf("%s: include %s is over 1 MiB", r.cardPath, rel)
	}
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	// SVG goes in as an image: a browser runs no script in an <img>.
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg":
		if alt == "" {
			return "", fmt.Errorf("%s: include %s needs alt text: include: %s <alt text>", r.cardPath, rel, rel)
		}
		name := r.lead + "-" + r.key + "-" + filepath.Base(rel)
		if err := copyFile(real, filepath.Join(r.filesDir, name)); err != nil {
			return "", fmt.Errorf("%s: include %s: %v", r.cardPath, rel, err)
		}
		return fmt.Sprintf(`<img src="files/%s" alt="%s">`, url.PathEscape(name), html.EscapeString(alt)), nil
	case ".md", ".html", ".mmd", ".mermaid":
	default:
		return "", fmt.Errorf("%s: include %s has an unsupported extension", r.cardPath, rel)
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return "", fmt.Errorf("%s: include %s: %v", r.cardPath, rel, err)
	}
	switch ext {
	case ".md":
		return renderMarkdown(string(b))
	case ".mmd", ".mermaid":
		r.mermaid = true
		return `<pre class="mermaid">` + html.EscapeString(string(b)) + `</pre>`, nil
	}
	// A lead can copy outside text into an include, so HTML is cleaned: no
	// script, event handler, form control, id or javascript: link.
	// The wrapper clips the include so its styles cannot draw over the form.
	return `<div class="include">` + htmlPolicy.Sanitize(string(b)) + `</div>`, nil
}

// htmlPolicy is an allowlist that keeps layout, tables, lists and inline
// styles for UI mocks. It allows no id or name, so an include cannot shadow
// window globals such as window.lavish.
var htmlPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowStandardURLs()
	p.AllowAttrs("href").OnElements("a")
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.AllowImages()
	p.AllowLists()
	p.AllowTables()
	p.AllowElements("p", "br", "hr", "div", "span", "h1", "h2", "h3", "h4", "h5", "h6",
		"strong", "em", "b", "i", "u", "s", "small", "sub", "sup", "mark", "kbd", "code", "pre",
		"blockquote", "figure", "figcaption", "section", "article", "aside", "header", "footer",
		"nav", "dl", "dt", "dd", "details", "summary")
	p.AllowAttrs("class", "style", "title", "lang", "role", "aria-label", "aria-hidden").Globally()
	return p
}()

// resolveDir maps a lexical path under the unresolved task dir to the resolved one.
func (r *renderer) resolveDir(p string) string {
	if rel, err := filepath.Rel(r.rawTaskDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join(r.taskDir, rel)
	}
	return p
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// buildCard reads and renders the card for one decision into q.
func buildCard(r *renderer, q *askQuestion) error {
	src, err := os.ReadFile(r.cardPath)
	if err != nil {
		return err
	}
	c, err := parseCard(string(src))
	if err != nil {
		return fmt.Errorf("%s: %v", r.cardPath, err)
	}
	q.Question, q.Change = c.question, c.change
	if q.Context, err = r.body(c.context); err != nil {
		return err
	}
	if q.Next, err = r.body(c.next); err != nil {
		return err
	}
	for _, x := range c.extra {
		b, err := r.body(x.body)
		if err != nil {
			return err
		}
		q.Extra = append(q.Extra, askExtra{x.title, b})
	}
	for _, o := range c.options {
		b, err := r.body(o.body)
		if err != nil {
			return err
		}
		opt := askOption{ID: o.id, Label: o.label, Body: b, Recommended: o.id == c.recommend}
		if opt.Recommended {
			q.Options = append([]askOption{opt}, q.Options...)
		} else {
			q.Options = append(q.Options, opt)
		}
	}
	return nil
}

func cmdAsk(a *app, args []string) error {
	fs := a.flags("ask")
	outFlag := fs.String("out", "", "write the page to `DIR` (default <run root>/ask, or <cards dir>/../ask)")
	cardsFlag := fs.String("cards", "", "render every card in `DIR` instead of the leads' open decisions")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := exactArgs(pos, 0, "ask [--cards DIR] [--out DIR]"); err != nil {
		return err
	}
	abs := func(p string) string {
		p = expandHome(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(a.cwd, p)
		}
		return filepath.Clean(p)
	}
	var out, cardsDir, root string
	if *cardsFlag != "" {
		cardsDir = abs(*cardsFlag)
		if st, err := os.Stat(cardsDir); err != nil || !st.IsDir() {
			return fail("--cards %s is not a directory", cardsDir)
		}
		out = filepath.Join(filepath.Dir(cardsDir), "ask")
	} else {
		s, err := a.site()
		if err != nil {
			return err
		}
		root = s.root
		out = filepath.Join(root, "ask")
	}
	if *outFlag != "" {
		out = abs(*outFlag)
	}
	filesDir := filepath.Join(out, "files")
	if err := os.RemoveAll(filesDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return err
	}

	var page askPage
	var warnings []askWarning
	add := func(q askQuestion, r *renderer, fallback string) {
		q.ID = fmt.Sprintf("q%d", len(page.Questions)+1)
		var werr error
		if r != nil {
			card := q
			if werr = buildCard(r, &card); werr == nil {
				q = card
				page.Mermaid = page.Mermaid || r.mermaid
			}
		}
		if q.Question == "" {
			q.Question = fallback
			q.Context, q.Next, q.Extra, q.Options, q.Change = "", "", nil, nil, ""
		}
		if q.Warning != "" && werr == nil {
			werr = errors.New(q.Warning)
		}
		if werr != nil {
			msg := werr.Error()
			if r != nil {
				msg = strings.TrimPrefix(msg, r.cardPath+": ")
			}
			q.Warning = msg
			warnings = append(warnings, askWarning{q.Lead, q.Label, msg})
		}
		page.Questions = append(page.Questions, q)
	}

	if cardsDir != "" {
		// Each <key>.md in the folder is one open question; a card that is
		// answered moves to answered/, which this glob does not read.
		topic := filepath.Dir(cardsDir)
		topicReal, err := filepath.EvalSymlinks(topic)
		if err != nil {
			topicReal = topic
		}
		paths, _ := filepath.Glob(filepath.Join(cardsDir, "*.md"))
		sort.Strings(paths)
		for _, cardPath := range paths {
			key := strings.TrimSuffix(filepath.Base(cardPath), ".md")
			if !keyRe.MatchString(key) {
				warnings = append(warnings, askWarning{"", filepath.Base(cardPath), fmt.Sprintf("skipped: the name must match %s.md", keyRe)})
				continue
			}
			r := &renderer{key: key, cardPath: cardPath, taskDir: topicReal, rawTaskDir: topic, qDir: cardsDir, filesDir: filesDir}
			add(askQuestion{Label: key}, r, key)
		}
	}
	var leads []openLead
	if root != "" {
		leads = openLeads(root)
	}
	for _, ol := range leads {
		taskReal, err := filepath.EvalSymlinks(ol.dir)
		if err != nil {
			taskReal = ol.dir
		}
		// The lead name and key go into a shell command on the page, so only
		// names that match their patterns are used there.
		lead := filepath.Base(ol.dir)
		if !taskIDRe.MatchString(lead) {
			warnings = append(warnings, askWarning{"", lead, fmt.Sprintf("skipped task directory: the name must match %s", taskIDRe)})
			continue
		}
		keyless := 0
		for _, e := range openDecisions(readStatus(ol.dir)) {
			q := askQuestion{Lead: lead, Label: e.Key, Resolve: e.Key}
			var r *renderer
			if !keyRe.MatchString(e.Key) {
				if e.Key != "" {
					q.Warning = fmt.Sprintf("key %q must match %s; answer it without --resolve", e.Key, keyRe)
				}
				keyless++
				q.Label, q.Resolve = fmt.Sprintf("keyless-%d", keyless), ""
			} else if cardPath := filepath.Join(ol.dir, "questions", e.Key+".md"); fileExists(cardPath) {
				r = &renderer{lead: lead, key: e.Key, cardPath: cardPath, taskDir: taskReal, rawTaskDir: ol.dir,
					qDir: filepath.Join(ol.dir, "questions"), filesDir: filesDir}
			}
			add(q, r, e.Text)
		}
	}

	var buf bytes.Buffer
	if err := askTmpl.Execute(&buf, page); err != nil {
		return err
	}
	path := filepath.Join(out, "index.html")
	if err := writeAtomic(path, buf.Bytes()); err != nil {
		return err
	}
	// AXI-style output: short key: value lines an agent reads in a few tokens.
	fmt.Fprintf(a.out, "page: %s\n", path)
	fmt.Fprintf(a.out, "questions: %d\n", len(page.Questions))
	if len(warnings) > 0 {
		fmt.Fprintf(a.out, "warnings[%d]{question,message}:\n", len(warnings))
		for _, w := range warnings {
			fmt.Fprintf(a.out, "  %s,%s\n", toonValue(w.id()), toonValue(w.msg))
		}
	}
	if len(page.Questions) == 0 {
		fmt.Fprintln(a.out, "No open questions.")
		return nil
	}
	fmt.Fprintln(a.out, "help[2]:")
	fmt.Fprintf(a.out, "  lavish-axi %s\n", path)
	fmt.Fprintf(a.out, "  lavish-axi poll %s\n", path)
	return nil
}

type askWarning struct{ lead, label, msg string }

func (w askWarning) id() string {
	if w.lead == "" {
		return w.label
	}
	return w.lead + "/" + w.label
}

// toonValue quotes a TOON value when it holds a delimiter, a quote or edge
// spaces.
func toonValue(v string) string {
	if v != "" && !strings.ContainsAny(v, ",:\"\\\n") && strings.TrimSpace(v) == v {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
