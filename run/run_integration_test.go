//go:build integration

package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/stack"
)

// Run manually on a host with rootless podman, its socket and docker-compose
// v2 (README, "Wymagania hosta"):
//
//	go test -tags integration -run Integration -v ./run/
//
// The fixture needs a local base image with bash, git and getent (default
// docker.io/library/golang:1.27; AFK_TEST_IMAGE overrides it). The Agent is a
// fake script put in the place of the host's claude: no Claude token and no
// network are needed.

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
`
	fixtureConfig = `[forge]
kind = "github"
repo = "afkt/%s"
[gate]
cmd = "getent hosts svc >/dev/null && test -f result.txt"
[protect]
paths = ["secrets/key"]
`
)

// fakeAgent checks what a Run gives the Agent, then commits like one would.
// The ticket body selects the behaviour.
const fakeAgent = `#!/bin/bash
set -eu
prompt=$(cat)
fail() { echo "FAKE: $*" >&2; exit 1; }
[ "$CLAUDE_CODE_OAUTH_TOKEN" = "fake-$((6 * 7))" ] || fail "token not in the environment"
ticket=$(grep -o '/[^[:space:]]*/context/TICKET.md' <<<"$prompt" | head -1)
[ -n "$ticket" ] || fail "prompt without the ticket path"
[ -r "$ticket" ] || fail "ticket unreadable"
ctx=$(dirname "$ticket")
[ "$PWD" = "$(dirname "$ctx")/repo" ] || fail "not in the tree the prompt names: $PWD"
grep -qF "$PWD" <<<"$prompt" || fail "prompt without the clone path"
if grep -rqsF "$CLAUDE_CODE_OAUTH_TOKEN" "$PWD" "$HOME" "$ctx" /tmp; then fail "token on disk"; fi
if touch "$ctx/probe" 2>/dev/null; then fail "the context directory is writable"; fi
[ -r "$HOME/.claude/settings.json" ] || fail "no settings.json"
if ( : >> "$HOME/.claude/settings.json" ) 2>/dev/null; then fail "settings.json is writable"; fi
[ -r "$HOME/bin/claude" ] && ! [ -w "$HOME/bin/claude" ] || fail "claude is not mounted read-only"
[ ! -e "$HOME/.claude/projects" ] || fail "the memory of the projects is in the Run's home"
touch "$HOME/probe" && rm "$HOME/probe" || fail "home is not writable"
[[ "$(git rev-parse --abbrev-ref HEAD)" == ticket-[0-9]* ]] || fail "wrong branch"
git rev-parse -q --verify origin/main >/dev/null || fail "no origin/main"
[ "$GIT_AUTHOR_NAME" = "afk agent" ] || fail "no git identity"
[ -z "${DOCKER_HOST:-}${CONTAINER_HOST:-}" ] || fail "socket in the environment"
[ -z "$(find / \( -path /proc -o -path /sys \) -prune -o \( -name podman.sock -o -name docker.sock -o -name '*.sock' \) -print 2>/dev/null)" ] || fail "socket in the Sandbox"
getent hosts svc >/dev/null || fail "service svc of the Stack not reachable"
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"fake agent working"},{"type":"tool_use","name":"Bash","input":{"command":"git commit"}}]}}'
if grep -q 'code review' <<<"$prompt"; then
	echo '{"type":"result","subtype":"success","is_error":false,"structured_output":{"status":"done","reason":"Fake review.","summary":"Fine.","deviations":[],"findings_fixed":[],"findings_skipped":[]}}'
	exit 0
fi
if grep -q 'MODE: sleep' "$ticket"; then
	echo partial > partial.txt
	git add partial.txt
	git commit -qm partial
	sleep 600
fi
if grep -q 'MODE: resume' "$ticket"; then
	grep -q Wznawiasz <<<"$prompt" || fail "no resume note"
	[ -f partial.txt ] || fail "the earlier work is gone"
fi
if grep -q 'MODE: needs_info' "$ticket"; then
	echo '{"type":"result","subtype":"success","is_error":false,"structured_output":{"status":"needs_info","reason":"Unclear.","summary":"","deviations":[],"questions":["Which one?"]}}'
	exit 0
fi
if grep -q 'MODE: protected' "$ticket"; then
	echo '# changed' >> .afk.toml
else
	echo done > result.txt
	git add result.txt
