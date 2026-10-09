package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func leadDir(dir string) (string, error) {
	if dir == "" {
		dir = os.Getenv("ORCH_TASK_DIR")
	}
	if dir == "" {
		return "", fail("pass --dir <task dir> (or set ORCH_TASK_DIR)")
	}
	dir = expandHome(dir)
	if _, err := os.Stat(filepath.Join(dir, "task.md")); err != nil {
		return "", fail("%s is not a task directory", dir)
	}
	return dir, nil
}

func cmdReport(a *app, args []string) error {
	fs := a.flags("report")
	dirFlag := fs.String("dir", "", "task directory (default: $ORCH_TASK_DIR)")
	key := fs.String("key", "", "slug that a later `resolved` line closes")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return &exitError{code: 2, msg: "usage: orch report [--dir D] [--key K] <state> <text...>"}
	}
	dir, err := leadDir(*dirFlag)
	if err != nil {
		return err
	}
	state, text := pos[0], strings.TrimSpace(strings.Join(pos[1:], " "))
	if !slices.Contains(leadStates, state) {
		return fail("state must be one of: %s", strings.Join(leadStates, ", "))
	}
	if text == "" {
		return fail("a report needs a one-line text")
	}
	if *key != "" && !keyRe.MatchString(*key) {
		return fail("--key must be a short lowercase slug")
	}
	if err := a.appendStatus(dir, state, text, *key, ""); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "reported "+state)
	return nil
}

func cmdInbox(a *app, args []string) error {
	fs := a.flags("inbox")
	dirFlag := fs.String("dir", "", "task directory (default: $ORCH_TASK_DIR)")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	dir, err := leadDir(*dirFlag)
	if err != nil {
		return err
	}
	msgs := pendingMessages(dir)
	if len(msgs) == 0 {
		fmt.Fprintln(a.out, "no new messages")
		return nil
	}
	handled := filepath.Join(dir, "inbox", "handled")
	if err := os.MkdirAll(handled, 0o755); err != nil {
		return err
	}
	for _, p := range msgs {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "--- message %s ---\n%s\n", strings.TrimSuffix(filepath.Base(p), ".msg"), strings.TrimRight(string(b), "\n"))
		if err := os.Rename(p, filepath.Join(handled, filepath.Base(p))); err != nil {
			return err
		}
	}
	return nil
}
