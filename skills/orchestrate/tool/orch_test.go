package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary doubles as a fake `herdr` when ORCH_FAKE_HERDR names its
// state file, so the commands run against a recorded, scriptable Herdr.
func TestMain(m *testing.M) {
	if path := os.Getenv("ORCH_FAKE_HERDR"); path != "" {
		os.Exit(fakeHerdr(path, os.Args[1:]))
	}
	os.Exit(m.Run())
}

type fakeAgent struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type fakeState struct {
	Agents  map[string]*fakeAgent `json:"agents"`
	Panes   map[string]string     `json:"panes"`
	Next    int                   `json:"next"`
	Session string                `json:"session"`
	Down    bool                  `json:"down"`
	Calls   [][]string            `json:"calls"`
}

func loadFake(path string) *fakeState {
	st := &fakeState{Agents: map[string]*fakeAgent{}, Panes: map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, st)
	}
	return st
}

func saveFake(path string, st *fakeState) {
	b, _ := json.Marshal(st)
	_ = os.WriteFile(path, b, 0o644)
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func fakeHerdr(path string, args []string) int {
	st := loadFake(path)
	st.Calls = append(st.Calls, args)
	defer saveFake(path, st)
	if len(args) < 2 {
		return 2
	}
	print := func(v any) int {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
		return 0
	}
	switch args[0] + " " + args[1] {
	case "agent list":
		if st.Down {
			fmt.Fprintln(os.Stderr, "connection refused")
			return 1
		}
		var agents []map[string]any
		for pane, ag := range st.Agents {
			agents = append(agents, map[string]any{"pane_id": pane, "tab_id": st.Panes[pane], "agent": ag.Kind, "agent_status": ag.Status})
		}
		return print(map[string]any{"result": map[string]any{"agents": agents}})
	case "pane get":
		if _, ok := st.Panes[args[2]]; !ok {
			fmt.Fprintf(os.Stderr, `{"error":{"code":"pane_not_found","message":"pane %s not found"}}`+"\n", args[2])
			return 1
		}
		return print(map[string]any{"result": map[string]any{"pane": map[string]any{"pane_id": args[2]}}})
	case "tab create":
		st.Next++
		tab, pane := fmt.Sprintf("w1:t%d", st.Next), fmt.Sprintf("w1:p%d", st.Next)
		st.Panes[pane] = tab
		return print(map[string]any{"result": map[string]any{"tab": map[string]any{"tab_id": tab}, "root_pane": map[string]any{"pane_id": pane}}})
	case "pane split":
		st.Next++
		pane := fmt.Sprintf("w1:p%d", st.Next)
		st.Panes[pane] = st.Panes[args[2]]
		return print(map[string]any{"result": map[string]any{"pane": map[string]any{"pane_id": pane}}})
	case "agent start":
		st.Agents[flagValue(args, "--pane")] = &fakeAgent{Name: args[2], Kind: flagValue(args, "--kind"), Status: "idle"}
		return print(map[string]any{})
	case "agent prompt":
		if ag := st.Agents[args[2]]; ag != nil {
			ag.Status = "working"
		}
		return print(map[string]any{})
	case "agent read":
		for i := 1; i <= 250; i++ {
			fmt.Printf("line %d\n", i)
		}
		return 0
	case "pane close":
		delete(st.Panes, args[2])
		delete(st.Agents, args[2])
		return print(map[string]any{})
	case "pane current":
		return print(map[string]any{"result": map[string]any{"pane": map[string]any{"agent_session": map[string]any{"value": st.Session}}}})
	case "pane rename", "agent wait", "agent focus":
		return print(map[string]any{})
	}
	fmt.Fprintf(os.Stderr, "fake herdr: unhandled %v\n", args)
	return 2
}

type harness struct {
	t         *testing.T
	a         *app
	stdin     *bytes.Buffer
	out, errw *bytes.Buffer
	tmp       string
	repo      string
	fake      string
	now       time.Time
}

func setup(t *testing.T) *harness {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skill, _ := filepath.Abs("..")
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"ORCH_HOME": filepath.Join(tmp, "state"), "ORCH_ROLES_DIR": filepath.Join(tmp, "roles"),
		"ORCH_CONFIG":    filepath.Join(tmp, "config.toml"),
		"ORCH_LOCKS_DIR": filepath.Join(tmp, "locks"), "ORCH_LOCK_POLL_MS": "50",
		"ORCH_HERDR_CONFIG": filepath.Join(tmp, "none.toml"),
		"ORCH_SKILL_DIR":    skill, "ORCH_SELF": "/skill/scripts/orch",
		"HERDR_ENV": "1", "HERDR_WORKSPACE_ID": "w1", "HERDR_PANE_ID": "w1:p0",
		"ORCH_HERDR": os.Args[0], "ORCH_FAKE_HERDR": filepath.Join(tmp, "herdr.json"),
		"ORCH_ROLE": "", "ORCH_TASK_DIR": "",
	} {
		t.Setenv(k, v)
	}
	repo := filepath.Join(tmp, "repo")
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}, {"-C", repo, "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	h := &harness{t: t, stdin: &bytes.Buffer{}, out: &bytes.Buffer{}, errw: &bytes.Buffer{}, tmp: tmp, repo: repo,
		fake: filepath.Join(tmp, "herdr.json"), now: time.Unix(1_800_000_000, 0)}
	h.a = &app{cwd: repo, stdin: h.stdin, out: h.out, errw: h.errw, now: func() time.Time { return h.now }}
	return h
}

func (h *harness) run(args ...string) (int, string) {
	h.out.Reset()
	h.errw.Reset()
	code := h.a.run(args)
	return code, h.out.String() + h.errw.String()
}

