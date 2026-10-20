package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const cleanPackageReply = `{"packages":[{"id":"api","will_change":[],"claims":[],"questions":[],"reuse":[]}]}`

func TestGateReplyValidation(t *testing.T) {
	sp := parseSpec(threePkgSpec())
	for _, tc := range []struct{ name, reply, reason string }{
		{"missing packages", `{}`, "packages"},
		{"null packages", `{"packages":null}`, "packages"},
		{"id only", `{"packages":[{"id":"api"}]}`, "explicit arrays"},
		{"null array", strings.Replace(cleanPackageReply, `"claims":[]`, `"claims":null`, 1), "explicit arrays"},
		{"null question", strings.Replace(cleanPackageReply, `"questions":[]`, `"questions":[null]`, 1), "salvaged"},
		{"missing decisiveness", strings.Replace(cleanPackageReply, `"questions":[]`, `"questions":[{"question":"Overwrite?"}]`, 1), "salvaged"},
		{"unknown question package", strings.Replace(cleanPackageReply, `"questions":[]`, `"questions":[{"package":"unknown","question":"Overwrite?","changes_result":true}]`, 1), "unknown package"},
		{"wrong array type", strings.Replace(cleanPackageReply, `"claims":[]`, `"claims":{}`, 1), "salvaged"},
		{"unknown package", strings.Replace(cleanPackageReply, `"api"`, `"unknown"`, 1), "unknown package"},
		{"unsolicited items", strings.Replace(cleanPackageReply, `"reuse":[]`, `"reuse":[],"items":[{"item":"unknown#1","verdict":"maybe"}]`, 1), "unexpected item verdicts"},
		{"duplicate package", strings.Replace(cleanPackageReply, `}]}`, `},{"id":"api","will_change":[],"claims":[],"questions":[],"reuse":[]}]}`, 1), "duplicate package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &gateRecord{}
			err := applyGroup("group:api", []string{"api"})(tc.reply, rec, t.TempDir(), sp)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err = %v, want %s", err, tc.reason)
			}
			if len(rec.Items) != 0 {
				t.Fatalf("invalid reply established item coverage: %+v", rec.Items)
			}
		})
	}
	for _, reply := range []string{`{}`, `{"findings":null}`, `{"findings":{}}`, `{"findings":[{"severity":"warning","where":"Design rules","claim":"bad enum","evidence":"writer.py:1"}]}`} {
		if err := applyFindings("adversary-a")(reply, &gateRecord{}, t.TempDir(), sp); err == nil {
			t.Errorf("accepted %s", reply)
		}
	}
}

func TestGateItemValidation(t *testing.T) {
	items := []gateItem{{ID: "api#1", Package: "api", Text: "write rows"}}
	for _, tc := range []struct{ name, verdicts, reason string }{
		{"unknown", `[{"item":"api#9","verdict":"new","candidate":null,"why":"x"}]`, "unknown item"},
		{"duplicate aliases", `[{"item":"api#1","verdict":"new","candidate":null,"why":"x"},{"item":"1","verdict":"new","candidate":null,"why":"x"}]`, "duplicate item"},
		{"invalid enum", `[{"item":"api#1","verdict":"maybe","candidate":null,"why":"x"}]`, "invalid verdict"},
		{"invalid candidate type", `[{"item":"api#1","verdict":"reuse","candidate":{"file":"writer.py"},"why":"x"}]`, "invalid candidate type"},
		{"missing candidate", `[{"item":"api#1","verdict":"new","why":"x"}]`, "missing candidate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"packages":[{"id":"api","items":` + tc.verdicts + `}]}`
			rec := &gateRecord{}
			err := applyItemsOnly("retry", []string{"api"}, items)(raw, rec, t.TempDir(), nil)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err = %v, want %s", err, tc.reason)
			}
			if len(rec.missingItems(items)) != 1 {
				t.Fatalf("invalid reply covered item: %+v", rec.Items)
			}
		})
	}
}

