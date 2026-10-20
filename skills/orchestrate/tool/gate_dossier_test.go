package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dossierRepo is a committed repo with a queue package and a spec of one
// package that owns it, names things that exist and things that do not.
func dossierRepo(t *testing.T) (code, src string) {
	t.Helper()
	t.Setenv("ORCH_CACHE_DIR", t.TempDir())
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is not installed")
	}
	t.Setenv("ORCH_GOPLS", filepath.Join(t.TempDir(), "no-gopls"))
	t.Setenv("ORCH_TSLS", filepath.Join(t.TempDir(), "no-tsls"))
	code = t.TempDir()
	for name, body := range map[string]string{
		"internal/queue/queue.go":      "package queue\n\n// Enqueue adds a transfer to the queue.\nfunc Enqueue() {}\n",
		"internal/queue/queue_test.go": "package queue\n",
		"internal/store.go":            "package internal\n\nfunc SendTransfer() {}\n// POST /api/transfers is served here\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(code, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(code, name), []byte(body), 0o644)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@e.com", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = code
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	src = planHeader + "### api: Queue API\nkind: ship\nowns: internal/queue\n\nBuild the queue. It adds `SendTransfer`, a `transfer_rows` table and `Nonexistent`, and serves POST /api/transfers. See internal/queue/queue.go:4 and internal/gone.go:9.\n\n#### Builds\n- Code that queues a transfer and serves POST /api/transfers.\n\n#### Acceptance\nTests pass.\n\n" + planTail
	return code, src
}

func buildDossier(t *testing.T, code, src, cache string) *dossierSet {
	t.Helper()
	sp := parseSpec(src)
	sp.validate()
	if len(sp.problems) > 0 {
		t.Fatalf("spec problems: %v", sp.problems)
	}
	env := newCtxEnv(code, ctxOpts{Skip: []string{"spec.md"}})
	env.CacheDir = cache
	ds := newDossierSet(env, sp, packageBlocks(sp, src))
	ds.build([]string{"api"})
	return ds
}

func TestDossierRanksFilesAndFindsWhatExistsAndWhatDoesNot(t *testing.T) {
	code, src := dossierRepo(t)
	jb, _, _ := fakeJbcontext(t)
	t.Setenv("ORCH_JBCONTEXT", jb)
	// The indexed Put chunk does not exist in this checkout. It must not
	// contribute semantic ranking points to the real SendTransfer text hit.
	stale := buildDossier(t, code, src, t.TempDir()).text("api")
	wantContains(t, stale, "`internal/store.go` (score 1: text): text `SendTransfer`",
		"- `internal/store.go` (lines 3, 4) in func SendTransfer via text `SendTransfer`; text `POST /api/transfers`\n    func SendTransfer() {}")
	if strings.Contains(stale, "concept search") {
		t.Fatalf("a stale semantic hit contributed ranking evidence:\n%s", stale)
	}
	// A truthful semantic anchor still gets the original cross-tool bonus.
	fakeJbResults(t, [][3]string{{"internal/store.go", "2", `\nfunc SendTransfer() {}`}})
	text := buildDossier(t, code, src, t.TempDir()).text("api")

	wantContains(t, text,
		"### 1. Ranked files", "### 2. Interface sheet", "### 3. Existing things that look like what this package builds (outside its owns)", "### 4. Names not found in completed bounded lookups (not proof of absence)",
		// owned and named in the text: 2 + 2 is not it; owned file also pointed at gets both reasons
		"`internal/queue/queue.go` (score 2: own): owned; named in the text",
		// a concept hit (2) and a rare rg hit (1) in the same file, plus 2 for two kinds agreeing
		"`internal/store.go` (score 5: jb+text): concept search; text `SendTransfer`",
		// outside the owns, with the line and a snippet
		"- `internal/store.go` (lines 3, 4) in func SendTransfer via concept \"SendTransfer Nonexistent transfer_rows queue api\"; concept \"code queues transfer serves post api transfers\"; concept \"internal queue\"\n    func SendTransfer() {}",
		// names that exist nowhere, and a pointer to a file that does not exist
		"- `Nonexistent`", "- `transfer_rows`", "- `internal/gone.go`")
	if strings.Contains(text, "`internal/queue/queue.go:") {
		t.Errorf("a hit inside the owns was listed as an existing thing:\n%s", text)
	}
	if strings.Contains(text, "`SendTransfer`\n") && strings.Contains(strings.SplitAfter(text, "### 4.")[1], "SendTransfer") {
		t.Errorf("a found name was listed as missing:\n%s", text)
	}
}

