// Command afk runs AI coding agents against a Project's tickets.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/image"
)

func main() {
	ctx, abort := handleSignals(context.Background())
	dir, err := os.Getwd()
	if err == nil {
		err = runCommand(ctx, abort, os.Args[1:], dir, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "afk:", err)
		os.Exit(1)
	}
}

// handleSignals turns the first SIGINT/SIGTERM into cancellation of the
// returned context (graceful stop) and the second into closing abort (stop
// right now). A third signal gets the default action.
func handleSignals(parent context.Context) (ctx context.Context, abort <-chan struct{}) {
	ctx, cancel := context.WithCancel(parent)
	hard := make(chan struct{})
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		cancel()
		<-signals
		close(hard)
		signal.Stop(signals)
	}()
	return ctx, hard
}

// runCommand executes the command from args in the Project containing dir.
// Closing abort stops long work immediately, without cleanup.
func runCommand(ctx context.Context, abort <-chan struct{}, args []string, dir string, stdout io.Writer) (err error) {
	// The Factory reads .afk.toml only from the base branch, never the working tree (ADR-0002).
	if len(args) > 0 && args[0] == "factory" {
		err = runFactory(ctx, args[1:], dir, stdout)
		return
	}
	cfg, err := config.Load(dir)
	if err != nil {
		err = fmt.Errorf("loading project: %w", err)
		return
	}
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "config":
		var data []byte
		data, err = cfg.Marshal()
		if err != nil {
			return
		}
		_, err = stdout.Write(data)
	case "image":
		err = runImage(ctx, abort, args[1:], dir, cfg, stdout)
	case "vm":
		err = runVM(ctx, args[1:], dir, cfg, stdout)
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	return
}

// runImage executes `afk image <subcommand>`.
func runImage(ctx context.Context, abort <-chan struct{}, args []string, dir string, cfg config.Config, stdout io.Writer) (err error) {
	if len(args) != 1 || args[0] != "build" {
		err = fmt.Errorf("usage: afk image build")
		return
	}
	paths, err := image.DefaultPaths()
	if err != nil {
		return
	}
	// Like .afk.toml above: the working tree of the Project.
	root, err := config.FindRoot(dir)
	if err != nil {
		return
	}
	settings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("reading .claude/settings.json: %w", err)
		return
	}
	_, err = image.Build(ctx, image.Options{Paths: paths, Config: cfg, Out: stdout, Abort: abort, ProjectSettings: settings})
	return
}
