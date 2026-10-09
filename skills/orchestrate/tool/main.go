// Command orch runs one orchestrator with one Herdr pane per lead.
//
// State for a repo lives in <herdr worktrees dir>/<repo>/.orchestrate/<task-id>/
// (task.md, lead.json, status.log, inbox/, report.md). ORCH_HOME overrides the
// base directory. Per-repo settings come from <repo>/.agents/orchestrate.toml.
// Run it through the skill's scripts/orch wrapper, which builds this binary and
// sets ORCH_SELF and ORCH_SKILL_DIR.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const usage = `orch - one orchestrator, one Herdr pane per lead

Orchestrator:
  orch new <id> --kind ship|scout --harness claude|codex --model M --effort E [--base B] [--depends a,b]
  orch check <id>
  orch spawn <id>
  orch send <id> [--resolve KEY] <text...|->
  orch status [--json] [--all]
  orch watch [--interval S] [--timeout S] [--once]
  orch peek <id> [--lines N]
  orch focus <id>
  orch close <id> [--force]
  orch become [--off]
Lead:
  orch report [--dir D] [--key K] <state> <text...>
  orch inbox [--dir D]
Hooks:
  orch hook pretool|stop|session-start
`

type app struct {
	cwd   string
	stdin io.Reader
	out   io.Writer
	errw  io.Writer
	now   func() time.Time
}

// exitError carries a message for stderr and the process exit code.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func fail(format string, a ...any) error {
	return &exitError{code: 1, msg: fmt.Sprintf(format, a...)}
}

var commands = map[string]func(*app, []string) error{
	"new":    cmdNew,
	"check":  cmdCheck,
	"spawn":  cmdSpawn,
	"send":   cmdSend,
	"status": cmdStatus,
	"watch":  cmdWatch,
	"peek":   cmdPeek,
	"focus":  cmdFocus,
	"close":  cmdClose,
	"become": cmdBecome,
	"report": cmdReport,
	"inbox":  cmdInbox,
	"hook":   cmdHook,
}

func main() {
	cwd, _ := os.Getwd()
	a := &app{cwd: cwd, stdin: os.Stdin, out: os.Stdout, errw: os.Stderr, now: time.Now}
	os.Exit(a.run(os.Args[1:]))
}

func (a *app) run(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(a.out, usage)
		return 0
	}
	fn, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(a.errw, "orch: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	err := fn(a, args[1:])
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintf(a.errw, "orch: %s\n", ee.msg)
		}
		return ee.code
	}
	fmt.Fprintf(a.errw, "orch: %v\n", err)
	return 1
}

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("orch "+name, flag.ContinueOnError)
	fs.SetOutput(a.errw)
	return fs
}

// parseArgs parses flags anywhere among the positional arguments; everything
// after a literal "--" is positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, s := range args {
		if s == "--" {
			tail, args = args[i+1:], args[:i]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, &exitError{code: 2}
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	return append(pos, tail...), nil
}

func exactArgs(pos []string, n int, form string) error {
	if len(pos) != n {
		return &exitError{code: 2, msg: "usage: orch " + form}
	}
	return nil
}
