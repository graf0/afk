package chat

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/graf0/afk/stack"
)

func TestList(t *testing.T) {
	runtimeDir, err := os.MkdirTemp("", "afk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	stateDir := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	// A Chat is a state directory; a lock file says when it was last started.
	for name, age := range map[string]time.Duration{"plan": 5 * time.Minute, "long-name": 30 * time.Hour, "old": 72 * time.Hour} {
		dir := stack.Dir(stateDir, "o-r", stack.Chat, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		lock := lockPath(dir)
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(lock, now, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	// The Run's state and files of the Project are no Chats.
	for _, dir := range []string{stack.Dir(stateDir, "o-r", stack.Run, "ticket-1"), filepath.Join(stateDir, "o-r", "chats", "Bad Name")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plan := stack.Dir(stateDir, "o-r", stack.Chat, "plan")
	socket, err := socketPath(plan)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := serveChannel(t.Context(), socket, io.Discard) // running
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	var out strings.Builder
	if err = List(&out, stateDir, "o-r", now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"long-name  stopped  30h\n", "old        stopped  3d\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(out.String(), "plan       running") || strings.Contains(out.String(), "ticket-1") || strings.Contains(out.String(), "Bad") {
		t.Errorf("list =\n%s", out.String())
	}
}

func TestListNoChats(t *testing.T) {
	var out strings.Builder
	if err := List(&out, t.TempDir(), "o-r", time.Now()); err != nil || out.Len() != 0 {
		t.Errorf("= %q, %v", out.String(), err)
	}
}
