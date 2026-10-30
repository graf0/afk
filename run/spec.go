package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/forge"
)

// sessionFunc runs work in a guest prepared for r: in production the Sandbox
// of a Stack (run.enter), in tests a local clone.
type sessionFunc func(ctx context.Context, r run, work workFunc) (res result, err error)

// ErrNeedsInfo is wrapped by the error of a Spec stopped by needs_info of a
// child: the questions are on the Forge and the Stack is gone, so the Factory goes on.
var ErrNeedsInfo = errors.New("needs info")

// specRun is the state of the Run of a Spec: its children, one Run each, on
// the shared branch ticket-<spec>, then the review of the whole branch.
type specRun struct {
	run  // Ticket is the Spec; tip moves with every child
	open sessionFunc

	// leftover is the state an earlier Factory left for one of the Runs of the
	// Spec; the Run of its ticket continues in it (takeLeftover).
	leftover *Leftover

	pushed   string            // commit of the branch on the Forge: what afk pushed or read last
	pull     forge.PullRequest // zero until it exists
	children []int             // all sub-issues, open or not
}

// --- spec ---

// ExecuteSpec runs the Spec opts.Ticket like docs/reference/ralph.sh, do_spec:
// its open sub-issues in ascending order, the first one without an open
// blocker each time, every one as a Run in the Sandbox of a Stack of its own
// that starts from the end of the branch ticket-<spec>; then a review of the
// whole branch.
// After each child the branch is pushed, the first one opens a draft pull
// request (Closes for the Spec and all children), and the report goes on the
// pull request. The child is closed and loses ready_for_agent. After the
// review the pull request is ready for review. needs_info on a child stops
// the Spec with labels on the child and the Spec; the finished children stay
// on the branch. The branch of a draft pull request is continued where it
// ended; failures leave the state as in Execute.
func ExecuteSpec(ctx context.Context, opts Options) (err error) {
	// Resume is per Run, set by takeLeftover.
	leftover := opts.Resume
	opts.Resume = nil
	r, err := newRun(ctx, opts)
	if err != nil {
		return
	}
	s := specRun{run: r, leftover: leftover, open: func(ctx context.Context, r run, work workFunc) (res result, err error) {
		res, err = r.enter(ctx, work)
		return
	}}
	err = s.execute(ctx)
	return
}

func (s *specRun) execute(ctx context.Context) (err error) {
	if !s.Ticket.HasLabel(s.Base.Config.Labels.Spec) {
		err = fmt.Errorf("ticket #%d lacks the label %q: it is not a Spec", s.Ticket.Number, s.Base.Config.Labels.Spec)
		return
	}
	if s.Ticket.SubIssues == 0 {
		err = fmt.Errorf("spec #%d has no sub-issues", s.Ticket.Number)
		return
	}
	s.say("spec #%d %s", s.Ticket.Number, agent.Sanitize(s.Ticket.Title))
	if err = s.start(ctx); err != nil {
		return
	}
	for {
		var child forge.Ticket
		var ok bool
		if child, ok, err = s.next(ctx); err != nil {
			return
		}
		if !ok {
			break
		}
		if err = s.runChild(ctx, child); err != nil {
			return
		}
	}
	if err = s.finish(ctx); err == nil && s.leftover != nil {
		s.say("state %s of #%d was not used (nothing of the Spec is left for it)", s.leftover.Name, s.leftover.Ticket)
	}
	return
}

// --- start ---

