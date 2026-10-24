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
)

// Base is the base branch of a Run in the user's repository.
type Base struct {
	Repo   string // root of the user's repository
	Branch string // e.g. main
	Commit string
	Config config.Config // .afk.toml at Commit, never the working tree (ADR-0002)
	Ident  Ident         // the user's git identity: author of the squashed result
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
	if refErr != nil || !strings.HasPrefix(ref, originPrefix) {
		ref = originPrefix + defaultBranch
	}
	base.Branch = strings.TrimPrefix(ref, originPrefix)
	if base.Commit, err = git(ctx, "-C", base.Repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		err = fmt.Errorf("resolving the base branch: %w", err)
		return
	}
	if base.Ident, err = readIdent(ctx, base.Repo); err != nil {
		return
	}
	data, err := git(ctx, "-C", base.Repo, "cat-file", "blob", base.Commit+":"+config.FileName)
	if err != nil {
		err = fmt.Errorf("reading %s from origin/%s: %w", config.FileName, base.Branch, err)
		return
	}
	if base.Config, err = config.Parse([]byte(data)); err != nil {
		err = fmt.Errorf("%s at origin/%s: %w", config.FileName, base.Branch, err)
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
