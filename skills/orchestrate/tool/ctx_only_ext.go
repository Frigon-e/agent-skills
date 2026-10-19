package main

import (
	_ "embed"
	"os"
	"path/filepath"
)

// ctxOnlyExtension is the pi extension that limits a pass's shell to orch ctx.
//
//go:embed pi_ctx_only.ts
var ctxOnlyExtension []byte

// writeCtxOnlyExtension puts the extension next to the PATH shim, so the pass
// loads it with -e from a path that does not depend on where the skill lives.
func writeCtxOnlyExtension(dir string) (string, error) {
	path := filepath.Join(dir, "orch-ctx-only.ts")
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(ctxOnlyExtension) {
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, "ext-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(ctxOnlyExtension); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}
