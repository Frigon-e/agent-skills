package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

func cmdBenchMigrate(a *app, args []string) error {
	fs := a.flags("bench migrate")
	suite := fs.String("suite", "", "suite to migrate (default all)")
	run := fs.String("run", "", "one run to reconstruct or correct")
	options := benchRecordFlags(fs)
	pos, e := parseArgs(fs, args)
	if e != nil {
		return e
	}
	if e := exactArgs(pos, 0, "bench migrate [--suite S --run ID] [--commit SHA --commit-basis user|note --commit-evidence TEXT]"); e != nil {
		return e
	}
	opts := options()
	if e := benchRecordOptsCheck(opts); e != nil {
		return e
	}
	if *run != "" && *suite == "" {
		return fail("--run requires --suite")
	}
	if opts.Commit != "" && *run == "" {
		return fail("a historical --commit must target exactly one --run")
	}
	if *suite != "" {
		if e := benchSuiteName(*suite); e != nil {
			return e
		}
	}
	if *run != "" {
		if e := benchName(*run); e != nil {
			return e
		}
	}
	storePath := filepath.Join(benchDir(), "store.json")
	if benchExists(storePath) {
		var store map[string]any
		if e := benchReadJSON(storePath, &store); e != nil {
			return e
		}
		version, _ := store["schema_version"].(float64)
		if version != 1 && version != 2 {
			return fail("unsupported store schema %v", version)
		}
		if version == 1 {
			store["schema_version"] = 2
			if e := benchJSON(storePath, store); e != nil {
				return e
			}
		}
	}
	suites := []string{*suite}
	if *suite == "" {
		suites, e = benchSuiteDirs()
		if e != nil {
			return e
		}
	}
	for _, name := range suites {
		if e := benchSuiteName(name); e != nil {
			return e
		}
		lock := filepath.Join(benchDir(), name, ".lock")
		if e := benchSafe(lock); e != nil {
			return e
		}
		e := benchWithLock(lock, func() error {
			runs, e := benchRuns(name)
			if e != nil {
				return e
			}
			selected := *run == ""
			for _, r := range runs {
				if *run != "" && r.ID != *run && r.Label != *run {
					continue
				}
				selected = true
				if r.SchemaVersion >= 2 && opts.Commit == "" && opts.DirtyFrom == "" {
					fmt.Fprintf(a.out, "migrate: %s/%s already schema 2\n", name, r.ID)
					continue
				}
				snap := filepath.Join(r.dir, "snapshot")
				var rec gateRecord
				if e := benchReadJSON(filepath.Join(snap, "gate.json"), &rec); e != nil {
					return e
				}
				old := r.Subject
				oldBlobs, oldWall, oldPrompts, oldHistory := r.Blobs, r.WallSeconds, r.PromptsStored, r.SubjectHistory
				if e := benchEnrichRun(a, r, &rec, snap, snap, opts); e != nil {
					return e
				}
				if old != "" {
					r.Blobs, r.WallSeconds, r.PromptsStored, r.SubjectHistory = oldBlobs, oldWall, oldPrompts, oldHistory
				}
				if old != "" && old != r.Subject {
					r.SubjectHistory = append(r.SubjectHistory, benchSubjectMove{At: a.now().UTC(), From: old, To: r.Subject, Basis: opts.CommitBasis, Evidence: opts.CommitEvidence})
				}
				// Merge additions into the original JSON, retaining unrecognized v1 fields.
				path := filepath.Join(r.dir, "run.json")
				var original map[string]json.RawMessage
				if e := benchReadJSON(path, &original); e != nil {
					return e
				}
				r.SchemaVersion = 2
				b, _ := json.Marshal(r)
				var updated map[string]json.RawMessage
				_ = json.Unmarshal(b, &updated)
				for _, field := range []string{"schema_version", "subject", "itemset", "blobs", "prompts_stored", "wall_seconds", "subject_history", "warnings", "inputs"} {
					if value, ok := updated[field]; ok {
						original[field] = value
					}
				}
				if e := benchVerifyRun(r, snap); e != nil {
					return e
				}
				if e := benchJSON(path, original); e != nil {
					return e
				}
				fmt.Fprintf(a.out, "migrate: %s/%s -> %s\n", name, r.ID, r.Subject)
				for _, warning := range r.Warnings {
					fmt.Fprintln(a.out, "warning: "+warning)
				}
			}
			if !selected {
				return fail("run %q not found", *run)
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	path := filepath.Join(benchDir(), "store.json")
	if benchExists(path) {
		var store map[string]any
		if e := benchReadJSON(path, &store); e != nil {
			return e
		}
		store["schema_version"] = 2
		if e := benchJSON(path, store); e != nil {
			return e
		}
	} else if e := benchStoreInit(a.now()); e != nil {
		return e
	}
	return benchReadme()
}
