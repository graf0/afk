package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/sandbox"
	"github.com/graf0/afk/stack"
)

const (
	// Files next to the clone, in the Run's context directory: read-only in
	// the Sandbox, written by the host alone.
	contextName    = "context"
	ticketFileName = "TICKET.md"
	specFileName   = "SPEC.md"

	// defaultAgent is the host's claude, mounted read-only into the Agent's $HOME.
	defaultAgent = `"$HOME/bin/claude"`
)

// sandboxGuest is the Sandbox of the Run's Stack as its guest.
type sandboxGuest struct {
	run      run
	stack    *stack.Stack
	sessions int // numbers the prompt files
}

func (g *sandboxGuest) Exec(ctx context.Context, command string, stdout io.Writer) (err error) {
	err = g.stack.Exec(ctx, command, stdout)
	return
}

// Agent runs one session in the Sandbox. The token is in its environment (the
// Stack's env file) and the prompt is a file of the context directory, so
// neither is on a command line.
func (g *sandboxGuest) Agent(ctx context.Context, prompt, model string) (report agent.Report, err error) {
	r := g.run
	g.sessions++
	promptFile := filepath.Join(r.contextDir(), fmt.Sprintf("prompt-%d.md", g.sessions))
	if err = os.WriteFile(promptFile, []byte(prompt), 0o644); err != nil {
		err = fmt.Errorf("writing the prompt: %w", err)
		return
	}
	binary := defaultAgent
	if r.AgentBinary != "" {
		binary = agent.ShellQuote(r.AgentBinary)
	}
	command := agent.SandboxCommand(binary, promptFile, model)
	reader, writer := io.Pipe()
	execDone := make(chan error, 1)
	go func() {
		execErr := g.stack.Exec(ctx, command, writer)
		_ = writer.Close()
		execDone <- execErr
	}()
	report, streamErr := agent.Stream(reader, r.Out, r.log)
	// A stream that failed stops the Agent: its next write fails.
	_ = reader.CloseWithError(streamErr)
	execErr := <-execDone
	switch {
	case ctx.Err() != nil:
		err = fmt.Errorf("agent interrupted: %w", ctx.Err())
	case execErr != nil:
		// The exit message and stderr come from the Sandbox.
		err = fmt.Errorf("agent exited: %s", agent.Sanitize(execErr.Error()))
	}
	if err = errors.Join(err, streamErr); err != nil {
		err = fmt.Errorf("%w (log: %s)", err, r.log.path)
	}
	return
}

// --- paths ---

// stackName is the name of the Run's Stack: ticket-N unless id is set.
func (r run) stackName() (name string) {
	return cmp.Or(r.id, ticketName(r.Ticket.Number))
}

func (r run) stackDir() (dir string) {
	return stack.Dir(r.StateDir, r.project, stack.Run, r.stackName())
}

// treePath is the Agent's clone, the same path on the host and in the Sandbox.
func (r run) treePath() (path string) { return stack.TreeIn(r.stackDir()) }

func (r run) contextDir() (dir string) { return filepath.Join(r.stackDir(), contextName) }

func (r run) contextFile(name string) (path string) { return filepath.Join(r.contextDir(), name) }

// --- stack ---