// start lists the children and decides between a new branch and a resumed
// one: a branch on the Forge continues when its pull request is a draft (or
// there is none yet) and the tip is valid; the base then is the merge base
// of the tip, as the base branch may have moved on. A child whose commit is
// on the branch is not run again (finishChild).
func (s *specRun) start(ctx context.Context) (err error) {
	subs, err := s.Forge.SubIssues(ctx, s.Ticket.Number)
	if err != nil {
		return
	}
	for _, sub := range subs {
		s.children = append(s.children, sub.Number)
	}
	slices.Sort(s.children)
	remoteTip, err := s.quarantine.RemoteTip(ctx, s.Forge.Remote(), s.branch)
	if err != nil {
		return
	}
	// A branch on the Forge is resumed, together with the result of the
	// run that pushed it (Resume); a new one must have no result.
	if remoteTip == "" {
		if err = s.quarantine.CheckNew(ctx, s.branch); err != nil {
			return
		}
	}
	if remoteTip == "" && s.leftover != nil {
		// The clone was made from the base of the earlier Run, not today's.
		if s.Base.Commit, err = s.quarantine.Base(ctx, s.branch); err != nil {
			return
		}
	} else if err = s.quarantine.FetchBase(ctx, s.Base.Repo, s.Base.Commit, s.branch); err != nil {
		return
	}
	if remoteTip == "" {
		s.say("base origin/%s %s", s.Base.Branch, s.Base.Commit)
		return
	}
	pull, found, err := s.Forge.FindPull(ctx, s.branch)
	if err != nil {
		return
	}
	if found && (pull.State != "open" || !pull.Draft) {
		err = fmt.Errorf("pull request #%d of %s is not an open draft (%s): afk does not continue it; finish or close it, delete the branch (git push origin --delete %s) and run again, or remove %s from #%d by hand",
			pull.Number, s.branch, pull.URL, s.branch, s.Base.Config.Labels.ReadyForAgent, s.Ticket.Number)
		return
	}
	tip, base, err := s.quarantine.Resume(ctx, s.Forge.Remote(), s.branch, s.Base.Config.Protect.Paths)
	if err != nil {
		return
	}
	s.tip, s.pushed, s.Base.Commit = tip, tip, base
	if found {
		s.pull = pull
	}
	s.say("resuming %s at %s on the base %s", s.branch, tip[:12], base[:12])
	return
}

// --- children ---

// next returns the first open child without an open blocker; ok is false
// when no child is open. Open children all blocked is an error. Every
// open child must carry ready_for_agent: a human decides what runs
// (ADR-0003), and the ones that don't would fail only after the others.
func (s *specRun) next(ctx context.Context) (child forge.Ticket, ok bool, err error) {
	subs, err := s.Forge.SubIssues(ctx, s.Ticket.Number)
	if err != nil {
		return
	}
	subs = slices.DeleteFunc(subs, func(sub forge.SubIssue) bool { return !sub.Open })
	slices.SortFunc(subs, func(a, b forge.SubIssue) int { return cmp.Compare(a.Number, b.Number) })
	label := s.Base.Config.Labels.ReadyForAgent
	for _, sub := range subs {
		if !sub.HasLabel(label) {
			err = fmt.Errorf("spec #%d: open child #%d lacks the label %q: only tickets a human marked ready run in the Factory", s.Ticket.Number, sub.Number, label)
			return
		}
	}
	for _, sub := range subs {
		if child, err = s.Forge.Ticket(ctx, sub.Number); err != nil {
			return
		}
		if child.SubIssues > 0 {
			err = fmt.Errorf("spec #%d: child #%d has sub-issues itself: nested Specs are not supported", s.Ticket.Number, child.Number)
			return
		}
		var blocked bool
		if blocked, err = s.Forge.Blocked(ctx, child); err != nil {
			return
		}
		if !blocked {
			ok = true
			return
		}
		s.say("child #%d has an open blocker, skipping", child.Number)
	}
	if len(subs) > 0 {
		err = fmt.Errorf("spec #%d: open children remain, but all are blocked", s.Ticket.Number)
	}
	return
}

// runChild runs child in a fresh Stack from the tip of the branch, accepts the
// result and publishes it. needs_info stops the Spec.
func (s *specRun) runChild(ctx context.Context, child forge.Ticket) (err error) {
	r := s.run
	r.Ticket, r.spec = child, &s.Ticket
	s.say("child #%d %s", child.Number, agent.Sanitize(child.Title))
	titleOf(s.Out).begin(fmt.Sprintf("#%d %s", child.Number, child.Title))
	done, err := s.onBranch(ctx, child)
	if err != nil || done {
		if done {
			err = s.finishChild(ctx, child)
		}
		return
	}
	logPath, err := r.openLog(fmt.Sprintf("%s-%d", s.branch, child.Number))
	if err != nil {
		return
	}
	defer r.closeLogs()
	s.say("start %s, log %s", r.start()[:12], logPath)
	s.takeLeftover(&r)
	res, err := s.open(ctx, r, r.workChild)
	if err != nil {
		return
	}
	if res.Report.Status == agent.StatusNeedsInfo {
		err = s.needsInfo(ctx, child, res.Report)
		return
	}
	s.tip = res.Commit
	err = s.publishChild(ctx, child, res)
	return
}

