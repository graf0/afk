package chat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLockChat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := lockChat("x", path)
	if err != nil {
		t.Fatal(err)
	}
	// The second holder is refused with the PID of the first.
	_, err = lockChat("x", path)
	if err == nil || !strings.Contains(err.Error(), "PID "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("second lock: err = %v, want a refusal naming PID %d", err, os.Getpid())
	}
	unlock()
	unlock, err = lockChat("x", path)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock()
}

func TestLockPath(t *testing.T) {
	if got := lockPath("/s/o-r/chats/plan"); got != "/s/o-r/chats/plan.lock" {
		t.Errorf("lock path = %q: it lives beside the state directory, not in it", got)
	}
}

// A Chat runs while a process serves its channel; the probe takes no lock.
func TestRunning(t *testing.T) {
	runtimeDir, err := os.MkdirTemp("", "afk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	dir := filepath.Join(t.TempDir(), "chats", "x")
	if running(dir) {
		t.Error("running without a socket")
	}
	socket, err := socketPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := serveChannel(t.Context(), socket, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !running(dir) {
		t.Error("not running with the channel served")
	}
	// The probe does not make the Chat look in use to the afk that starts it.
	unlock, err := lockChat("x", lockPath(dir))
	if err != nil {
		t.Errorf("lock while probed: %v", err)
	} else {
		unlock()
	}
	stop()
	if running(dir) {
		t.Error("running after the channel was closed")
	}
	// A socket a dead process left refuses the connection.
	if err = os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if running(dir) {
		t.Error("running with a stale socket")
	}
}
