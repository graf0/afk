package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/forge"
)

// Refs of a Run in the Quarantine repo, per ticket branch.
const (
	baseRefPrefix     = "refs/afk/base/"     // the base of the Run
	incomingRefPrefix = "refs/afk/incoming/" // raw result from the guest
	resultRefPrefix   = "refs/heads/"        // validated result
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
	out, err = q.gitWith(ctx, nil, "", args...)
	return
}

func (q Quarantine) gitWith(ctx context.Context, env []string, stdin string, args ...string) (out string, err error) {
	full := append([]string{"--git-dir=" + q.Dir}, quarantineConfig...)
	out, err = gitWith(ctx, env, stdin, append(full, args...)...)
	return
}

// --- base ---

// CheckNew fails when branch already has a result: continuing a Run comes later.
func (q Quarantine) CheckNew(ctx context.Context, branch string) (err error) {
	if _, refErr := q.git(ctx, "rev-parse", "--quiet", "--verify", resultRef(branch)); refErr == nil {
		err = fmt.Errorf("%s already has a result in %s; remove it first: git --git-dir=%s update-ref -d %s",
			branch, q.Dir, q.Dir, resultRef(branch))
	}
	return
}

// FetchBase copies commit of the user's repository repo to the base ref of branch.
func (q Quarantine) FetchBase(ctx context.Context, repo, commit, branch string) (err error) {
	if _, err = q.git(ctx, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", repo, "+"+commit+":"+baseRef(branch)); err != nil {
		err = fmt.Errorf("copying the base to the Quarantine repo: %w", err)
	}
	return
}

// Base returns the base of branch: what FetchBase last copied there, the
// start of an earlier Run's guest.
func (q Quarantine) Base(ctx context.Context, branch string) (commit string, err error) {
	if commit, err = q.git(ctx, "rev-parse", "--verify", "--end-of-options", baseRef(branch)+"^{commit}"); err != nil {
		err = fmt.Errorf("reading the base of %s in %s (was its Run started here?): %w", branch, q.Dir, err)
	}
	return
}

// Drop deletes every ref of branch in the Quarantine repo: the base, the
// raw and the validated result. Missing ones are skipped.
func (q Quarantine) Drop(ctx context.Context, branch string) (err error) {
	for _, ref := range []string{baseRef(branch), incomingRef(branch), resultRef(branch)} {
		if _, err = q.git(ctx, "update-ref", "-d", ref); err != nil {
			err = fmt.Errorf("deleting %s: %w", ref, err)
			return
		}
	}
	return
}

// DeleteRemote deletes branch from remote, but only while it is still at
// tip, the commit the caller read: a branch that got commits since stays.
// The caller has checked that no pull request needs it.
func (q Quarantine) DeleteRemote(ctx context.Context, remote forge.Remote, branch, tip string) (err error) {
	ref := resultRef(branch)
	if _, err = q.gitWith(ctx, remote.Env, "", "push", "--quiet", "--no-verify", "--force-with-lease="+ref+":"+tip, "--delete", "--end-of-options", remote.URL, ref); err != nil {
		err = fmt.Errorf("deleting %s from %s: %w", branch, remote.URL, err)
	}
	return
}

// Bundle writes a git bundle of the base ref of branch and, when there is
// one, of its result (the Spec's branch so far), with full history, to path.
func (q Quarantine) Bundle(ctx context.Context, branch, path string) (err error) {
	refs := []string{baseRef(branch)}
	if _, refErr := q.git(ctx, "rev-parse", "--quiet", "--verify", resultRef(branch)); refErr == nil {
		refs = append(refs, resultRef(branch))
	}
	if _, err = q.git(ctx, append([]string{"bundle", "create", "--quiet", path}, refs...)...); err != nil {
		err = fmt.Errorf("bundling the base: %w", err)
	}
	return
}

// --- result ---

// Import fetches HEAD of the bundle at path (from the guest) to the incoming
// ref of branch and returns its commit.
func (q Quarantine) Import(ctx context.Context, path, branch string) (head string, err error) {
	ref := incomingRef(branch)
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
		err = q.protectedError(ctx, base, head, protected)
	}
	return
}

