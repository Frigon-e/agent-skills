package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// repoConfig comes from <repo>/.agents/orchestrate.toml, so anyone who can
// commit to the repo controls it. It only shapes briefs.
type repoConfig struct {
	BaseBranch   string   `toml:"base_branch"`
	LeadSkills   []string `toml:"lead_skills"`
	Check        string   `toml:"check"`
	QuietMinutes int      `toml:"quiet_minutes"`
}

// userConfig holds what a repository must not control: how leads launch
// and how many run on this machine. Each file decodes into its own type, so
// a repo key can't reach these fields under any spelling (the decoder
// matches field names case-insensitively).
type userConfig struct {
	MaxLeads   int      `toml:"max_leads"`
	ClaudeArgs []string `toml:"claude_args"`
	CodexArgs  []string `toml:"codex_args"`
}

type config struct {
	repoConfig
	userConfig
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

func userConfigPath() string {
	if p := os.Getenv("ORCH_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(envOr("XDG_CONFIG_HOME", expandHome("~/.config")), "orchestrate", "config.toml")
}

var userKeys = []string{"max_leads", "claude_args", "codex_args"}

// decodeStrict refuses keys v has no field for. A key that belongs in the
// other file gets a pointer there instead of "unknown key".
func decodeStrict(path string, v any, otherKeys []string, otherFile string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	md, err := toml.DecodeFile(path, v)
	if err != nil {
		return fail("%s: %v", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		k := und[0].String()
		if slices.ContainsFunc(otherKeys, func(o string) bool { return strings.EqualFold(o, k) }) {
			return fail("%s: %q is not allowed here; it belongs in %s", path, k, otherFile)
		}
		return fail("%s: unknown key %q", path, k)
	}
	return nil
}

func loadConfig(repo string) (config, error) {
	cfg := config{
		repoConfig{LeadSkills: []string{"route-work"}, QuietMinutes: 30},
		userConfig{MaxLeads: 3},
	}
	path := filepath.Join(repo, ".agents", "orchestrate.toml")
	user := userConfigPath()
	repoKeys := []string{"base_branch", "lead_skills", "check", "quiet_minutes"}
	if err := decodeStrict(path, &cfg.repoConfig, userKeys, user); err != nil {
		return cfg, err
	}
	if err := decodeStrict(user, &cfg.userConfig, repoKeys, "<repo>/.agents/orchestrate.toml"); err != nil {
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
