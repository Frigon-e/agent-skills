package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateAgentPrefixAndTools(t *testing.T) {
	h := setup(t)
	base := newGatePass("a", "package", "shared", "own", "TAG", "task", `{}`, nil)
	base.ctxTool = true
	var prefixes []string
	for _, harness := range []string{"pi", "claude"} {
		p, err := gateAgentPass(h.repo, harness, base)
		if err != nil {
			t.Fatal(err)
		}
		prefixes = append(prefixes, p.shared)
		if !strings.HasPrefix(p.shared, "READ ONLY.") || !strings.HasPrefix(p.prompt, p.shared) || !strings.Contains(p.task, ctxBudgetHint) {
			t.Fatalf("prompt: %+v", p)
		}
		if !strings.Contains(strings.Join(p.cli.Env, "\n"), "ORCH_CTX_ROOT="+evalOr(h.repo)) {
			t.Fatal(p.cli.Env)
		}
		if harness == "pi" && !strings.Contains(strings.Join(piPassTools(p), " "), "read,grep,find,ls,bash") {
			t.Fatal(piPassTools(p))
		}
		if harness == "claude" {
			args := claudePassArgs(profile{Harness: "claude", Model: "haiku"}, p)
			if flagValue(args, "--tools") != "Read,Grep,Glob,Bash" || !strings.Contains(flagValue(args, "--allowedTools"), "Bash(orch ctx:*)") {
				t.Fatal(args)
			}
		}
		twice, _ := gateAgentPass(h.repo, harness, p)
		if twice.prompt != p.prompt || twice.shared != p.shared {
			t.Fatal("notice duplicated")
		}
	}
	if prefixes[0] != prefixes[1] {
		t.Fatal("shared notice differs by harness")
	}
	off := base
	off.ctxTool = false
	p, err := gateAgentPass(h.repo, "pi", off)
	if err != nil || p.shared != off.shared || p.prompt != off.prompt || len(p.cli.Env) != 0 {
		t.Fatal("off changed prompt/env")
	}
	for _, fn := range []func(int, ...bool) string{budgetSentence, budgetSentenceItems} {
		if strings.Contains(fn(10), ctxBudgetHint) || !strings.Contains(fn(10, true), ctxBudgetHint) {
			t.Fatal("budget toggle")
		}
	}
}

func TestGateHeadlessArgumentsAndEnv(t *testing.T) {
	for _, harness := range []string{"pi", "claude"} {
		for _, ctxOn := range []bool{false, true} {
			for _, filesOn := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/ctx-%v/files-%v", harness, ctxOn, filesOn), func(t *testing.T) {
					h := setup(t)
					log := filepath.Join(h.tmp, "args")
					envlog := filepath.Join(h.tmp, "env")
					script := fmt.Sprintf("printf '%%s\\n' \"$@\" > %q\nprintf '%%s\\n' \"$ORCH_CTX_ROOT\" > %q\n", log, envlog)
					if harness == "pi" {
						script += "printf '{}'"
					} else {
						script += `printf '%s\n' '{"type":"result","result":"{}"}'`
					}
					t.Setenv("ORCH_"+strings.ToUpper(harness), fakeRunner(t, harness, script))
					t.Setenv("ORCH_CTX_ROOT", "")
					p := gatePass{name: "test", shared: "shared", task: "task", prompt: "sharedtask", ctxTool: ctxOn, contextFiles: filesOn}
					if _, err := runHeadless(h.repo, profile{Harness: harness, Model: "test"}, p, filepath.Join(h.tmp, "sessions"), ""); err != nil {
						t.Fatal(err)
					}
					args := string(mustRead(t, log))
					env := strings.TrimSpace(string(mustRead(t, envlog)))
					if ctxOn && evalOr(env) != evalOr(h.repo) || !ctxOn && env != "" {
						t.Fatalf("env: %q", env)
					}
					if strings.Contains(args, "READ ONLY.") != ctxOn {
						t.Fatal(args)
					}
					if harness == "pi" {
						if strings.Contains(args, "--no-context-files") == filesOn {
							t.Fatal(args)
						}
						if strings.Contains(args, "read,grep,find,ls,bash") != ctxOn {
							t.Fatal(args)
						}
					} else {
						if strings.Contains(args, "claudeMdExcludes") == filesOn {
							t.Fatal(args)
						}
						if strings.Contains(args, "Bash(orch ctx:*)") != ctxOn {
							t.Fatal(args)
						}
					}
				})
			}
		}
	}
}

// The pane's shell already has the user's PATH: only the shim dir is prepended,
// so the machine PATH is never typed into a pane.
func TestGatePaneEnvPrependsToThePanesOwnPath(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(1)
	h.fakePi(gatePasses(nil))
	var dirs []string
	for i := 0; i < 120; i++ {
		dirs = append(dirs, fmt.Sprintf("/opt/long/path/entry-%03d/bin", i))
	}
	long := strings.Join(dirs, string(os.PathListSeparator))
	t.Setenv("PATH", long+string(os.PathListSeparator)+os.Getenv("PATH"))
	h.must("gate", "spec.md", "--runner", "pi:test", "--ctx-tool", "on")
	exports := 0
	for _, c := range h.gateCalls("pane", "run") {
		cmd := strings.Join(c.Args[3:], " ")
		if len(cmd) > 300 || strings.Contains(cmd, "entry-050") {
			t.Fatalf("pane run typed the machine PATH (%d characters): %.80s...", len(cmd), cmd)
		}
		if strings.HasPrefix(cmd, "export PATH='") {
			exports++
			if !strings.HasSuffix(cmd, `:'"$PATH"`) {
				t.Fatalf("PATH export does not extend the pane's own PATH: %s", cmd)
			}
		}
	}
	if exports == 0 {
		t.Fatal("no PATH export reached the pane")
	}
	_ = gdir
}

