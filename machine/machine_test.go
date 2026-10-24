package machine

import (
	"errors"
	"fmt"
	"path/filepath"
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
