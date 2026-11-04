package forge

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/graf0/afk/config"
)

// The remote of a local forge is the origin of the user's clone, with no
// credentials of afk; everything else is unsupported.
func TestLocal(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@git.example:o/r.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	f, err := New(t.Context(), cfgOf(config.Forge{Kind: config.ForgeLocal, Repo: "o/r", Dir: ".scratch"}), t.TempDir(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Remote(); got.Repo != "o/r" || got.URL != "git@git.example:o/r.git" || got.Env != nil {
		t.Errorf("Remote() = %+v", got)
	}
	if _, err = f.Ticket(t.Context(), 1); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Ticket: err = %v, want ErrUnsupported", err)
	}
	if _, err = f.CreatePull(t.Context(), "h", "b", "t", "", false); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("CreatePull: err = %v, want ErrUnsupported", err)
	}

	// No origin: no forge.
	if _, err = New(t.Context(), cfgOf(config.Forge{Kind: config.ForgeLocal, Repo: "o/r"}), t.TempDir(), t.TempDir()); err == nil {
		t.Error("New without origin: want an error")
	}
}