// takeLeftover sends the Run r to the state an earlier Factory left for its
// ticket, once: the next ones start fresh.
func (s *specRun) takeLeftover(r *run) {
	if s.leftover == nil || s.leftover.Ticket != r.Ticket.Number {
		return
	}
	r.Resume, r.resumed = s.leftover, true
	s.leftover = nil
}

// onBranch reports whether the commit of child is on the branch already: a
// previous run failed after the push, before the child was closed.
func (s *specRun) onBranch(ctx context.Context, child forge.Ticket) (done bool, err error) {
	if s.tip == "" {
		return
	}
	subjects, err := s.quarantine.Subjects(ctx, s.Base.Commit, s.tip)
	if err != nil {
		return
	}
	suffix := fmt.Sprintf("(#%d)", child.Number)
	done = slices.ContainsFunc(subjects, func(subject string) bool { return strings.HasSuffix(subject, suffix) })
	return
}

// finishChild does what is left of a child that is on the branch: the pull
// request when missing, the comment on the child, closing it, taking the
// label off. Its report is gone with the previous run, so it is not on the pull request.
func (s *specRun) finishChild(ctx context.Context, child forge.Ticket) (err error) {
	s.say("child #%d is on %s already: finishing its Forge steps; its report is not on the pull request", child.Number, s.branch)
	var steps []step
	if s.pull.Number == 0 {
		steps = append(steps, s.pullStep(ctx, &s.pull, s.Ticket.Title, s.closes(), true))
	}
	if err = s.runSteps(slices.Concat(steps, s.closeSteps(ctx, child, s.tip))); err == nil {
		s.say("pull request #%d: %s", s.pull.Number, s.pull.URL)
	}
	return
}

// closeSteps comment on child that it is done in commit, close it and take
// ready_for_agent off.
func (s *specRun) closeSteps(ctx context.Context, child forge.Ticket, commit string) (steps []step) {
	ticket := fmt.Sprintf("#%d", child.Number)
	return []step{{
		name: "comment on " + ticket + " where it is done",
		todo: "comment on " + ticket + " that it is done in " + s.branch + ", with a link to the pull request",
		do: func() (err error) {
			text := fmt.Sprintf("afk: zrobione w %s @ %s, raport w PR #%d (%s)", s.branch, commit[:7], s.pull.Number, s.pull.URL)
			return s.Forge.Comment(ctx, child.Number, text)
		},
	}, {
		name: "close " + ticket,
		todo: "close " + ticket,
		do:   func() (err error) { return s.Forge.CloseIssue(ctx, child.Number) },
	}, s.removeReadyStep(ctx, child.Number)}
}

// publishChild writes a finished child to the Forge: push, the draft pull
// request with the first one, the report, ready_for_human when a person must
// look, and the child closed with a link to the pull request, without
// ready_for_agent.
func (s *specRun) publishChild(ctx context.Context, child forge.Ticket, res result) (err error) {
	ticket := fmt.Sprintf("#%d", child.Number)
	steps := []step{s.pushStep(ctx, &s.pushed, res.Commit)}
	if s.pull.Number == 0 {
		steps = append(steps, s.pullStep(ctx, &s.pull, s.Ticket.Title, s.closes(), true))
	}
	steps = append(steps, s.commentStep(ctx, &s.pull, "report of "+ticket, reportComment(ticket+" "+child.Title, res.Report)))
	if s.needsHuman(res) {
		steps = append(steps, s.humanStep(ctx, &s.pull))
	}
	steps = append(steps, s.closeSteps(ctx, child, res.Commit)...)
	if err = s.runSteps(steps); err == nil {
		s.say("pull request #%d: %s", s.pull.Number, s.pull.URL)
	}
	return
}

