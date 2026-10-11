package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type specPackage struct {
	id, title                          string
	kind, model, effort, harness, base string
	deps, owns                         []string
	body, acceptance                   string
}

type spec struct {
	defaults map[string]string
	title    string
	sections map[string]string
	packages []*specPackage
	problems []string
}

func (sp *spec) add(format string, a ...any) {
	sp.problems = append(sp.problems, fmt.Sprintf(format, a...))
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var packageKeys = map[string]bool{"kind": true, "model": true, "effort": true, "harness": true, "depends": true, "owns": true}

// parseSpec reads the spec format of references/spec-format.md. Headings
// inside code fences are ignored. Problems are collected, not returned early.
func parseSpec(src string) *spec {
	sp := &spec{defaults: map[string]string{}, sections: map[string]string{}}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			sp.add("front matter has no closing ---")
		} else {
			for _, l := range lines[1:end] {
				if k, v, ok := strings.Cut(l, ":"); ok {
					sp.defaults[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			lines = lines[end+1:]
		}
	}

	// level-2 sections, with the package area kept as raw lines
	var cur string
	secLines := map[string][]string{}
	fenced := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
		} else if !fenced {
			if m := headingRe.FindStringSubmatch(l); m != nil && sp.title == "" {
				sp.title = m[1]
				continue
			}
			if strings.HasPrefix(l, "## ") {
				cur = strings.TrimSpace(l[3:])
				secLines[cur] = nil
				continue
			}
		}
		if cur != "" {
			secLines[cur] = append(secLines[cur], l)
		}
	}
	for k, v := range secLines {
		sp.sections[k] = strings.TrimSpace(strings.Join(v, "\n"))
	}
	if sp.title == "" {
		sp.add("spec has no `# Title` heading")
	}
	for _, name := range []string{"Behaviour", "Design rules", "Authority", "Packages", "Open questions"} {
		if _, ok := sp.sections[name]; !ok {
			sp.add("section `## %s` is missing", name)
		}
	}
	for _, name := range []string{"Behaviour", "Design rules"} {
		if t, ok := sp.sections[name]; ok && t == "" {
			sp.add("section `## %s` is empty", name)
		}
	}
	if auth, ok := sp.sections["Authority"]; ok {
		for _, label := range []string{"May decide", "Must escalate", "Never"} {
			if labelled(auth, label) == "" {
				sp.add("Authority needs a non-empty `%s:` line", label)
			}
		}
	}
	if oq, ok := sp.sections["Open questions"]; ok && oq != "" && oq != "None." {
		sp.add("open questions are not empty; ask them first")
	}
	if _, ok := sp.sections["Packages"]; ok {
		sp.parsePackages(secLines["Packages"])
	}
	return sp
}

func (sp *spec) parsePackages(lines []string) {
	var blocks [][]string
	fenced := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(l, "### ") {
			blocks = append(blocks, nil)
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1] = append(blocks[len(blocks)-1], l)
		}
	}
	if len(blocks) == 0 {
		sp.add("`## Packages` has no `### <id>: <title>` block")
	}
	for _, b := range blocks {
		sp.parsePackage(b)
	}
}

