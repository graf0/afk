package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/graf0/afk/tui"
)

const (
	tuiUsage     = "usage: afk tui"
	sidebarUsage = "usage: afk sidebar [--current <window>] (started by afk tui)"
)

// runTUI executes `afk tui`: the tmux session of all Chats, Factory and
// abandoned Runs, whatever the Project of the current directory.
func runTUI(ctx context.Context, args []string) (err error) {
	if len(args) != 0 {
		err = errors.New(tuiUsage)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	err = tui.Run(ctx, exe)
	return
}

// runSidebar executes `afk sidebar`, the left pane of the TUI.
func runSidebar(ctx context.Context, args []string) (err error) {
	fs := flag.NewFlagSet("sidebar", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	current := fs.String("current", "", "")
	if err = fs.Parse(args); err != nil || fs.NArg() != 0 {
		err = errors.New(sidebarUsage)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	err = tui.RunSidebar(ctx, exe, *current)
	return
}
