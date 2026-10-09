package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const panesPerTab = 4

func cmdNew(a *app, args []string) error {
	fs := a.flags("new")
	kind := fs.String("kind", "", "ship or scout")
	harness := fs.String("harness", "", "claude or codex")
	model := fs.String("model", "", "model id or alias, or `default`")
	effort := fs.String("effort", "", "effort level, or `default`")
	base := fs.String("base", "", "base branch (default: repo config, else the current branch)")
	deps := fs.String("depends", "", "comma-separated task ids that must be done first")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := exactArgs(pos, 1, "new <id> --kind ship|scout --harness claude|codex --model M --effort E"); err != nil {
		return err
	}
	switch {
	case *kind != "ship" && *kind != "scout":
		return fail("--kind must be ship or scout")
	case *harness != "claude" && *harness != "codex":
		return fail("--harness must be claude or codex")
	case *model == "" || *effort == "":
		return fail("--model and --effort are required (use `default` to inherit)")
	}
	id := pos[0]
	s, err := a.site()
	if err != nil {
		return err
	}
	dir, err := taskDir(s.root, id)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "task.md")
	if _, err := os.Stat(path); err == nil {
		return fail("%s already exists; edit it instead", path)
	}
	baseBranch := s.cfg.BaseBranch
	if *base != "" {
		baseBranch = *base
	}
	sha, err := git(s.repo, "rev-parse", "--short=12", baseBranch+"^{commit}")
	if err != nil || sha == "" {
		return fail("base %q does not resolve in %s", baseBranch, s.repo)
	}
	var depList []string
	for _, d := range strings.Split(*deps, ",") {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		if !taskIDRe.MatchString(d) {
			return fail("--depends: %q is not a task id", d)
		}
		depList = append(depList, d)
	}
	skill, err := skillDir()
	if err != nil {
		return err
	}
	tmpl, err := os.ReadFile(filepath.Join(skill, "references", "task-template.md"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "inbox", "handled"), 0o755); err != nil {
		return err
	}
	text := strings.NewReplacer(
		"@@ID@@", id,
		"@@KIND@@", *kind,
		"@@REPO@@", s.repo,
		"@@BASE@@", baseBranch+"@"+sha,
		"@@HARNESS@@", *harness,
		"@@MODEL@@", *model,
		"@@EFFORT@@", *effort,
		"@@DEPENDS@@", strings.Join(depList, ", "),
		"@@REPORT_BACK@@", reportBack(dir, id, *kind, s.repo, baseBranch, sha, s.cfg, skill),
	).Replace(string(tmpl))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(a.out, path)
	return nil
}

// reportBack is the generated section that tells a lead how to talk back.
func reportBack(dir, id, kind, repo, base, sha string, cfg config, skill string) string {
	me := selfPath()
	deliverable := fmt.Sprintf("commits on your own branch, in your own worktree, never in the main checkout: "+
		"`git -C %s worktree add -b <branch> %s %s` (branch per the repo's convention, else `orch/%s`). "+
		"Name the branch and head sha in your done line; never merge into `%s` or push unless a message tells you to",
		repo, worktreePath(dir, id), sha, id, base)
	if kind == "scout" {
		deliverable = fmt.Sprintf("a self-contained report at `%s` (findings with file:line evidence, then a recommendation); no branch, no edits to the repo", filepath.Join(dir, "report.md"))
	}
	skills := "none"
	if len(cfg.LeadSkills) > 0 {
		skills = strings.Join(cfg.LeadSkills, ", ")
	}
	lines := []string{
		fmt.Sprintf("You are lead `%s`. The orchestrator is your only contact: never address the user directly; anything for them goes up as needs-decision.", id),
		fmt.Sprintf("Read the lead protocol before starting: `%s`.", filepath.Join(skill, "references", "lead.md")),
		fmt.Sprintf("- Report: `%s report --dir %s <state> [--key <slug>] \"<one line>\"`. States: working, needs-decision, blocked, paused, done, failed, note.", me, dir),
		fmt.Sprintf("- Messages: when a line starting `orch:` says a message arrived, run `%s inbox --dir %s`.", me, dir),
		"- Deliverable: " + deliverable + ".",
		"- Skills to load for the work: " + skills + ".",
	}
	if cfg.Check != "" {
		lines = append(lines, fmt.Sprintf("- Repo check command: `%s`.", cfg.Check))
	}
	return strings.Join(lines, "\n")
}

