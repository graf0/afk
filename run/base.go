package run

import (
	"context"
	"fmt"
	"strings"

	"github.com/graf0/afk/config"
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
	return readBase(ctx, dir, true)
}

// ReadBaseLocal is ReadBase without the fetch: the origin refs as they are.
// For the commands that start nothing (the Project's name is all they need).
func ReadBaseLocal(ctx context.Context, dir string) (base Base, err error) {
	return readBase(ctx, dir, false)
}

func readBase(ctx context.Context, dir string, fetch bool) (base Base, err error) {
	if base.Repo, err = git(ctx, "-C", dir, "rev-parse", "--show-toplevel"); err != nil {
		return
	}
	if fetch {
		if _, err = git(ctx, "-C", base.Repo, "fetch", "--quiet", "origin"); err != nil {
			err = fmt.Errorf("fetching the base branch: %w", err)
			return
		}
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
	if base.Settings, err = readSettings(ctx, base.Repo, base.Commit); err != nil {
		err = fmt.Errorf("%w (origin/%s)", err, base.Branch)
		return
	}
	base.Plugins, _, err = config.EffectivePlugins(base.Config.Agent, base.Settings)
	if err != nil {
		err = fmt.Errorf("plugins at origin/%s: %w", base.Branch, err)
	}
	return
}

// readSettings is .claude/settings.json at commit in repo; a missing file is
// no settings (nil).
func readSettings(ctx context.Context, repo, commit string) (settings []byte, err error) {
	listed, listErr := git(ctx, "-C", repo, "ls-tree", "--name-only", commit, "--", settingsPath)
	if listErr != nil || listed == "" {
		return
	}
	data, err := git(ctx, "-C", repo, "cat-file", "blob", commit+":"+settingsPath)
	if err != nil {
		err = fmt.Errorf("reading %s: %w", settingsPath, err)
		return
	}
	settings = []byte(data)
	return
}

// pinned is b with the Settings and Plugins as of b.Commit, read in repo,
// which must have that commit: a resumed Run takes everything of its Sandbox
// from the one base commit its clone was made from, not from the base branch
// as it is today.
func (b Base) pinned(ctx context.Context, repo string) (pinned Base, err error) {
	pinned = b
	data, err := git(ctx, "-C", repo, "cat-file", "blob", b.Commit+":"+config.FileName)
	if err != nil {
		err = fmt.Errorf("reading %s at %s: %w", config.FileName, b.Commit, err)
		return
	}
	cfg, err := config.ParseNamed(fmt.Sprintf("%s at %s", config.FileName, b.Commit), []byte(data))
	if err != nil {
		return
	}
	if pinned.Settings, err = readSettings(ctx, repo, b.Commit); err != nil {
		return
	}
	pinned.Plugins, _, err = config.EffectivePlugins(cfg.Agent, pinned.Settings)
	if err != nil {
		err = fmt.Errorf("plugins at %s: %w", b.Commit, err)
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
