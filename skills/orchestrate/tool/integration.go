package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// A spec whose packages depend on each other gets one integration branch,
// orch/int-<slug>. A verified ship package is merged into it by `orch verify`,
// and a dependent lead starts from its tip. The branch is never the base
// branch: merging it there is the user's call.

const integrationPrefix = "orch/int-"

// integrationRe is the only form of branch name that reaches git from a
// lead-writable task.md: it cannot start with `-` or hold a ref operator.
var integrationRe = regexp.MustCompile(`^orch/int-([a-z0-9][a-z0-9-]*)$`)

func integrationSlug(branch string) (string, bool) {
	m := integrationRe.FindStringSubmatch(branch)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func integrationDir(root, slug string) string { return filepath.Join(root, "integration", slug) }

// gitOutput runs git and returns stdout and stderr together, for messages.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// integrationTip returns the full sha at the tip of a validated branch.
func integrationTip(repo, branch string) (string, error) {
	sha, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil || !baseSHARe.MatchString(sha) {
		return "", fail("integration branch %s does not exist in %s", branch, repo)
	}
	return sha, nil
}

// isAncestor reports whether sha is reachable from tip.
func isAncestor(repo, sha, tip string) bool {
	_, err := git(repo, "merge-base", "--is-ancestor", sha, tip)
	return err == nil
}

// ensureIntegrationBranch cuts branch from base when it is absent and reuses
// it otherwise.
func ensureIntegrationBranch(repo, branch, base string) error {
	if _, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return nil
	}
	sha, err := git(repo, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if err != nil || !baseSHARe.MatchString(sha) {
		return fail("base %q does not resolve in %s", base, repo)
	}
	if out, err := gitOutput(repo, "branch", branch, sha); err != nil {
		return fail("git branch %s: %s", branch, out)
	}
	return nil
}

// integrate merges a verified ship package into its integration branch, in
// the merge worktree, under the branch's lock. The merged sha is the lead's
// worktree HEAD, never a name from task.md. A conflict is aborted and a
// failing repo check is reset, so the branch only ever moves to a green tip.
func (a *app) integrate(s site, dir, id string, fm map[string]string) error {
	branch := fm["integration"]
	slug, ok := integrationSlug(branch)
	if !ok {
		return fail("task.md integration %q is not an orch integration branch; verify will not pass it to git", branch)
	}
	head, err := git(worktreePath(dir, id), "rev-parse", "HEAD")
	if err != nil || !baseSHARe.MatchString(head) {
		return fail("cannot read HEAD of the worktree of %s", id)
	}
	idir := integrationDir(s.root, slug)
	if err := os.MkdirAll(idir, 0o755); err != nil {
		return err
	}
	return withFlock(filepath.Join(idir, "lock"), func() error {
		tip, err := integrationTip(s.repo, branch)
		if err != nil {
			return err
		}
		if isAncestor(s.repo, head, tip) {
			if err := os.WriteFile(filepath.Join(dir, "integrated"), []byte(head+"\n"), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "integration: already merged into %s\n", branch)
			return nil
		}
		iwt := filepath.Join(idir, "wt")
		if _, err := os.Stat(iwt); err != nil {
			if out, err := gitOutput(s.repo, "worktree", "add", iwt, branch); err != nil {
				return fail("cannot create the merge worktree %s: %s", iwt, out)
			}
		} else if cur, _ := git(iwt, "symbolic-ref", "--short", "HEAD"); cur != branch {
			return fail("merge worktree %s is on %q, not %s", iwt, cur, branch)
		}
		before, err := git(iwt, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("orch: merge %s into %s", id, branch)
		// No hooks: with a tracked hooks directory, the lead's own branch could
		// otherwise run its code in this merge.
		if out, err := gitOutput(iwt, "-c", "core.hooksPath=/dev/null", "merge", "--no-ff", "--no-edit", "-m", msg, head); err != nil {
			conflicts, _ := git(iwt, "diff", "--name-only", "--diff-filter=U")
			_, _ = git(iwt, "merge", "--abort")
			if conflicts == "" {
				return fail("git merge of %s into %s failed: %s", id, branch, out)
			}
			files := strings.Split(conflicts, "\n")
			fmt.Fprintf(a.out, "integration: conflict\nfiles[%d]:\n", len(files))
			for _, f := range files {
				fmt.Fprintf(a.out, "  %s\n", toonValue(f))
			}
			_ = a.appendStatus(dir, "note", fmt.Sprintf("merge into %s conflicted in %d files; left unmerged", branch, len(files)), "", "orch")
			return &exitError{code: 1}
		}
		if s.cfg.Check != "" {
			if code, out := runCheck(iwt, s.cfg.Check); code != 0 {
				if rout, err := gitOutput(iwt, "reset", "--hard", before); err != nil {
					return fail("check failed after merging %s and the reset to %s failed: %s", id, before, rout)
				}
				fmt.Fprintf(a.out, "integration: check failed\noutput (last lines):\n%s\n", out)
				_ = a.appendStatus(dir, "note", fmt.Sprintf("merge into %s failed the repo check; reset to %s", branch, before[:12]), "", "orch")
				return &exitError{code: 1}
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "integrated"), []byte(head+"\n"), 0o644); err != nil {
			return err
		}
		now, _ := git(iwt, "rev-parse", "--short=12", "HEAD")
		_ = a.appendStatus(dir, "note", fmt.Sprintf("merged into %s at %s", branch, now), "", "orch")
		fmt.Fprintf(a.out, "integration: merged into %s (%s)\n", branch, now)
		return nil
	})
}

// requireMerged refuses a dependent whose prerequisites are not in the
// integration branch, and re-pins its base to the branch tip while no
// worktree exists yet. Prerequisites without an integration key (scouts)
// have nothing to merge and are skipped.
func requireMerged(s site, dir, id string, fm map[string]string) error {
	branch := fm["integration"]
	if branch == "" || len(depends(fm)) == 0 {
		return nil
	}
	if _, ok := integrationSlug(branch); !ok {
		return fail("task.md integration %q is not an orch integration branch", branch)
	}
	tip, err := integrationTip(s.repo, branch)
	if err != nil {
		return err
	}
	for _, dep := range depends(fm) {
		depDir, err := taskDir(s.root, dep)
		if err != nil {
			return err
		}
		dfm, _, err := readTask(filepath.Join(depDir, "task.md"))
		if err != nil || dfm["integration"] == "" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(depDir, "integrated"))
		merged := strings.TrimSpace(string(b))
		if !baseSHARe.MatchString(merged) || !isAncestor(s.repo, merged, tip) {
			return fail("dependency %s is done but not merged into %s: run orch verify %s", dep, branch, dep)
		}
	}
	if _, err := os.Stat(worktreePath(dir, id)); err == nil {
		return nil // a respawned lead keeps the base its worktree was cut from
	}
	short, err := git(s.repo, "rev-parse", "--short=12", tip)
	if err != nil || !baseSHARe.MatchString(short) {
		return fail("cannot shorten %s", tip)
	}
	path := filepath.Join(dir, "task.md")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fresh, err := repinBrief(string(b), branch, short)
	if err != nil {
		return err
	}
	if fresh == string(b) {
		return nil
	}
	return os.WriteFile(path, []byte(fresh), 0o644)
}

// repinBrief points a brief at a new base sha: the `base:` front matter line
// and the sha baked into the Report-back worktree command. Both shas must be
// plain hex, as they reach git later.
func repinBrief(text, branch, sha string) (string, error) {
	if !baseSHARe.MatchString(sha) {
		return "", fail("%q is not a commit sha", sha)
	}
	end := strings.Index(text, "\n---\n")
	if !strings.HasPrefix(text, "---\n") || end < 0 {
		return "", fail("task.md has no front matter")
	}
	head, rest := text[:end], text[end:]
	i := strings.Index(head, "\nbase:")
	if i < 0 {
		return "", fail("task.md has no base line")
	}
	line := head[i+1:]
	if j := strings.Index(line, "\n"); j >= 0 {
		line = line[:j]
	}
	_, old, _ := strings.Cut(line, "@")
	old = strings.TrimSpace(old)
	if !baseSHARe.MatchString(old) {
		return "", fail("task.md base %q is not a commit sha", strings.TrimSpace(strings.TrimPrefix(line, "base:")))
	}
	head = head[:i+1] + "base: " + branch + "@" + sha + head[i+1+len(line):]
	tail, after, ok := strings.Cut(rest, "\n# Report-back\n")
	if ok {
		after = strings.ReplaceAll(after, old, sha)
		rest = tail + "\n# Report-back\n" + after
	}
	return head + rest, nil
}

// specInputsHash fingerprints what a package's brief is made from, ignoring
// whitespace, so `orch plan` can tell that the spec moved under a brief.
func specInputsHash(p *specPackage) string {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	parts := []string{p.id, p.kind, p.model, p.effort, p.harness,
		norm(strings.Join(p.deps, ",")), norm(strings.Join(p.owns, ",")), norm(specText(p)), norm(p.acceptance)}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:12]
}