func TestDossierIsDeterministicCachedAndCapped(t *testing.T) {
	code, src := dossierRepo(t)
	t.Setenv("ORCH_JBCONTEXT", filepath.Join(t.TempDir(), "no-jbcontext"))
	cache := t.TempDir()
	first := buildDossier(t, code, src, cache).text("api")
	second := buildDossier(t, code, src, cache).text("api")
	if first != second {
		t.Errorf("a rebuild from the cache differs:\n%s\n---\n%s", first, second)
	}
	if entries, _ := filepath.Glob(filepath.Join(cache, "rg3-*.json")); len(entries) == 0 {
		t.Error("rg results were not cached")
	}

	// 60 owned files: 40 are taken, 12 are ranked, and long sections are cut between entries.
	for i := 0; i < 60; i++ {
		os.WriteFile(filepath.Join(code, "internal", "queue", fmt.Sprintf("f%02d.go", i)), []byte("package queue\n"), 0o644)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = code
	cmd.Run()
	env := newCtxEnv(code, ctxOpts{})
	if n := len(env.ownedFiles([]string{"internal/queue"})); n != dossierOwnsCap {
		t.Errorf("owned files = %d, want the cap %d", n, dossierOwnsCap)
	}
	d := buildDossier(t, code, src, t.TempDir()).docs["api"]
	if len(d.Files) != dossierFiles {
		t.Errorf("ranked files = %d, want %d", len(d.Files), dossierFiles)
	}
	long := make([]string, 200)
	for i := range long {
		long[i] = fmt.Sprintf("- entry %03d with some text", i)
	}
	got := capEntries(long, 500)
	if len(got) > 500+40 || !strings.HasSuffix(got, "more omitted\n") || strings.Contains(got, "entry 0") && !strings.Contains(got, "\n") {
		t.Errorf("capEntries = %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasPrefix(line, "- entry ") && !strings.HasPrefix(line, "- ... ") {
			t.Errorf("a line was cut: %q", line)
		}
	}
	if capEntries(nil, 10) != "(none)\n" {
		t.Error("an empty section is not marked")
	}
}

func TestDossierSheetHasSignatureDocAndCallers(t *testing.T) {
	d := &ctxEnv{Code: t.TempDir()}
	os.WriteFile(filepath.Join(d.Code, "a.go"), []byte("package a\n\n// Enqueue adds a transfer.\n// More detail.\nfunc Enqueue() {}\n"), 0o644)
	lines := d.Lines("a.go")
	if got := docLine(lines, 5); got != "Enqueue adds a transfer." {
		t.Errorf("docLine = %q", got)
	}
	if got := docLine([]string{"/**", " * Makes a schema.", " */", "export const x = 1"}, 4); got != "Makes a schema." {
		t.Errorf("jsdoc docLine = %q", got)
	}
	if got := docLine([]string{"package a", "", "func B() {}"}, 3); got != "" {
		t.Errorf("no comment gave %q", got)
	}
}

func TestDecodeLookupsSalvagesObjects(t *testing.T) {
	good, ok := decodeLookups(`text {"lookups":[{"package":"api","kind":"text","query":"Put"}]} more`)
	if !ok || len(good) != 1 || good[0].Query != "Put" {
		t.Fatalf("decode = %+v %v", good, ok)
	}
	salv, ok := decodeLookups(`{"lookups":[{"package":"api","kind":"text","query":"Put"},{"package":"api","kind":"symbol","query":"Enqueue"}, {"package":"api","kind":"rea`)
	if !ok || len(salv) != 2 || salv[1].Kind != "symbol" {
		t.Fatalf("salvage = %+v %v", salv, ok)
	}
	if _, ok := decodeLookups("no json here"); ok {
		t.Error("garbage decoded")
	}
}

func TestPlannerLookupsRunAndStayInsideTheRepo(t *testing.T) {
	code, src := dossierRepo(t)
	jb, _, _ := fakeJbcontext(t)
	t.Setenv("ORCH_JBCONTEXT", jb)
	ds := buildDossier(t, code, src, t.TempDir())
	long := strings.Repeat("x", 3000)
	os.WriteFile(filepath.Join(code, "internal", "long.txt"), []byte(long+"\n"), 0o644)
	ds.runLookups([]string{"api"}, []lookup{
		lk("api", "text", "SendTransfer", "", 0, 0, "who sends"),
		lk("api", "text", "SendTransfer", "", 0, 0, "who sends"), // duplicate
		lk("api", "read", "", "internal/queue/queue.go", 3, 5, ""),
		lk("api", "read", "", "../outside.txt", 1, 2, ""),
		lk("api", "read", "", "internal/long.txt", 1, 2, ""),
		lk("api", "concept", "queue transfers", "", 0, 0, ""),
		lk("api", "symbol", "Enqueue", "", 0, 0, ""),
		lk("nope", "text", "x", "", 0, 0, ""),
		lk("api", "bogus", "x", "", 0, 0, ""),
	})
	text := ds.text("api")
	wantContains(t, text, "### 5. Lookups the planner asked for", "- [text] `SendTransfer` - who sends", "internal/store.go:3:",
		"- [read] `internal/queue/queue.go:3-5`", "3: // Enqueue adds a transfer to the queue.", "error: path outside the repo",
		"- [concept] `queue transfers`", "no semantic candidates in the searched index window; this does not establish absence", "- [symbol] `Enqueue`", "`Enqueue` Function at `internal/queue/queue.go:4`", "unknown kind")
	if strings.Count(text, "- [text] `SendTransfer`") != 1 {
		t.Error("a duplicate lookup ran twice")
	}
	if strings.Contains(text, strings.Repeat("x", 1600)) {
		t.Error("a lookup result was not capped")
	}
	if n := len(ds.docs["api"].Planned); n != 7 {
		t.Errorf("planned entries = %d, want 7 (nine asked, one duplicate, one for another package)", n)
	}
}

func lk(pkg, kind, query, path string, from, to int, why string) lookup {
	return lookup{Package: pkg, ctxLookup: ctxLookup{Kind: kind, Query: query, Path: path, From: from, To: to, Why: why}}
}

func TestDossierNamesWhatOtherPackagesOwnAndHowTheyRelate(t *testing.T) {
	code, src := dossierRepo(t)
	jb, _, _ := fakeJbcontext(t)
	t.Setenv("ORCH_JBCONTEXT", jb)
	// internal/store.go is named by the api package and owned by web, or by nobody.
	web := func(deps string) string {
		return strings.Replace(src, planTail, "### web: Web\nkind: ship\n"+deps+"owns: internal/store.go\n\nBuild the web side.\n\n#### Builds\n- Code that shows a transfer.\n\n#### Acceptance\nTests pass.\n\n"+planTail, 1)
	}
	for _, c := range []struct {
		name, src, want string
	}{
		{"downstream", strings.Replace(web("depends: api\n"), "kind: ship\nowns: internal/queue", "kind: ship\nowns: internal/queue", 1), "- owned by web, which uses this package: internal/store.go"},
		{"upstream", strings.Replace(web(""), "### api: Queue API\nkind: ship\n", "### api: Queue API\nkind: ship\ndepends: web\n", 1), "- owned by web, which this package uses: internal/store.go"},
		{"unrelated", web(""), "- owned by web, unrelated to this package: internal/store.go"},
		{"unowned", src, "- owned by no package (existing code): internal/store.go"},
	} {
		text := buildDossier(t, code, c.src, t.TempDir()).text("api")
		wantContains(t, text, "### 3b. Not in our workspace\n", c.want)
		if strings.Contains(strings.SplitAfter(strings.SplitAfter(text, "### 3b.")[1], "### 4.")[0], "internal/queue/queue.go") {
			t.Errorf("%s: the package's own file was listed as foreign", c.name)
		}
	}
}

func TestScopeFindingsKnowWhoOwnsTheFileAndStripRemarks(t *testing.T) {
	_, src := dossierRepo(t)
	src = strings.Replace(src, planTail, "### web: Web\nkind: ship\ndepends: api\nowns: internal/web/\n\nWeb.\n\n#### Builds\n- Code that shows a transfer.\n\n#### Acceptance\nOk.\n\n### core: Core\nkind: ship\nowns: internal/core/\n\nCore.\n\n#### Builds\n- Code that stores a transfer.\n\n#### Acceptance\nOk.\n\n"+planTail, 1)
	src = strings.Replace(src, "### api: Queue API\nkind: ship\n", "### api: Queue API\nkind: ship\ndepends: core\n", 1)
	sp := parseSpec(src)
	sp.validate()
	if len(sp.problems) > 0 {
		t.Fatalf("spec problems: %v", sp.problems)
	}
	rec := &gateRecord{}
	scopeFor("api", scopeReply{WillChange: []string{
		"internal/web/signup.go",                                  // owned by a package that builds on api: it wires api in
		"internal/core/session.go",                                // owned by a package api builds on
		"internal/other/thing.go",                                 // owned by nobody
		"internal/queue/review.go (new review queue, thresholds)", // api's own, with a remark
		"internal/queue/extra.go (new)",
	}}, rec, t.TempDir(), sp)
	got := map[string]string{}
	for _, f := range rec.Findings {
		got[f.Evidence] = f.Severity + "|" + f.Claim
	}
	if len(got) != 3 {
		t.Fatalf("findings = %v, want three (the package's own files are not findings)", got)
	}
	if v := got["internal/web/signup.go:1"]; !strings.HasPrefix(v, "concern|") || !strings.Contains(v, "owned by web, which uses this package and wires it in") {
		t.Errorf("a file owned downstream is a question for its owner: %q", v)
	}
	if v := got["internal/core/session.go:1"]; !strings.HasPrefix(v, "blocker|") || !strings.HasSuffix(v, ", owned by core") {
		t.Errorf("a file owned upstream is a blocker: %q", v)
	}
	if v := got["internal/other/thing.go:1"]; !strings.HasPrefix(v, "concern|") {
		t.Errorf("a file nobody owns is a concern: %q", v)
	}
}

func TestChangePathDropsTrailingRemarks(t *testing.T) {
	for in, want := range map[string]string{
		"internal/a.go":       "internal/a.go",
		"internal/a.go (new)": "internal/a.go",
		"internal/a.go (new review queue, block list)": "internal/a.go",
		"`internal/a.go`":  "internal/a.go",
		"internal/a(1).go": "internal/a(1).go",
	} {
		if got := changePath(in); got != want {
			t.Errorf("changePath(%q) = %q, want %q", in, got, want)
		}
	}
}
