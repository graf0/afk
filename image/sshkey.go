package image

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

const sshKeyName = "id_ed25519"

// ensureSSHKey returns the path of the `afk` private key in dir and its public
// key in authorized_keys format, generating the key pair on first use.
// An existing key is never overwritten; the public key is derived from the
// private one, not read from disk.
func ensureSSHKey(dir string) (privatePath, publicKey string, err error) {
	privatePath = filepath.Join(dir, sshKeyName)
	data, err := os.ReadFile(privatePath)
	if errors.Is(err, os.ErrNotExist) {
		data, err = generateSSHKey(dir, privatePath)
	}
	if err != nil {
		privatePath = ""
		return
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		privatePath = ""
		err = fmt.Errorf("parsing ssh key %s: %w", filepath.Join(dir, sshKeyName), err)
		return
	}
	publicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return
}

// generateSSHKey creates an ed25519 key pair; the private key file is created
// exclusively with mode 0600.
func generateSSHKey(dir, privatePath string) (data []byte, err error) {
	if err = os.MkdirAll(dir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", dir, err)
		return
	}
	publicRaw, privateRaw, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		err = fmt.Errorf("generating ssh key: %w", err)
		return
	}
	block, err := ssh.MarshalPrivateKey(privateRaw, "afk")
	if err != nil {
		err = fmt.Errorf("encoding ssh key: %w", err)
		return
	}
	data = pem.EncodeToMemory(block)
	public, err := ssh.NewPublicKey(publicRaw)
	if err != nil {
		err = fmt.Errorf("encoding ssh public key: %w", err)
		return
	}
	file, err := os.OpenFile(privatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		err = fmt.Errorf("creating %s: %w", privatePath, err)
		return
	}
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(privatePath)
		err = fmt.Errorf("writing %s: %w", privatePath, err)
		return
	}
	if err = os.WriteFile(privatePath+".pub", ssh.MarshalAuthorizedKey(public), 0o644); err != nil {
		err = fmt.Errorf("writing %s.pub: %w", privatePath, err)
	}
	return
}
