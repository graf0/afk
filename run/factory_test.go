package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
)

// factoryEnv is a Factory on a fake GitHub with injected VMs: Execute
// records the tickets and does what a Run does to the Forge (the Spec and its
// children are closed, the label goes), unless fail has an error for it.
type factoryEnv struct {
	fake      *fakeGitHub
	factory   *Factory
	out       *bytes.Buffer
	executed  []int
	resumes   map[int]Leftover // by ticket, for the Runs that continued in a VM
	cleaned   []string
	events    []string
	leftovers []Leftover
	fail      map[int]error
	reads     int   // ReadBase calls
	imageErr  error // what EnsureImage fails with
	unclaimed []int // claims a resumed Run did not hold
}

// testLabels are the default [labels].
var testLabels = config.Labels{NeedsTriage: "needs-triage", NeedsInfo: "needs-info", ReadyForAgent: "ready-for-agent", ReadyForHuman: "ready-for-human", Wontfix: "wontfix", Spec: "spec"}

func newFactoryEnv(t *testing.T, issues map[int]*fakeIssue, selector ...int) (env *factoryEnv) {
	t.Helper()
	env = &factoryEnv{fake: &fakeGitHub{issues: issues, labels: testLabels.Names()}, out: &bytes.Buffer{}, resumes: map[int]Leftover{}, fail: map[int]error{}}
	env.factory = &Factory{
		Forge:    env.fake.serve(t, t.TempDir()),
		Labels:   testLabels,
		Selector: selector,
		Claims:   Claims{Dir: t.TempDir()},
		Out:      env.out,
		EnsureImage: func(ctx context.Context, base Base) (err error) {
			env.events = append(env.events, "image")
			return env.imageErr
		},
		ReadBase: func(ctx context.Context) (base Base, err error) {
			env.reads++
			return
		},
		Preflight: func(ctx context.Context, base Base) (err error) {
			env.events = append(env.events, "preflight")
			return
		},
		Leftovers: func() (leftovers []Leftover, err error) { return env.leftovers, nil },
		Cleanup: func(ctx context.Context, l Leftover, vmOnly bool) (err error) {
			if vmOnly {
				env.cleaned = append(env.cleaned, l.Machine+" (VM only)")
			} else {
				env.cleaned = append(env.cleaned, l.Machine)
			}
			return
		},
		Execute: func(ctx context.Context, base Base, ticket forge.Ticket, resume *Leftover) (err error) {
			env.events = append(env.events, "execute")
			env.executed = append(env.executed, ticket.Number)
			if resume != nil {
				env.resumes[ticket.Number] = *resume
				// The resumed Run holds its claims: another Factory can't take them.
				for _, n := range []int{ticket.Number, resume.Ticket} {
					if release, ok, _ := env.factory.Claims.Claim(n); ok {
						release()
						env.unclaimed = append(env.unclaimed, n)
					}
				}
			}
			if err = env.fail[ticket.Number]; err != nil {
				return
			}
			for _, issue := range issues {
				if issue.parent == ticket.Number {
					issue.open = false
				}
			}
			issues[ticket.Number].labels = nil
			return
		},
	}
	return
}

func ready() []string { return []string{"ready-for-agent"} }

func readySpec() []string { return []string{"ready-for-agent", "spec"} }

func TestFactoryPick(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		// A Spec of two ready children: taken as a whole, the children are not picked.
		10: {title: "Spec", open: true, labels: readySpec()},
		11: {title: "Child", open: true, labels: ready(), parent: 10},
		12: {title: "Child", open: true, labels: ready(), parent: 10},
		// A Spec with an open child that is not ready.
		20: {title: "Half-ready spec", open: true, labels: readySpec()},
		21: {title: "Child", open: true, labels: ready(), parent: 20},
		22: {title: "Child", open: true, labels: []string{"needs-triage"}, parent: 20},
		// A Spec without children, and a ticket with sub-issues but no Spec label.
		30: {title: "Childless", open: true, labels: readySpec(), body: "Intro\n## User Stories\n1. As a user"},
		31: {title: "Unlabeled", open: true, labels: ready()},
		32: {title: "Child", open: true, labels: ready(), parent: 31},
		// Native blocker, open.
		40: {title: "Native", open: true, labels: ready(), blockers: []int{41}},
		41: {title: "Blocker", open: true},
		// Blocker named in the body: open, then closed.
		50: {title: "Body", open: true, labels: ready(), body: "Blocked by: #41"},
		60: {title: "Unblocked", open: true, labels: ready(), body: "Blocked by: #61", blockers: []int{61}},
		61: {title: "Done", open: false},
		// The list by label is stale: #70 lost the label meanwhile.
		70: {title: "Stale", open: true},
		80: {title: "Plain", open: true, labels: ready()},
	})
	env.fake.listed = []int{80, 70, 60, 50, 40, 32, 31, 30, 22, 21, 20, 12, 11, 10}
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	if want := []int{10, 60, 80}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v\n%s", env.executed, want, env.out)
	}
	for _, want := range []string{"skipping spec #20: its open child #22 lacks", "skipping spec #30: it has no sub-issues", "skipping #31: it has sub-issues but lacks the label \"spec\""} {
		if got := env.out.String(); strings.Count(got, want) != 1 {
			t.Errorf("want one %q in:\n%s", want, got)
		}
	}
	if got := env.events; !slices.Equal(got, []string{"image", "preflight", "execute", "image", "execute", "image", "execute"}) {
		t.Errorf("events %v, want an image check before every Run and one preflight before the first", got)
	}
}