func (h *harness) must(args ...string) string {
	h.t.Helper()
	code, out := h.run(args...)
	if code != 0 {
		h.t.Fatalf("orch %v exited %d:\n%s", args, code, out)
	}
	return out
}

func (h *harness) hook(event string, payload map[string]any) (int, string) {
	b, _ := json.Marshal(payload)
	h.stdin.Reset()
	h.stdin.Write(b)
	return h.run("hook", event)
}

func (h *harness) dir(id string) string { return filepath.Join(h.tmp, "state", "repo", id) }

var placeholderRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

// task scaffolds and fills a brief ready to spawn.
func (h *harness) task(id string, extra ...string) {
	h.t.Helper()
	h.must(append([]string{"new", id, "--kind", "ship", "--harness", "claude", "--model", "sonnet", "--effort", "high"}, extra...)...)
	path := filepath.Join(h.dir(id), "task.md")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, placeholderRe.ReplaceAll(b, []byte("filled in")), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) fakeState() *fakeState { return loadFake(h.fake) }

func (h *harness) editFake(fn func(*fakeState)) {
	st := h.fakeState()
	fn(st)
	saveFake(h.fake, st)
}

func (h *harness) lead(id string) *lead {
	h.t.Helper()
	l, err := loadLead(h.dir(id))
	if err != nil || l == nil {
		h.t.Fatalf("no lead.json for %s: %v", id, err)
	}
	return l
}

func callsOf(st *fakeState, prefix ...string) [][]string {
	var out [][]string
	for _, c := range st.Calls {
		if len(c) >= len(prefix) && slices.Equal(c[:len(prefix)], prefix) {
			out = append(out, c)
		}
	}
	return out
}

func wantContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

func TestLifecycle(t *testing.T) {
	h := setup(t)
	h.must("new", "t1", "--kind", "ship", "--harness", "claude", "--model", "sonnet", "--effort", "high")
	code, out := h.run("check", "t1")
	if code != 1 {
		t.Fatalf("check on a fresh scaffold exited %d, want 1", code)
	}
	wantContains(t, out, "refused: unfilled {{placeholder}}")

	brief, _ := os.ReadFile(filepath.Join(h.dir("t1"), "task.md"))
	wantContains(t, string(brief), "id: t1", "base: main@", "/skill/scripts/orch report --dir "+h.dir("t1"), "references/lead.md",
		"worktree add -b <branch> "+filepath.Join(h.tmp, "state", "repo", "worktrees", "t1")+" ")
	os.Remove(filepath.Join(h.dir("t1"), "task.md"))
	h.task("t1")
	wantContains(t, h.must("check", "t1"), "t1: ok")

	out = h.must("spawn", "t1")
	wantContains(t, out, "spawned lead-t1 (claude sonnet) in pane w1:p1", "no watcher running")
	l := h.lead("t1")
	if l.PaneID != "w1:p1" || l.TabID != "w1:t1" {
		t.Fatalf("lead placed at %s/%s", l.TabID, l.PaneID)
	}
	st := h.fakeState()
	create := callsOf(st, "tab", "create")
	if len(create) != 1 || flagValue(create[0], "--label") != "leads" || !slices.Contains(create[0], "ORCH_ROLE=lead") || !slices.Contains(create[0], "--no-focus") {
		t.Fatalf("tab create = %v", create)
	}
	start := callsOf(st, "agent", "start", "lead-t1")
	if len(start) != 1 || !slices.Equal(start[0][len(start[0])-5:], []string{"--", "--model", "sonnet", "--effort", "high"}) {
		t.Fatalf("agent start = %v", start)
	}
	if p := callsOf(st, "agent", "prompt", "w1:p1"); len(p) != 1 || !strings.Contains(p[0][3], filepath.Join(h.dir("t1"), "task.md")) {
		t.Fatalf("launch prompt = %v", p)
	}

	h.must("report", "--dir", h.dir("t1"), "working", "reading the code")
	h.must("report", "--dir", h.dir("t1"), "done", "branch feat/x at abc123")
	out = h.must("watch", "--once")
	if want := "orch: t1 done: branch feat/x at abc123\n"; out != want {
		t.Fatalf("watch printed %q, want %q", out, want)
	}
	if out := h.must("watch", "--once"); out != "" {
		t.Fatalf("second scan repeated events: %q", out)
	}
	wantContains(t, h.must("status"), "t1", "done: branch feat/x", "leads open: 1/3")

	wantContains(t, h.must("close", "t1"), "closed lead-t1")
	if _, ok := h.fakeState().Panes["w1:p1"]; ok {
		t.Fatal("pane still open after close")
	}
	if out := h.must("watch", "--once"); !strings.Contains(out, "no open leads") {
		t.Fatalf("watch with no leads printed %q", out)
	}
}

