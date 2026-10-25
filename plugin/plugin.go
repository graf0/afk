// Package plugin picks the host user's Claude Code plugins for a Sandbox VM
// and packs their directories. Nothing else from ~/.claude is ever read
// into the archive (docs/workflow.md, "Pluginy użytkownika").
package plugin

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// userScope is the scope of installed_plugins.json entries that belong to
// the user, not to some other project's checkout.
const userScope = "user"

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Plugin is a plugin to copy into the guest.
type Plugin struct {
	ID  string // "<name>@<marketplace>"; the directory name in the guest
	Dir string // directory on the host, inside the plugins directory
}

// Install is an entry of installed_plugins.json.
type Install struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
}

// ValidID reports whether id is a plugin id, "<name>@<marketplace>", safe
// as a directory name and as a shell word.
func ValidID(id string) (ok bool) { return idPattern.MatchString(id) }

// Name is the plugin's name without the marketplace.
func (p Plugin) Name() (name string) {
	name, _, _ = strings.Cut(p.ID, "@")
	return
}

// --- selection ---

// ParseEnabled reads the plugins enabled in the host's settings.json.
func ParseEnabled(settings []byte) (enabled map[string]bool, err error) {
	var parsed struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err = json.Unmarshal(settings, &parsed); err != nil {
		err = fmt.Errorf("parsing settings.json: %w", err)
		return
	}
	enabled = parsed.EnabledPlugins
	return
}

// ParseInstalled reads installed_plugins.json.
func ParseInstalled(data []byte) (installed map[string][]Install, err error) {
	var parsed struct {
		Plugins map[string][]Install `json:"plugins"`
	}
	if err = json.Unmarshal(data, &parsed); err != nil {
		err = fmt.Errorf("parsing installed_plugins.json: %w", err)
		return
	}
	installed = parsed.Plugins
	return
}

// Select returns the plugins of allow that are enabled on the host, in the
// order of allow, and the ones it skipped because the host has them
// disabled (the user turned them off). A plugin absent from the host's
// installed plugins is an error, and so is an install path that is not
// exactly <root>/cache/<marketplace>/<name>/<version>.
func Select(enabled map[string]bool, installed map[string][]Install, allow []string, root string) (plugins []Plugin, disabled []string, err error) {
	root = filepath.Clean(root)
	for _, id := range allow {
		if !ValidID(id) {
			err = fmt.Errorf("invalid plugin id %q: want <name>@<marketplace>", id)
			return
		}
		i := slices.IndexFunc(installed[id], func(in Install) bool { return in.Scope == userScope })
		if i < 0 {
			err = fmt.Errorf("plugin %s is not installed on the host (user scope)", id)
			return
		}
		if !enabled[id] {
			disabled = append(disabled, id)
			continue
		}
		dir := filepath.Clean(installed[id][i].InstallPath)
		if !installPath(root, dir, id) {
			err = fmt.Errorf("plugin %s: install path %q is not %s/cache/<marketplace>/<name>/<version>", id, dir, root)
			return
		}
		plugins = append(plugins, Plugin{ID: id, Dir: dir})
	}
	return
}

// installPath reports whether dir is root/cache/<marketplace>/<name>/<version>
// of plugin id: nothing broader, like the whole cache, may be copied.
func installPath(root, dir, id string) (ok bool) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	name, market, _ := strings.Cut(id, "@")
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 4 && parts[0] == "cache" && parts[1] == market && parts[2] == name &&
		parts[3] != "" && parts[3] != "." && parts[3] != ".."
}

// within reports whether path is strictly inside dir; both are clean.
func within(dir, path string) (ok bool) {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// --- archive ---

// Tar writes the plugins' directories to w, each under <ID>/. Only
// directories and regular files are packed: symlinks (whatever they point
// to) and .git are left out, and a plugin directory that resolves outside
// root, the plugins directory, is an error.
func Tar(w io.Writer, root string, plugins []Plugin) (err error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		err = fmt.Errorf("resolving %s: %w", root, err)
		return
	}
	archive := tar.NewWriter(w)
	for _, p := range plugins {
		if err = addPlugin(archive, realRoot, p); err != nil {
			err = fmt.Errorf("packing plugin %s: %w", p.ID, err)
			return
		}
	}
	if err = archive.Close(); err != nil {
		err = fmt.Errorf("closing archive: %w", err)
	}
	return
}

// addPlugin writes one plugin directory to archive. Files with several hard
// links are an error: the other link may be a secret outside the plugin.
func addPlugin(archive *tar.Writer, realRoot string, p Plugin) (err error) {
	dir, err := filepath.EvalSymlinks(p.Dir)
	if err != nil {
		err = fmt.Errorf("resolving %s: %w", p.Dir, err)
		return
	}
	if !within(realRoot, dir) {
		err = fmt.Errorf("%s resolves outside %s", p.Dir, realRoot)
		return
	}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) (err error) {
		if walkErr != nil {
			err = walkErr
			return
		}
		if entry.Name() == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return
		}
		info, err := entry.Info()
		if err != nil {
			return
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && !entry.IsDir() && stat.Nlink > 1 {
			err = fmt.Errorf("%s has %d hard links", path, stat.Nlink)
			return
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return
		}
		// Neither owners nor names of the host leak into the guest, and no
		// setuid, setgid or sticky bits.
		header.Name = filepath.ToSlash(filepath.Join(p.ID, rel))
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		header.Mode &= 0o777
		if err = archive.WriteHeader(header); err != nil || entry.IsDir() {
			return
		}
		file, err := os.Open(path)
		if err != nil {
			return
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		_, err = io.Copy(archive, file)
		return
	})
	return
}