// needsInfo tells the author of child and of the Spec that the Agent
// stopped, and fails the Spec; the finished children stay on the branch.
func (s *specRun) needsInfo(ctx context.Context, child forge.Ticket, report agent.Report) (err error) {
	spec := s.Ticket.Number
	text := fmt.Sprintf("afk: stop na #%d (needs-info). Zrobione dzieci są na branchu %s.", child.Number, s.branch)
	steps := slices.Concat(s.needsInfoSteps(ctx, child.Number, needsInfoComment(report)), s.needsInfoSteps(ctx, spec, text))
	if err = s.runSteps(steps); err != nil {
		return
	}
	err = fmt.Errorf("spec #%d: child #%d %w (%s); the finished children stay on %s", spec, child.Number, ErrNeedsInfo, agent.Sanitize(report.Reason), s.branch)
	return
}

// --- review ---

// finish reviews the whole branch in a fresh Stack and writes the outcome to the
// Forge: push of the review's fixes, report, the description of the pull
// request, ready_for_human when a person must look, the pull request ready for
// review and the Spec without ready_for_agent.
func (s *specRun) finish(ctx context.Context) (err error) {
	if s.tip == "" {
		err = fmt.Errorf("spec #%d has no open children and no branch to review", s.Ticket.Number)
		return
	}
	r := s.run
	s.say("spec #%d: code review of %s", s.Ticket.Number, s.branch)
	titleOf(s.Out).begin(fmt.Sprintf("#%d %s (review)", s.Ticket.Number, s.Ticket.Title))
	logPath, err := r.openLog(s.branch + "-review")
	if err != nil {
		return
	}
	defer r.closeLogs()
	s.say("start %s, log %s", r.tip[:12], logPath)
	s.takeLeftover(&r)
	res, err := s.open(ctx, r, r.workReview)
	if err != nil {
		return
	}
	subjects, err := s.quarantine.Subjects(ctx, s.Base.Commit, res.Commit)
	if err != nil {
		return
	}
	body := s.closes() + "\n" + AgentText(commitList(subjects))

	var steps []step
	if res.Commit != s.pushed {
		steps = append(steps, s.pushStep(ctx, &s.pushed, res.Commit))
	}
	if s.pull.Number == 0 {
		steps = append(steps, s.pullStep(ctx, &s.pull, s.Ticket.Title, s.closes(), true))
	}
	steps = append(steps, s.commentStep(ctx, &s.pull, "review report", reviewComment(*res.Review)), step{
		name: "update the pull request description",
		todo: fmt.Sprintf("replace the description of the pull request with %q", body),
		do:   func() (err error) { return s.Forge.UpdatePullBody(ctx, s.pull.Number, body) },
	})
	if s.needsHuman(res) {
		steps = append(steps, s.humanStep(ctx, &s.pull))
	}
	steps = append(steps, step{
		name: "mark the pull request ready for review",
		todo: "mark the pull request ready for review",
		do:   func() (err error) { return s.Forge.ReadyPull(ctx, s.pull) },
	}, s.removeReadyStep(ctx, s.Ticket.Number))
	if err = s.runSteps(steps); err == nil {
		s.say("pull request #%d: %s", s.pull.Number, s.pull.URL)
	}
	return
}

// --- texts ---

// closes is "Closes #N" for the Spec and every child, a line each: merging
// the pull request closes them all. afk's own closing keywords, never from AgentText.
func (s *specRun) closes() (text string) {
	var b strings.Builder
	for _, n := range slices.Concat([]int{s.Ticket.Number}, s.children) {
		fmt.Fprintf(&b, "Closes #%d\n", n)
	}
	return b.String()
}

// commitList is subjects as a Markdown list.
func commitList(subjects []string) (list string) {
	var b strings.Builder
	for _, subject := range subjects {
		b.WriteString("- " + subject + "\n")
	}
	return b.String()
}