func TestSpawnGridAndCap(t *testing.T) {
	h := setup(t)
	os.WriteFile(os.Getenv("ORCH_CONFIG"), []byte("max_leads = 5\nclaude_args = [\"--permission-mode\", \"auto\"]\n"), 0o644)
	ids := []string{"a", "b", "c", "d", "e", "f"}
	for _, id := range ids {
		h.task(id)
	}
	for i, id := range ids[:5] {
		h.now = h.now.Add(time.Second)
		h.must("spawn", id)
		_ = i
	}
	pa, pb, pc := h.lead("a").PaneID, h.lead("b").PaneID, h.lead("c").PaneID
	var splits [][2]string
	for _, c := range callsOf(h.fakeState(), "pane", "split") {
		splits = append(splits, [2]string{c[2], flagValue(c, "--direction")})
	}
	want := [][2]string{{pa, "right"}, {pa, "down"}, {pb, "down"}}
	if !slices.Equal(splits, want) {
		t.Fatalf("splits = %v, want %v (c=%s)", splits, want, pc)
	}
	creates := callsOf(h.fakeState(), "tab", "create")
	if len(creates) != 2 || flagValue(creates[1], "--label") != "leads-2" {
		t.Fatalf("tab creates = %v", creates)
	}
	if h.lead("d").TabID != h.lead("a").TabID || h.lead("e").TabID == h.lead("a").TabID {
		t.Fatal("fifth lead should open a second tab, the first four share one")
	}
	start := callsOf(h.fakeState(), "agent", "start", "lead-a")[0]
	if !slices.Equal(start[len(start)-2:], []string{"--permission-mode", "auto"}) {
		t.Fatalf("claude_args not passed: %v", start)
	}
	code, out := h.run("spawn", "f")
	if code != 75 || !strings.Contains(out, "deferred: 5/5 leads open") {
		t.Fatalf("spawn over the cap = %d %q", code, out)
	}
	code, out = h.run("spawn", "a")
	if code != 1 || !strings.Contains(out, "already open") {
		t.Fatalf("respawn of an open lead = %d %q", code, out)
	}
}

func TestCheckRefusals(t *testing.T) {
	h := setup(t)
	h.task("dep")
	h.task("t1", "--depends", "dep")
	path := filepath.Join(h.dir("t1"), "task.md")
	b, _ := os.ReadFile(path)
	text := strings.Replace(string(b), "Out: filled in", "", 1)
	text = strings.Replace(text, "# Pointers\n\nfilled in", "# Pointers\n", 1)
	os.WriteFile(path, []byte(text), 0o644)
	code, out := h.run("check", "t1")
	if code != 1 {
		t.Fatalf("check exited %d", code)
	}
	wantContains(t, out, "Scope needs a non-empty `Out:` line", "section `# Pointers` is empty", `depends_on: "dep" is not done yet`)

	h.must("report", "--dir", h.dir("dep"), "done", "merged")
	code, out = h.run("check", "t1")
	if strings.Contains(out, "depends_on") || code != 1 {
		t.Fatalf("dependency done but still refused: %s", out)
	}

	code, out = h.run("new", "Bad_ID", "--kind", "ship", "--harness", "claude", "--model", "x", "--effort", "y")
	if code != 1 || !strings.Contains(out, "must match") {
		t.Fatalf("bad id accepted: %d %s", code, out)
	}
	code, out = h.run("new", "t2", "--kind", "ship", "--harness", "claude", "--model", "x", "--effort", "y", "--depends", "../x")
	if code != 1 || !strings.Contains(out, "not a task id") {
		t.Fatalf("path-like dependency accepted: %d %s", code, out)
	}
}

func TestSendInboxAndDecisions(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	d := h.dir("t1")
	h.must("report", "--dir", d, "--key", "db", "needs-decision", "Postgres or MySQL?")
	h.must("report", "--dir", d, "--key", "port", "needs-decision", "which port?")
	wantContains(t, h.must("status"), "open decisions:", "t1 [db] needs-decision: Postgres or MySQL?", "t1 [port]")

	out := h.must("send", "t1", "--resolve", "db", "Use", "Postgres")
	wantContains(t, out, "sent message 001 to lead-t1")
	bells := callsOf(h.fakeState(), "agent", "prompt", "w1:p1")
	if len(bells) != 2 || !strings.HasPrefix(bells[1][3], "orch: new message for you. Run: /skill/scripts/orch inbox --dir ") {
		t.Fatalf("doorbell = %v", bells)
	}
	status := h.must("status")
	if strings.Contains(status, "[db]") || !strings.Contains(status, "[port]") {
		t.Fatalf("resolve closed the wrong decision:\n%s", status)
	}

	out = h.must("inbox", "--dir", d)
	wantContains(t, out, "--- message 001 ---", "from: orchestrator", "re: db", "Use Postgres")
	if out := h.must("inbox", "--dir", d); out != "no new messages\n" {
		t.Fatalf("message not acknowledged: %q", out)
	}

	h.editFake(func(st *fakeState) { st.Agents["w1:p1"].Status = "blocked" })
	h.stdin.WriteString("line one\nline two\n")
	wantContains(t, h.must("send", "t1", "-"), "stored message 002", "no doorbell")
	msg, _ := os.ReadFile(filepath.Join(d, "inbox", "002.msg"))
	wantContains(t, string(msg), "line one\nline two\n")

	// The orchestrator's own lines never count as the lead's state.
	if got := lastState(leadEntries(readStatus(d))); got != "needs-decision" {
		t.Fatalf("lead state = %q", got)
	}
}

func TestWatchHerdrEvents(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	if out := h.must("watch", "--once"); out != "" {
		t.Fatalf("working lead produced %q", out)
	}
	h.editFake(func(st *fakeState) { st.Agents["w1:p1"].Status = "blocked" })
	wantContains(t, h.must("watch", "--once"), "orch: t1 ui-blocked")
	if out := h.must("watch", "--once"); out != "" {
		t.Fatalf("ui-blocked repeated: %q", out)
	}

	h.editFake(func(st *fakeState) { st.Agents["w1:p1"].Status = "idle" })
	h.must("watch", "--once")
	h.now = h.now.Add(31 * time.Minute)
	wantContains(t, h.must("watch", "--once"), "orch: t1 quiet: idle 31m without a report")
	h.now = h.now.Add(time.Hour)
	if out := h.must("watch", "--once"); out != "" {
		t.Fatalf("quiet repeated: %q", out)
	}

	h.editFake(func(st *fakeState) { st.Down = true })
	wantContains(t, h.must("watch", "--once"), "herdr unreachable")
	h.editFake(func(st *fakeState) { st.Down = false; delete(st.Agents, "w1:p1") })
	wantContains(t, h.must("watch", "--once"), "orch: t1 gone: agent exited (pane w1:p1)")
	if out := h.must("watch", "--once"); out != "" {
		t.Fatalf("gone repeated: %q", out)
	}
	wantContains(t, h.must("status"), "gone")
}