// worktreePath puts a ship lead's worktree beside the run state: next to the
// other Herdr worktrees of the repo, or under the run root otherwise.
func worktreePath(dir, id string) string {
	root := filepath.Dir(dir)
	if filepath.Base(root) == ".orchestrate" {
		return filepath.Join(filepath.Dir(root), id)
	}
	return filepath.Join(root, "worktrees", id)
}

func cmdCheck(a *app, args []string) error {
	if err := exactArgs(args, 1, "check <id>"); err != nil {
		return err
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	problems, warnings, err := checkTask(s.root, args[0])
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(a.out, "warning: "+w)
	}
	for _, p := range problems {
		fmt.Fprintln(a.out, "refused: "+p)
	}
	if len(problems) > 0 {
		return &exitError{code: 1}
	}
	fmt.Fprintf(a.out, "%s: ok\n", args[0])
	return nil
}

func cmdSpawn(a *app, args []string) error {
	if err := exactArgs(args, 1, "spawn <id>"); err != nil {
		return err
	}
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("HERDR_WORKSPACE_ID") == "" {
		return fail("spawn must run inside a Herdr pane (HERDR_ENV=1)")
	}
	id := args[0]
	s, err := a.site()
	if err != nil {
		return err
	}
	problems, warnings, err := checkTask(s.root, id)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fail("task.md is not ready:\n  %s", strings.Join(problems, "\n  "))
	}
	dir, _ := taskDir(s.root, id)
	if l, err := loadLead(dir); err != nil {
		return err
	} else if l != nil && l.ClosedAt == 0 {
		return fail("lead %s is already open in pane %s", id, l.PaneID)
	}
	leads := openLeads(s.root)
	if len(leads) >= s.cfg.MaxLeads {
		return &exitError{code: 75, msg: fmt.Sprintf(
			"deferred: %d/%d leads open; close one first or raise max_leads in %s",
			len(leads), s.cfg.MaxLeads, filepath.Join(s.repo, ".agents", "orchestrate.toml"))}
	}
	fm, _, err := readTask(filepath.Join(dir, "task.md"))
	if err != nil {
		return err
	}
	name := leadName(id)
	env := []string{"ORCH_TASK_DIR=" + dir, "ORCH_TASK_ID=" + id, "ORCH_ROLE=lead"}
	paneID, tabID, err := placePane(leads, s.repo, env)
	if err != nil {
		return err
	}
	_, _ = herdrCmd(herdrTimeout, "pane", "rename", paneID, name)
	l := &lead{ID: id, Name: name, Harness: fm["harness"], Model: fm["model"], PaneID: paneID, TabID: tabID, SpawnedAt: a.now().Unix()}
	if err := saveLead(dir, l); err != nil {
		return err
	}
	start := append([]string{"agent", "start", name, "--kind", fm["harness"], "--pane", paneID, "--timeout", "90000", "--"}, harnessArgs(fm, s.cfg)...)
	if _, err := herdrCmd(120*time.Second, start...); err != nil {
		_ = a.appendStatus(dir, "note", "agent did not report ready in pane "+paneID, "", "orch")
		fmt.Fprintf(a.out, "opened pane %s for %s, but the agent is not ready (%v). Look with: orch peek %s; give up with: orch close %s --force\n", paneID, name, err, id, id)
		return nil
	}
	launch := fmt.Sprintf("You are orchestrate lead %q. Read %s and follow it exactly, starting with its Report-back section.", id, filepath.Join(dir, "task.md"))
	if _, err := herdrCmd(herdrTimeout, "agent", "prompt", paneID, launch); err != nil {
		return err
	}
	_, waitErr := herdrCmd(30*time.Second, "agent", "wait", paneID, "--until", "working", "--until", "blocked", "--timeout", "20000")
	if err := a.appendStatus(dir, "working", "spawned in pane "+paneID, "", "orch"); err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(a.out, "warning: "+w)
	}
	fmt.Fprintf(a.out, "spawned %s (%s %s) in pane %s\n", name, fm["harness"], fm["model"], paneID)
	if waitErr != nil {
		fmt.Fprintf(a.out, "warning: start of work not confirmed within 20s; check with: orch peek %s\n", id)
	}
	if !watcherAlive(s.root) {
		fmt.Fprintf(a.out, "no watcher running: start `%s watch` with the Monitor tool\n", selfPath())
	}
	return nil
}

