package run

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/stack"
)

const (
	// Files next to the clone, in the Run's context directory: read-only in
	// the Sandbox, written by the host alone.
	contextName        = "context"
	ticketFileName     = "TICKET.md"
	specFileName       = "SPEC.md"
	claudeSettingsName = "settings.json"

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
	return stack.Dir(r.Paths.StateDir, r.project, stack.Run, r.stackName())
}

// treePath is the Agent's clone, the same path on the host and in the Sandbox.
func (r run) treePath() (path string) { return stack.TreeIn(r.stackDir()) }

func (r run) contextDir() (dir string) { return filepath.Join(r.stackDir(), contextName) }

func (r run) contextFile(name string) (path string) { return filepath.Join(r.contextDir(), name) }

// --- stack ---

// sandboxMount is a bind mount of the Sandbox; dir says what is mounted.
type sandboxMount struct {
	stack.Mount
	dir bool
}

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
	files, mounts, err := r.sandbox()
	if err != nil {
		return
	}
	st, err := r.newStack(repo, mounts)
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
	if err = r.prepareContext(st, files, mounts); err != nil {
		return
	}
	if err = st.Up(ctx); err != nil {
		return
	}
	up = true
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
// the files of the context directory and the mounts. It is the one list: the
// Stack mounts them, prepareContext makes their mount points.
func (r run) sandbox() (files map[string][]byte, mounts []sandboxMount, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		err = fmt.Errorf("finding the home directory: %w", err)
		return
	}
	settings, err := claudeSettings(r.Base.Settings, r.Base.Plugins)
	if err != nil {
		return
	}
	files = map[string][]byte{ticketFileName: []byte(agent.TicketFile(r.Ticket)), claudeSettingsName: settings}
	if r.spec != nil {
		files[specFileName] = []byte(agent.TicketFile(*r.spec))
	}
	// Written by the host, read-only for the Agent, outside the clone and home.
	mounts = []sandboxMount{
		{Mount: stack.Mount{Source: r.contextDir(), Target: r.contextDir(), ReadOnly: true}, dir: true},
		{Mount: stack.Mount{Source: r.contextFile(claudeSettingsName), Target: filepath.Join(home, ".claude", claudeSettingsName), ReadOnly: true}},
	}
	if r.AgentBinary == "" {
		var binary string
		if binary, err = hostClaude(); err != nil {
			return
		}
		mounts = append(mounts, sandboxMount{Mount: stack.Mount{Source: binary, Target: filepath.Join(home, "bin", "claude"), ReadOnly: true}})
	}
	pluginFiles, pluginMounts, err := pluginView(home, r.contextDir(), r.pluginDataDir(), r.Base.Plugins)
	if err != nil {
		return
	}
	maps.Copy(files, pluginFiles)
	mounts = append(mounts, pluginMounts...)
	return
}

// pluginDataDir is the data directory of the plugins of this Run alone.
func (r run) pluginDataDir() (dir string) { return filepath.Join(r.stackDir(), "plugin-data") }

// newStack is the Stack of the Run from the base commit in repo: its token and
// mounts.
func (r run) newStack(repo string, mounts []sandboxMount) (st *stack.Stack, err error) {
	env := map[string]string{
		"GIT_AUTHOR_NAME": guestGitName, "GIT_AUTHOR_EMAIL": guestGitEmail,
		"GIT_COMMITTER_NAME": guestGitName, "GIT_COMMITTER_EMAIL": guestGitEmail,
		"DISABLE_AUTOUPDATER": "1",
	}
	if r.ClaudeToken != "" {
		env[agent.TokenEnv] = r.ClaudeToken
	}
	plain := make([]stack.Mount, len(mounts))
	for i, m := range mounts {
		plain[i] = m.Mount
	}
	st, err = stack.New(stack.Options{
		Repo: repo, Commit: r.Base.Commit, Project: r.project, Kind: stack.Run, Name: r.stackName(),
		StateDir: r.Paths.StateDir, Env: env, Mounts: plain, Out: r.Out,
	})
	return
}