func (sp *spec) parsePackage(block []string) {
	head := strings.TrimSpace(block[0][4:])
	id, title, _ := strings.Cut(head, ":")
	p := &specPackage{id: strings.TrimSpace(id), title: strings.TrimSpace(title)}
	sp.packages = append(sp.packages, p)
	name := p.id
	if name == "" {
		name = head
	}
	if p.title == "" {
		sp.add("package %s: heading needs `### <id>: <title>`", name)
	}
	keys := map[string]string{}
	i := 1
	for ; i < len(block) && strings.TrimSpace(block[i]) != ""; i++ {
		k, v, ok := strings.Cut(block[i], ":")
		k = strings.TrimSpace(k)
		switch {
		case !ok:
			sp.add("package %s: line %q is not `key: value`", name, block[i])
		case !packageKeys[k]:
			sp.add("package %s: unknown key %q", name, k)
		default:
			keys[k] = strings.TrimSpace(v)
		}
	}
	var body, acc []string
	inAcc, fenced := false, false
	for _, l := range block[i:] {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(l, "#### ") {
			inAcc = strings.TrimSpace(l[5:]) == "Acceptance"
			if inAcc {
				continue
			}
		}
		if inAcc {
			acc = append(acc, l)
		} else {
			body = append(body, l)
		}
	}
	p.body = strings.TrimSpace(strings.Join(body, "\n"))
	p.acceptance = strings.TrimSpace(strings.Join(acc, "\n"))
	p.kind = keys["kind"]
	pick := func(key string) string {
		if v := keys[key]; v != "" {
			return v
		}
		return sp.defaults[key]
	}
	p.model, p.effort, p.harness = pick("model"), pick("effort"), pick("harness")
	p.deps, p.owns = splitList(keys["depends"]), splitList(keys["owns"])
	if _, ok := sp.sections["Packages"]; !ok {
		return
	}
	if !taskIDRe.MatchString(p.id) {
		sp.add("package %q: id must match %s", p.id, taskIDRe)
	}
	if p.kind != "ship" && p.kind != "scout" {
		sp.add("package %s: kind must be ship or scout", name)
	}
	if p.harness != "claude" && p.harness != "codex" {
		sp.add("package %s: harness must be claude or codex (set it on the package or in the front matter)", name)
	}
	if p.model == "" {
		sp.add("package %s: no model (set it on the package or in the front matter)", name)
	}
	if p.effort == "" {
		sp.add("package %s: no effort (set it on the package or in the front matter)", name)
	}
	if p.kind == "ship" && len(p.owns) == 0 {
		sp.add("package %s: a ship package needs `owns:`", name)
	}
	if p.body == "" {
		sp.add("package %s: needs body text between its key lines and `#### Acceptance`", name)
	}
	if p.acceptance == "" {
		sp.add("package %s: needs a non-empty `#### Acceptance` subsection", name)
	}
}

// validate checks the cross-package rules.
func (sp *spec) validate() {
	ids := map[string]bool{}
	for _, p := range sp.packages {
		if ids[p.id] {
			sp.add("package id %s is used twice", p.id)
		}
		ids[p.id] = true
	}
	for _, p := range sp.packages {
		for _, d := range p.deps {
			if !ids[d] {
				sp.add("package %s: depends on %q, which is not a package of this spec", p.id, d)
			}
		}
	}
	if _, left := sp.waves(); len(left) > 0 {
		sp.add("dependency cycle among: %s", strings.Join(left, " "))
	}
	for i, p := range sp.packages {
		for _, q := range sp.packages[i+1:] {
			for _, a := range p.owns {
				for _, b := range q.owns {
					if ownsOverlap(a, b) {
						sp.add("ownership overlap: %s owns %q and %s owns %q", p.id, a, q.id, b)
					}
				}
			}
		}
	}
}

// waves returns topological layers (sorted ids) and the ids stuck in a cycle.
func (sp *spec) waves() (waves [][]string, left []string) {
	done := map[string]bool{}
	pending := map[string]*specPackage{}
	for _, p := range sp.packages {
		pending[p.id] = p
	}
	known := map[string]bool{}
	for id := range pending {
		known[id] = true
	}
	for len(pending) > 0 {
		var wave []string
		for id, p := range pending {
			ready := true
			for _, d := range p.deps {
				if known[d] && !done[d] {
					ready = false
				}
			}
			if ready {
				wave = append(wave, id)
			}
		}
		if len(wave) == 0 {
			for id := range pending {
				left = append(left, id)
			}
			sort.Strings(left)
			return waves, left
		}
		sort.Strings(wave)
		for _, id := range wave {
			done[id] = true
			delete(pending, id)
		}
		waves = append(waves, wave)
	}
	return waves, nil
}

func normOwn(p string) (path string, dir bool) {
	p = strings.TrimSpace(p)
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	dir = strings.HasSuffix(p, "/")
	return strings.TrimRight(p, "/"), dir
}

// ownsOverlap: equal, or one is a directory that contains the other.
func ownsOverlap(a, b string) bool {
	pa, da := normOwn(a)
	pb, db := normOwn(b)
	return pa == pb || (da && strings.HasPrefix(pb, pa+"/")) || (db && strings.HasPrefix(pa, pb+"/"))
}

