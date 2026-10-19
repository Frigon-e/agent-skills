package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// orch ctx: the retrieval layer as commands, for any git repo, with no run
// state. Output is TOON on stdout (--json for JSON), results grouped by file
// with totals, a definite sentence for an empty result, structured errors
// (exit 2 for a usage error), and a trailing `sources:` line.

const ctxVersion = "1.0.0"

var ctxSubcommands = []string{"status", "refresh", "watch", "find", "sym", "text", "batch", "brief", "read", "refs"}

type ctxHelp struct {
	usage, summary string
	examples       []string
}

var ctxHelps = map[string]ctxHelp{
	"read":    {"orch ctx read <path> [--from N --to N]", "For scripts; agents use their native read tool. Read a bounded repo-relative range.", []string{"for scripts: orch ctx read internal/pay.go --from 10 --to 40", "for scripts: orch ctx read README.md --json"}},
	"status":  {"orch ctx status [--json]", "Index revision against HEAD, the watch daemon and the tools in use.", []string{"orch ctx status", "orch ctx status --json"}},
	"refresh": {"orch ctx refresh [--full] [--json]", "Make the jbcontext index current as ctx_index allows; --full also builds a missing index.", []string{"orch ctx refresh", "orch ctx refresh --full"}},
	"watch":   {"orch ctx watch add|remove [dir]", "Keep (or stop keeping) an overlay index of uncommitted changes; never starts the daemon.", []string{"orch ctx watch add", "orch ctx watch remove ~/code/app"}},
	"find": {`orch ctx find "<concept>" [flags]`, "Semantic search: where a concept lives, each chunk in full with the declarations in it and the size of its package. A ranked guess, not proof; confirm absence with text.",
		[]string{`orch ctx find "refund retry policy" --limit 3`, `orch ctx find "webhook signature check" -p internal/ --tests include`, `orch ctx find "refund retry policy" --limit 8 --page 2`}},
	"sym": {"orch ctx sym <Name|pkg.Name|Type.Method> [flags]", "An exact symbol: definition with doc comment and body preview, callers outside tests (line, enclosing function), implementers, where it is wired. Tests are counted, not listed.",
		[]string{"orch ctx sym StartCheckout", "orch ctx sym StartCheckout --refs 3 --impl=false", "orch ctx sym StartCheckout --limit 5 --page 2"}},
	"text": {"orch ctx text <pattern> [flags]", "ripgrep with smart-case, skipping lockfiles, minified, generated and vendored files. Hits are grouped by file with the enclosing function and 2 lines of context. The pattern is a regular expression; --fixed makes it literal.",
		[]string{`orch ctx text polar --glob 'internal/**' --word`, `orch ctx text 'func \(s \*Store\)' --type go --limit 20`, `orch ctx text Gateway --limit 6 --page 2`}},
	"batch": {"orch ctx batch [<plan.json|->] [-q kind:query key=value ...] [flags]", "Many lookups in one call, run in parallel from the cache, answered in plan order. A place hit twice is printed once; each answer is held to 9000 characters and the batch to 70000, so past about 8 lookups add limit=N to the broad ones. The read kind is for scripts; agents use their native read tool.",
		[]string{`orch ctx batch plan.json`, `orch ctx batch -q "symbol:StartCheckout refs=3" -q "text:polar tests=none glob=internal/**" -q "text:polar limit=6 page=2"`, `echo '{"lookups":[{"kind":"text","query":"x"}]}' | orch ctx batch -`}},
	"brief": {"orch ctx brief <file|-> [--owns a,b,c] [flags]", "Ranked files, interfaces, look-alikes outside the owns and names found nowhere, for a piece of text.",
		[]string{"orch ctx brief task.md --owns internal/billing", "echo 'add a retry to StartCheckout' | orch ctx brief -"}},
}

func cmdCtx(a *app, args []string) error {
	readonly := ctxReadOnly()
	if len(args) > 0 && args[0] == "--readonly" {
		readonly, args = true, args[1:]
	}
	if readonly {
		root, err := ctxPinnedRoot()
		if err != nil {
			return ctxErr(a, err.Error())
		}
		copy := *a
		copy.cwd = root
		a = &copy
		if len(args) == 0 || !ctxReadCommand(args[0]) {
			return ctxErr(a, "read-only ctx allows only find, sym, text, read, refs and batch")
		}
	}
	if len(args) == 0 {
		return ctxHome(a)
	}
	switch args[0] {
	case "--version", "-v", "-V":
		fmt.Fprintln(a.out, ctxVersion)
		return nil
	case "--help", "-h", "help":
		return ctxHome(a)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "read":
		return ctxReadCmd(a, rest)
	case "refs":
		return ctxSymCmd(a, rest)
	case "status":
		return ctxStatusCmd(a, rest)
	case "refresh":
		return ctxRefreshCmd(a, rest)
	case "watch":
		return ctxWatchCmd(a, rest)
	case "find":
		return ctxFindCmd(a, rest)
	case "sym":
		return ctxSymCmd(a, rest)
	case "text":
		return ctxTextCmd(a, rest)
	case "batch":
		return ctxBatchCmd(a, rest)
	case "brief":
		return ctxBriefCmd(a, rest)
	}
	return ctxErr(a, "unknown subcommand "+sub, "valid subcommands: "+strings.Join(ctxSubcommands, ", ")+" (orch ctx with no arguments shows the state)")
}

