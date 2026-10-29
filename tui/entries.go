package tui

import (
	"cmp"
	"slices"

	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/run"
)

// Entry is a line of the sidebar: a Chat or an abandoned Run.
type Entry struct {
	Name    string // libvirt name of the VM; the tmux window is called the same
	Project string
	Kind    machine.Kind
	ID      string
	Repo    string // the Project's repository on the host; may be empty
	Running bool
}

// groupEntries keeps the Chats and the Run VMs of a Factory among machines
// (of any Project), grouped by Project: by Project, Chats before Runs, by id.
func groupEntries(machines []machine.Machine) (entries []Entry) {
	for _, m := range machines {
		if m.Kind == machine.KindChat || run.IsLeftover(m) {
			entries = append(entries, Entry{Name: m.Name, Project: m.Project, Kind: m.Kind, ID: m.ID, Repo: m.Repo, Running: m.Running})
		}
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		return cmp.Or(
			cmp.Compare(a.Project, b.Project),
			cmp.Compare(a.Kind, b.Kind), // "chat" before "run"
			cmp.Compare(a.ID, b.ID),
		)
	})
	return
}

// listEntries reads the entries from libvirt.
func listEntries() (entries []Entry, err error) {
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	machines, err := conn.ListAll()
	if err != nil {
		return
	}
	entries = groupEntries(machines)
	return
}
