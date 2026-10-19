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
	"sort"
	"strings"
	"time"
)

const (
	ctxSnippet   = 6  // snippet lines kept per concept hit (the gate's cards)
	ctxBodyLines = 80 // lines of a concept chunk kept for `orch ctx`
	ctxCallLimit = 20 * time.Second
	ctxRgMax     = 2000 // hits read from one rg call
)

type codeHit struct {
	Path    string   `json:"path"`
	Start   int      `json:"start"`
	End     int      `json:"end"`
	Snippet []string `json:"snippet"`
	Body    []string `json:"body,omitempty"` // the chunk, up to ctxBodyLines lines
}

func jbcontextBin() string {
	if p := os.Getenv("ORCH_JBCONTEXT"); p != "" {
		return p
	}
	return expandHome("~/.jbcontext/bin/jbcontext")
}

// searchCode runs one jbcontext search in dir and returns its hits, from the
// cache under cacheDir when the same search was run before. rev pins it to an
// indexed revision when not empty.
func searchCode(bin, dir, cacheDir, rev, pathFilter string, limit int, query string) ([]codeHit, error) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", query, rev, pathFilter, limit)))
	cache := filepath.Join(cacheDir, fmt.Sprintf("jb2-%x.json", sum[:8]))
	if b, err := os.ReadFile(cache); err == nil {
		var hits []codeHit
		if json.Unmarshal(b, &hits) == nil {
			return hits, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctxCallLimit)
	defer cancel()
	args := []string{"search", "--limit", fmt.Sprint(limit), "--json-output"}
	if rev != "" {
		args = append(args, "--revision", rev)
	}
	if pathFilter != "" {
		args = append(args, "--path-filter", pathFilter)
	}
	cmd := exec.CommandContext(ctx, bin, append(args, query)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("jbcontext timed out after %s", ctxCallLimit)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		return nil, fmt.Errorf("jbcontext failed: %v: %s", err, short(msg, 200))
	}
	hits, err := parseJbcontext(out)
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(hits); err == nil {
		_ = os.MkdirAll(cacheDir, 0o755)
		_ = os.WriteFile(cache, b, 0o644)
	}
	return hits, nil
}

// parseJbcontext reads `jbcontext search --json-output`: JSON objects of type
// search_result, each with results holding the file, the chunk text and the
// line the chunk starts on.
func parseJbcontext(out []byte) ([]codeHit, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var hits []codeHit
	seen := false
	for dec.More() {
		var o struct {
			Type    string `json:"type"`
			Results []struct {
				Result struct {
					Position struct {
						Path string `json:"relativePath"`
					} `json:"sourcePosition"`
				} `json:"result"`
				Content   string `json:"content"`
				StartLine int    `json:"contentStartLine"`
			} `json:"results"`
		}
		if err := dec.Decode(&o); err != nil {
			if seen {
				break
			}
			return nil, fmt.Errorf("jbcontext output is not JSON: %v", err)
		}
		seen = true
		if o.Type != "search_result" {
			continue
		}
		for _, r := range o.Results {
			lines := strings.Split(strings.Trim(r.Content, "\n"), "\n")
			start := max(r.StartLine, 1)
			if strings.HasPrefix(r.Content, "\n") {
				start++
			}
			hits = append(hits, codeHit{Path: r.Result.Position.Path, Start: start, End: start + len(lines) - 1, Snippet: lines[:min(len(lines), ctxSnippet)], Body: lines[:min(len(lines), ctxBodyLines)]})
		}
	}
	return hits, nil
}

// indexDetail describes the index a search used, for the sources line.
func (e *ctxEnv) indexDetail() string {
	st := e.Ready()
	if st.head == "" {
		return "index of unknown revision"
	}
	d := "index " + sha7(orDefault(st.indexd, "none")) + " (HEAD " + sha7(st.head)
	if st.rev == "" {
		d += ", stale"
	}
	if st.dirty {
		d += ", uncommitted changes"
	}
	return d + ")"
}

