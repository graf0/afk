package run

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// git runs host git with args and returns its trimmed stdout. Only on
// host-owned repositories: the user's and the Quarantine repo (ADR-0002).
func git(ctx context.Context, args ...string) (out string, err error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if err = cmd.Run(); err != nil {
		err = fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		return
	}
	out = strings.TrimSpace(stdout.String())
	return
}