func harnessArgs(fm map[string]string, cfg config) []string {
	var out []string
	model, effort := fm["model"], fm["effort"]
	if fm["harness"] == "claude" {
		if model != "default" {
			out = append(out, "--model", model)
		}
		if effort != "default" {
			out = append(out, "--effort", effort)
		}
		return append(out, cfg.ClaudeArgs...)
	}
	if model != "default" {
		out = append(out, "-m", model)
	}
	if effort != "default" {
		out = append(out, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	return append(out, cfg.CodexArgs...)
}

// placePane fills `leads` tabs as 2x2 grids (left, right, bottom-left,
// bottom-right) and opens a new tab once every tab is full.
func placePane(leads []openLead, repo string, env []string) (paneID, tabID string, err error) {
	workspace := os.Getenv("HERDR_WORKSPACE_ID")
	var envFlags []string
	for _, kv := range env {
		envFlags = append(envFlags, "--env", kv)
	}
	byTab := map[string][]string{}
	var tabs []string
	for _, ol := range leads {
		ok, err := paneExists(ol.lead.PaneID)
		if err != nil {
			return "", "", err
		}
		if !ok {
			continue
		}
		if _, seen := byTab[ol.lead.TabID]; !seen {
			tabs = append(tabs, ol.lead.TabID)
		}
		byTab[ol.lead.TabID] = append(byTab[ol.lead.TabID], ol.lead.PaneID)
	}
	for _, tab := range tabs {
		panes := byTab[tab]
		if !strings.HasPrefix(tab, workspace+":") || len(panes) >= panesPerTab {
			continue
		}
		target, direction := panes[0], "right"
		switch len(panes) {
		case 2:
			direction = "down"
		case 3:
			target, direction = panes[1], "down"
		}
		var res struct {
			Result struct {
				Pane struct {
					PaneID string `json:"pane_id"`
				} `json:"pane"`
			} `json:"result"`
		}
		split := append([]string{"pane", "split", target, "--direction", direction, "--cwd", repo, "--no-focus"}, envFlags...)
		if err := herdrJSON(&res, herdrTimeout, split...); err != nil {
			return "", "", err
		}
		return res.Result.Pane.PaneID, tab, nil
	}
	label := "leads"
	if len(tabs) > 0 {
		label = fmt.Sprintf("leads-%d", len(tabs)+1)
	}
	var res struct {
		Result struct {
			Tab struct {
				TabID string `json:"tab_id"`
			} `json:"tab"`
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	create := append([]string{"tab", "create", "--workspace", workspace, "--cwd", repo, "--label", label, "--no-focus"}, envFlags...)
	if err := herdrJSON(&res, herdrTimeout, create...); err != nil {
		return "", "", err
	}
	return res.Result.RootPane.PaneID, res.Result.Tab.TabID, nil
}

func cmdSend(a *app, args []string) error {
	fs := a.flags("send")
	resolve := fs.String("resolve", "", "also close the lead's open decision `KEY`")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return &exitError{code: 2, msg: "usage: orch send <id> [--resolve KEY] <text...|->"}
	}
	if *resolve != "" && !keyRe.MatchString(*resolve) {
		return fail("--resolve must be a short lowercase slug")
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	dir, err := taskDir(s.root, pos[0])
	if err != nil {
		return err
	}
	l, err := loadLead(dir)
	if err != nil {
		return err
	}
	if l == nil {
		return fail("no lead spawned for %s", pos[0])
	}
	body := strings.Join(pos[1:], " ")
	if body == "-" {
		b, err := io.ReadAll(a.stdin)
		if err != nil {
			return err
		}
		body = string(b)
	}
	if strings.TrimSpace(body) == "" {
		return fail("empty message")
	}
	inbox := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(filepath.Join(inbox, "handled"), 0o755); err != nil {
		return err
	}
	seq := 0
	for _, pattern := range []string{"*.msg", "handled/*.msg"} {
		found, _ := filepath.Glob(filepath.Join(inbox, pattern))
		for _, p := range found {
			if n, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(p), ".msg")); err == nil && n > seq {
				seq = n
			}
		}
	}
	name := fmt.Sprintf("%03d", seq+1)
	header := "from: orchestrator\nat: " + a.now().Format(time.RFC3339) + "\n"
	if *resolve != "" {
		header += "re: " + *resolve + "\n"
	}
	if err := writeAtomic(filepath.Join(inbox, name+".msg"), []byte(header+"\n"+strings.TrimRight(body, "\n")+"\n")); err != nil {
		return err
	}
	if *resolve != "" {
		if err := a.appendStatus(dir, "resolved", "answered in message "+name, *resolve, "orch"); err != nil {
			return err
		}
	}
	status := ""
	if l.ClosedAt == 0 {
		if agents, err := agentMap(); err == nil {
			status = agents[l.PaneID].Status
		}
	}
	switch status {
	case "":
		fmt.Fprintf(a.out, "stored message %s; the lead is not running, so no doorbell was sent\n", name)
	case "blocked":
		fmt.Fprintf(a.out, "stored message %s; the lead is waiting at a prompt in its pane, so no doorbell was sent (orch peek %s)\n", name, l.ID)
	default:
		bell := fmt.Sprintf("orch: new message for you. Run: %s inbox --dir %s", selfPath(), dir)
		if _, err := herdrCmd(herdrTimeout, "agent", "prompt", l.PaneID, bell); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "sent message %s to %s\n", name, l.Name)
	}
	return nil
}

type row struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Agent     string  `json:"agent"`
	Herdr     string  `json:"herdr"`
	State     string  `json:"state"`
	Last      string  `json:"last"`
	LastAt    int64   `json:"last_at,omitempty"`
	Pane      string  `json:"pane,omitempty"`
	Decisions []entry `json:"decisions"`
}

