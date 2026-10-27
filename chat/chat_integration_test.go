//go:build integration

package chat

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/run"
)

// Run manually on a host with KVM and libvirt, with a Base image built for
// the project (the image must carry the default `afk` key):
//
//	AFK_TEST_PROJECT=afk-vm-test go test -tags integration -run Integration -v ./chat/
//
// The Agent is replaced by a script that checks what a Chat gives it.

const projectConfig = `[forge]
kind = "github"
repo = "%s"
[gate]
cmd = "true"
[tool.fake]
setup = ["echo setup > setup-ran"]
env = { TOOL_VAR = "tool-value" }
`

// fakeClaude runs in the guest instead of `claude …`.
const fakeClaude = `set -eu
fail() { echo "FAKE: $*" >&2; exit 1; }
claude --version >/dev/null || fail "no claude in the image"
[ "$PWD" = /work/repo ] || fail "not in the clone: $PWD"
[ "$(git rev-parse --abbrev-ref HEAD)" = chat-itest ] || fail "wrong branch"
git rev-parse -q --verify origin/main >/dev/null || fail "no origin/main"
[ "$(cat setup-ran)" = setup ] || fail "setup of the Tool did not run"
[ "$TOOL_VAR" = tool-value ] || fail "env of the Tool missing"
[ "$CLAUDE_CODE_OAUTH_TOKEN" = fake-42 ] || fail "token not in the environment"
! grep -rqsF "$CLAUDE_CODE_OAUTH_TOKEN" /work /home/agent /tmp || fail "token on disk"
public=$(curl -s -m 20 -o /dev/null -w '%{http_code}' https://example.com/ || true)
private=$(curl -s -m 20 -o /dev/null -w '%{http_connect}' https://10.99.0.1/ || true)
[ "$public" != 000 ] || fail "public host unreachable"
[ "$private" = 403 ] || fail "private address not denied: $private"
echo first > session-marker
echo work > work.txt && git add work.txt && git commit -qm "work in the chat"
sync # the VM is destroyed (power off) later
`

// fakeContinue runs in the guest on Attach: the same clone, the same env.
const fakeContinue = `set -eu
fail() { echo "FAKE: $*" >&2; exit 1; }
[ "$PWD" = /work/repo ] || fail "not in the clone: $PWD"
[ "$(cat session-marker)" = first ] || fail "not the clone of the first session"
[ "$TOOL_VAR" = tool-value ] || fail "env of the Tool missing"
[ "$CLAUDE_CODE_OAUTH_TOKEN" = fake-42 ] || fail "token not in the environment"
public=$(curl -s -m 20 -o /dev/null -w '%{http_code}' https://example.com/ || true)
private=$(curl -s -m 20 -o /dev/null -w '%{http_connect}' https://10.99.0.1/ || true)
[ "$public" != 000 ] || fail "public host unreachable"
[ "$private" = 403 ] || fail "private address not denied: $private"
`

