package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const ctxBudgetHint = "Prefer one `orch ctx batch` over several greps; read what it returns before searching again."

const gatePacketContract = ` For every unresolved decisive question, include packet from your own review: goal, known_facts (checked state and constraints), evidence_ids, unknown (the exact missing fact or decision), options [{id,text,gain,cost}], recommendation (an option id), rationale, authority (the spec's actual authority basis), and next_action. Do not invent project vocabulary, trade-offs, facts or delegated authority. Leave unavailable fields empty; incomplete packets are research work, not ready owner questions. No formatting pass will fill them.`
const adversaryEvidenceScope = " Follow bounded relevant incoming AND outgoing references (at most two hops and eight additional files), registrations/configuration, and tests. Stop at the existing turn budget, not at forward imports. Include coverage_gaps (an array of strings) alongside findings in your JSON. Name failed/unrun checks explicitly; an empty findings list does not establish complete exploration."

// Keep the original reviewer's bounded coverage report, including a missing
// report. Notes are observations, not evidence of complete repository coverage.
func recordAdversaryCoverage(name string, apply func(string, *gateRecord, string, *spec) error) func(string, *gateRecord, string, *spec) error {
	return func(raw string, rec *gateRecord, repo string, sp *spec) error {
		var reply struct {
			Gaps *[]string `json:"coverage_gaps"`
		}
		if err := extractJSON(raw, &reply); err != nil || reply.Gaps == nil {
			rec.Notes = append(rec.Notes, name+" coverage: not reported; bounded incoming/outgoing references, registrations/configuration and tests remain unverified")
		} else if len(*reply.Gaps) == 0 {
			rec.Notes = append(rec.Notes, name+" coverage: reviewer reported no further gaps within the exploration bound; this does not establish repository-wide coverage")
		} else {
			for _, gap := range *reply.Gaps {
				rec.Notes = append(rec.Notes, name+" reported coverage gap: "+gap)
			}
		}
		return apply(raw, rec, repo, sp)
	}
}

func gateEvidenceContract(p gatePass) gatePass {
	if p.noTools {
		return p
	}
	old := p.task
	if p.class == "adversary" {
		p.task = strings.ReplaceAll(p.task, ". Open other files only when one of these imports or calls them directly. Stop once you have checked these; an empty findings list is a valid answer.", ".")
		if !strings.Contains(p.task, adversaryEvidenceScope) {
			p.task = strings.ReplaceAll(p.task, `{"findings":`, `{"coverage_gaps":[],"findings":`)
			p.task += adversaryEvidenceScope
			if p.apply != nil {
				p.apply = recordAdversaryCoverage(p.name, p.apply)
			}
		}
	} else if p.apply != nil && strings.Contains(p.task, `"questions"`) && !strings.Contains(p.task, gatePacketContract) {
		p.task = strings.ReplaceAll(p.task, `"changes_result":false}`, `"changes_result":false,"packet":{"goal":"","known_facts":[],"evidence_ids":[],"unknown":"","options":[{"id":"a","text":"","gain":"","cost":""}],"recommendation":"","rationale":"","authority":"","next_action":""}}`)
		p.task += gatePacketContract
	}
	if old != p.task {
		p.prompt = p.shared + p.task
	}
	return p
}

func gateAgentPass(repo, harness string, p gatePass) (gatePass, error) {
	p = gateEvidenceContract(p)
	if !p.ctxTool {
		return p, nil
	}
	cli, err := CtxCLIFor(harness, repo)
	if err != nil {
		return p, err
	}
	if p.ctxOnly && harness == "pi" {
		cli.Notice += "\n" + ctxOnlyNotice
	}
	if p.ctxProfile != "" {
		cli.Env = append(cli.Env, "ORCH_CTX_PROFILE="+p.ctxProfile)
	}
	p.cli = cli
	if !strings.HasPrefix(p.shared, cli.Notice+"\n\n") {
		p.shared = cli.Notice + "\n\n" + p.shared
		p.prompt = cli.Notice + "\n\n" + p.prompt
	}
	if !p.noTools && !strings.Contains(p.task, ctxBudgetHint) {
		p.task += "\n" + ctxBudgetHint
		p.prompt += "\n" + ctxBudgetHint
	}
	return p, nil
}

func passContext(p gatePass) context.Context {
	if p.ctx != nil {
		return p.ctx
	}
	return context.Background()
}

func agentEnv(base, extra []string) []string {
	if base == nil {
		base = os.Environ()
	}
	env := append([]string{}, base...)
	for _, value := range extra {
		key, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		kept := env[:0]
		for _, entry := range env {
			if !strings.HasPrefix(entry, key+"=") {
				kept = append(kept, entry)
			}
		}
		env = append(kept, value)
	}
	return env
}

func piPassTools(p gatePass) []string {
	if p.noTools {
		return []string{"--no-tools"}
	}
	if p.ctxOnly && p.cli.Extension != "" {
		// Only the read tool and a shell that the extension limits to orch ctx.
		return []string{"--tools", strings.Join(append([]string{"read"}, p.cli.Tools...), ","), "-e", p.cli.Extension}
	}
	tools := append([]string{"read", "grep", "find", "ls"}, p.cli.Tools...)
	return []string{"--tools", strings.Join(tools, ",")}
}

func claudePassArgs(choice profile, p gatePass) []string {
	args := claudeArgs(choice, p.noTools)
	if !p.noTools && len(p.cli.Tools) > 0 {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--tools" {
				args[i+1] += ",Bash"
			}
			if args[i] == "--allowedTools" {
				args[i+1] += "," + strings.Join(p.cli.Tools, ",")
			}
		}
	}
	if !p.contextFiles {
		// Exclude repository memories without --bare, which disables subscription auth.
		args = append(args, "--settings", `{"claudeMdExcludes":["**"]}`)
	}
	return args
}

// paneAgentEnv sets env in the pane's shell. The pane already has the user's
// PATH, so a PATH built as "<dirs>:<orch's PATH>" only prepends <dirs> to the
// pane's own: the machine PATH is never typed into the pane.
func paneAgentEnv(pane string, env []string) error {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return fmt.Errorf("invalid agent environment entry")
		}
		// Env names come from CtxCLIFor; values remain literal shell strings.
		line := "export " + key + "=" + shellQuote(value)
		if prefix, ok := strings.CutSuffix(value, string(os.PathListSeparator)+os.Getenv("PATH")); key == "PATH" && ok {
			line = "export PATH=" + shellQuote(prefix+string(os.PathListSeparator)) + `"$PATH"`
		}
		if _, err := herdrCmd(herdrTimeout, "pane", "run", pane, line); err != nil {
			return err
		}
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func treeWarning(rec *gateRecord) string {
	if len(rec.TreeChanged) > 0 {
		return "UNVERIFIED: checkout changed: " + strings.Join(rec.TreeChanged, ", ") + "; in flight: " + strings.Join(rec.TreeChangedPasses, ", ") + ". Findings retained for inspection; approval withheld. Files were NOT reverted."
	}
	if rec.TreeCheckError != "" {
		return "UNVERIFIED: checkout could not be checked: " + rec.TreeCheckError + ". Findings retained for inspection; approval withheld."
	}
	return ""
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
