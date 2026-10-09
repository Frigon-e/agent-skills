package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// userConfigPath holds the settings a repository must not control: how
// leads launch and how many run on this machine.
func userConfigPath() string {
	if p := os.Getenv("ORCH_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(envOr("XDG_CONFIG_HOME", expandHome("~/.config")), "orchestrate", "config.toml")
}

// userKeys are only read from the user's config. A repo file that sets one
// is refused, so a commit can't add launch flags such as one that skips
// permission prompts.
var userKeys = []string{"max_leads", "claude_args", "codex_args"}

func decodeConfig(path string, cfg *config, allowed func(key string) bool, hint string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	md, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return fail("%s: %v", path, err)
	}
	for _, k := range md.Keys() {
		if !allowed(k.String()) {
			return fail("%s: %q is not allowed here%s", path, k.String(), hint)
		}
	}
	if und := md.Undecoded(); len(und) > 0 {
		return fail("%s: unknown key %q", path, und[0].String())
	}
	return nil
}

func loadConfig(repo string) (config, error) {
	cfg := config{MaxLeads: 3, LeadSkills: []string{"route-work"}, QuietMinutes: 30}
	path := filepath.Join(repo, ".agents", "orchestrate.toml")
	user := userConfigPath()
	isUserKey := func(k string) bool { return slices.Contains(userKeys, k) }
	if err := decodeConfig(path, &cfg, func(k string) bool { return !isUserKey(k) },
		fmt.Sprintf("; %s belong in %s", strings.Join(userKeys, ", "), user)); err != nil {
		return cfg, err
	}
	if err := decodeConfig(user, &cfg, isUserKey,
		fmt.Sprintf("; only %s go here, the rest in <repo>/.agents/orchestrate.toml", strings.Join(userKeys, ", "))); err != nil {
		return cfg, err
	}
	if cfg.MaxLeads < 1 || cfg.QuietMinutes < 1 {
		return cfg, fail("max_leads (%s) and quiet_minutes (%s) must be at least 1", user, path)
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
