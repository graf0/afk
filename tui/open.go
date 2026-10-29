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

// factoryPrefix starts the name of every Factory window; "/" is in no Chat or
// Run name, and "shell/" is the other prefix.
const factoryPrefix = "factory/"

// openEntry shows the window of e in the afk tmux server, creating it first
// when the Chat has none: the main pane runs `afk chat attach`, the sidebar
// sits on its left. An abandoned Run gets a note; acting on it is a shell
// away (`afk vm shell`).
func openEntry(ctx context.Context, exe string, e Entry) (err error) {
	if !safeArg(e.Name) || !safeArg(e.ID) {
		err = fmt.Errorf("opening %q: unusable name in the machine metadata", e.Name)
		return
	}
	var create func() []string // a Factory window is made by M-F only
	if e.Kind != KindFactory {
		create = func() []string { return openArgs(exe, e) }
	}
	err = showWindow(ctx, e.Name, create)
	return
}

// showWindow selects the window called name; a missing one is created first
// with the tmux commands of create (nil: the window must exist).
func showWindow(ctx context.Context, name string, create func() []string) (err error) {
	out, err := exec.CommandContext(ctx, "tmux", tmuxArgs("list-windows", "-t", session, "-F", "#{window_name}")...).Output()
	if err != nil {
		err = fmt.Errorf("listing tmux windows: %w", err)
		return
	}
	args, err := showArgs(string(out), name, create)
	if err != nil {
		return
	}
	if out, err = exec.CommandContext(ctx, "tmux", tmuxArgs(args...)...).CombinedOutput(); err != nil {
		err = fmt.Errorf("opening window %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return
}

// showArgs is the tmux command selecting the window name, given the names of
// the existing windows in list, one per line; create makes a missing one.
func showArgs(list, name string, create func() []string) (args []string, err error) {
	if !strings.Contains("\n"+list, "\n"+name+"\n") {
		if create == nil {
			err = fmt.Errorf("window %s is gone", name)
			return
		}
		args = append(create(), ";")
	}
	args = append(args, "select-window", "-t", windowTarget(name))
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
	Options                           []option // applied to the main pane's window or pane
	PaneTitle                         string   // the main pane's title until the program sets its own
}

// option is a tmux option of a window (Scope "-w") or of the main pane ("-p").
type option struct{ Scope, Name, Value string }

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
	for _, o := range w.Options {
		args = append(args, ";", "set-option", o.Scope, "-t", target, o.Name, o.Value)
	}
	if w.PaneTitle != "" {
		args = append(args, ";", "select-pane", "-t", target, "-T", w.PaneTitle)
	}
	args = append(args, ";")
	args = append(args, sidebarSplit(target, exe, w.Current)...)
	return
}

// newFactoryWindow is the window of `afk factory selector...` (see
// factorySelector) of the Project in repo, called factoryPrefix+id. The
// options make it a Factory for the sidebar, keep its pane after the end
// (tmux shows the exit code) and show the pane title (OSC 2) on a border,
// not on the sidebar's.
func newFactoryWindow(exe, id, project, repo string, selector []string) (w window, err error) {
	_, ok := safeDir(repo)
	if !ok || !safeArg(project) || !safeArg(id) {
		err = fmt.Errorf("factory window in %q (Project %q): unusable path or name", repo, project)
		return
	}
	name := factoryPrefix + id
	w = window{
		Name: name, Current: name, Dir: repo, Project: project, Kind: string(KindFactory),
		Cmd:       append([]string{exe, "factory"}, selector...),
		PaneTitle: "afk factory", // not the host name tmux starts with
		Options: []option{
			{"-w", "@afk-repo", repo},
			{"-w", "pane-border-status", "top"},
			{"-w", "pane-border-format", "#{?pane_at_left, ,#{pane_title}}"},
			{"-p", "@afk-main", "1"},       // how the sidebar finds the pane, whatever its index
			{"-p", "remain-on-exit", "on"}, // the sidebar pane is not kept
		},
	}
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
