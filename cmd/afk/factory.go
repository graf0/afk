package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/run"
)

const factoryUsage = "usage: afk factory ['*' | '#N' ...] (every ready ticket by default, or the listed ones in order)"

// runFactory executes `afk factory [selector]`: the Factory loop of the
// Project, which ends when nothing is left to take, or on the first failed Run.
func runFactory(ctx context.Context, args []string, dir string, stdout io.Writer) (err error) {
	selector, err := parseSelector(args)
	if err != nil {
		return
	}
	paths, err := image.DefaultPaths()
	if err != nil {
		return
	}
	claudeToken, err := agent.OAuthToken(paths.StateDir)
	if err != nil {
		return
	}
	base, err := run.ReadBase(ctx, dir)
	if err != nil {
		return
	}
	gitForge, err := forge.New(ctx, base.Config, paths.StateDir)
	if err != nil {
		return
	}
	// OSC 2 only on a terminal: never into logs or redirections.
	if file, ok := stdout.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		stdout = run.NewTitle(file)
	}
	factory, err := run.NewFactory(ctx, base, gitForge, paths, claudeToken, selector, stdout)
	if err != nil {
		return
	}
	err = factory.Run(ctx)
	err = offerTemplateOnFailure(ctx, err, dir, os.Stdin, stdout, isTerminal(os.Stdin))
	return
}

// offerTemplateOnFailure is the end of `afk factory`: a Project without
// compose.afk.yml on the base branch (stack.ErrNoComposeAfk from the first Run)
// gets the template on a terminal, which is no failure; any other err stays.
func offerTemplateOnFailure(ctx context.Context, err error, dir string, in io.Reader, out io.Writer, tty bool) (outErr error) {
	if err == nil {
		return
	}
	created, outErr := offerTemplate(ctx, err, dir, in, out, tty)
	if created {
		outErr = nil
	}
	return
}

// parseSelector accepts no arguments or "*" (every ready ticket: nil), else
// ticket numbers, "#N" or "N", in the order given.
func parseSelector(args []string) (selector []int, err error) {
	if len(args) == 0 || (len(args) == 1 && args[0] == "*") {
		return
	}
	for _, arg := range args {
		n, convErr := strconv.Atoi(strings.TrimPrefix(arg, "#"))
		if convErr != nil || n <= 0 {
			selector, err = nil, errors.New(factoryUsage)
			return
		}
		selector = append(selector, n)
	}
	return
}
