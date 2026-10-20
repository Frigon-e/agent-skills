package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRecord(t *testing.T, dir string) gateRecord {
	t.Helper()
	var rec gateRecord
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, "gate.json")), &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func piCalls(t *testing.T, h *harness) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(h.tmp, "fakepi", "calls.log"))
	return string(b)
}

func TestGateDossierCodeGoesAfterThePackageBlocksAndOffIsToday(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(2)
	h.sessionPi(gatePasses(nil))
	t.Setenv("HERDR_ENV", "")
	out := h.must("gate", "spec.md")
	wantContains(t, out, "dossier: code, 2 package(s),", "chars (~")
	calls := piCalls(t, h)
	var group string
	for _, l := range strings.Split(calls, "\n") {
		if strings.Contains(l, "GATE-GROUP") {
			group = l
		}
	}
	// the fake pi logs "$*" on several lines; use the whole log from the first package line
	i, j, k := strings.Index(calls, "PACKAGES UNDER REVIEW"), strings.Index(calls, dossierTitle), strings.Index(calls, "GATE-GROUP")
	if group == "" || i < 0 || j < i || k < j {
		t.Fatalf("order must be package blocks, dossier, tag (%d %d %d):\n%s", i, j, k, calls)
	}
	wantContains(t, calls, "## Package api", "### 1. Ranked files", "Use the evidence dossier", "Budget: about 20 turns.", "Budget: about 40 turns.")
	if b := mustRead(t, filepath.Join(gdir, "dossier", "api.md")); !strings.Contains(string(b), "### 4. Names not found in completed bounded lookups (not proof of absence)") {
		t.Errorf("dossier file:\n%s", b)
	}
	rec := readRecord(t, gdir)
	if rec.Dossier != "code" || rec.DossierChars["api"] == 0 || rec.Cache != "short" {
		t.Errorf("record: dossier %q chars %v cache %q", rec.Dossier, rec.DossierChars, rec.Cache)
	}
	wantContains(t, string(mustRead(t, filepath.Join(gdir, "report.md"))), "Dossier: code, 2 package(s)")

	// --dossier off: no dossier section, no sentence, no files.
	os.RemoveAll(filepath.Join(gdir, "dossier"))
	h.sessionPi(gatePasses(nil))
	out = h.must("gate", "spec.md", "--dossier", "off")
	calls = piCalls(t, h)
	if strings.Contains(calls, dossierTitle) || strings.Contains(calls, "Use the evidence dossier") || strings.Contains(out, "dossier:") || exists(filepath.Join(gdir, "dossier")) {
		t.Errorf("--dossier off still produced a dossier:\n%s\n%s", out, calls)
	}
	if code, _ := h.run("gate", "spec.md", "--dossier", "maybe"); code == 0 {
		t.Error("a bad --dossier value was accepted")
	}
}

func TestGateDryRunShowsDossierSizesAndPlannerPass(t *testing.T) {
	h := setup(t)
	h.gateRepo(2)
	dir := h.fakePi(gatePasses(nil))
	out := h.must("gate", "spec.md", "--dry-run")
	wantContains(t, out, "dossier: code; cache: short", "  dossier: code, 2 package(s),", "    api: ", "turn budget: 20 soft, 28 hard")
	out = h.must("gate", "spec.md", "--dry-run", "--dossier", "plan", "--cache", "long")
	wantContains(t, out, "dossier: plan; cache: long", "planner: plan:group:api+1", "tools: none", "section 5")
	out = h.must("gate", "spec.md", "--dry-run", "--dossier", "off")
	if strings.Contains(out, "    api: ") {
		t.Errorf("--dossier off showed dossier sizes:\n%s", out)
	}
	if exists(filepath.Join(dir, "calls.log")) {
		t.Error("pi was called")
	}
	// --only works in a dry run and names the valid keys when it is wrong.
	out = h.must("gate", "spec.md", "--dry-run", "--only", "group:api+1", "--experiment", "x")
	if strings.Contains(out, "adversary-a") || !strings.Contains(out, "pass: group:api+1") {
		t.Errorf("--only dry run:\n%s", out)
	}
	code, out := h.run("gate", "spec.md", "--dry-run", "--only", "nope", "--experiment", "x")
	if code == 0 || !strings.Contains(out, "group:api+1") {
		t.Errorf("a wrong key: %d %s", code, out)
	}
}

