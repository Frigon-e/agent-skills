package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	if *key == contextKey && state != "needs-decision" {
		return fail("--key %s is the full-context handoff and must be reported as needs-decision (lead protocol, \"Context budget\")", contextKey)
	}
	if err := a.appendStatus(dir, state, text, *key, ""); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "reported "+state)
	if state == "paused" || state == "note" {
		// These are logged but never wake the orchestrator; say so, so a lead
		// that needs an answer does not wait on one. working is routine and
		// stays quiet.
		fmt.Fprintln(a.out, "the orchestrator is not woken by "+state+"; use needs-decision, blocked, done or failed to reach it")
	}
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
	// New work arrives here, so this is where a lead learns its context is
	// too full to take on something unrelated.
	if l, _ := loadLead(dir); l != nil {
		if agents, err := agentMap(); err == nil {
			ag := agents[l.PaneID]
			uc, _ := loadUserConfig()
			if n := contextTokens(ag.Agent, ag.Session.Value); n >= uc.LeadContextLimit {
				fmt.Fprintf(a.out, "context: %s tokens, over the %s lead limit. If these messages start work unrelated to what you hold, follow \"Context budget\" in the lead protocol before starting it.\n", kTokens(n), kTokens(uc.LeadContextLimit))
			}
		}
	}
	return nil
}

// contextTokens is how many tokens an agent session held at its latest turn,
// read from the tail of its harness transcript; 0 when that can't be found.
func contextTokens(harness, session string) int {
	if session == "" || strings.ContainsAny(session, `/\*?[`) {
		return 0
	}
	var pattern string
	switch harness {
	case "claude":
		pattern = filepath.Join(envOr("CLAUDE_CONFIG_DIR", expandHome("~/.claude")), "projects", "*", session+".jsonl")
	case "codex":
		pattern = filepath.Join(envOr("CODEX_HOME", expandHome("~/.codex")), "sessions", "*", "*", "*", "*-"+session+".jsonl")
	default:
		return 0
	}
	paths, _ := filepath.Glob(pattern)
	if len(paths) == 0 {
		return 0
	}
	lines := bytes.Split(readTail(paths[0], 4<<20), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if n := turnTokens(lines[i]); n > 0 {
			return n
		}
	}
	return 0
}

// turnTokens reads the context size from one transcript line: a Claude
// assistant message's usage or compaction marker, or a Codex token_count
// event.
func turnTokens(line []byte) int {
	if !bytes.Contains(line, []byte(`"usage"`)) && !bytes.Contains(line, []byte(`"token_count"`)) && !bytes.Contains(line, []byte(`"compact_boundary"`)) {
		return 0
	}
	var e struct {
		IsSidechain bool   `json:"isSidechain"`
		Subtype     string `json:"subtype"`
		Compact     struct {
			Post int `json:"postTokens"`
		} `json:"compactMetadata"`
		Message struct {
			Usage struct {
				Input       int `json:"input_tokens"`
				CacheRead   int `json:"cache_read_input_tokens"`
				CacheCreate int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Payload struct {
			Type string `json:"type"`
			Info struct {
				Last struct {
					Input int `json:"input_tokens"`
				} `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &e) != nil || e.IsSidechain {
		return 0
	}
	if e.Payload.Type == "token_count" {
		return e.Payload.Info.Last.Input
	}
	if e.Subtype == "compact_boundary" {
		return e.Compact.Post
	}
	u := e.Message.Usage
	return u.Input + u.CacheRead + u.CacheCreate
}

// readTail returns up to the last n bytes of a file.
func readTail(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > n {
		_, _ = f.Seek(st.Size()-n, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	return b
}

func kTokens(n int) string { return fmt.Sprintf("%dK", (n+500)/1000) }
