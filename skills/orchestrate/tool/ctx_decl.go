package main

import (
	"fmt"
	"regexp"
	"strings"
)

// The declaration tier of symbol resolution, for any language in the registry
// and with no server: a name is a symbol only if some declaration form of a
// language matches it, anchored and word-bounded (`def X(`, `class X`,
// `func X`, `fn X`, ...). Callers are word-bounded text hits outside the
// definition, labelled "by name": a pointer, not a call graph.

var declName = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func declKind(k string) string {
	switch k {
	case "func", "fn", "def", "function", "fun":
		return "Function"
	case "method":
		return "Method"
	case "class", "object", "impl":
		return "Class"
	case "interface", "trait":
		return "Interface"
	case "struct":
		return "Struct"
	case "enum", "record":
		return "Enum"
	case "type", "typealias", "union":
		return "Type"
	case "const", "val", "static":
		return "Constant"
	case "var", "let":
		return "Variable"
	case "mod", "module", "namespace":
		return "Module"
	case "macro":
		return "Macro"
	}
	return "Symbol"
}

// declPattern is the union of the declaration forms of sym in language l.
func declPattern(l *langDef, sym string) string {
	var alts []string
	for _, d := range l.Decl {
		alts = append(alts, "(?:"+strings.ReplaceAll(d, "{sym}", regexp.QuoteMeta(sym))+")")
	}
	return strings.Join(alts, "|")
}

func langGlobs(l *langDef) []string {
	var g []string
	for _, e := range l.Exts {
		g = append(g, "*"+e)
	}
	return g
}

// declSites finds the lines of language l's files that declare name.
func (e *ctxEnv) declSites(l *langDef, name string) []rgHit {
	if len(l.Decl) == 0 || !declName.MatchString(name) {
		return nil
	}
	res, err := e.Text(rgQuery{Pattern: declPattern(l, name), Globs: langGlobs(l), CaseSensitive: true, PerFile: 4})
	if err != nil {
		return nil
	}
	var out []rgHit
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	for _, h := range res.Hits {
		ls := e.Lines(h.Path)
		if h.Line >= 1 && h.Line <= len(ls) && !word.MatchString(ls[h.Line-1]) {
			continue
		}
		if isTestPath(h.Path) || e.Skip[h.Path] || h.Line < 1 || h.Line > len(ls) {
			continue
		}
		line := ls[h.Line-1]
		// A form with no declaring keyword (an assignment, a method signature) is
		// a declaration in a class-like body or at top level, not a local.
		if k, _, _ := l.declOfLine(line); k == "" && indentOf(line) > 0 {
			kind, _, _ := strings.Cut(enclosing(ls, h.Line, h.Path), " ")
			switch kind {
			case "class", "struct", "interface", "object", "impl", "trait", "enum", "module", "mod", "namespace", "type":
			default:
				continue
			}
		}
		out = append(out, h)
	}
	return out
}

// declFacts resolves names by their declarations in every language of the
// repo. A name no declaration form matches has no fact: it is plain text.
func (e *ctxEnv) declFacts(name string) *symFact {
	base := name[strings.LastIndex(name, ".")+1:]
	f := &symFact{Symbol: name, Tier: "declaration scan (rg)"}
	for _, lc := range detectedLangs(e.Code) {
		l := langByID(lc.ID)
		if l == nil || len(f.Defs) >= goplsMaxDefs {
			continue
		}
		sites := e.declSites(l, base)
		if len(sites) == 0 {
			continue
		}
		f.Lang = l.ID
		first := len(f.Defs)
		for _, h := range sites {
			if len(f.Defs) >= goplsMaxDefs {
				break
			}
			ls := e.Lines(h.Path)
			kind, _, _ := l.declOfLine(ls[h.Line-1])
			f.Defs = append(f.Defs, symDef{Where: fmt.Sprintf("%s:%d", h.Path, h.Line), Kind: declKind(kind)})
		}
		// Callers by name, on the first definition of the language.
		d := &f.Defs[first]
		if d.Kind == "Module" {
			continue
		}
		d.Func, d.ByName = true, true
		defs := map[string]bool{}
		for _, h := range sites {
			defs[fmt.Sprintf("%s:%d", h.Path, h.Line)] = true
		}
		res, err := e.Text(rgQuery{Pattern: base, Fixed: true, Word: true, CaseSensitive: true, Globs: langGlobs(l)})
		if err != nil {
			continue
		}
		for _, h := range res.Hits {
			ls := e.Lines(h.Path)
			if defs[fmt.Sprintf("%s:%d", h.Path, h.Line)] || e.Skip[h.Path] || isCommentLine(l, strings.TrimSpace(lineAt(ls, h.Line))) {
				continue
			}
			loc := fmt.Sprintf("%s:%d", h.Path, h.Line)
			if isTestPath(h.Path) {
				d.Tests++
				if len(d.TestRefs) < ctxMaxRefs {
					d.TestRefs = append(d.TestRefs, loc)
				}
				continue
			}
			d.Total++
			if len(d.Callers) < ctxMaxRefs {
				d.Callers = append(d.Callers, loc)
			}
		}
	}
	if len(f.Defs) == 0 {
		return nil
	}
	return f
}

// genericSymbols lists names a text mentions that might be symbols in any
// language: backticked identifiers and snake_case words. Plain lowercase
// words are not (the declaration tier would find too many). The declaration
// tier decides which are symbols.
func genericSymbols(q string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		last := s[strings.LastIndex(s, ".")+1:]
		switch {
		case len(s) < 4 || seen[s] || goplsCommon[last] || fileExtRe.MatchString(s) || strings.Contains(s, "/") || !symIdent.MatchString(s):
			return
		case s == strings.ToLower(s) && !strings.ContainsAny(s, "_."):
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, m := range idBacktick.FindAllStringSubmatch(q, -1) {
		span := m[1]
		if i := strings.Index(span, "("); i > 0 {
			span = span[:i]
		}
		add(span)
	}
	for _, s := range snakeRe.FindAllString(q, -1) {
		if len(s) >= 6 {
			add(s)
		}
	}
	return out
}