func planReply(lookups string) map[string]string {
	return gatePasses(map[string]string{"plan": `{"lookups":[` + lookups + `]}`})
}

func TestGatePlanArmRunsAToolLessPlannerThenTheLookups(t *testing.T) {
	for _, mode := range []string{"headless", "pane"} {
		t.Run(mode, func(t *testing.T) {
			h := setup(t)
			gdir := h.gateRepo(2)
			h.sessionPi(planReply(`{"package":"api","kind":"text","query":"Put","why":"who calls it"},{"package":"p1","kind":"read","path":"internal/store.go","from":1,"to":3}`))
			if mode == "headless" {
				t.Setenv("HERDR_ENV", "")
			}
			out := h.must("gate", "spec.md", "--dossier", "plan")
			calls := piCalls(t, h)
			wantContains(t, calls, "GATE-PLAN", "### 5. Lookups the planner asked for", "- [text] `Put` - who calls it", "- [read] `internal/store.go:1-3`", "1: package internal")
			if mode == "headless" {
				b, _ := os.ReadFile(filepath.Join(h.tmp, "fakepi", "plan.log"))
				if strings.TrimSpace(string(b)) != "no-tools" {
					t.Errorf("the planner ran with tools or not once: %q", b)
				}
			} else {
				plans := 0
				for _, c := range h.gateCalls("agent", "start") {
					if strings.Contains(fmt.Sprint(c.Extra["prompt"]), "GATE-PLAN") {
						plans++
						if !strings.Contains(strings.Join(c.Args, " "), "--no-tools") || strings.Contains(strings.Join(c.Args, " "), "--tools") {
							t.Errorf("planner start args: %v", c.Args)
						}
					}
				}
				if plans != 1 {
					t.Errorf("planner passes = %d, want 1", plans)
				}
			}
			rec := readRecord(t, gdir)
			if _, ok := rec.PassSeconds["plan:group:api+1"]; !ok {
				t.Errorf("no planner seconds: %v", rec.PassSeconds)
			}
			if rec.PassTokens["plan:group:api+1"].Input == 0 {
				t.Errorf("no planner tokens: %v", rec.PassTokens)
			}
			if rec.State != "pass" {
				t.Errorf("state %s: %v", rec.State, rec.PassErrors)
			}
			wantContains(t, out, "dossier: plan,")
			if b := mustRead(t, filepath.Join(gdir, "dossier", "p1.md")); !strings.Contains(string(b), "### 5.") {
				t.Errorf("dossier file lacks section 5:\n%s", b)
			}
		})
	}
}

func TestGatePlanArmFallsBackToTheCodeDossier(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(1)
	h.sessionPi(gatePasses(map[string]string{"plan": "I cannot decide"}))
	t.Setenv("HERDR_ENV", "")
	out := h.must("gate", "spec.md", "--dossier", "plan")
	wantContains(t, out, "gate: pass", "plan:group:api: the reply held no lookups; the pass used the code dossier only")
	wantContains(t, out, `note: "plan:group:api: the reply held no lookups; the pass used the code dossier only"`)
	if strings.Contains(piCalls(t, h), "### 5.") {
		t.Error("section 5 appeared without lookups")
	}
	rec := readRecord(t, gdir)
	var retrievalNotes []string
	for _, note := range rec.Notes {
		if !strings.Contains(note, " coverage:") {
			retrievalNotes = append(retrievalNotes, note)
		}
	}
	if len(retrievalNotes) != 2 || !strings.HasPrefix(retrievalNotes[1], "plan:group:api: ") {
		t.Errorf("retrieval notes: %v", retrievalNotes)
	}
}

