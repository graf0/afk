package chat

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/graf0/afk/config"
)

const (
	// vmKeyDir is the directory in the Project's state with the ssh key of the
	// test VMs; vmKeyName the private key in it (the public one adds .pub).
	vmKeyDir  = "vm-ssh"
	vmKeyName = "id_ed25519"
)

// VMKeyPath is the private ssh key of the test VMs of the Project whose state
// directory is projectDir.
func VMKeyPath(projectDir string) (path string) {
	return filepath.Join(projectDir, vmKeyDir, vmKeyName)
}

// EnsureVMKey makes the ssh key of the Project's test VMs once (ssh-keygen,
// ed25519, no passphrase) and returns its public part. Several afk processes
// may race: the key is made in a directory of its own and renamed into place,
// and the loser uses the winner's.
func EnsureVMKey(projectDir string) (public string, err error) {
	final := filepath.Join(projectDir, vmKeyDir)
	if public, err = readPublicKey(final); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err = os.MkdirAll(projectDir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", projectDir, err)
		return
	}
	tmp, err := os.MkdirTemp(projectDir, vmKeyDir+"-")
	if err != nil {
		err = fmt.Errorf("creating a directory for the ssh key: %w", err)
		return
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if out, genErr := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "afk-vm", "-f", filepath.Join(tmp, vmKeyName)).CombinedOutput(); genErr != nil {
		err = fmt.Errorf("ssh-keygen: %w: %s", genErr, strings.TrimSpace(string(out)))
		return
	}
	if err = os.Rename(tmp, final); err != nil {
		// Another process was first: its key is there (the rename onto a full directory fails).
		if _, readErr := readPublicKey(final); readErr != nil {
			err = fmt.Errorf("installing the ssh key: %w", err)
			return
		}
	}
	return readPublicKey(final)
}

func readPublicKey(dir string) (public string, err error) {
	data, err := os.ReadFile(filepath.Join(dir, vmKeyName+".pub"))
	public = strings.TrimSpace(string(data))
	return
}

// sshConfig is the ssh configuration of the Agent for the test VMs: an alias
// per VM with its user and the one key, mounted at keyPath. The address of a
// VM is a DHCP lease and changes, so host keys are not remembered; that holds
// for the aliases only, never for other hosts the Agent reaches. The address
// comes from `afk-ai vm list`: ssh -o HostName=<address> <vm>.
func sshConfig(vms []config.VM, keyPath string) (text []byte) {
	var b strings.Builder
	for _, vm := range vms {
		fmt.Fprintf(&b, "Host %s\n  User %s\n  IdentityFile %s\n  IdentitiesOnly yes\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel ERROR\n",
			vm.Name, vm.User, keyPath)
	}
	text = []byte(b.String())
	return
}
