package image

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	t.Setenv("PATH", "/fake/bin:"+os.Getenv("PATH"))
	paths := Paths{CacheDir: "/home/u/.cache/afk"}

	cmd := packerCommand(t.Context(), "/home/u/.cache/afk/bin/packer-1.16.2", paths, "/work", "build", "-machine-readable")

	if cmd.Path != "/home/u/.cache/afk/bin/packer-1.16.2" {
		t.Errorf("Path = %q", cmd.Path)
	}
	if want := []string{"/home/u/.cache/afk/bin/packer-1.16.2", "build", "-machine-readable"}; !slices.Equal(cmd.Args, want) {
		t.Errorf("Args = %q, want %q", cmd.Args, want)
	}
	if cmd.Dir != "/work" {
		t.Errorf("Dir = %q", cmd.Dir)
	}
	// Go's exec uses the last value of a duplicated variable.
	lastValue := func(key string) (value string) {
		for _, entry := range cmd.Env {
			if v, ok := strings.CutPrefix(entry, key+"="); ok {
				value = v
			}
		}
		return
	}
	for key, want := range map[string]string{
		"PACKER_PLUGIN_PATH": "/home/u/.cache/afk/plugins",
		"PACKER_CONFIG_DIR":  "/home/u/.cache/afk/packer-config",
		"CHECKPOINT_DISABLE": "1",
	} {
		if got := lastValue(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(lastValue("PATH"), "/fake/bin") {
		t.Error("PATH not inherited (qemu must be found)")
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("packer must run in its own process group, or the terminal's Ctrl-C reaches it twice")
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
	cmd := packerCommand(t.Context(), bin, Paths{CacheDir: t.TempDir()}, t.TempDir())
	err := runPacker(cmd, &out, true)
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
	cmd := packerCommand(t.Context(), bin, Paths{CacheDir: t.TempDir()}, t.TempDir())
	if err := runPacker(cmd, &out, true); err != nil {
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
	cmd := packerCommand(ctx, bin, Paths{CacheDir: t.TempDir()}, t.TempDir())
	done := make(chan error, 1)
	go func() { done <- runPacker(cmd, &out, true) }()

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