func leadRows(root string, includeClosed bool) []row {
	agents, herdrErr := agentMap()
	var rows []row
	for _, dir := range allTasks(root) {
		l, _ := loadLead(dir)
		if l != nil && l.ClosedAt != 0 && !includeClosed {
			continue
		}
		fm, _, _ := readTask(filepath.Join(dir, "task.md"))
		entries := readStatus(dir)
		mine := leadEntries(entries)
		r := row{
			ID:        filepath.Base(dir),
			Kind:      fm["kind"],
			Agent:     fm["harness"] + "/" + fm["model"],
			State:     lastState(mine),
			Decisions: openDecisions(entries),
		}
		last := mine
		if len(last) == 0 {
			last = entries
		}
		if len(last) > 0 {
			e := last[len(last)-1]
			r.Last, r.LastAt = e.State+": "+e.Text, e.At
		}
		switch {
		case l == nil:
			r.Herdr = "draft"
		case l.ClosedAt != 0:
			r.Herdr, r.Pane = "closed", l.PaneID
		case herdrErr != nil:
			r.Herdr, r.Pane = "?", l.PaneID
		default:
			r.Herdr, r.Pane = "gone", l.PaneID
			if ag, ok := agents[l.PaneID]; ok {
				r.Herdr = ag.Status
			}
		}
		if r.State == "" {
			r.State = "draft"
			if l != nil {
				r.State = "spawned"
			}
		}
		rows = append(rows, r)
	}
	return rows
}

