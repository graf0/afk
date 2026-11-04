package chat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newLocalFixture is the channel of a Chat on a local forge with the tickets in .scratch.
func newLocalFixture(t *testing.T) (fx *pullFixture) {
	t.Helper()
	return newChannelFixture(t, ".scratch")
}

// gitOut runs git in dir and returns its trimmed output.
func gitOut(t *testing.T, dir string, args ...string) (out string) {
	t.Helper()
	data, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(data))
}

// pushMain commits path with content in the user's repository and pushes
// it to main on the Forge: someone else's change to the base branch.
func (fx *pullFixture) pushMain(t *testing.T, path, content string) {
	t.Helper()
	file := filepath.Join(fx.user, path)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, fx.user, "add", path)
	gitIn(t, fx.user, "commit", "-q", "-m", "human: "+path)
	gitIn(t, fx.user, "push", "-q", fx.remoteDir, "main")
}

func TestPush(t *testing.T) {
	fx := newLocalFixture(t)
	fx.commit(t, "a.txt", "first")

	out, warning, err := fx.call(t, "", "push")
	if err != nil || !strings.HasPrefix(out, "chat-plan on o/r at ") || warning != "" {
		t.Fatalf("push = %q, %q, %v", out, warning, err)
	}
	if got := fx.remoteTip(t); got != fx.head(t) {
		t.Errorf("remote tip = %q, want the clone's HEAD", got)
	}
	if _, warning, err = fx.call(t, "", "push"); err != nil || !strings.Contains(warning, "no new commits") {
		t.Errorf("push again = %q, %v", warning, err)
	}
	if len(fx.pulls) != 0 {
		t.Errorf("pulls = %q, want none", fx.pulls)
	}

	// The commands of a Forge with an API are not routes of this Chat.
	for _, args := range [][]string{{"pr", "create", "--title", "T"}, {"issue", "view", "1"}} {
		if _, _, err = fx.call(t, "", args...); err == nil || !strings.Contains(err.Error(), "no such route") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestPushProtectedPath(t *testing.T) {
	fx := newLocalFixture(t)
	fx.commit(t, ".github/workflows/ci.yml", "sneaky")
	if _, _, err := fx.call(t, "", "push"); err == nil || !strings.Contains(err.Error(), "protected paths") {
		t.Errorf("err = %v", err)
	}
	if fx.remoteTip(t) != "" {
		t.Error("something reached the Forge")
	}
}

// Only the changes under the tickets directory reach main, as one commit on
// main as it is now, by the host's identity.
func TestTicketsPublish(t *testing.T) {
	fx := newLocalFixture(t)
	fx.commit(t, ".scratch/f/issues/01-x.md", "# X\n\nStatus: needs-triage\n")
	fx.commit(t, "code.go", "package x")
	fx.pushMain(t, "docs/a.md", "moved on")
	before := fx.remoteRef(t, "main")

	out, _, err := fx.call(t, "", "tickets", "publish", "-m", "Ticket X")
	if err != nil || !strings.HasPrefix(out, "main on o/r at ") {
		t.Fatalf("publish = %q, %v", out, err)
	}
	main := fx.remoteRef(t, "main")
	if got := gitOut(t, fx.remoteDir, "rev-parse", main+"^"); got != before {
		t.Errorf("parent of the published commit = %q, want main before %q", got, before)
	}
	if got := gitOut(t, fx.remoteDir, "diff-tree", "-r", "--name-only", "--no-commit-id", main); got != ".scratch/f/issues/01-x.md" {
		t.Errorf("published paths = %q, want only the ticket", got)
	}
	if got := gitOut(t, fx.remoteDir, "log", "-1", "--format=%an <%ae>|%s|%b", main); !strings.HasPrefix(got, "Host <host@example.com>|Ticket X|") || !strings.Contains(got, "— z Chat plan") {
		t.Errorf("published commit = %q", got)
	}
	if fx.remoteTip(t) != "" {
		t.Error("tickets publish pushed the branch of the Chat")
	}

	// Again: main has it all already.
	if out, _, err = fx.call(t, "", "tickets", "publish", "-m", "again"); err != nil || !strings.Contains(out, "nothing to publish") {
		t.Errorf("publish again = %q, %v", out, err)
	}
	if got := fx.remoteRef(t, "main"); got != main {
		t.Errorf("main = %q, want it unmoved at %q", got, main)
	}

	// A further change goes on top, the earlier one merging cleanly with itself.
	fx.commit(t, ".scratch/f/issues/01-x.md", "# X\n\nStatus: needs-info\n")
	if _, _, err = fx.call(t, "", "tickets", "publish", "-m", "info"); err != nil {
		t.Fatalf("publish of a further change: %v", err)
	}
	if got := gitOut(t, fx.remoteDir, "show", "main:.scratch/f/issues/01-x.md"); !strings.Contains(got, "needs-info") {
		t.Errorf("ticket on main = %q", got)
	}
}

func TestTicketsPublishNothing(t *testing.T) {
	fx := newLocalFixture(t)
	before := fx.remoteRef(t, "main")
	for i := range 2 { // without commits, then with commits outside the tickets
		if i == 1 {
			fx.commit(t, "code.go", "package x")
		}
		out, _, err := fx.call(t, "", "tickets", "publish", "-m", "m")
		if err != nil || !strings.Contains(out, "nothing to publish") {
			t.Errorf("publish %d = %q, %v", i, out, err)
		}
	}
	if got := fx.remoteRef(t, "main"); got != before {
		t.Errorf("main moved to %q", got)
	}
}

func TestTicketsPublishConflict(t *testing.T) {
	fx := newLocalFixture(t)
	fx.pushMain(t, ".scratch/f/spec.md", "human\n")
	fx.commit(t, ".scratch/f/spec.md", "agent\n")
	before := fx.remoteRef(t, "main")
	if _, _, err := fx.call(t, "", "tickets", "publish", "-m", "m"); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("err = %v", err)
	}
	if got := fx.remoteRef(t, "main"); got != before {
		t.Errorf("main moved to %q", got)
	}
}

// A Chat never makes a ticket ready for the Factory; nor goes a protected
// path past the validation, though outside the tickets.
func TestTicketsPublishRefused(t *testing.T) {
	for name, commit := range map[string][2]string{
		"ready":     {".scratch/f/issues/01-x.md", "# X\n\n  status:  Ready-For-Agent \n"},
		"protected": {".github/workflows/ci.yml", "sneaky"},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newLocalFixture(t)
			before := fx.remoteRef(t, "main")
			fx.commit(t, ".scratch/f/spec.md", "spec\n")
			fx.commit(t, commit[0], commit[1])
			if _, _, err := fx.call(t, "", "tickets", "publish", "-m", "m"); err == nil {
				t.Error("want an error")
			}
			if got := fx.remoteRef(t, "main"); got != before {
				t.Errorf("main moved to %q", got)
			}
		})
	}
}

func TestPushUsage(t *testing.T) {
	fx := newLocalFixture(t)
	for _, args := range [][]string{{"push", "x"}, {"tickets", "publish"}, {"tickets", "publish", "-m", " "}, {"tickets", "publish", "-m", "m", "x"}, {"tickets"}} {
		if _, _, err := fx.call(t, "", args...); err == nil || !strings.Contains(err.Error(), "wrong arguments") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}