// ctxErr prints a structured error on stdout and returns a usage error (exit 2).
func ctxErr(a *app, msg string, help ...string) error {
	fmt.Fprintf(a.out, "error: %s\n", msg)
	switch len(help) {
	case 0:
	case 1:
		fmt.Fprintf(a.out, "help: %s\n", help[0])
	default:
		fmt.Fprintf(a.out, "help[%d]:\n", len(help))
		for _, h := range help {
			fmt.Fprintf(a.out, "  %s\n", h)
		}
	}
	return &exitError{code: 2}
}

func flagName(n string) string {
	if len(n) == 1 {
		return "-" + n
	}
	return "--" + n
}

// writeHelp prints the concise reference of one subcommand.
func writeHelp(a *app, name string, fs *flag.FlagSet) {
	h := ctxHelps[name]
	if name == "find" {
		fmt.Fprint(a.out, ctxQueryGuide+"\n")
	}
	fmt.Fprintf(a.out, "usage: %s\ndescription: %s\n", h.usage, h.summary)
	if fs != nil {
		var rows []string
		fs.VisitAll(func(f *flag.Flag) {
			rows = append(rows, fmt.Sprintf("  %s,%s,%s", flagName(f.Name), toonValue(f.DefValue), toonValue(f.Usage)))
		})
		if len(rows) > 0 {
			fmt.Fprintf(a.out, "flags[%d]{flag,default,meaning}:\n%s\n", len(rows), strings.Join(rows, "\n"))
		}
	}
	fmt.Fprintf(a.out, "examples[%d]:\n", len(h.examples))
	for _, e := range h.examples {
		fmt.Fprintf(a.out, "  %s\n", e)
	}
}

