package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// worktreeWithSpec adds a linked worktree on branch feat holding a committed
// spec whose base is feat; the main checkout stays on main without it.
func worktreeWithSpec(t *testing.T, h *harness) string {
	t.Helper()
	wt := filepath.Join(h.tmp, "wt")
	h.git("worktree", "add", "-q", "-b", "feat", wt)
	body := strings.Replace(threePkgSpec(), "base: main", "base: feat", 1)
	writeFiles(t, wt, map[string]string{"spec.md": body})
	for _, args := range [][]string{{"add", "spec.md"}, {"commit", "-q", "-m", "spec"}} {
		if out, err := execGit(wt, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return wt
}

func execGit(dir string, args ...string) (string, error) {
	out, err := git(dir, args...)
	return out, err
}

func TestPlanFromLinkedWorktree(t *testing.T) {
	h := setup(t)
	wt := worktreeWithSpec(t, h)
	h.a.cwd = wt

	out := h.must("plan", "spec.md", "--dry-run")
	wantContains(t, out, "spec: spec.md", "packages: 3", "  store,ship,haiku,,would-create")
	if entries, _ := os.ReadDir(filepath.Join(h.tmp, "state", "repo")); len(entries) > 0 {
		t.Fatalf("dry run created tasks: %v", entries)
	}

	out = h.must("plan", "spec.md")
	wantContains(t, out, "  store,ship,haiku,,created", "  api,ship,sonnet,store,created")
	fm, _, err := readTask(filepath.Join(h.dir("store"), "task.md"))
	if err != nil || fm["spec"] != "spec.md#store" || !strings.HasPrefix(fm["base"], "feat@") {
		t.Fatalf("store: spec = %q base = %q, err %v", fm["spec"], fm["base"], err)
	}

	h.a.cwd = h.repo
	out = h.must("plan", filepath.Join(wt, "spec.md"))
	wantContains(t, out, "  store,ship,haiku,,exists", "  api,ship,sonnet,store,exists")
}

func TestPlanLinkedWorktreeUncommittedEdits(t *testing.T) {
	h := setup(t)
	wt := worktreeWithSpec(t, h)
	b, err := os.ReadFile(filepath.Join(wt, "spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, wt, map[string]string{"spec.md": string(b) + "more\n"})
	h.a.cwd = wt
	code, out := h.run("plan", "spec.md")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	wantContains(t, out, "spec has changes not in feat", "nothing was created")
}

func TestPlanSpecInUnrelatedRepo(t *testing.T) {
	h := setup(t)
	other := filepath.Join(h.tmp, "other")
	if out, err := execGit(h.tmp, "init", "-q", "-b", "main", other); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	writeFiles(t, other, map[string]string{"spec.md": threePkgSpec()})
	code, out := h.run("plan", filepath.Join(other, "spec.md"))
	if code == 0 || !strings.Contains(out, "is not inside the repo") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