func TestIntegrationNew(t *testing.T) {
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
	conn, err := machine.Connect()
	if err != nil {
		t.Skipf("no libvirt: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	remoteDir := filepath.Join(root, "origin.git")
	cfgText := strings.Replace(projectConfig, "%s", owner+"/"+name, 1)
	script := `set -eu
git init -q -b main origin.git --bare
git init -q -b main repo
cd repo
echo hello > hello.txt
cat > .afk.toml <<'EOF'
` + cfgText + `EOF
git add -A
git -c user.name=t -c user.email=t@t commit -qm base
git rev-parse HEAD`
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	commit := strings.TrimSpace(string(out))
	cfg, err := config.ParseNamed("test", []byte(cfgText))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Base:        run.Base{Repo: repo, Branch: "main", Commit: commit, Config: cfg},
		Name:        "itest",
		Effort:      "low",
		ClaudeToken: "fake-42",
		Paths:       paths,
		Forge:       remoteOnly{remote: forge.Remote{Repo: owner + "/" + name, URL: remoteDir}},
		Command:     []string{"bash", "-c", fakeClaude},
	}
	var output bytes.Buffer
	opts.Out = &output
	t.Cleanup(func() { _ = conn.Kill("afk-chat-itest") })

	err = New(t.Context(), opts)
	t.Logf("output:\n%s", &output)
	if err != nil {
		t.Fatal(err)
	}
	m, err := conn.Get("afk-chat-itest")
	if err != nil {
		t.Fatalf("the VM must stay after the Agent ends: %v", err)
	}
	if !m.Running || m.Kind != machine.KindChat || m.Effort != "low" || m.Repo != repo || m.Created.IsZero() {
		t.Errorf("machine = %+v", m)
	}
	logs, _ := filepath.Glob(filepath.Join(paths.StateDir, m.Project, "logs", "chat-itest-*-egress.txt"))
	var denials []byte
	if len(logs) == 1 {
		denials, _ = os.ReadFile(logs[0])
	}
	if !strings.Contains(string(denials), "10.99.0.1") {
		t.Errorf("egress denials = %q, want the private address", denials)
	}

	// The second Chat of the name is refused, before any VM.
	output.Reset()
	if err = New(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second New: err = %v, want a refusal", err)
	}

	// A second process holding the Chat's lock: Attach is refused with its PID.
	chatDir := filepath.Join(paths.StateDir, m.Project, stateName, "itest")
	unlock, err := lockChat("itest", chatDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = Attach(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "PID "+strconv.Itoa(os.Getpid())) {
		t.Errorf("Attach of a Chat in use: err = %v, want a refusal naming the PID", err)
	}
	// Export does not take the lock: it pushes the Chat's commit to the remote.
	output.Reset()
	err = Export(t.Context(), opts)
	t.Logf("export output:\n%s", &output)
	if err != nil {
		t.Fatalf("Export while the Chat is in use: %v", err)
	}
	tip := func() string {
		out, _ := exec.Command("git", "--git-dir", remoteDir, "log", "-1", "--format=%H %s", "chat-itest").CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	if got := tip(); !strings.HasSuffix(got, " work in the chat") || !strings.Contains(output.String(), "exported chat-itest at "+got[:12]) {
		t.Errorf("remote chat-itest = %q, output:\n%s", got, &output)
	}
	unlock()

	// The host restarts: the VM is shut off.
	shutOff := func() {
		t.Helper()
		if out, virshErr := exec.Command("virsh", "-c", "qemu:///system", "destroy", "afk-chat-itest").CombinedOutput(); virshErr != nil {
			t.Fatalf("virsh destroy: %v\n%s", virshErr, out)
		}
		if m, err = conn.Get("afk-chat-itest"); err != nil || m.Running {
			t.Fatalf("after destroy: %+v, %v", m, err)
		}
	}
	// Export starts it, finds nothing new and pushes nothing.
	shutOff()
	before := tip()
	output.Reset()
	if err = Export(t.Context(), opts); err != nil || !strings.Contains(output.String(), "no new commits") || tip() != before {
		t.Errorf("second Export: %v, tip %q, output:\n%s", err, tip(), &output)
	}
	// Attach starts it and returns to the same clone.
	shutOff()
	output.Reset()
	opts.Command = []string{"bash", "-c", fakeContinue}
	err = Attach(t.Context(), opts)
	t.Logf("attach output:\n%s", &output)
	if err != nil {
		t.Fatal(err)
	}
	if m, err = conn.Get("afk-chat-itest"); err != nil || !m.Running {
		t.Errorf("the VM must run after Attach: %+v, %v", m, err)
	}
	if !strings.Contains(output.String(), "starting VM") {
		t.Errorf("Attach did not report starting the VM:\n%s", &output)
	}

	// --- rm ---
	stateDir := filepath.Join(paths.StateDir, m.Project)
	quarantine, err := run.OpenQuarantine(t.Context(), filepath.Join(stateDir, run.QuarantineName))
	if err != nil {
		t.Fatal(err)
	}
	overlayDir := filepath.Join(machine.DefaultRoot, "chats", "itest")
	remove := func(force bool) error {
		output.Reset()
		err := Remove(t.Context(), opts, force)
		t.Logf("rm (force %v): %v\n%s", force, err, &output)
		return err
	}
	attachWith := func(script string) {
		t.Helper()
		opts.Command = []string{"bash", "-c", "set -eu; " + script}
		if err := Attach(t.Context(), opts); err != nil {
			t.Fatal(err)
		}
	}
	// The Chat in use refuses, force or not; the VM stays.
	unlock, err = lockChat("itest", chatDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = remove(true); err == nil || !strings.Contains(err.Error(), "PID "+strconv.Itoa(os.Getpid())) {
		t.Errorf("Remove of a Chat in use: err = %v", err)
	}
	unlock()
	// The files the first session left are uncommitted work.
	if err = remove(false); err == nil || !strings.Contains(err.Error(), "uncommitted path(s)") {
		t.Errorf("Remove of a dirty Chat: err = %v", err)
	}
	// A commit that was not exported.
	attachWith("git clean -fdxq && echo more > more.txt && git add more.txt && git commit -qm unexported")
	if err = remove(false); err == nil || !strings.Contains(err.Error(), "1 commit(s) after the last export") {
		t.Errorf("Remove with an unexported commit: err = %v", err)
	}
	if _, err = conn.Get("afk-chat-itest"); err != nil {
		t.Errorf("a refused Remove must keep the VM: %v", err)
	}
	// Exported and clean: removed without force, everything but the branch.
	if err = Export(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	wantTip := tip()
	if err = remove(false); err != nil {
		t.Fatalf("Remove of a clean, exported Chat: %v", err)
	}
	gone := func(chat, branch, overlay string) {
		t.Helper()
		if _, getErr := conn.Get("afk-chat-" + chat); getErr == nil {
			t.Errorf("VM of %s still exists", chat)
		}
		for _, dir := range []string{filepath.Join(stateDir, stateName, chat), overlay} {
			if _, statErr := os.Stat(dir); statErr == nil {
				t.Errorf("%s still exists", dir)
			}
		}
		if base, baseErr := quarantine.Base(t.Context(), branch); baseErr == nil {
			t.Errorf("Quarantine base of %s still exists: %s", branch, base)
		}
	}
	gone("itest", "chat-itest", overlayDir)
	if tip() != wantTip || wantTip == "" {
		t.Errorf("remote chat-itest = %q, want it untouched at %q", tip(), wantTip)
	}
	if err = remove(false); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("second Remove: err = %v", err)
	}

	// A Chat with an unexported commit goes with --force, VM never started.
	opts.Name = "itest2"
	opts.Command = []string{"bash", "-c", "git commit -q --allow-empty -m unexported"}
	t.Cleanup(func() { _ = conn.Kill("afk-chat-itest2") })
	if err = New(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if err = remove(false); err == nil || !strings.Contains(err.Error(), "1 commit(s) after the base") {
		t.Errorf("Remove of a Chat with an unexported commit: err = %v", err)
	}
	if err = remove(true); err != nil {
		t.Fatal(err)
	}
	gone("itest2", "chat-itest2", filepath.Join(machine.DefaultRoot, "chats", "itest2"))
}

// remoteOnly is a Forge of which Chat uses only the remote.
type remoteOnly struct {
	forge.Forge
	remote forge.Remote
}

func (f remoteOnly) Remote() (remote forge.Remote) { return f.remote }
