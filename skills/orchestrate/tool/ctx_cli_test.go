package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCtxCLIShimNoticeAndTools(t *testing.T) {
	c := newCtxHarness(t)
	exe := filepath.Join(t.TempDir(), "orch-current")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf first"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"pi", "claude"} {
		cfg, err := ctxCLIForExecutable(h, c.repo, exe)
		if err != nil {
			t.Fatal(err)
		}
		tools := []string{"bash"}
		if h == "claude" {
			tools = []string{"Bash(orch ctx:*)"}
		}
		if !reflect.DeepEqual(cfg.Tools, tools) {
			t.Fatalf("tools=%v", cfg.Tools)
		}
		if len(cfg.Env) != 3 || cfg.Env[0] != "ORCH_CTX_ROOT="+evalOr(c.repo) || cfg.Env[1] != "ORCH_CTX_READONLY=1" {
			t.Fatalf("env=%v", cfg.Env)
		}
		shimDir := strings.SplitN(strings.TrimPrefix(cfg.Env[2], "PATH="), string(os.PathListSeparator), 2)[0]
		shim := filepath.Join(shimDir, "orch")
		if !strings.HasPrefix(shimDir, ctxCacheDir(evalOr(c.repo))+string(filepath.Separator)) {
			t.Fatal(shimDir)
		}
		if target, err := os.Readlink(shim); err != nil || target != exe {
			t.Fatalf("target=%q err=%v", target, err)
		}
		again, err := ctxCLIForExecutable(h, c.repo, exe)
		if err != nil || !reflect.DeepEqual(again, cfg) {
			t.Fatalf("not idempotent: %+v %v", again, err)
		}
		lines := strings.Split(cfg.Notice, "\n")
		if len(lines) < 10 || len(lines) > 30 || !strings.HasPrefix(cfg.Notice, "READ ONLY.") {
			t.Fatal(cfg.Notice)
		}
		for _, part := range []string{"edited, created, deleted, moved or formatted", "No network", "orch ctx find", "one behaviour per query", "orch ctx sym", "orch ctx text", "orch ctx batch", "DEFAULT TO BATCH", "in ONE call", "never cd", "native read tool with offset and limit", "issue all the reads in one turn", "Never use sed, cat, head or tail", "A violation fails the review"} {
			if !strings.Contains(cfg.Notice, part) {
				t.Fatal(part)
			}
		}
		if strings.Contains(cfg.Notice, "orch ctx read") || strings.Contains(cfg.Notice, "read:") {
			t.Fatal("notice recommends a script-only read command")
		}
		cmd := exec.Command("/bin/sh", "-c", "orch")
		cmd.Env = append(os.Environ(), cfg.Env...)
		if out, err := cmd.Output(); err != nil || string(out) != "first" {
			t.Fatalf("shim execution=%q %v", out, err)
		}
		// Atomically replace the binary path, like go build does. The old shim
		// must follow the path to the replacement rather than retain an inode.
		next := exe + ".new"
		if err := os.WriteFile(next, []byte("#!/bin/sh\nprintf rebuilt"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, exe); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(shim).Output(); err != nil || string(out) != "rebuilt" {
			t.Fatalf("rebuilt shim=%q %v", out, err)
		}
		if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf first"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CtxCLIFor("unknown", c.repo); err == nil {
		t.Fatal("unknown harness accepted")
	}
	if _, err := CtxCLIFor("pi", t.TempDir()); err == nil {
		t.Fatal("non-repo accepted")
	}
	if cfg, err := CtxCLIFor("pi", c.repo); err != nil || len(cfg.Tools) != 1 {
		t.Fatalf("exported function: %+v %v", cfg, err)
	}
}

func TestCtxCLIGuidanceTruth(t *testing.T) {
	for name, guide := range map[string]string{"review notice": ctxCLINotice, "concept help": ctxQueryGuide} {
		for _, claim := range []string{"proof that something does not exist", "Confirm absence with text", "confirm absence with text", "faster and shows more", "--full lifts the cap"} {
			if strings.Contains(guide, claim) {
				t.Errorf("%s retains misleading claim %q", name, claim)
			}
		}
		for _, advice := range []string{
			"independent lookups in ONE call with orch ctx batch", "native Read/read",
			"ok, empty, partial, failed", "exact, lower_bound, unknown",
			"Semantic candidates never establish absence", "exact identity from labelled candidates",
			"all-failed batch exits 1", "partial batch exits 0 with status: partial",
			"not zero callers", "2000 excerpts", "2000 raw ranks", "four definitions after filters",
			"--full raises finite display caps", "gitignore/hidden rules and the tests filter",
			`ctx_trust = ["/absolute/repo"]`, "ctx_deny wins", "Read-only mode never runs repo-local servers",
		} {
			if !strings.Contains(guide, advice) {
				t.Errorf("%s missing advice %q", name, advice)
			}
		}
	}
	// Assert the actual help command includes the shared advice, not only a
	// constant that the command might stop using in a future refactor.
	c := newCtxHarness(t)
	help := c.must("ctx", "find", "--help")
	for _, advice := range []string{"independent lookups in ONE call with orch ctx batch", "native Read/read", "status: partial", "count_kind"} {
		if !strings.Contains(help, advice) {
			t.Errorf("find help missing %q", advice)
		}
	}
	if strings.Contains(help, "confirm absence") || strings.Contains(help, "faster and shows more") {
		t.Errorf("find help retains misleading advice: %s", help)
	}
	if sym := c.must("ctx", "sym", "--help"); !strings.Contains(sym, "Exact or candidate-labelled definitions") || strings.Contains(sym, "An exact symbol:") {
		t.Errorf("symbol help misstates identity guarantees: %s", sym)
	}
}

func TestCtxCLICommandsOnly(t *testing.T) {
	for _, name := range ctxSubcommands {
		if name == "mcp" {
			t.Fatal("removed command still advertised")
		}
	}
	c := newCtxHarness(t)
	if err := cmdCtx(c.a, []string{"mcp"}); err == nil {
		t.Fatal("removed command accepted")
	}
	pinCtx(t, c)
	for _, sub := range []string{"refresh", "status", "watch", "brief", "mcp"} {
		if err := cmdCtx(c.a, []string{sub}); err == nil {
			t.Fatalf("read-only command allowed: %s", sub)
		}
	}
}
