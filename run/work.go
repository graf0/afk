package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/graf0/afk/agent"
)

const (
	maxGateTail       = 16 << 10 // gate output kept for the fix prompt
	gateTerminalLines = 40       // lines of a failed gate shown on the terminal
)

// guest is the Sandbox as the Run uses it after the clone is prepared.
type guest interface {
	// Exec runs command in the Agent's clone, its stdout going to stdout.
	Exec(ctx context.Context, command string, stdout io.Writer) (err error)
	// Agent runs one Agent session in the clone; empty model is the default.
	Agent(ctx context.Context, prompt, model string) (report agent.Report, err error)
}

// workFunc is what a Run does in a prepared Sandbox.
type workFunc func(ctx context.Context, g guest) (res result, err error)

// result is what a Run brings to publish.
type result struct {
	Report agent.Report  // the implementation, with the reports of fix sessions appended
	Review *agent.Report // nil when there was no review (needs_info)
	Fixes  int           // fix sessions of the gate, over the whole Run
	Commit string        // the Run's end of the branch, accepted in the Quarantine repo

	gateRuns int // numbers the gate's output files
}

// bashCommand runs script in bash, its stderr in its stdout.
func bashCommand(script string) (command string) {
	return "bash -c " + agent.ShellQuote(script) + " 2>&1"
}

// human reports whether a person must look before merging: the Agent
// deviated or doubts, a fix session was needed, or the review skipped findings.
func (res result) human() (ok bool) {
	return len(res.Report.Deviations) > 0 || len(res.Report.Doubts) > 0 || res.Fixes > 0 ||
		(res.Review != nil && len(res.Review.FindingsSkipped) > 0)
}

// addFix appends the report of the fix session to the Run's report
// (docs/reference/ralph.sh, gate).
func (res *result) addFix(fix agent.Report) {
	res.Fixes++
	res.Report.Summary += fmt.Sprintf("\n\nPoprawka po bramce (%d): %s", res.Fixes, fix.Summary)
	res.Report.Deviations = append(res.Report.Deviations, fix.Deviations...)
	res.Report.Doubts = append(res.Report.Doubts, fix.Doubts...)
}

// --- work ---

// work runs the Agent in the prepared Sandbox and brings its result through
// the gate and the review into the Quarantine repo as the result branch: the
// implementation squashed into one commit on the base and, when the review
// committed anything, a "code review" commit on top. Every batch of commits
// from the Sandbox is validated like the first. needs_info returns early.
func (r run) work(ctx context.Context, g guest) (res result, err error) {
	res, head, implemented, err := r.implement(ctx, g)
	if err != nil || res.Report.Status == agent.StatusNeedsInfo {
		return
	}
	r.resumed = false // the note was for the implementation
	if err = r.review(ctx, g, &res, head, implemented); err != nil {
		return
	}
	err = r.accept(ctx, res)
	return
}

// workChild is work of a child of a Spec: the implementation only, squashed
// on the tip of the Spec's branch, which it moves. The review is of the whole
// branch at the end (workReview).
func (r run) workChild(ctx context.Context, g guest) (res result, err error) {
	res, _, _, err = r.implement(ctx, g)
	if err != nil || res.Report.Status == agent.StatusNeedsInfo {
		return
	}
	err = r.accept(ctx, res)
	return
}

// workReview is the review of the whole branch of a Spec, in a Sandbox that
// starts at its tip; the result is the tip, with a "code review" commit on
// top when the review committed anything.
func (r run) workReview(ctx context.Context, g guest) (res result, err error) {
	if err = r.review(ctx, g, &res, r.tip, r.tip); err != nil {
		return
	}
	err = r.accept(ctx, res)
	return
}

