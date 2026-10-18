package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// `orch spawn <id> --auto` replaces the orchestrator's choice of harness,
// model and effort with a lookup: triage rates the finished brief, the first
// dispatch rule with that tier supplies the profiles, and the first profile
// that is healthy, allowed and not a degraded stand-in wins. Without --apply
// it only says what it would do, until the triage report earns more trust.
var tierEffort = map[string]string{"settled": "low", "standard": "high", "planning": "high"}

type autoChoice struct {
	tier, reason, rule string
	profile            *profile
	effort             string
	skipped            []string
	parkedWhy          string
	degradedOption     *profile
}

// autoPick resolves a task made with --harness auto. The task keeps a hand
// chosen profile otherwise: auto applies only when it was asked for.
func (a *app) autoPick(s site, id string, allowDegraded bool) (*autoChoice, *triageRecord, error) {
	dir, err := taskDir(s.root, id)
	if err != nil {
		return nil, nil, err
	}
	fm, _, err := readTask(filepath.Join(dir, "task.md"))
	if err != nil {
		return nil, nil, fail("no task %s", id)
	}
	if fm["harness"] != "auto" {
		return nil, nil, fail("%s has a hand-chosen profile (%s %s). --auto applies only to tasks made with --harness auto --model auto --effort auto", id, fm["harness"], fm["model"])
	}
	if s.cfg.TriageMode == "off" {
		return nil, nil, fail("--auto needs triage, and triage_mode is off in %s", userConfigPath())
	}
	rec, err := a.triageTask(s, id, false)
	if err != nil {
		return nil, nil, fail("cannot triage %s: %v", id, err)
	}
	var rule *dispatchRule
	for i := range s.cfg.Dispatch {
		if s.cfg.Dispatch[i].Tier == rec.Tier {
			rule = &s.cfg.Dispatch[i]
			break
		}
	}
	if rule == nil {
		return nil, nil, fail("triage rated %s as %s, but no dispatch rule has tier = %q (add it in %s)", id, rec.Tier, rec.Tier, userConfigPath())
	}
	c := a.chooseProfile(s, rec.Tier, rec.Reason, rule, fm["private"] == "true", allowDegraded)
	return c, rec, nil
}

// chooseProfile walks one dispatch rule and takes the first profile that is
// healthy, allowed and not a degraded stand-in the user has not approved.
func (a *app) chooseProfile(s site, tier, reason string, rule *dispatchRule, private, allowDegraded bool) *autoChoice {
	c := &autoChoice{tier: tier, reason: reason, rule: rule.When}
	limited := loadHealth(a.now())
	for i := range rule.Use {
		p := &rule.Use[i]
		switch {
		case private && p.Harness != "claude":
			c.skipped = append(c.skipped, p.String()+": the task is private, so Claude only")
		case limited[lane(p.Harness, p.Model)].State == "limited":
			c.skipped = append(c.skipped, p.String()+": "+lane(p.Harness, p.Model)+" is limited")
		case s.cfg.modelAllowed(p.Harness, p.Model, a.now()) != nil:
			c.skipped = append(c.skipped, p.String()+": not allowed right now ("+p.Model+" is an emergency stand-in)")
		case p.Degraded && !allowDegraded:
			c.skipped = append(c.skipped, p.String()+": degraded fallback, needs the user's approval (--degraded)")
			if c.degradedOption == nil {
				c.degradedOption = p
			}
		case c.profile == nil:
			c.profile = p
		}
	}
	if c.profile == nil {
		c.parkedWhy = "no usable profile in the rule"
		return c
	}
	c.effort = c.profile.Effort
	if c.effort == "" {
		c.effort = tierEffort[c.tier]
	}
	return c
}

func (a *app) spawnAuto(id string, apply, allowDegraded bool) error {
	s, err := a.site()
	if err != nil {
		return err
	}
	c, rec, err := a.autoPick(s, id, allowDegraded)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "auto: %s is %s work (%s)\n  rule: %s\n", id, c.tier, rec.Reason, c.rule)
	for _, sk := range c.skipped {
		fmt.Fprintf(a.out, "  skipped %s\n", sk)
	}
	if c.profile == nil {
		fmt.Fprintf(a.out, "  parked: %s\n", c.parkedWhy)
		if c.degradedOption != nil {
			fmt.Fprintf(a.out, "  degraded option: %s; ask the user, then rerun with --degraded\n", c.degradedOption)
		}
		return &exitError{code: 1}
	}
	pick := fmt.Sprintf("%s %s (effort %s)", c.profile.Harness, c.profile.Model, c.effort)
	if !apply {
		fmt.Fprintf(a.out, "  would use: %s\ndry run: nothing changed. Add --apply to set the profile and spawn\n", pick)
		return nil
	}
	dir, _ := taskDir(s.root, id)
	path := filepath.Join(dir, "task.md")
	for k, v := range map[string]string{"harness": c.profile.Harness, "model": c.profile.Model, "effort": c.effort} {
		if err := setFrontMatter(path, k, v); err != nil {
			return err
		}
	}
	if c.profile.Degraded {
		if err := setFrontMatter(path, "degraded", "true"); err != nil {
			return err
		}
	}
	_ = a.appendStatus(dir, "note", fmt.Sprintf("auto: %s -> %s (%s)", c.tier, strings.TrimSpace(pick), rec.Reason), "", "orch")
	// The triage record was written while the task still said "auto"; record
	// what it really runs on so the report compares like with like.
	moved := *rec
	moved.At, moved.Actual = a.now().Unix(), c.profile.Harness+" "+c.profile.Model
	_ = appendTriage(s.root, moved)
	fmt.Fprintf(a.out, "  using: %s\n", pick)
	return a.spawn(id, "")
}
