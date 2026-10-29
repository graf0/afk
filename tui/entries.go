package tui

import (
	"cmp"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/run"
)

// factoryFormat lists the panes of the session with what parseFactories
// reads; the title goes last, it may hold anything. @afk-main marks the
// Factory pane itself (pane option), whatever its index.
const factoryFormat = "#{window_name}\t#{pane_id}\t#{@afk-main}\t#{@afk-kind}\t#{@afk-project}\t#{@afk-repo}\t#{pane_dead}\t#{pane_dead_status}\t#{pane_dead_signal}\t#{pane_title}"

// KindFactory is the Kind of the entries made of tmux windows running
// `afk factory`; no machine has it, the type is only shared.
const KindFactory machine.Kind = "factory"

// Entry is a line of the sidebar: a Chat, an abandoned Run or a Factory
// started in the TUI.
type Entry struct {
	Name    string // libvirt name of the VM; the tmux window is called the same
	Project string
	Kind    machine.Kind
	ID      string
	Repo    string // the Project's repository on the host; may be empty
	Running bool   // the VM runs; the Factory has not ended
	Title   string // Factory: the pane title (OSC 2), its header
	Pane    string // Factory: tmux id of its pane (%N)
	Exit    string // Factory, once ended: "exit 3", "signal 2" or "?"
}

// groupEntries keeps the Chats and the Run VMs of a Factory among machines
// (of any Project), adds factories and groups them by Project: by Project,
// then by kind (Chats, Factories, Runs), by id.
func groupEntries(machines []machine.Machine, factories []Entry) (entries []Entry) {
	for _, m := range machines {
		if m.Kind == machine.KindChat || run.IsLeftover(m) {
			entries = append(entries, Entry{Name: m.Name, Project: m.Project, Kind: m.Kind, ID: m.ID, Repo: m.Repo, Running: m.Running})
		}
	}
	entries = append(entries, factories...)
	slices.SortFunc(entries, func(a, b Entry) int {
		return cmp.Or(
			cmp.Compare(a.Project, b.Project),
			cmp.Compare(a.Kind, b.Kind), // "chat", "factory", "run"
			cmp.Compare(a.ID, b.ID),
		)
	})
	return
}

// listEntries reads the entries from libvirt and the Factories from tmux. A
// tmux failure comes back as err together with the entries of libvirt.
func listEntries(ctx context.Context) (entries []Entry, err error) {
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	machines, err := conn.ListAll()
	if err != nil {
		return
	}
	factories, err := listFactories(ctx)
	entries = groupEntries(machines, factories)
	return
}

// listFactories reads the Factories from the options of the tmux windows.
func listFactories(ctx context.Context) (factories []Entry, err error) {
	out, err := exec.CommandContext(ctx, "tmux", tmuxArgs("list-panes", "-s", "-t", session, "-F", factoryFormat)...).Output()
	if err != nil {
		err = fmt.Errorf("listing Factories in tmux: %w", err)
		return
	}
	factories = parseFactories(string(out))
	return
}

// parseFactories reads factoryFormat lines: the Factory is the pane marked
// @afk-main in a window with @afk-kind=factory (the other is the sidebar).
func parseFactories(out string) (factories []Entry) {
	for line := range strings.Lines(out) {
		f := strings.SplitN(strings.TrimSuffix(line, "\n"), "\t", 10)
		if len(f) < 10 || f[2] != "1" || f[3] != string(KindFactory) {
			continue
		}
		id, ok := strings.CutPrefix(f[0], factoryPrefix)
		if !ok {
			continue
		}
		e := Entry{Name: f[0], Pane: f[1], Project: f[4], Kind: KindFactory, ID: id, Repo: f[5], Running: f[6] != "1", Title: f[9]}
		if !e.Running {
			e.Exit = exitLabel(f[7], f[8])
		}
		factories = append(factories, e)
	}
	return
}

// exitLabel says how a dead pane ended from tmux's exit status and signal (one
// of them is empty); "?" when both are.
func exitLabel(status, signal string) (label string) {
	switch {
	case status != "":
		label = "exit " + status
	case signal != "":
		label = "signal " + signal
	default:
		label = "?"
	}
	return
}