// implement runs the Agent's implementation session and the gate, and
// squashes the result on the start of the Run. It returns the validated head
// in the Sandbox and the squashed commit. needs_info returns early.
func (r run) implement(ctx context.Context, g guest) (res result, head, implemented string, err error) {
	cfg := r.Base.Config
	r.say("agent started")
	if res.Report, err = r.agentSession(ctx, g, r.prompt(), cfg.Agent.Model); err != nil {
		return
	}
	if res.Report.Status == agent.StatusNeedsInfo {
		return
	}
	if head, err = r.fetchResult(ctx, g); err != nil {
		return
	}
	if head == r.start() {
		err = errors.New("the agent reported done without commits")
		return
	}
	if head, err = r.gate(ctx, g, head, &res); err != nil {
		return
	}
	implemented, err = r.squash(ctx, r.start(), head, r.commitMessage(res.Report))
	res.Commit = implemented
	return
}

// review runs the review session after head, which implemented is the
// squashed commit of, and sets res.Commit to the end of the branch: with
// the review's fixes, through the gate, squashed in a second commit.
func (r run) review(ctx context.Context, g guest, res *result, head, implemented string) (err error) {
	cfg := r.Base.Config
	r.say("code review (model %s)", cmp.Or(cfg.Agent.ReviewModel, "default"))
	review, err := r.agentSession(ctx, g, agent.ReviewPrompt(r.task()), cfg.Agent.ReviewModel)
	if err != nil {
		return
	}
	if review.Status != agent.StatusDone {
		err = fmt.Errorf("code review returned %s, not done: %s", review.Status, agent.Sanitize(review.Reason))
		return
	}
	res.Review = &review
	res.Commit = implemented
	reviewed, err := r.fetchResult(ctx, g)
	if err != nil {
		return
	}
	if reviewed != head {
		if reviewed, err = r.gate(ctx, g, reviewed, res); err != nil {
			return
		}
		res.Commit, err = r.squash(ctx, implemented, reviewed, r.reviewMessage(review))
	}
	return
}

// accept moves the result branch to the result of the Run.
func (r run) accept(ctx context.Context, res result) (err error) {
	if err = r.quarantine.Accept(ctx, r.branch, res.Commit, r.tip); err != nil {
		return
	}
	r.say("result valid, %s in %s", r.branch, r.quarantine.Dir)
	r.say("  git --git-dir=%s log %s..%s", r.quarantine.Dir, r.Base.Commit[:12], r.branch)
	return
}

// agentSession runs one Agent session and prints its report.
func (r run) agentSession(ctx context.Context, g guest, prompt, model string) (report agent.Report, err error) {
	if report, err = g.Agent(ctx, prompt, model); err != nil {
		return
	}
	r.say("agent report:")
	_, _ = fmt.Fprint(r.Out, report.String())
	return
}

// squash creates one commit with the tree of head on top of parent and
// validates it against the base.
func (r run) squash(ctx context.Context, parent, head, message string) (squashed string, err error) {
	if squashed, err = r.quarantine.Squash(ctx, parent, head, message, r.Base.Ident); err != nil {
		return
	}
	if _, err = r.quarantine.Validate(ctx, r.Base.Commit, squashed, r.protected()); err != nil {
		err = fmt.Errorf("squashed result %s rejected: %w", squashed, err)
	}
	return
}

// --- Sandbox branch ---

// fetchResult brings HEAD of the Sandbox's clone into the Quarantine repo; see
// GuestBranch.Fetch.
func (r run) fetchResult(ctx context.Context, g guest) (head string, err error) {
	dirty, err := Dirty(ctx, g.Exec)
	if err != nil {
		return
	}
	if dirty != "" {
		err = fmt.Errorf("the agent left uncommitted changes:\n%s", agent.Sanitize(dirty))
		return
	}
	return r.guestBranch().Fetch(ctx, g.Exec, r.start(), r.protected(), r.Out)
}

// ExecFunc runs command in the Sandbox's clone, its stdout going to stdout.
type ExecFunc func(ctx context.Context, command string, stdout io.Writer) (err error)

// Dirty returns the uncommitted changes of the Sandbox's clone, `git status
// --porcelain`: empty when it is clean.
func Dirty(ctx context.Context, exec ExecFunc) (status string, err error) {
	var out tailBuffer
	if err = exec(ctx, "git status --porcelain", &out); err != nil {
		err = fmt.Errorf("checking the guest's working tree: %w", err)
		return
	}
	status = strings.TrimSpace(out.String())
	return
}