// enter runs work in the Sandbox of the Run's Stack: in the state an earlier
// Factory left when there is one (opts.Resume), else in a fresh clone.
func (r run) enter(ctx context.Context, work workFunc) (res result, err error) {
	resume := r.Resume != nil
	// The compose files and everything else of the Sandbox come from one
	// base commit. A resumed Run's is old: it is in the Quarantine repo, not
	// necessarily in the user's repository any more.
	repo := r.Base.Repo
	if resume {
		repo = r.quarantine.Dir
		if r.Base, err = r.Base.pinned(ctx, repo); err != nil {
			return
		}
	}
	view, err := r.sandbox()
	if err != nil {
		return
	}
	st, err := r.newStack(repo, view.Mounts)
	if err != nil {
		return
	}
	var checkout string // the command that puts the fresh clone on its branch
	if !resume {
		if _, statErr := os.Stat(r.stackDir()); statErr == nil {
			err = fmt.Errorf("%s holds the state of an earlier Run of %s: `afk factory` continues it", r.stackDir(), r.stackName())
			return
		}
		r.say("cloning the base into %s", r.treePath())
		var start string
		if start, err = r.guestBranch().CloneTo(ctx, r.treePath(), r.Base.Branch, r.start() != r.Base.Commit); err != nil {
			_ = os.RemoveAll(r.stackDir()) // nothing of the Agent's yet
			return
		}
		checkout = r.guestBranch().CheckoutCommand(start)
	} else {
		r.say("resuming in %s", r.stackDir())
	}
	// Whatever happens, the Stack ends with this Run (ADR-0015): its state
	// goes with a result, stays with a failure or Ctrl-C.
	up := false
	defer func() {
		cleanup := context.WithoutCancel(ctx)
		switch {
		case err == nil:
			// The result is good whatever happens to the leftovers.
			if rmErr := st.Remove(cleanup); rmErr != nil {
				r.say("warning: removing the Stack %s failed, its state stays in %s: %v", r.stackName(), r.stackDir(), agent.Sanitize(rmErr.Error()))
				if downErr := st.Down(cleanup); downErr != nil {
					r.say("warning: %v", agent.Sanitize(downErr.Error()))
				}
				return
			}
			r.say("Stack %s removed", r.stackName())
		case !up && !resume:
			// Never started: no work to continue.
			err = errors.Join(err, st.Remove(cleanup))
		default:
			r.say("Run failed; its state stays in %s: `afk factory` continues it", r.stackDir())
			err = errors.Join(err, st.Down(cleanup))
		}
	}()
	if resume {
		// The Sandbox of a Factory that was killed may still run, and its Agent
		// owns the home: nothing of it may run while the mount points are made.
		if err = st.Down(ctx); err != nil {
			return
		}
	}
	if err = r.prepareContext(st, view); err != nil {
		return
	}
	if err = st.Up(ctx); err != nil {
		return
	}
	up = true
	if r.stackFiles != nil {
		r.stackFiles.paths = st.Protected()
	}
	r.say("Stack %s is up", r.stackName())
	if checkout != "" {
		if err = st.Exec(ctx, checkout, io.Discard); err != nil {
			err = fmt.Errorf("checking out the clone in the Sandbox: %w", err)
			return
		}
	}
	if err = st.Setup(ctx); err != nil {
		return
	}
	res, err = work(ctx, &sandboxGuest{run: r, stack: st})
	return
}

// sandbox is what the host gives the Sandbox besides the Stack's own mounts:
// the view of the Agent (package sandbox) and the context directory with the
// ticket, written by the host, read-only for the Agent, outside the clone and
// home.
func (r run) sandbox() (view sandbox.View, err error) {
	view, err = sandbox.Build(sandbox.Options{
		ContextDir: r.contextDir(), DataDir: r.pluginDataDir(),
		Settings: r.Base.Settings, Plugins: r.Base.Plugins, NoClaude: r.AgentBinary != "",
	})
	if err != nil {
		return
	}
	view.Files[ticketFileName] = []byte(agent.TicketFile(r.Ticket))
	if r.spec != nil {
		view.Files[specFileName] = []byte(agent.TicketFile(*r.spec))
	}
	view.Mounts = append(view.Mounts, stack.Mount{Source: r.contextDir(), Target: r.contextDir(), ReadOnly: true, Dir: true})
	return
}

// pluginDataDir is the data directory of the plugins of this Run alone.
func (r run) pluginDataDir() (dir string) { return filepath.Join(r.stackDir(), "plugin-data") }

// newStack is the Stack of the Run from the base commit in repo: its token and
// mounts.
func (r run) newStack(repo string, mounts []stack.Mount) (st *stack.Stack, err error) {
	st, err = stack.New(stack.Options{
		Repo: repo, Commit: r.Base.Commit, Project: r.project, Kind: stack.Run, Name: r.stackName(),
		StateDir: r.StateDir, Env: sandbox.Env(r.ClaudeToken), Mounts: mounts, Out: r.Out,
	})
	return
}

// --- context ---

// prepareContext prepares the view in the context directory and the Agent's
// home. A Run's clone and home may be the Agent's work of an earlier Factory.
func (r run) prepareContext(st *stack.Stack, view sandbox.View) (err error) {
	if view.Files[specFileName] == nil {
		if err = os.RemoveAll(r.contextFile(specFileName)); err != nil {
			return
		}
	}
	err = view.Prepare(st.Home(), st.Tree())
	return
}