// Concept is a semantic code search with jbcontext. Hits in files the env
// skips (the spec) are left out.
func (e *ctxEnv) Concept(query, pathFilter string, limit int) ([]codeHit, error) {
	if why := e.jbMissing(); why != "" {
		return nil, fmt.Errorf("%s", why)
	}
	rev := e.jbRev()
	e.indexNote()
	hits, err := searchCode(e.JB, e.Code, e.CacheDir, rev, pathFilter, limit, query)
	if rev != "" && e.Ready().dirty {
		// A pinned search reads only the committed snapshot. An unpinned one
		// also sees this worktree's edits, so run both and let the check below
		// keep what still matches the files.
		if more, merr := searchCode(e.JB, e.Code, e.CacheDir, "", pathFilter, limit, query); merr == nil {
			hits = mergeHits(hits, more)
		}
	}
	e.use("jbcontext", e.indexDetail())
	var paths []string
	for _, h := range hits {
		paths = append(paths, h.Path)
	}
	ignored := e.ignoredPaths(paths)
	var out []codeHit
	stale := 0
	edited := map[string]bool{}
	for _, f := range e.editedFiles() {
		edited[f] = true
	}
	for _, h := range hits {
		if e.Skip[h.Path] || ignored[h.Path] {
			continue
		}
		// A pinned search of a file nobody edited reads the checkout's own
		// committed text, and outside a git repo there is no other revision, so
		// only the file's presence is checked; the text is compared when the
		// file is edited here or a search in a repo was not pinned.
		if !e.hitMatchesCheckout(h, edited[h.Path] || rev == "" && e.Ready().head != "") {
			stale++
			continue
		}
		out = append(out, h)
	}
	if files := e.editedFiles(); rev != "" && len(files) > 0 {
		list := files[:min(len(files), 8)]
		more := ""
		if len(files) > len(list) {
			more = fmt.Sprintf(" (+%d more)", len(files)-len(list))
		}
		e.note("edited in this worktree, so jbcontext may not show their current text: " + strings.Join(list, ", ") + more + "; read them with orch ctx text or read")
	}
	if stale > 0 {
		e.note(fmt.Sprintf("jbcontext: %d hits dropped because their file is not in this checkout or no longer reads as the index saw it", stale))
	}
	return out, err
}

// mergeHits adds the hits of more that are not in base. A hit is the same hit
// only with the same file, start and text: the committed and the edited text of
// one place are both kept, and the check against the file keeps the right one.
func mergeHits(base, more []codeHit) []codeHit {
	key := func(h codeHit) string { return fmt.Sprintf("%s:%d:%s", h.Path, h.Start, strings.Join(h.Body, "\n")) }
	seen := map[string]bool{}
	for _, h := range base {
		seen[key(h)] = true
	}
	for _, h := range more {
		if k := key(h); !seen[k] {
			seen[k] = true
			base = append(base, h)
		}
	}
	return base
}

// hitMatchesCheckout reports whether a hit's file is in this checkout and, with
// compareText, its first line is still where the index said. jbcontext shares one repository id
// between worktrees, so a search can return another worktree's untracked file,
// or text from a snapshot that this worktree has since edited. Lines can be a
// couple off between the index and the file, so the first non-blank line of the
// hit is looked for within two lines of its start.
func (e *ctxEnv) hitMatchesCheckout(h codeHit, compareText bool) bool {
	if !ctxWithinRoot(e.Code, h.Path) {
		return false
	}
	if !compareText {
		return true
	}
	want := ""
	for _, l := range h.Body {
		if want = strings.TrimSpace(l); want != "" {
			break
		}
	}
	if want == "" {
		for _, l := range h.Snippet {
			if want = strings.TrimSpace(l); want != "" {
				break
			}
		}
	}
	if want == "" {
		return true // an empty chunk has nothing to compare
	}
	ls := e.Lines(h.Path)
	for i := max(h.Start-3, 0); i < min(h.Start+2, len(ls)); i++ {
		if strings.TrimSpace(ls[i]) == want {
			return true
		}
	}
	return false
}

