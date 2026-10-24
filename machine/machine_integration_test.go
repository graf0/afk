//go:build integration

package machine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/digitalocean/go-libvirt"
	"golang.org/x/crypto/ssh"

	"github.com/graf0/afk/egress"
	"github.com/graf0/afk/image"
)

// Run manually on a host with KVM and libvirt, after `afk image build` in a
// Project (the image must carry the default `afk` key):
//
//	AFK_TEST_PROJECT=<project name> go test -tags integration -run Integration -v ./machine/
//
// The project name is the directory under /var/lib/afk/base, e.g. graf0-dfmx.

type integrationEnv struct {
	conn    *Conn
	project string
	disk    string
	paths   image.Paths
}

func setupIntegration(t *testing.T) (env integrationEnv) {
	t.Helper()
	env.project = os.Getenv("AFK_TEST_PROJECT")
	if env.project == "" {
		t.Skip("AFK_TEST_PROJECT not set")
	}
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no usable /dev/kvm: %v", err)
	}
	_ = kvm.Close()
	if env.paths, err = image.DefaultPaths(); err != nil {
		t.Fatal(err)
	}
	if env.disk, err = image.LatestDisk(env.paths, env.project); err != nil {
		t.Fatal(err)
	}
	if env.conn, err = Connect(); err != nil {
		t.Skipf("no libvirt: %v", err)
	}
	t.Cleanup(func() { _ = env.conn.Close() })
	return
}

func (env integrationEnv) spec(t *testing.T) (spec Spec) {
	t.Helper()
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	return Spec{Project: env.project, Kind: KindRun, ID: "it-" + hex.EncodeToString(id), BaseDisk: env.disk}
}

// assertGone checks that nothing of machine name is left: domain, network,
// nwfilter, directory, QEMU process.
func assertGone(t *testing.T, conn *Conn, name, dir string) {
	t.Helper()
	if _, err := conn.lv.DomainLookupByName(name); !isCode(err, libvirt.ErrNoDomain) {
		t.Errorf("domain %s: %v, want gone", name, err)
	}
	if _, err := conn.lv.NetworkLookupByName(name); !isCode(err, libvirt.ErrNoNetwork) {
		t.Errorf("network %s: %v, want gone", name, err)
	}
	if _, err := conn.lv.NwfilterLookupByName(name); !isCode(err, libvirt.ErrNoNwfilter) {
		t.Errorf("nwfilter %s: %v, want gone", name, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("directory %s: %v, want gone", dir, err)
	}
	if out, err := exec.Command("pgrep", "-af", "guest="+name+",").Output(); err == nil {
		t.Errorf("QEMU still running: %s", out)
	}
}

// lanAddr returns an IPv4 address of the host outside the machine networks.
func lanAddr(t *testing.T) (addr string) {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil && !strings.HasPrefix(ipNet.IP.String(), "10.99.") {
			return ipNet.IP.String()
		}
	}
	t.Skip("host has no LAN IPv4 address")
	return
}

// listenAndCount listens on all host interfaces and counts accepted connections.
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

