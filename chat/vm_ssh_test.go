package chat

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
)

func TestEnsureVMKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := t.TempDir()
	if _, err := ReadVMKey(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadVMKey before any key: err = %v, want fs.ErrNotExist", err)
	}
	first, err := EnsureVMKey(dir)
	if err != nil || !strings.HasPrefix(first, "ssh-ed25519 ") {
		t.Fatalf("EnsureVMKey = %q, %v", first, err)
	}
	info, err := os.Stat(VMKeyPath(dir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("private key: %v, %v", info, err)
	}
	// Once per Project: the second call returns the same key.
	second, err := EnsureVMKey(dir)
	if err != nil || second != first {
		t.Errorf("second call = %q, %v; want the same key", second, err)
	}
	if read, err := ReadVMKey(dir); err != nil || read != first {
		t.Errorf("ReadVMKey = %q, %v; want the made key", read, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("state has %d entries, want only the key directory", len(entries))
	}
}

func TestSSHConfig(t *testing.T) {
	got := string(sshConfig([]config.VM{{Name: "win11", User: "admin"}, {Name: "deb", User: "tester"}}, "/home/a/.ssh/k"))
	for _, want := range []string{"Host win11\n  User admin\n", "Host deb\n  User tester\n", "StrictHostKeyChecking no\n", "IdentityFile /home/a/.ssh/k\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("config lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Host *") {
		t.Error("host key checking is off for every host, not only the VMs")
	}
}
