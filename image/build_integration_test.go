//go:build integration

package image

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graf0/afk/config"
)

// Run manually on a host with KVM and write access to /var/lib/afk/base:
//
//	go test -tags integration -run Integration -timeout 30m -v ./image/
//
// The first run downloads Packer, the qemu plugin and the Debian cloud image.

func integrationPaths(t *testing.T) (paths Paths) {
	t.Helper()
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no usable /dev/kvm: %v", err)
	}
	_ = kvm.Close()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	paths = Paths{
		CacheDir: filepath.Join(home, ".cache", "afk-integration"), // kept between runs
		StateDir: t.TempDir(),
		BaseDir:  defaultBaseDir,
	}
	return
}

func integrationConfig(provision []string) (cfg config.Config) {
	cfg.Forge = config.Forge{Kind: "github", Repo: "afk/integration-test"}
	cfg.Image = config.Image{Template: debian13Name, Provision: provision}
	return
}

// lanAddr returns a non-loopback IPv4 address of the host.
func lanAddr(t *testing.T) (addr string) {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	t.Skip("host has no LAN IPv4 address")
	return
}

// listenAndCount listens on all interfaces and counts accepted connections.
func listenAndCount(t *testing.T) (port int, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted = new(atomic.Int32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, accepted
}

func TestIntegrationBuildAndIsolationProbe(t *testing.T) {
	paths := integrationPaths(t)
	port, accepted := listenAndCount(t)
	lan := lanAddr(t)
	imageDir := paths.ImageDir("afk-integration-test")
	t.Cleanup(func() { _ = os.RemoveAll(imageDir) })

	// docs/isolation.md probe, run in the guest as a project provision step
	// (the tunnel is up and the apt proxy is still configured).
	probe := strings.NewReplacer("{PORT}", itoa(port), "{LAN}", lan).Replace(`
p() { (timeout 4 bash -c "echo > /dev/tcp/$1/$2" 2>/dev/null && echo "PROBE direct $1:$2 REACHABLE") || echo "PROBE direct $1:$2 blocked"; }
p 10.0.2.2 {PORT}; p {LAN} {PORT}; p 1.1.1.1 443
echo "PROBE proxy status $(curl -s -m 20 -x http://127.0.0.1:3128 -o /dev/null -w '%{http_code}' https://deb.debian.org/debian/)"
test "$(git --version | cut -c1-3)" = git
command -v claude
`)
	var out syncBuffer
	start := time.Now()
	got, err := Build(t.Context(), Options{Paths: paths, Config: integrationConfig([]string{probe}), Out: &out})
	t.Logf("build took %s", time.Since(start).Round(time.Second))
	t.Logf("output:\n%s", out.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != imageDir {
		t.Errorf("image dir = %q, want %q", got, imageDir)
	}
	if info, err := os.Stat(filepath.Join(imageDir, "debian13.qcow2")); err != nil || info.Size() == 0 {
		t.Errorf("image missing or empty: %v", err)
	}

	for _, want := range []string{
		"PROBE direct 10.0.2.2:" + itoa(port) + " blocked",
		"PROBE direct " + lan + ":" + itoa(port) + " blocked",
		"PROBE direct 1.1.1.1:443 blocked",
		"PROBE proxy status 200",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("probe output lacks %q", want)
		}
	}
	if strings.Contains(out.String(), "REACHABLE") {
		t.Error("guest reached something directly")
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("host listener accepted %d connections from the guest", n)
	}
	assertNoQEMU(t)
}

func TestIntegrationInterruptLeavesNoQEMU(t *testing.T) {
	paths := integrationPaths(t)
	imageDir := paths.ImageDir("afk-integration-test")
	t.Cleanup(func() { _ = os.RemoveAll(imageDir) })

	ctx, cancel := contextWithCancel(t)
	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		_, err := Build(ctx, Options{Paths: paths, Config: integrationConfig(nil), Out: &out})
		done <- err
	}()

	deadline := time.After(10 * time.Minute)
	for !strings.Contains(out.String(), "Waiting for SSH") {
		select {
		case err := <-done:
			t.Fatalf("build ended before the VM was up: %v\n%s", err, out.String())
		case <-deadline:
			t.Fatalf("VM did not start in time:\n%s", out.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("Build succeeded after interrupt")
	}
	assertNoQEMU(t)
	if _, err := os.Stat(imageDir); err == nil {
		t.Error("interrupted build installed an image")
	}
	entries, _ := os.ReadDir(paths.BaseDir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".build-") {
			t.Errorf("leftover build dir %s", entry.Name())
		}
	}
}

// assertNoQEMU fails if a QEMU started by an afk build (restrict=on user net) is alive.
func assertNoQEMU(t *testing.T) {
	t.Helper()
	outBytes, err := exec.Command("pgrep", "-af", "qemu-system.*user.0,restrict=on").Output()
	if err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(outBytes)))
		for scanner.Scan() {
			t.Errorf("QEMU still running: %s", scanner.Text())
		}
	}
}

func itoa(n int) (s string) { return strconv.Itoa(n) }

func contextWithCancel(t *testing.T) (ctx context.Context, cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel = context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return
}
