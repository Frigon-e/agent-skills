package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const briefWordLimit = 1500

var (
	taskIDRe         = regexp.MustCompile(`^[a-z][a-z0-9-]{0,26}$`)
	headingRe        = regexp.MustCompile(`^# (\S.*?)\s*$`)
	requiredSections = []string{"Intent", "Scope", "Pointers", "Spec", "Acceptance", "Authority", "Report-back"}
	frontmatterKeys  = []string{"id", "kind", "repo", "base", "harness", "model", "effort", "depends_on"}
)

func taskDir(root, id string) (string, error) {
	if !taskIDRe.MatchString(id) {
		return "", fail("task id %q must match %s", id, taskIDRe)
	}
	return filepath.Join(root, id), nil
}

func leadName(id string) string { return "lead-" + id }

// readTask splits task.md into its frontmatter keys and its body.
func readTask(path string) (map[string]string, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	text, fm := string(b), map[string]string{}
	if !strings.HasPrefix(text, "---\n") {
		return fm, text, nil
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return fm, text, nil
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			fm[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return fm, text[4+end+5:], nil
}

// sections maps each top-level `# Heading` outside code fences to its text.
func sections(body string) map[string]string {
	lines := map[string][]string{}
	name, fenced := "", false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if !fenced {
			if m := headingRe.FindStringSubmatch(line); m != nil {
				name = m[1]
				lines[name] = nil
				continue
			}
		}
		if name != "" {
			lines[name] = append(lines[name], line)
		}
	}
	out := make(map[string]string, len(lines))
	for k, v := range lines {
		out[k] = strings.TrimSpace(strings.Join(v, "\n"))
	}
	return out
}

// labelled returns the value of a `Label: value` line.
func labelled(text, label string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(label) + `:[ \t]*(.*)$`)
	if m := re.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func depends(fm map[string]string) []string {
	var out []string
	for _, d := range strings.Split(strings.Trim(fm["depends_on"], "[] "), ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// checkTask refuses a brief with gaps: problems block spawning, warnings don't.
func checkTask(root, id string) (problems, warnings []string, err error) {
	dir, err := taskDir(root, id)
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "task.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("%s does not exist (run: orch new %s ...)", path, id)}, nil, nil
	}
	fm, body, _ := readTask(path)
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	for _, key := range frontmatterKeys {
		if _, ok := fm[key]; !ok {
			add("frontmatter: missing `%s`", key)
		}
	}
	if fm["id"] != "" && fm["id"] != id {
		add("frontmatter: id %q does not match directory %q", fm["id"], id)
	}
	if k := fm["kind"]; k != "ship" && k != "scout" {
		add("frontmatter: kind must be ship or scout")
	}
	if h := fm["harness"]; h != "claude" && h != "codex" {
		add("frontmatter: harness must be claude or codex")
	}
	for _, key := range []string{"model", "effort", "base"} {
		if v, ok := fm[key]; ok && v == "" {
			add("frontmatter: `%s` is empty (use `default` to inherit)", key)
		}
	}
	if repo := fm["repo"]; repo != "" {
		if st, err := os.Stat(repo); err != nil || !st.IsDir() {
			add("frontmatter: repo %s is not a directory", repo)
		}
	}
	if strings.Contains(string(raw), "{{") {
		add("unfilled {{placeholder}} left in task.md")
	}
	secs := sections(body)
	for _, name := range requiredSections {
		text, ok := secs[name]
		switch {
		case !ok:
			add("section `# %s` is missing", name)
		case text == "":
			add("section `# %s` is empty", name)
		}
	}
	for _, label := range []string{"In", "Out"} {
		if labelled(secs["Scope"], label) == "" {
			add("Scope needs a non-empty `%s:` line", label)
		}
	}
	for _, label := range []string{"May decide", "Must escalate", "Never"} {
		if labelled(secs["Authority"], label) == "" {
			add("Authority needs a non-empty `%s:` line", label)
		}
	}
	for _, dep := range depends(fm) {
		depDir, err := taskDir(root, dep)
		if err != nil {
			add("depends_on: %v", err)
			continue
		}
		if st, err := os.Stat(depDir); err != nil || !st.IsDir() {
			add("depends_on: no task %q", dep)
		} else if lastState(leadEntries(readStatus(depDir))) != "done" {
			add("depends_on: %q is not done yet", dep)
		}
	}
	if words := len(strings.Fields(body)); words > briefWordLimit {
		warnings = append(warnings, fmt.Sprintf("task.md is %d words; leads re-read it, so keep it under %d", words, briefWordLimit))
	}
	return problems, warnings, nil
}
