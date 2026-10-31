// Command afk runs AI coding agents against a Project's tickets.
package main

import (
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
	"github.com/graf0/afk/stack"
)

func main() {
	ctx := handleSignals(context.Background())
	name := "afk"
	var err error
	if filepath.Base(os.Args[0]) == chat.CLIBinary { // afk mounted in a Chat's Sandbox (ADR-0012)
		name = chat.CLIBinary
		err = chat.RunForge(ctx, chat.CLISocket, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
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
		if _, err = stdout.Write(data); err == nil {
			err = printVMKey(cfg, stdout)
		}
	case args[0] == "chat":
		err = runChat(ctx, args[1:], dir, stdout)
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	return
}

// printVMKey prints the public ssh key of the Project's test VMs, as comment
// lines after the config, when [[vm]] declares any. The key is made here if no
// Chat did yet: it has to be on the VMs before the first Chat uses them.
func printVMKey(cfg config.Config, stdout io.Writer) (err error) {
	if len(cfg.VM) == 0 {
		return
	}
	stateDir, err := stack.DefaultStateDir()
	if err != nil {
		return
	}
	project, err := stack.ProjectName(cfg.Forge.Repo)
	if err != nil {
		return
	}
	public, err := chat.EnsureVMKey(filepath.Join(stateDir, project))
	if err != nil {
		return
	}
	_, err = fmt.Fprintf(stdout, "\n# ssh public key of the test VMs (add it to authorized_keys of each VM user):\n# %s\n", public)
	return
}