func TestWatchQuietSkipsWaitingLeads(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	h.must("report", "--dir", h.dir("t1"), "paused", "waiting for the dependency")
	h.editFake(func(st *fakeState) { st.Agents["w1:p1"].Status = "idle" })
	h.must("watch", "--once")
	h.now = h.now.Add(2 * time.Hour)
	if out := h.must("watch", "--once"); out != "" {
		t.Fatalf("paused lead raised %q", out)
	}
}

func TestWatchCheckpointTimeout(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	h.a.now = time.Now
	code, out := h.run("watch", "--timeout", "1", "--interval", "0.05")
	if code != 124 || !strings.Contains(out, "checkpoint") {
		t.Fatalf("checkpoint = %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(h.tmp, "state", "repo", "watch.pid")); !os.IsNotExist(err) {
		t.Fatal("watch.pid left behind")
	}
	h.must("report", "--dir", h.dir("t1"), "failed", "tests do not build")
	code, out = h.run("watch", "--timeout", "5", "--interval", "0.05")
	if code != 0 || !strings.Contains(out, "orch: t1 failed: tests do not build") {
		t.Fatalf("checkpoint with an event = %d %q", code, out)
	}
}

func TestCloseNeedsDoneOrForce(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	code, out := h.run("close", "t1")
	if code != 1 || !strings.Contains(out, "last reported nothing") {
		t.Fatalf("close of a working lead = %d %q", code, out)
	}
	wantContains(t, h.must("close", "t1", "--force"), "closed lead-t1")
	if l := h.lead("t1"); l.ClosedAt == 0 {
		t.Fatal("closed_at not recorded")
	}
	wantContains(t, h.must("status", "--all"), "closed")
	if code, _ := h.run("peek", "t1"); code != 1 {
		t.Fatal("peek of a closed lead should fail")
	}
}

func TestPeekReadsEnoughLines(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	out := h.must("peek", "t1", "--lines", "3")
	if out != "line 248\nline 249\nline 250\n" {
		t.Fatalf("peek = %q", out)
	}
	read := callsOf(h.fakeState(), "agent", "read")[0]
	if flagValue(read, "--lines") != "200" {
		t.Fatalf("peek asked Herdr for %v", read)
	}
}

func TestOrchestratorHooks(t *testing.T) {
	h := setup(t)
	file := filepath.Join(h.repo, "main.go")
	edit := map[string]any{"session_id": "s1", "cwd": h.repo, "tool_name": "Edit", "tool_input": map[string]any{"file_path": file}}
	if code, _ := h.hook("pretool", edit); code != 0 {
		t.Fatal("hook acted without the orchestrator role")
	}

	h.editFake(func(st *fakeState) { st.Session = "s1" })
	wantContains(t, h.must("become"), "orchestrator role on for pane w1:p0 (session s1)")

	code, out := h.hook("pretool", edit)
	if code != 2 || !strings.Contains(out, "Project changes belong to a lead") {
		t.Fatalf("edit in repo = %d %q", code, out)
	}
	edit["session_id"] = "s2"
	if code, _ := h.hook("pretool", edit); code != 0 {
		t.Fatal("marker applied to another session in the same pane")
	}

	cases := []struct {
		tool  string
		input map[string]any
		deny  bool
	}{
		{"Write", map[string]any{"file_path": filepath.Join(h.tmp, "notes.md")}, false},
		{"Agent", map[string]any{"subagent_type": "context-explorer"}, false},
		{"Agent", map[string]any{"subagent_type": "implementer"}, true},
		{"Agent", map[string]any{}, true},
		{"Workflow", map[string]any{}, true},
		{"Bash", map[string]any{"command": "git status && git log -3"}, false},
		{"Bash", map[string]any{"command": "cd x && git commit -m wip"}, true},
		{"Bash", map[string]any{"command": "git -C ../wt merge feat"}, true},
		{"Bash", map[string]any{"command": "gh pr merge 12 --squash"}, true},
		{"Bash", map[string]any{"command": "gh pr view 12"}, false},
		{"Read", map[string]any{"file_path": file}, false},
	}
	for _, c := range cases {
		code, out := h.hook("pretool", map[string]any{"session_id": "s1", "cwd": h.repo, "tool_name": c.tool, "tool_input": c.input})
		if (code == 2) != c.deny {
			t.Errorf("%s %v: exit %d %q, deny=%v", c.tool, c.input, code, out, c.deny)
		}
	}

	// A run root inside a repository stays writable for briefs.
	t.Setenv("ORCH_HOME", filepath.Join(h.repo, ".state"))
	brief := filepath.Join(h.repo, ".state", "repo", "t1", "task.md")
	if code, out := h.hook("pretool", map[string]any{"session_id": "s1", "cwd": h.repo, "tool_name": "Write", "tool_input": map[string]any{"file_path": brief}}); code != 0 {
		t.Fatalf("brief write denied: %q", out)
	}
	t.Setenv("ORCH_HOME", filepath.Join(h.tmp, "state"))

	stop := map[string]any{"session_id": "s1", "cwd": h.repo}
	if code, _ := h.hook("stop", stop); code != 0 {
		t.Fatal("stop blocked with no leads")
	}
	h.task("t1")
	h.must("spawn", "t1")
	code, out = h.hook("stop", stop)
	if code != 2 || !strings.Contains(out, "no watcher is running") {
		t.Fatalf("stop with a lead and no watcher = %d %q", code, out)
	}
	stop["stop_hook_active"] = true
	if code, _ := h.hook("stop", stop); code != 0 {
		t.Fatal("stop hook looped")
	}

	code, out = h.hook("session-start", map[string]any{"session_id": "s1", "cwd": h.repo})
	if code != 0 {
		t.Fatalf("session-start exited %d", code)
	}
	wantContains(t, out, "load the `orchestrate` skill", "t1", "Watcher stopped")

	h.must("become", "--off")
	if code, _ := h.hook("pretool", edit); code != 0 {
		t.Fatal("role still on after become --off")
	}
}

func TestLeadStopGuard(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	d := h.dir("t1")
	t.Setenv("ORCH_ROLE", "lead")
	t.Setenv("ORCH_TASK_DIR", d)
	stop := func(active bool) int {
		code, _ := h.hook("stop", map[string]any{"session_id": "x", "stop_hook_active": active})
		return code
	}
	if stop(false) != 2 {
		t.Fatal("silent first turn not sent back")
	}
	if stop(true) != 0 {
		t.Fatal("guard looped")
	}
	h.must("report", "working", "waiting on two implementers")
	if stop(false) != 0 {
		t.Fatal("turn with a report sent back")
	}
	if stop(false) != 2 {
		t.Fatal("silent working turn not sent back")
	}
	h.must("report", "--key", "db", "needs-decision", "which db?")
	stop(false)
	if stop(false) != 0 {
		t.Fatal("lead waiting on the orchestrator sent back")
	}
	t.Setenv("ORCH_ROLE", "")
	h.must("send", "t1", "--resolve", "db", "postgres")
	t.Setenv("ORCH_ROLE", "lead")
	code, out := h.hook("stop", map[string]any{"session_id": "x"})
	if code != 2 || !strings.Contains(out, "1 unread message(s)") {
		t.Fatalf("answered lead stopping silently = %d %q", code, out)
	}

	code, out = h.hook("session-start", map[string]any{"session_id": "x"})
	if code != 0 {
		t.Fatalf("lead session-start = %d", code)
	}
	wantContains(t, out, `You are orchestrate lead "t1"`, "task.md", "1 unread message(s)")
}

func TestRunRootFollowsHerdrConfig(t *testing.T) {
	h := setup(t)
	t.Setenv("ORCH_HOME", "")
	cfg := filepath.Join(h.tmp, "herdr.toml")
	os.WriteFile(cfg, []byte("[ui]\ntheme = \"x\"\n\n[worktrees]\ndirectory = \""+filepath.Join(h.tmp, "wt")+"\"\n"), 0o644)
	t.Setenv("ORCH_HERDR_CONFIG", cfg)
	if got, want := runRoot(h.repo), filepath.Join(h.tmp, "wt", "repo", ".orchestrate"); got != want {
		t.Fatalf("runRoot = %s, want %s", got, want)
	}
	t.Setenv("ORCH_HERDR_CONFIG", filepath.Join(h.tmp, "missing.toml"))
	if got := runRoot(h.repo); !strings.HasSuffix(got, filepath.Join(".local", "state", "orchestrate", "repo")) {
		t.Fatalf("fallback runRoot = %s", got)
	}
	// From a linked worktree the run root is still the main checkout's.
	wt := filepath.Join(h.tmp, "linked")
	if out, err := exec.Command("git", "-C", h.repo, "worktree", "add", "-q", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v %s", err, out)
	}
	if got, _ := repoRoot(wt); got != h.repo {
		t.Fatalf("repoRoot from worktree = %s", got)
	}
}

func TestConfigRejectsUnknownKeys(t *testing.T) {
	h := setup(t)
	os.MkdirAll(filepath.Join(h.repo, ".agents"), 0o755)
	os.WriteFile(filepath.Join(h.repo, ".agents", "orchestrate.toml"), []byte("max_lead = 2\n"), 0o644)
	code, out := h.run("status")
	if code != 1 || !strings.Contains(out, `unknown key "max_lead"`) {
		t.Fatalf("typo accepted: %d %q", code, out)
	}
}

// A repository must not choose how leads launch or how many run here.
func TestRepoConfigCannotSetUserKeys(t *testing.T) {
	h := setup(t)
	os.MkdirAll(filepath.Join(h.repo, ".agents"), 0o755)
	repoFile := filepath.Join(h.repo, ".agents", "orchestrate.toml")
	for _, line := range []string{
		`claude_args = ["--dangerously-skip-permissions"]`,
		`codex_args = ["--dangerously-bypass-approvals-and-sandbox"]`,
		"max_leads = 50",
		`CLAUDE_ARGS = ["--dangerously-skip-permissions"]`,
		`Codex_Args = ["--dangerously-bypass-approvals-and-sandbox"]`,
	} {
		os.WriteFile(repoFile, []byte(line+"\n"), 0o644)
		code, out := h.run("status")
		if code != 1 || !strings.Contains(out, "is not allowed here") || !strings.Contains(out, os.Getenv("ORCH_CONFIG")) {
			t.Fatalf("repo set %s: %d %q", line, code, out)
		}
	}
	os.Remove(repoFile)
	os.WriteFile(os.Getenv("ORCH_CONFIG"), []byte("check = \"make test\"\n"), 0o644)
	if code, out := h.run("status"); code != 1 || !strings.Contains(out, `"check" is not allowed here`) {
		t.Fatalf("user config set a repo key: %d %q", code, out)
	}
}

// Notes reach the orchestrator on every board, so their size is capped.
func TestRepoNotes(t *testing.T) {
	h := setup(t)
	wantContains(t, h.must("status"), "repo config: none")
	os.MkdirAll(filepath.Join(h.repo, ".agents"), 0o755)
	repoFile := filepath.Join(h.repo, ".agents", "orchestrate.toml")
	os.WriteFile(repoFile, []byte(`notes = ["one test VM: run-e2e one lead at a time"]`+"\n"), 0o644)
	out := h.must("status")
	wantContains(t, out, "note: one test VM: run-e2e one lead at a time")
	if strings.Contains(out, "repo config: none") {
		t.Fatalf("config file present but board says none:\n%s", out)
	}
	for name, notes := range map[string]string{
		"too many":  `["a", "b", "c", "d", "e", "f"]`,
		"too long":  `["` + strings.Repeat("x", maxNoteLen+1) + `"]`,
		"multiline": `["a\nb"]`,
		"blank":     `["  "]`,
	} {
		os.WriteFile(repoFile, []byte("notes = "+notes+"\n"), 0o644)
		if code, out := h.run("status"); code != 1 || !strings.Contains(out, "note") {
			t.Errorf("%s accepted: %d %q", name, code, out)
		}
		if code, _ := h.run("config-check", repoFile); code != 1 {
			t.Errorf("config-check accepted %s", name)
		}
	}
	// A draft elsewhere (a setup lead's worktree) is checked on its own.
	draft := filepath.Join(h.tmp, "draft.toml")
	os.WriteFile(draft, []byte(`check = "make test"`+"\nmax_leads = 9\n"), 0o644)
	if code, out := h.run("config-check", draft); code != 1 || !strings.Contains(out, "is not allowed here") {
		t.Errorf("config-check accepted a user key: %d %q", code, out)
	}
	os.WriteFile(draft, []byte(`notes = ["one VM"]`+"\n"), 0o644)
	wantContains(t, h.must("config-check", draft), "ok:", "1 of 5 notes")
}

// A lock gives one lead a shared resource across many steps, and frees when
// that lead closes or its pane goes away.
func TestLocks(t *testing.T) {
	h := setup(t)
	os.MkdirAll(filepath.Join(h.repo, ".agents"), 0o755)
	os.WriteFile(filepath.Join(h.repo, ".agents", "orchestrate.toml"),
		[]byte(`locks = { e2e-vm = "from deploy until you have read the results" }`+"\n"), 0o644)
	for _, id := range []string{"t1", "t2", "t3"} {
		h.task(id)
	}
	brief, _ := os.ReadFile(filepath.Join(h.dir("t1"), "task.md"))
	wantContains(t, string(brief), "Lock `e2e-vm`: hold it from deploy until you have read the results",
		"lock acquire e2e-vm --dir "+h.dir("t1")+" --wait 3600")
	os.WriteFile(os.Getenv("ORCH_CONFIG"), []byte("max_leads = 5\n"), 0o644)
	for _, id := range []string{"t1", "t2", "t3"} {
		h.must("spawn", id)
	}

	wantContains(t, h.must("lock", "acquire", "e2e-vm", "--dir", h.dir("t1")), "acquired e2e-vm")
	h.must("lock", "acquire", "e2e-vm", "--dir", h.dir("t1")) // a holder may take it again
	code, out := h.run("lock", "acquire", "e2e-vm", "--dir", h.dir("t2"))
	if code != 75 || !strings.Contains(out, "held by t1") {
		t.Fatalf("second holder: %d %q", code, out)
	}
	if code, out := h.run("lock", "release", "e2e-vm", "--dir", h.dir("t2")); code != 1 || !strings.Contains(out, "held by t1") {
		t.Fatalf("non-holder released: %d %q", code, out)
	}
	wantContains(t, h.must("status"), "lock: e2e-vm held by t1")

	// A waiter reports paused, so the board shows why it is idle.
	code, _ = h.run("lock", "acquire", "e2e-vm", "--dir", h.dir("t2"), "--wait", "1")
	if code != 75 || !strings.Contains(lastLine(h.dir("t2")), "paused") || !strings.Contains(lastLine(h.dir("t2")), "waiting for lock e2e-vm") {
		t.Fatalf("waiter: %d, status %q", code, lastLine(h.dir("t2")))
	}

	// Closing the holder frees the lock.
	h.must("report", "--dir", h.dir("t1"), "done", "ran e2e")
	h.must("close", "t1")
	if !strings.Contains(lastLine(h.dir("t1")), "released e2e-vm") {
		t.Fatalf("close note: %q", lastLine(h.dir("t1")))
	}
	h.must("lock", "acquire", "e2e-vm", "--dir", h.dir("t2"))

	// A holder whose pane died is stale: the next acquire takes over.
	h.editFake(func(st *fakeState) { delete(st.Panes, h.lead("t2").PaneID) })
	wantContains(t, h.must("lock", "list"), "held by t2", "stale")
	h.must("lock", "acquire", "e2e-vm", "--dir", h.dir("t3"))
	wantContains(t, h.must("lock", "release", "e2e-vm", "--dir", h.dir("t3")), "released e2e-vm")
	wantContains(t, h.must("lock", "list"), "no locks held")

	// The orchestrator can force a stuck lock free (with the user's agreement).
	h.must("lock", "acquire", "e2e-vm", "--dir", h.dir("t3"))
	wantContains(t, h.must("lock", "release", "e2e-vm", "--force"), "released e2e-vm (forced)")

	for _, bad := range []string{`locks = { "E2E VM" = "x" }`, `locks = { vm = "" }`} {
		os.WriteFile(filepath.Join(h.repo, ".agents", "orchestrate.toml"), []byte(bad+"\n"), 0o644)
		if code, _ := h.run("status"); code != 1 {
			t.Errorf("config accepted %s", bad)
		}
	}
}

// Racing takers get exactly one winner.
func TestLockRace(t *testing.T) {
	h := setup(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := range 12 {
		dir := filepath.Join(h.tmp, fmt.Sprintf("task%d", i))
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "task.md"), []byte("x"), 0o644)
		wg.Add(1)
		go func() {
			defer wg.Done()
			held, err := tryLock(lockHolder{Name: "vm", Lead: filepath.Base(dir), Dir: dir})
			if err != nil {
				t.Error(err)
			}
			if held == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("%d leads took the lock", winners)
	}
}

// Dispatch rules live in the user config only and are printed, not matched.
func TestDispatchRules(t *testing.T) {
	h := setup(t)
	wantContains(t, h.must("dispatch"), "no dispatch rules")
	os.WriteFile(os.Getenv("ORCH_CONFIG"), []byte(`
[[dispatch]]
when = "mechanical edits a test can verify"
use = [{ harness = "claude", model = "haiku", effort = "low" }]
why = "cheap and fast"

[[dispatch]]
when = "large or ambiguous multi-file work"
use = [{ harness = "claude", model = "opus", effort = "high" }, { harness = "codex", model = "gpt-5.5", effort = "high" }]
`), 0o644)
	wantContains(t, h.must("dispatch"),
		"- mechanical edits a test can verify -> claude haiku low (cheap and fast)",
		"- large or ambiguous multi-file work -> claude opus high | codex gpt-5.5 high")
	os.WriteFile(os.Getenv("ORCH_CONFIG"), []byte("[[dispatch]]\nwhen = \"x\"\nuse = [{ harness = \"gemini\", model = \"m\" }]\n"), 0o644)
	if code, _ := h.run("dispatch"); code != 1 {
		t.Fatal("unknown harness accepted")
	}
	os.Remove(os.Getenv("ORCH_CONFIG"))
	os.MkdirAll(filepath.Join(h.repo, ".agents"), 0o755)
	os.WriteFile(filepath.Join(h.repo, ".agents", "orchestrate.toml"), []byte("[[dispatch]]\nwhen = \"x\"\n"), 0o644)
	if code, out := h.run("dispatch"); code != 1 || !strings.Contains(out, "is not allowed here") {
		t.Fatalf("repo set dispatch: %d %q", code, out)
	}
}

func lastLine(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "status.log"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

func TestParseArgsInterspersed(t *testing.T) {
	a := &app{errw: &bytes.Buffer{}}
	fs := a.flags("send")
	resolve := fs.String("resolve", "", "")
	pos, err := parseArgs(fs, []string{"t1", "--resolve", "k", "hello", "-", "--", "--world"})
	if err != nil || *resolve != "k" || !slices.Equal(pos, []string{"t1", "hello", "-", "--world"}) {
		t.Fatalf("pos=%v resolve=%q err=%v", pos, *resolve, err)
	}
}

func TestOpenDecisions(t *testing.T) {
	entries := []entry{
		{State: "needs-decision", Key: "a", Text: "1"},
		{State: "blocked", Text: "keyless"},
		{State: "needs-decision", Key: "b", Text: "2"},
		{State: "resolved", Key: "a", By: "orch"},
		{State: "working", By: "orch"},
	}
	got := openDecisions(entries)
	if len(got) != 2 || got[0].Key != "b" || got[1].Text != "keyless" {
		t.Fatalf("open = %+v", got)
	}
	got = openDecisions(append(entries, entry{State: "working", Text: "moving on"}))
	if len(got) != 1 || got[0].Key != "b" {
		t.Fatalf("lead moving on should close only keyless decisions: %+v", got)
	}
}

const askCard = `---
question: Does a script make the page?
recommend: orch
change: hard
---
## Context
Some <b>context</b> here.

include: ctx.md

## Options
### script: A script makes the page
Gain: fast.
include: diagram.svg A diagram

### orch: The orchestrator writes it
Gain: flexible.
include: shot.png A screenshot

## Next
Then we go on.
`

// askLead spawns a lead t1 with the given open decisions and returns its questions dir.
func askLead(t *testing.T, h *harness, keys ...string) string {
	t.Helper()
	h.task("t1")
	h.must("spawn", "t1")
	for _, k := range keys {
		h.must("report", "--dir", h.dir("t1"), "--key", k, "needs-decision", "Question for "+k+"?")
	}
	qd := filepath.Join(h.dir("t1"), "questions")
	if err := os.MkdirAll(qd, 0o755); err != nil {
		t.Fatal(err)
	}
	return qd
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *harness) askPage() string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.tmp, "state", "repo", "ask", "index.html"))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func TestAsk(t *testing.T) {
	h := setup(t)
	qd := askLead(t, h, "page", "other")
	writeFiles(t, qd, map[string]string{
		"page.md": askCard, "ctx.md": "Included **bold** text.\n", "diagram.svg": `<svg id="inline-svg"></svg>`,
		"shot.png": "PNGDATA",
	})
	out := h.must("ask")
	wantContains(t, out, "index.html", "2 questions", "lavish-axi ", "lavish-axi poll ")
	page := h.askPage()
	wantContains(t, page, "Does a script make the page?", "Question for other?", "Recommended", "Change later: hard",
		"<strong>bold</strong>", `src="files/t1-page-diagram.svg"`, `alt="A diagram"`, `src="files/t1-page-shot.png"`, `alt="A screenshot"`,
		`data-lavish-question="t1/page"`, `data-lavish-question="t1/other"`, "Then we go on.", "Some ")
	if strings.Contains(page, "<b>context") {
		t.Error("raw markdown HTML was emitted")
	}
	if strings.Index(page, "The orchestrator writes it") > strings.Index(page, "A script makes the page") {
		t.Error("recommended option is not first")
	}
	if b, err := os.ReadFile(filepath.Join(h.tmp, "state", "repo", "ask", "files", "t1-page-shot.png")); err != nil || string(b) != "PNGDATA" {
		t.Errorf("png not copied: %v", err)
	}
	h.must("send", "t1", "--resolve", "page", "go")
	h.must("ask")
	page = h.askPage()
	if strings.Contains(page, "Does a script make the page?") || !strings.Contains(page, "Question for other?") {
		t.Errorf("resolved question should leave the page:\n%s", page)
	}
	if _, err := os.Stat(filepath.Join(h.tmp, "state", "repo", "ask", "files", "t1-page-shot.png")); err == nil {
		t.Error("files/ should be rebuilt without the resolved png")
	}
}