// pi asks "Trust project folder?" in a folder it has not seen and waits for a
// key: herdr then sees no agent, or an idle one that never answers. A gate pass
// must never wait on that dialog, and read-only passes need no project files.
func TestGatePanePiIgnoresProjectFilesInsteadOfAskingForTrust(t *testing.T) {
	h := setup(t)
	h.gateRepo(1)
	h.fakePi(gatePasses(nil))
	h.must("gate", "spec.md", "--runner", "pi:test")
	starts := 0
	for _, c := range h.gateCalls("agent", "start") {
		starts++
		if args := strings.Join(c.Args, " "); !strings.Contains(args, "--kind pi") || !strings.Contains(args, " -- --no-approve ") {
			t.Fatalf("pi start can wait on the trust dialog: %s", args)
		}
	}
	if starts == 0 {
		t.Fatal("no pane start recorded")
	}
}

func TestGatePaneCtxAndContextFlags(t *testing.T) {
	for _, harness := range []string{"pi", "claude"} {
		for _, on := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", harness, on), func(t *testing.T) {
				h := setup(t)
				gdir := h.gateRepo(1)
				h.fakePi(gatePasses(nil))
				t.Setenv("ORCH_FAKE_CLAUDE_REPO", h.repo)
				h.must("gate", "spec.md", "--runner", harness+":test", "--ctx-tool", onOff(on), "--context-files", onOff(on))
				calls := h.gateCalls()
				starts := 0
				exports := 0
				for _, c := range calls {
					if len(c.Args) > 1 && c.Args[0] == "pane" && c.Args[1] == "run" && strings.Contains(strings.Join(c.Args, " "), "export ORCH_CTX_ROOT=") {
						exports++
					}
					if len(c.Args) < 2 || c.Args[0] != "agent" || c.Args[1] != "start" {
						continue
					}
					starts++
					args := strings.Join(c.Args, " ")
					prompt, _ := c.Extra["prompt"].(string)
					if strings.HasPrefix(prompt, "READ ONLY.") != on {
						t.Fatal(prompt)
					}
					if harness == "pi" && strings.Contains(args, "--no-context-files") == on {
						t.Fatal(args)
					}
					if harness == "claude" && strings.Contains(args, "claudeMdExcludes") == on {
						t.Fatal(args)
					}
				}
				if starts == 0 || on && exports == 0 || !on && exports != 0 {
					t.Fatalf("starts %d exports %d", starts, exports)
				}
				rec := readRecord(t, gdir)
				if rec.CtxTool != onOff(on) || rec.ContextFiles != onOff(on) {
					t.Fatal(rec)
				}
			})
		}
	}
}

func TestGateToggleValidationAndExperiment(t *testing.T) {
	h := setup(t)
	gdir := h.gateRepo(1)
	h.fakePi(gatePasses(nil))
	for _, flag := range []string{"--ctx-tool", "--context-files"} {
		if code, out := h.run("gate", "spec.md", flag, "maybe"); code != 1 || !strings.Contains(out, "is on or off") {
			t.Fatalf("%d %s", code, out)
		}
	}
	h.must("gate", "spec.md", "--only", "api", "--experiment", "cli", "--ctx-tool", "off", "--context-files", "on")
	rec := readRecord(t, filepath.Join(gdir, "experiments", "cli"))
	if rec.CtxTool != "off" || rec.ContextFiles != "on" {
		t.Fatal(rec)
	}
	h.must("gate", "spec.md", "--only", "api", "--experiment", "default")
	rec = readRecord(t, filepath.Join(gdir, "experiments", "default"))
	if rec.CtxTool != "on" || rec.ContextFiles != "off" {
		t.Fatal(rec)
	}
	out := h.must("gate", "compare", "spec.md", "cli", "default")
	wantContains(t, out, "ctx_tool: off; context_files: on", "ctx_tool: on; context_files: off")
}

func TestGatePassCarriesTheCtxProfileOnlyWhenSet(t *testing.T) {
	h := setup(t)
	env := func(profile string) string {
		got, err := gateAgentPass(h.repo, "pi", gatePass{name: "a", shared: "s", task: "t", prompt: "st", ctxTool: true, ctxProfile: profile})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got.cli.Env, " ")
	}
	if strings.Contains(env(""), "ORCH_CTX_PROFILE") {
		t.Error("the default profile sets no variable")
	}
	if !strings.Contains(env("lean"), "ORCH_CTX_PROFILE=lean") {
		t.Error("ORCH_CTX_PROFILE=lean missing from the pass environment")
	}
}