// GuestBranch is a branch of the Quarantine repo that a Sandbox works on: its
// base goes into the Sandbox, its result comes back.
type GuestBranch struct {
	Quarantine Quarantine
	Name       string // the branch, also in the Sandbox's clone
	TempDir    string // holds bundles on the host while they are sent
}

// CloneTo is the host's half of Clone: the base of the branch from the
// Quarantine repo goes into the new directory dir as git objects and refs, with
// the base as origin/<baseBranch> (and, with fromTip as in Clone, the tip at
// refs/afk/start). It returns start, the ref the branch is to begin at: the
// Sandbox checks it out (CheckoutCommand), never the host. The tree of the tip
// is the Agent's work, and checking it out would run what the host's git
// config says (filters from .gitattributes, hooks). So the host only inits and
// fetches, with no config of the user's (ADR-0002); once the Sandbox starts,
// it never runs git there again.
func (b GuestBranch) CloneTo(ctx context.Context, dir, baseBranch string, fromTip bool) (start string, err error) {
	temp, err := os.CreateTemp(b.TempDir, "base-*.bundle")
	if err != nil {
		err = fmt.Errorf("creating base bundle: %w", err)
		return
	}
	_ = temp.Close()
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = b.Quarantine.Bundle(ctx, b.Name, temp.Name()); err != nil {
		return
	}
	origin := originPrefix + baseBranch
	refspecs, start := []string{"+" + baseRef(b.Name) + ":" + origin}, origin
	if fromTip {
		refspecs, start = append(refspecs, "+"+resultRef(b.Name)+":"+guestStart), guestStart
	}
	// No global or system config, no hooks, no template: nothing to run.
	isolated := []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}
	steps := [][]string{
		{"-c", "core.hooksPath=" + os.DevNull, "init", "-q", "--template=", "-b", b.Name, dir},
		append([]string{"-C", dir, "-c", "core.hooksPath=" + os.DevNull, "fetch", "-q", "--no-tags", temp.Name()}, refspecs...),
	}
	for _, args := range steps {
		if _, err = gitWith(ctx, isolated, "", args...); err != nil {
			err = fmt.Errorf("cloning the base into %s: %w", dir, err)
			return
		}
	}
	return
}

// CheckoutCommand is the command that puts the branch on start in the clone
// CloneTo made, for the Sandbox to run.
func (b GuestBranch) CheckoutCommand(start string) (command string) {
	return "git checkout -q -B " + agent.ShellQuote(b.Name) + " " + agent.ShellQuote(start)
}

// Fetch brings HEAD of the Sandbox's clone into the Quarantine repo as a
// bundle, without running anything of the clone on the host (ADR-0002), and
// validates it against start and protected. It returns the commit; without
// new commits that is start itself, unvalidated. Uncommitted changes in the
// clone are not part of the bundle: see Dirty.
func (b GuestBranch) Fetch(ctx context.Context, exec ExecFunc, start string, protected []string, out io.Writer) (head string, err error) {
	var tip tailBuffer
	if err = exec(ctx, "git rev-parse HEAD", &tip); err != nil {
		err = fmt.Errorf("reading HEAD in the guest: %w", err)
		return
	}
	if strings.TrimSpace(tip.String()) == start {
		head = start
		return
	}
	file, err := os.CreateTemp(b.TempDir, "result-*.bundle")
	if err != nil {
		err = fmt.Errorf("creating result bundle: %w", err)
		return
	}
	defer func() { _ = os.Remove(file.Name()) }()
	err = exec(ctx, "git bundle create -q - "+agent.ShellQuote(start+"..HEAD"), &limitWriter{w: file, left: maxResultSize})
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		err = fmt.Errorf("bundling the result in the guest: %w", err)
		return
	}
	if head, err = b.Quarantine.Import(ctx, file.Name(), b.Name); err != nil {
		return
	}
	commits, err := b.Quarantine.Validate(ctx, start, head, protected)
	if err != nil {
		err = fmt.Errorf("result %s rejected: %w", head, err)
		return
	}
	say(out, "result %s: %d commit(s) on %s", head[:12], commits, start[:12])
	return
}

