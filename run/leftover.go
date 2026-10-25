package run

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/machine"
)

// Leftover is a Run an earlier Factory left in its VM: after a failure or
// Ctrl-C the VM stays (docs/workflow.md).
type Leftover struct {
	Machine string // libvirt name of the VM
	// Ticket is the ticket the Run was for: a child of a Spec, the Spec
	// itself for its review. Zero for a VM of the preflight.
	Ticket int
}

// ListLeftovers returns the Run machines of project, by ticket, from the
// metadata of libvirt domains. Machines that are no Run of a Factory (`afk vm up`) are left out.
func ListLeftovers(project string) (leftovers []Leftover, err error) {
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	machines, err := conn.List(project)
	if err != nil {
		return
	}
	for _, m := range machines {
		if n, ok := leftoverTicket(m); ok {
			leftovers = append(leftovers, Leftover{Machine: m.Name, Ticket: n})
		}
	}
	slices.SortStableFunc(leftovers, func(a, b Leftover) int { return cmp.Compare(a.Ticket, b.Ticket) })
	return
}

// leftoverTicket reads the ticket from the id of a Run machine, which
// session makes <ticket>-<random>.
func leftoverTicket(m machine.Machine) (n int, ok bool) {
	number, _, found := strings.Cut(m.ID, "-")
	n, err := strconv.Atoi(number)
	ok = m.Kind == machine.KindRun && found && err == nil && n >= 0
	return
}

// cleanLeftover destroys the VM of l and, unless vmOnly, what its Run left
// of ticket-N. vmOnly is for a child of a Spec: its work is on the Spec's
// branch, which is not its to delete.
func cleanLeftover(ctx context.Context, g *forge.GitHub, q Quarantine, l Leftover, vmOnly bool, out io.Writer) (err error) {
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	err = conn.Kill(l.Machine)
	_ = conn.Close()
	if err != nil || vmOnly || l.Ticket == 0 {
		return
	}
	err = cleanBranch(ctx, g, q, fmt.Sprintf("ticket-%d", l.Ticket), out)
	return
}

// cleanBranch drops the refs of branch in the Quarantine repo, and the
// branch on the Forge when it has no pull request. A branch with a pull
// request is never deleted, and one that moved since it was read stays.
func cleanBranch(ctx context.Context, g *forge.GitHub, q Quarantine, branch string, out io.Writer) (err error) {
	if err = q.Drop(ctx, branch); err != nil {
		return
	}
	tip, err := q.RemoteTip(ctx, g.Remote(), branch)
	if err != nil || tip == "" {
		return
	}
	pull, found, err := g.FindPull(ctx, branch)
	if err != nil {
		return
	}
	if found {
		_, _ = fmt.Fprintf(out, "= branch %s stays on %s: pull request #%d\n", branch, g.Repo, pull.Number)
		return
	}
	if err = q.DeleteRemote(ctx, g.Remote(), branch, tip); err == nil {
		_, _ = fmt.Fprintf(out, "= branch %s without a pull request deleted from %s\n", branch, g.Repo)
	}
	return
}
