//go:build integration

package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
)

// Run manually on a host with KVM and libvirt, with a Base image built for
// the project (the image must carry the default `afk` key):
//
//	AFK_TEST_PROJECT=afk-vm-test go test -tags integration -run Integration -v ./run/
//
// The Agent is a fake script committed in the base: no Claude token needed.

// fakeAgent checks what a Run gives the Agent, then commits like one would.
// The ticket body selects the behaviour.
const fakeAgent = `#!/bin/bash
set -eu
prompt=$(cat)
fail() { echo "FAKE: $*" >&2; exit 1; }
[ "$CLAUDE_CODE_OAUTH_TOKEN" = "fake-$((6 * 7))" ] || fail "token not in the environment"
! grep -rqsF "$CLAUDE_CODE_OAUTH_TOKEN" /work /home/agent /tmp || fail "token on disk"
grep -q /work/TICKET.md <<<"$prompt" || fail "prompt without the ticket path"
[[ "$(git rev-parse --abbrev-ref HEAD)" == ticket-[0-9]* ]] || fail "wrong branch"
git rev-parse -q --verify origin/main >/dev/null || fail "no origin/main"
allowed=$(curl -s -m 20 -o /dev/null -w '%{http_code}' https://api.anthropic.com/ || true)
denied=$(curl -s -m 20 -o /dev/null -w '%{http_connect}' https://example.com/ || true)
[ "$allowed" != 000 ] || fail "api.anthropic.com unreachable"
[ "$denied" = 403 ] || fail "example.com not denied: $denied"
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"fake agent working"},{"type":"tool_use","name":"Bash","input":{"command":"git commit"}}]}}'
if grep -q 'MODE: sleep' /work/TICKET.md; then sleep 600; fi
if grep -q 'MODE: protected' /work/TICKET.md; then
	echo '# changed' >> .afk.toml
else
	echo done > result.txt
	git add result.txt
fi
git commit -qam 'fake work' >&2
echo '{"type":"result","subtype":"success","is_error":false,"structured_output":{"status":"done","reason":"Fake.","summary":"Fake work.","deviations":[]}}'
`

var vmNamePattern = regexp.MustCompile(`VM (afk-run-[a-z0-9-]+) started`)

func TestIntegrationExecute(t *testing.T) {
	project := os.Getenv("AFK_TEST_PROJECT")
	if project == "" {
		t.Skip("AFK_TEST_PROJECT not set")
	}
	owner, name, _ := strings.Cut(project, "-")
	realPaths, err := image.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	// Own state directory, with the real `afk` ssh key the image trusts.
	paths := realPaths
	paths.StateDir = t.TempDir()
	if err = os.CopyFS(filepath.Join(paths.StateDir, "ssh"), os.DirFS(filepath.Join(realPaths.StateDir, "ssh"))); err != nil {
		t.Fatal(err)
	}

	root, user, _ := userRepo(t)
	if err = os.WriteFile(filepath.Join(user, "fake-claude"), []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	sh(t, user, `sed -i 's|repo = "o/r"|repo = "`+owner+"/"+name+`"|' .afk.toml && git add -A && git commit -qm fake && git push -q origin main`)
	base, err := ReadBase(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := machine.Connect()
	if err != nil {
		t.Skipf("no libvirt: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	execute := func(ctx context.Context, t *testing.T, number int, body string) (out string, vm string, err error) {
		var buf bytes.Buffer
		start := time.Now()
		err = Execute(ctx, Options{
			Base:        base,
			Ticket:      forge.Ticket{Number: number, Title: "Fake", Body: body},
			ClaudeToken: "fake-42",
			Paths:       paths,
			Out:         &buf,
			AgentBinary: "/work/repo/fake-claude",
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
		if match := vmNamePattern.FindStringSubmatch(out); match != nil {
			vm = match[1]
			t.Cleanup(func() { _ = conn.Kill(vm) })
		}
		return
	}
	quarantine := filepath.Join(paths.StateDir, project, QuarantineName)

	t.Run("done: result in quarantine, VM destroyed", func(t *testing.T) {
		out, vm, err := execute(t.Context(), t, 1, "Do the work.")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"fake agent working", "→ Bash git commit", "status: done — Fake.", "result valid: 1 commit(s)"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q", want)
			}
		}
		files := sh(t, root, "git --git-dir="+quarantine+" show --name-only --format= refs/heads/ticket-1")
		if files != "result.txt" {
			t.Errorf("result commit changes %q, want result.txt", files)
		}
		if _, err := conn.Get(vm); err == nil {
			t.Errorf("VM %s still exists", vm)
		}
		logs, _ := filepath.Glob(filepath.Join(paths.StateDir, project, "logs", "ticket-1-*.jsonl"))
		if len(logs) != 1 {
			t.Errorf("logs = %v, want one", logs)
		}
	})

	t.Run("existing result: refused before the VM starts", func(t *testing.T) {
		out, vm, err := execute(t.Context(), t, 1, "Do the work.")
		if err == nil || !strings.Contains(err.Error(), "already has a result") || vm != "" {
			t.Errorf("err = %v, VM %q, output:\n%s", err, vm, out)
		}
	})

	t.Run("protected path: rejected, VM stays", func(t *testing.T) {
		out, vm, err := execute(t.Context(), t, 2, "MODE: protected")
		if err == nil || !strings.Contains(err.Error(), ".afk.toml") {
			t.Fatalf("err = %v, want rejection naming .afk.toml", err)
		}
		if !strings.Contains(out, "afk vm kill "+vm) {
			t.Error("output lacks how to kill the VM")
		}
		if _, err := conn.Get(vm); err != nil {
			t.Errorf("VM %s gone after failure: %v", vm, err)
		}
		if err := conn.Kill(vm); err != nil {
			t.Error(err)
		}
	})

	t.Run("interrupted: VM stays", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second) // the fake agent sleeps
		defer cancel()
		_, vm, err := execute(ctx, t, 3, "MODE: sleep")
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("err = %v, want interruption", err)
		}
		if _, err := conn.Get(vm); err != nil {
			t.Errorf("VM %s gone after interruption: %v", vm, err)
		}
		if err := conn.Kill(vm); err != nil {
			t.Error(err)
		}
	})
}