// ctxDefaultExcludes are the globs rg skips unless asked: lockfiles, minified
// and generated files, vendored and built directories.
var ctxDefaultExcludes = []string{
	"!*.lock", "!package-lock.json", "!pnpm-lock.yaml", "!yarn.lock", "!go.sum", "!*.min.js", "!*.min.css", "!*.map",
	"!*.pb.go", "!*_gen.go", "!*.gen.go", "!*.generated.*", "!**/vendor/**", "!**/dist/**", "!**/node_modules/**",
}

// ctxGeneratedExcludes are the name-based excludes that --generated lifts.
var ctxGeneratedExcludes = map[string]bool{"!*.min.js": true, "!*.min.css": true, "!*.map": true, "!*.pb.go": true, "!*_gen.go": true, "!*.gen.go": true, "!*.generated.*": true}

type rgHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type rgCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// rgQuery is one text search with ripgrep.
type rgQuery struct {
	Pattern       string
	Fixed         bool // -F: the pattern is a literal string, not a regular expression
	Word          bool // -w
	IgnoreCase    bool // -i (the default is smart-case)
	Multiline     bool // -U
	OnlyMatching  bool // -o
	FilesOnly     bool // -l
	Count         bool // -c
	PerFile       int  // -m, matches per file
	Globs         []string
	Type          string // -t
	Path          string // a file or directory to search
	AllFiles      bool   // do not skip lockfiles, generated and vendored files
	CaseSensitive bool   // -s instead of smart-case
	Generated     string // none (default), include or only: the generated-file mode; include and only lift the name-based generated excludes
}

// rgResult is what a search found: match and context lines, or the files, or
// the counts per file.
type rgResult struct {
	Hits   []rgHit   `json:"hits,omitempty"`
	Files  []string  `json:"files,omitempty"`
	Counts []rgCount `json:"counts,omitempty"`
}

const rgTextCap = 400 // characters kept of a matching line

// Text searches the checkout with rg, from the cache when the same search ran
// before. Results come sorted by path; files the env skips are left out.
func (e *ctxEnv) Text(q rgQuery) (rgResult, error) {
	if e.ReadOnly && q.Path != "" && !ctxWithinRoot(e.Code, q.Path) {
		return rgResult{}, fmt.Errorf("read-only ctx: path %s is outside the pinned root or missing", q.Path)
	}
	var skip []string
	for s := range e.Skip {
		skip = append(skip, s)
	}
	sort.Strings(skip)
	key := fmt.Sprintf("%+v\x00%s", q, strings.Join(skip, ","))
	sum := sha256.Sum256([]byte(key))
	cache := filepath.Join(e.CacheDir, fmt.Sprintf("rg-%x.json", sum[:8]))
	e.use("rg", orDefault(which(e.RG), e.RG))
	if b, err := os.ReadFile(cache); err == nil {
		var res rgResult
		if json.Unmarshal(b, &res) == nil {
			return res, nil
		}
	}
	args := []string{"--sort", "path", "-S"}
	if q.CaseSensitive {
		args[2] = "-s"
	}
	switch {
	case q.FilesOnly:
		args = append(args, "-l")
	case q.Count:
		args = append(args, "-c", "--with-filename")
	default:
		args = append(args, "--json")
	}
	for flag, on := range map[string]bool{"-F": q.Fixed, "-w": q.Word, "-U": q.Multiline} {
		if on {
			args = append(args, flag)
		}
	}
	if q.IgnoreCase {
		args = append(args, "-i")
	}
	if q.PerFile > 0 {
		args = append(args, "-m", fmt.Sprint(q.PerFile))
	}
	if q.Type != "" {
		args = append(args, "-t", q.Type)
	}
	if !q.AllFiles {
		for _, g := range ctxDefaultExcludes {
			if (q.Generated == "include" || q.Generated == "only") && ctxGeneratedExcludes[g] {
				continue
			}
			args = append(args, "-g", g)
		}
	}
	for _, s := range skip {
		args = append(args, "-g", "!"+s)
	}
	for _, g := range q.Globs {
		args = append(args, "-g", g)
	}
	args = append(args, "-e", q.Pattern, "--")
	if q.Path != "" {
		args = append(args, q.Path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctxCallLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.RG, args...)
	cmd.Dir = e.Code
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return rgResult{}, fmt.Errorf("rg timed out after %s", ctxCallLimit)
	}
	if err != nil {
		// 1 is no match; a glob that leaves no file to search is no match either
		noFiles := strings.Contains(stderr.String(), "No files were searched")
		if ee, ok := err.(*exec.ExitError); !noFiles && (!ok || ee.ExitCode() != 1) {
			return rgResult{}, fmt.Errorf("rg failed: %v: %s", err, short(strings.TrimSpace(stderr.String()), 160))
		}
	}
	var res rgResult
	clean := func(p string) string { return filepath.ToSlash(strings.TrimPrefix(p, "./")) }
	switch {
	case q.FilesOnly:
		for _, l := range strings.Split(string(out), "\n") {
			if l != "" {
				res.Files = append(res.Files, clean(l))
			}
		}
	case q.Count:
		for _, l := range strings.Split(string(out), "\n") {
			if i := strings.LastIndex(l, ":"); i > 0 {
				var n int
				if _, err := fmt.Sscanf(l[i+1:], "%d", &n); err == nil {
					res.Counts = append(res.Counts, rgCount{clean(l[:i]), n})
				}
			}
		}
	default:
		res.Hits = parseRgJSON(out, q.OnlyMatching, q.Multiline)
	}
	if b, err := json.Marshal(res); err == nil {
		_ = os.MkdirAll(e.CacheDir, 0o755)
		_ = os.WriteFile(cache, b, 0o644)
	}
	return res, nil
}

