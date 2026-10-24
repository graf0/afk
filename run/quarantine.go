package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// quarantineConfig hardens every git call on the Quarantine repo, which holds
// objects from the untrusted guest: no hooks, objects checked on fetch.
var quarantineConfig = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "transfer.fsckObjects=true",
}

// Quarantine is the host's bare repository for a Project's Run results
// (GLOSSARY.md). Nothing is ever checked out or run in it.
type Quarantine struct {
	Dir string
}

// OpenQuarantine opens the Quarantine repo at dir, creating it if needed.
func OpenQuarantine(ctx context.Context, dir string) (q Quarantine, err error) {
	q = Quarantine{Dir: dir}
	if _, statErr := os.Stat(filepath.Join(dir, "HEAD")); statErr == nil {
		return
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", filepath.Dir(dir), err)
		return
	}
	if _, err = git(ctx, "init", "--quiet", "--bare", dir); err != nil {
		err = fmt.Errorf("creating the Quarantine repo: %w", err)
		return
	}
	// Sample hooks never run, but nothing hook-like belongs here.
	if err = os.RemoveAll(filepath.Join(dir, "hooks")); err != nil {
		err = fmt.Errorf("removing hooks of the Quarantine repo: %w", err)
	}
	return
}

func (q Quarantine) git(ctx context.Context, args ...string) (out string, err error) {
	full := append([]string{"--git-dir=" + q.Dir}, quarantineConfig...)
	out, err = git(ctx, append(full, args...)...)
	return
}

// --- base ---

// FetchBase copies commit of the user's repository repo to ref.
func (q Quarantine) FetchBase(ctx context.Context, repo, commit, ref string) (err error) {
	if _, err = q.git(ctx, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", repo, "+"+commit+":"+ref); err != nil {
		err = fmt.Errorf("copying the base to the Quarantine repo: %w", err)
	}
	return
}

// Bundle writes a git bundle of ref (full history) to path.
func (q Quarantine) Bundle(ctx context.Context, ref, path string) (err error) {
	if _, err = q.git(ctx, "bundle", "create", "--quiet", path, ref); err != nil {
		err = fmt.Errorf("bundling the base: %w", err)
	}
	return
}

// --- result ---

// Import fetches HEAD of the bundle at path (from the guest) to ref and
// returns its commit.
func (q Quarantine) Import(ctx context.Context, path, ref string) (head string, err error) {
	if _, err = q.git(ctx, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", path, "+HEAD:"+ref); err != nil {
		err = fmt.Errorf("importing the result: %w", err)
		return
	}
	if head, err = q.git(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		err = fmt.Errorf("importing the result: %w", err)
	}
	return
}

// Validate checks the result head against base: base is an ancestor of
// head, there are commits, and no commit in base..head touches a protected
// path. It returns the number of commits.
func (q Quarantine) Validate(ctx context.Context, base, head string, protected []string) (commits int, err error) {
	if _, ancestorErr := q.git(ctx, "merge-base", "--is-ancestor", base, head); ancestorErr != nil {
		err = fmt.Errorf("the result does not contain the base %s", base)
		return
	}
	count, err := q.git(ctx, "rev-list", "--count", base+".."+head)
	if err != nil {
		return
	}
	if commits, err = strconv.Atoi(count); err != nil {
		err = fmt.Errorf("counting commits: %w", err)
		return
	}
	if commits == 0 {
		err = errors.New("the result has no commits")
		return
	}
	// Every commit against every parent (-m): a path changed and reverted
	// still counts. No renames: moving a file out of or into a protected path
	// changes both paths. Mode and type changes (symlinks) are changes too.
	out, err := q.git(ctx, "log", "-m", "--no-renames", "--no-ext-diff", "--no-textconv", "--name-only", "--format=", "-z", base+".."+head)
	if err != nil {
		return
	}
	if hits := protectedChanges(strings.Split(out, "\x00"), protected); len(hits) > 0 {
		err = fmt.Errorf("the result changes protected paths: %s", strings.Join(hits, ", "))
	}
	return
}

// Accept points ref at the validated head.
func (q Quarantine) Accept(ctx context.Context, ref, head string) (err error) {
	_, err = q.git(ctx, "update-ref", ref, head)
	return
}

// protectedChanges returns the changed paths that touch a protected entry:
// the entry itself, anything under it, or one of its parent directories
// (replaced by a file or a symlink). Entries may end with "/". Matching
// ignores case, as checkouts on case-insensitive file systems do.
func protectedChanges(changed, protected []string) (hits []string) {
	seen := map[string]bool{}
	for _, path := range changed {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		p := strings.ToLower(path)
		for _, entry := range protected {
			e := strings.ToLower(strings.TrimSuffix(entry, "/"))
			if e == "" {
				continue
			}
			if p == e || strings.HasPrefix(p, e+"/") || strings.HasPrefix(e, p+"/") {
				hits = append(hits, path)
				break
			}
		}
	}
	return
}