func TestGateExperimentIsolatedAndCompared(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(3)
	over := map[string]string{"package-api": `{"id":"api","will_change":[],"claims":[],"questions":[{"package":"api","question":"Which table holds the queue rows?","changes_result":true}],"reuse":[{"builds":"Put wrapper","existing":"Put","evidence":"internal/store.go:3","why":"same job"}]}`}
	h.sessionPi(gatePasses(over))
	t.Setenv("HERDR_ENV", "")

	out := h.must("gate", "spec.md", "--only", "api+2", "--dossier", "code", "--experiment", "e1")
	wantContains(t, out, "experiment: e1 (dossier code, cache short, groups group:api+2)", "dossier: code, 3 package(s)", "tokens: ")
	if strings.Contains(piCalls(t, h), "GATE-ADVERSARY") {
		t.Error("an experiment ran an adversary")
	}
	for _, f := range []string{"gate.json", "report.md", "status"} {
		if exists(filepath.Join(gdir, f)) {
			t.Errorf("an experiment wrote the real %s", f)
		}
	}
	if exists(filepath.Join(gdir, "questions")) {
		t.Error("an experiment wrote the real questions dir")
	}
	exp := filepath.Join(gdir, "experiments", "e1")
	rec := readRecord(t, exp)
	if rec.Experiment != "e1" || rec.State != "experiment" || len(rec.Only) != 1 || len(rec.Questions) != 1 || len(rec.Findings) != 1 || rec.Findings[0].Source != "reuse" {
		t.Errorf("experiment record: %+v", rec)
	}
	for _, p := range []string{"report.md", "dossier/api.md", "sessions/group-api+2"} {
		if !exists(filepath.Join(exp, p)) {
			t.Errorf("experiment lacks %s", p)
		}
	}
	wantContains(t, string(mustRead(t, filepath.Join(exp, "report.md"))), "Exploration per pass")

	h.sessionPi(gatePasses(over))
	h.must("gate", "spec.md", "--only", "group:api+2", "--dossier", "off", "--cache", "long", "--experiment", "e2")
	cmp := h.must("gate", "compare", "spec.md", "e1", "e2")
	wantContains(t, cmp, "experiment A: e1 (dossier code, cache short, groups group:api+2)", "experiment B: e2 (dossier off, cache long, groups group:api+2)",
		"requests 1; tokens: input 100, cache read 300 (75% of input), cache write 5, output 10; max context 400 in group:api+2",
		"cost $0.00 (incomplete: 1 request(s) without cost)", "findings: 1 (1 verified; by source: reuse 1); questions: 1 (1 need a decision)",
		"pass group:api+2: ", "exploration: requests 1;", "overlap of findings: 1 matched, 1 in e1, 1 in e2, Jaccard 1.00", "overlap of questions: 1 matched")
	if code, out := h.run("gate", "compare", "spec.md", "e1", "missing"); code == 0 || !strings.Contains(out, "experiment missing: no record") {
		t.Errorf("compare with a missing experiment: %d %s", code, out)
	}
	// Argument checks.
	for _, args := range [][]string{{"gate", "spec.md", "--only", "api+2"}, {"gate", "spec.md", "--experiment", "e3"}, {"gate", "spec.md", "--only", "nope", "--experiment", "e3"}, {"gate", "spec.md", "--only", "api+2", "--experiment", "Bad Name"}} {
		if code, _ := h.run(args...); code == 0 {
			t.Errorf("%v was accepted", args)
		}
	}
}

func TestMatchItemsPairsByWords(t *testing.T) {
	m, onlyA, onlyB := matchItems(
		[]string{"Which table holds the queue rows?", "Should deletes be soft?"},
		[]string{"Which table holds queue rows", "Is the endpoint paginated?"})
	if m != 1 || len(onlyA) != 1 || !strings.Contains(onlyA[0], "soft") || len(onlyB) != 1 || !strings.Contains(onlyB[0], "paginated") {
		t.Errorf("matched %d, only A %v, only B %v", m, onlyA, onlyB)
	}
}

