// Command afk runs AI coding agents against a Project's tickets.
package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/graf0/afk/chat"
	"github.com/graf0/afk/config"
)

func main() {
	ctx := handleSignals(context.Background())
	name := "afk"
	var err error
	if filepath.Base(os.Args[0]) == chat.ForgeBinary { // afk mounted in a Chat's Sandbox (ADR-0012)
		name = chat.ForgeBinary
		err = chat.RunForge(ctx, cmp.Or(os.Getenv(chat.SocketEnv), chat.ForgeSocket), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	} else {
		var dir string
		if dir, err = os.Getwd(); err == nil {
			err = runCommand(ctx, os.Args[1:], dir, os.Stdout)
		}
	}
	if err != nil {
		// Config errors come one per line; each line gets the prefix.
		for line := range strings.SplitSeq(strings.TrimSuffix(err.Error(), "\n"), "\n") {
			fmt.Fprintf(os.Stderr, "%s: %s\n", name, line)
		}
		os.Exit(1)
	}
}

// handleSignals turns the first SIGINT/SIGTERM into cancellation of the
// returned context (graceful stop: the Stack goes down) and says so on stderr.
// A second signal gets the default action: stop right now.
func handleSignals(parent context.Context) (ctx context.Context) {
	ctx, cancel := context.WithCancel(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		fmt.Fprintln(os.Stderr, "afk: cleaning up; Ctrl-C again aborts")
		cancel()
		signal.Stop(signals)
	}()
	return ctx
}

// runCommand executes the command from args in the Project containing dir.
func runCommand(ctx context.Context, args []string, dir string, stdout io.Writer) (err error) {
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
	case args[0] == "chat":
		err = runChat(ctx, args[1:], dir, stdout)
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	return
}