func TestGateUnsupportedCandidatesRemainRepairableAcrossLanguages(t *testing.T) {
	for _, tc := range []struct{ name, file, body, symbol string }{
		{"Python", "writer.py", "def write_rows(rows):\n    return list(rows)\n", "write_rows"},
		{"TypeScript", "writer.ts", "export function writeRows(rows: string[]): string[] {\n  return rows;\n}\n", "writeRows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFiles(t, repo, map[string]string{tc.file: tc.body})
			items := []gateItem{{ID: "api#1", Package: "api", Text: "write rows"}}
			rec := &gateRecord{}
			rec.ensureItems(items)
			apply := applyItemsOnly("retry", []string{"api"}, items)
			reply := func(candidate string) string {
				b, _ := json.Marshal(candidate)
				return `{"packages":[{"id":"api","items":[{"item":"api#1","verdict":"extend","candidate":` + string(b) + `,"why":"add a format parameter to the writer"}]}]}`
			}
			if err := apply(reply(tc.file+"#missing_writer"), rec, repo, nil); err != nil {
				t.Fatal(err)
			}
			if len(rec.missingItems(items)) != 1 || !strings.Contains(rec.coverageLine(), "no verdict 1") {
				t.Fatalf("unsupported candidate counted: %+v", rec.Items)
			}
			if err := apply(reply(tc.file+"#"+tc.symbol), rec, repo, nil); err != nil {
				t.Fatal(err)
			}
			if len(rec.missingItems(items)) != 0 || !rec.Items[0].Verified {
				t.Fatalf("valid language-specific symbol was not repaired: %+v", rec.Items)
			}
		})
	}
}

func TestGateFutureFileOwnershipIsStructural(t *testing.T) {
	sp := groupSpec(map[string]string{"api": "api/", "writer": "writer/", "web": "web/"}, map[string]string{"web": "api"}, "api", "writer", "web")
	rec := &gateRecord{}
	scopeFor("api", scopeReply{WillChange: []string{"writer/new.py", "web/new.ts"}}, rec, t.TempDir(), sp)
	if len(rec.Findings) != 2 {
		t.Fatalf("findings: %+v", rec.Findings)
	}
	for i, sev := range []string{"blocker", "concern"} {
		f := rec.Findings[i]
		if !f.Verified || f.Support != "structural" || f.Severity != sev {
			t.Errorf("future ownership: %+v", f)
		}
	}
}

func TestGateFutureFileOwnershipBlocksApproval(t *testing.T) {
	h := setup(t)
	h.commitSpec(threePkgSpec())
	h.fakePi(gatePasses(map[string]string{
		"package":     `{"id":"@ID@","will_change":[],"claims":[],"questions":[],"reuse":[]}`,
		"package-api": `{"id":"api","will_change":["migrations/future.sql"],"claims":[],"questions":[],"reuse":[]}`,
	}))
	if exists(filepath.Join(h.repo, "migrations", "future.sql")) {
		t.Fatal("fixture must be a future file")
	}
	code, out := h.run("gate", "spec.md")
	if code != 1 {
		t.Fatalf("future-file ownership collision passed: %s", out)
	}
	wantContains(t, out, "gate: fail", "blockers: 1 verified")
}

func TestGateSalvagedFindingsRemainDiagnostic(t *testing.T) {
	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{"writer.py": "def write_rows(rows):\n    return rows\n"})
	raw := `{"findings":[{"severity":"concern","where":"Design rules","claim":"writer exists","evidence":"writer.py:1"}]`
	rec := &gateRecord{}
	err := applyFindings("adversary-a")(raw, rec, repo, nil)
	if err == nil || len(rec.Findings) != 1 || len(rec.Salvaged) != 1 {
		t.Fatalf("salvaged findings: err %v record %+v", err, rec)
	}
}

