package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Provider health is per machine, not per repo: a Claude usage limit stops
// every repo's leads. A person sets it with `orch health set`, because no API
// reports a subscription limit; the watcher only hints at it.
var harnesses = []string{"claude", "pi"}

// A lane is what runs out together. Pi reaches Anthropic through its own
// credits, so "pi-anthropic" can be usable while the Claude subscription is
// limited, and OpenRouter can be limited while Anthropic credits remain.
var lanes = []string{"claude", "pi", "pi-anthropic"}

func lane(harness, model string) string {
	if harness == "pi" && strings.HasPrefix(model, "anthropic/") {
		return "pi-anthropic"
	}
	return harness
}

type healthEntry struct {
	State  string `json:"state"`
	Since  int64  `json:"since"`
	Until  int64  `json:"until,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func healthPath() string {
	if p := os.Getenv("ORCH_HEALTH"); p != "" {
		return p
	}
	return filepath.Join(envOr("XDG_STATE_HOME", expandHome("~/.local/state")), "orchestrate", "health.json")
}

// loadHealth returns the limited harnesses at time now. An entry past its
// Until time counts as healthy again.
func loadHealth(now time.Time) map[string]healthEntry {
	out := map[string]healthEntry{}
	b, err := os.ReadFile(healthPath())
	if err != nil {
		return out
	}
	var all map[string]healthEntry
	if json.Unmarshal(b, &all) != nil {
		return out
	}
	for h, e := range all {
		if e.State == "limited" && (e.Until == 0 || e.Until > now.Unix()) {
			out[h] = e
		}
	}
	return out
}

func saveHealth(h map[string]healthEntry) error {
	path := healthPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(h, "", "  ")
	return writeAtomic(path, append(b, '\n'))
}

func (e healthEntry) String() string {
	s := "limited since " + time.Unix(e.Since, 0).Format("15:04")
	if e.Until != 0 {
		s += ", until " + time.Unix(e.Until, 0).Format("15:04")
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

func cmdHealth(a *app, args []string) error {
	fs := a.flags("health")
	forDur := fs.String("for", "", "how long a limit lasts, such as 3h; it clears itself after that")
	reason := fs.String("reason", "", "why the harness is limited")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	cur := loadHealth(a.now())
	if len(pos) == 0 {
		for _, h := range lanes {
			if e, ok := cur[h]; ok {
				fmt.Fprintf(a.out, "%s: %s\n", h, e)
			} else {
				fmt.Fprintf(a.out, "%s: ok\n", h)
			}
		}
		return nil
	}
	if len(pos) != 3 || pos[0] != "set" || !slices.Contains(lanes, pos[1]) || (pos[2] != "ok" && pos[2] != "limited") {
		return &exitError{code: 2, msg: "usage: orch health [set <claude|pi|pi-anthropic> ok|limited [--for DURATION] [--reason TEXT]]"}
	}
	if pos[2] == "ok" {
		delete(cur, pos[1])
	} else {
		e := healthEntry{State: "limited", Since: a.now().Unix(), Reason: strings.TrimSpace(*reason)}
		if *forDur != "" {
			d, err := time.ParseDuration(*forDur)
			if err != nil || d <= 0 {
				return fail("--for %q is not a duration such as 90m or 3h", *forDur)
			}
			e.Until = a.now().Add(d).Unix()
		}
		cur[pos[1]] = e
	}
	if err := saveHealth(cur); err != nil {
		return err
	}
	if pos[2] == "ok" {
		logEvent(filepath.Dir(healthPath()), auditEvent{At: a.now().Unix(), System: "health", Task: pos[1], Result: "ok", Text: pos[1] + " marked ok"})
	} else {
		logEvent(filepath.Dir(healthPath()), auditEvent{At: a.now().Unix(), System: "health", Task: pos[1], Result: "problem", Text: pos[1] + " " + cur[pos[1]].String()})
	}
	if pos[2] == "ok" {
		fmt.Fprintf(a.out, "%s: ok\n", pos[1])
	} else {
		fmt.Fprintf(a.out, "%s: %s\n", pos[1], cur[pos[1]])
		fmt.Fprintln(a.out, "new tasks and spawns on it are refused; see `orch dispatch` for what to use now")
	}
	return nil
}

// harnessUsable refuses a limited harness, and a degraded dispatch profile
// unless the caller allows it. A degraded profile is a weaker stand-in that
// only the user may approve (the orchestrator passes --degraded after asking).
func harnessUsable(cfg config, harness, model string, allowDegraded bool, now time.Time) error {
	if harness == "auto" {
		return nil // resolved by `orch spawn --auto`, which checks the chosen profile
	}
	if err := cfg.modelAllowed(harness, model, now); err != nil {
		return err
	}
	if e, ok := loadHealth(now)[lane(harness, model)]; ok {
		return fail("%s is %s. Pick a usable profile from `orch dispatch`, or if the limit is over run: orch health set %s ok", lane(harness, model), e, lane(harness, model))
	}
	if !allowDegraded && degradedProfile(cfg, harness, model) {
		return fail("%s %s is a degraded fallback in your dispatch rules: it is weaker than the primary profile. Ask the user, then pass --degraded; or wait for the primary to recover", harness, model)
	}
	return nil
}

// degradedProfile reports whether harness+model appears in dispatch rules only
// as a degraded profile.
func degradedProfile(cfg config, harness, model string) bool {
	degraded := false
	for _, r := range cfg.Dispatch {
		for _, p := range r.Use {
			if p.Harness == harness && p.Model == model {
				if !p.Degraded {
					return false
				}
				degraded = true
			}
		}
	}
	return degraded
}

// modelListed reports whether a model is on the allowed list at all, in the
// permanent list or in any conditional one. Config loading uses it, because a
// dispatch profile may name a model that is only allowed in an emergency.
func (c config) modelListed(harness, model string) error {
	if len(c.AllowedModels) == 0 {
		return nil
	}
	if slices.Contains(c.AllowedModels[harness], model) {
		return nil
	}
	for _, w := range c.AllowedWhenLimited {
		if slices.Contains(w.Allow[harness], model) {
			return nil
		}
	}
	return c.notAllowed(harness, model, "")
}

// modelAllowed is the check at the moment of starting an agent. The permanent
// list always counts; a conditional entry counts only while its harness is
// limited, so Haiku can come in when pi is down and not otherwise.
func (c config) modelAllowed(harness, model string, now time.Time) error {
	if len(c.AllowedModels) == 0 {
		return nil
	}
	if slices.Contains(c.AllowedModels[harness], model) {
		return nil
	}
	limited := loadHealth(now)
	for _, w := range c.AllowedWhenLimited {
		if slices.Contains(w.Allow[harness], model) {
			if _, down := limited[w.Harness]; down {
				return nil
			}
			return c.notAllowed(harness, model, fmt.Sprintf("; %s is allowed only while %s is limited, and it is not", model, w.Harness))
		}
	}
	return c.notAllowed(harness, model, "")
}

func (c config) notAllowed(harness, model, extra string) error {
	list := c.AllowedModels[harness]
	if len(list) == 0 && extra == "" {
		return fail("no %s models are allowed (allowed_models in %s)", harness, userConfigPath())
	}
	return fail("%s model %q is not on the allowed list: %s%s. Change allowed_models in %s only if you mean to allow it", harness, model, strings.Join(list, ", "), extra, userConfigPath())
}

func cmdModels(a *app, args []string) error {
	if _, err := parseArgs(a.flags("models"), args); err != nil {
		return err
	}
	s, err := a.site()
	if err != nil {
		return err
	}
	if len(s.cfg.AllowedModels) == 0 {
		fmt.Fprintf(a.out, "allowed_models: not set, so any model is accepted. Set a [allowed_models] table in %s\n", userConfigPath())
		return nil
	}
	limited := loadHealth(a.now())
	for _, h := range harnesses {
		state := "ok"
		if e, ok := limited[h]; ok {
			state = e.String()
		}
		fmt.Fprintf(a.out, "%s (%s):\n", h, state)
		for _, m := range s.cfg.AllowedModels[h] {
			if l := lane(h, m); l != h {
				if e, ok := limited[l]; ok {
					fmt.Fprintf(a.out, "  %s (%s %s)\n", m, l, e)
					continue
				}
			}
			fmt.Fprintf(a.out, "  %s\n", m)
		}
		for _, w := range s.cfg.AllowedWhenLimited {
			_, down := limited[w.Harness]
			for _, m := range w.Allow[h] {
				state := "not allowed now"
				if down {
					state = "allowed now"
				}
				fmt.Fprintf(a.out, "  %s (only while %s is limited: %s)\n", m, w.Harness, state)
			}
		}
	}
	return nil
}
