package stack

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	composeFile    = "compose.yml"
	composeAfkFile = "compose.afk.yml"
)

// export replaces dir with the tree of commit in repo. Compose files and
// build contexts come from here, never from a working tree (ADR-0010).
func export(ctx context.Context, repo, commit, dir string) (err error) {
	for _, name := range []string{composeFile, composeAfkFile} {
		if out, catErr := exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "-e", "--end-of-options", commit+":"+name).CombinedOutput(); catErr != nil {
			err = fmt.Errorf("%s is missing on the base branch (commit and push it): %s", name, strings.TrimSpace(string(out)))
			return
		}
	}
	if err = os.RemoveAll(dir); err != nil {
		return
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	var stderr bytes.Buffer
	archive := exec.CommandContext(ctx, "git", "-C", repo, "archive", "--format=tar", "--end-of-options", commit)
	archive.Stderr = &stderr
	untar := exec.CommandContext(ctx, "tar", "-x", "--no-same-owner", "-C", dir)
	untar.Stderr = &stderr
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return
	}
	untar.Stdin = pipe
	if err = untar.Start(); err != nil {
		return
	}
	if err = archive.Start(); err != nil {
		_ = untar.Process.Kill()
		_ = untar.Wait()
		return
	}
	archiveErr := archive.Wait()
	if err = untar.Wait(); err == nil {
		err = archiveErr
	}
	if err != nil {
		err = fmt.Errorf("exporting %s: %w: %s", commit, err, strings.TrimSpace(stderr.String()))
	}
	return
}
