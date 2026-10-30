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
	"github.com/graf0/afk/stack"
)

// Leftover is the state of a Run an earlier Factory left: after a failure or
// Ctrl-C the Stack goes down, but its clone and home stay (docs/workflow.md).
type Leftover struct {
	Name string // of the Stack, and of its directory in the Project's state
	// Ticket is the ticket the Run was for: a child of a Spec, the Spec
	// itself for its review. Zero for the state of a preflight.
	Ticket int
}

// ListLeftovers returns the Runs of project that have state, by ticket: the
// state directory is the only record (ADR-0015).
func ListLeftovers(stateDir, project string) (leftovers []Leftover, err error) {
	names, err := stack.List(stateDir, project, stack.Run)
	if err != nil {
		return
	}
	for _, name := range names {
		if n, ok := leftoverTicket(name); ok {
			leftovers = append(leftovers, Leftover{Name: name, Ticket: n})
		}
	}
	slices.SortStableFunc(leftovers, func(a, b Leftover) int { return cmp.Compare(a.Ticket, b.Ticket) })
	return
}

// ticketName is the name of a ticket's branch and of the Stack of its Run.
func ticketName(n int) (name string) { return fmt.Sprintf("ticket-%d", n) }

// ticketNumber is the inverse of ticketName: ok is false for any other name.
func ticketNumber(name string) (n int, ok bool) {
	digits, found := strings.CutPrefix(name, "ticket-")
	if !found {
		return
	}
	n, err := strconv.Atoi(digits)
	ok = err == nil && n > 0 && ticketName(n) == name
	return
}

// leftoverTicket reads the ticket from the name of a Run's Stack; ok is
// false for what is no Run of a Factory. The state of a preflight has ticket 0.
func leftoverTicket(name string) (n int, ok bool) {
	if rest, found := strings.CutPrefix(name, preflightPrefix); found && rest != "" {
		return 0, true
	}
	return ticketNumber(name)
}

// removeLeftover removes the Stack, volumes and state of l: its Run is over.
func removeLeftover(ctx context.Context, stateDir, project string, l Leftover) (err error) {
	st, err := stack.Open(stateDir, project, stack.Run, l.Name)
	if err != nil {
		return
	}
	err = st.Remove(ctx)
	return
}

// cleanBranch drops the refs of branch in the Quarantine repo, and the
// branch on the Forge when it has no pull request. A branch with a pull
// request is never deleted, and one that moved since it was read stays.
func cleanBranch(ctx context.Context, g forge.Forge, q Quarantine, branch string, out io.Writer) (err error) {
	if err = q.Drop(ctx, branch); err != nil {
		return
	}
	remote := g.Remote()
	tip, err := q.RemoteTip(ctx, remote, branch)
	if err != nil || tip == "" {
		return
	}
	pull, found, err := g.FindPull(ctx, branch)
	if err != nil {
		return
	}
	if found {
		_, _ = fmt.Fprintf(out, "= branch %s stays on %s: pull request #%d\n", branch, remote.Repo, pull.Number)
		return
	}
	if err = q.DeleteRemote(ctx, remote, branch, tip); err == nil {
		_, _ = fmt.Fprintf(out, "= branch %s without a pull request deleted from %s\n", branch, remote.Repo)
	}
	return
}
