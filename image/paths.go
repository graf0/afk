package image

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultBaseDir holds Base images; /var/lib rather than $HOME because
// libvirt-qemu cannot read the home directory (docs/architecture.md).
const defaultBaseDir = "/var/lib/afk/base"

// Paths are the directories afk uses for building images.
type Paths struct {
	CacheDir string // ~/.cache/afk: Packer binary, plugins, config
	StateDir string // ~/.local/state/afk: ssh key
	BaseDir  string // /var/lib/afk/base: finished Base images
}

// DefaultPaths returns the standard locations (docs/architecture.md).
func DefaultPaths() (paths Paths, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		err = fmt.Errorf("finding home directory: %w", err)
		return
	}
	paths = Paths{
		CacheDir: filepath.Join(home, ".cache", "afk"),
		StateDir: filepath.Join(home, ".local", "state", "afk"),
		BaseDir:  defaultBaseDir,
	}
	return
}

// BinDir holds Packer binaries (packer-<version>).
func (p Paths) BinDir() (dir string) { return filepath.Join(p.CacheDir, "bin") }

// PluginDir is PACKER_PLUGIN_PATH.
func (p Paths) PluginDir() (dir string) { return filepath.Join(p.CacheDir, "plugins") }

// PackerConfigDir is PACKER_CONFIG_DIR.
func (p Paths) PackerConfigDir() (dir string) { return filepath.Join(p.CacheDir, "packer-config") }

// SSHDir holds the `afk` ssh key pair.
func (p Paths) SSHDir() (dir string) { return filepath.Join(p.StateDir, "ssh") }

// ImageDir is where the Base image of project lives.
func (p Paths) ImageDir(project string) (dir string) { return filepath.Join(p.BaseDir, project) }