// ctxParse parses flags anywhere among the arguments. --help prints the
// reference and reports done; an unknown flag is a structured error (exit 2)
// that lists the valid ones.
func ctxParse(a *app, name string, fs *flag.FlagSet, args []string) (pos []string, done bool, err error) {
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	for _, s := range args {
		if s == "--" {
			break
		}
		if s == "-h" || s == "--help" {
			writeHelp(a, name, fs)
			return nil, true, nil
		}
	}
	var tail []string
	for i, s := range args {
		if s == "--" {
			tail, args = args[i+1:], args[:i]
			break
		}
	}
	for {
		if perr := fs.Parse(args); perr != nil {
			msg := strings.TrimPrefix(perr.Error(), "flag provided but not defined: ")
			var names []string
			fs.VisitAll(func(f *flag.Flag) { names = append(names, flagName(f.Name)) })
			if msg != perr.Error() {
				return nil, true, ctxErr(a, "unknown flag "+flagName(strings.TrimLeft(msg, "-"))+" for `ctx "+name+"`", "valid flags for `ctx "+name+"`: "+strings.Join(names, ", ")+" (--help shows examples)")
			}
			return nil, true, ctxErr(a, strings.ReplaceAll(perr.Error(), "\n", " ")+" for `ctx "+name+"`", "valid flags for `ctx "+name+"`: "+strings.Join(names, ", "))
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	return append(pos, tail...), false, nil
}

// ctxCommon are the flags every lookup command shares.
type ctxCommon struct {
	json    *bool
	full    *bool
	fields  *string
	compact *bool
}

func addCommon(fs *flag.FlagSet) ctxCommon {
	return ctxCommon{
		json:    fs.Bool("json", false, "print JSON instead of TOON"),
		full:    fs.Bool("full", false, "lift the caps on hits, line width and answer size"),
		fields:  fs.String("fields", "", "extra fields: "+strings.Join(ctxFieldNames, ", ")),
		compact: fs.Bool("compact", false, "the shortest answer: counts, signatures and first lines, no context or callers"),
	}
}

func (c ctxCommon) fmt(a *app) (ctxFmt, error) {
	f := ctxFmt{Full: *c.full, Compact: *c.compact, Fields: map[string]bool{}}
	for _, n := range strings.Split(*c.fields, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		ok := false
		for _, v := range ctxFieldNames {
			ok = ok || v == n
		}
		if !ok {
			return f, ctxErr(a, "unknown field "+n, "valid fields: "+strings.Join(ctxFieldNames, ", "))
		}
		f.Fields[n] = true
	}
	return f, nil
}

func addTests(fs *flag.FlagSet) *string {
	return fs.String("tests", "none", "test files: none (hidden, counted), include or only")
}

func addGenerated(fs *flag.FlagSet) *string {
	return fs.String("generated", "none", "generated files: none (hidden, counted), include or only")
}

func checkTests(a *app, t string) error {
	if !validTests(t) {
		return ctxErr(a, "unknown value for --tests: "+t, "valid values: none, include, only")
	}
	return nil
}

// ctxEnvFor is the env of the checkout the command runs in. Work marked
// private (the lead's task.md) never uses jbcontext.
func ctxEnvFor(a *app) (*ctxEnv, error) {
	cwd := a.cwd
	if ctxReadOnly() {
		root, err := ctxPinnedRoot()
		if err != nil {
			return nil, err
		}
		cwd = root
	}
	top, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return nil, fail("not inside a git repository: %s", a.cwd)
	}
	cfg, err := loadConfig(top)
	if err != nil {
		return nil, err
	}
	private := false
	if dir := os.Getenv("ORCH_TASK_DIR"); dir != "" {
		if fm, _, err := readTask(filepath.Join(dir, "task.md")); err == nil && fm["private"] == "true" {
			private = true
		}
	}
	return newCtxEnv(top, ctxOpts{Cfg: cfg, Private: private}), nil
}

// emit prints the body then the notes and the sources line; with asJSON it
// prints the data, the notes and the sources as JSON instead.
func (a *app) ctxEmit(e *ctxEnv, asJSON bool, body string, data any) error {
	if asJSON {
		b, err := json.MarshalIndent(map[string]any{"result": data, "notes": e.Notes(), "sources": e.Sources()}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, string(b))
		return nil
	}
	fmt.Fprint(a.out, strings.TrimRight(body, "\n")+"\n")
	if notes := e.Notes(); len(notes) > 0 {
		fmt.Fprintf(a.out, "notes[%d]:\n", len(notes))
		for _, n := range notes {
			fmt.Fprintf(a.out, "  %s\n", toonValue(n))
		}
	}
	fmt.Fprintf(a.out, "sources: %s\n", e.Sources())
	return nil
}

// ---- home and status ----

func homePath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}

// ctxHome is `orch ctx` with no arguments: the live state, then hints.
func ctxHome(a *app) error {
	exe, _ := os.Executable()
	fmt.Fprintf(a.out, "bin: %s\ndescription: Code facts for any git repo (concepts, symbols, exact text, batches, briefs) from tools, no model\n", homePath(exe))
	e, err := ctxEnvFor(a)
	if err != nil {
		fmt.Fprintf(a.out, "repo: none (not inside a git repository)\nhelp[1]:\n  cd into a git repository, then run `orch ctx`\n")
		return nil
	}
	head, _ := git(e.Code, "rev-parse", "HEAD")
	porcelain, _ := git(e.Code, "status", "--porcelain")
	dirty := ""
	if strings.TrimSpace(porcelain) != "" {
		dirty = " (uncommitted changes)"
	}
	fmt.Fprintf(a.out, "repo: %s\nhead: %s%s\n", e.Code, sha7(head), dirty)
	fmt.Fprintf(a.out, "index: %s\n", indexSummary(e, head))
	ready := ctxReadiness(e)
	fmt.Fprintf(a.out, "%s\n", strings.Replace(ready, "ctx: ", "tools: ", 1))
	fmt.Fprint(a.out, "find: describe one behavior in plain language, e.g. `Code that checks a user's credentials and establishes a signed-in session.`; not a noun list\n")
	fmt.Fprint(a.out, "files: Read files with your native read tool (offset and limit for part of a file); orch ctx is for finding things, not for reading them; never use sed, cat, head or tail.\n")
	fmt.Fprint(a.out, "help[5]:\n"+
		"  orch ctx text <pattern> [--glob G] [--word]    exact strings, and their absence\n"+
		"  orch ctx sym <Name> [--refs N]                 an exact symbol\n"+
		"  orch ctx find \"Code that lists payment gateways\"   where something lives, in the form \"Code that <does X>\"\n"+
		"  orch ctx batch -q \"symbol:X\" -q \"text:Y\"       several lookups in one call\n"+
		"  orch ctx <subcommand> --help                   flags and examples\n")
	return nil
}