// The branch name comes from the spec file's base name, so two specs named
// alike would share one branch. The first spec to plan it records its path
// here and any other spec path is refused.
func integrationSpecFile(root, slug string) string {
	return filepath.Join(integrationDir(root, slug), "spec")
}

func integrationOwner(root, slug string) string {
	b, _ := os.ReadFile(integrationSpecFile(root, slug))
	return strings.TrimSpace(string(b))
}

func recordIntegrationOwner(root, slug, rel string) error {
	if err := os.MkdirAll(integrationDir(root, slug), 0o755); err != nil {
		return err
	}
	return os.WriteFile(integrationSpecFile(root, slug), []byte(rel+"\n"), 0o644)
}

var integrationSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// cmdIntegration: `orch integration remove <slug>` drops the merge worktree
// of a spec's integration branch. The branch stays; the user merges it into
// the base branch and deletes it.
func cmdIntegration(a *app, args []string) error {
	if len(args) != 2 || args[0] != "remove" {
		return &exitError{code: 2, msg: "usage: orch integration remove <slug>"}
	}
	slug := args[1]
	if !integrationSlugRe.MatchString(slug) {
		return fail("%q is not an integration slug", slug)
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	idir := integrationDir(s.root, slug)
	iwt := filepath.Join(idir, "wt")
	if _, err := os.Stat(iwt); err != nil {
		fmt.Fprintf(a.out, "integration: no merge worktree for %s\n", slug)
		return nil
	}
	lock := filepath.Join(idir, "lock")
	err = withFlock(lock, func() error {
		if dirty, err := git(iwt, "status", "--porcelain"); err != nil || dirty != "" {
			return fail("merge worktree %s has uncommitted changes; clean it up first", iwt)
		}
		if out, err := gitOutput(s.repo, "worktree", "remove", iwt); err != nil {
			return fail("git worktree remove %s: %s", iwt, out)
		}
		fmt.Fprintf(a.out, "integration: removed the merge worktree of %s; the branch %s%s stays for you to merge and delete\n", slug, integrationPrefix, slug)
		return nil
	})
	if err == nil {
		_ = os.Remove(lock) // nothing merges here any more
	}
	return err
}