// --- gate ---

// gate runs [gate].cmd in the Sandbox on head. When it fails, the Agent gets
// a fix session with the end of the output, up to [gate].max_fixes of them;
// each batch of commits is fetched and validated. It returns the head that
// passed. Running out of fixes, or a fix session that does not return done,
// fails the Run.
func (r run) gate(ctx context.Context, g guest, head string, res *result) (passed string, err error) {
	cfg := r.Base.Config
	passed = head
	for fixes := 0; ; fixes++ {
		var tail, path string
		var ok bool
		if tail, path, ok, err = r.gateAttempt(ctx, g, res); err != nil || ok {
			return
		}
		if fixes >= cfg.Gate.MaxFixes {
			err = fmt.Errorf("gate failed after %d fix session(s) (output: %s)", fixes, path)
			return
		}
		r.say("gate fix session %d/%d", fixes+1, cfg.Gate.MaxFixes)
		var fix agent.Report
		if fix, err = r.agentSession(ctx, g, agent.FixPrompt(r.task(), cfg.Gate.Cmd, tail), cfg.Agent.Model); err != nil {
			return
		}
		if fix.Status != agent.StatusDone {
			err = fmt.Errorf("the agent did not fix the gate (status %s): %s (output: %s)", fix.Status, agent.Sanitize(fix.Reason), path)
			return
		}
		res.addFix(fix)
		if passed, err = r.fetchResult(ctx, g); err != nil {
			return
		}
	}
}

// gateAttempt runs the gate once. The whole output goes to a file next to
// the Run's log; tail is the end of it. A failing command is a failed gate,
// not an error; only Ctrl-C and a file that can't be written are errors.
func (r run) gateAttempt(ctx context.Context, g guest, res *result) (tail, path string, ok bool, err error) {
	res.gateRuns++
	path = fmt.Sprintf("%s-gate-%d.txt", strings.TrimSuffix(r.log.path, ".jsonl"), res.gateRuns)
	// Bounded like the log: the Sandbox decides how much it prints.
	out, err := newRotatingLog(path)
	if err != nil {
		err = fmt.Errorf("gate output file: %w", err)
		return
	}
	defer func() { _ = out.Close() }()
	buffer := &tailBuffer{max: maxGateTail}
	execErr := g.Exec(ctx, bashCommand(r.Base.Config.Gate.Cmd), io.MultiWriter(out, buffer))
	if err = ctx.Err(); err != nil {
		return
	}
	if execErr == nil {
		r.say("gate passed (output: %s)", path)
		ok = true
		return
	}
	// The error is the exit status (or a broken connection); the reason is in the output.
	_, _ = fmt.Fprintf(out, "\n%s\n", agent.Sanitize(execErr.Error()))
	tail = buffer.String()
	r.say("gate failed (attempt %d, output: %s); end of the output:", res.gateRuns, path)
	for line := range strings.Lines(lastLines(agent.Sanitize(tail), gateTerminalLines)) {
		r.say("%s", strings.TrimRight(line, "\n"))
	}
	return
}

// lastLines is the last n lines of text, without the final newline.
func lastLines(text string, n int) (tail string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// --- helpers ---

// start is the commit the Agent's clone starts from: the tip of the Spec's
// branch, else the base.
func (r run) start() (commit string) { return cmp.Or(r.tip, r.Base.Commit) }

func (r run) task() (task agent.Task) {
	var names []string
	for _, id := range r.Base.Plugins {
		name, _, _ := strings.Cut(id, "@")
		names = append(names, name)
	}
	task = agent.Task{
		RepoDir:    r.treePath(),
		Branch:     r.branch,
		BaseRef:    "origin/" + r.Base.Branch,
		TicketPath: r.contextFile(ticketFileName),
		Protected:  r.protected(),
		Plugins:    names,
		Resumed:    r.resumed,
	}
	if r.spec != nil {
		task.SpecPath = r.contextFile(specFileName)
	}
	return
}
