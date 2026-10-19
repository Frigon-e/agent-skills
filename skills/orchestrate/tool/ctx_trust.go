package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ctxRepoTrusted reports whether repo-local language servers may run for code.
// In normal mode a git repository and every linked worktree of it are trusted,
// ctx_deny excludes a repository (and so its worktrees), and ctx_trust adds
// paths outside that rule. Read-only mode never trusts a checkout.
func ctxRepoTrusted(code string) bool {
	if ctxReadOnly() {
		return false
	}
	cfg, err := loadUserConfig()
	if err != nil {
		return false
	}
	identity := ctxRepoIdentity(code)
	if ctxRepoListed(code, identity, cfg.CtxDeny) {
		return false
	}
	return identity != "" || ctxRepoListed(code, identity, cfg.CtxTrust)
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ctxRepoListed matches code against a config list by path or by repository.
func ctxRepoListed(code, identity string, roots []string) bool {
	for _, root := range roots {
		root = expandHome(root)
		if evalOr(root) == evalOr(code) {
			return true
		}
		if identity != "" && ctxRepoIdentity(root) == identity {
			return true
		}
	}
	return false
}

// ctxRepoRoots lists the checkouts of the repository that code belongs to: code
// first, then the main checkout and the other linked worktrees. A directory that
// is not in a git repository is its own family.
func ctxRepoRoots(code string) []string {
	roots := []string{code}
	out, err := git(code, "worktree", "list", "--porcelain")
	if err != nil {
		return roots
	}
	seen := map[string]bool{evalOr(code): true}
	var bare bool
	var path string
	flush := func() {
		if path != "" && !bare && !seen[evalOr(path)] && dirExists(path) {
			seen[evalOr(path)] = true
			roots = append(roots, path)
		}
		path, bare = "", false
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			path = strings.TrimPrefix(line, "worktree ")
		case line == "bare":
			bare = true
		}
	}
	flush()
	return roots
}

// Linked worktrees share a common git directory, even when their paths differ.
func ctxRepoIdentity(code string) string {
	dir, err := git(code, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || dir == "" {
		return ""
	}
	return evalOr(dir)
}

func ctxServerSkip(label string) string {
	if ctxReadOnly() {
		return label + " server in the repo not run in read-only mode"
	}
	return label + " server found in the repo, not run (the repo is in ctx_deny or is not a git repo; add it to ctx_trust)"
}

// ctxPathInRoot reports whether path is inside the checkout or any other
// worktree of its repository.
func ctxPathInRoot(code, path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for _, r := range ctxRepoRoots(code) {
		root := evalOr(r)
		if top, err := git(root, "rev-parse", "--show-toplevel"); err == nil && top != "" {
			root = evalOr(top)
		}
		rel, err := filepath.Rel(root, real)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Resolve before execution so PATH and env overrides cannot smuggle in a
// symlink to a server in the inspected checkout.
func ctxServerBinary(code, bin string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return "", err
	}
	if ctxPathInRoot(code, real) && !ctxRepoTrusted(code) {
		return "", fmt.Errorf("%s", ctxServerSkip("language"))
	}
	return real, nil
}
