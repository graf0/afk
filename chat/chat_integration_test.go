//go:build integration

package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/run"
	"github.com/graf0/afk/stack"
)

// Run manually on a host with rootless podman, its socket and docker-compose
// v2 (README, "Wymagania hosta"):
//
//	go test -tags integration -run Integration -v ./chat/
//
// The fixture needs a local base image with bash, git and getent (default
// docker.io/library/golang:1.27; AFK_TEST_IMAGE overrides it). The Agent is a
// fake script put in the place of the host's claude: no Claude token and no
// network are needed. afk-forge is the real afk, built from cmd/afk.

const (
	defaultTestImage = "docker.io/library/golang:1.27"
	fakeToken        = "fake-42"
)

const (
	fixtureCompose = `services:
  svc:
    image: %s
    command: ["sleep", "infinity"]
`
	fixtureComposeAfk = `services:
  afk:
    build:
      context: .afk
    depends_on: [svc]
    volumes:
      - cache:/cache
  afk-setup:
    image: %s
    command: sh -c 'test -f /t/hello.txt && touch /t/setup-saw-clone'
    volumes:
      - ${AFK_TREE}:/t
volumes:
  cache:
    name: ${AFK_CACHE}-cache
`
	fixtureConfig = `[forge]
kind = "github"
repo = "afkt/%s"
[gate]
cmd = "true"
`
)

// fakeClaude checks what a Chat gives the Agent, then behaves as the MODE-x
// argument (after --) says.
const fakeClaude = `#!/bin/bash
set -eu
fail() { echo "FAKE: $*" >&2; exit 1; }
trap 'echo "FAKE: line $LINENO failed: $BASH_COMMAND" >&2' ERR
mode=default
skip=
for arg in "$@"; do
	if [ -n "$skip" ]; then skip=; echo "FAKE-ARG: <value>"; continue; fi
	case "$arg" in
		MODE-*) mode=${arg#MODE-} ;;
		--append-system-prompt|--settings) skip=1 ;;
	esac
	echo "FAKE-ARG: $arg"
done
name=$(basename "$(dirname "$PWD")")
key=$(printf %s "$PWD" | sed 's/[^a-zA-Z0-9]/-/g')
projects="$HOME/.claude/projects/$key"
[ "$CLAUDE_CODE_OAUTH_TOKEN" = "fake-$((6 * 7))" ] || fail "token not in the environment"
[ "$(git rev-parse --abbrev-ref HEAD)" = "chat-$name" ] || fail "wrong branch"
git rev-parse -q --verify origin/main >/dev/null || fail "no origin/main"
[ -f hello.txt ] || fail "the clone is not checked out"
[ -f setup-saw-clone ] || fail "afk-setup did not see the clone"
[ "$GIT_AUTHOR_NAME" = "afk agent" ] || fail "no git identity"
if grep -rqsF "$CLAUDE_CODE_OAUTH_TOKEN" "$PWD" "$HOME" /tmp; then fail "token on disk"; fi
[ "$(command -v afk-forge)" = "$HOME/bin/afk-forge" ] || fail "afk-forge is not on the PATH: $(command -v afk-forge)"
[ ! -w "$HOME/bin/afk-forge" ] || fail "afk-forge is writable"
[ ! -w "$HOME/bin/claude" ] || fail "claude is writable"
[ ! -w "$HOME/.claude/settings.json" ] || fail "settings.json is writable"
[ "$AFK_SOCKET" = /run/afk/afk.sock ] && [ -S "$AFK_SOCKET" ] || fail "no channel socket"
socks=$(find / \( -path /proc -o -path /sys \) -prune -o -name '*.sock' -print 2>/dev/null || true)
[ "$socks" = /run/afk/afk.sock ] || fail "sockets in the Sandbox: $socks"
[ -z "${DOCKER_HOST:-}${CONTAINER_HOST:-}" ] || fail "podman socket in the environment"
getent hosts svc >/dev/null || fail "service svc of the Stack not reachable"
[ "$(afk-forge issue list --label bug)" = "#1  [bug]  Itest ticket" ] || fail "afk-forge issue list"
[ -d "$projects/memory" ] || fail "no memory directory under $projects"
touch "$HOME/probe" && rm "$HOME/probe" || fail "home is not writable"
case "$mode" in
	read-memory)
		[ "$(cat "$projects/memory/note-plan.md")" = remembered-by-plan ] || fail "memory of the other Chat missing"
		;;
	sleep)
		echo SLEEPING
		exec sleep 600
		;;
	exit3)
		exit 3
		;;
	*)
		echo "remembered-by-$name" > "$projects/memory/note-$name.md"
		touch "$projects/session.jsonl" # a conversation to continue
		afk-forge issue create --title "Idea" --label ready-for-agent
		echo first > session-marker
		echo work > work.txt && git add work.txt && git commit -q --allow-empty -m "work in the chat"
		;;
esac
`

