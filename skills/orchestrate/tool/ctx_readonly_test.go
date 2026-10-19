package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pinCtx(t *testing.T, c *ctxHarness) {
	t.Helper()
	t.Setenv("ORCH_CTX_ROOT", evalOr(c.repo))
	t.Setenv("ORCH_CTX_READONLY", "1")
}

func TestCtxReadonlyCommandsAndRoot(t *testing.T) {
	c := newCtxHarness(t)
	pinCtx(t, c)
	for _, sub := range []string{"refresh", "watch", "status", "brief", "nonesuch"} {
		err := cmdCtx(c.a, []string{sub})
		if err == nil {
			t.Fatalf("allowed %s", sub)
		}
	}
	c.a.cwd = t.TempDir()
	wantContains(t, c.must("ctx", "--readonly", "read", "pay/pay.go"), "type Gateway interface")
	wantContains(t, c.must("ctx", "batch", "-q", "read:pay/pay.go"), "type Gateway interface")
	err := cmdCtx(c.a, []string{"batch", filepath.Join(t.TempDir(), "outside.json")})
	if err == nil {
		t.Fatal("allowed batch plan outside root")
	}
	t.Setenv("ORCH_CTX_ROOT", "")
	if err := cmdCtx(c.a, []string{"read", "pay/pay.go"}); err == nil {
		t.Fatal("missing root allowed")
	}
}

func TestCtxReadonlyDoesNotBuildOrRefresh(t *testing.T) {
	for _, rev := range []string{"", strings.Repeat("b", 40)} {
		t.Run(rev, func(t *testing.T) {
			c := newCtxHarness(t)
			pinCtx(t, c)
			if err := os.WriteFile(filepath.Join(c.jbDir, "rev"), []byte(rev), 0o644); err != nil {
				t.Fatal(err)
			}
			out := c.must("ctx", "find", "Code that starts a checkout")
			wantContains(t, out, "read-only ctx: index builds and refreshes are disabled")
			if strings.Contains(c.log(c.jbCalls), "index ") {
				t.Fatalf("index mutated: %s", c.log(c.jbCalls))
			}
			got, _ := os.ReadFile(filepath.Join(c.jbDir, "rev"))
			if string(got) != rev {
				t.Fatalf("revision changed: %s", got)
			}
		})
	}
}

func TestCtxReadonlyRejectsTraversalAndSymlinks(t *testing.T) {
	c := newCtxHarness(t)
	pinCtx(t, c)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(c.repo, "escape")); err != nil {
		t.Fatal(err)
	}
	e, err := ctxEnvFor(c.a)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{outside, "../secret", "escape"} {
		if ls := e.Lines(p); len(ls) > 0 {
			t.Fatalf("read %s", p)
		}
		if _, err := e.Text(rgQuery{Pattern: "secret", Path: p}); err == nil {
			t.Fatalf("searched %s", p)
		}
		v := e.readLookup(ctxLookup{Path: p}, ctxFmt{})
		if v.Err == "" {
			t.Fatalf("read lookup %s", p)
		}
	}
	if c.log(c.rgCalls) != "" {
		t.Fatal("rg ran for rejected paths")
	}
	for _, p := range e.files() {
		if p == "escape" {
			t.Fatal("escape in tracked files")
		}
	}
}