func TestFactoryUserStoriesIsNoSpec(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		1: {title: "PRD-like", open: true, labels: ready(), body: "Intro\n## User Stories\n1. As a user"},
	})
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	if want := []int{1}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v: no label, no Spec, no heuristic\n%s", env.executed, want, env.out)
	}
}

func TestFactoryNothingToDo(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{1: {title: "Not ready", open: true}})
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(env.events) != 0 {
		t.Errorf("events %v: no Run, no preflight", env.events)
	}
}

func TestFactoryList(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		1:  {title: "One", open: true, labels: ready()},
		2:  {title: "Two", open: true, labels: ready()},
		3:  {title: "Blocked", open: true, labels: ready(), blockers: []int{4}},
		4:  {title: "Blocker", open: true},
		5:  {title: "Closed", open: false, labels: ready()},
		6:  {title: "Not ready", open: true},
		7:  {title: "Childless", open: true, labels: readySpec()},
		10: {title: "Spec", open: true, labels: readySpec()},
		11: {title: "Child", open: true, labels: ready(), parent: 10},
	}, 2, 3, 5, 6, 7, 11, 1)
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	if want := []int{2, 1}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v in the order given", env.executed, want)
	}
	for _, want := range []string{"skipping #3: it has an open blocker", "skipping #5: it is closed", "skipping #6: it is closed or lacks", "skipping spec #7: it has no sub-issues", "skipping #11: it is part of Spec #10: run #10"} {
		if !strings.Contains(env.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, env.out)
		}
	}
}

func TestFactorySubIssuesWithoutLabel(t *testing.T) {
	issues := func() map[int]*fakeIssue {
		return map[int]*fakeIssue{
			1: {title: "Unlabeled", open: true, labels: ready()},
			2: {title: "Child", open: true, labels: ready(), parent: 1},
			3: {title: "Plain", open: true, labels: ready()},
		}
	}
	// Every round of the loop reads #1 again, and warns once.
	env := newFactoryEnv(t, issues())
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	if want := []int{3}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v", env.executed, want)
	}
	if got := strings.Count(env.out.String(), "skipping #1: it has sub-issues but lacks the label \"spec\""); got != 1 {
		t.Errorf("want one warning in:\n%s", env.out)
	}

	env = newFactoryEnv(t, issues(), 1, 3)
	err := env.factory.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), `#1 has sub-issues but lacks the label "spec"`) {
		t.Errorf("list: err = %v, want a refusal naming the label", err)
	}
	if len(env.executed) != 0 {
		t.Errorf("executed %v, want none: the error stops the Factory", env.executed)
	}
}

func TestFactoryLabelsPreflight(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{1: {title: "One", open: true, labels: ready()}})
	// Names compare ignoring case, like on the Forge.
	env.fake.labels = []string{"Ready-For-Agent", "needs-info", "needs-triage", "ready-for-human"}
	err := env.factory.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "wontfix, spec") || strings.Contains(err.Error(), "needs-info") || strings.Contains(err.Error(), "ready-for") {
		t.Errorf("err = %v, want the missing labels, only", err)
	}
	if len(env.events) != 0 {
		t.Errorf("events %v: nothing may start", env.events)
	}

	env.fake.labels = append(env.fake.labels, "spec", "wontfix")
	if err = env.factory.Run(t.Context()); err != nil {
		t.Errorf("all labels present: %v", err)
	}
	if want := []int{1}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v", env.executed, want)
	}
}

func TestFactoryClaimed(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		1:  {title: "Claimed", open: true, labels: ready()},
		2:  {title: "Free", open: true, labels: ready()},
		10: {title: "Spec", open: true, labels: readySpec()},
		11: {title: "Child", open: true, labels: ready(), parent: 10},
	})
	other := env.factory.Claims // another Factory of the Project
	for _, n := range []int{1, 11} {
		release, ok, err := other.Claim(n)
		if err != nil || !ok {
			t.Fatalf("claim #%d: %v, %v", n, ok, err)
		}
		defer release()
	}
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []int{2}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v: #1 and the Spec #10 (child #11) are claimed\n%s", env.executed, want, env.out)
	}
	// The claims of a finished Run are given back.
	for _, n := range []int{2} {
		release, ok, err := other.Claim(n)
		if err != nil || !ok {
			t.Errorf("claim #%d after its Run: %v, %v", n, ok, err)
		}
		release()
	}
}

