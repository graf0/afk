package image

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for the concurrent writes runPacker makes.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
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

func writeScript(t *testing.T, body string) (path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "fake-packer")
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return
}

func TestPackerCommand(t *testing.T) {
	t.Setenv("PACKER_PLUGIN_PATH", "/evil/plugins")
	t.Setenv("PACKER_CONFIG_DIR", "/evil/config")
	t.Setenv("CHECKPOINT_DISABLE", "0")
	t.Setenv("PATH", "/fake/bin")
	t.Setenv("HOME", "/home/u")
	t.Setenv("TMPDIR", "/fake/tmp")
	t.Setenv("GITHUB_TOKEN", "secret")
	t.Setenv("PKR_VAR_x", "secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	paths := Paths{CacheDir: "/home/u/.cache/afk"}

	cmd := packerCommand(t.Context(), nil, "/home/u/.cache/afk/bin/packer-1.16.2", paths, "/work", "build", "-machine-readable")

	if cmd.Path != "/home/u/.cache/afk/bin/packer-1.16.2" {
		t.Errorf("Path = %q", cmd.Path)
	}
	if want := []string{"/home/u/.cache/afk/bin/packer-1.16.2", "build", "-machine-readable"}; !slices.Equal(cmd.Args, want) {
		t.Errorf("Args = %q, want %q", cmd.Args, want)
	}
	if cmd.Dir != "/work" {
		t.Errorf("Dir = %q", cmd.Dir)
	}
	want := []string{
		"PATH=/fake/bin",
		"HOME=/home/u",
		"PACKER_PLUGIN_PATH=/home/u/.cache/afk/plugins",
		"PACKER_CONFIG_DIR=/home/u/.cache/afk/packer-config",
		"CHECKPOINT_DISABLE=1",
		"TMPDIR=/fake/tmp",
	}
	if !slices.Equal(cmd.Env, want) {
		t.Errorf("Env = %q, want exactly %q (no host secrets)", cmd.Env, want)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("packer must run in its own process group, or the terminal's Ctrl-C reaches it twice")
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("Pdeathsig = %v, want SIGKILL", cmd.SysProcAttr.Pdeathsig)
	}
}

func TestPackerEnvOmitsUnsetTMPDIR(t *testing.T) {
	t.Setenv("TMPDIR", "") // restores the original value after the test
	if err := os.Unsetenv("TMPDIR"); err != nil {
		t.Fatal(err)
	}
	for _, entry := range packerEnv(Paths{CacheDir: "/c"}) {
		if strings.HasPrefix(entry, "TMPDIR=") {
			t.Errorf("unset TMPDIR passed on as %q", entry)
		}
	}
}

func TestRunPackerStreamsEventsAndReportsErrors(t *testing.T) {
	bin := writeScript(t, `
echo '1,qemu.debian,ui,say,==> qemu.debian: Starting'
echo '2,,error,boom%!(PACKER_COMMA) really'
echo 'plain stderr' >&2
exit 1
`)
	var out syncBuffer
	cmd := packerCommand(t.Context(), nil, bin, Paths{CacheDir: t.TempDir()}, t.TempDir())
	err := runPackerMachineReadable(cmd, &out)
	if err == nil || !strings.Contains(err.Error(), "boom, really") {
		t.Errorf("err = %v, want packer's error message", err)
	}
	for _, want := range []string{"==> qemu.debian: Starting", "error: boom, really", "plain stderr"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunPackerSuccess(t *testing.T) {
	bin := writeScript(t, "echo '1,,ui,say,done'\n")
	var out syncBuffer
	cmd := packerCommand(t.Context(), nil, bin, Paths{CacheDir: t.TempDir()}, t.TempDir())
	if err := runPackerMachineReadable(cmd, &out); err != nil {
		t.Fatal(err)
	}
}

// Cancelling the context must send SIGINT to Packer and wait until it exits,
// so Packer has time to stop QEMU.
func TestRunPackerCancelSendsSIGINTAndWaits(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "cleaned-up")
	bin := writeScript(t, `
trap 'sleep 0.3; touch `+marker+`; echo "1,,ui,say,cleaned up"; exit 130' INT
echo '1,,ui,say,ready'
while true; do sleep 0.05; done
`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out syncBuffer
	cmd := packerCommand(ctx, nil, bin, Paths{CacheDir: t.TempDir()}, t.TempDir())
	done := make(chan error, 1)
	go func() { done <- runPackerMachineReadable(cmd, &out) }()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "ready") {
		if time.Now().After(deadline) {
			t.Fatal("fake packer did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("err = nil, want error after interrupt")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runPacker did not return after cancel")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("runPacker returned before packer finished its SIGINT cleanup: %v", err)
	}
	if !strings.Contains(out.String(), "cleaned up") {
		t.Errorf("output = %q", out.String())
	}
}

// stubbornScript is a fake Packer that ignores SIGINT and has a child in its
// process group (like the qemu plugin and QEMU); both pids go to pidFile.
func stubbornScript(t *testing.T, pidFile string) (bin string) {
	t.Helper()
	return writeScript(t, `
trap '' INT
sleep 600 &
echo "$$ $!" > `+pidFile+`
echo '1,,ui,say,ready'
while true; do sleep 0.05; done
`)
}

// startStubborn runs stubbornScript through runPackerMachineReadable and
// returns its pids once it is ready.
func startStubborn(t *testing.T, ctx context.Context, abort <-chan struct{}) (parent, child int, done <-chan error) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pids")
	var out syncBuffer
	cmd := packerCommand(ctx, abort, stubbornScript(t, pidFile), Paths{CacheDir: t.TempDir()}, t.TempDir())
	errs := make(chan error, 1)
	go func() { errs <- runPackerMachineReadable(cmd, &out) }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "ready") {
		if time.Now().After(deadline) {
			t.Fatal("fake packer did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Sscan(string(data), &parent, &child); err != nil {
		t.Fatalf("pid file %q: %v", data, err)
	}
	return parent, child, errs
}

// processAlive reports whether pid is running (a zombie is not).
func processAlive(pid int) (alive bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	state := data[strings.LastIndexByte(string(data), ')')+2]
	return state != 'Z'
}

func waitDead(t *testing.T, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for processAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d still alive", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// After the grace period the whole process group is killed, children included.
func TestRunPackerKillsGroupAfterGrace(t *testing.T) {
	old := interruptGrace
	interruptGrace = 300 * time.Millisecond
	t.Cleanup(func() { interruptGrace = old })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	parent, child, done := startStubborn(t, ctx, nil)
	t.Cleanup(func() { _ = syscall.Kill(-parent, syscall.SIGKILL) })

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("err = nil, want error after kill")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runPacker did not return after the grace period")
	}
	waitDead(t, parent, child)
}

// Closing abort kills the group at once, without waiting for the grace period.
func TestRunPackerAbortKillsGroupImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	abort := make(chan struct{})
	parent, child, done := startStubborn(t, ctx, abort)
	t.Cleanup(func() { _ = syscall.Kill(-parent, syscall.SIGKILL) })

	cancel()
	close(abort)
	select {
	case err := <-done:
		if err == nil {
			t.Error("err = nil, want error after kill")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runPacker did not return after abort")
	}
	waitDead(t, parent, child)
}

func TestRunPackerPlain(t *testing.T) {
	bin := writeScript(t, "echo out; echo err >&2; exit 3\n")
	var out syncBuffer
	cmd := packerCommand(t.Context(), nil, bin, Paths{CacheDir: t.TempDir()}, t.TempDir())
	if err := runPacker(cmd, &out); err == nil {
		t.Error("err = nil, want exit status error")
	}
	for _, want := range []string{"out", "err"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %q", want, out.String())
		}
	}
}
