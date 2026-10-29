// Package tui is the terminal UI of afk: a tmux server with a sidebar
// (ADR-0008).
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	// socket and session name the afk server of tmux (`tmux -L afk`).
	socket  = "afk"
	session = "afk"
	// sidebarWidth is the width of the left pane in columns.
	sidebarWidth = 32

	// minMajor.minMinor is the oldest tmux afk works with.
	minMajor, minMinor = 3, 3
)

var minVersion = fmt.Sprintf("%d.%d", minMajor, minMinor)

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

// parseVersion reads the major and minor number from the output of
// `tmux -V`: "tmux 3.5a", "tmux next-3.6".
func parseVersion(out string) (major, minor int, err error) {
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		err = fmt.Errorf("cannot read the tmux version from %q", out)
		return
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return
}

// checkVersion fails when tmux is missing or older than minVersion.
func checkVersion(ctx context.Context) (err error) {
	out, err := exec.CommandContext(ctx, "tmux", "-V").Output()
	if err != nil {
		err = fmt.Errorf("afk tui needs tmux %s or newer: running tmux -V: %w", minVersion, err)
		return
	}
	major, minor, err := parseVersion(string(out))
	if err != nil {
		err = fmt.Errorf("afk tui needs tmux %s or newer: %w", minVersion, err)
		return
	}
	if major < minMajor || major == minMajor && minor < minMinor {
		err = fmt.Errorf("afk tui needs tmux %s or newer, found %d.%d", minVersion, major, minor)
	}
	return
}

// Run starts the afk tmux server with a session of the sidebar and attaches
// the terminal to it. When the session exists (a second `afk tui`, or after a
// detach) it only attaches. Closing the terminal detaches: windows live on in
// the server.
func Run(ctx context.Context, exe string) (err error) {
	if err = checkVersion(ctx); err != nil {
		return
	}
	args := attachArgs()
	if exec.CommandContext(ctx, "tmux", tmuxArgs("has-session", "-t", session)...).Run() != nil {
		args = startArgs(exe)
	}
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = withoutTmux(os.Environ()) // the afk server is no nested tmux
	if err = cmd.Run(); err != nil {
		err = fmt.Errorf("running tmux: %w", err)
	}
	return
}

// --- command construction ---

// tmuxArgs prefixes args with the options choosing the afk server; -f
// /dev/null keeps ~/.tmux.conf out.
func tmuxArgs(args ...string) (full []string) {
	full = append([]string{"-L", socket, "-f", "/dev/null"}, args...)
	return
}

func attachArgs() (args []string) {
	args = tmuxArgs("attach-session", "-t", session)
	return
}

// startArgs is one tmux invocation: it creates the session with the "home"
// window (sidebar and a placeholder), applies the embedded configuration and
// attaches. Created together with the attach, the layout gets the real size
// of the terminal.
func startArgs(exe string) (args []string) {
	args = tmuxArgs("new-session", "-s", session, "-n", "home", "tail", "-f", "/dev/null")
	for _, cmd := range config() {
		args = append(args, ";")
		args = append(args, cmd...)
	}
	args = append(args, ";")
	args = append(args, sidebarSplit("", exe, "")...)
	return
}

// config is the tmux configuration of afk, one command per element. M-Up and
// M-Down need no prefix; they go to the sidebar (pane 0 of every window),
// which moves the selection and switches the window. Other keys reach the
// Agent. The status line is off, the window is the sidebar and the Agent.
func config() (cmds [][]string) {
	cmds = [][]string{
		{"set-option", "-g", "status", "off"},
		{"set-option", "-g", "pane-base-index", "0"},
		{"set-option", "-g", "escape-time", "10"},
		{"set-option", "-g", "prefix", "None"}, // Ctrl+B belongs to the Agent; closing the terminal detaches
		{"set-option", "-g", "prefix2", "None"},
		{"unbind-key", "C-b"},
		{"bind-key", "-n", "M-Up", "send-keys", "-t", ":.0", "Up"},
		{"bind-key", "-n", "M-Down", "send-keys", "-t", ":.0", "Down"},
	}
	return
}

// sidebarSplit puts the sidebar on the left of window target (the current
// one when empty) without taking the focus. current is the entry the
// window shows.
func sidebarSplit(target, exe, current string) (args []string) {
	args = []string{"split-window", "-hbd", "-l", strconv.Itoa(sidebarWidth)}
	if target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, exe, "sidebar")
	if current != "" {
		args = append(args, "--current", current)
	}
	return
}

func withoutTmux(env []string) (out []string) {
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TMUX=") {
			out = append(out, kv)
		}
	}
	return
}