func cmdStatus(a *app, args []string) error {
	fs := a.flags("status")
	asJSON := fs.Bool("json", false, "machine-readable output")
	all := fs.Bool("all", false, "include closed leads")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	rows := leadRows(s.root, *all)
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{"root": s.root, "watcher": watcherAlive(s.root), "leads": rows}, "", "  ")
		fmt.Fprintln(a.out, string(b))
		return nil
	}
	fmt.Fprintln(a.out, a.board(s, rows))
	return nil
}

func (a *app) board(s site, rows []row) string {
	live := 0
	for _, r := range rows {
		if r.Herdr != "draft" && r.Herdr != "closed" {
			live++
		}
	}
	watcher := "stopped"
	if watcherAlive(s.root) {
		watcher = "running"
	}
	out := []string{
		"orchestrate: " + s.root,
		fmt.Sprintf("watcher: %s · leads open: %d/%d", watcher, live, s.cfg.MaxLeads),
	}
	if len(rows) == 0 {
		return strings.Join(append(out, "no tasks yet"), "\n")
	}
	out = append(out, fmt.Sprintf("%-20s %-5s %-16s %-8s %6s  %s", "ID", "KIND", "AGENT", "HERDR", "AGE", "LAST"))
	var decisions []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%-20s %-5s %-16s %-8s %6s  %s", r.ID, r.Kind, short(r.Agent, 16), r.Herdr, a.age(r.LastAt), short(r.Last, 70)))
		for _, d := range r.Decisions {
			key := ""
			if d.Key != "" {
				key = " [" + d.Key + "]"
			}
			decisions = append(decisions, fmt.Sprintf("  %s%s %s: %s (%s)", r.ID, key, d.State, short(d.Text, 90), a.age(d.At)))
		}
	}
	if len(decisions) > 0 {
		out = append(out, "open decisions:")
		out = append(out, decisions...)
	}
	return strings.Join(out, "\n")
}

// watchSeen is the watcher's memory of one lead, kept in watch.json.
type watchSeen struct {
	SpawnedAt    int64  `json:"spawned_at"`
	Offset       int64  `json:"offset"`
	Herdr        string `json:"herdr,omitempty"`
	IdleSince    int64  `json:"idle_since,omitempty"`
	LastReport   int64  `json:"last_report,omitempty"`
	QuietAlerted bool   `json:"quiet_alerted,omitempty"`
}

func cmdWatch(a *app, args []string) error {
	fs := a.flags("watch")
	interval := fs.Float64("interval", 5, "seconds between scans")
	timeout := fs.Int("timeout", 0, "checkpoint mode: exit 124 after `N` seconds with nothing actionable")
	once := fs.Bool("once", false, "scan once, then exit")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	pidFile, stateFile := filepath.Join(s.root, "watch.pid"), filepath.Join(s.root, "watch.json")
	if watcherAlive(s.root) {
		b, _ := os.ReadFile(pidFile)
		return fail("a watcher is already running (pid %s)", strings.Fields(string(b))[0])
	}
	myPid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%s %d\n", myPid, a.now().Unix())), 0o644); err != nil {
		return err
	}
	defer func() {
		if b, err := os.ReadFile(pidFile); err == nil && strings.HasPrefix(string(b), myPid+" ") {
			os.Remove(pidFile)
		}
	}()
	seen := map[string]*watchSeen{}
	if b, err := os.ReadFile(stateFile); err == nil {
		_ = json.Unmarshal(b, &seen)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	started, herdrDown := a.now(), false
	say := func(msg string) { fmt.Fprintln(a.out, "orch: "+msg) }
	for {
		emitted := 0
		leads := openLeads(s.root)
		if len(leads) == 0 {
			say("no open leads; watcher exiting")
			return nil
		}
		agents, err := agentMap()
		if err != nil {
			if !herdrDown {
				say(fmt.Sprintf("herdr unreachable, watching status files only (%v)", err))
				emitted++
			}
			herdrDown = true
		} else {
			herdrDown = false
		}
		for _, ol := range leads {
			st := seen[ol.lead.ID]
			if st == nil || st.SpawnedAt != ol.lead.SpawnedAt {
				var offset int64
				if st != nil {
					offset = st.Offset
				}
				st = &watchSeen{SpawnedAt: ol.lead.SpawnedAt, Offset: offset}
				seen[ol.lead.ID] = st
			}
			emitted += a.watchLead(ol, st, agents, s.cfg, say)
		}
		b, _ := json.Marshal(seen)
		if err := writeAtomic(stateFile, b); err != nil {
			return err
		}
		if *once {
			return nil
		}
		if *timeout > 0 {
			if emitted > 0 {
				return nil
			}
			if a.now().Sub(started) >= time.Duration(*timeout)*time.Second {
				say(fmt.Sprintf("checkpoint, nothing actionable in %ds", *timeout))
				return &exitError{code: 124}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Duration(*interval * float64(time.Second))):
		}
	}
}

