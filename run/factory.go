package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
)

// Factory runs tickets one after another, like docs/reference/ralph.sh,
// main (docs/workflow.md, Factory). Execute, Leftovers and Cleanup are the
// seams to the VMs; NewFactory sets the real ones.
type Factory struct {
	Forge forge.Forge
	// Labels are the [labels] as of the start of the Factory: they hold for
	// all its decisions, and the preflight checks (once) that the
	// repository has every one of them.
	Labels   config.Labels
	Selector []int // the tickets to run, in this order; nil: every ready one, "*"
	Claims   Claims
	Out      io.Writer

	// ReadBase reads the base branch, once per Run: the same Base goes to
	// EnsureImage, Preflight and Execute, so they agree on the configuration.
	ReadBase func(ctx context.Context) (base Base, err error)
	// EnsureImage makes sure the Base image is there and current, building
	// it when not, whenever a new VM is to start from it. Its failure stops
	// the Factory.
	EnsureImage func(ctx context.Context, base Base) (err error)
	// Preflight checks that a Run can succeed, once, before the first one.
	Preflight func(ctx context.Context, base Base) (err error)
	// Leftovers lists the Runs earlier Factories left in VMs.
	Leftovers func() (leftovers []Leftover, err error)
	// Cleanup destroys the VM of a leftover Run and, unless vmOnly, what it
	// left on the Forge (not for a child of a Spec: its branch is the Spec's).
	Cleanup func(ctx context.Context, l Leftover, vmOnly bool) (err error)
	// Execute runs the ticket: a Spec when it has the label [labels].spec.
	// With resume it continues in that VM.
	Execute func(ctx context.Context, base Base, ticket forge.Ticket, resume *Leftover) (err error)

	handled     map[int]bool // run in this Factory: the list by label lags
	warned      map[int]bool
	preflighted bool
}

// NewFactory is a Factory of the Project of base that runs real Runs in
// VMs. Every Run reads the base branch again: it moves while a Factory works.
func NewFactory(ctx context.Context, base Base, gitForge forge.Forge, paths image.Paths, claudeToken string, selector []int, out io.Writer) (f *Factory, err error) {
	project, err := image.ProjectName(base.Config.Forge.Repo)
	if err != nil {
		return
	}
	stateDir := filepath.Join(paths.StateDir, project)
	q, err := OpenQuarantine(ctx, filepath.Join(stateDir, QuarantineName))
	if err != nil {
		return
	}
	options := func(base Base, ticket forge.Ticket) (opts Options) {
		return Options{Ticket: ticket, Base: base, ClaudeToken: claudeToken, Paths: paths, Out: out, Forge: gitForge}
	}
	claims := Claims{Dir: filepath.Join(paths.StateDir, "claims", project)}
	f = &Factory{
		Forge:    gitForge,
		Labels:   base.Config.Labels,
		Selector: selector,
		Claims:   claims,
		Out:      out,
		ReadBase: func(ctx context.Context) (current Base, err error) {
			current, err = ReadBase(ctx, base.Repo)
			// The Runs decide with the labels of the Factory: the ones its preflight checked.
			current.Config.Labels = f.Labels
			return
		},
		EnsureImage: func(ctx context.Context, current Base) (err error) {
			err = ensureSandboxImage(ctx, current, paths, out, image.Build)
			return
		},
		Preflight: func(ctx context.Context, current Base) (err error) {
			err = preflight(ctx, options(current, forge.Ticket{}), claims)
			return
		},
		Leftovers: func() (leftovers []Leftover, err error) { return ListLeftovers(project) },
		Cleanup: func(ctx context.Context, l Leftover, vmOnly bool) (err error) {
			return cleanLeftover(ctx, gitForge, q, l, vmOnly, out)
		},
		Execute: func(ctx context.Context, current Base, ticket forge.Ticket, resume *Leftover) (err error) {
			opts := options(current, ticket)
			opts.Resume = resume
			if f.isSpec(ticket) {
				err = ExecuteSpec(ctx, opts)
				return
			}
			err = Execute(ctx, opts)
			return
		},
	}
	return
}