// protectedError names, per commit of base..head, the protected paths it
// touches. Subjects and paths come from the guest.
func (q Quarantine) protectedError(ctx context.Context, base, head string, protected []string) (err error) {
	shas, err := q.git(ctx, "rev-list", base+".."+head)
	if err != nil {
		return
	}
	var lines []string
	for sha := range strings.FieldsSeq(shas) {
		var out, subject string
		if out, err = q.git(ctx, "log", "-1", "-m", "--no-renames", "--no-ext-diff", "--no-textconv", "--name-only", "--format=", "-z", sha); err != nil {
			return
		}
		hits := protectedChanges(strings.Split(out, "\x00"), protected)
		if len(hits) == 0 {
			continue
		}
		if subject, err = q.git(ctx, "log", "-1", "--format=%s", sha); err != nil {
			return
		}
		// One line per commit: a path holding a newline must not fake a line of its own.
		paths := strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(agent.Sanitize(strings.Join(hits, ", ")))
		lines = append(lines, fmt.Sprintf("  %s %s: %s", sha[:12], agent.Sanitize(subject), paths))
	}
	err = fmt.Errorf("the result changes protected paths (commit, paths):\n%s", strings.Join(lines, "\n"))
	return
}

// Result returns the commit of the result branch, empty when it has none.
func (q Quarantine) Result(ctx context.Context, branch string) (commit string, err error) {
	if _, refErr := q.git(ctx, "rev-parse", "--quiet", "--verify", resultRef(branch)); refErr != nil {
		return
	}
	commit, err = q.git(ctx, "rev-parse", "--verify", "--end-of-options", resultRef(branch)+"^{commit}")
	return
}

// Accept sets the result branch to the validated head. It only moves the
// branch from old, which is empty when it must not exist yet: nothing
// else is ever overwritten.
func (q Quarantine) Accept(ctx context.Context, branch, head, old string) (err error) {
	if _, err = q.git(ctx, "update-ref", resultRef(branch), head, old); err != nil {
		err = fmt.Errorf("accepting the result (is %s not at %q?): %w", resultRef(branch), old, err)
	}
	return
}

// Resume imports the tip of branch from remote as the result branch of a
// Spec. The base ref of branch moves to the merge base of the tip and
// the base it had (the base branch may have moved on since the Spec began);
// the tip is validated against it like any result. The result branch may
// already exist from the run that pushed the tip: it must be at the tip or
// behind it, and is moved to it. A branch with commits the remote lacks is an
// error, as nothing here may overwrite them. It returns the tip and the new base.
func (q Quarantine) Resume(ctx context.Context, remote forge.Remote, branch string, protected []string) (tip, base string, err error) {
	ref := incomingRef(branch)
	if _, err = q.gitWith(ctx, remote.Env, "", "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--end-of-options", remote.URL, "+"+resultRef(branch)+":"+ref); err != nil {
		err = fmt.Errorf("fetching %s from %s: %w", branch, remote.URL, err)
		return
	}
	if tip, err = q.git(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		err = fmt.Errorf("reading the tip of %s: %w", branch, err)
		return
	}
	if base, err = q.git(ctx, "merge-base", baseRef(branch), tip); err != nil {
		err = fmt.Errorf("%s on %s has no common history with the base: %w", branch, remote.URL, err)
		return
	}
	if _, err = q.Validate(ctx, base, tip, protected); err != nil {
		err = fmt.Errorf("%s on %s rejected: %w", branch, remote.URL, err)
		return
	}
	// The result of the previous run, if any.
	local, refErr := q.git(ctx, "rev-parse", "--quiet", "--verify", resultRef(branch)+"^{commit}")
	if refErr != nil {
		local = ""
	}
	if local != "" {
		if _, ancestorErr := q.git(ctx, "merge-base", "--is-ancestor", local, tip); ancestorErr != nil {
			err = fmt.Errorf("%s in %s (%s) has commits that %s lacks: push them (git --git-dir=%s push %s %s) or drop them (git --git-dir=%s update-ref -d %s), then run again",
				branch, q.Dir, local, remote.URL, q.Dir, remote.URL, branch, q.Dir, resultRef(branch))
			return
		}
	}
	if _, err = q.git(ctx, "update-ref", baseRef(branch), base); err != nil {
		err = fmt.Errorf("moving the base of %s: %w", branch, err)
		return
	}
	err = q.Accept(ctx, branch, tip, local)
	return
}