// indexSummary is one phrase about the jbcontext index.
func indexSummary(e *ctxEnv, head string) string {
	if e.NoJB {
		return "off (" + e.NoJBWhy + ")"
	}
	if which(e.JB) == "" {
		return "jbcontext missing"
	}
	revs, ok := e.jbIndexed()
	switch {
	case !ok:
		return "status unavailable"
	case len(revs) == 0:
		return "none (orch ctx refresh builds one under ctx_index = full)"
	}
	for _, r := range revs {
		if r == head {
			return "fresh (" + sha7(head) + ")"
		}
	}
	return "stale (at " + sha7(revs[0]) + "; orch ctx refresh)"
}

type ctxStatus struct {
	Repo        string       `json:"repo"`
	Head        string       `json:"head"`
	Dirty       bool         `json:"dirty"`
	Policy      string       `json:"ctx_index"`
	JBcontext   string       `json:"jbcontext"` // path, or "" when missing
	Disabled    string       `json:"jbcontext_disabled,omitempty"`
	Indexed     string       `json:"indexed_revision,omitempty"`
	IndexFresh  bool         `json:"index_fresh"`
	WatchPID    string       `json:"watch_pid,omitempty"`
	WatchRoots  []string     `json:"watch_roots,omitempty"`
	Watched     bool         `json:"watched"`
	WatchStatus string       `json:"watch_status,omitempty"`
	Gopls       string       `json:"gopls"`
	TSServer    string       `json:"ts_server"`
	RG          string       `json:"rg"`
	Languages   []langStatus `json:"languages,omitempty"`
}

func ctxStatusCmd(a *app, args []string) error {
	fs := a.flags("ctx status")
	asJSON := fs.Bool("json", false, "print JSON instead of TOON")
	if _, done, err := ctxParse(a, "status", fs, args); done {
		return err
	}
	e, err := ctxEnvFor(a)
	if err != nil {
		return err
	}
	st := ctxStatus{Repo: e.Code, Policy: e.Policy, Gopls: which(e.Gopls), RG: which(e.RG)}
	if p := tsServerPath(e.Code); p != "" {
		st.TSServer = p
		e.use("ts server", p)
	}
	if st.Gopls != "" {
		e.use("gopls", st.Gopls)
	}
	if st.RG != "" {
		e.use("rg", st.RG)
	}
	st.Head, _ = git(e.Code, "rev-parse", "HEAD")
	porcelain, _ := git(e.Code, "status", "--porcelain")
	st.Dirty = strings.TrimSpace(porcelain) != ""
	var b strings.Builder
	fmt.Fprintf(&b, "repo: %s\nhead: %s\ndirty: %v\nctx_index: %s\n", e.Code, sha7(st.Head), st.Dirty, e.Policy)
	switch {
	case e.NoJB:
		st.Disabled = e.NoJBWhy
		fmt.Fprintf(&b, "jbcontext: disabled (%s)\n", e.NoJBWhy)
	case which(e.JB) == "":
		fmt.Fprintf(&b, "jbcontext: missing (%s)\n", e.JB)
	default:
		st.JBcontext = which(e.JB)
		revs, ok := e.jbIndexed()
		if ok && len(revs) > 0 {
			st.Indexed = revs[0]
			for _, r := range revs {
				st.IndexFresh = st.IndexFresh || r == st.Head
			}
			if st.IndexFresh {
				st.Indexed = st.Head
			}
			e.use("jbcontext", "index "+sha7(st.Indexed)+" (HEAD "+sha7(st.Head)+map[bool]string{true: "", false: ", stale"}[st.IndexFresh]+")")
		}
		fmt.Fprintf(&b, "jbcontext: %s\nindex: %s\n", st.JBcontext, indexSummary(e, st.Head))
		line, pid, roots := e.watchStatus()
		st.WatchStatus, st.WatchPID, st.WatchRoots = line, pid, roots
		for _, r := range roots {
			st.Watched = st.Watched || evalOr(r) == evalOr(e.Code)
		}
		fmt.Fprintf(&b, "watch: %s\nwatched: %v\n", toonValue(line), st.Watched)
		if len(roots) > 0 {
			fmt.Fprintf(&b, "watch_roots[%d]:\n", len(roots))
			for _, r := range roots {
				fmt.Fprintf(&b, "  %s\n", toonValue(r))
			}
		}
	}
	rows := [][2]string{{"gopls", st.Gopls}, {"ts_server", st.TSServer}, {"rg", st.RG}}
	fmt.Fprintf(&b, "tools[%d]{name,path}:\n", len(rows))
	for _, t := range rows {
		fmt.Fprintf(&b, "  %s,%s\n", t[0], toonValue(orDefault(t[1], "missing")))
	}
	st.Languages = e.langStatuses()
	if len(st.Languages) > 0 {
		fmt.Fprintf(&b, "languages[%d]{name,files,server,hint}:\n", len(st.Languages))
		for _, l := range st.Languages {
			fmt.Fprintf(&b, "  %s,%d,%s,%s\n", l.ID, l.Files, toonValue(orDefault(l.Server, "missing")), toonValue(l.Hint))
		}
	}
	return a.ctxEmit(e, *asJSON, b.String(), st)
}

