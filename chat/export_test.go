package chat

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/run"
)

// exportFixture is a Chat whose guest is a local clone, exported to a local bare remote.
type exportFixture struct {
	t      *testing.T
	root   string
	guest  string
	remote string
	out    *bytes.Buffer
	e      exporter
}

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

func newExportFixture(t *testing.T) (f *exportFixture) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(name, "t")
	}
	for _, name := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(name, "t@example.com")
	}
	root := t.TempDir()
	sh(t, root, `
git init -q --bare -b main origin.git
git init -q -b main user
cd user
echo hello > hello.txt
mkdir .claude && echo '{}' > .claude/settings.json
git add -A && git commit -qm base
git remote add origin ../origin.git && git push -q origin main`)
	user := filepath.Join(root, "user")
	base := sh(t, user, "git rev-parse HEAD")
	sh(t, root, "git clone -q origin.git guest && cd guest && git checkout -q -b chat-x")

	q, err := run.OpenQuarantine(t.Context(), filepath.Join(root, "quarantine.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = q.FetchBase(t.Context(), user, base, "chat-x"); err != nil {
		t.Fatal(err)
	}
	chatDir := filepath.Join(root, "chatdir")
	if err = os.Mkdir(chatDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = (snapshot{BaseBranch: "main", Protect: []string{".claude/", "secret"}}).write(chatDir); err != nil {
		t.Fatal(err)
	}
	f = &exportFixture{t: t, root: root, guest: filepath.Join(root, "guest"), remote: filepath.Join(root, "origin.git"), out: &bytes.Buffer{}}
	f.e = exporter{Quarantine: q, Remote: forge.Remote{Repo: "o/r", URL: f.remote}, Name: "x", ChatDir: chatDir, TempDir: root, Out: f.out}
	return
}

func (f *exportFixture) exec(ctx context.Context, command string, stdout io.Writer) (err error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir, cmd.Stdout = f.guest, stdout
	return cmd.Run()
}

// export runs one export and returns its output.
func (f *exportFixture) export() (out string, err error) {
	f.out.Reset()
	err = f.e.export(f.t.Context(), f.exec)
	return f.out.String(), err
}

func (f *exportFixture) tip() (sha string) {
	f.t.Helper()
	return sh(f.t, f.remote, "git rev-parse --verify -q chat-x || true")
}

func TestExport(t *testing.T) {
	f := newExportFixture(t)
	guestHead := func() string { return sh(t, f.guest, "git rev-parse HEAD") }

	// No commits: a message, no branch.
	if out, err := f.export(); err != nil || !strings.Contains(out, "no new commits") || f.tip() != "" {
		t.Fatalf("no commits: %q, %v, tip %q", out, err, f.tip())
	}

	// A dirty tree: a warning, the commits go.
	sh(t, f.guest, "echo 1 > a && git add a && git commit -qm one && echo wip > wip")
	out, err := f.export()
	if err != nil || !strings.Contains(out, "warning: uncommitted") || f.tip() != guestHead() {
		t.Fatalf("first export: %q, %v, tip %q", out, err, f.tip())
	}
	if !strings.Contains(out, "exported chat-x at "+guestHead()[:12]) {
		t.Errorf("output lacks the pushed sha:\n%s", out)
	}
	sh(t, f.guest, "rm wip")

	// Nothing new since the export.
	if out, err = f.export(); err != nil || !strings.Contains(out, "no new commits") {
		t.Fatalf("repeat: %q, %v", out, err)
	}

	// More commits move the branch.
	sh(t, f.guest, "echo 2 > b && git add b && git commit -qm two")
	if out, err = f.export(); err != nil || f.tip() != guestHead() {
		t.Fatalf("second export: %q, %v, tip %q", out, err, f.tip())
	}

	// Rewritten history passes.
	sh(t, f.guest, "git commit -q --amend -m 'two, amended' && git rebase -q --root --exec true")
	if out, err = f.export(); err != nil || f.tip() != guestHead() {
		t.Fatalf("rewritten history: %q, %v, tip %q", out, err, f.tip())
	}

	// A protected path rejects the whole export, naming commit and path.
	sh(t, f.guest, "echo 3 > c && git add c && git commit -qm good && echo '{\"x\":1}' > .claude/settings.json && git commit -qam 'touch claude'")
	bad := guestHead()
	before := f.tip()
	_, err = f.export()
	if err == nil || !strings.Contains(err.Error(), bad[:12]+" touch claude: .claude/settings.json") || f.tip() != before {
		t.Fatalf("protected path: err = %v, tip %q", err, f.tip())
	}
	sh(t, f.guest, "git reset -q --hard HEAD~1")

	// Someone else's push between exports: refused, and theirs stays.
	sh(t, f.root, "git clone -q origin.git other && cd other && git checkout -q -b chat-x origin/chat-x && echo theirs > t && git add t && git commit -qm theirs && git push -q origin chat-x")
	theirs := f.tip()
	sh(t, f.guest, "echo 4 > d && git add d && git commit -qm four")
	if _, err = f.export(); err == nil || f.tip() != theirs {
		t.Fatalf("foreign push: err = %v, tip %q, want refusal and %q", err, f.tip(), theirs)
	}
}

func TestExportRefusesOldSnapshot(t *testing.T) {
	f := newExportFixture(t)
	if err := (snapshot{BaseBranch: "main"}).write(f.e.ChatDir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.export(); err == nil || !strings.Contains(err.Error(), "no protected paths") {
		t.Errorf("err = %v", err)
	}
}

func TestExportRemoteStates(t *testing.T) {
	f := newExportFixture(t)
	guestHead := func() string { return sh(t, f.guest, "git rev-parse HEAD") }

	// Someone's branch chat-x before the first export: refused.
	sh(t, f.root, "git clone -q origin.git other && cd other && git checkout -q -b chat-x && echo theirs > t && git add t && git commit -qm theirs && git push -q origin chat-x")
	theirs := f.tip()
	sh(t, f.guest, "echo 1 > a && git add a && git commit -qm one")
	if _, err := f.export(); err == nil || f.tip() != theirs {
		t.Fatalf("existing branch: err = %v, tip %q", err, f.tip())
	}
	sh(t, f.remote, "git branch -D chat-x")

	if out, err := f.export(); err != nil || f.tip() != guestHead() {
		t.Fatalf("first export: %q, %v", out, err)
	}

	// Pushed, but the state lost it (a crash before the snapshot was written): only recorded.
	snap, _ := readSnapshot(f.e.ChatDir)
	snap.Exported = ""
	if err := snap.write(f.e.ChatDir); err != nil {
		t.Fatal(err)
	}
	if out, err := f.export(); err != nil || !strings.Contains(out, "no new commits") {
		t.Fatalf("unrecorded push: %q, %v", out, err)
	}
	if snap, _ = readSnapshot(f.e.ChatDir); snap.Exported != guestHead() {
		t.Errorf("exported = %q, want %q recorded", snap.Exported, guestHead())
	}

	// Deleted on the remote by a human: created again.
	sh(t, f.remote, "git branch -D chat-x")
	sh(t, f.guest, "echo 2 > b && git add b && git commit -qm two")
	if out, err := f.export(); err != nil || f.tip() != guestHead() {
		t.Fatalf("deleted branch: %q, %v, tip %q", out, err, f.tip())
	}

	// HEAD back at the base: nothing pushed, the remote keeps the export.
	exported := f.tip()
	sh(t, f.guest, "git reset -q --hard origin/main")
	if out, err := f.export(); err != nil || !strings.Contains(out, "back at the base") || f.tip() != exported {
		t.Fatalf("reset to base: %q, %v, tip %q", out, err, f.tip())
	}
}
