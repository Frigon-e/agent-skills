package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sweepCard(t *testing.T, topic, key, area string, options ...string) {
	t.Helper()
	dir := filepath.Join(topic, "questions")
	os.MkdirAll(dir, 0o755)
	body := "---\nquestion: Which way for " + key + "?\n"
	if area != "" {
		body += "area: " + area + "\n"
	}
	body += "---\n## Context\nWe must choose.\n"
	if len(options) > 0 {
		body += "\n## Options\n"
		for _, o := range options {
			body += "### " + o + ": option " + o + "\nGain: x\nCost: y\n\n"
		}
	}
	if err := os.WriteFile(filepath.Join(dir, key+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sweepPassFile(t *testing.T, topic, pass, key, json string) {
	t.Helper()
	dir := filepath.Join(topic, "passes", pass)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ans(choice string, evidence ...string) string {
	ev := `"` + strings.Join(evidence, `","`) + `"`
	if len(evidence) == 0 {
		ev = ""
	}
	return `{"choice":"` + choice + `","confidence":0.8,"reason":"because","evidence":[` + ev + `]}`
}

func TestSweepSettlesOnlyWhatIsProved(t *testing.T) {
	h := setup(t)
	os.MkdirAll(filepath.Join(h.repo, ".agents"), 0o755)
	os.WriteFile(filepath.Join(h.repo, ".agents", "orchestrate.toml"), []byte("human_only = [\"Billing\"]\n"), 0o644)
	os.MkdirAll(filepath.Join(h.repo, "adr"), 0o755)
	os.WriteFile(filepath.Join(h.repo, "adr", "1.md"), []byte("a\nb\nc\n"), 0o644)
	polar := filepath.Join(h.tmp, "polar")
	os.MkdirAll(filepath.Join(polar, "src"), 0o755)
	os.WriteFile(filepath.Join(polar, "src", "a.ts"), []byte("x\ny\n"), 0o644)
	topic := filepath.Join(h.tmp, "topic")

	for _, k := range []string{"agree", "differ", "fake", "short", "bad-choice", "url-only", "outside", "owned"} {
		sweepCard(t, topic, k, "", "a", "b")
	}
	sweepCard(t, topic, "money", "billing", "a", "b")
	sweepCard(t, topic, "free", "")
	for _, p := range []string{"flash", "sonnet"} {
		sweepPassFile(t, topic, p, "agree", ans("a", "adr/1.md:2"))
		sweepPassFile(t, topic, p, "money", ans("a", "adr/1.md:2"))
		sweepPassFile(t, topic, p, "free", ans("", "adr/1.md:2"))
		sweepPassFile(t, topic, p, "url-only", ans("a", "https://stripe.com/docs"))
		sweepPassFile(t, topic, p, "outside", ans("a", "polar/src/a.ts:2"))
	}
	sweepPassFile(t, topic, "flash", "differ", ans("a", "adr/1.md:1"))
	sweepPassFile(t, topic, "sonnet", "differ", ans("b", "adr/1.md:3"))
	sweepPassFile(t, topic, "flash", "fake", ans("a", "adr/1.md:2"))
	sweepPassFile(t, topic, "sonnet", "fake", ans("a", "adr/1.md:99")) // line does not exist
	sweepPassFile(t, topic, "flash", "short", ans("a", "adr/1.md:2"))  // only one pass answered
	sweepPassFile(t, topic, "flash", "bad-choice", ans("zzz", "adr/1.md:2"))
	sweepPassFile(t, topic, "sonnet", "bad-choice", ans("a", "adr/1.md:2"))
	sweepPassFile(t, topic, "flash", "owned", ans("a", "adr/1.md:2"))
	sweepPassFile(t, topic, "sonnet", "owned", `{"choice":"a","confidence":0.9,"reason":"tax","evidence":["adr/1.md:2"],"owner":"accountant"}`)
	os.WriteFile(filepath.Join(topic, "dropped.json"), []byte(`[
{"question":"same as agree","reason":"duplicate","duplicate_of":"agree"},
{"question":"ghost","reason":"duplicate","duplicate_of":"nope"},
{"question":"vanished","reason":"gone"},
{"question":"decided","reason":"adr","answered_by":"adr/1.md:99"}]`), 0o644)

	out := h.must("sweep", topic, "--roots", "polar="+polar)
	section := func(title string) string {
		i := strings.Index(out, title)
		if i < 0 {
			t.Fatalf("no %q section:\n%s", title, out)
		}
		rest := out[i:]
		if j := strings.Index(rest[len(title):], "\n]:"); j >= 0 {
			rest = rest[:len(title)+j]
		}
		lines := strings.Split(rest, "\n")
		end := 1
		for end < len(lines) && strings.HasPrefix(lines[end], "  ") {
			end++
		}
		return strings.Join(lines[:end], "\n")
	}
	check := func(title string, want, notWant []string) {
		t.Helper()
		sec := section(title)
		for _, w := range want {
			if !strings.Contains(sec, w) {
				t.Errorf("%s lacks %q:\n%s", title, w, sec)
			}
		}
		for _, w := range notWant {
			if strings.Contains(sec, w) {
				t.Errorf("%s wrongly holds %q:\n%s", title, w, sec)
			}
		}
	}
	check("settled: the passes agree", []string{"agree -> a", "outside -> a"}, []string{"fake", "url-only", "money"})
	check("disagree:", []string{"differ (flash=a, sonnet=b)"}, nil)
	check("recheck:", []string{"fake", "does not resolve", "short", "1 of 2 passes", "bad-choice", "not an option", "url-only", "no evidence"}, []string{"differ", "agree"})
	check("user:", []string{"money (area billing is human-only)", "free", "owned (pass sonnet says the owner is accountant)"}, []string{"agree ->"})
	wantContains(t, out, "dropped: \"ghost\" is marked a duplicate of \"nope\"", "dropped: \"vanished\" was dropped with no duplicate_of", "dropped: \"decided\" says adr/1.md:99 answers it")
	if strings.Contains(out, "same as agree") {
		t.Errorf("a valid dropped entry was flagged:\n%s", out)
	}

	// Cards for the user carry what the passes said, and only those cards.
	fu := filepath.Join(topic, "sweep", "for-user")
	got := string(mustRead(t, filepath.Join(fu, "owned.md")))
	wantContains(t, got, "## Passes", "Why this is yours: pass sonnet says the owner is accountant", "**flash** chose `a`", "Evidence: adr/1.md:2")
	if _, err := os.Stat(filepath.Join(fu, "agree.md")); err == nil {
		t.Error("a settled question was sent to the user")
	}
	askOut := h.must("ask", "--cards", fu)
	wantContains(t, askOut, "questions: 3")
	if strings.Contains(askOut, "warnings[") {
		t.Errorf("a for-user card did not render:\n%s", askOut)
	}
	wantContains(t, string(mustRead(t, filepath.Join(topic, "sweep", "ask", "index.html"))), "Why this is yours", "sonnet")

	// A final pass breaks the tie, but only with real evidence; URLs count on request.
	sweepPassFile(t, topic, "settle", "differ", ans("b", "adr/1.md:3"))
	out = h.must("sweep", topic, "--roots", "polar="+polar, "--accept-urls")
	wantContains(t, out, "differ -> b (final pass settle)", "url-only -> a")
	sweepPassFile(t, topic, "settle", "differ", ans("b", "adr/1.md:99"))
	if out := h.must("sweep", topic); !strings.Contains(out, "differ (flash=a, sonnet=b)") {
		t.Errorf("a final pass without evidence settled the question:\n%s", out)
	}
}

func TestSweepRefusesBadInput(t *testing.T) {
	h := setup(t)
	topic := filepath.Join(h.tmp, "empty")
	for _, args := range [][]string{{"sweep", topic}, {"sweep", topic, "--min-passes", "1"}, {"sweep", topic, "--roots", "polar"}} {
		if code, _ := h.run(args...); code == 0 {
			t.Errorf("orch %v succeeded", args)
		}
	}
}