func TestFactoryFailureStops(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		1: {title: "Fine", open: true, labels: ready()},
		2: {title: "Fails", open: true, labels: ready()},
		3: {title: "Never", open: true, labels: ready()},
	})
	boom := errors.New("gate failed")
	env.fail[2] = boom
	err := env.factory.Run(t.Context())
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "#2") {
		t.Errorf("err = %v, want the failure of #2", err)
	}
	if want := []int{1, 2}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v: the failure stops the Factory", env.executed, want)
	}
	for _, want := range []string{"afk vm shell", "afk vm kill"} {
		if !strings.Contains(env.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, env.out)
		}
	}
}

func TestFactoryEnsuresImageBeforeEveryRun(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		1: {title: "One", open: true, labels: ready()},
		2: {title: "Two", open: true, labels: ready()},
	})
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"image", "preflight", "execute", "image", "execute"}
	if !slices.Equal(env.events, want) {
		t.Errorf("events = %v, want %v", env.events, want)
	}
}

func TestFactoryImageFailureStops(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		1: {title: "One", open: true, labels: ready()},
		2: {title: "Two", open: true, labels: ready()},
	})
	boom := errors.New("packer failed")
	env.imageErr = boom

	err := env.factory.Run(t.Context())
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the build error", err)
	}
	if len(env.executed) != 0 || slices.Contains(env.events, "preflight") {
		t.Errorf("events = %v, executed = %v; want nothing run on a failed build", env.events, env.executed)
	}
}

func TestFactoryNeedsInfoGoesOn(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		1: {title: "Spec", open: true, labels: readySpec()},
		2: {title: "Child", open: true, labels: ready(), parent: 1},
		3: {title: "Next", open: true, labels: ready()},
	})
	env.fail[1] = fmt.Errorf("spec #1: child #2: %w", ErrNeedsInfo)
	// A Spec stopped by needs_info loses its label on the Forge.
	execute := env.factory.Execute
	env.factory.Execute = func(ctx context.Context, base Base, ticket forge.Ticket, resume *Leftover) (err error) {
		err = execute(ctx, base, ticket, resume)
		env.fake.issues[ticket.Number].labels = nil
		return
	}
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 3}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v: needs_info does not stop the Factory", env.executed, want)
	}
}

func TestFactoryInterrupted(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{1: {title: "One", open: true, labels: ready()}, 2: {title: "Two", open: true, labels: ready()}})
	ctx, cancel := context.WithCancel(t.Context())
	env.factory.Execute = func(ctx context.Context, base Base, ticket forge.Ticket, resume *Leftover) (err error) {
		cancel()
		return ctx.Err()
	}
	err := env.factory.Run(ctx)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("err = %v, want interrupted", err)
	}
}

func TestFactoryResume(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		5: {title: "Failed single", open: true, labels: ready()},
		6: {title: "Closed since", open: false},
		7: {title: "Label gone", open: true},
		// The VM of a child: the Spec continues.
		10: {title: "Spec", open: true, labels: readySpec()},
		11: {title: "Child", open: true, labels: ready(), parent: 10},
		13: {title: "Finished child", open: false, parent: 10},
		80: {title: "Queue", open: true, labels: ready()},
	})
	env.leftovers = []Leftover{{"vm-pre", 0}, {"vm-5", 5}, {"vm-6", 6}, {"vm-7", 7}, {"vm-11", 11}, {"vm-13", 13}}
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	if want := []int{5, 10, 80}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v: leftovers before the queue\n%s", env.executed, want, env.out)
	}
	if want := (map[int]Leftover{5: {"vm-5", 5}, 10: {"vm-11", 11}}); len(env.resumes) != len(want) || env.resumes[5] != want[5] || env.resumes[10] != want[10] {
		t.Errorf("resumes %v, want %v", env.resumes, want)
	}
	if len(env.unclaimed) != 0 {
		t.Errorf("a resumed Run did not hold the claims of %v", env.unclaimed)
	}
	// A finished child has no branch of its own: only its VM goes.
	if want := []string{"vm-pre (VM only)", "vm-6", "vm-13 (VM only)"}; !slices.Equal(env.cleaned, want) {
		t.Errorf("cleaned %v, want %v", env.cleaned, want)
	}
	if !strings.Contains(env.out.String(), "VM vm-7 stays") {
		t.Errorf("output does not say that vm-7 stays:\n%s", env.out)
	}
}

