package chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/run"
)

// pullForge is a Forge of which the pull routes use Remote, FindPull and
// CreatePull; the pull requests it opened are in pulls.
type pullForge struct {
	fakeForge
	pulls    *[]string // "head→base title | body | draft"
	existing forge.PullRequest
}

func (f pullForge) FindPull(context.Context, string) (pull forge.PullRequest, found bool, err error) {
	return f.existing, f.existing.Number != 0, nil
}

func (f pullForge) CreatePull(_ context.Context, head, base, title, body string, draft bool) (pull forge.PullRequest, err error) {
	*f.pulls = append(*f.pulls, fmt.Sprintf("%s→%s %s | %s | %t", head, base, title, body, draft))
	pull = forge.PullRequest{Number: 40 + len(*f.pulls), URL: fmt.Sprintf("https://forge/pull/%d", 40+len(*f.pulls)), State: "open"}
	return
}

// pullFixture is a Chat "plan" with a Sandbox clone and a remote, both local
// repositories, and its channel with the pull routes.
type pullFixture struct {
	socket    string
	clone     string // the Sandbox's clone, where the Agent commits
	remoteDir string // the bare repository of the Forge
	pulls     []string
	protected []string
	forge     pullForge
}

func newPullFixture(t *testing.T) (fx *pullFixture) {
	t.Helper()
	root := t.TempDir()
	user, remoteDir, stateDir := filepath.Join(root, "user"), filepath.Join(root, "origin.git"), filepath.Join(root, "state")
	for _, dir := range []string{user, remoteDir, stateDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, remoteDir, "init", "-q", "--bare", "-b", "main")
	gitIn(t, user, "init", "-q", "-b", "main")
	gitIn(t, user, "commit", "-q", "--allow-empty", "-m", "base")
	base, err := exec.Command("git", "-C", user, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}

	// The Chat's start: the base in the Quarantine repo, cloned into the Sandbox.
	ctx := t.Context()
	quarantine, err := run.OpenQuarantine(ctx, filepath.Join(stateDir, run.QuarantineName))
	if err != nil {
		t.Fatal(err)
	}
	if err = quarantine.FetchBase(ctx, user, strings.TrimSpace(string(base)), "chat-plan"); err != nil {
		t.Fatal(err)
	}
	fx = &pullFixture{clone: filepath.Join(root, "clone"), remoteDir: remoteDir}
	branch := run.GuestBranch{Quarantine: quarantine, Name: "chat-plan", TempDir: stateDir}
	start, err := branch.CloneTo(ctx, fx.clone, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, fx.clone, "checkout", "-q", "-B", "chat-plan", start)

	fx.protected = []string{".github/"}
	fx.forge = pullForge{fakeForge: fakeForge{remote: forge.Remote{Repo: "o/r", URL: remoteDir}}, pulls: &fx.pulls}
	return fx.serve(t, quarantine, stateDir)
}

