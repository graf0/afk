//go:build integration

package image

import (
	"bufio"
	"context"
	"encoding/json"
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

const integrationProject = "afk-integration-test"

func integrationConfig(provision []string) (cfg config.Config) {
	cfg.Forge = config.Forge{Kind: "github", Repo: "afk/integration-test"}
	cfg.Image = config.Image{Template: "debian13", Provision: provision}
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
	projectDir := paths.projectDir(integrationProject)
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	// docs/isolation.md probe, run in the guest as a project provision step
	// (the tunnel is up and the apt proxy is still configured).
	probe := strings.NewReplacer("{PORT}", strconv.Itoa(port), "{LAN}", lan).Replace(`
p() { (timeout 4 bash -c "echo > /dev/tcp/$1/$2" 2>/dev/null && echo "PROBE direct $1:$2 REACHABLE") || echo "PROBE direct $1:$2 blocked"; }
p 10.0.2.2 {PORT}; p {LAN} {PORT}; p 1.1.1.1 443
echo "PROBE proxy status $(curl -s -m 20 -x http://127.0.0.1:3128 -o /dev/null -w '%{http_code}' https://deb.debian.org/debian/)"
test "$(git --version | cut -c1-3)" = git
command -v claude
exit 0 # must not skip the mandatory final steps
`)
	var out syncBuffer
	start := time.Now()
	got, err := Build(t.Context(), Options{Paths: paths, Config: integrationConfig([]string{probe}), Out: &out})
	t.Logf("build took %s", time.Since(start).Round(time.Second))
	t.Logf("output:\n%s", out.String())
	if err != nil {
		t.Fatal(err)
	}
	if latest, err := Latest(paths, integrationProject); err != nil || latest != got || filepath.Dir(got) != projectDir {
		t.Errorf("Latest = %q, %v; want the built version %q in %s", latest, err, got, projectDir)
	}
	disk := filepath.Join(got, templates["debian13"].DiskName)
	if info, err := os.Stat(disk); err != nil || info.Size() == 0 {
		t.Errorf("image missing or empty: %v", err)
	}
	assertFinalSteps(t, paths, disk)

	for _, want := range []string{
		"PROBE direct 10.0.2.2:" + strconv.Itoa(port) + " blocked",
		"PROBE direct " + lan + ":" + strconv.Itoa(port) + " blocked",
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
	interruptBuildWhenOutputContains(t, paths, nil, "Waiting for SSH")
}

func TestIntegrationInterruptDuringProvisioningLeavesNoQEMU(t *testing.T) {
	paths := integrationPaths(t)
	interruptBuildWhenOutputContains(t, paths, []string{"echo PROVISIONING-NOW; sleep 600"}, "PROVISIONING-NOW")
}

// interruptBuildWhenOutputContains starts a build, cancels it once its output
// contains marker, and checks nothing is left behind.
func interruptBuildWhenOutputContains(t *testing.T, paths Paths, provision []string, marker string) {
	t.Helper()
	projectDir := paths.projectDir(integrationProject)
	t.Cleanup(func() { _ = os.RemoveAll(projectDir) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		_, err := Build(ctx, Options{Paths: paths, Config: integrationConfig(provision), Out: &out})
		done <- err
	}()

	deadline := time.After(10 * time.Minute)
	for !strings.Contains(out.String(), marker) {
		select {
		case err := <-done:
			t.Fatalf("build ended before %q: %v\n%s", marker, err, out.String())
		case <-deadline:
			t.Fatalf("%q did not appear in time:\n%s", marker, out.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("Build succeeded after interrupt")
	}
	assertNoQEMU(t)
	if _, err := Latest(paths, integrationProject); err == nil {
		t.Error("interrupted build installed an image")
	}
	entries, _ := os.ReadDir(projectDir)
	for _, entry := range entries {
		t.Errorf("leftover in project dir: %s", entry.Name())
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

// assertFinalSteps checks the mandatory final steps in the built disk by
// reading its root file system with debugfs (no root needed); skipped when the
// tools are missing.
func assertFinalSteps(t *testing.T, paths Paths, disk string) {
	t.Helper()
	const sfdisk, debugfs = "/sbin/sfdisk", "/sbin/debugfs"
	for _, tool := range []string{sfdisk, debugfs} {
		if _, err := os.Stat(tool); err != nil {
			t.Logf("final steps not verified: %v", err)
			return
		}
	}
	raw := filepath.Join(paths.CacheDir, "verify.raw")
	t.Cleanup(func() { _ = os.Remove(raw) })
	if out, err := exec.Command("qemu-img", "convert", "-O", "raw", disk, raw).CombinedOutput(); err != nil {
		t.Fatalf("qemu-img convert: %v\n%s", err, out)
	}
	layout, err := exec.Command(sfdisk, "-J", raw).Output()
	if err != nil {
		t.Fatalf("sfdisk: %v", err)
	}
	var table struct {
		PartitionTable struct {
			Partitions []struct{ Start, Size int64 }
		}
	}
	if err = json.Unmarshal(layout, &table); err != nil {
		t.Fatal(err)
	}
	var rootStart, rootSize int64 // the root file system is the largest partition
	for _, partition := range table.PartitionTable.Partitions {
		if partition.Size > rootSize {
			rootStart, rootSize = partition.Start, partition.Size
		}
	}
	fsImage := raw + "?offset=" + strconv.FormatInt(rootStart*512, 10)
	inGuest := func(command string) (output string) {
		out, err := exec.Command(debugfs, "-R", command, fsImage).CombinedOutput()
		if err != nil {
			t.Fatalf("debugfs %q: %v\n%s", command, err, out)
		}
		return string(out)
	}
	for _, path := range []string{"/etc/cloud/cloud-init.disabled", "/etc/netplan/90-afk.yaml"} {
		if out := inGuest("stat " + path); !strings.Contains(out, "Inode:") {
			t.Errorf("%s missing in the image:\n%s", path, out)
		}
	}
	if out := inGuest("stat /etc/machine-id"); !strings.Contains(out, "Size: 0") {
		t.Errorf("machine-id not empty:\n%s", out)
	}
	for _, path := range []string{"/etc/apt/apt.conf.d/90afk-proxy", "/etc/netplan/50-cloud-init.yaml"} {
		if out := inGuest("stat " + path); strings.Contains(out, "Inode:") {
			t.Errorf("%s still in the image:\n%s", path, out)
		}
	}
}
