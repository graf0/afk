package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/digitalocean/go-libvirt"
)

func TestName(t *testing.T) {
	name, err := Name(KindRun, "graf0-dfmx-42")
	if err != nil || name != "afk-run-graf0-dfmx-42" {
		t.Errorf("Name = %q, %v", name, err)
	}
	kind, id, err := parseName(name)
	if err != nil || kind != KindRun || id != "graf0-dfmx-42" {
		t.Errorf("parseName(%q) = %q, %q, %v", name, kind, id, err)
	}
	if name, err = Name(KindChat, "c1"); err != nil || name != "afk-chat-c1" {
		t.Errorf("Name = %q, %v", name, err)
	}

	for _, bad := range []struct {
		kind Kind
		id   string
	}{{"vm", "x"}, {KindRun, ""}, {KindRun, "A"}, {KindRun, "-x"}, {KindRun, "a/b"}, {KindRun, "../x"}} {
		if _, err := Name(bad.kind, bad.id); err == nil {
			t.Errorf("Name(%q, %q) accepted", bad.kind, bad.id)
		}
	}
	for _, bad := range []string{"default", "afk-run-", "afk-vm-x", "run-x", "afk-run-X", "afk-run-a/b"} {
		if _, _, err := parseName(bad); err == nil {
			t.Errorf("parseName(%q) accepted", bad)
		}
	}
}

func TestDir(t *testing.T) {
	if got := (&Conn{}).dir(KindRun, "x"); got != "/var/lib/afk/runs/x" {
		t.Errorf("dir = %q", got)
	}
	root := t.TempDir()
	if got := (&Conn{Root: root}).dir(KindChat, "y"); got != filepath.Join(root, "chats", "y") {
		t.Errorf("dir = %q", got)
	}
}

func TestIsCode(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", libvirt.Error{Code: uint32(libvirt.ErrNoNetwork)})
	if !isCode(err, libvirt.ErrNoNetwork) || isCode(err, libvirt.ErrNoDomain) || isCode(errors.New("x"), libvirt.ErrNoNetwork) {
		t.Error("isCode misclassifies errors")
	}
}

func TestOverlays(t *testing.T) {
	root := t.TempDir()
	var want []string
	for _, rel := range []string{"runs/r1/disk.qcow2", "chats/c1/disk.qcow2", "runs/r2/other", "base/x/disk.qcow2"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(rel, "disk.qcow2") && !strings.HasPrefix(rel, "base") {
			want = append(want, path)
		}
	}
	got, err := Overlays(root)
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("Overlays = %v, %v; want %v", got, err, want)
	}
}