func TestAskRefusals(t *testing.T) {
	cases := map[string]struct{ card, want string }{
		"outside":   {"---\nquestion: Q one?\n---\n## Context\ninclude: ../../x.svg\n", "outside the task directory"},
		"symlink":   {"---\nquestion: Q one?\n---\n## Context\ninclude: link.svg\n", "outside the task directory"},
		"noalt":     {"---\nquestion: Q one?\n---\n## Context\ninclude: a.png\n", "needs alt text"},
		"ext":       {"---\nquestion: Q one?\n---\n## Context\ninclude: a.txt\n", "unsupported extension"},
		"missing":   {"---\nquestion: Q one?\n---\n## Context\ninclude: nope.md\n", "file not found"},
		"oneoption": {"---\nquestion: Q one?\n---\n## Options\n### a: Only\nx\n", "one option"},
		"badrec":    {"---\nquestion: Q one?\nrecommend: z\n---\n## Options\n### a: A\nx\n### b: B\ny\n", "not an option"},
		"noq":       {"---\nchange: easy\n---\n", "needs a question"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := setup(t)
			qd := askLead(t, h, "k")
			outside := filepath.Join(h.tmp, "secret.svg")
			writeFiles(t, h.tmp, map[string]string{"secret.svg": "<svg>SECRET</svg>"})
			writeFiles(t, qd, map[string]string{"k.md": c.card, "a.png": "x", "a.txt": "x"})
			if err := os.Symlink(outside, filepath.Join(qd, "link.svg")); err != nil {
				t.Fatal(err)
			}
			code, out := h.run("ask")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			wantContains(t, out, "warning: t1/k", c.want)
			page := h.askPage()
			wantContains(t, page, "Question for k?", "Warning: ", `data-lavish-question="t1/k"`)
			if strings.Contains(page, "SECRET") {
				t.Error("outside file leaked into page")
			}
		})
	}
}

