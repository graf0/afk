package image

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"
)

const (
	// defaultBaseDir holds Base images; /var/lib rather than $HOME because
	// libvirt-qemu cannot read the home directory (docs/architecture.md).
	defaultBaseDir = "/var/lib/afk/base"

	// versionFormat names a Base image version: a UTC timestamp that sorts
	// lexicographically in time order.
	versionFormat = "20060102T150405Z"

	// sharedDirMode is the mode of directories shared with group kvm.
	sharedDirMode = os.ModeSetgid | 0o775
)

var versionPattern = regexp.MustCompile(`^\d{8}T\d{6}Z$`)

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

// Latest returns the directory of the newest Base image of project. Each
// version directory appears atomically after a successful build, so every one
// is complete; in-progress builds (.build-*) are ignored.
func Latest(paths Paths, project string) (dir string, err error) {
	projectDir := paths.projectDir(project)
	entries, err := os.ReadDir(projectDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("reading %s: %w", projectDir, err)
		return
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() && versionPattern.MatchString(entry.Name()) {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) == 0 {
		err = fmt.Errorf("no Base image for project %q in %s: run `afk image build`", project, projectDir)
		return
	}
	dir = filepath.Join(projectDir, slices.Max(versions))
	return
}

// newVersion names the Base image version built at t.
func newVersion(t time.Time) (version string) { return t.UTC().Format(versionFormat) }

// binDir holds Packer binaries (packer-<version>).
func (p Paths) binDir() (dir string) { return filepath.Join(p.CacheDir, "bin") }

// pluginDir is PACKER_PLUGIN_PATH.
func (p Paths) pluginDir() (dir string) { return filepath.Join(p.CacheDir, "plugins") }

// packerConfigDir is PACKER_CONFIG_DIR.
func (p Paths) packerConfigDir() (dir string) { return filepath.Join(p.CacheDir, "packer-config") }

// sshDir holds the `afk` ssh key pair.
func (p Paths) sshDir() (dir string) { return filepath.Join(p.StateDir, "ssh") }

// projectDir holds the versions of project's Base image.
func (p Paths) projectDir(project string) (dir string) { return filepath.Join(p.BaseDir, project) }

// ensureSharedDir creates dir if needed and makes it group gid's, mode
// setgid 2775, so images stay readable by libvirt-qemu. The result is checked,
// because a directory afk does not own cannot be fixed.
func ensureSharedDir(dir string, gid int) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w (%s must belong to group kvm and be writable by the user)", err, dir)
		}
	}()
	if err = os.MkdirAll(dir, sharedDirMode); err != nil {
		err = fmt.Errorf("creating %s: %w", dir, err)
		return
	}
	info, err := os.Stat(dir)
	if err != nil {
		return
	}
	// chown/chmod by a non-owner fail even when nothing would change.
	if info.Sys().(*syscall.Stat_t).Gid != uint32(gid) {
		if err = os.Chown(dir, -1, gid); err != nil {
			err = fmt.Errorf("setting group of %s: %w", dir, err)
			return
		}
	}
	if info.Mode()&(os.ModeSetgid|os.ModePerm) != sharedDirMode {
		if err = os.Chmod(dir, sharedDirMode); err != nil {
			err = fmt.Errorf("setting mode of %s: %w", dir, err)
			return
		}
	}
	if info, err = os.Stat(dir); err != nil {
		return
	}
	if got := info.Sys().(*syscall.Stat_t).Gid; got != uint32(gid) || info.Mode()&(os.ModeSetgid|os.ModePerm) != sharedDirMode {
		err = fmt.Errorf("%s has group %d and mode %v, want group %d and setgid 2775", dir, got, info.Mode(), gid)
	}
	return
}