// Subjects lists the subjects of the commits in base..head, oldest first.
func (q Quarantine) Subjects(ctx context.Context, base, head string) (subjects []string, err error) {
	out, err := q.git(ctx, "log", "--reverse", "--format=%s", base+".."+head)
	if err != nil {
		return
	}
	if out != "" {
		subjects = strings.Split(out, "\n")
	}
	return
}

// --- squash and push ---

// Squash creates one commit with the tree of head on top of base, authored
// and committed by ident, and returns it. No ref points to it yet.
func (q Quarantine) Squash(ctx context.Context, base, head, message string, ident Ident) (commit string, err error) {
	env := []string{
		"GIT_AUTHOR_NAME=" + ident.Name, "GIT_AUTHOR_EMAIL=" + ident.Email,
		"GIT_COMMITTER_NAME=" + ident.Name, "GIT_COMMITTER_EMAIL=" + ident.Email,
	}
	if commit, err = q.gitWith(ctx, env, message, "commit-tree", "-F", "-", "-p", base, "--end-of-options", head+"^{tree}"); err != nil {
		err = fmt.Errorf("squashing the result: %w", err)
	}
	return
}

// RemoteTip returns the commit of branch on remote, empty when it has none.
func (q Quarantine) RemoteTip(ctx context.Context, remote forge.Remote, branch string) (tip string, err error) {
	out, err := q.gitWith(ctx, remote.Env, "", "ls-remote", "--heads", "--end-of-options", remote.URL, resultRef(branch))
	if err != nil {
		err = fmt.Errorf("listing branches of %s: %w", remote.URL, err)
		return
	}
	// The pattern matches any ref ending in it; only the exact one counts.
	for line := range strings.Lines(out) {
		if sha, ref, _ := strings.Cut(strings.TrimSpace(line), "\t"); ref == resultRef(branch) {
			tip = sha
		}
	}
	return
}

// Push sends the result branch to remote. It never overwrites someone's
// work: the lease lets the push only create the branch (old is empty, so
// one that exists on the remote fails it) or move it from old, the commit
// afk pushed or read last.
func (q Quarantine) Push(ctx context.Context, remote forge.Remote, branch, old string) (err error) {
	ref := resultRef(branch)
	if _, err = q.gitWith(ctx, remote.Env, "", "push", "--quiet", "--no-verify", "--force-with-lease="+ref+":"+old, "--end-of-options", remote.URL, ref+":"+ref); err != nil {
		err = fmt.Errorf("pushing %s to %s: %w", branch, remote.URL, err)
	}
	return
}

func baseRef(branch string) (ref string)     { return baseRefPrefix + branch }
func incomingRef(branch string) (ref string) { return incomingRefPrefix + branch }
func resultRef(branch string) (ref string)   { return resultRefPrefix + branch }

// protectedChanges returns the changed paths that touch a protected entry:
// the entry itself, anything under it, or one of its parent directories
// (replaced by a file or a symlink). Entries may end with "/"; an entry
// starting with "**/" matches below any directory too. Matching ignores
// case, as checkouts on case-insensitive file systems do.
func protectedChanges(changed, protected []string) (hits []string) {
	seen := map[string]bool{}
	for _, path := range changed {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if slices.ContainsFunc(protected, func(entry string) bool { return touches(strings.ToLower(path), entry) }) {
			hits = append(hits, path)
		}
	}
	return
}

// touches reports whether the lower-case path p touches entry.
func touches(p, entry string) (ok bool) {
	e, anyDepth := strings.CutPrefix(strings.ToLower(strings.TrimSuffix(entry, "/")), "**/")
	if e == "" {
		return false
	}
	for {
		if p == e || strings.HasPrefix(p, e+"/") || strings.HasPrefix(e, p+"/") {
			return true
		}
		_, rest, found := strings.Cut(p, "/")
		if !anyDepth || !found {
			return false
		}
		p = rest
	}
}
