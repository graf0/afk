package main

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/run"
)

const factoryUsage = "usage: afk factory '#N' (one ticket or Spec; '*' and lists are not supported yet)"

// runFactory executes `afk factory '#N'`: one Run of ticket N, or of a Spec
// (a ticket with sub-issues) a Run of each of its children and a review.
func runFactory(ctx context.Context, args []string, dir string, stdout io.Writer) (err error) {
	n, err := parseSelector(args)
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
	forgeToken, err := forge.HostToken(ctx)
	if err != nil {
		return
	}
	github, err := forge.NewGitHub(base.Config.Forge, forgeToken)
	if err != nil {
		return
	}
	ticket, err := github.ReadyTicket(ctx, n, base.Config.Labels.ReadyForAgent)
	if err != nil {
		return
	}
	opts := run.Options{Base: base, Ticket: ticket, ClaudeToken: claudeToken, Paths: paths, Out: stdout, Forge: github}
	if ticket.SubIssues > 0 {
		err = run.ExecuteSpec(ctx, opts)
		return
	}
	err = run.Execute(ctx, opts)
	return
}

// parseSelector accepts exactly one ticket number, "#N" or "N".
func parseSelector(args []string) (n int, err error) {
	if len(args) != 1 {
		err = errors.New(factoryUsage)
		return
	}
	n, convErr := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
	if convErr != nil || n <= 0 {
		err = errors.New(factoryUsage)
	}
	return
}
