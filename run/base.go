package run

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/egress"
)

const (
	originPrefix = "refs/remotes/origin/"
	originHead   = originPrefix + "HEAD"

	// defaultBranch is the base branch when origin/HEAD is not set.
	defaultBranch = "main"

	// settingsPath is the Project's Claude Code settings in the repository.
	settingsPath = ".claude/settings.json"
)

// Base is the base branch of a Run in the user's repository.
type Base struct {
	Repo   string // root of the user's repository
	Branch string // e.g. main
	Commit string
	Config config.Config // .afk.toml at Commit, never the working tree (ADR-0002)
	// Settings is .claude/settings.json at Commit (nil: none).
	Settings []byte
	// Plugins are the effective plugin ids at Commit: [agent].plugins and the
	// enabled ones of .claude/settings.json (ADR-0009).
	Plugins []string
	Ident   Ident // the user's git identity: author of the squashed result
}

// ReadBase fetches origin in the repository containing dir and reads the
// Project config from the tip of origin's default branch (origin/HEAD, else
// origin/main).
func ReadBase(ctx context.Context, dir string) (base Base, err error) {
	if base.Repo, err = git(ctx, "-C", dir, "rev-parse", "--show-toplevel"); err != nil {
		return
	}
	if _, err = git(ctx, "-C", base.Repo, "fetch", "--quiet", "origin"); err != nil {
		err = fmt.Errorf("fetching the base branch: %w", err)
		return
	}
	ref, refErr := git(ctx, "-C", base.Repo, "symbolic-ref", "--quiet", originHead)
	fellBack := refErr != nil || !strings.HasPrefix(ref, originPrefix)
	if fellBack {
		ref = originPrefix + defaultBranch
	}
	base.Branch = strings.TrimPrefix(ref, originPrefix)
	if base.Commit, err = git(ctx, "-C", base.Repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		if fellBack {
			err = fmt.Errorf("origin/HEAD is not set and origin/%s does not exist: run `git remote set-head origin --auto`: %w", defaultBranch, err)
			return
		}
		err = fmt.Errorf("resolving the base branch origin/%s: %w", base.Branch, err)
		return
	}
	if base.Ident, err = readIdent(ctx, base.Repo); err != nil {
		return
	}
	data, err := git(ctx, "-C", base.Repo, "cat-file", "blob", base.Commit+":"+config.FileName)
	if err != nil {
		err = fmt.Errorf("reading %s from origin/%s (commit and push it): %w", config.FileName, base.Branch, err)
		return
	}
	if base.Config, err = config.ParseNamed(fmt.Sprintf("%s at origin/%s", config.FileName, base.Branch), []byte(data)); err != nil {
		return
	}
	// A missing settings.json is no plugins.
	if listed, listErr := git(ctx, "-C", base.Repo, "ls-tree", "--name-only", base.Commit, "--", settingsPath); listErr == nil && listed != "" {
		var settings string
		if settings, err = git(ctx, "-C", base.Repo, "cat-file", "blob", base.Commit+":"+settingsPath); err != nil {
			err = fmt.Errorf("reading %s from origin/%s: %w", settingsPath, base.Branch, err)
			return
		}
		base.Settings = []byte(settings)
	}
	base.Plugins, _, err = config.EffectivePlugins(base.Config.Agent, base.Settings)
	if err != nil {
		err = fmt.Errorf("plugins at origin/%s: %w", base.Branch, err)
	}
	return
}

// Ident is a git identity.
type Ident struct {
	Name  string
	Email string
}

// readIdent reads the author identity git would use in repo.
func readIdent(ctx context.Context, repo string) (ident Ident, err error) {
	out, err := git(ctx, "-C", repo, "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		err = fmt.Errorf("reading your git identity (set user.name and user.email): %w", err)
		return
	}
	// "Name <email> 1700000000 +0200"
	name, rest, ok := strings.Cut(out, " <")
	email, _, ok2 := strings.Cut(rest, ">")
	if !ok || !ok2 || name == "" || email == "" {
		err = fmt.Errorf("unexpected git identity %q", out)
		return
	}
	ident = Ident{Name: name, Email: email}
	return
}

// EgressPolicy is the egress policy of a Sandbox VM of the Project: its
// [network].allow plus the domains the Agent itself needs.
func EgressPolicy(cfg config.Config) (policy egress.Policy, err error) {
	if policy, err = egress.Allowlist(slices.Concat(cfg.Network.Allow, agent.Domains)); err != nil {
		err = fmt.Errorf("network.allow: %w", err)
	}
	return
}
