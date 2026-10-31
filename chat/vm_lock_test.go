package chat

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestVMLocks(t *testing.T) {
	dir := t.TempDir()
	a, b := newVMLocks(dir, "alpha"), newVMLocks(dir, "beta")
	if err := a.claim("win11"); err != nil {
		t.Fatal(err)
	}
	if err := a.claim("win11"); err != nil {
		t.Errorf("claiming again: %v", err)
	}
	var busy vmBusyError
	if err := b.claim("win11"); !errors.As(err, &busy) || busy.owner != "alpha" {
		t.Errorf("other Chat claim = %v, want busy by alpha", err)
	}
	if err := b.claim("other"); err != nil {
		t.Errorf("another VM: %v", err)
	}
	if owner, ok := b.ownerOf("win11"); !ok || owner != "alpha" {
		t.Errorf("owner = %q, %v", owner, ok)
	}
	if _, err := b.release("win11"); !errors.As(err, &busy) {
		t.Errorf("release of a VM held by another = %v", err)
	}
	if held, err := a.release("win11"); err != nil || !held {
		t.Errorf("release = %v, %v", held, err)
	}
	if err := b.claim("win11"); err != nil {
		t.Errorf("claim after release: %v", err)
	}
	if held, err := a.release("win11"); err == nil || held {
		t.Errorf("release by a non-owner = %v, %v", held, err)
	}
	b.close()
	if _, ok := a.ownerOf("win11"); ok {
		t.Error("close left the VM held")
	}
}

func TestVMLocksStaleOwner(t *testing.T) {
	dir := t.TempDir()
	l := newVMLocks(dir, "alpha")
	// A file a process left when it died (SIGKILL): no flock held. Its name
	// alone, even next to a live PID, owns nothing.
	if err := os.WriteFile(l.path("win11"), []byte(fmt.Sprintf("p/ghost %d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if owner, ok := l.ownerOf("win11"); ok {
		t.Errorf("owner = %q: a dead process owns nothing", owner)
	}
	if held, err := l.release("win11"); held || err != nil {
		t.Errorf("release of a stale file = %v, %v: want nothing held", held, err)
	}
	if err := l.claim("win11"); err != nil {
		t.Errorf("claim over a stale file: %v", err)
	}
}