func TestGateCacheRetentionReachesHeadlessAndPanePi(t *testing.T) {
	t.Run("headless", func(t *testing.T) {
		h := setup(t)
		gdir := h.gateRepo(1)
		t.Setenv("HERDR_ENV", "")
		t.Setenv("PI_CACHE_RETENTION", "inherited")
		for _, tc := range []struct {
			args []string
			want string
		}{
			{nil, "inherited"}, {[]string{"--cache", "long"}, "long"}, {[]string{"--cache", "short"}, ""},
		} {
			h.fakePi(gatePasses(nil))
			out := h.must(append([]string{"gate", "spec.md"}, tc.args...)...)
			b, _ := os.ReadFile(filepath.Join(h.tmp, "fakepi", "cache.log"))
			lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			if len(lines) != 3 {
				t.Fatalf("pi ran %d times", len(lines))
			}
			for _, l := range lines {
				if l != tc.want {
					t.Errorf("%v: pi saw PI_CACHE_RETENTION=%q, want %q", tc.args, l, tc.want)
				}
			}
			if tc.want == "long" {
				wantContains(t, out, "cache: long")
				if rec := readRecord(t, gdir); rec.Cache != "long" {
					t.Errorf("record cache = %q", rec.Cache)
				}
			}
		}
	})
	t.Run("pane", func(t *testing.T) {
		h := setup(t)
		h.gateRepo(1)
		h.sessionPi(gatePasses(nil))
		h.must("gate", "spec.md", "--cache", "long", "--ctx-tool", "off")
		runs := h.gateCalls("pane", "run")
		if len(runs) != 3 {
			t.Fatalf("pane run calls = %d, want one per pass", len(runs))
		}
		for _, r := range runs {
			if r.Extra["cmd"] != "export PI_CACHE_RETENTION=long" {
				t.Errorf("pane run: %v", r.Extra)
			}
		}
		b, _ := os.ReadFile(filepath.Join(h.tmp, "fakepi", "cache.log"))
		if got := strings.Fields(string(b)); len(got) != 3 || got[0] != "long" || got[1] != "long" || got[2] != "long" {
			t.Errorf("pi saw %q", b)
		}
		// short: unset, and the default sends nothing at all.
		h.sessionPi(gatePasses(nil))
		os.Remove(h.fake + ".gate.log")
		h.must("gate", "spec.md", "--cache", "short", "--ctx-tool", "off")
		for _, r := range h.gateCalls("pane", "run") {
			if r.Extra["cmd"] != "unset PI_CACHE_RETENTION" {
				t.Errorf("pane run: %v", r.Extra)
			}
		}
		os.Remove(h.fake + ".gate.log")
		h.must("gate", "spec.md", "--ctx-tool", "off")
		if n := len(h.gateCalls("pane", "run")); n != 0 {
			t.Errorf("the default touched the pane environment %d times", n)
		}
	})
}