func cmdPlan(a *app, args []string) error {
	fs := a.flags("plan")
	dry := fs.Bool("dry-run", false, "check the spec and show what would be created; write nothing")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := exactArgs(pos, 1, "plan <spec> [--dry-run]"); err != nil {
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
	real, err := filepath.EvalSymlinks(specPath)
	if err != nil {
		return fail("spec %s: %v", pos[0], err)
	}
	repo, err := filepath.EvalSymlinks(s.repo)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(repo, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fail("spec %s is not inside the repo %s", pos[0], s.repo)
	}
	rel = filepath.ToSlash(rel)
	src, err := os.ReadFile(real)
	if err != nil {
		return err
	}
	sp := parseSpec(string(src))
	sp.validate()

	baseBranch := s.cfg.BaseBranch
	if b := sp.defaults["base"]; b != "" {
		baseBranch = b
	}
	if strings.HasPrefix(baseBranch, "-") {
		sp.add("base %q is not a branch or commit", baseBranch)
	} else if sha, err := git(s.repo, "rev-parse", "--verify", "--quiet", "--short=12", baseBranch+"^{commit}"); err != nil || sha == "" {
		sp.add("base %q does not resolve in %s", baseBranch, s.repo)
	} else if _, err := git(s.repo, "cat-file", "-e", sha+":"+rel); err != nil {
		sp.add("%s is not in %s: commit the spec to %s first; leads read it from their worktree", rel, baseBranch, baseBranch)
	} else if _, err := git(s.repo, "diff", "--quiet", sha, "--", rel); err != nil {
		sp.add("spec has changes not in %s: commit them first", baseBranch)
	}

	status := map[string]string{}
	for _, p := range sp.packages {
		if !taskIDRe.MatchString(p.id) {
			continue
		}
		path := filepath.Join(s.root, p.id, "task.md")
		if _, err := os.Stat(path); err != nil {
			status[p.id] = "created"
			continue
		}
		fm, _, _ := readTask(path)
		if fm["spec"] == rel+"#"+p.id {
			status[p.id] = "exists"
		} else {
			sp.add("task id %s is already used by another task (%s)", p.id, path)
		}
	}

	if n := len(sp.problems); n > 0 {
		fmt.Fprintf(a.out, "problems[%d]:\n", n)
		for _, p := range sp.problems {
			fmt.Fprintf(a.out, "  %s\n", toonValue(p))
		}
		noun := "problems"
		if n == 1 {
			noun = "problem"
		}
		return fail("spec has %d %s; nothing was created", n, noun)
	}

	for _, p := range sp.packages {
		if status[p.id] != "created" {
			continue
		}
		if *dry {
			status[p.id] = "would-create"
			continue
		}
		if _, err := newTask(s, sp.taskOpts(p, rel, baseBranch)); err != nil {
			return err
		}
	}

	waves, _ := sp.waves()
	fmt.Fprintf(a.out, "spec: %s\npackages: %d\n", toonValue(rel), len(sp.packages))
	fmt.Fprintf(a.out, "tasks[%d]{id,kind,model,depends,status}:\n", len(sp.packages))
	for _, p := range sp.packages {
		deps := ""
		if len(p.deps) > 0 {
			deps = toonValue(strings.Join(p.deps, " "))
		}
		fmt.Fprintf(a.out, "  %s,%s,%s,%s,%s\n", toonValue(p.id), toonValue(p.kind), toonValue(p.model), deps, status[p.id])
	}
	fmt.Fprintf(a.out, "waves[%d]:\n", len(waves))
	for _, w := range waves {
		fmt.Fprintf(a.out, "  %s\n", toonValue(strings.Join(w, " ")))
	}
	fmt.Fprintln(a.out, "help[2]:\n  orch check <id>\n  orch spawn <id>")
	return nil
}

func (sp *spec) taskOpts(p *specPackage, rel, base string) taskOpts {
	in := strings.Join(p.owns, ", ")
	if in == "" {
		in = "research only, no repo edits"
	}
	var others []string
	for _, q := range sp.packages {
		if q != p {
			others = append(others, q.owns...)
		}
	}
	out := "anything not listed in In"
	if len(others) > 0 {
		out = "files owned by other packages of this spec: " + strings.Join(others, ", ")
	}
	auth := sp.sections["Authority"]
	return taskOpts{
		id: p.id, kind: p.kind, harness: p.harness, model: p.model, effort: p.effort, base: base,
		deps: p.deps, spec: rel + "#" + p.id,
		fill: map[string]string{
			"Intent": fmt.Sprintf("%s: %s. The whole feature is in `%s`; read its Behaviour and Design rules first.", sp.title, p.title, rel),
			"Scope":  "In: " + in + "\nOut: " + out,
			"Pointers": fmt.Sprintf("`%s` ## Behaviour - what the feature does\n`%s` ## Design rules - decisions already made\n`%s` ### %s - this package",
				rel, rel, rel, p.id),
			"Spec":       p.body,
			"Acceptance": p.acceptance,
			"Authority": fmt.Sprintf("May decide: %s\nMust escalate: %s\nNever: %s",
				labelled(auth, "May decide"), labelled(auth, "Must escalate"), labelled(auth, "Never")),
		},
	}
}
