package chat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/run"
	"github.com/graf0/afk/stack"
)

func TestValidateName(t *testing.T) {
	for _, name := range []string{"plan", "a", "x-1", "0abc", strings.Repeat("a", 32)} {
		if err := ValidateName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", "-x", "x-", "A", "a b", "a_b", "a/b", "..", strings.Repeat("a", 33), "ls", "shell", "rm"} {
		if err := ValidateName(name); err == nil {
			t.Errorf("%q: want an error", name)
		}
	}
}

func TestClaudeProjectKey(t *testing.T) {
	if got := claudeProjectKey("/home/u/.local/state/afk/o-r/chats/x/repo"); got != "-home-u--local-state-afk-o-r-chats-x-repo" {
		t.Errorf("key = %q", got)
	}
}

func TestSystemPrompt(t *testing.T) {
	got := systemPrompt("plan", "main", "/s/repo")
	for _, want := range []string{`"plan"`, "origin/main", "chat-plan", "afk-forge", "pr create", "stack logs", "stack reseed", "afk-forge vm", "vm release", "ready-for-agent", "/s/repo"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestCommand(t *testing.T) {
	newOne := &chat{Options: Options{Name: "plan", Base: run.Base{Branch: "main"}}, dir: "/s/chats/plan", created: true}
	oldOne := &chat{Options: Options{Name: "plan", Base: run.Base{Branch: "main", Config: config.Config{Agent: config.Agent{Model: "opus"}}}}, dir: "/s/chats/plan"}
	withArgs := &chat{Options: Options{Name: "plan", Args: []string{"--resume", "a b"}, AgentBinary: "/bin/fake"}, dir: "/s/chats/plan"}

	got := newOne.command()
	for _, want := range []string{`export PATH="$HOME/bin:$PATH"; exec "$HOME/bin/claude" '--dangerously-skip-permissions'`, "skipDangerousModePermissionPrompt", "--append-system-prompt"} {
		if !strings.Contains(got, want) {
			t.Errorf("new chat: command lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "continue") || strings.Contains(got, "--model") {
		t.Errorf("new chat without a model continues nothing:\n%s", got)
	}

	// An old Chat continues, but only when Claude Code has a conversation for the clone.
	got = oldOne.command()
	for _, want := range []string{"'--model' 'opus'", "resume=--continue", "projects/-s-chats-plan-repo/*.jsonl", "$resume"} {
		if !strings.Contains(got, want) {
			t.Errorf("old chat: command lacks %q:\n%s", want, got)
		}
	}

	// Args after -- replace --continue.
	got = withArgs.command()
	if !strings.Contains(got, "'--resume' 'a b'") || strings.Contains(got, "continue") || !strings.Contains(got, "exec '/bin/fake'") {
		t.Errorf("args: command =\n%s", got)
	}
}

func TestSandboxMounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := &chat{
		Options:  Options{Name: "plan", AgentBinary: "/bin/fake", AfkBinary: "/usr/bin/afk-host", ClaudeToken: "t"},
		stateDir: "/s/o-r", dir: "/s/o-r/chats/plan", socket: "/run/user/1/afk/ab/afk.sock",
	}
	view, err := c.sandbox()
	if err != nil {
		t.Fatal(err)
	}
	if view.Files["settings.json"] == nil {
		t.Error("no settings.json for the Agent")
	}
	got := map[string]stack.Mount{}
	for _, m := range view.Mounts {
		got[m.Target] = m
	}
	if m := got[filepath.Join(home, "bin", "afk-forge")]; m.Source != "/usr/bin/afk-host" || !m.ReadOnly {
		t.Errorf("afk-forge = %+v, want the host's afk, read-only", m)
	}
	if m := got["/run/afk"]; m.Source != "/run/user/1/afk/ab" || m.ReadOnly || !m.Dir {
		t.Errorf("channel = %+v, want the directory of the Chat's socket (a file would pin an old inode)", m)
	}
	memory := filepath.Join(home, ".claude", "projects", "-s-o-r-chats-plan-repo", "memory")
	if m := got[memory]; m.Source != "/s/o-r/claude-memory" || m.ReadOnly || !m.Dir {
		t.Errorf("memory = %+v, want the Project's directory under the key of the clone", m)
	}
	if m := got[filepath.Join(home, ".claude", "settings.json")]; !m.ReadOnly {
		t.Errorf("settings = %+v, want read-only", m)
	}
	if _, ok := got[filepath.Join(home, "bin", "claude")]; ok {
		t.Error("the host's claude is mounted though AgentBinary is set")
	}
	env := c.env()
	if env[SocketEnv] != ForgeSocket || env["CLAUDE_CODE_OAUTH_TOKEN"] != "t" {
		t.Errorf("env = %v", env)
	}
}

// --- lifecycle, with a Stack that only records ---

// fakeStack records what the Chat does with its Stack. onAttach is the session.
type fakeStack struct {
	calls    []string
	upErr    error
	onAttach func(ctx context.Context, command string) (err error)
	onDown   func()
	cleanup  []error // ctx.Err() of the calls that end the Stack
}

func (f *fakeStack) Up(context.Context) (err error) { f.calls = append(f.calls, "Up"); return f.upErr }
func (f *fakeStack) Setup(context.Context) (err error) {
	f.calls = append(f.calls, "Setup")
	return
}
func (f *fakeStack) Exec(_ context.Context, command string, _ io.Writer) (err error) {
	f.calls = append(f.calls, "Exec "+command)
	return
}
func (f *fakeStack) Container(context.Context) (name string, err error) {
	f.calls = append(f.calls, "Container")
	return "afk_o-r_chat_plan-afk-1", nil
}
func (f *fakeStack) Protected() (paths []string) { return nil }
func (f *fakeStack) Attach(ctx context.Context, command string, _ *os.File, _, _ io.Writer) (err error) {
	f.calls = append(f.calls, "Attach")
	if f.onAttach != nil {
		err = f.onAttach(ctx, command)
	}
	return
}
func (f *fakeStack) Down(ctx context.Context) (err error) {
	f.calls = append(f.calls, "Down")
	f.cleanup = append(f.cleanup, ctx.Err())
	if f.onDown != nil {
		f.onDown()
	}
	return
}
func (f *fakeStack) Remove(ctx context.Context) (err error) {
	f.calls = append(f.calls, "Remove")
	f.cleanup = append(f.cleanup, ctx.Err())
	return
}

func (f *fakeStack) Services(context.Context) (services []string, err error)  { return }
func (f *fakeStack) Logs(context.Context, string, int, io.Writer) (err error) { return }
func (f *fakeStack) Restart(context.Context, string, io.Writer) (err error)   { return }
func (f *fakeStack) Reseed(context.Context, io.Writer) (err error)            { return }

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// lifecycle is a Chat of a user repository with one commit and a remote with
// the branch chat-taken, with a fake Stack to run it in.
type lifecycle struct {
	opts  Options
	stack *fakeStack
	out   *bytes.Buffer
}

func newLifecycle(t *testing.T) (l *lifecycle) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	runtimeDir, err := os.MkdirTemp("", "afk") // short: a socket path is
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	root := t.TempDir()
	user, remoteDir := filepath.Join(root, "user"), filepath.Join(root, "origin.git")
	for _, dir := range []string{user, remoteDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, remoteDir, "init", "-q", "--bare", "-b", "main")
	gitIn(t, user, "init", "-q", "-b", "main")
	gitIn(t, user, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, user, "branch", "chat-taken")
	gitIn(t, user, "push", "-q", remoteDir, "chat-taken")
	out, err := exec.Command("git", "-C", user, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	l = &lifecycle{stack: &fakeStack{}, out: &bytes.Buffer{}}
	l.opts = Options{
		Base: run.Base{
			Repo: user, Branch: "main", Commit: strings.TrimSpace(string(out)),
			Config: config.Config{Forge: config.Forge{Repo: "o/r"}},
		},
		Name: "plan", ClaudeToken: "fake", StateDir: filepath.Join(root, "state"),
		Forge:       fakeForge{remote: forge.Remote{Repo: "o/r", URL: remoteDir}},
		AgentBinary: "/bin/fake", AfkBinary: os.Args[0], Out: l.out,
	}
	return
}

// enter is Run without the real Stack.
func (l *lifecycle) enter(ctx context.Context) (c *chat, err error) {
	if c, err = newChat(ctx, l.opts); err != nil {
		return
	}
	view, err := c.sandbox()
	if err != nil {
		return
	}
	unlock, err := c.begin()
	if err != nil {
		return
	}
	defer unlock()
	err = c.enter(ctx, l.stack, view)
	return
}

func (l *lifecycle) exists(t *testing.T, path string) (ok bool) {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

func TestEnterNewChat(t *testing.T) {
	l := newLifecycle(t)
	var command string
	var socketDuring bool
	var branchBase string
	l.stack.onAttach = func(_ context.Context, cmd string) (err error) {
		command = cmd
		socket, _ := socketPath(filepath.Join(l.opts.StateDir, "o-r", "chats", "plan"))
		_, statErr := os.Stat(socket)
		socketDuring = statErr == nil && running(filepath.Join(l.opts.StateDir, "o-r", "chats", "plan"))
		return
	}
	c, err := l.enter(t.Context())
	if err != nil {
		t.Fatalf("%v\n%s", err, l.out)
	}
	if len(l.stack.calls) != 6 || !strings.HasPrefix(l.stack.calls[2], "Exec git checkout -q -B 'chat-plan' ") ||
		!slices.Equal(slices.Concat(l.stack.calls[:2], l.stack.calls[3:]), []string{"Up", "Container", "Setup", "Attach", "Down"}) {
		t.Errorf("calls = %q, want Up, Container, checkout in the Sandbox, Setup, Attach, Down", l.stack.calls)
	}
	if !socketDuring {
		t.Error("no channel socket while claude runs")
	}
	if l.exists(t, c.socket) {
		t.Error("the socket stays after the session")
	}
	if !l.exists(t, filepath.Join(c.tree(), ".git")) || strings.Contains(command, "continue") {
		t.Errorf("clone present %v; command = %s", l.exists(t, filepath.Join(c.tree(), ".git")), command)
	}
	if branchBase, err = c.quarantine.Base(t.Context(), "chat-plan"); err != nil || branchBase != l.opts.Base.Commit {
		t.Errorf("base in the Quarantine repo = %q, %v", branchBase, err)
	}
	for _, want := range []string{"Sandbox container afk_o-r_chat_plan-afk-1", "state stays in " + c.dir} {
		if !strings.Contains(l.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, l.out)
		}
	}
	// The clone is only inited and fetched on the host: no checkout (ADR-0002).
	if l.exists(t, filepath.Join(c.tree(), "base.txt")) {
		t.Error("files checked out on the host")
	}
}

func TestEnterReturningChat(t *testing.T) {
	l := newLifecycle(t)
	if _, err := l.enter(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{"continues": {nil, "resume=--continue"}, "args replace --continue": {[]string{"--resume"}, "'--resume'"}} {
		t.Run(name, func(t *testing.T) {
			l.stack = &fakeStack{}
			l.opts.Args = tc.args
			var command string
			l.stack.onAttach = func(_ context.Context, cmd string) (err error) { command = cmd; return }
			if _, err := l.enter(t.Context()); err != nil {
				t.Fatalf("%v\n%s", err, l.out)
			}
			// Down first: a Sandbox of a killed afk may still run in the Agent's home.
			if !slices.Equal(l.stack.calls, []string{"Down", "Up", "Container", "Setup", "Attach", "Down"}) {
				t.Errorf("calls = %q: a returning Chat is brought down first and not checked out again", l.stack.calls)
			}
			if !strings.Contains(command, tc.want) || (tc.args != nil) == strings.Contains(command, "continue") {
				t.Errorf("command = %s, want %q", command, tc.want)
			}
		})
	}
}

func TestEnterReturningWithoutClone(t *testing.T) {
	l := newLifecycle(t)
	c, err := newChat(t.Context(), l.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(c.dir, 0o700); err != nil { // a start that died before the clone
		t.Fatal(err)
	}
	if _, err = l.enter(t.Context()); err == nil || !strings.Contains(err.Error(), "afk chat rm plan") {
		t.Errorf("err = %v, want a pointer to `afk chat rm`", err)
	}
	if slices.Contains(l.stack.calls, "Up") || !l.exists(t, c.dir) {
		t.Errorf("calls = %q; state kept = %v", l.stack.calls, l.exists(t, c.dir))
	}
}

func TestEnterUpFails(t *testing.T) {
	boom := errors.New("boom")
	l := newLifecycle(t)
	l.stack.upErr = boom
	c, err := newChat(t.Context(), l.opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.enter(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if !slices.Equal(l.stack.calls[len(l.stack.calls)-1:], []string{"Remove"}) {
		t.Errorf("calls = %q: a new Chat that never started is removed", l.stack.calls)
	}
	if _, baseErr := c.quarantine.Base(t.Context(), "chat-plan"); baseErr == nil {
		t.Error("the base of the failed Chat stays in the Quarantine repo")
	}

	// An old Chat is the Agent's: its state stays, the Stack goes down.
	l = newLifecycle(t)
	if _, err = l.enter(t.Context()); err != nil {
		t.Fatal(err)
	}
	l.stack = &fakeStack{upErr: boom}
	if _, err = l.enter(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if !slices.Equal(l.stack.calls, []string{"Down", "Up", "Down"}) {
		t.Errorf("calls = %q, want Down, Up, Down", l.stack.calls)
	}
}

func TestEnterExposePortBusy(t *testing.T) {
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	port := strconv.Itoa(other.Addr().(*net.TCPAddr).Port)

	l := newLifecycle(t)
	l.opts.Expose = true
	l.opts.Base.Config.Expose = map[string]string{"web": port + ":80"}
	if _, err = l.enter(t.Context()); err == nil || !strings.Contains(err.Error(), port) {
		t.Fatalf("err = %v, want the busy port", err)
	}
	if slices.Contains(l.stack.calls, "Up") {
		t.Errorf("calls = %q: the Stack must not come up", l.stack.calls)
	}
}

func TestEnterExposeHoldsPortsUntilDown(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(free.Addr().(*net.TCPAddr).Port)
	_ = free.Close()

	l := newLifecycle(t)
	l.opts.Expose = true
	l.opts.Base.Config.Expose = map[string]string{"web": port + ":80"}
	other := newClaims(filepath.Join(l.opts.StateDir, exposeLocksName), claimPort, "o-r/other")
	var ownerAtDown string
	l.stack.onDown = func() { ownerAtDown, _ = other.ownerOf(port) }
	if _, err = l.enter(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The Stack publishes the ports until it is down: so long they are claimed.
	if ownerAtDown != "o-r/plan" {
		t.Errorf("owner of port %s at Down = %q, want o-r/plan", port, ownerAtDown)
	}
	if owner, held := other.ownerOf(port); held {
		t.Errorf("port %s still held by %q after the Chat", port, owner)
	}
}

func TestEnterExistingBranch(t *testing.T) {
	l := newLifecycle(t)
	l.opts.Name = "taken" // chat-taken is on the Forge
	if _, err := l.enter(t.Context()); err == nil || !strings.Contains(err.Error(), "branch chat-taken already exists") {
		t.Errorf("err = %v", err)
	}
	if slices.Contains(l.stack.calls, "Up") || l.stack.calls[len(l.stack.calls)-1] != "Remove" {
		t.Errorf("calls = %q", l.stack.calls)
	}
}

func TestEnterSessionEnds(t *testing.T) {
	t.Run("claude fails: the Stack goes down, the state stays", func(t *testing.T) {
		l := newLifecycle(t)
		l.stack.onAttach = func(context.Context, string) (err error) { return errors.New("exit status 1") }
		c, err := l.enter(t.Context())
		if err == nil || !strings.Contains(err.Error(), "claude in chat plan") {
			t.Errorf("err = %v", err)
		}
		if last := l.stack.calls[len(l.stack.calls)-1]; last != "Down" || !l.exists(t, c.tree()) {
			t.Errorf("calls = %q", l.stack.calls)
		}
	})

	t.Run("--rm removes the Chat after claude", func(t *testing.T) {
		l := newLifecycle(t)
		l.opts.Remove = true
		c, err := l.enter(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := l.stack.calls[len(l.stack.calls)-2:]; !slices.Equal(got, []string{"Attach", "Remove"}) {
			t.Errorf("calls = %q, want Remove right after Attach", l.stack.calls)
		}
		if _, baseErr := c.quarantine.Base(t.Context(), "chat-plan"); baseErr == nil {
			t.Error("the Chat's refs stay in the Quarantine repo")
		}
		if !strings.Contains(l.out.String(), "Chat plan removed") {
			t.Errorf("output:\n%s", l.out)
		}
	})

	t.Run("a signal ends the session: Down with a live context, even with --rm", func(t *testing.T) {
		l := newLifecycle(t)
		l.opts.Remove = true
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		l.stack.onAttach = func(ctx context.Context, _ string) (err error) {
			cancel()
			<-ctx.Done()
			return errors.New("signal: killed")
		}
		c, err := l.enter(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want the interruption", err)
		}
		if last := l.stack.calls[len(l.stack.calls)-1]; last != "Down" || len(l.stack.cleanup) != 1 || l.stack.cleanup[0] != nil {
			t.Errorf("calls = %q, cleanup ctx errors = %v: Down must run on a context that is not cancelled", l.stack.calls, l.stack.cleanup)
		}
		if !l.exists(t, c.tree()) {
			t.Error("the clone of an interrupted Chat is gone")
		}
	})
}

// One process per Chat: a second one is refused while the first holds the lock.
func TestEnterChatInUse(t *testing.T) {
	l := newLifecycle(t)
	l.stack.onAttach = func(ctx context.Context, _ string) (err error) {
		second := newLifecycleFrom(l)
		_, err = second.enter(ctx)
		if err == nil || !strings.Contains(err.Error(), "in use by another afk process") {
			t.Errorf("second process: err = %v", err)
		}
		err = nil
		return
	}
	if _, err := l.enter(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, l.out)
	}
}

func newLifecycleFrom(l *lifecycle) (second *lifecycle) {
	opts := l.opts
	opts.Out = &bytes.Buffer{}
	return &lifecycle{opts: opts, stack: &fakeStack{}, out: &bytes.Buffer{}}
}

func TestSandboxVMAccess(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := t.TempDir()
	newChat := func(vms []config.VM) *chat {
		var base run.Base
		base.Config.VM = vms
		return &chat{
			Options:  Options{Name: "plan", AgentBinary: "/bin/fake", AfkBinary: "/usr/bin/afk-host", Base: base},
			stateDir: state, dir: filepath.Join(state, "chats", "plan"),
		}
	}
	mounts := func(c *chat) (targets map[string]stack.Mount, files map[string][]byte) {
		view, err := c.sandbox()
		if err != nil {
			t.Fatal(err)
		}
		targets = map[string]stack.Mount{}
		for _, m := range view.Mounts {
			targets[m.Target] = m
		}
		return targets, view.Files
	}
	key := filepath.Join(home, ".ssh", sshKeyTarget)

	got, files := mounts(newChat(nil))
	if _, ok := got[key]; ok || files[sshConfigName] != nil {
		t.Error("a Project without [[vm]] gets the ssh key or config")
	}
	got, files = mounts(newChat([]config.VM{{Name: "win11", User: "admin", OS: "windows"}}))
	if m := got[key]; m.Source != VMKeyPath(state) || !m.ReadOnly {
		t.Errorf("key mount = %+v, want the Project's key, read-only", m)
	}
	if m := got[filepath.Join(home, ".ssh", "config")]; !m.ReadOnly || !strings.HasSuffix(m.Source, "/context/ssh_config") {
		t.Errorf("config mount = %+v", m)
	}
	if !strings.Contains(string(files[sshConfigName]), "Host win11") {
		t.Errorf("ssh config = %q", files[sshConfigName])
	}
}