// hostClaude is the file of the host's claude: the target of a symlink,
// which would be a dangling link in the Sandbox.
func hostClaude() (path string, err error) {
	found, err := exec.LookPath("claude")
	if err != nil {
		err = fmt.Errorf("claude is not on the PATH: %w", err)
		return
	}
	path, err = filepath.EvalSymlinks(found)
	return
}

// --- context ---

// prepareContext writes the files of the context directory and makes the mount
// points of mounts in the Agent's home. A Run's clone and home may be the
// Agent's work of an earlier Factory: only files are touched here, never git,
// and symlinks in the way of a mount point go.
func (r run) prepareContext(st *stack.Stack, files map[string][]byte, mounts []sandboxMount) (err error) {
	if err = os.MkdirAll(r.contextDir(), 0o755); err != nil {
		err = fmt.Errorf("creating the context directory: %w", err)
		return
	}
	if files[specFileName] == nil {
		if err = os.RemoveAll(r.contextFile(specFileName)); err != nil {
			return
		}
	}
	for name, data := range files {
		if err = os.WriteFile(r.contextFile(name), data, 0o644); err != nil {
			err = fmt.Errorf("writing %s: %w", name, err)
			return
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		err = fmt.Errorf("finding the home directory: %w", err)
		return
	}
	for _, dir := range []string{st.Home(), r.pluginDataDir()} {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			err = fmt.Errorf("creating %s: %w", dir, err)
			return
		}
	}
	// The tree is mounted under the home when the state directory is.
	points := append([]sandboxMount{{Mount: stack.Mount{Target: st.Tree()}, dir: true}}, mounts...)
	for _, m := range points {
		rel, relErr := filepath.Rel(home, m.Target)
		if relErr != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue // not in the home: no mount point of ours
		}
		if err = mountPoint(st.Home(), rel, m.dir); err != nil {
			err = fmt.Errorf("mount point %s: %w", m.Target, err)
			return
		}
	}
	return
}

// mountPoint makes rel in the Agent's home what a mount over it needs: a
// directory or a file, never a symlink. The Agent can write in its home, and
// podman makes the mount points it finds missing as a subuid.
func mountPoint(home, rel string, isDir bool) (err error) {
	path := home
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		path = filepath.Join(path, part)
		wantDir := i < len(parts)-1 || isDir
		info, statErr := os.Lstat(path)
		if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || info.IsDir() != wantDir) {
			if err = os.RemoveAll(path); err != nil { // a link goes, not what it points to
				return
			}
			statErr = fs.ErrNotExist
		}
		switch {
		case errors.Is(statErr, fs.ErrNotExist) && wantDir:
			err = os.Mkdir(path, 0o755)
		case errors.Is(statErr, fs.ErrNotExist):
			err = os.WriteFile(path, nil, 0o644)
		default:
			err = statErr
		}
		if err != nil {
			return
		}
	}
	return
}

// claudeSettings is the settings.json of the Agent: the Project's from the
// base branch, with every effective plugin enabled (ADR-0009).
func claudeSettings(project []byte, plugins []string) (data []byte, err error) {
	settings := map[string]json.RawMessage{}
	if len(project) > 0 {
		if err = json.Unmarshal(project, &settings); err != nil {
			err = fmt.Errorf("parsing %s: %w", settingsPath, err)
			return
		}
	}
	enabled := map[string]bool{}
	if raw, ok := settings["enabledPlugins"]; ok {
		if err = json.Unmarshal(raw, &enabled); err != nil {
			err = fmt.Errorf("parsing enabledPlugins in %s: %w", settingsPath, err)
			return
		}
	}
	for _, id := range plugins {
		enabled[id] = true
	}
	if len(enabled) > 0 {
		if settings["enabledPlugins"], err = json.Marshal(enabled); err != nil {
			return
		}
	}
	data, err = json.MarshalIndent(settings, "", "  ")
	return
}
