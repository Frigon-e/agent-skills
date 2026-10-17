package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// repoConfig comes from <repo>/.agents/orchestrate.toml, so anyone who can
// commit to the repo controls it. It only shapes briefs.
type repoConfig struct {
	BaseBranch   string   `toml:"base_branch"`
	LeadSkills   []string `toml:"lead_skills"`
	Check        string   `toml:"check"`
	QuietMinutes int      `toml:"quiet_minutes"`
	// Notes reach the orchestrator on every board, so loadConfig caps them.
	Notes []string `toml:"notes"`
	// Locks maps a lock name to when a lead must hold it; every brief says so.
	Locks map[string]string `toml:"locks"`
}

const maxNotes, maxNoteLen = 5, 160

// userConfig holds what a repository must not control: how leads launch
// and how many run on this machine. Each file decodes into its own type, so
// a repo key can't reach these fields under any spelling (the decoder
// matches field names case-insensitively).
type userConfig struct {
	MaxLeads   int      `toml:"max_leads"`
	ClaudeArgs []string `toml:"claude_args"`
	CodexArgs  []string `toml:"codex_args"`
	// LeadContextLimit is the context size, in tokens, past which a lead
	// handed unrelated work asks for a compact or a fresh session.
	LeadContextLimit int `toml:"lead_context_limit"`
	// Dispatch rules say which harness and model suit which work. orch only
	// prints them; the orchestrator matches them by judgment.
	Dispatch []dispatchRule `toml:"dispatch"`
}

type dispatchRule struct {
	When string    `toml:"when"`
	Use  []profile `toml:"use"`
	Why  string    `toml:"why"`
}

type profile struct {
	Harness string `toml:"harness"`
	Model   string `toml:"model"`
	Effort  string `toml:"effort"`
}

func (p profile) String() string {
	return strings.TrimSpace(p.Harness + " " + p.Model + " " + p.Effort)
}

type config struct {
	repoConfig
	userConfig
	repoFile string // empty when the repo has no orchestrate.toml
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
	root := runRoot(repo)
	if _, err := os.Stat(root); err == nil {
		// Existing state, possibly from before the marker: record whose it is.
		_ = claimRoot(root, repo)
	}
	return site{repo: repo, root: root, cfg: cfg}, nil
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
// under one directory per repo. The directory is named after the repo folder
// and records the repo path in a `.repo` file (see claimRoot); a different
// repo with the same folder name gets a path hash added instead of sharing
// the first one's state. It only reads, so a dry run still writes nothing.
func runRoot(repo string) string {
	name := filepath.Base(repo)
	plain := runRootNamed(name)
	if owner := rootOwner(plain); owner == "" || owner == repo {
		return plain
	}
	sum := sha256.Sum256([]byte(repo))
	return runRootNamed(name + "-" + hex.EncodeToString(sum[:4]))
}

func runRootNamed(name string) string {
	if home := os.Getenv("ORCH_HOME"); home != "" {
		return filepath.Join(expandHome(home), name)
	}
	if dir := herdrWorktreesDir(); dir != "" {
		return filepath.Join(dir, name, ".orchestrate")
	}
	return filepath.Join(expandHome("~/.local/state/orchestrate"), name)
}

// rootOwner is the repo path recorded in root, or "" when nobody claimed it.
func rootOwner(root string) string {
	b, _ := os.ReadFile(filepath.Join(root, ".repo"))
	return strings.TrimSpace(string(b))
}

// claimRoot records repo as the owner of root, creating root. Call it where
// state is first written. A root from before the marker belongs to the first
// repo that claims it. The marker is created exclusively, so of two repos that
// race for one name the loser is told and must re-resolve with runRoot.
func claimRoot(root, repo string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, ".repo"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if owner := rootOwner(root); owner != repo && owner != "" {
			return fail("%s already belongs to %s; run orch again", root, owner)
		}
		return nil
	}
	defer f.Close()
	_, err = f.WriteString(repo + "\n")
	return err
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

var userKeys = []string{"max_leads", "claude_args", "codex_args", "lead_context_limit", "dispatch"}

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

var repoKeys = []string{"base_branch", "lead_skills", "check", "quiet_minutes", "notes", "locks"}

// readRepoConfig decodes a repo file over the defaults already in rc.
func readRepoConfig(path string, rc *repoConfig) error {
	if err := decodeStrict(path, rc, userKeys, userConfigPath()); err != nil {
		return err
	}
	if len(rc.Notes) > maxNotes {
		return fail("%s: %d notes, at most %d: rewrite or drop one before adding another", path, len(rc.Notes), maxNotes)
	}
	for _, n := range rc.Notes {
		if strings.TrimSpace(n) == "" || strings.ContainsAny(n, "\r\n") || utf8.RuneCountInString(n) > maxNoteLen {
			return fail("%s: each note must be one non-empty line of at most %d characters: %q", path, maxNoteLen, short(n, 40))
		}
	}
	if len(rc.Locks) > maxNotes {
		return fail("%s: %d locks, at most %d", path, len(rc.Locks), maxNotes)
	}
	for name, when := range rc.Locks {
		if !lockNameRe.MatchString(name) {
			return fail("%s: lock name %q must be a short lowercase slug", path, name)
		}
		if strings.TrimSpace(when) == "" || strings.ContainsAny(when, "\r\n") || utf8.RuneCountInString(when) > maxNoteLen {
			return fail("%s: lock %s needs one line of at most %d characters saying when to hold it", path, name, maxNoteLen)
		}
	}
	return nil
}

// loadUserConfig reads the machine's settings over their defaults. Leads use
// it alone; they have no repo checkout to read the rest from.
func loadUserConfig() (userConfig, error) {
	uc := userConfig{MaxLeads: 3, LeadContextLimit: 150_000}
	err := decodeStrict(userConfigPath(), &uc, repoKeys, "<repo>/.agents/orchestrate.toml")
	return uc, err
}

func loadConfig(repo string) (config, error) {
	cfg := config{repoConfig: repoConfig{LeadSkills: []string{"route-work"}, QuietMinutes: 30}}
	path := filepath.Join(repo, ".agents", "orchestrate.toml")
	user := userConfigPath()
	if err := readRepoConfig(path, &cfg.repoConfig); err != nil {
		return cfg, err
	}
	if _, err := os.Stat(path); err == nil {
		cfg.repoFile = path
	}
	uc, err := loadUserConfig()
	if err != nil {
		return cfg, err
	}
	cfg.userConfig = uc
	for i, r := range cfg.Dispatch {
		if strings.TrimSpace(r.When) == "" || len(r.Use) == 0 {
			return cfg, fail("%s: dispatch rule %d needs `when` and at least one `use` profile", user, i+1)
		}
		for _, p := range r.Use {
			if (p.Harness != "claude" && p.Harness != "codex") || p.Model == "" {
				return cfg, fail("%s: dispatch rule %d: each profile needs harness claude or codex and a model", user, i+1)
			}
		}
	}
	if cfg.MaxLeads < 1 || cfg.QuietMinutes < 1 || cfg.LeadContextLimit < 1 {
		return cfg, fail("max_leads and lead_context_limit (%s) and quiet_minutes (%s) must be at least 1", user, path)
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