// --- loop ---

// Run resumes the leftover Runs of the Project, then takes the tickets of
// the selector. A failed Run stops the Factory with its error; its VM stays,
// and running the Factory again continues it. needs_info does not stop it.
func (f *Factory) Run(ctx context.Context) (err error) {
	f.handled, f.warned, f.preflighted = map[int]bool{}, map[int]bool{}, false
	if err = f.resumeLeftovers(ctx); err != nil {
		return
	}
	if f.Selector == nil {
		for taken := true; taken && err == nil; {
			taken, err = f.runNext(ctx)
		}
		return
	}
	for _, n := range f.Selector {
		if f.handled[n] {
			continue
		}
		if err = f.runListed(ctx, n); err != nil {
			return
		}
	}
	return
}

// checkLabels refuses to start when the repository lacks a label of
// [labels]: afk doesn't create them. It is part of the preflight: it runs
// before the first Run, not for an empty queue.
func (f *Factory) checkLabels(ctx context.Context) (err error) {
	have, err := f.Forge.Labels(ctx)
	if err != nil {
		return
	}
	var missing []string
	for _, name := range f.Labels.Names() {
		if !forge.ContainsLabel(have, name) && !forge.ContainsLabel(missing, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		err = fmt.Errorf("preflight: the repository lacks the labels from [labels]: %s: create them, afk doesn't", strings.Join(missing, ", "))
	}
	return
}

// resumeLeftovers deals with the Runs earlier Factories left, before the
// queue (resume in ralph.sh, main). With a list selector only the Runs of
// the listed tickets (and of their Specs) are touched.
func (f *Factory) resumeLeftovers(ctx context.Context) (err error) {
	leftovers, err := f.Leftovers()
	if err != nil {
		return
	}
	for _, l := range leftovers {
		if err = f.resumeLeftover(ctx, l); err != nil {
			return
		}
	}
	return
}

// resumeLeftover claims the leftover first: the VM of a live Run of another
// Factory looks the same, and is never touched. Then the ticket is closed, so
// the VM and branch are cleaned up; it is ready, so the Run continues in the
// VM (holding the claim, with that of its Spec); otherwise the VM stays.
func (f *Factory) resumeLeftover(ctx context.Context, l Leftover) (err error) {
	key := strconv.Itoa(l.Ticket)
	if l.Ticket == 0 {
		key = preflightKey(l.Machine)
	}
	release, ok, err := f.Claims.ClaimKeys(key)
	if err != nil {
		return
	}
	handedOver := false
	defer func() {
		if !handedOver {
			release()
		}
	}()
	if !ok {
		f.say("VM %s is in use by another Factory: leaving it", l.Machine)
		return
	}
	if l.Ticket == 0 {
		f.say("VM %s of an earlier preflight: destroying it", l.Machine)
		err = f.Cleanup(ctx, l, true)
		return
	}
	ticket, open, err := f.Forge.Peek(ctx, l.Ticket)
	if err != nil {
		return
	}
	if f.Selector != nil && !slices.Contains(f.Selector, l.Ticket) && !slices.Contains(f.Selector, ticket.Parent) {
		f.say("VM %s of #%d is not selected: leaving it", l.Machine, l.Ticket)
		return
	}
	if !open {
		f.say("#%d is closed: cleaning up VM %s", l.Ticket, l.Machine)
		err = f.Cleanup(ctx, l, ticket.Parent != 0)
		return
	}
	target, ready := ticket, f.ready(ticket)
	// A child continues as part of its Spec.
	if ready && ticket.Parent != 0 {
		var parentOpen bool
		if target, parentOpen, err = f.Forge.Peek(ctx, ticket.Parent); err != nil {
			return
		}
		ready = parentOpen && f.ready(target)
	}
	switch {
	case !ready:
		f.say("#%d (or its Spec) lacks the label %q: VM %s stays (afk vm shell %s, afk vm kill %s)", l.Ticket, f.Labels.ReadyForAgent, l.Machine, l.Machine, l.Machine)
	case f.handled[target.Number]:
		f.say("#%d was resumed already: VM %s stays (afk vm kill %s)", target.Number, l.Machine, l.Machine)
	default:
		f.say("resuming #%d %s in VM %s", target.Number, target.Title, l.Machine)
		handedOver = true
		_, err = f.take(ctx, target, &l, []string{key}, release)
	}
	return
}

// runNext takes the lowest-numbered ticket ready to run, like pick in
// ralph.sh; taken is false when nothing is left.
func (f *Factory) runNext(ctx context.Context) (taken bool, err error) {
	numbers, err := f.Forge.ListLabeled(ctx, f.Labels.ReadyForAgent)
	if err != nil {
		return
	}
	for _, n := range numbers {
		if f.handled[n] {
			continue
		}
		var ticket forge.Ticket
		var ok bool
		if ticket, ok, err = f.pick(ctx, n, false); err != nil {
			return
		}
		if !ok {
			continue
		}
		if taken, err = f.take(ctx, ticket, nil, nil, nil); err != nil || taken {
			return
		}
	}
	return
}

// runListed runs ticket n of an explicit selector under the rules of pick,
// which then says why it skips one.
func (f *Factory) runListed(ctx context.Context, n int) (err error) {
	ticket, ok, err := f.pick(ctx, n, true)
	if err != nil || !ok {
		return
	}
	_, err = f.take(ctx, ticket, nil, nil, nil)
	return
}

// --- pick ---

// pick decides on ticket n from a fresh read: ok when it is ready, open, no
// Spec's child, unblocked and, for a Spec (by label), with children, every
// open one ready. The ones the label list can't have (children, closed) are
// skipped silently, unless the user named the ticket (explicit). A ticket
// with sub-issues but without the Spec label is an error when named, else
// skipped with a warning.
func (f *Factory) pick(ctx context.Context, n int, explicit bool) (ticket forge.Ticket, ok bool, err error) {
	ticket, open, err := f.Forge.Peek(ctx, n)
	if err != nil {
		return
	}
	skip := func(format string, args ...any) {
		if explicit {
			f.warn(n, "skipping "+format, args...)
		}
	}
	switch {
	case !open || !f.ready(ticket):
		skip("#%d: it is closed or lacks the label %q", n, f.Labels.ReadyForAgent)
		return
	case ticket.Parent != 0:
		skip("#%d: it is part of Spec #%d: run #%d", n, ticket.Parent, ticket.Parent)
		return
	}
	isSpec := f.isSpec(ticket)
	switch {
	case !isSpec && ticket.SubIssues > 0 && explicit:
		err = fmt.Errorf("#%d has sub-issues but lacks the label %q: add it to run the ticket as a Spec", n, f.Labels.Spec)
		return
	case !isSpec && ticket.SubIssues > 0:
		f.warn(n, "skipping #%d: it has sub-issues but lacks the label %q", n, f.Labels.Spec)
		return
	case isSpec && ticket.SubIssues == 0:
		f.warn(n, "skipping spec #%d: it has no children (sub-issues, on Gitea a task list): split it first", n)
		return
	}
	blocked, err := f.Forge.Blocked(ctx, ticket)
	if err != nil {
		return
	}
	if blocked {
		skip("#%d: it has an open blocker", n)
		return
	}
	if isSpec {
		var subs []forge.SubIssue
		if subs, err = f.Forge.SubIssues(ctx, n); err != nil {
			return
		}
		for _, sub := range subs {
			if sub.Open && !sub.HasLabel(f.Labels.ReadyForAgent) {
				f.warn(n, "skipping spec #%d: its open child #%d lacks the label %q", n, sub.Number, f.Labels.ReadyForAgent)
				return
			}
		}
	}
	ok = true
	return
}

// isSpec reports whether ticket is a Spec: it carries [labels].spec.
func (f *Factory) isSpec(ticket forge.Ticket) (ok bool) {
	return ticket.HasLabel(f.Labels.Spec)
}

// ready reports whether ticket carries the label.
func (f *Factory) ready(ticket forge.Ticket) (ok bool) {
	return ticket.HasLabel(f.Labels.ReadyForAgent)
}

// --- take ---

// take runs ticket under the claim of it (and of the children of a Spec),
// after the Base image is ensured (whenever a new VM starts from it) and
// the preflight, which runs once. held are claims the caller has
// taken already, release gives them back: take owns them from here. taken
// is false when another Factory holds a claim or finished the ticket meanwhile.
func (f *Factory) take(ctx context.Context, ticket forge.Ticket, resume *Leftover, held []string, release func()) (taken bool, err error) {
	if release != nil {
		defer release()
	}
	keys := []string{strconv.Itoa(ticket.Number)}
	if f.isSpec(ticket) {
		var subs []forge.SubIssue
		if subs, err = f.Forge.SubIssues(ctx, ticket.Number); err != nil {
			return
		}
		for _, sub := range subs {
			if sub.Open {
				keys = append(keys, strconv.Itoa(sub.Number))
			}
		}
	}
	keys = slices.DeleteFunc(slices.Compact(keys), func(key string) bool { return slices.Contains(held, key) })
	releaseMore, ok, err := f.Claims.ClaimKeys(keys...)
	if err != nil {
		return
	}
	defer releaseMore()
	if !ok {
		f.warn(ticket.Number, "#%d (or its children) is claimed by another Factory: skipping", ticket.Number)
		return
	}
	// The claim came after the read: the other Factory may have finished it since.
	fresh, open, err := f.Forge.Peek(ctx, ticket.Number)
	if err != nil {
		return
	}
	if !open || !f.ready(fresh) {
		f.say("#%d is no longer ready: skipping", ticket.Number)
		return
	}
	if ticket, err = f.Forge.ReadyTicket(ctx, ticket.Number, f.Labels.ReadyForAgent); err != nil {
		return
	}
	base, err := f.ReadBase(ctx)
	if err != nil {
		return
	}
	if !f.preflighted {
		if err = f.checkLabels(ctx); err != nil {
			return
		}
	}
	// A resumed Run continues in its VM, which has its own backing file; only
	// a new VM (a fresh Run, or the preflight) needs a current image.
	if resume == nil || !f.preflighted {
		if err = f.EnsureImage(ctx, base); err != nil {
			err = fmt.Errorf("ensuring the Base image: %w", err)
			return
		}
	}
	if !f.preflighted {
		f.say("preflight")
		if err = f.Preflight(ctx, base); err != nil {
			return
		}
		f.preflighted = true
	}
	taken = true
	f.handled[ticket.Number] = true
	f.say("#%d %s", ticket.Number, ticket.Title)
	err = f.Execute(ctx, base, ticket, resume)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		err = fmt.Errorf("interrupted at #%d: a VM stays; `afk factory` continues it: %w", ticket.Number, err)
	case errors.Is(err, ErrNeedsInfo):
		f.say("%v", err)
		err = nil
	case errors.Is(err, machine.ErrNoMemory):
		f.say("#%d: no VM was created, the Factory stops; free host memory and run `afk factory` again.", ticket.Number)
		err = fmt.Errorf("#%d: %w", ticket.Number, err)
	default:
		f.say("#%d failed, the Factory stops. A VM of the Run stays: afk vm list, afk vm shell <vm>, afk vm kill <vm>; `afk factory` continues the Run.", ticket.Number)
		err = fmt.Errorf("#%d: %w", ticket.Number, err)
	}
	return
}

// --- output ---

// say prints a line of afk itself, prefixed "= " like ralph.sh.
func (f *Factory) say(format string, args ...any) {
	_, _ = fmt.Fprintf(f.Out, "= "+format+"\n", args...)
}

// warn is say, once per ticket: the same skip comes up in every round.
func (f *Factory) warn(n int, format string, args ...any) {
	if !f.warned[n] {
		f.warned[n] = true
		f.say(format, args...)
	}
}