// parseRgJSON reads `rg --json`: match events with their path,
// line and text. With onlyMatching a match is its matched text.
func parseRgJSON(out []byte, onlyMatching, multiline bool) []rgHit {
	var hits []rgHit
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Path  struct{ Text string } `json:"path"`
				Lines struct{ Text string } `json:"lines"`
				Line  int                   `json:"line_number"`
				Subs  []struct {
					Match struct{ Text string } `json:"match"`
				} `json:"submatches"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || (ev.Type != "match") || ev.Data.Path.Text == "" {
			continue
		}
		path := filepath.ToSlash(strings.TrimPrefix(ev.Data.Path.Text, "./"))
		text := strings.TrimSpace(strings.SplitN(strings.TrimRight(ev.Data.Lines.Text, "\r\n"), "\n", 2)[0])
		if onlyMatching {
			for _, s := range ev.Data.Subs {
				hits = append(hits, rgHit{Path: path, Line: ev.Data.Line, Text: short(s.Match.Text, rgTextCap)})
			}
			continue
		}
		if multiline {
			for i, l := range strings.Split(strings.TrimRight(ev.Data.Lines.Text, "\r\n"), "\n") {
				hits = append(hits, rgHit{Path: path, Line: ev.Data.Line + i, Text: short(strings.TrimSpace(l), rgTextCap)})
			}
			continue
		}
		hits = append(hits, rgHit{Path: path, Line: ev.Data.Line, Text: short(text, rgTextCap)})
		if len(hits) >= ctxRgMax {
			break
		}
	}
	return hits
}

// Lines reads a file of the checkout into lines; nil if it cannot be read.
func (e *ctxEnv) Lines(path string) []string {
	if e.ReadOnly && !ctxWithinRoot(e.Code, path) {
		return nil
	}
	e.mu.Lock()
	ls, ok := e.lineCache[path]
	e.mu.Unlock()
	if ok {
		return ls
	}
	if b, err := os.ReadFile(filepath.Join(e.Code, path)); err == nil {
		ls = strings.Split(string(b), "\n")
	}
	e.mu.Lock()
	if e.lineCache == nil {
		e.lineCache = map[string][]string{}
	}
	e.lineCache[path] = ls
	e.mu.Unlock()
	return ls
}
