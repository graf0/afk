package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/chat"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/run"
)

const chatUsage = "usage: afk chat new <name> [--model <model>] [--effort <low|medium|high|xhigh|max>] | afk chat attach <name> | afk chat export <name> | afk chat rm [--force] <name> | afk chat list"

// runChat executes `afk chat <subcommand>`.
func runChat(ctx context.Context, args []string, dir string, stdout io.Writer) (err error) {
	if len(args) == 0 {
		err = errors.New(chatUsage)
		return
	}
	switch args[0] {
	case "new":
		err = chatNew(ctx, args[1:], dir, stdout)
	case "attach":
		if len(args) != 2 {
			err = errors.New(chatUsage)
			return
		}
		err = chatAttach(ctx, args[1], dir, stdout)
	case "export":
		if len(args) != 2 {
			err = errors.New(chatUsage)
			return
		}
		err = chatExport(ctx, args[1], dir, stdout)
	case "rm":
		err = chatRm(ctx, args[1:], dir, stdout)
	case "list":
		if len(args) != 1 {
			err = errors.New(chatUsage)
			return
		}
		err = chatList(ctx, dir, stdout)
	default:
		err = errors.New(chatUsage)
	}
	return
}

// chatNew creates the Chat and enters it.
func chatNew(ctx context.Context, args []string, dir string, stdout io.Writer) (err error) {
	name, model, effort, err := parseChatNew(args)
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
	err = chat.New(ctx, chat.Options{
		Base: base, Name: name, Model: model, Effort: effort,
		ClaudeToken: claudeToken, Paths: paths, Forge: gitForge, Out: stdout,
	})
	return
}

// chatAttach returns to the existing Chat name.
func chatAttach(ctx context.Context, name string, dir string, stdout io.Writer) (err error) {
	if err = chat.ValidateName(name); err != nil {
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
	err = chat.Attach(ctx, chat.Options{Base: base, Name: name, ClaudeToken: claudeToken, Paths: paths, Out: stdout})
	return
}

// chatExport pushes the commits of the Chat name to chat-<name> on the Forge.
func chatExport(ctx context.Context, name string, dir string, stdout io.Writer) (err error) {
	if err = chat.ValidateName(name); err != nil {
		return
	}
	paths, err := image.DefaultPaths()
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
	err = chat.Export(ctx, chat.Options{Base: base, Name: name, Paths: paths, Forge: gitForge, Out: stdout})
	return
}

// chatRm destroys the Chat and its VM.
func chatRm(ctx context.Context, args []string, dir string, stdout io.Writer) (err error) {
	name, force, err := parseChatRm(args)
	if err != nil {
		return
	}
	paths, err := image.DefaultPaths()
	if err != nil {
		return
	}
	base, err := run.ReadBase(ctx, dir)
	if err != nil {
		return
	}
	err = chat.Remove(ctx, chat.Options{Base: base, Name: name, Paths: paths, Out: stdout}, force)
	return
}

// parseChatRm reads `[--force] <name>`, the flag before or after the name.
func parseChatRm(args []string) (name string, force bool, err error) {
	fs := flag.NewFlagSet("chat rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&force, "force", false, "")
	if err = fs.Parse(args); err != nil || fs.NArg() == 0 {
		err = errors.New(chatUsage)
		return
	}
	name = fs.Arg(0)
	if err = fs.Parse(fs.Args()[1:]); err != nil || fs.NArg() != 0 {
		err = errors.New(chatUsage)
		return
	}
	err = chat.ValidateName(name)
	return
}

// parseChatNew reads `<name> [--model M] [--effort E]`, the flags before or
// after the name, and validates them.
func parseChatNew(args []string) (name, model, effort string, err error) {
	fs := flag.NewFlagSet("chat new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&model, "model", "", "")
	fs.StringVar(&effort, "effort", "", "")
	if err = fs.Parse(args); err != nil || fs.NArg() == 0 {
		err = errors.New(chatUsage)
		return
	}
	name = fs.Arg(0)
	if err = fs.Parse(fs.Args()[1:]); err != nil || fs.NArg() != 0 {
		err = errors.New(chatUsage)
		return
	}
	err = errors.Join(chat.ValidateName(name), chat.ValidateEffort(effort))
	return
}

// chatList prints the Chats of the Project.
func chatList(ctx context.Context, dir string, stdout io.Writer) (err error) {
	base, err := run.ReadBase(ctx, dir)
	if err != nil {
		return
	}
	project, err := image.ProjectName(base.Config.Forge.Repo)
	if err != nil {
		return
	}
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	machines, err := conn.List(project)
	if err != nil {
		return
	}
	err = chat.List(stdout, machines, time.Now())
	return
}