func TestGateTurnBudgetPromptsOnceAndInterruptsAtTheHardLimit(t *testing.T) {
	old := [2]int{gateTurnsBase, gateTurnsPerPackage}
	gateTurnsBase, gateTurnsPerPackage = 2, 2 // one package: soft 4, hard 5
	t.Cleanup(func() { gateTurnsBase, gateTurnsPerPackage = old[0], old[1] })

	t.Run("soft", func(t *testing.T) {
		h := setup(t)
		gdir := h.gateRepo(1)
		h.sessionPi(gatePasses(nil))
		os.WriteFile(h.fake+".budget", []byte("soft:4"), 0o644)
		out := h.must("gate", "spec.md")
		wantContains(t, out, "gate: pass", "budget hits: soft 1, hard 0")
		var prompts []string
		for _, c := range h.gateCalls("agent", "prompt") {
			prompts = append(prompts, fmt.Sprint(c.Extra["text"]))
			if c.Extra["wait"] != false {
				t.Errorf("the soft prompt waited: %v", c.Args)
			}
		}
		if len(prompts) != 1 || prompts[0] != "Budget reached (4 turns). Stop exploring and reply now with the final JSON from what you have." {
			t.Errorf("prompts: %q", prompts)
		}
		if n := len(h.gateCalls("agent", "send-keys")); n != 3 { // ctrl+d of each of the three passes only
			t.Errorf("send-keys calls = %d", n)
		}
		tk := readRecord(t, gdir).PassTokens["group:api"]
		if tk.BudgetHit != "soft" || tk.Turns != 4 {
			t.Errorf("group tokens: %+v", tk)
		}
		wantContains(t, piCalls(t, h), "Budget: about 4 turns.")
	})
	t.Run("hard", func(t *testing.T) {
		h := setup(t)
		gdir := h.gateRepo(1)
		h.sessionPi(gatePasses(nil))
		os.WriteFile(h.fake+".budget", []byte("hard:6"), 0o644)
		out := h.must("gate", "spec.md")
		wantContains(t, out, "gate: pass", "budget hits: soft 0, hard 1")
		var keys []string
		for _, c := range h.gateCalls("agent", "send-keys") {
			keys = append(keys, c.Args[3])
		}
		if !strings.Contains(strings.Join(keys, " "), "ctrl+c") {
			t.Errorf("no interrupt: %v", keys)
		}
		prompts := h.gateCalls("agent", "prompt")
		if len(prompts) != 1 || prompts[0].Extra["text"] != "Reply now with the final JSON only." || prompts[0].Extra["wait"] != true {
			t.Errorf("prompts: %+v", prompts)
		}
		if tk := readRecord(t, gdir).PassTokens["group:api"]; tk.BudgetHit != "hard" {
			t.Errorf("group tokens: %+v", tk)
		}
	})
	t.Run("context", func(t *testing.T) {
		old := gateContextSoft
		gateContextSoft = 50 // the fake session's requests read 110 tokens
		t.Cleanup(func() { gateContextSoft = old })
		h := setup(t)
		h.gateRepo(1)
		h.sessionPi(gatePasses(nil))
		os.WriteFile(h.fake+".budget", []byte("soft:1"), 0o644)
		h.must("gate", "spec.md")
		prompts := h.gateCalls("agent", "prompt")
		if len(prompts) != 1 || !strings.HasPrefix(fmt.Sprint(prompts[0].Extra["text"]), "Budget reached (context 110 tokens).") {
			t.Errorf("prompts: %+v", prompts)
		}
	})
}

func TestSessionProgressAndProfile(t *testing.T) {
	dir := t.TempDir()
	call := func(name, path string) string {
		return fmt.Sprintf(`{"name":%q,"type":"toolCall","arguments":{"path":%q}}`, name, path)
	}
	res := func(tool string, n int) string {
		return fmt.Sprintf(`{"type":"message","message":{"role":"toolResult","toolName":%q,"content":[{"type":"text","text":%q}]}}`, tool, strings.Repeat("x", n))
	}
	asst := func(in, cr int, parts ...string) string {
		return fmt.Sprintf(`{"type":"message","message":{"role":"assistant","content":[%s],"usage":{"input":%d,"cacheRead":%d,"output":1}}}`, strings.Join(parts, ","), in, cr)
	}
	writeFiles(t, dir, map[string]string{"s.jsonl": strings.Join([]string{
		`{"type":"session"}`,
		asst(10, 0, call("read", "docs/spec.md"), call("grep", "")),
		res("read", 300), res("grep", 5000),
		asst(20, 100, call("read", "docs/spec.md"), call("read", "a.go")),
		res("read", 40), res("read", 60),
		asst(30, 200, call("read", "a.go")),
		res("read", 10),
		asst(40, 300, `{"type":"text","text":"{}"}`),
	}, "\n") + "\n"})
	turns, ctx := sessionProgress(dir)
	if turns != 3 || ctx != 340 {
		t.Errorf("progress = %d turns, context %d; want 3 and 340", turns, ctx)
	}
	p, ok := profileSession(dir, "docs/spec.md")
	if !ok || p.Requests != 4 || p.Calls["read"] != 4 || p.Calls["grep"] != 1 || p.ResultChars != 5410 || p.SpecReads != 2 {
		t.Fatalf("profile = %+v %v", p, ok)
	}
	if len(p.Largest) != 5 || p.Largest[0] != (bigResult{"grep", 5000}) || p.Largest[1] != (bigResult{"read", 300}) {
		t.Errorf("largest = %+v", p.Largest)
	}
	if len(p.Repeated) != 2 || p.Repeated[0] != (pathCount{"a.go", 2}) || p.Repeated[1] != (pathCount{"docs/spec.md", 2}) {
		t.Errorf("repeated = %+v", p.Repeated)
	}
	wantContains(t, p.String(), "requests 4", "grep 1, read 4", "result chars 5410", "grep 5000", "a.go x2", "spec reads 2")
	tk, _ := sessionTokens(dir)
	if tk.Turns != 3 || tk.Requests != 4 {
		t.Errorf("tokens: %+v", tk)
	}
	if _, ok := profileSession(t.TempDir(), ""); ok {
		t.Error("an empty dir has a profile")
	}
}

