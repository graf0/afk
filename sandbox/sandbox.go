// Package sandbox is the view of the host the Agent gets in the Sandbox of a
// Stack, for a Chat or a Run alike: the Agent's identity and token, the host's
// claude, the Claude settings and plugins, and the mount points those need in
// the Agent's home. The Agent owns its home and clone, so everything here
// treats them as untrusted: files only, never git, and a symlink in the way is
// removed, never followed or written through (ADR-0002).
package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/stack"
)

const (
	// GitName and GitEmail are the commit identity of the Agent; it has no other.
	GitName  = "afk agent"
	GitEmail = "agent@afk.invalid"

	settingsName = "settings.json"
)

// Options describe the view of the Agent.
type Options struct {
	// ContextDir holds the files the host writes for the Agent: settings and
	// the plugin metadata. Each is mounted read-only from there, one by one.
	ContextDir string
	// DataDir is the writable data directory of the plugins: the Agent's own.
	DataDir string
	// Settings is the Project's .claude/settings.json (nil: none), and Plugins
	// the effective plugins: all enabled for the Agent (ADR-0009).
	Settings []byte
	Plugins  []string
	// NoClaude leaves out the host's claude, mounted read-only into the
	// Agent's $HOME/bin unless set (tests give their own).
	NoClaude bool
}

// View is what the host gives the Sandbox: Files for ContextDir and Mounts.
// The caller adds its own files and mounts, then Prepare writes and prepares
// them all. It is the one list: the Stack mounts them, Prepare makes their
// mount points.
type View struct {
	Files  map[string][]byte
	Mounts []stack.Mount

	contextDir, dataDir string
	home                string // of the host user: where the Sandbox sees the Agent's
}

// Build is the view of the Agent for opts.
func Build(opts Options) (v View, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		err = fmt.Errorf("finding the home directory: %w", err)
		return
	}
	v = View{contextDir: opts.ContextDir, dataDir: opts.DataDir, home: home}
	settings, err := claudeSettings(opts.Settings, opts.Plugins)
	if err != nil {
		return
	}
	v.Files = map[string][]byte{settingsName: settings}
	v.Mounts = []stack.Mount{{Source: filepath.Join(opts.ContextDir, settingsName), Target: filepath.Join(home, ".claude", settingsName), ReadOnly: true}}
	if !opts.NoClaude {
		var binary string
		if binary, err = hostClaude(); err != nil {
			return
		}
		v.Mounts = append(v.Mounts, stack.Mount{Source: binary, Target: filepath.Join(home, "bin", "claude"), ReadOnly: true})
	}
	pluginFiles, pluginMounts, err := pluginView(home, opts.ContextDir, opts.DataDir, opts.Plugins)
	if err != nil {
		return
	}
	for name, data := range pluginFiles {
		v.Files[name] = data
	}
	v.Mounts = append(v.Mounts, pluginMounts...)
	return
}

// Prepare writes the files into the context directory, creates the data
// directory and agentHome (the host side of the Agent's $HOME), and makes the
// mount points the mounts and the tree need in it. The clone and home may be
// the Agent's work of an earlier start: nothing of the Agent may be running
// (the caller brings the Stack down first), or it could swap a symlink in.
func (v View) Prepare(agentHome, tree string) (err error) {
	if err = os.MkdirAll(v.contextDir, 0o755); err != nil {
		err = fmt.Errorf("creating %s: %w", v.contextDir, err)
		return
	}
	for name, data := range v.Files {
		if err = os.WriteFile(filepath.Join(v.contextDir, name), data, 0o644); err != nil {
			err = fmt.Errorf("writing %s: %w", name, err)
			return
		}
	}
	for _, dir := range []string{agentHome, v.dataDir} {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			err = fmt.Errorf("creating %s: %w", dir, err)
			return
		}
	}
	err = mountPoints(v.home, agentHome, tree, v.Mounts)
	return
}

// Env is the environment of the Sandbox: the Agent's git identity and the
// Claude token (when there is one).
func Env(claudeToken string) (env map[string]string) {
	env = map[string]string{
		"GIT_AUTHOR_NAME": GitName, "GIT_AUTHOR_EMAIL": GitEmail,
		"GIT_COMMITTER_NAME": GitName, "GIT_COMMITTER_EMAIL": GitEmail,
		"DISABLE_AUTOUPDATER": "1",
	}
	if claudeToken != "" {
		env[agent.TokenEnv] = claudeToken
	}
	return
}

// hostClaude is the file of the host's claude: the target of a symlink, which
// would be a dangling link in the Sandbox.
func hostClaude() (path string, err error) {
	found, err := exec.LookPath("claude")
	if err != nil {
		err = fmt.Errorf("claude is not on the PATH: %w", err)
		return
	}
	path, err = filepath.EvalSymlinks(found)
	return
}

// mountPoints makes the mount points of tree and mounts that lie under the
// host's home in the Agent's home on the host: the Sandbox sees the Agent's
// home under the path of the host's own, so a mount there needs a point in it.
// podman makes the points it finds missing as a subuid. Targets outside the
// home need nothing.
func mountPoints(hostHome, agentHome, tree string, mounts []stack.Mount) (err error) {
	points := append([]stack.Mount{{Target: tree, Dir: true}}, mounts...)
	for _, m := range points {
		rel, relErr := filepath.Rel(hostHome, m.Target)
		if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			continue // not in the home: no mount point of ours
		}
		if err = mountPoint(agentHome, rel, m.Dir); err != nil {
			err = fmt.Errorf("mount point %s: %w", m.Target, err)
			return
		}
	}
	return
}

// mountPoint makes rel in the Agent's home what a mount over it needs: a
// directory or a file, never a symlink. Walking down, a link or a thing of the
// wrong type is removed (a link goes, not what it points to); a missing file
// is created with O_EXCL and O_NOFOLLOW, so even a link put there meanwhile is
// not written through; a plain file that is there stays as it is.
func mountPoint(home, rel string, isDir bool) (err error) {
	path := home
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		path = filepath.Join(path, part)
		wantDir := i < len(parts)-1 || isDir
		info, statErr := os.Lstat(path)
		if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || info.IsDir() != wantDir) {
			if err = os.RemoveAll(path); err != nil {
				return
			}
			statErr = fs.ErrNotExist
		}
		switch {
		case errors.Is(statErr, fs.ErrNotExist) && wantDir:
			err = os.Mkdir(path, 0o755)
		case errors.Is(statErr, fs.ErrNotExist):
			var file *os.File
			if file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644); err == nil {
				err = file.Close()
			}
		default:
			err = statErr
		}
		if err != nil {
			return
		}
	}
	return
}
