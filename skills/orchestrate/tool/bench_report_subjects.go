package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type benchTotalFlag struct{ total, all bool }

func (v *benchTotalFlag) String() string {
	if v.all {
		return "all"
	}
	return strconv.FormatBool(v.total)
}
func (v *benchTotalFlag) IsBoolFlag() bool { return true }
func (v *benchTotalFlag) Set(s string) error {
	if s == "all" {
		v.total, v.all = true, true
		return nil
	}
	b, e := strconv.ParseBool(s)
	if e != nil {
		return fail("--total must be true, false or all")
	}
	v.total, v.all = b, false
	return nil
}

type benchSubjectSummary struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Runs        []string `json:"runs"`
	Keys        []string `json:"keys"`
	Groups      []string `json:"groups"`
	Itemsets    []string `json:"itemsets"`
	TextStored  bool     `json:"text_stored"`
}

func benchHistorySubjects(runs []*benchRun, key *benchKey) []benchSubjectSummary {
	out := []benchSubjectSummary{}
	index := map[string]int{}
	add := func(id string) int {
		if i, ok := index[id]; ok {
			return i
		}
		s := benchSubjectSummary{ID: id, Description: "subject: not recorded"}
		if subj, e := benchSubjectLoad(id); e == nil {
			v, _ := benchSpecLoad(subj.Spec.Hash)
			s.Description = benchDescribe(*subj, v)
			s.TextStored = objectHas("spec", subj.Spec.Hash)
		}
		index[id] = len(out)
		out = append(out, s)
		return len(out) - 1
	}
	appendUnique := func(list []string, s string) []string {
		if s == "" {
			return list
		}
		for _, old := range list {
			if old == s {
				return list
			}
		}
		return append(list, s)
	}
	for _, r := range runs {
		i := add(r.Subject)
		out[i].Runs = append(out[i].Runs, r.Label)
		for _, group := range benchSortedKeys(r.Groups) {
			if strings.HasPrefix(group, "group:") {
				out[i].Groups = appendUnique(out[i].Groups, group+" ("+strings.Join(r.Groups[group].Packages, ", ")+")")
			}
		}
		label := "none"
		if r.Itemset != "" {
			label = r.Itemset
			path, _ := benchObjectPath("itemset", r.Itemset)
			var set benchItemset
			if benchReadJSON(path, &set) == nil {
				label += " (" + set.Origin + ")"
			}
		}
		out[i].Itemsets = appendUnique(out[i].Itemsets, label)
	}
	if key != nil {
		i := add(key.Subject)
		out[i].Keys = append(out[i].Keys, key.ID)
		out[i].Itemsets = appendUnique(out[i].Itemsets, key.Itemset+" (key)")
	}
	return out
}
func benchSaveReport(a *app, suite string, h benchHistory) error {
	lock := filepath.Join(benchDir(), suite, ".lock")
	if e := benchSafe(lock); e != nil {
		return e
	}
	return benchWithLock(lock, func() error {
		var b strings.Builder
		benchPrintHistory(&b, h)
		dir := filepath.Join(benchDir(), suite, "reports")
		if e := benchMkdir(dir); e != nil {
			return e
		}
		base := a.now().UTC().Format("20060102-1504") + "-history"
		name := base + ".md"
		for i := 2; benchExists(filepath.Join(dir, name)); i++ {
			name = fmt.Sprintf("%s-%d.md", base, i)
		}
		if e := benchImmutable(filepath.Join(dir, name), []byte(b.String())); e != nil {
			return e
		}
		path := filepath.Join(dir, "index.md")
		if e := benchSafe(path); e != nil {
			return e
		}
		old, e := os.ReadFile(path)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		return benchWrite(path, append(old, []byte(fmt.Sprintf("- [%s](%s)\n", name, name))...))
	})
}