func TestAskEscaping(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	h.must("report", "--dir", h.dir("t1"), "--key", "k", "needs-decision", "Use <b>bold</b>?")
	h.must("report", "--dir", h.dir("t1"), "--key", "c", "needs-decision", "card?")
	qd := filepath.Join(h.dir("t1"), "questions")
	os.MkdirAll(qd, 0o755)
	writeFiles(t, qd, map[string]string{"c.md": "---\nquestion: Card <i>q</i>?\n---\n## Context\n<script>alert(1)</script>\n\nplain\n"})
	h.must("ask")
	page := h.askPage()
	wantContains(t, page, "Use &lt;b&gt;bold&lt;/b&gt;?", "Card &lt;i&gt;q&lt;/i&gt;?", "plain")
	for _, bad := range []string{"<b>bold", "alert(1)</script>", "<i>q"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains unescaped %q", bad)
		}
	}
}

func TestAskEmpty(t *testing.T) {
	h := setup(t)
	out := h.must("ask")
	wantContains(t, out, "No open questions.")
	wantContains(t, h.askPage(), "No open questions.")
}

func TestRenderMarkdownKeepsLineBreaks(t *testing.T) {
	got, err := renderMarkdown("Gain: a\nCost: b\n")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, got, "Gain: a<br>\nCost: b")
}

