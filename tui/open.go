package tui

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/graf0/afk/machine"
)

// openEntry shows the window of e in the afk tmux server, creating it first
// when the Chat has none: the main pane runs `afk chat attach`, the sidebar
// sits on its left. An abandoned Run gets a note; acting on it is a shell
// away (`afk vm shell`).
func openEntry(ctx context.Context, exe string, e Entry) (err error) {
	if !safeArg(e.Name) || !safeArg(e.ID) {
		err = fmt.Errorf("opening %q: unusable name in the machine metadata", e.Name)
		return
	}
	target := session + ":=" + e.Name
	out, err := exec.CommandContext(ctx, "tmux", tmuxArgs("list-windows", "-t", session, "-F", "#{window_name}")...).Output()
	if err != nil {
		err = fmt.Errorf("listing tmux windows: %w", err)
		return
	}
	var args []string
	if !strings.Contains("\n"+string(out), "\n"+e.Name+"\n") {
		args = append(openArgs(exe, e, target), ";")
	}
	args = append(args, "select-window", "-t", target)
	if out, err = exec.CommandContext(ctx, "tmux", tmuxArgs(args...)...).CombinedOutput(); err != nil {
		err = fmt.Errorf("opening window %s: %w: %s", e.Name, err, strings.TrimSpace(string(out)))
	}
	return
}

// openArgs creates the window of e (called target) in the background: the
// main pane, the options saying what it is, and the sidebar on its left.
func openArgs(exe string, e Entry, target string) (args []string) {
	args = []string{"new-window", "-d", "-t", session + ":", "-n", e.Name}
	// The repo comes from libvirt metadata and tmux expands formats in -c.
	if repo, ok := safeDir(e.Repo); ok {
		args = append(args, "-c", repo)
	}
	if e.Kind == machine.KindChat {
		args = append(args, exe, "chat", "attach", e.ID)
	} else {
		args = append(args, "sh", "-c", `echo "Run $0 left in its VM: afk vm shell $0 / afk vm kill $0"; exec tail -f /dev/null`, e.Name)
	}
	args = append(args, ";", "set-option", "-w", "-t", target, "@afk-kind", string(e.Kind))
	if safeArg(e.Project) {
		args = append(args, ";", "set-option", "-w", "-t", target, "@afk-project", e.Project)
	}
	args = append(args, ";")
	args = append(args, sidebarSplit(target, exe, e.Name)...)
	return
}

// safeDir returns dir for tmux's -c: an absolute clean path with `#` escaped
// (no format expansion). Anything else is not passed.
func safeDir(dir string) (safe string, ok bool) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !safeArg(dir) {
		return
	}
	return strings.ReplaceAll(dir, "#", "##"), true
}

// safeArg reports whether s can be a tmux argument as it is: no control
// characters, and no trailing `;`, which tmux reads as a command separator.
func safeArg(s string) (ok bool) {
	return s != "" && !strings.HasSuffix(s, ";") && !strings.ContainsFunc(s, unicode.IsControl)
}