func TestIntegrationIsolation(t *testing.T) {
	env := setupIntegration(t)
	signer, err := image.SSHSigner(env.paths)
	if err != nil {
		t.Fatal(err)
	}
	port, accepted := listenAndCount(t)
	lan := lanAddr(t)

	spec := env.spec(t)
	name, _ := Name(spec.Kind, spec.ID)
	dir := env.conn.dir(spec.Kind, spec.ID)
	t.Cleanup(func() { _ = env.conn.Kill(name) })

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	m, err := env.conn.Up(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	client, err := m.SSH(ctx, signer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	elapsed := time.Since(start)
	t.Logf("%s: start → ssh %s", m.Name, elapsed.Round(100*time.Millisecond))
	if elapsed > 20*time.Second {
		t.Errorf("start → ssh took %s, want about 10 s", elapsed)
	}

	// Metadata is the only state: Get must give back the same machine.
	got, err := env.conn.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != m.Addr || got.Project != spec.Project || got.Kind != spec.Kind || got.ID != spec.ID || !keysEqual(got.HostKey, m.HostKey) {
		t.Errorf("Get = %+v, want %+v", got, m)
	}

	t.Run("wrong host key is rejected", func(t *testing.T) {
		impostor := m
		other, _, err := newHostKey()
		if err != nil {
			t.Fatal(err)
		}
		impostor.HostKey = other
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		if c, err := impostor.SSH(ctx, signer); !errors.Is(err, ErrHostKey) {
			if c != nil {
				_ = c.Close()
			}
			t.Errorf("err = %v, want ErrHostKey", err)
		}
	})

	t.Run("network isolation and egress proxy", func(t *testing.T) {
		policy, err := egress.Allowlist([]string{"deb.debian.org", "api.anthropic.com"})
		if err != nil {
			t.Fatal(err)
		}
		stop, err := ServeEgress(ctx, client, policy)
		if err != nil {
			t.Fatal(err)
		}
		defer stop()

		gateway := m.Addr[:strings.LastIndex(m.Addr, ".")] + ".1"
		probe := strings.NewReplacer("{PORT}", strconv.Itoa(port), "{LAN}", lan, "{GW}", gateway, "{PROXY}", ProxyAddr).Replace(`
p() { (timeout 4 bash -c "echo > /dev/tcp/$1/$2" 2>/dev/null && echo "PROBE direct $1:$2 REACHABLE") || echo "PROBE direct $1:$2 blocked"; }
p {GW} {PORT}
sudo ip route add {LAN}/32 via {GW} && echo "PROBE route added"
p {LAN} {PORT}
p 1.1.1.1 443
echo "PROBE allowed $(curl -s -m 20 -x http://{PROXY} -o /dev/null -w '%{http_code}' https://deb.debian.org/debian/)"
echo "PROBE denied connect $(curl -s -m 20 -x http://{PROXY} -o /dev/null -w '%{http_connect}' https://example.com/)"
echo "PROBE denied plain $(curl -s -m 20 -x http://{PROXY} -o /dev/null -w '%{http_code}' http://example.com/)"
`)
		session, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		out, err := session.CombinedOutput(probe)
		_ = session.Close()
		t.Logf("probe:\n%s", out)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"PROBE direct " + gateway + ":" + strconv.Itoa(port) + " blocked",
			"PROBE route added",
			"PROBE direct " + lan + ":" + strconv.Itoa(port) + " blocked",
			"PROBE direct 1.1.1.1:443 blocked",
			"PROBE allowed 200",
			"PROBE denied connect 403",
			"PROBE denied plain 403",
		} {
			if !strings.Contains(string(out), want) {
				t.Errorf("probe output lacks %q", want)
			}
		}
		if strings.Contains(string(out), "REACHABLE") {
			t.Error("guest reached something directly")
		}
		if n := accepted.Load(); n != 0 {
			t.Errorf("host listener accepted %d connections from the guest", n)
		}
	})

	_ = client.Close()
	if err = env.conn.Kill(name); err != nil {
		t.Fatal(err)
	}
	assertGone(t, env.conn, name, dir)
}

// Cancelling Up at any point must leave nothing behind, QEMU included.
func TestIntegrationCancelledUpLeavesNothing(t *testing.T) {
	env := setupIntegration(t)
	for _, delay := range []time.Duration{0, 100 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			spec := env.spec(t)
			name, _ := Name(spec.Kind, spec.ID)
			ctx, cancel := context.WithCancel(t.Context())
			timer := time.AfterFunc(delay, cancel)
			defer timer.Stop()
			_, err := env.conn.Up(ctx, spec)
			cancel()
			if err == nil { // cancelled too late: Up finished first
				t.Logf("Up finished before cancel after %s", delay)
				if err = env.conn.Kill(name); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Logf("Up: %v", err)
			}
			assertGone(t, env.conn, name, env.conn.dir(spec.Kind, spec.ID))
		})
	}
}

func keysEqual(a, b ssh.PublicKey) (equal bool) {
	return a != nil && b != nil && string(a.Marshal()) == string(b.Marshal())
}
