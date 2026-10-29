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
	err = showWindow(ctx, e.Name, func() []string { return openArgs(exe, e) })
	return
}

// showWindow selects the window called name; a missing one is created first
// with the tmux commands of create.
func showWindow(ctx context.Context, name string, create func() []string) (err error) {
	out, err := exec.CommandContext(ctx, "tmux", tmuxArgs("list-windows", "-t", session, "-F", "#{window_name}")...).Output()
	if err != nil {
		err = fmt.Errorf("listing tmux windows: %w", err)
		return
	}
	var args []string
	if !strings.Contains("\n"+string(out), "\n"+name+"\n") {
		args = append(create(), ";")
	}
	args = append(args, "select-window", "-t", windowTarget(name))
	if out, err = exec.CommandContext(ctx, "tmux", tmuxArgs(args...)...).CombinedOutput(); err != nil {
		err = fmt.Errorf("opening window %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return
}

// windowTarget is the tmux target of the window called name, exactly.
func windowTarget(name string) (target string) {
	target = session + ":=" + name
	return
}

// openArgs creates the window of e in the background: the main pane, the
// options saying what it is, and the sidebar on its left.
func openArgs(exe string, e Entry) (args []string) {
	cmd := []string{exe, "chat", "attach", e.ID}
	if e.Kind != machine.KindChat {
		cmd = []string{"sh", "-c", `echo "Run $0 left in its VM: afk vm shell $0 / afk vm kill $0"; exec tail -f /dev/null`, e.Name}
	}
	args = windowArgs(exe, window{Name: e.Name, Current: e.Name, Dir: e.Repo, Project: e.Project, Kind: string(e.Kind), Cmd: cmd})
	return
}

// window describes a tmux window of afk: its main pane runs Cmd in Dir, its
// sidebar shows the entry Current; Kind and Project, when not empty, are set
// as the options @afk-kind and @afk-project. The strings come from libvirt
// metadata or user input.
type window struct {
	Name, Current, Dir, Project, Kind string
	Cmd                               []string
}

// windowArgs creates w in the background, the sidebar on its left.
func windowArgs(exe string, w window) (args []string) {
	target := windowTarget(w.Name)
	args = []string{"new-window", "-d", "-t", session + ":", "-n", w.Name}
	// tmux expands formats in -c.
	if safe, ok := safeDir(w.Dir); ok {
		args = append(args, "-c", safe)
	}
	args = append(args, w.Cmd...)
	if w.Kind != "" {
		args = append(args, ";", "set-option", "-w", "-t", target, "@afk-kind", w.Kind)
	}
	if safeArg(w.Project) {
		args = append(args, ";", "set-option", "-w", "-t", target, "@afk-project", w.Project)
	}
	args = append(args, ";")
	args = append(args, sidebarSplit(target, exe, w.Current)...)
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