// syncBuffer is a bytes.Buffer the Sandbox's stderr and the Chat can both write.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() (s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestIntegrationChat(t *testing.T) {
	testImage := os.Getenv("AFK_TEST_IMAGE")
	if testImage == "" {
		testImage = defaultTestImage
	}
	if err := exec.Command("podman", "image", "exists", testImage).Run(); err != nil {
		t.Skipf("test image %s is not available locally: %v", testImage, err)
	}
	name := fmt.Sprintf("chat%d", time.Now().UnixNano()%1_000_000_000)
	project := "afkt-" + name
	stateDir := t.TempDir()
	t.Cleanup(func() { cleanProject(stateDir, project) })

	// The host's claude, as the Chat finds it: a symlink to the real file.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude-2.0.0"), []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("claude-2.0.0", filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The real afk, for afk-forge: the test binary is no afk.
	afk := filepath.Join(t.TempDir(), "afk")
	build := exec.Command("go", "build", "-o", afk, "../cmd/afk")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building afk: %v\n%s", err, out)
	}

	isolateGit(t)
	root := t.TempDir()
	user, remoteDir := filepath.Join(root, "user"), filepath.Join(root, "origin.git")
	sh(t, root, `
git init -q --bare -b main origin.git
git init -q -b main user
cd user
echo hello > hello.txt
cat > .afk.toml <<'EOF'
`+fmt.Sprintf(fixtureConfig, name)+`EOF
git add -A
git commit -qm config
git remote add origin ../origin.git
git push -q origin main`)
	withoutStack, err := run.ReadBase(t.Context(), user) // before the Project has a Stack
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"compose.yml":     fmt.Sprintf(fixtureCompose, testImage),
		"compose.afk.yml": fmt.Sprintf(fixtureComposeAfk, testImage),
		".afk/Dockerfile": "FROM " + testImage + "\n",
	}
	for path, content := range files {
		if err = os.MkdirAll(filepath.Dir(filepath.Join(user, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(user, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sh(t, user, "git add -A && git commit -qm stack && git push -q origin main")
	base, err := run.ReadBase(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}

	var created []forge.Ticket
	var comments, edits []string
	github := fakeForge{
		tickets: []forge.Ticket{{Number: 1, Title: "Itest ticket", Labels: []string{"bug"}}},
		created: &created, comments: &comments, edits: &edits,
		remote: forge.Remote{Repo: "afkt/" + name, URL: remoteDir},
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	options := func(chatName string, out *syncBuffer) Options {
		return Options{
			Base: base, Name: chatName, ClaudeToken: fakeToken, StateDir: stateDir, Forge: github,
			AfkBinary: afk, In: devNull, Out: out, Err: out,
		}
	}
	enter := func(ctx context.Context, t *testing.T, opts Options) (out string, err error) {
		t.Helper()
		start := time.Now()
		err = Run(ctx, opts)
		out = opts.Out.(*syncBuffer).String()
		t.Logf("Run of %s took %s, err = %v, output:\n%s", opts.Name, time.Since(start).Round(time.Second), err, out)
		return
	}
	stackDir := func(chatName string) (dir string) { return stack.Dir(stateDir, project, stack.Chat, chatName) }
	compose := func(chatName string) (label string) {
		return fmt.Sprintf("label=com.docker.compose.project=afk_%s_chat_%s", project, chatName)
	}
	containers := func(chatName string) (ids string) {
		return podman(t, "ps", "-a", "--quiet", "--filter", compose(chatName))
	}
	volumes := func(chatName string) (ids string) {
		return podman(t, "volume", "ls", "--quiet", "--filter", compose(chatName))
	}
	exists := func(path string) (ok bool) {
		_, err := os.Stat(path)
		return err == nil
	}
	quarantine := filepath.Join(stateDir, project, run.QuarantineName)
	// listed is the state `afk chat ls` shows for the Chat chatName, "" without a line.
	listed := func(chatName string) (state string) {
		var buf bytes.Buffer
		if err := List(&buf, stateDir, project, time.Now()); err != nil {
			t.Fatal(err)
		}
		for line := range strings.Lines(buf.String()) {
			if fields := strings.Fields(line); len(fields) == 3 && fields[0] == chatName {
				return fields[1]
			}
		}
		return ""
	}

	t.Run("no compose.afk.yml on the base branch: the template is for the CLI to offer", func(t *testing.T) {
		opts := options("tpl", &syncBuffer{})
		opts.Base = withoutStack
		_, err := enter(t.Context(), t, opts)
		if !errors.Is(err, stack.ErrNoComposeAfk) {
			t.Errorf("err = %v, want stack.ErrNoComposeAfk", err)
		}
		if exists(stackDir("tpl")) || exists(filepath.Join(quarantine, "refs", "afk", "base", "chat-tpl")) {
			t.Error("a Chat that never started stays")
		}
	})

	t.Run("new Chat: the Agent gets its Sandbox, the Stack ends with claude, the state stays", func(t *testing.T) {
		out, err := enter(t.Context(), t, options("plan", &syncBuffer{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Sandbox container afk_" + project + "_chat_plan", "Stack of Chat plan is down", "--dangerously-skip-permissions"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q", want)
			}
		}
		if strings.Contains(out, "--continue") {
			t.Error("a new Chat continues nothing")
		}
		if containers("plan") != "" {
			t.Errorf("containers after claude ended: %q", containers("plan"))
		}
		if !exists(filepath.Join(stackDir("plan"), "repo", ".git")) || !exists(filepath.Join(stackDir("plan"), "home")) {
			t.Error("clone or home gone")
		}
		if got := sh(t, filepath.Join(stackDir("plan"), "repo"), "git log -1 --format=%s chat-plan"); got != "work in the chat" {
			t.Errorf("commit on chat-plan = %q", got)
		}
		if volumes("plan") == "" {
			t.Error("the cache volume of the Chat is gone with its Stack")
		}
		// The ticket tool refused ready-for-agent.
		if len(created) != 1 || strings.Join(created[0].Labels, ",") != "needs-triage" {
			t.Errorf("created = %+v, want one issue with needs-triage", created)
		}
		if exists(filepath.Join(stackDir("plan"), "afk.sock")) {
			t.Error("socket in the state of the Chat")
		}
		if out, _ := exec.Command("grep", "-rlF", fakeToken, stateDir).Output(); len(out) > 0 {
			t.Errorf("token in the state directory: %s", out)
		}
		if got := sh(t, "/", "git --git-dir="+quarantine+" rev-parse refs/afk/base/chat-plan"); got != base.Commit {
			t.Errorf("base in the Quarantine repo = %q", got)
		}
		if got := listed("plan"); got != "stopped" {
			t.Errorf("ls shows plan as %q", got)
		}
	})

	t.Run("returning: --continue, the work is there", func(t *testing.T) {
		out, err := enter(t.Context(), t, options("plan", &syncBuffer{}))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "FAKE-ARG: --continue") {
			t.Error("a Chat that exists gets --continue")
		}
		if got := sh(t, filepath.Join(stackDir("plan"), "repo"), "cat session-marker"); got != "first" {
			t.Errorf("session-marker = %q", got)
		}
		if containers("plan") != "" {
			t.Error("Stack up after the second session")
		}
	})

	t.Run("args after -- replace --continue", func(t *testing.T) {
		opts := options("plan", &syncBuffer{})
		opts.Args = []string{"--resume", "abc"}
		out, err := enter(t.Context(), t, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "FAKE-ARG: --resume\nFAKE-ARG: abc") || strings.Contains(out, "--continue") {
			t.Errorf("output:\n%s", out)
		}
	})

	t.Run("memory of the Project is shared by its Chats", func(t *testing.T) {
		opts := options("second", &syncBuffer{})
		opts.Args = []string{"MODE-read-memory"}
		if _, err := enter(t.Context(), t, opts); err != nil {
			t.Fatal(err)
		}
		memory := filepath.Join(stateDir, project, memoryName)
		if got := sh(t, memory, "cat note-plan.md"); got != "remembered-by-plan" {
			t.Errorf("note of plan = %q", got)
		}
		// Not in the home of either Chat: it is a mount.
		if out, _ := exec.Command("sh", "-c", "ls "+stackDir("plan")+"/home/.claude/projects/*/memory | head -3").CombinedOutput(); strings.Contains(string(out), "note-plan.md") {
			t.Errorf("the memory is a copy in the home of the Chat: %s", out)
		}
	})

	t.Run("running Chat: ls, shell, one process, the socket only in its Sandbox, then a signal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		opts := options("live", &syncBuffer{})
		opts.Args = []string{"MODE-sleep"}
		in, inWrite, err := os.Pipe() // an open terminal: never at EOF
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = in.Close(); _ = inWrite.Close() }()
		opts.In = in
		done := make(chan error, 1)
		go func() { done <- Run(ctx, opts) }()
		out := opts.Out.(*syncBuffer)
		for i := 0; !strings.Contains(out.String(), "SLEEPING"); i++ {
			select {
			case err := <-done:
				t.Fatalf("Run ended early: %v\n%s", err, out.String())
			case <-time.After(500 * time.Millisecond):
			}
			if i > 240 {
				t.Fatalf("no session after two minutes:\n%s", out.String())
			}
		}
		if got := listed("live"); got != "running" {
			t.Errorf("ls shows live as %q, want running", got)
		}
		// One process per Chat.
		if _, err := enter(t.Context(), t, options("live", &syncBuffer{})); err == nil || !strings.Contains(err.Error(), "in use by another afk process") {
			t.Errorf("second process: err = %v", err)
		}
		if err := Remove(t.Context(), options("live", &syncBuffer{})); err == nil || !strings.Contains(err.Error(), "in use by another afk process") {
			t.Errorf("rm of a running Chat: err = %v", err)
		}
		// A shell in the running Chat's Sandbox has the channel.
		shellIn, shellInWrite, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = shellInWrite.WriteString(`echo "in:$PWD"; afk-forge issue view 1 | head -1; exit 0` + "\n")
		_ = shellInWrite.Close()
		shellOpts := options("live", &syncBuffer{})
		shellOpts.In = shellIn
		if err = Shell(t.Context(), shellOpts); err != nil {
			t.Fatalf("shell: %v\n%s", err, shellOpts.Out.(*syncBuffer).String())
		}
		if got := shellOpts.Out.(*syncBuffer).String(); !strings.Contains(got, "in:"+filepath.Join(stackDir("live"), "repo")) || !strings.Contains(got, "# #1 Itest ticket") {
			t.Errorf("shell output:\n%s", got)
		}
		// Not in the other services of the Stack.
		svc := podman(t, "ps", "--quiet", "--filter", compose("live"), "--filter", "label=com.docker.compose.service=svc")
		if svc == "" {
			t.Fatal("service svc is not running")
		}
		if out, err := exec.Command("podman", "exec", svc, "sh", "-c", "ls /run/afk 2>&1; find / -xdev -name '*.sock' 2>/dev/null").CombinedOutput(); err == nil && strings.Contains(string(out), "afk.sock") {
			t.Errorf("service svc sees the channel socket: %s", out)
		}
		// A signal ends the session: the Stack goes down, the state stays.
		cancel()
		select {
		case err = <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want the interruption", err)
			}
		case <-time.After(2 * time.Minute):
			t.Fatal("Run did not end after the signal")
		}
		if containers("live") != "" {
			t.Errorf("containers after the signal: %q", containers("live"))
		}
		if !exists(filepath.Join(stackDir("live"), "repo", ".git")) {
			t.Error("state of the interrupted Chat is gone")
		}
		if got := listed("live"); got != "stopped" {
			t.Errorf("ls shows live as %q, want stopped", got)
		}
		// And a shell has nothing to enter.
		if err = Shell(t.Context(), shellOpts); err == nil || !strings.Contains(err.Error(), "not running") {
			t.Errorf("shell of a stopped Chat: err = %v", err)
		}
	})

	t.Run("claude fails: the Stack goes down, the Chat stays", func(t *testing.T) {
		opts := options("fails", &syncBuffer{})
		opts.Args = []string{"MODE-exit3"}
		_, err := enter(t.Context(), t, opts)
		if err == nil || !strings.Contains(err.Error(), "claude in chat fails") {
			t.Errorf("err = %v", err)
		}
		if containers("fails") != "" || !exists(filepath.Join(stackDir("fails"), "repo", ".git")) {
			t.Errorf("containers %q, state %v", containers("fails"), exists(stackDir("fails")))
		}
	})

	t.Run("--rm: nothing of the Chat is left", func(t *testing.T) {
		opts := options("tmp", &syncBuffer{})
		opts.Remove = true
		out, err := enter(t.Context(), t, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Chat tmp removed") {
			t.Error("output does not say it removed the Chat")
		}
		if exists(stackDir("tmp")) || containers("tmp") != "" || volumes("tmp") != "" {
			t.Errorf("left: state %v, containers %q, volumes %q", exists(stackDir("tmp")), containers("tmp"), volumes("tmp"))
		}
		if exists(filepath.Join(quarantine, "refs", "afk", "base", "chat-tmp")) {
			t.Error("refs in the Quarantine repo")
		}
	})

	t.Run("rm: clone, home, volumes and refs of a stopped Chat", func(t *testing.T) {
		var out syncBuffer
		if err := Remove(t.Context(), options("plan", &out)); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		if exists(stackDir("plan")) || containers("plan") != "" || volumes("plan") != "" {
			t.Errorf("left: state %v, containers %q, volumes %q", exists(stackDir("plan")), containers("plan"), volumes("plan"))
		}
		if exists(filepath.Join(quarantine, "refs", "afk", "base", "chat-plan")) {
			t.Error("refs in the Quarantine repo")
		}
		if err := Remove(t.Context(), options("plan", &out)); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("rm again: err = %v", err)
		}
		// The name is free again.
		if _, err := enter(t.Context(), t, options("plan", &syncBuffer{})); err != nil {
			t.Errorf("a Chat of a removed name: %v", err)
		}
	})
}

