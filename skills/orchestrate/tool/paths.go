package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type config struct {
	BaseBranch   string   `toml:"base_branch"`
	MaxLeads     int      `toml:"max_leads"`
	LeadSkills   []string `toml:"lead_skills"`
	Check        string   `toml:"check"`
	QuietMinutes int      `toml:"quiet_minutes"`
	ClaudeArgs   []string `toml:"claude_args"`
	CodexArgs    []string `toml:"codex_args"`
}

// site is the repo an orchestrator command runs against.
type site struct {
	repo string
	root string
	cfg  config
}

func (a *app) site() (site, error) {
	repo, err := repoRoot(a.cwd)
	if err != nil {
		return site{}, err
	}
	cfg, err := loadConfig(repo)
	if err != nil {
		return site{}, err
	}
	return site{repo: repo, root: runRoot(repo), cfg: cfg}, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

// repoRoot returns the main checkout of the repository containing dir, also
// when dir is inside a linked worktree.
func repoRoot(dir string) (string, error) {
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return "", fail("not inside a git repository: %s", dir)
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	if top, err := git(dir, "rev-parse", "--show-toplevel"); err == nil && top != "" {
		return top, nil
	}
	return common, nil
}

func herdrWorktreesDir() string {
	var cfg struct {
		Worktrees struct {
			Directory string `toml:"directory"`
		} `toml:"worktrees"`
	}
	path := expandHome(envOr("ORCH_HERDR_CONFIG", "~/.config/herdr/config.toml"))
	if _, err := toml.DecodeFile(path, &cfg); err != nil || cfg.Worktrees.Directory == "" {
		return ""
	}
	return expandHome(cfg.Worktrees.Directory)
}

// runRoot keeps a repo's run state next to its Herdr worktrees, so both live
// under one directory per repo.
func runRoot(repo string) string {
	name := filepath.Base(repo)
	if home := os.Getenv("ORCH_HOME"); home != "" {
		return filepath.Join(expandHome(home), name)
	}
	if dir := herdrWorktreesDir(); dir != "" {
		return filepath.Join(dir, name, ".orchestrate")
	}
	return filepath.Join(expandHome("~/.local/state/orchestrate"), name)
}

func rolesDir() string {
	return expandHome(envOr("ORCH_ROLES_DIR", "~/.local/state/orchestrate/roles"))
}

func loadConfig(repo string) (config, error) {
	cfg := config{MaxLeads: 3, LeadSkills: []string{"route-work"}, QuietMinutes: 30}
	path := filepath.Join(repo, ".agents", "orchestrate.toml")
	if _, err := os.Stat(path); err == nil {
		md, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return cfg, fail("%s: %v", path, err)
		}
		if und := md.Undecoded(); len(und) > 0 {
			return cfg, fail("%s: unknown key %q", path, und[0].String())
		}
	}
	if cfg.MaxLeads < 1 || cfg.QuietMinutes < 1 {
		return cfg, fail("%s: max_leads and quiet_minutes must be at least 1", path)
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "HEAD"
		if b, err := git(repo, "symbolic-ref", "--short", "HEAD"); err == nil && b != "" {
			cfg.BaseBranch = b
		}
	}
	return cfg, nil
}

// selfPath is how agents should invoke orch: the wrapper, not the cached binary.
func selfPath() string {
	if p := os.Getenv("ORCH_SELF"); p != "" {
		return p
	}
	p, _ := os.Executable()
	return p
}

func skillDir() (string, error) {
	if d := os.Getenv("ORCH_SKILL_DIR"); d != "" {
		return d, nil
	}
	return "", fail("ORCH_SKILL_DIR is unset; run orch through the skill's scripts/orch")
}