// ---- refresh and watch ----

func ctxRefreshCmd(a *app, args []string) error {
	fs := a.flags("ctx refresh")
	asJSON := fs.Bool("json", false, "print JSON instead of TOON")
	full := fs.Bool("full", false, "build a full index when the repo has none, whatever ctx_index says (never past off or ctx_deny)")
	if _, done, err := ctxParse(a, "refresh", fs, args); done {
		return err
	}
	e, err := ctxEnvFor(a)
	if err != nil {
		return err
	}
	if why := e.jbMissing(); why != "" {
		return a.ctxEmit(e, *asJSON, "refresh: nothing to do ("+why+")\n", map[string]string{"skipped": why})
	}
	st := e.Refresh(*full)
	e.use("jbcontext", e.indexDetail())
	msg := "refresh: index is current (" + sha7(st.rev) + ")"
	if st.rev == "" {
		msg = "refresh: index is not current (HEAD " + sha7(st.head) + ")"
	}
	return a.ctxEmit(e, *asJSON, msg+"\n", map[string]any{"head": st.head, "indexed": st.indexd, "current": st.rev != ""})
}

func ctxWatchCmd(a *app, args []string) error {
	fs := a.flags("ctx watch")
	pos, done, err := ctxParse(a, "watch", fs, args)
	if done {
		return err
	}
	if len(pos) == 0 || (pos[0] != "add" && pos[0] != "remove") {
		return ctxErr(a, "ctx watch needs add or remove", "orch ctx watch add|remove [dir]")
	}
	e, err := ctxEnvFor(a)
	if err != nil {
		return err
	}
	if why := e.jbMissing(); why != "" {
		return ctxErr(a, why)
	}
	dir := e.Code
	if len(pos) > 1 {
		dir = expandHome(pos[1])
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(a.cwd, dir)
		}
	}
	out, err := exec.Command(e.JB, "watch", pos[0], "--project-path", dir).CombinedOutput()
	if err != nil {
		return fail("jbcontext watch %s failed: %v", pos[0], err)
	}
	fmt.Fprintf(a.out, "watch: %s %s\n", pos[0], toonValue(dir))
	if s := strings.TrimSpace(string(out)); s != "" {
		fmt.Fprintf(a.out, "detail: %s\n", toonValue(s))
	}
	return nil
}

var watchPIDRe = regexp.MustCompile(`pid (\d+)`)

// watchStatus reads `jbcontext watch status`: whether the daemon runs and the
// roots it watches.
func (e *ctxEnv) watchStatus() (line, pid string, roots []string) {
	ctx, cancel := context.WithTimeout(context.Background(), jbStatusLimit)
	defer cancel()
	out, err := exec.CommandContext(ctx, e.JB, "watch", "status").Output()
	if err != nil {
		return "unavailable", "", nil
	}
	for i, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		l = strings.TrimSpace(l)
		if i == 0 {
			line = l
			if m := watchPIDRe.FindStringSubmatch(l); m != nil {
				pid = m[1]
			}
			continue
		}
		if f := strings.Fields(l); len(f) >= 2 && strings.HasPrefix(f[len(f)-1], "/") {
			roots = append(roots, f[len(f)-1])
		}
	}
	return line, pid, roots
}

func which(bin string) string {
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	return ""
}

// ---- find, sym, text ----

func ctxFindCmd(a *app, args []string) error {
	fs := a.flags("ctx find")
	path := fs.String("p", "", "only files under this path")
	limit := fs.Int("limit", 0, "results per page (default 8)")
	maxN := fs.Int("max", 0, "same as --limit")
	page := fs.Int("page", 0, "1-based page of the ranked list (default 1)")
	tests := addTests(fs)
	gen := addGenerated(fs)
	common := addCommon(fs)
	pos, done, err := ctxParse(a, "find", fs, args)
	if done {
		return err
	}
	if len(pos) != 1 {
		return ctxErr(a, "ctx find needs one concept in quotes", ctxHelps["find"].usage)
	}
	visited := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { visited[fl.Name] = true })
	if err := checkPaging(a, visited, map[string]int{"limit": *limit, "max": *maxN, "page": *page}); err != nil {
		return err
	}
	f, err := common.fmt(a)
	if err != nil {
		return err
	}
	if err := checkTests(a, *tests); err != nil {
		return err
	}
	if err := checkGenerated(a, *gen); err != nil {
		return err
	}
	e, err := ctxEnvFor(a)
	if err != nil {
		return err
	}
	v := e.conceptLookup(ctxLookup{Kind: "concept", Query: pos[0], Path: *path, Limit: *limit, Max: *maxN, Page: *page, Tests: *tests, Gen: *gen}, f)
	return a.ctxView(e, *common.json, f, v)
}

