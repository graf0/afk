package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/graf0/afk/chat"
	afkconfig "github.com/graf0/afk/config"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
)

// actions is what the sidebar does besides listing: tmux windows and `afk`
// subcommands. Tests replace it.
type actions struct {
	Open       func(Entry) error
	NewChat    func(name, repo string) error              // window running `afk chat new`, shown
	NewFactory func(repo string, selector []string) error // window running `afk factory`, shown
	Interrupt  func(pane string) error                    // Ctrl-C to the Factory pane (%N)
	Shell      func(Entry) error                          // window running a shell in the VM, shown
	Run        func(dir string, args ...string) (out string, code int, err error)
	Detach     func(window string) error // ends the main pane (`afk chat attach`) and waits for it
	Close      func(window string) error // selects home, kills the window
	Focus      func(on bool)             // the keyboard goes to the sidebar pane for a prompt
}

// realActions runs the actions on the afk tmux server; subcommands are exe.
func realActions(ctx context.Context, exe string) (act actions) {
	tmux := func(args ...string) (out string, err error) {
		combined, err := exec.CommandContext(ctx, "tmux", tmuxArgs(args...)...).CombinedOutput()
		out = strings.TrimSpace(string(combined))
		return
	}
	act = actions{
		Open: func(e Entry) error { return openEntry(ctx, exe, e) },
		NewChat: func(name, repo string) (err error) {
			vm, err := machine.Name(machine.KindChat, name)
			if err != nil {
				err = fmt.Errorf("new chat %q: %w", name, err)
				return
			}
			err = showWindow(ctx, vm, func() []string {
				return windowArgs(exe, window{Name: vm, Current: vm, Dir: repo, Kind: string(machine.KindChat), Cmd: keepOnFailure(exe, "chat", "new", name)})
			})
			return
		},
		NewFactory: func(repo string, selector []string) (err error) {
			cfg, err := afkconfig.Load(repo)
			if err != nil {
				err = fmt.Errorf("new Factory: %w", err)
				return
			}
			project, err := image.ProjectName(cfg.Forge.Repo)
			if err != nil {
				err = fmt.Errorf("new Factory: %w", err)
				return
			}
			// Millisecond ids: two Factories of a Project get two windows.
			w, err := newFactoryWindow(exe, strconv.FormatInt(time.Now().UnixMilli(), 36), project, repo, selector)
			if err != nil {
				return
			}
			err = showWindow(ctx, w.Name, func() []string { return windowArgs(exe, w) })
			return
		},
		Interrupt: func(pane string) (err error) {
			if out, sendErr := tmux("send-keys", "-t", pane, "C-c"); sendErr != nil {
				err = fmt.Errorf("interrupting the Factory in %s: %w: %s", pane, sendErr, out)
			}
			return
		},
		Shell: func(e Entry) (err error) {
			if !safeArg(e.Name) {
				err = fmt.Errorf("shell in %q: unusable name in the machine metadata", e.Name)
				return
			}
			args := []string{"vm", "shell", e.Name}
			if e.Kind == machine.KindChat {
				args = []string{"chat", "shell", e.ID}
			}
			err = showWindow(ctx, shellWindow(e), func() []string {
				return windowArgs(exe, window{Name: shellWindow(e), Current: e.Name, Dir: e.Repo, Cmd: keepOnFailure(exe, args...)})
			})
			return
		},
		Run: func(dir string, args ...string) (out string, code int, err error) {
			cmd := exec.CommandContext(ctx, exe, args...)
			cmd.Dir = dir
			combined, err := cmd.CombinedOutput()
			out = strings.TrimSpace(string(combined))
			if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exitErr.ExitCode()
			}
			return
		},
		Detach: func(window string) (err error) {
			// Pane 0 is the sidebar (split to the left), pane 1 the main one.
			pane := windowTarget(window) + ".1"
			out, perr := tmux("display-message", "-p", "-t", pane, "#{pane_pid}")
			pid, convErr := strconv.Atoi(out)
			if perr != nil || convErr != nil {
				return // no main pane: nothing holds the Chat
			}
			if out, err = tmux("kill-pane", "-t", pane); err != nil {
				err = fmt.Errorf("ending the session in %s: %w: %s", window, err, out)
				return
			}
			for range 50 {
				if syscall.Kill(pid, 0) != nil {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			err = fmt.Errorf("the session in %s did not end", window)
			return
		},
		Close: func(window string) (err error) {
			_, _ = tmux("select-window", "-t", windowTarget("home"))
			if out, killErr := tmux("kill-window", "-t", windowTarget(window)); killErr != nil {
				err = fmt.Errorf("closing window %s: %w: %s", window, killErr, out)
			}
			return
		},
		Focus: func(on bool) {
			args := []string{"select-pane", "-l"} // back to the pane the keys came from
			if pane := os.Getenv("TMUX_PANE"); on && pane != "" {
				args = []string{"select-pane", "-t", pane}
			}
			_, _ = tmux(args...)
		},
	}
	return
}

// keepOnFailure runs exe with args and, when it fails, waits for Enter so the
// error stays readable in the window.
func keepOnFailure(exe string, args ...string) (cmd []string) {
	cmd = append([]string{"sh", "-c", `"$@" || { echo "afk: exit $?"; read -r _; }`, "sh", exe}, args...)
	return
}

// shellWindow is the tmux window of the shell in e; "/" is not in any id, so
// it never equals the name of an entry.
func shellWindow(e Entry) (name string) {
	name = "shell/" + e.Name
	return
}

// --- command construction ---

// exportArgs, rmArgs and killArgs are the `afk` subcommands of the actions;
// ids come from libvirt metadata and are validated.
func exportArgs(e Entry) (args []string, err error) {
	if e.Kind != machine.KindChat {
		err = errors.New("only a Chat can be exported")
		return
	}
	if err = chat.ValidateName(e.ID); err != nil {
		return
	}
	args = []string{"chat", "export", e.ID}
	return
}

func rmArgs(e Entry, force bool) (args []string, err error) {
	if err = chat.ValidateName(e.ID); err != nil {
		return
	}
	args = []string{"chat", "rm"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, e.ID)
	return
}

func killArgs(e Entry) (args []string, err error) {
	if _, err = machine.Name(e.Kind, e.ID); err != nil {
		return
	}
	if !safeArg(e.Name) {
		err = fmt.Errorf("unusable name %q", e.Name)
		return
	}
	args = []string{"vm", "kill", e.Name}
	return
}

// factorySelector turns what the user typed into the arguments of
// `afk factory`: "*" (also for nothing) or ticket numbers, "#N" or "N". They
// go to the command line as they are, so only numbers are let through.
func factorySelector(input string) (selector []string, err error) {
	fields := strings.Fields(input)
	if len(fields) == 0 || len(fields) == 1 && fields[0] == "*" {
		selector = []string{"*"}
		return
	}
	for _, f := range fields {
		n, convErr := strconv.Atoi(strings.TrimPrefix(f, "#"))
		if convErr != nil || n <= 0 {
			selector, err = nil, fmt.Errorf("selector: %q is no ticket number; use * or e.g. #12 #15", f)
			return
		}
		selector = append(selector, strconv.Itoa(n))
	}
	return
}

// --- Project resolution ---

// findProject returns the directory of the Project containing dir: the
// nearest one up the tree with .afk.toml.
func findProject(dir string) (project string, ok bool) {
	if !filepath.IsAbs(dir) {
		return
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, afkconfig.FileName)); err == nil {
			return d, true
		}
		if d == filepath.Dir(d) {
			return
		}
	}
}

// projectFor is the directory `afk chat new` runs in: the Project of the
// selected entry (selected may be nil), else of the current directory.
func projectFor(selected *Entry, cwd string) (project string, err error) {
	ok := false
	if selected != nil {
		project, ok = findProject(selected.Repo)
	}
	if !ok {
		project, ok = findProject(cwd)
	}
	if !ok {
		err = errors.New("no Project: select an entry or enter a directory with .afk.toml")
	}
	return
}
