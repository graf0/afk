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