func (a *app) ctxView(e *ctxEnv, asJSON bool, f ctxFmt, v *ctxView) error {
	var b strings.Builder
	v.write(&b, f, nil, 0, f.answerCap())
	return a.ctxEmit(e, asJSON, b.String(), v)
}

func ctxSymCmd(a *app, args []string) error {
	fs := a.flags("ctx sym")
	refs := fs.Int("refs", symRefsDefault, "list this many callers outside tests, each with its line and enclosing function (0: only their count)")
	ctxN := fs.Int("C", 0, "lines above each caller (default 0)")
	impl := fs.Bool("impl", true, "list the implementers of an interface method (--impl=false hides them)")
	kind := fs.String("kind", "", "only definitions of this kind (Function, Method, Variable, Interface, ...)")
	in := fs.String("in", "", "only definitions under this path")
	limit := fs.Int("limit", 0, "callers and implementers per page (default: --refs for callers, 10 for implementers)")
	page := fs.Int("page", 0, "1-based page of the callers and implementers (default 1)")
	tests := addTests(fs)
	gen := addGenerated(fs)
	common := addCommon(fs)
	pos, done, err := ctxParse(a, "sym", fs, args)
	if done {
		return err
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if len(pos) != 1 {
		return ctxErr(a, "ctx sym needs one symbol name", ctxHelps["sym"].usage)
	}
	if err := checkPaging(a, visited, map[string]int{"limit": *limit, "page": *page}); err != nil {
		return err
	}
	f, err := common.fmt(a)
	if err != nil {
		return err
	}
	if err := checkTests(a, *tests); err != nil {
		return err
	}
	if err := checkGenerated(a, *gen); err != nil {
		return err
	}
	e, err := ctxEnvFor(a)
	if err != nil {
		return err
	}
	o := defaultSymOpts(f)
	if visited["refs"] {
		o.Refs = *refs
	}
	if visited["impl"] {
		o.Impl = *impl
	}
	if visited["C"] {
		o.Context = *ctxN
	}
	o.Tests, o.Kind, o.In, o.Generated = *tests, *kind, *in, *gen
	o.Limit, o.Page = *limit, *page
	v := e.symView(e.Sym(pos[0]), o)
	return a.ctxView(e, *common.json, f, v)
}

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

func ctxTextCmd(a *app, args []string) error {
	fs := a.flags("ctx text")
	l := ctxLookup{Kind: "text"}
	both := func(short, long string, p *bool, usage string) {
		fs.BoolVar(p, long, false, usage)
		fs.BoolVar(p, short, false, "same as --"+long)
	}
	fs.StringVar(&l.Path, "p", "", "only files under this path")
	both("F", "fixed", &l.Fixed, "the pattern is a literal string, not a regular expression")
	both("w", "word", &l.Word, "match whole words only")
	both("i", "ignore-case", &l.IgnoreCase, "ignore case (default: smart-case)")
	var globs stringsFlag
	fs.Var(&globs, "glob", "only files matching this glob; repeatable; prefix ! to exclude")
	fs.StringVar(&l.Type, "type", "", "only this file type (go, ts, md, ...)")
	both("l", "files-only", &l.FilesOnly, "list the files that match")
	both("c", "count", &l.Count, "count matches per file")
	ctxN := fs.Int("C", ctxTune.Context, fmt.Sprintf("lines of context around a match (default %d; 0 with --compact)", ctxTune.Context))
	both("U", "multiline", &l.Multiline, "let a pattern match across lines")
	both("o", "only-matching", &l.OnlyMatching, "print only the matched text")
	fs.IntVar(&l.PerFile, "per-file", 0, "most matches per file")
	fs.IntVar(&l.Limit, "limit", 0, "hits per page (default 10)")
	fs.IntVar(&l.Max, "max", 0, "same as --limit")
	fs.IntVar(&l.Page, "page", 0, "1-based page of the hits, sorted by path then line (default 1)")
	fs.BoolVar(&l.All, "all", false, "also search lockfiles, generated, minified and vendored files")
	tests := addTests(fs)
	gen := addGenerated(fs)
	common := addCommon(fs)
	pos, done, err := ctxParse(a, "text", fs, args)
	if done {
		return err
	}
	if len(pos) != 1 {
		return ctxErr(a, "ctx text needs one pattern", ctxHelps["text"].usage)
	}
	f, err := common.fmt(a)
	if err != nil {
		return err
	}
	if err := checkTests(a, *tests); err != nil {
		return err
	}
	if err := checkGenerated(a, *gen); err != nil {
		return err
	}
	l.Query, l.Glob, l.Tests, l.Gen = pos[0], strList(globs), *tests, *gen
	visited := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) {
		visited[fl.Name] = true
		if fl.Name == "C" {
			l.Context = ctxN
		}
	})
	if err := checkPaging(a, visited, map[string]int{"limit": l.Limit, "max": l.Max, "page": l.Page}); err != nil {
		return err
	}
	if err := l.validate(); err != nil {
		return ctxErr(a, err.Error())
	}
	e, err := ctxEnvFor(a)
	if err != nil {
		return err
	}
	return a.ctxView(e, *common.json, f, e.textLookup(l, f))
}

