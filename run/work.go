package run

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/machine"
)

const (
	maxGateTail       = 16 << 10 // gate output kept for the fix prompt
	gateTerminalLines = 40       // lines of a failed gate shown on the terminal
)

// guest is the Sandbox VM as the Run uses it after the clone is prepared.
type guest interface {
	// Exec runs command in the Agent's clone, its stdout going to stdout.
	Exec(ctx context.Context, command string, stdout io.Writer) (err error)
	// Agent runs one Agent session in the clone; empty model is the default.
	Agent(ctx context.Context, prompt, model string) (report agent.Report, err error)
}

// sshGuest is the guest over ssh.
type sshGuest struct {
	run    run
	client *ssh.Client
}

// result is what a Run brings to publish.
type result struct {
	Report agent.Report  // the implementation, with the reports of fix sessions appended
	Review *agent.Report // nil when there was no review (needs_info)
	Fixes  int           // fix sessions of the gate, over the whole Run

	gateRuns int // numbers the gate's output files
}

func (g sshGuest) Exec(ctx context.Context, command string, stdout io.Writer) (err error) {
	err = remote(ctx, g.client, "cd "+guestRepo+" && "+command, nil, stdout)
	return
}

func (g sshGuest) Agent(ctx context.Context, prompt, model string) (report agent.Report, err error) {
	r := g.run
	report, err = agent.Session{
		Token:      r.ClaudeToken,
		Prompt:     prompt,
		Dir:        guestRepo,
		Model:      model,
		Proxy:      "http://" + machine.ProxyAddr,
		Binary:     r.AgentBinary,
		PluginDirs: r.guestPluginDirs(),
	}.Run(ctx, g.client, r.Out, r.log)
	if err != nil {
		err = fmt.Errorf("%w (log: %s)", err, r.log.path)
	}
	return
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

// work runs the Agent in the prepared guest and brings its result through
// the gate and the review into the Quarantine repo as the result branch: the
// implementation squashed into one commit on the base and, when the review
// committed anything, a "code review" commit on top. Every batch of commits
// from the guest is validated like the first. needs_info returns early.
func (r run) work(ctx context.Context, g guest) (res result, err error) {
	cfg := r.Base.Config
	r.say("agent started")
	if res.Report, err = r.agentSession(ctx, g, r.prompt(), cfg.Agent.Model); err != nil {
		return
	}
	if res.Report.Status == agent.StatusNeedsInfo {
		return
	}
	head, err := r.fetchResult(ctx, g)
	if err != nil {
		return
	}
	if head, err = r.gate(ctx, g, head, &res); err != nil {
		return
	}
	implemented, err := r.squash(ctx, r.Base.Commit, head, r.commitMessage(res.Report))
	if err != nil {
		return
	}
	final := implemented

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
	reviewed, err := r.fetchResult(ctx, g)
	if err != nil {
		return
	}
	if reviewed != head {
		if reviewed, err = r.gate(ctx, g, reviewed, &res); err != nil {
			return
		}
		if final, err = r.squash(ctx, implemented, reviewed, r.reviewMessage(review)); err != nil {
			return
		}
	}
	if err = r.quarantine.Accept(ctx, r.branch, final); err != nil {
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
	if _, err = r.quarantine.Validate(ctx, r.Base.Commit, squashed, r.Base.Config.Protect.Paths); err != nil {
		err = fmt.Errorf("squashed result %s rejected: %w", squashed, err)
	}
	return
}

// --- fetch ---

// fetchResult brings HEAD of the guest's clone into the Quarantine repo as a
// bundle, without running anything of the clone on the host (ADR-0002), and
// validates it against the base. It returns the commit.
func (r run) fetchResult(ctx context.Context, g guest) (head string, err error) {
	var status tailBuffer
	if err = g.Exec(ctx, "git status --porcelain", &status); err != nil {
		err = fmt.Errorf("checking the guest's working tree: %w", err)
		return
	}
	if dirty := strings.TrimSpace(status.String()); dirty != "" {
		err = fmt.Errorf("the agent left uncommitted changes:\n%s", agent.Sanitize(dirty))
		return
	}
	file, err := os.CreateTemp(r.stateDir, "result-*.bundle")
	if err != nil {
		err = fmt.Errorf("creating result bundle: %w", err)
		return
	}
	defer func() { _ = os.Remove(file.Name()) }()
	err = g.Exec(ctx, "git bundle create -q - "+agent.ShellQuote(r.Base.Commit+"..HEAD"), &limitWriter{w: file, left: maxResultSize})
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		err = fmt.Errorf("bundling the result in the guest: %w", err)
		return
	}
	if head, err = r.quarantine.Import(ctx, file.Name(), r.branch); err != nil {
		return
	}
	commits, err := r.quarantine.Validate(ctx, r.Base.Commit, head, r.Base.Config.Protect.Paths)
	if err != nil {
		err = fmt.Errorf("result %s rejected: %w", head, err)
		return
	}
	r.say("result %s: %d commit(s) on the base", head[:12], commits)
	return
}

// --- gate ---

// gate runs [gate].cmd in the guest on head. When it fails, the Agent gets
// a fix session with the end of the output, up to [gate].max_fixes of them;
// each batch of commits is fetched and validated. It returns the head that
// passed. Running out of fixes, or a fix session that does not return done,
// fails the Run. An empty [gate].cmd is no gate.
func (r run) gate(ctx context.Context, g guest, head string, res *result) (passed string, err error) {
	cfg := r.Base.Config
	passed = head
	if cfg.Gate.Cmd == "" {
		return
	}
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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		err = fmt.Errorf("creating gate output file: %w", err)
		return
	}
	// Bounded like the log: the guest decides how much it prints.
	out := &rotatingLog{path: path, max: logRotateSize, file: file}
	defer func() { _ = out.Close() }()
	buffer := &tailBuffer{max: maxGateTail}
	execErr := g.Exec(ctx, "bash -c "+agent.ShellQuote(r.Base.Config.Gate.Cmd)+" 2>&1", io.MultiWriter(out, buffer))
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

func (r run) task() (task agent.Task) {
	var names []string
	for _, p := range r.plugins {
		names = append(names, p.Name())
	}
	return agent.Task{
		RepoDir:    guestRepo,
		Branch:     r.branch,
		BaseRef:    "origin/" + r.Base.Branch,
		TicketPath: guestTicket,
		Protected:  r.Base.Config.Protect.Paths,
		Plugins:    names,
	}
}

// guestPluginDirs are the directories of the copied plugins in the guest.
func (r run) guestPluginDirs() (dirs []string) {
	for _, p := range r.plugins {
		dirs = append(dirs, guestPlugins+"/"+p.ID)
	}
	return
}