func TestGateInsideHerdrNeedsItsTabAndHeadlessIsExplicit(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(1)
	h.sessionPi(gatePasses(nil))
	os.WriteFile(h.fake+".tabfail", nil, 0o644)
	code, out := h.run("gate", "spec.md")
	if code != 1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	wantContains(t, out, "gate: unverified", `pass_failed: all - "the gate tab could not be opened in herdr, so no pass ran`)
	if exists(filepath.Join(h.tmp, "fakepi", "calls.log")) {
		t.Error("a pass ran headless after the tab failed")
	}
	if rec := readRecord(t, gdir); rec.State != "incomplete" {
		t.Errorf("state %s", rec.State)
	}
	// An explicit choice runs headless, inside herdr as well.
	for _, via := range []string{"flag", "env"} {
		h.sessionPi(gatePasses(nil))
		os.Remove(h.fake + ".gate.log")
		tabs := len(callsOf(h.fakeState(), "tab", "create"))
		args := []string{"gate", "spec.md"}
		if via == "flag" {
			args = append(args, "--headless")
		} else {
			t.Setenv("ORCH_HEADLESS", "1")
		}
		wantContains(t, h.must(args...), "gate: pass")
		if exists(h.fake+".gate.log") || len(callsOf(h.fakeState(), "tab", "create")) != tabs {
			t.Errorf("%s: herdr was used", via)
		}
	}
}

func TestGateKeepSessionsKeepsTheSessionsOfPassesThatSucceeded(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(1)
	t.Setenv("HERDR_ENV", "")
	h.sessionPi(gatePasses(nil))
	h.must("gate", "spec.md")
	if exists(filepath.Join(gdir, "sessions", "group-api")) {
		t.Error("a succeeding pass kept its session without --keep-sessions")
	}
	h.sessionPi(gatePasses(nil))
	h.must("gate", "spec.md", "--keep-sessions")
	for _, p := range []string{"group-api", "adversary-a"} {
		if !exists(filepath.Join(gdir, "sessions", p, "session.jsonl")) {
			t.Errorf("--keep-sessions dropped the session of %s", p)
		}
	}
	wantContains(t, string(mustRead(t, filepath.Join(gdir, "report.md"))), "Exploration per pass", "group:api: requests 1;")
}

func TestGateDryRunDumpsTheDossierFilesARealPassGets(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(2)
	h.sessionPi(gatePasses(nil))
	t.Setenv("HERDR_ENV", "")
	dump := filepath.Join(h.tmp, "dump")
	out := h.must("gate", "spec.md", "--dry-run", "--only", "group:api+1", "--experiment", "x", "--dump-dossier", dump)
	wantContains(t, out, "dossier files (2):", filepath.Join(dump, "api.md"), filepath.Join(dump, "p1.md"))
	names, _ := os.ReadDir(dump)
	if len(names) != 2 || exists(gdir) {
		t.Errorf("the dump wrote %d files, gate dir exists: %v", len(names), exists(gdir))
	}
	h.must("gate", "spec.md", "--only", "group:api+1", "--experiment", "x")
	for _, id := range []string{"api", "p1"} {
		want := mustRead(t, filepath.Join(gdir, "experiments", "x", "dossier", id+".md"))
		if got := mustRead(t, filepath.Join(dump, id+".md")); string(got) != string(want) || len(got) == 0 {
			t.Errorf("%s: the dumped dossier differs from the one a real pass got", id)
		}
	}
	if code, out := h.run("gate", "spec.md", "--only", "group:api+1", "--experiment", "y", "--dump-dossier", dump); code == 0 || !strings.Contains(out, "needs --dry-run") {
		t.Errorf("--dump-dossier without --dry-run: %d %s", code, out)
	}
}