// ---- batch and brief ----

const ctxBatchMax = 50

func ctxBatchCmd(a *app, args []string) error {
	fs := a.flags("ctx batch")
	var inline stringsFlag
	fs.Var(&inline, "q", `an inline lookup, "kind:query key=value ..."; repeatable`)
	tests := addTests(fs)
	common := addCommon(fs)
	pos, done, err := ctxParse(a, "batch", fs, args)
	if done {
		return err
	}
	f, err := common.fmt(a)
	if err != nil {
		return err
	}
	if err := checkTests(a, *tests); err != nil {
		return err
	}
	if len(pos) > 1 || (len(pos) == 0 && len(inline) == 0) {
		return ctxErr(a, "ctx batch needs a plan (file or -) or at least one -q lookup", ctxHelps["batch"].usage)
	}
	var lks []ctxLookup
	if len(pos) == 1 {
		raw, err := readArg(a, pos[0])
		if err != nil {
			return err
		}
		var plan struct {
			Lookups []ctxLookup `json:"lookups"`
		}
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&plan); err != nil {
			return ctxErr(a, "the plan is not valid: "+err.Error(), `plan format: {"lookups":[{"kind":"concept|symbol|text|read","query":"...","why":"", ...options}]}`, "option keys: "+strings.Join(ctxOptionKeys, ", "))
		}
		lks = plan.Lookups
	}
	for _, q := range inline {
		l, err := parseInlineLookup(q)
		if err != nil {
			return ctxErr(a, err.Error())
		}
		lks = append(lks, l)
	}
	if len(lks) == 0 {
		return ctxErr(a, "the plan holds no lookups", ctxHelps["batch"].usage)
	}
	if len(lks) > ctxBatchMax {
		return ctxErr(a, fmt.Sprintf("the plan holds %d lookups; at most %d at once", len(lks), ctxBatchMax))
	}
	for i := range lks {
		if lks[i].Tests == "" && *tests != "none" {
			lks[i].Tests = *tests
		}
	}
	e, err := ctxEnvFor(a)
	if err != nil {
		return err
	}
	views, err := e.BatchViews(lks, f)
	if err != nil {
		return ctxErr(a, err.Error(), "valid kinds: "+strings.Join(ctxKinds, ", "), "valid options: "+strings.Join(ctxOptionKeys, ", "))
	}
	return a.ctxEmit(e, *common.json, renderBatch(views, f), views)
}

