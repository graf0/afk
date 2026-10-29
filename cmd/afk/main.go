// Command afk runs AI coding agents against a Project's tickets.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/graf0/afk/chat"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/run"
)

func main() {
	ctx, abort := handleSignals(context.Background())
	name := "afk"
	var err error
	if filepath.Base(os.Args[0]) == chat.ForgeBinary { // the copy in a Chat's VM (ADR-0012)
		name = chat.ForgeBinary
		err = chat.RunForge(ctx, chat.ForgeSocket, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	} else {
		var dir string
		if dir, err = os.Getwd(); err == nil {
			err = runCommand(ctx, abort, os.Args[1:], dir, os.Stdout)
		}
	}
	if err != nil {
		// Config errors come one per line; each line gets the prefix.
		for line := range strings.SplitSeq(strings.TrimSuffix(err.Error(), "\n"), "\n") {
			fmt.Fprintf(os.Stderr, "%s: %s\n", name, line)
		}
		if errors.Is(err, chat.ErrNeedsForce) {
			os.Exit(chat.ExitNeedsForce) // the TUI asks about --force
		}
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
	// Commands that run something read the base branch (ADR-0010). The working
	// tree is read only to check the file: by `afk config` and by afk without a command.
	switch {
	case len(args) == 0:
		_, err = config.Load(dir)
	case args[0] == "config":
		var cfg config.Config
		if cfg, err = config.Load(dir); err != nil {
			return
		}
		var data []byte
		if data, err = cfg.Marshal(); err != nil {
			return
		}
		_, err = stdout.Write(data)
	case args[0] == "image":
		err = runImage(ctx, abort, args[1:], dir, stdout)
	case args[0] == "chat":
		err = runChat(ctx, args[1:], dir, stdout)
	case args[0] == "vm":
		err = runVM(ctx, args[1:], dir, stdout)
	case args[0] == "tui": // all Projects: no config of the current one
		err = runTUI(ctx, args[1:])
	case args[0] == "sidebar":
		err = runSidebar(ctx, args[1:])
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	return
}

// runImage executes `afk image <subcommand>`.
func runImage(ctx context.Context, abort <-chan struct{}, args []string, dir string, stdout io.Writer) (err error) {
	if len(args) != 1 || args[0] != "build" {
		err = fmt.Errorf("usage: afk image build")
		return
	}
	base, err := run.ReadBase(ctx, dir)
	if err != nil {
		return
	}
	paths, err := image.DefaultPaths()
	if err != nil {
		return
	}
	_, err = image.BuildExclusive(ctx, image.Options{Paths: paths, Config: base.Config, Out: stdout, Abort: abort, ProjectSettings: base.Settings}, func() ([]string, error) { return machine.Overlays("") })
	return
}