fi
git commit -qam 'fake work' >&2
echo '{"type":"result","subtype":"success","is_error":false,"structured_output":{"status":"done","reason":"Fake.","summary":"Fake work.","deviations":[]}}'
`

// syncBuffer is a bytes.Buffer the Sandbox's stderr and the Run can both write.
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

func TestIntegrationExecute(t *testing.T) {
	testImage := os.Getenv("AFK_TEST_IMAGE")
	if testImage == "" {
		testImage = defaultTestImage
	}
	if err := exec.Command("podman", "image", "exists", testImage).Run(); err != nil {
		t.Skipf("test image %s is not available locally: %v", testImage, err)
	}
	name := fmt.Sprintf("run%d", time.Now().UnixNano()%1_000_000_000)
	project := "afkt-" + name
	paths := image.Paths{StateDir: t.TempDir()}
	t.Cleanup(func() { cleanProject(paths.StateDir, project) })

	// The host's claude, as the Run finds it: a symlink to the real file.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude-2.0.0"), []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("claude-2.0.0", filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	root, user, _ := userRepo(t)
	withoutStack, err := ReadBase(t.Context(), user) // before the Project has a Stack
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".afk.toml":       fmt.Sprintf(fixtureConfig, name),
		"compose.yml":     fmt.Sprintf(fixtureCompose, testImage),
		"compose.afk.yml": fixtureComposeAfk,
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
	base, err := ReadBase(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeGitHub{}
	github := fake.serve(t, filepath.Join(root, "origin.git"))
	execute := func(ctx context.Context, t *testing.T, number int, body string, resume *Leftover) (out string, err error) {
		t.Helper()
		fake.requests, fake.bodies = nil, nil
		var buf syncBuffer
		start := time.Now()
		err = Execute(ctx, Options{
			Base:        base,
			Ticket:      forge.Ticket{Number: number, Title: "Fake", Body: body},
			ClaudeToken: fakeToken,
			Paths:       paths,
			Out:         &buf,
			Forge:       github,
			Resume:      resume,
		})
		out = buf.String()
		t.Logf("Execute took %s, err = %v, output:\n%s", time.Since(start).Round(time.Second), err, out)
		if err != nil {
			logs, _ := filepath.Glob(filepath.Join(paths.StateDir, project, "logs", "*.jsonl"))
			for _, log := range logs {
				data, _ := os.ReadFile(log)
				t.Logf("%s:\n%s", log, data)
			}
		}
		return
	}
	quarantine := filepath.Join(paths.StateDir, project, QuarantineName)
	stackDir := func(number int) (dir string) {
		return stack.Dir(paths.StateDir, project, stack.Run, fmt.Sprintf("ticket-%d", number))
	}
	containers := func(number int) (ids string) {
		out, err := exec.Command("podman", "ps", "-a", "--quiet", "--filter", fmt.Sprintf("label=com.docker.compose.project=afk_%s_run_ticket-%d", project, number)).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	exists := func(path string) (ok bool) {
		_, err := os.Stat(path)
		return err == nil
	}

	t.Run("no compose.afk.yml on the base branch: the template is for the CLI to offer", func(t *testing.T) {
		old := base
		base = withoutStack
		base.Config = old.Config // the Project's name, whatever the commit says
		defer func() { base = old }()
		_, err := execute(t.Context(), t, 9, "Do the work.", nil)
		if !errors.Is(err, stack.ErrNoComposeAfk) {
			t.Errorf("err = %v, want stack.ErrNoComposeAfk", err)
		}
		if exists(stackDir(9)) {
			t.Error("state of a Run that never started stays")
		}
	})

	t.Run("done: gate in the Sandbox, squashed, pushed, PR opened, Stack removed", func(t *testing.T) {
		out, err := execute(t.Context(), t, 1, "Do the work.", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"fake agent working", "→ Bash git commit", "status: done — Fake.", "gate passed", "result valid, ticket-1", "pull request #42"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q", want)
			}
		}
		files := sh(t, root, "git --git-dir="+quarantine+" show --name-only --format= refs/heads/ticket-1")
		if files != "result.txt" {
			t.Errorf("result commit changes %q, want result.txt", files)
		}
		if got := sh(t, root, "git --git-dir=origin.git log -1 --format=%s%n%b ticket-1"); got != "Fake (#1)\nFake work." {
			t.Errorf("pushed commit message = %q", got)
		}
		// The agent report, then the review report.
		want := []string{"POST /repos/o/r/pulls", "POST /repos/o/r/issues/42/comments", "POST /repos/o/r/issues/42/comments", "DELETE /repos/o/r/issues/1/labels/ready-for-agent"}
		if !slices.Equal(fake.requests, want) {
			t.Errorf("Forge requests = %q, want %q", fake.requests, want)
		}
		if exists(stackDir(1)) || containers(1) != "" {
			t.Errorf("Stack of the finished Run stays: state %v, containers %q", exists(stackDir(1)), containers(1))
		}
		if out, _ := exec.Command("grep", "-rlF", fakeToken, paths.StateDir).Output(); len(out) > 0 {
			t.Errorf("token in the state directory: %s", out)
		}
		logs, _ := filepath.Glob(filepath.Join(paths.StateDir, project, "logs", "ticket-1-*.jsonl"))
		if len(logs) != 1 {
			t.Errorf("logs = %v, want one", logs)
		}
	})

	t.Run("existing result: refused before the Stack starts", func(t *testing.T) {
		out, err := execute(t.Context(), t, 1, "Do the work.", nil)
		if err == nil || !strings.Contains(err.Error(), "already has a result") || exists(stackDir(1)) {
			t.Errorf("err = %v, output:\n%s", err, out)
		}
	})

	t.Run("state of an earlier Run: a fresh Run refuses it", func(t *testing.T) {
		if err := os.MkdirAll(stackDir(8), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := execute(t.Context(), t, 8, "Do the work.", nil)
		if err == nil || !strings.Contains(err.Error(), "state of an earlier Run") {
			t.Errorf("err = %v", err)
		}
		if err = os.RemoveAll(stackDir(8)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("branch on the remote: refused before the Stack starts", func(t *testing.T) {
		sh(t, root, "git --git-dir=origin.git branch ticket-4 main")
		out, err := execute(t.Context(), t, 4, "Do the work.", nil)
		if err == nil || !strings.Contains(err.Error(), "already exists on") || exists(stackDir(4)) {
			t.Errorf("err = %v, output:\n%s", err, out)
		}
	})

	t.Run("needs_info: questions on the ticket, no push, Stack removed", func(t *testing.T) {
		if _, err := execute(t.Context(), t, 5, "MODE: needs_info", nil); err != nil {
			t.Fatal(err)
		}
		want := []string{"POST /repos/o/r/issues/5/comments", "POST /repos/o/r/issues/5/labels", "DELETE /repos/o/r/issues/5/labels/ready-for-agent"}
		if !slices.Equal(fake.requests, want) {
			t.Errorf("Forge requests = %q, want %q", fake.requests, want)
		}
		if refs := sh(t, root, "git --git-dir=origin.git for-each-ref --format='%(refname)' refs/heads/ticket-5"); refs != "" {
			t.Errorf("ticket-5 pushed: %q", refs)
		}
		if exists(stackDir(5)) || containers(5) != "" {
			t.Error("Stack of a Run that ended in needs_info stays")
		}
	})

	t.Run("protected path: rejected, state stays, Stack down", func(t *testing.T) {
		out, err := execute(t.Context(), t, 2, "MODE: protected", nil)
		if err == nil || !strings.Contains(err.Error(), ".afk.toml") {
			t.Fatalf("err = %v, want rejection naming .afk.toml", err)
		}
		if !strings.Contains(out, stackDir(2)) {
			t.Error("output lacks where the state stays")
		}
		if !exists(filepath.Join(stackDir(2), "repo", ".git")) || containers(2) != "" {
			t.Errorf("state %v, containers %q: want the clone kept and the Stack down", exists(stackDir(2)), containers(2))
		}
		if err = removeLeftover(t.Context(), paths.StateDir, project, Leftover{Name: "ticket-2", Ticket: 2}); err != nil {
			t.Fatal(err)
		}
		if exists(stackDir(2)) {
			t.Error("removeLeftover left the state")
		}
	})

	t.Run("interrupted: state stays, a later Run resumes in it", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { // the fake agent commits its partial work, then sleeps
			for ctx.Err() == nil {
				if exists(filepath.Join(stackDir(3), "repo", "partial.txt")) {
					time.Sleep(time.Second) // the commit follows the file
					cancel()
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()
		_, err := execute(ctx, t, 3, "MODE: sleep", nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want the interruption", err)
		}
		if containers(3) != "" {
			t.Errorf("Stack up after the interruption: %q", containers(3))
		}
		if out, _ := exec.Command("grep", "-rlF", fakeToken, paths.StateDir).Output(); len(out) > 0 {
			t.Errorf("token in the state directory after the interruption: %s", out)
		}
		leftovers, err := ListLeftovers(paths.StateDir, project)
		if want := []Leftover{{Name: "ticket-3", Ticket: 3}}; err != nil || !slices.Equal(leftovers, want) {
			t.Fatalf("leftovers = %v, %v; want %v", leftovers, err, want)
		}

		// The ticket may have been edited since: the clone stays, the ticket file is
		// rewritten. The base commit may be gone from the user's repository: the
		// compose files come from the Quarantine repo, which has it.
		old := base
		base.Repo = filepath.Join(root, "no-such-repository")
		defer func() { base = old }()
		out, err := execute(t.Context(), t, 3, "MODE: resume", &leftovers[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "resuming in") {
			t.Errorf("output does not say it resumed:\n%s", out)
		}
		files := sh(t, root, "git --git-dir="+quarantine+" show --name-only --format= refs/heads/ticket-3 | sort")
		if files != "partial.txt\nresult.txt" {
			t.Errorf("result commit changes %q, want the work of both sessions", files)
		}
		if exists(stackDir(3)) || containers(3) != "" {
			t.Error("Stack of the resumed Run stays after its result")
		}
	})
}

// cleanProject removes what a test left: the Stacks of its Runs, their
// volumes and the images it built.
func cleanProject(stateDir, project string) {
	ctx := context.Background()
	names, _ := stack.List(stateDir, project, stack.Run)
	for _, name := range names {
		_ = removeLeftover(ctx, stateDir, project, Leftover{Name: name})
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
