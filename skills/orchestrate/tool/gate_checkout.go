package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type checkoutStamp struct {
	status, diff, head string
	files              map[string]string
}

func checkoutGit(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("checkout check: git %s: %w", args[0], err)
	}
	return string(out), nil
}

func checkoutFingerprint(root string) (checkoutStamp, error) {
	s := checkoutStamp{files: map[string]string{}}
	var err error
	if s.head, err = checkoutGit(root, "rev-parse", "HEAD"); err != nil {
		return s, err
	}
	if s.status, err = checkoutGit(root, "status", "--porcelain=v1", "-z", "--untracked-files=all"); err != nil {
		return s, err
	}
	diff, err := checkoutGit(root, "diff", "HEAD", "--binary", "--no-ext-diff", "--no-textconv")
	if err != nil {
		return s, err
	}
	s.diff = fmt.Sprintf("%x", sha256.Sum256([]byte(diff)))
	entries := strings.Split(s.status, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		state, path := entry[:2], entry[3:]
		s.files[path] = state
		if strings.ContainsAny(state, "RC") && i+1 < len(entries) {
			i++
			s.files[entries[i]] = "renamed"
		}
		if strings.Contains(state, "D") {
			continue
		}
		s.files[path] += checkoutPathStamp(root, path)
	}
	return s, nil
}

// checkoutPathStamp stamps one changed or untracked path without following
// links: a symlink (such as a node_modules link) is its target text, a directory
// (a nested repository) its name, a file its content hash. A path that cannot be
// read gets a marker, so the check still sees it change and never fails the run.
func checkoutPathStamp(root, path string) string {
	full := filepath.Join(root, filepath.FromSlash(path))
	fi, err := os.Lstat(full)
	switch {
	case err != nil:
		return "unreadable"
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return "unreadable"
		}
		return "link:" + target
	case fi.IsDir():
		return "dir"
	}
	hash, err := checkoutGit(root, "hash-object", "--no-filters", "--", path)
	if err != nil {
		return "unreadable"
	}
	return hash
}

func checkoutChanges(root string, before, after checkoutStamp) []string {
	paths := map[string]bool{}
	for path, value := range before.files {
		if after.files[path] != value {
			paths[path] = true
		}
	}
	for path, value := range after.files {
		if before.files[path] != value {
			paths[path] = true
		}
	}
	if before.head != after.head {
		names, err := checkoutGit(root, "diff", "--name-only", "-z", strings.TrimSpace(before.head), strings.TrimSpace(after.head))
		if err == nil {
			for _, path := range strings.Split(names, "\x00") {
				if path != "" {
					paths[path] = true
				}
			}
		}
	}
	if len(paths) == 0 && (before.status != after.status || before.diff != after.diff || before.head != after.head) {
		paths["(index or HEAD)"] = true
	}
	var out []string
	for path := range paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

type checkoutGuard struct {
	root          string
	before        checkoutStamp
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	flight        map[string]bool
	files, passes map[string]bool
	failure       string
}

func newCheckoutGuard(root string) (*checkoutGuard, error) {
	before, err := checkoutFingerprint(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &checkoutGuard{root: root, before: before, ctx: ctx, cancel: cancel, flight: map[string]bool{}, files: map[string]bool{}, passes: map[string]bool{}}, nil
}

func (g *checkoutGuard) run(name string, run func() (string, string, error)) (string, string, error) {
	g.mu.Lock()
	if g.ctx.Err() != nil {
		g.mu.Unlock()
		return "", "", g.ctx.Err()
	}
	g.flight[name] = true
	g.mu.Unlock()
	out, hit, err := run()
	g.mu.Lock()
	defer g.mu.Unlock()
	after, checkErr := checkoutFingerprint(g.root)
	var changed []string
	if checkErr == nil {
		changed = checkoutChanges(g.root, g.before, after)
	}
	if checkErr != nil || len(changed) > 0 {
		for _, path := range changed {
			g.files[path] = true
		}
		for pass := range g.flight {
			g.passes[pass] = true
		}
		if checkErr != nil {
			g.failure = checkErr.Error()
		}
		g.cancel()
	}
	delete(g.flight, name)
	if g.ctx.Err() != nil {
		return "", hit, fmt.Errorf("checkout changed or could not be checked; pass discarded")
	}
	return out, hit, err
}

func (g *checkoutGuard) finish(rec *gateRecord) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ctx.Err() == nil {
		return
	}
	rec.TreeChanged = nil
	rec.TreeChangedPasses = nil
	for path := range g.files {
		rec.TreeChanged = append(rec.TreeChanged, path)
	}
	for pass := range g.passes {
		rec.TreeChangedPasses = append(rec.TreeChangedPasses, pass)
	}
	sort.Strings(rec.TreeChanged)
	sort.Strings(rec.TreeChangedPasses)
	rec.TreeCheckError = g.failure
	rec.Status = "unverified"
	rec.Findings = nil
	rec.Questions = nil
	rec.Items = nil
	rec.PassErrors["all"] = "checkout changed or could not be checked; review discarded; no files reverted"
}