func readArg(a *app, name string) (string, error) {
	var r io.Reader = a.stdin
	if name != "-" {
		path := expandHome(name)
		if ctxReadOnly() {
			root, err := ctxPinnedRoot()
			if err != nil {
				return "", err
			}
			if !ctxWithinRoot(root, path) {
				return "", fail("read-only ctx: file %s is outside the pinned root", name)
			}
			path = filepath.Join(root, path)
		}
		f, err := os.Open(path)
		if err != nil {
			return "", fail("%v", err)
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(r)
	return string(b), err
}

func ctxBriefCmd(a *app, args []string) error {
	fs := a.flags("ctx brief")
	owns := fs.String("owns", "", "comma-separated files or directories the work owns")
	size := fs.Int("max", 8000, "characters of the brief")
	common := addCommon(fs)
	pos, done, err := ctxParse(a, "brief", fs, args)
	if done {
		return err
	}
	if len(pos) != 1 {
		return ctxErr(a, "ctx brief needs a file or - for stdin", ctxHelps["brief"].usage)
	}
	f, err := common.fmt(a)
	if err != nil {
		return err
	}
	text, err := readArg(a, pos[0])
	if err != nil {
		return err
	}
	e, err := ctxEnvFor(a)
	if err != nil {
		return err
	}
	var own []string
	for _, o := range strings.Split(*owns, ",") {
		if o = strings.TrimSpace(o); o != "" {
			own = append(own, o)
		}
	}
	b := e.Briefs([]briefInput{{ID: "brief", Title: briefTitle(text), Text: text, Owns: own}})[0]
	body := e.briefTOON(b, f)
	if !f.Full && len(body) > *size {
		body = cutLines(strings.TrimRight(body, "\n"), *size) + " (--full or --max N lifts the cap)\n"
	}
	return a.ctxEmit(e, *common.json, body, b)
}

// briefTOON renders a brief: ranked files, the interface sheet, look-alikes
// outside the owns, and names found nowhere.
func (e *ctxEnv) briefTOON(b *ctxBrief, f ctxFmt) string {
	var w strings.Builder
	fmt.Fprintf(&w, "brief: %d files ranked, %d symbols, %d look-alikes outside the owns, %d names not found\n", len(b.FileRows), len(b.Facts), len(b.LikeRows), len(b.Names))
	if len(b.FileRows) > 0 {
		fmt.Fprintf(&w, "files[%d]{path,score,why}:\n", len(b.FileRows))
		for _, r := range b.FileRows {
			fmt.Fprintf(&w, "  %s,%d,%s\n", toonValue(r.Path), r.Score, toonValue(strings.Join(r.Why, "; ")))
		}
	}
	for _, fact := range b.Facts {
		v := e.symView(fact, symOpts{Compact: true})
		w.WriteString("\n")
		v.write(&w, f, nil, 0, 0)
	}
	if len(b.LikeRows) > 0 {
		fmt.Fprintf(&w, "look_alikes[%d]{at,via,text}:\n", len(b.LikeRows))
		for _, r := range b.LikeRows {
			text := ""
			if len(r.Snippet) > 0 {
				text = short(strings.TrimSpace(r.Snippet[0]), f.width())
			}
			fmt.Fprintf(&w, "  %s,%s,%s\n", toonValue(r.At), toonValue(r.Via), toonValue(text))
		}
	}
	if len(b.Names) > 0 {
		fmt.Fprintf(&w, "not_found[%d]:\n", len(b.Names))
		for _, n := range b.Names {
			fmt.Fprintf(&w, "  %s\n", toonValue(n))
		}
	} else {
		w.WriteString("not_found: 0 names missing\n")
	}
	return w.String()
}

// briefTitle is the first non-empty line of text, without markdown markers.
func briefTitle(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#-* ")); l != "" {
			return short(l, 120)
		}
	}
	return ""
}

// ctxReadiness is the one-line state of the retrieval layer for `orch doctor`:
// the index, the watch and the tools, and what to run when something is off.
func ctxReadiness(e *ctxEnv) string {
	var parts, fixes []string
	switch {
	case e.NoJB:
		parts = append(parts, "jbcontext off ("+e.NoJBWhy+")")
	case which(e.JB) == "":
		parts = append(parts, "jbcontext missing")
	default:
		head, _ := git(e.Code, "rev-parse", "HEAD")
		revs, ok := e.jbIndexed()
		fresh := false
		for _, r := range revs {
			fresh = fresh || r == head
		}
		switch {
		case !ok:
			parts = append(parts, "index status unavailable")
		case fresh:
			parts = append(parts, "index fresh")
		case len(revs) == 0:
			parts = append(parts, "no index")
			fixes = append(fixes, "orch ctx refresh")
		default:
			parts = append(parts, "index stale")
			fixes = append(fixes, "orch ctx refresh")
		}
		_, _, roots := e.watchStatus()
		watched := false
		for _, r := range roots {
			watched = watched || evalOr(r) == evalOr(e.Code)
		}
		if watched {
			parts = append(parts, "watch on")
		} else {
			parts = append(parts, "watch off")
			fixes = append(fixes, "orch ctx watch add")
		}
	}
	for _, t := range []struct {
		name string
		ok   bool
	}{{"gopls", which(e.Gopls) != ""}, {"ts server", tsServerPath(e.Code) != ""}, {"rg", which(e.RG) != ""}} {
		if t.ok {
			parts = append(parts, t.name+" ok")
		} else {
			parts = append(parts, t.name+" missing")
		}
	}
	if _, why := tsServerChoice(e.Code); why != "" {
		parts = append(parts, why)
		e.note(why)
	}
	line := "ctx: " + strings.Join(parts, ", ")
	if len(fixes) > 0 {
		line += " (run: " + strings.Join(fixes, "; ") + ")"
	}
	if sts := e.langStatuses(); len(sts) > 0 {
		var ls []string
		for _, s := range sts {
			ls = append(ls, s.String())
		}
		line += "\nctx languages: " + strings.Join(ls, "; ")
	}
	return line
}

// tsServerPath is the TypeScript server if it exists, else "".
func tsServerPath(code string) string {
	if p := tslsBin(code); p != "" && (fileExists(p) || which(p) != "") {
		return p
	}
	return ""
}