// --- helpers ---

func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(name, "t")
	}
	for _, name := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(name, "t@example.com")
	}
}

// sh runs script with bash in dir.
func sh(t *testing.T, dir, script string) (out string) {
	t.Helper()
	cmd := exec.Command("bash", "-euc", script)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, data)
	}
	return strings.TrimSpace(string(data))
}

func podman(t *testing.T, args ...string) (out string) {
	t.Helper()
	data, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

// cleanProject removes what a test left: the Stacks of its Chats, their
// volumes and the images it built.
func cleanProject(stateDir, project string) {
	ctx := context.Background()
	names, _ := stack.List(stateDir, project, stack.Chat)
	for _, name := range names {
		if st, err := stack.Open(stateDir, project, stack.Chat, name); err == nil {
			_ = st.Remove(ctx)
		}
	}
	volumes, _ := exec.Command("podman", "volume", "ls", "--quiet", "--filter", "name=afk_"+project+"_").Output()
	for _, volume := range strings.Fields(string(volumes)) {
		_ = exec.Command("podman", "volume", "rm", "--force", volume).Run()
	}
	images, _ := exec.Command("podman", "images", "--quiet", "--filter", "reference=localhost/afk/"+project+"/*").Output()
	for _, id := range strings.Fields(string(images)) {
		_ = exec.Command("podman", "rmi", "--force", id).Run()
	}
}