// watchLead prints one line per actionable event for one lead and returns how
// many it printed. agents is nil while Herdr is unreachable.
func (a *app) watchLead(ol openLead, st *watchSeen, agents map[string]herdrAgent, cfg config, say func(string)) int {
	id, now, emitted := ol.lead.ID, a.now().Unix(), 0
	emit := func(msg string) {
		say(id + " " + msg)
		emitted++
	}
	if f, err := os.Open(filepath.Join(ol.dir, "status.log")); err == nil {
		info, _ := f.Stat()
		if info.Size() < st.Offset {
			st.Offset = 0
		}
		if info.Size() > st.Offset {
			_, _ = f.Seek(st.Offset, io.SeekStart)
			chunk, _ := io.ReadAll(f)
			complete := chunk[:strings.LastIndex(string(chunk), "\n")+1]
			for _, line := range strings.Split(string(complete), "\n") {
				e, ok := parseLine(line)
				if !ok || e.By == "orch" {
					continue
				}
				st.LastReport, st.QuietAlerted = now, false
				if notifyStates[e.State] {
					key := ""
					if e.Key != "" {
						key = " [" + e.Key + "]"
					}
					emit(fmt.Sprintf("%s%s: %s", e.State, key, e.Text))
				}
			}
			st.Offset += int64(len(complete))
		}
		f.Close()
	}
	if agents == nil {
		return emitted
	}
	ag, ok := agents[ol.lead.PaneID]
	if !ok {
		if st.Herdr != "gone" {
			reason := "pane closed"
			if exists, err := paneExists(ol.lead.PaneID); err == nil && exists {
				reason = "agent exited"
			}
			emit(fmt.Sprintf("gone: %s (pane %s); see orch status", reason, ol.lead.PaneID))
		}
		st.Herdr = "gone"
		return emitted
	}
	if ag.Status == "blocked" && st.Herdr != "blocked" {
		emit("ui-blocked: waiting at a prompt in its pane; run orch peek " + id)
	}
	if ag.Status == "idle" || ag.Status == "done" {
		if st.IdleSince == 0 {
			st.IdleSince = now
		}
		waiting := waitingOnOrch[lastState(leadEntries(readStatus(ol.dir)))]
		idleFor := now - max(st.IdleSince, st.LastReport)
		if !waiting && !st.QuietAlerted && idleFor >= int64(cfg.QuietMinutes)*60 {
			emit(fmt.Sprintf("quiet: idle %dm without a report; run orch peek %s", idleFor/60, id))
			st.QuietAlerted = true
		}
	} else {
		st.IdleSince = 0
	}
	st.Herdr = ag.Status
	return emitted
}