// A key from status.log goes into a shell command on the page, so a key
// that does not match keyRe must never reach data-resolve.
func TestAskRejectsUnsafeKey(t *testing.T) {
	h := setup(t)
	h.task("t1")
	h.must("spawn", "t1")
	f, err := os.OpenFile(filepath.Join(h.dir("t1"), "status.log"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "needs-decision [key=x; touch pwned]: Pick one?")
	f.Close()
	out := h.must("ask")
	wantContains(t, out, "1 question\n")
	page := h.askPage()
	wantContains(t, page, `data-resolve=""`, "keyless-1", "must match")
	if strings.Contains(page, "touch pwned\"") || strings.Contains(page, `data-key="x;`) {
		t.Errorf("unsafe key reached the page attributes")
	}
}

// Leads can copy outside text into an include, so .html is cleaned and .svg
// is an <img>, where no script runs.
func TestAskIncludeSafety(t *testing.T) {
	h := setup(t)
	qd := askLead(t, h, "k")
	writeFiles(t, qd, map[string]string{
		"k.md": "---\nquestion: Q?\n---\n## Context\ninclude: mock.html\n\ninclude: d.svg A diagram\n",
		"mock.html": `<div class="card" style="padding: 8px" id="lavish" onclick="x()"><table><tr><td>Cell</td></tr></table>` +
			`<script>window.lavish.queuePrompt("evil")</script><a href="javascript:x()">link</a>` +
			`<button>Go</button><input name="note"><form data-lavish-question="t1/k"></form></div>`,
		"d.svg": `<svg><script>alert(1)</script></svg>`,
	})
	h.must("ask")
	page := h.askPage()
	wantContains(t, page, `class="card"`, `style="padding: 8px"`, "<td>Cell</td>", `src="files/t1-k-d.svg"`)
	for _, bad := range []string{`id="lavish"`, "onclick", `queuePrompt("evil")`, "javascript:", "<button>Go", `<input name="note"`, "alert(1)"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if n := strings.Count(page, `data-lavish-question=`); n != 1 {
		t.Errorf("page has %d answer forms, want 1", n)
	}
}
