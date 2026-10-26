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
	unlock()

	// The host restarts: the VM is shut off. Attach starts it and returns to the same clone.
	if out, virshErr := exec.Command("virsh", "-c", "qemu:///system", "destroy", "afk-chat-itest").CombinedOutput(); virshErr != nil {
		t.Fatalf("virsh destroy: %v\n%s", virshErr, out)
	}
	if m, err = conn.Get("afk-chat-itest"); err != nil || m.Running {
		t.Fatalf("after destroy: %+v, %v", m, err)
	}
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
}

// remoteOnly is a Forge of which Chat uses only the remote.
type remoteOnly struct {
	forge.Forge
	remote forge.Remote
}

func (f remoteOnly) Remote() (remote forge.Remote) { return f.remote }