func TestSessionProfileSeparatesOrchCtxFromRawShellAndRangedReads(t *testing.T) {
	cases := map[string]string{
		`orch ctx batch -q "find:a" -q "text:b"`:   "orch ctx batch",
		`orch ctx text foo --compact`:              "orch ctx",
		`orch ctx text foo | head -20`:             "orch ctx piped",
		`orch ctx batch -q "text:b" | head`:        "orch ctx batch piped",
		`grep -rn Gateway internal | head -40`:     "shell search",
		`cd internal && rg -n Gateway`:             "shell search",
		`sed -n '1,40p' internal/a.go`:             "shell read",
		`head -80 a.go`:                            "shell read",
		`go test ./...`:                            "shell other",
		`ls internal/crm | tail -5`:                "shell search",
		`orch ctx batch -q "text:b" 2>&1`:          "orch ctx batch",
		`echo hi`:                                  "shell other",
		`orch ctx find "Code that lists gateways"`: "orch ctx",
	}
	for cmd, want := range cases {
		if got := shellKind(cmd); got != want {
			t.Errorf("shellKind(%q) = %q, want %q", cmd, got, want)
		}
	}
	dir := t.TempDir()
	call := func(name, args string) string {
		return fmt.Sprintf(`{"type":"toolCall","name":%q,"arguments":%s}`, name, args)
	}
	asst := func(parts ...string) string {
		return fmt.Sprintf(`{"type":"message","message":{"role":"assistant","content":[%s]}}`, strings.Join(parts, ","))
	}
	writeFiles(t, dir, map[string]string{"s.jsonl": strings.Join([]string{
		asst(call("bash", `{"command":"orch ctx batch -q \"find:a\" -q \"text:b\" -q \"sym:c\""}`), call("bash", `{"command":"grep -rn x ."}`)),
		asst(call("read", `{"path":"a.go","offset":10,"limit":40}`), call("read", `{"path":"b.go"}`), call("read", `{"path":"c.go","limit":30}`)),
	}, "\n") + "\n"})
	p, ok := profileSession(dir, "")
	if !ok || p.Shell["orch ctx batch"] != 1 || p.Shell["shell search"] != 1 || p.BatchLookups != 3 || p.RangedReads != 2 || p.WholeReads != 1 {
		t.Fatalf("profile = %+v %v", p, ok)
	}
	wantContains(t, p.String(), "bash: orch ctx batch 1 (3 lookups), shell search 1", "reads: 2 ranged, 1 whole file")
}

func TestBenchToolRowsCountOrchCtxAgainstRawShell(t *testing.T) {
	r := &benchRun{Groups: map[string]benchGroup{
		"g1": {Exploration: &sessionProfile{Calls: map[string]int{"bash": 5, "read": 3}, Shell: map[string]int{"orch ctx batch": 2, "orch ctx": 1, "shell search": 2}, BatchLookups: 7, RangedReads: 2, WholeReads: 1}},
		"g2": {Exploration: &sessionProfile{Calls: map[string]int{"bash": 1, "read": 0}, Shell: map[string]int{"orch ctx piped": 1}, readBreakdownKnown: true}},
	}}
	want := map[string]float64{"orch ctx calls": 4, "orch ctx lookups (a batch counts each -q)": 9, "orch ctx piped into another tool": 1, "Raw shell search (grep, rg, find, ls)": 2, "Native reads": 3, "Native reads with a range": 2}
	for _, m := range benchToolRows {
		got := benchToolSum(r, nil, m.count)
		if w, ok := want[m.name]; ok && (got == nil || *got != w) {
			t.Errorf("%s = %v, want %v", m.name, got, w)
		}
	}
	// A profile recorded before shell calls were split cannot say what its bash calls were.
	old := &benchRun{Groups: map[string]benchGroup{"g": {Exploration: &sessionProfile{Calls: map[string]int{"bash": 4}}}}}
	if benchToolSum(old, nil, benchToolRows[0].count) != nil {
		t.Error("an old profile with bash calls must read as not recorded")
	}
}
