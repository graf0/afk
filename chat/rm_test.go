package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/run"
)

func TestUnsaved(t *testing.T) {
	f := newExportFixture(t)
	base := sh(t, f.guest, "git rev-parse HEAD")
	lost := func(from, what string) string {
		t.Helper()
		got, err := unsaved(t.Context(), f.exec, from, what)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// Never exported, HEAD at the base: nothing to lose.
	if got := lost(base, "the base"); got != "" {
		t.Errorf("clean at base: %q", got)
	}
	// Dirty tree.
	sh(t, f.guest, "echo 1 > a && echo 2 > b")
	if got := lost(base, "the base"); got != "2 uncommitted path(s)" {
		t.Errorf("dirty: %q", got)
	}
	// Commits after the base, plus the dirty path.
	sh(t, f.guest, "git add a && git commit -qm one && echo 3 > c && git add c && git commit -qm two")
	head := sh(t, f.guest, "git rev-parse HEAD")
	short := sh(t, f.guest, "git rev-parse --short HEAD")
	got := lost(base, "the base")
	if !strings.Contains(got, "1 uncommitted path(s) and 2 commit(s) after the base") || !strings.Contains(got, short) {
		t.Errorf("commits: %q", got)
	}
	// Exported HEAD, clean tree.
	sh(t, f.guest, "rm b")
	if got = lost(head, "the last export"); got != "" {
		t.Errorf("clean exported: %q", got)
	}
	// Exported commit unknown to the guest.
	sh(t, f.guest, "git reset -q --hard "+base+" && echo 4 > d && git add d && git commit -qm other")
	if got = lost(strings.Repeat("0", 40), "the last export"); !strings.Contains(got, "does not contain the last export") {
		t.Errorf("rewritten: %q", got)
	}
	// Reset behind the export: all of HEAD is exported, nothing to lose.
	sh(t, f.guest, "git reset -q --hard "+base)
	if got = lost(head, "the last export"); got != "" {
		t.Errorf("behind: %q", got)
	}
}

func TestShortSHA(t *testing.T) {
	for in, want := range map[string]string{
		strings.Repeat("ab", 20): "abababababab",
		"abc1234":                "abc1234",
		"\x1b[2Jabc":             "?",
		"":                       "?",
	} {
		if got := shortSHA(in); got != want {
			t.Errorf("shortSHA(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoveRefusals(t *testing.T) {
	cfg, err := config.ParseNamed("test", []byte("[forge]\nkind = \"github\"\nrepo = \"o/r\"\n[gate]\ncmd = \"true\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Base: run.Base{Config: cfg}, Name: "x", Paths: image.Paths{StateDir: t.TempDir()}}
	if err = Remove(t.Context(), Options{Name: "-x"}, true); err == nil || !strings.Contains(err.Error(), "invalid Chat name") {
		t.Errorf("bad name: %v", err)
	}
	// A held lock refuses, even with force.
	chatDir := filepath.Join(opts.Paths.StateDir, "o-r", stateName, "x")
	if err = os.MkdirAll(chatDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockChat("x", chatDir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = Remove(t.Context(), opts, true); err == nil || !strings.Contains(err.Error(), "PID "+strconv.Itoa(os.Getpid())) {
		t.Errorf("locked: err = %v, want a refusal naming the PID", err)
	}
	if _, err = os.Stat(chatDir); err != nil {
		t.Errorf("a refused Remove touched the state: %v", err)
	}
}

func TestForceRefusalIs(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", forceRefusal{errors.New("would lose x, use --force")})
	if !errors.Is(err, ErrNeedsForce) || errors.Is(errors.New("in use (PID 1)"), ErrNeedsForce) {
		t.Error("ErrNeedsForce matches wrongly")
	}
}
