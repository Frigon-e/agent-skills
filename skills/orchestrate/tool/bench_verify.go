package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type benchFinding struct {
	Level   string `json:"level"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

func benchSnapshotCheck(r *benchRun, b []byte) error {
	if benchHash(b) != r.SourceHash {
		return fail("run %s source_hash differs from snapshot/gate.json", r.ID)
	}
	var rec gateRecord
	if e := json.Unmarshal(b, &rec); e != nil {
		return e
	}
	if rec.Hash != "" && rec.Hash != r.Inputs.SpecHash {
		return fail("run %s snapshot spec differs from run inputs", r.ID)
	}
	if rec.Experiment != "" {
		return benchName(rec.Experiment)
	}
	return nil
}

func benchVerifyRun(r *benchRun, snap string) error {
	s, e := benchSubjectLoad(r.Subject)
	if e != nil {
		return e
	}
	if r.Inputs.SpecHash != s.Spec.Hash {
		return fail("run spec hash differs from subject")
	}
	if s.Spec.State != "unknown" || objectHas("spec", s.Spec.Hash) {
		if _, e := objectGet("spec", s.Spec.Hash); e != nil {
			return e
		}
	}
	if r.Itemset != "" {
		if _, e := objectGet("itemset", r.Itemset); e != nil {
			return e
		}
	}
	for name, ids := range r.Blobs {
		if name == "prompts" && r.PromptsStored != nil && !*r.PromptsStored {
			for _, id := range ids {
				if !benchHex64.MatchString(id) {
					return fail("invalid hashed-only prompt id")
				}
			}
			continue
		}
		for _, id := range ids {
			if _, e := objectGet("blob", id); e != nil {
				return e
			}
		}
	}
	if r.Kind != "report-only" {
		path := filepath.Join(snap, "gate.json")
		if e := benchSafe(path); e != nil {
			return e
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if e := benchSnapshotCheck(r, b); e != nil {
			return e
		}
	}
	return nil
}
func benchVerify(suite string) ([]benchFinding, error) {
	findings := []benchFinding{}
	seen := map[string]bool{}
	add := func(level, path string, e error) {
		if e != nil {
			identity := level + "\x00" + e.Error()
			if seen[identity] {
				return
			}
			seen[identity] = true
			findings = append(findings, benchFinding{level, path, e.Error()})
		}
	}
	walk := func(root string, fn func(string) error) {
		add("error", root, filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) && path == root {
				return nil
			}
			if e != nil {
				return e
			}
			if e := benchSafe(path); e != nil {
				add("error", path, e)
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				add("error", path, fail("symlink in benchmark store"))
				return nil
			}
			if !d.IsDir() {
				add("error", path, fn(path))
			}
			return nil
		}))
	}
	for _, kind := range []string{"spec", "itemset", "blob"} {
		walk(filepath.Join(benchDir(), "objects", kind), func(path string) error {
			name := filepath.Base(path)
			if strings.HasPrefix(name, ".") {
				return nil
			}
			if kind == "spec" && strings.HasSuffix(name, ".json") {
				id := strings.TrimSuffix(name, ".json")
				_, e := objectGet("spec", id)
				return e
			}
			if kind == "blob" && strings.HasSuffix(name, ".meta.json") {
				return nil
			}
			id := name
			if kind == "spec" {
				id = strings.TrimSuffix(id, ".md")
			}
			if kind == "itemset" {
				id = strings.TrimSuffix(id, ".json")
			}
			b, e := objectGet(kind, id)
			if e != nil {
				return e
			}
			if kind == "itemset" {
				var set benchItemset
				if e := benchReadJSON(path, &set); e != nil {
					return e
				}
				if set.Spec != "" {
					if _, e := objectGet("spec", set.Spec); e != nil {
						return e
					}
				}
			}
			_ = b
			return nil
		})
	}
	walk(filepath.Join(benchDir(), "subjects"), func(path string) error {
		if strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}
		id := strings.TrimSuffix(filepath.Base(path), ".json")
		s, e := benchSubjectLoad(id)
		if e != nil {
			return e
		}
		if s.Spec.State == "unknown" && !benchExists(filepath.Join(benchDir(), "objects", "spec", s.Spec.Hash+".md")) {
			add("info", path, fail("spec text unknown"))
		} else if _, e := objectGet("spec", s.Spec.Hash); e != nil {
			return e
		}
		if s.Dirty.DiffBlob != "" {
			if _, e := objectGet("blob", s.Dirty.DiffBlob); e != nil {
				return e
			}
		}
		if s.Repo.Path == "" || !benchExists(s.Repo.Path) {
			add("info", path, fail("repo path missing (info)"))
		}
		if s.CommitBasis != "git" {
			add("info", path, fail("commit basis %s; evidence: %s", s.CommitBasis, s.CommitEvidence))
		}
		return nil
	})
	suites := []string{suite}
	if suite == "" {
		var e error
		suites, e = benchSuiteDirs()
		if e != nil {
			return nil, e
		}
	}
	for _, name := range suites {
		if _, e := benchSuiteLoad(name); e != nil {
			add("error", name, e)
			continue
		}
		runs, e := benchRuns(name)
		if e != nil {
			add("error", name, e)
			continue
		}
		for _, r := range runs {
			path := filepath.Join(r.dir, "run.json")
			if r.Subject == "" {
				add("error", path, fail("missing subject; run bench migrate"))
				continue
			}
			add("error", path, benchVerifyRun(r, filepath.Join(r.dir, "snapshot")))
			for _, raw := range r.Raw {
				if !benchExists(raw) {
					add("info", path, fail("raw path missing: %s", raw))
				}
			}
			if r.Kind == "report-only" {
				add("info", path, fail("report-only: findings and execution time unknown"))
			}
		}
		keys, e := benchKeys(name)
		if e != nil {
			add("error", name, e)
			continue
		}
		for _, k := range keys {
			path := filepath.Join(benchDir(), name, "keys", k.ID+".json")
			_, e := benchSubjectLoad(k.Subject)
			add("error", path, e)
			_, e = objectGet("itemset", k.Itemset)
			add("error", path, e)
			for _, id := range k.PromptBlobs {
				_, e := objectGet("blob", id)
				add("error", path, e)
			}
			original := filepath.Join(benchDir(), name, "keys", k.ID+".original.json")
			if e := benchSafe(original); e != nil {
				add("error", path, e)
				continue
			}
			b, e := os.ReadFile(original)
			if e != nil {
				add("error", path, e)
			} else if benchHash(b) != k.OriginalSHA256 {
				add("error", path, fail("key original sha256 mismatch: %s", k.ID))
			}
			if k.BaseKey != "" {
				_, e := benchKeyLoad(name, k.BaseKey)
				add("error", path, e)
			}
		}
	}
	return findings, nil
}
func cmdBenchVerify(a *app, args []string) error {
	fs := a.flags("bench verify")
	suite := fs.String("suite", "", "verify this suite (objects are global)")
	pos, e := parseArgs(fs, args)
	if e != nil {
		return e
	}
	if e := exactArgs(pos, 0, "bench verify [--suite S]"); e != nil {
		return e
	}
	if *suite != "" {
		if e := benchSuiteName(*suite); e != nil {
			return e
		}
	}
	rows, e := benchVerify(*suite)
	if e != nil {
		return e
	}
	errors, infos := 0, 0
	for _, r := range rows {
		fmt.Fprintf(a.out, "%s: %s: %s\n", r.Level, r.Path, r.Message)
		if r.Level == "error" {
			errors++
		} else {
			infos++
		}
	}
	fmt.Fprintf(a.out, "verify: %d errors, %d info\n", errors, infos)
	if errors > 0 {
		return &exitError{code: 1}
	}
	return nil
}