// A resumed Run continues in its VM: the image is needed only for a new VM,
// here the preflight (the first Run of the Factory), and the fresh Run.
func TestFactoryResumeEnsuresImageOnlyForNewVMs(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		5: {title: "Resumed one", open: true, labels: ready()},
		6: {title: "Resumed two", open: true, labels: ready()},
		7: {title: "Fresh", open: true, labels: ready()},
	})
	env.leftovers = []Leftover{{"vm-5", 5}, {"vm-6", 6}}
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	want := []string{"image", "preflight", "execute", "execute", "image", "execute"}
	if !slices.Equal(env.events, want) {
		t.Errorf("events = %v, want %v", env.events, want)
	}
	if env.reads != 3 {
		t.Errorf("ReadBase calls = %d, want one per Run (3)", env.reads)
	}
}

// A Factory stops at a failed Run; the next one continues it in its VM.
func TestFactoryRerunContinuesFailedRun(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		1: {title: "Fine", open: true, labels: ready()},
		2: {title: "Fails once", open: true, labels: ready()},
		3: {title: "After", open: true, labels: ready()},
	})
	env.fail[2] = errors.New("agent crashed")
	if err := env.factory.Run(t.Context()); err == nil {
		t.Fatal("want the failure of #2")
	}
	if len(env.resumes) != 0 {
		t.Errorf("resumes %v in the first Factory", env.resumes)
	}
	// What the failed Run left in libvirt.
	env.leftovers = []Leftover{{"afk-run-2-ab12", 2}}
	delete(env.fail, 2)
	env.executed = nil
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	if want := []int{2, 3}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v", env.executed, want)
	}
	if got, ok := env.resumes[2]; !ok || got.Machine != "afk-run-2-ab12" {
		t.Errorf("#2 did not continue in the VM it left: resumes %v", env.resumes)
	}
	if _, ok := env.resumes[3]; ok {
		t.Error("#3 continued in a VM: it was never started")
	}
}

// The VM of a live Run of another Factory looks like a leftover: its claim
// tells them apart.
func TestFactoryLeftoverInUse(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		5: {title: "Running elsewhere", open: true, labels: ready()},
		6: {title: "Closed, running elsewhere", open: false},
		7: {title: "Failed here", open: true, labels: ready()},
	})
	env.leftovers = []Leftover{{"vm-pre", 0}, {"vm-5", 5}, {"vm-6", 6}, {"vm-7", 7}}
	other := env.factory.Claims
	for _, key := range []string{preflightKey("vm-pre"), "5", "6"} {
		release, ok, err := other.ClaimKeys(key)
		if err != nil || !ok {
			t.Fatalf("claim %s: %v, %v", key, ok, err)
		}
		defer release()
	}
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	if want := []int{7}; !slices.Equal(env.executed, want) || len(env.cleaned) != 0 {
		t.Errorf("executed %v, cleaned %v; want only #7 resumed, nothing of the others touched\n%s", env.executed, env.cleaned, env.out)
	}
	if got := strings.Count(env.out.String(), "in use by another Factory"); got != 3 {
		t.Errorf("%d VMs reported in use, want 3:\n%s", got, env.out)
	}
}

// With a list only the Runs of the listed tickets, or of their Specs, are touched.
func TestFactoryListLeftovers(t *testing.T) {
	env := newFactoryEnv(t, map[int]*fakeIssue{
		5:  {title: "Listed", open: true, labels: ready()},
		6:  {title: "Not listed", open: true, labels: ready()},
		7:  {title: "Closed, not listed", open: false},
		10: {title: "Listed Spec", open: true, labels: readySpec()},
		11: {title: "Child", open: true, labels: ready(), parent: 10},
		20: {title: "Other Spec", open: true, labels: readySpec()},
		21: {title: "Child", open: true, labels: ready(), parent: 20},
	}, 5, 10)
	env.leftovers = []Leftover{{"vm-5", 5}, {"vm-6", 6}, {"vm-7", 7}, {"vm-11", 11}, {"vm-21", 21}}
	if err := env.factory.Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, env.out)
	}
	if want := []int{5, 10}; !slices.Equal(env.executed, want) {
		t.Errorf("executed %v, want %v", env.executed, want)
	}
	if want := (map[int]Leftover{5: {"vm-5", 5}, 10: {"vm-11", 11}}); len(env.resumes) != 2 || env.resumes[5] != want[5] || env.resumes[10] != want[10] {
		t.Errorf("resumes %v, want %v", env.resumes, want)
	}
	if len(env.cleaned) != 0 {
		t.Errorf("cleaned %v: unlisted tickets' VMs must stay", env.cleaned)
	}
}
