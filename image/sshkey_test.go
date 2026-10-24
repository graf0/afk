package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestEnsureSSHKeyGeneratesAndReuses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh")

	privatePath, publicKey, err := ensureSSHKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "id_ed25519"); privatePath != want {
		t.Errorf("privatePath = %q, want %q", privatePath, want)
	}
	info, err := os.Stat(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("private key mode = %v, want 0600", info.Mode().Perm())
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		t.Fatalf("public key %q: %v", publicKey, err)
	}
	if pub.Type() != ssh.KeyAlgoED25519 {
		t.Errorf("key type = %s", pub.Type())
	}
	if onDisk, err := os.ReadFile(privatePath + ".pub"); err != nil || strings.TrimSpace(string(onDisk)) != publicKey {
		t.Errorf(".pub = %q (err %v), want %q", onDisk, err, publicKey)
	}

	// The private key must belong to the public one and be usable by ssh.
	data, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); got != publicKey {
		t.Errorf("signer public key = %q, want %q", got, publicKey)
	}

	again, publicAgain, err := ensureSSHKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != privatePath || publicAgain != publicKey {
		t.Error("second call generated a different key")
	}
}

func TestEnsureSSHKeyRejectsCorruptKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureSSHKey(dir); err == nil {
		t.Error("err = nil, want error for corrupt key (must not be overwritten silently)")
	}
}

func TestSSHSigner(t *testing.T) {
	paths := Paths{StateDir: t.TempDir()}
	if _, err := SSHSigner(paths); err == nil {
		t.Error("SSHSigner without a key succeeded")
	}
	_, publicKey, err := ensureSSHKey(paths.sshDir())
	if err != nil {
		t.Fatal(err)
	}
	signer, err := SSHSigner(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); got != publicKey {
		t.Errorf("signer public key = %q, want %q", got, publicKey)
	}
}