func (a *app) requireLead(id string) (string, *lead, error) {
	s, err := a.site()
	if err != nil {
		return "", nil, err
	}
	dir, err := taskDir(s.root, id)
	if err != nil {
		return "", nil, err
	}
	l, err := loadLead(dir)
	if err != nil {
		return "", nil, err
	}
	if l == nil || l.ClosedAt != 0 {
		return "", nil, fail("no open lead for %s", id)
	}
	return dir, l, nil
}

func cmdPeek(a *app, args []string) error {
	fs := a.flags("peek")
	n := fs.Int("lines", 40, "lines to show")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := exactArgs(pos, 1, "peek <id> [--lines N]"); err != nil {
		return err
	}
	_, l, err := a.requireLead(pos[0])
	if err != nil {
		return err
	}
	// Herdr returns nothing for small --lines values, so always ask for 200+.
	out, err := herdrCmd(herdrTimeout, "agent", "read", l.PaneID, "--source", "recent-unwrapped", "--lines", strconv.Itoa(max(*n, 200)))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	fmt.Fprintln(a.out, strings.Join(lines[max(0, len(lines)-*n):], "\n"))
	return nil
}

func cmdFocus(a *app, args []string) error {
	if err := exactArgs(args, 1, "focus <id>"); err != nil {
		return err
	}
	_, l, err := a.requireLead(args[0])
	if err != nil {
		return err
	}
	if _, err := herdrCmd(herdrTimeout, "agent", "focus", l.PaneID); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "focused "+l.Name)
	return nil
}

func cmdClose(a *app, args []string) error {
	fs := a.flags("close")
	force := fs.Bool("force", false, "close a lead that has not reported done or failed (needs the user's agreement)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := exactArgs(pos, 1, "close <id> [--force]"); err != nil {
		return err
	}
	dir, l, err := a.requireLead(pos[0])
	if err != nil {
		return err
	}
	state := lastState(leadEntries(readStatus(dir)))
	if state != "done" && state != "failed" && !*force {
		if state == "" {
			state = "nothing"
		}
		return fail("%s last reported %s; close after done or failed, or pass --force once the user has agreed to drop its work", l.ID, state)
	}
	exists, err := paneExists(l.PaneID)
	if err != nil {
		return err
	}
	if exists {
		_, _ = herdrCmd(herdrTimeout, "pane", "close", l.PaneID)
		if still, err := paneExists(l.PaneID); err != nil || still {
			return fail("pane %s is still open after close", l.PaneID)
		}
	}
	l.ClosedAt = a.now().Unix()
	if err := saveLead(dir, l); err != nil {
		return err
	}
	note := "closed by orchestrator"
	if *force {
		note += " (forced)"
	}
	if err := a.appendStatus(dir, "note", note, "", "orch"); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "closed "+l.Name)
	return nil
}

func cmdBecome(a *app, args []string) error {
	fs := a.flags("become")
	off := fs.Bool("off", false, "turn the orchestrator role off for this pane")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		return fail("become needs a Herdr pane (HERDR_PANE_ID is unset)")
	}
	marker := filepath.Join(rolesDir(), pane)
	if *off {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(a.out, "orchestrator role off for this pane")
		return nil
	}
	var res struct {
		Result struct {
			Pane herdrAgent `json:"pane"`
		} `json:"result"`
	}
	if err := herdrJSON(&res, herdrTimeout, "pane", "current", "--current"); err != nil {
		return err
	}
	session := res.Result.Pane.Session.Value
	if err := os.MkdirAll(rolesDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]any{"session": session, "at": a.now().Unix()})
	if err := os.WriteFile(marker, b, 0o644); err != nil {
		return err
	}
	if session == "" {
		fmt.Fprintf(a.out, "orchestrator role on for pane %s (no agent session found, so it applies to any session in this pane)\n", pane)
		return nil
	}
	fmt.Fprintf(a.out, "orchestrator role on for pane %s (session %s); guards and the status board are active\n", pane, session)
	return nil
}
