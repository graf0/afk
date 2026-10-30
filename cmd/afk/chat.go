package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/chat"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/run"
)

const chatUsage = "usage: afk chat <name> [--rm] [-- claude args...] | afk chat ls | afk chat shell <name> | afk chat rm <name>"

// What `afk chat` does.
const (
	chatEnter = "enter"
	chatList  = "ls"
	chatShell = "shell"
	chatRm    = "rm"
)

// chatCommand is `afk chat …` parsed.
type chatCommand struct {
	kind       string
	name       string   // not for ls
	remove     bool     // enter: --rm
	claudeArgs []string // enter: after "--"
}

// parseChat reads the arguments of `afk chat`: a subcommand (ls, shell <name>,
// rm <name>), or <name> with --rm before or after it and claude's arguments
// after "--". Those never reach the flags of afk.
func parseChat(args []string) (cmd chatCommand, err error) {
	head, tail, hasTail := args, []string(nil), false
	for i, arg := range args {
		if arg == "--" {
			head, tail, hasTail = args[:i], args[i+1:], true
			break
		}
	}
	if len(head) == 0 {
		err = errors.New(chatUsage)
		return
	}
	switch head[0] {
	case chatList, chatShell, chatRm:
		want := 2
		if head[0] == chatList {
			want = 1
		}
		if len(head) != want || hasTail {
			err = errors.New(chatUsage)
			return
		}
		cmd.kind = head[0]
		if want == 2 {
			cmd.name = head[1]
			err = chat.ValidateName(cmd.name)
		}
		return
	}
	flags := flag.NewFlagSet("chat", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&cmd.remove, "rm", false, "")
	if err = flags.Parse(head); err != nil || flags.NArg() == 0 {
		err = errors.New(chatUsage)
		return
	}
	cmd.kind, cmd.name = chatEnter, flags.Arg(0)
	if err = flags.Parse(flags.Args()[1:]); err != nil || flags.NArg() != 0 {
		err = errors.New(chatUsage)
		return
	}
	cmd.claudeArgs = tail
	if len(tail) == 0 {
		cmd.claudeArgs = nil
	}
	err = chat.ValidateName(cmd.name)
	return
}

// runChat executes `afk chat …`.
func runChat(ctx context.Context, args []string, dir string, stdout io.Writer) (err error) {
	cmd, err := parseChat(args)
	if err != nil {
		return
	}
	paths, err := image.DefaultPaths()
	if err != nil {
		return
	}
	// Only a Chat that starts needs the base branch as it is on the Forge now.
	readBase := run.ReadBaseLocal
	if cmd.kind == chatEnter {
		readBase = run.ReadBase
	}
	base, err := readBase(ctx, dir)
	if err != nil {
		return
	}
	opts := chat.Options{Base: base, Name: cmd.name, Paths: paths, In: os.Stdin, Out: stdout, Err: os.Stderr}
	switch cmd.kind {
	case chatEnter:
		err = chatEnterCommand(ctx, cmd, opts, dir)
	case chatList:
		var project string
		if project, err = image.ProjectName(base.Config.Forge.Repo); err != nil {
			return
		}
		err = chat.List(stdout, paths.StateDir, project, time.Now())
	case chatShell:
		err = chat.Shell(ctx, opts)
	case chatRm:
		err = chat.Remove(ctx, opts)
	}
	return
}

// chatEnterCommand creates the Chat or returns to it. A Project without
// compose.afk.yml on the base branch gets the template on a terminal.
func chatEnterCommand(ctx context.Context, cmd chatCommand, opts chat.Options, dir string) (err error) {
	if opts.ClaudeToken, err = agent.OAuthToken(opts.Paths.StateDir); err != nil {
		return
	}
	if opts.Forge, err = forge.New(ctx, opts.Base.Config, opts.Paths.StateDir); err != nil {
		return
	}
	opts.Args, opts.Remove = cmd.claudeArgs, cmd.remove
	err = chat.Run(ctx, opts)
	err = offerTemplateOnFailure(ctx, err, dir, os.Stdin, opts.Out, isTerminal(os.Stdin))
	return
}