func TestGateSalvagedItemsNeverEstablishCoverage(t *testing.T) {
	items := []gateItem{{ID: "api#1", Package: "api", Text: "write rows"}}
	raw := `{"packages":[{"id":"api","will_change":[],"claims":[],"questions":[],"items":[{"item":"api#1","verdict":"new","candidate":null,"why":"new behavior"}]`
	rec := &gateRecord{}
	err := applyGroupItems("group:api", []string{"api"}, itemMap(items))(raw, rec, t.TempDir(), parseSpec(threePkgSpec()))
	if err == nil || len(rec.Salvaged) != 1 || len(rec.missingItems(items)) != 1 || rec.Items[0].Verdict != "" {
		t.Fatalf("salvage established coverage: err %v record %+v", err, rec)
	}
	if err := applyItemsOnly("retry", []string{"api"}, items)(raw, rec, t.TempDir(), nil); err == nil {
		t.Fatal("salvaged retry established coverage")
	}
}

func TestGateSchemaRepairSharesCoverageSlot(t *testing.T) {
	for _, complete := range []bool{true, false} {
		name := "still incomplete"
		if complete {
			name = "complete replacement"
		}
		t.Run(name, func(t *testing.T) {
			h := setup(t)
			gdir := verdictRepo(t, h)
			items := `{"item":"api#1","verdict":"new","candidate":null,"why":"new behavior"}`
			if complete {
				items += `,{"item":"api#2","verdict":"new","candidate":null,"why":"new endpoint"}`
			}
			repaired := `{"packages":[{"id":"api","will_change":[],"claims":[],"questions":[],"items":[` + items + `]}]}`
			dir := scriptPi(t, h, `{}`, repaired)
			// A schema repair replaces the whole group response, unlike an
			// item-only coverage retry. Record every invocation to bound it.
			writeFiles(t, dir, map[string]string{"pi": `#!/bin/sh
 echo "$*" >> "` + dir + `/calls.log"
 case "$*" in
 *'Schema repair:'*) cat "` + dir + `/retry.json";;
 *GATE-GROUP*) echo '{}';;
 *) echo '{"findings":[]}';;
 esac
`})
			code, out := h.run("gate", "spec.md", "--verdicts")
			if complete && code != 0 {
				t.Fatalf("complete schema repair failed: %s", out)
			}
			if !complete && code != 1 {
				t.Fatalf("incomplete schema repair passed: %s", out)
			}
			calls := string(mustRead(t, filepath.Join(dir, "calls.log")))
			if strings.Count(calls, "GATE-GROUP") != 2 || strings.Contains(calls, "GATE-GROUP-ITEMS") {
				t.Fatalf("repair added a third attempt: %s", calls)
			}
			rec := readRecord(t, gdir)
			if complete && rec.Status != "pass" {
				t.Fatalf("repaired record: %+v", rec)
			}
			if !complete && (rec.Status != "unverified" || len(rec.Reasons) == 0) {
				t.Fatalf("incomplete record: %+v", rec)
			}
		})
	}
}

// Exercise the public gate with a fake reviewer, not just the decoder.
func TestGateReviewCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name, reply, status string
		code                int
	}{
		{"empty object", `{}`, "unverified", 1},
		{"package ID only", `{"packages":[{"id":"api"}]}`, "unverified", 1},
		{"explicit clean review", `{"findings":[]}`, "pass", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			gdir := h.gateRepo(1)
			h.fakePi(gatePasses(map[string]string{
				"group": cleanPackageReply,
				"adv-a": tc.reply,
			}))
			code, out := h.run("gate", "spec.md")
			if code != tc.code {
				t.Fatalf("exit %d, want %d:\n%s", code, tc.code, out)
			}
			wantContains(t, out, "gate: "+tc.status)
			rec := readRecord(t, gdir)
			if tc.code != 0 && len(rec.Reasons) == 0 {
				t.Fatal("incomplete review has no reasons")
			}
			if !exists(filepath.Join(gdir, "gate.json")) {
				t.Fatal("missing gate record")
			}
		})
	}
}
