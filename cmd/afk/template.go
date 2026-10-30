package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"

	"github.com/graf0/afk/stack"
)

// offerTemplate handles err from a Stack that has no compose.afk.yml on the
// base branch (stack.ErrNoComposeAfk); any other err is returned untouched.
// It first looks at the working tree containing dir: if the template files
// are already there, it writes nothing and says to commit and push them (or
// that a lone file is not overwritten). Otherwise, on a terminal, it asks
// whether to write the template there. After yes it writes it, tells the user
// to commit and push it to the base branch, and returns created: the caller
// then ends without error. Without a terminal or after no it returns an error.
func offerTemplate(ctx context.Context, err error, dir string, in io.Reader, out io.Writer, tty bool) (created bool, outErr error) {
	if !errors.Is(err, stack.ErrNoComposeAfk) {
		outErr = err
		return
	}
	root, rootErr := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if rootErr != nil {
		outErr = fmt.Errorf("%w\nfinding the working tree: %w", err, rootErr)
		return
	}
	tree := strings.TrimSpace(string(root))
	switch existing := stack.ExistingTemplateFiles(tree); len(existing) {
	case 0:
	case 2:
		outErr = fmt.Errorf("%w\nthe working tree has %s, but the base branch does not: commit and push them", err, strings.Join(existing, " and "))
		return
	default:
		outErr = fmt.Errorf("%w\n%s exists in the working tree and is not overwritten: remove it to get the template, or add the other file yourself, then commit and push both", err, existing[0])
		return
	}
	if !tty {
		outErr = fmt.Errorf("%w\nrun afk on a terminal to create the template, or add compose.afk.yml and .afk/Dockerfile yourself, then commit and push them to the base branch", err)
		return
	}
	_, _ = fmt.Fprintf(out, "compose.afk.yml is missing on the base branch. Create compose.afk.yml and .afk/Dockerfile in %s? [y/N] ", tree)
	answer, _ := bufio.NewReader(in).ReadString('\n')
	if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
		outErr = err
		return
	}
	if outErr = stack.WriteTemplate(tree); outErr != nil {
		return
	}
	created = true
	_, _ = fmt.Fprintf(out, "created compose.afk.yml and .afk/Dockerfile in %s. afk does not commit them: review, then\n  git add compose.afk.yml .afk && git commit -m 'afk: Stack' && git push\nto the base branch, and run afk again.\n", tree)
	return
}

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) (ok bool) { return term.IsTerminal(int(f.Fd())) }