// serve opens the channel with the pull routes of fx.
func (fx *pullFixture) serve(t *testing.T, quarantine run.Quarantine, stateDir string) (served *pullFixture) {
	t.Helper()
	dir := t.TempDir()
	fx.socket = filepath.Join(dir, "afk.sock")
	execInClone := func(ctx context.Context, command string, stdout io.Writer) (err error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir, cmd.Stdout = fx.clone, stdout
		return cmd.Run()
	}
	stop, err := serveChannel(t.Context(), fx.socket, io.Discard, pullRoutes(pullChannel{
		forgeChannel: forgeChannel{Forge: &fx.forge, ProjectLabels: config.Labels{}, Chat: "plan"},
		Quarantine:   quarantine, Branch: "chat-plan", BaseBranch: "main", TempDir: stateDir,
		Exec: execInClone, Protected: func() (paths []string) { return fx.protected },
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return fx
}

func (fx *pullFixture) commit(t *testing.T, path, message string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(fx.clone, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.clone, path), []byte(message), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, fx.clone, "add", path)
	gitIn(t, fx.clone, "commit", "-q", "-m", message)
}

func (fx *pullFixture) call(t *testing.T, stdin string, args ...string) (out, warning string, err error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err = RunForge(t.Context(), fx.socket, args, strings.NewReader(stdin), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// remoteTip is the commit of chat-plan on the Forge, empty without the branch.
func (fx *pullFixture) remoteTip(t *testing.T) (tip string) {
	t.Helper()
	out, _ := exec.Command("git", "-C", fx.remoteDir, "rev-parse", "--verify", "--quiet", "refs/heads/chat-plan").Output()
	return strings.TrimSpace(string(out))
}

func (fx *pullFixture) head(t *testing.T) (head string) {
	t.Helper()
	out, err := exec.Command("git", "-C", fx.clone, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestPullCreate(t *testing.T) {
	fx := newPullFixture(t)
	fx.commit(t, "a.txt", "first")

	out, warning, err := fx.call(t, "", "pr", "create", "--title", "Idea @bob", "--body", "Fixes #3")
	if err != nil || out != "https://forge/pull/41\n" || warning != "" {
		t.Fatalf("create = %q, %q, %v", out, warning, err)
	}
	if got := fx.remoteTip(t); got != fx.head(t) {
		t.Errorf("remote tip = %q, want the clone's HEAD", got)
	}
	if len(fx.pulls) != 1 || !strings.HasPrefix(fx.pulls[0], "chat-plan→main Idea @\u200bbob | ") ||
		!strings.Contains(fx.pulls[0], "F\u200bixes #3") || !strings.Contains(fx.pulls[0], "— z Chat plan") || !strings.HasSuffix(fx.pulls[0], "| false") {
		t.Errorf("pulls = %q", fx.pulls)
	}
}

func TestPullCreateBodyFromStdin(t *testing.T) {
	fx := newPullFixture(t)
	fx.commit(t, "a.txt", "first")
	if _, _, err := fx.call(t, "From stdin", "pr", "create", "--title", "T", "--body-file", "-"); err != nil {
		t.Fatal(err)
	}
	if len(fx.pulls) != 1 || !strings.Contains(fx.pulls[0], "From stdin") {
		t.Errorf("pulls = %q", fx.pulls)
	}
}

func TestPullCreateExisting(t *testing.T) {
	fx := newPullFixture(t)
	fx.commit(t, "a.txt", "first")
	if _, _, err := fx.call(t, "", "pr", "create", "--title", "T"); err != nil {
		t.Fatal(err)
	}
	fx.forge.existing = forge.PullRequest{Number: 41, URL: "https://forge/pull/41", State: "open"}
	fx.commit(t, "b.txt", "second")

	out, warning, err := fx.call(t, "", "pr", "create", "--title", "Other")
	if err != nil || out != "https://forge/pull/41\n" || !strings.Contains(warning, "#41") {
		t.Fatalf("second create = %q, %q, %v", out, warning, err)
	}
	if len(fx.pulls) != 1 {
		t.Errorf("pulls = %q, want no new pull request", fx.pulls)
	}
	if got := fx.remoteTip(t); got != fx.head(t) {
		t.Errorf("remote tip = %q, want the new HEAD", got)
	}

	// Again without new commits: nothing to push, the same pull request.
	if out, warning, err = fx.call(t, "", "pr", "create", "--title", "Other"); err != nil || out != "https://forge/pull/41\n" || !strings.Contains(warning, "no new commits") {
		t.Errorf("create without new commits = %q, %q, %v", out, warning, err)
	}

	// A closed pull request is not reused.
	fx.forge.existing.State = "closed"
	fx.commit(t, "c.txt", "third")
	if out, _, err = fx.call(t, "", "pr", "create", "--title", "Again"); err != nil || out != "https://forge/pull/42\n" {
		t.Errorf("create after close = %q, %v", out, err)
	}
}

func TestPullCreateNoCommits(t *testing.T) {
	fx := newPullFixture(t)
	_, _, err := fx.call(t, "", "pr", "create", "--title", "T")
	if err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Errorf("err = %v", err)
	}
	if fx.remoteTip(t) != "" || len(fx.pulls) != 0 {
		t.Errorf("something reached the Forge: tip %q, pulls %q", fx.remoteTip(t), fx.pulls)
	}
}

func TestPullCreateProtectedPath(t *testing.T) {
	fx := newPullFixture(t)
	fx.commit(t, "ok.txt", "fine")
	fx.commit(t, ".github/workflows/ci.yml", "sneaky")

	_, _, err := fx.call(t, "", "pr", "create", "--title", "T")
	if err == nil || !strings.Contains(err.Error(), "protected paths") || !strings.Contains(err.Error(), "sneaky") || !strings.Contains(err.Error(), ".github/workflows/ci.yml") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(err.Error(), "ok.txt") {
		t.Errorf("err names a commit that touches nothing protected: %v", err)
	}
	if fx.remoteTip(t) != "" || len(fx.pulls) != 0 {
		t.Errorf("something reached the Forge: tip %q, pulls %q", fx.remoteTip(t), fx.pulls)
	}
}

func TestPullCreateForeignBranch(t *testing.T) {
	fx := newPullFixture(t)
	fx.commit(t, "a.txt", "first")
	if _, _, err := fx.call(t, "", "pr", "create", "--title", "T"); err != nil {
		t.Fatal(err)
	}
	// Someone pushes to chat-plan.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", fx.remoteDir, other)
	gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "foreign")
	gitIn(t, other, "push", "-q", "-f", "origin", "HEAD:chat-plan")
	foreign := fx.remoteTip(t)
	fx.commit(t, "b.txt", "second")

	_, _, err := fx.call(t, "", "pr", "create", "--title", "T")
	if err == nil || !strings.Contains(err.Error(), "not where afk left it") {
		t.Errorf("err = %v", err)
	}
	if got := fx.remoteTip(t); got != foreign {
		t.Errorf("remote tip = %q, want the foreign %q untouched", got, foreign)
	}
}

func TestPullCreatePushRejected(t *testing.T) {
	fx := newPullFixture(t)
	fx.commit(t, "a.txt", "first")
	if _, _, err := fx.call(t, "", "pr", "create", "--title", "T"); err != nil {
		t.Fatal(err)
	}
	first := fx.remoteTip(t)
	hook := filepath.Join(fx.remoteDir, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fx.commit(t, "b.txt", "second")
	if _, _, err := fx.call(t, "", "pr", "create", "--title", "T"); err == nil {
		t.Fatal("push past a rejecting Forge: want an error")
	}
	if got := fx.remoteTip(t); got != first {
		t.Fatalf("remote tip = %q, want %q", got, first)
	}

	// The result branch went back with the failed push: the next call is not
	// taken for someone else's push.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fx.call(t, "", "pr", "create", "--title", "T"); err != nil {
		t.Fatalf("create after the Forge accepts again: %v", err)
	}
	if got := fx.remoteTip(t); got != fx.head(t) {
		t.Errorf("remote tip = %q, want the clone's HEAD", got)
	}
}

func TestPullCreateUncommitted(t *testing.T) {
	fx := newPullFixture(t)
	fx.commit(t, "a.txt", "first")
	if err := os.WriteFile(filepath.Join(fx.clone, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, warning, err := fx.call(t, "", "pr", "create", "--title", "T")
	if err != nil || out == "" || !strings.Contains(warning, "uncommitted") {
		t.Errorf("create = %q, %q, %v", out, warning, err)
	}
}

func TestPullCreateUsage(t *testing.T) {
	fx := newPullFixture(t)
	for _, args := range [][]string{{"pr"}, {"pr", "create"}, {"pr", "create", "--title", "T", "extra"}, {"pr", "view", "1"}, {"pr", "create", "--title", " "}} {
		if _, _, err := fx.call(t, "", args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}
