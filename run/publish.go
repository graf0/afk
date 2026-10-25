package run

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/forge"
)

// maxGitHubText bounds text from the Agent on GitHub; a comment takes at
// most 65536 characters (ralph.sh cuts at 60000 too).
const maxGitHubText = 60000

var (
	// mention is the start of an @-mention of a user or team. E-mail
	// addresses match too: a broken address in the Agent's text is cheaper
	// than guessing GitHub's rules for what precedes a mention.
	mention = regexp.MustCompile(`@([A-Za-z0-9])`)

	// closing is a GitHub closing keyword followed by an issue reference
	// (#N, owner/repo#N or a URL): in a PR body or a merged commit message
	// it closes that issue.
	closing = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s*(?:#\d|[\w.-]+/[\w.-]+#\d|https?://)`)
)

// step is one Forge write of publishing; todo says how to do it by hand.
type step struct {
	name string
	todo string
	do   func() (err error)
}

// --- remote ---

// checkRemote fails when ticket-N already exists on the Forge: a Run never
// touches someone's branch, and a second Run of a ticket is a human's call.
func (r run) checkRemote(ctx context.Context) (err error) {
	remote := r.Forge.Remote()
	tip, err := r.quarantine.RemoteTip(ctx, remote, r.branch)
	if err != nil || tip == "" {
		return
	}
	err = fmt.Errorf("branch %s already exists on %s: finish or close its pull request, or delete it (git push origin --delete %s), then run again",
		r.branch, remote.Repo, r.branch)
	return
}

// --- publish ---

// publish writes the outcome of the Run to the Forge. done: push of
// ticket-N, pull request, report comment, review report comment,
// ready_for_human when a person must look (result.human), ready_for_agent
// off the ticket. needs_info: the questions on the ticket, needs_info on and
// ready_for_agent off; no push.
// The steps run in order; a failure lists what is done and, one line per
// step, what is left to do by hand.
func (r run) publish(ctx context.Context, res result) (err error) {
	report := res.Report
	n := r.Ticket.Number
	if report.Status == agent.StatusNeedsInfo {
		err = r.runSteps(r.needsInfoSteps(ctx, n, needsInfoComment(report)))
		return
	}
	var pushed string
	var pull forge.PullRequest
	// afk's own closing keyword: added after agentText, it must work.
	body := fmt.Sprintf("Closes #%d\n", n)
	steps := []step{
		r.pushStep(ctx, &pushed, res.Commit),
		r.pullStep(ctx, &pull, r.Ticket.Title, body, false),
		r.commentStep(ctx, &pull, "agent report", reportComment("Implementacja", report)),
	}
	if res.Review != nil {
		steps = append(steps, r.commentStep(ctx, &pull, "review report", reviewComment(*res.Review)))
	}
	if r.needsHuman(res) {
		steps = append(steps, r.humanStep(ctx, &pull))
	}
	steps = append(steps, r.removeReadyStep(ctx, n))
	defer func() {
		if pull.Number != 0 {
			r.say("pull request #%d: %s", pull.Number, pull.URL)
		}
	}()
	err = r.runSteps(steps)
	return
}

// pushStep pushes the result branch, moving it from *pushed (empty: creating
// it) and remembering the commit.
func (r run) pushStep(ctx context.Context, pushed *string, commit string) (s step) {
	remote := r.Forge.Remote()
	return step{
		name: "push " + r.branch,
		todo: fmt.Sprintf("git --git-dir=%s push %s %s", r.quarantine.Dir, remote.URL, r.branch),
		do: func() (err error) {
			if err = r.quarantine.Push(ctx, remote, r.branch, *pushed); err == nil {
				*pushed = commit
			}
			return
		},
	}
}

// pullStep opens the pull request of the result branch into *pull.
func (r run) pullStep(ctx context.Context, pull *forge.PullRequest, title, body string, draft bool) (s step) {
	kind := "pull request"
	if draft {
		kind = "draft pull request"
	}
	return step{
		name: "open the " + kind,
		todo: fmt.Sprintf("open a %s %s → %s titled %q with the body %q", kind, r.branch, r.Base.Branch, title, body),
		do: func() (err error) {
			*pull, err = r.Forge.CreatePull(ctx, r.branch, r.Base.Branch, title, body, draft)
			return
		},
	}
}

// commentStep comments text, the what of the Run, on *pull.
func (r run) commentStep(ctx context.Context, pull *forge.PullRequest, what, text string) (s step) {
	return step{
		name: "comment the " + what + " on the pull request",
		todo: "comment the " + what + " (printed above) on the pull request",
		do:   func() (err error) { return r.Forge.Comment(ctx, pull.Number, text) },
	}
}

// needsHuman reports whether the pull request of res gets ready_for_human.
func (r run) needsHuman(res result) (ok bool) {
	return res.human()
}

// humanStep marks *pull ready_for_human.
func (r run) humanStep(ctx context.Context, pull *forge.PullRequest) (s step) {
	label := r.Base.Config.Labels.ReadyForHuman
	return step{
		name: "add " + label + " to the pull request",
		todo: "add the label " + label + " to the pull request",
		do:   func() (err error) { return r.Forge.AddLabel(ctx, pull.Number, label) },
	}
}

// needsInfoSteps puts text on issue n, which the Agent could not do, and
// marks it needs_info instead of ready_for_agent.
func (r run) needsInfoSteps(ctx context.Context, n int, text string) (steps []step) {
	labels := r.Base.Config.Labels
	ticket := fmt.Sprintf("#%d", n)
	steps = append(steps, step{
		name: "comment the questions on " + ticket,
		todo: "comment the agent's reason and questions (printed above) on " + ticket,
		do:   func() (err error) { return r.Forge.Comment(ctx, n, text) },
	})
	if labels.NeedsInfo != "" {
		steps = append(steps, step{
			name: "add " + labels.NeedsInfo + " to " + ticket,
			todo: "add the label " + labels.NeedsInfo + " to " + ticket,
			do:   func() (err error) { return r.Forge.AddLabel(ctx, n, labels.NeedsInfo) },
		})
	}
	steps = append(steps, r.removeReadyStep(ctx, n))
	return
}

// removeReadyStep takes ready_for_agent off issue n.
func (r run) removeReadyStep(ctx context.Context, n int) (s step) {
	label := r.Base.Config.Labels.ReadyForAgent
	ticket := fmt.Sprintf("#%d", n)
	return step{
		name: "remove " + label + " from " + ticket,
		todo: "remove the label " + label + " from " + ticket,
		do:   func() (err error) { return r.Forge.RemoveLabel(ctx, n, label) },
	}
}

// runSteps does steps in order. A failure lists what is done and what is
// left to do by hand.
func (r run) runSteps(steps []step) (err error) {
	for i, s := range steps {
		if err = s.do(); err != nil {
			err = fmt.Errorf("%s: %w\ndone:%s\nleft, to do by hand:%s", s.name, err, stepList(steps[:i], false), stepList(steps[i:], true))
			return
		}
		r.say("%s", s.name)
	}
	return
}

// stepList renders steps one per line, by name or, with todo, by how to
// do them by hand.
func stepList(steps []step, todo bool) (list string) {
	if len(steps) == 0 {
		return " nothing"
	}
	for _, s := range steps {
		line := s.name
		if todo {
			line = s.todo
		}
		list += "\n  - " + line
	}
	return
}

// --- texts ---

// commitMessage is the message of the squashed result: the ticket's title
// and number, then the Agent's summary.
func (r run) commitMessage(report agent.Report) (message string) {
	message = fmt.Sprintf("%s (#%d)\n", r.Ticket.Title, r.Ticket.Number)
	if summary := strings.TrimSpace(report.Summary); summary != "" {
		message += "\n" + agentText(summary) + "\n"
	}
	return
}

// reportComment is the Agent's report on the pull request, under heading
// (report() in docs/reference/ralph.sh).
func reportComment(heading string, report agent.Report) (text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n%s\n", heading, report.Summary)
	writeList(&b, "Odstępstwa od ticketu", report.Deviations)
	writeList(&b, "Wątpliwości do sprawdzenia", report.Doubts)
	return agentText(b.String())
}

// reviewMessage is the message of the commit with the review's fixes.
func (r run) reviewMessage(review agent.Report) (message string) {
	message = fmt.Sprintf("code review: %s\n", r.Ticket.Title)
	if summary := strings.TrimSpace(review.Summary); summary != "" {
		message += "\n" + agentText(summary) + "\n"
	}
	return
}

// reviewComment is the review's report on the pull request.
func reviewComment(review agent.Report) (text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "### Code review\n\n%s\n", strings.TrimSpace(review.Summary))
	writeList(&b, "Poprawione w review", review.FindingsFixed)
	writeList(&b, "Pominięte w review", review.FindingsSkipped)
	return agentText(b.String())
}

// needsInfoComment tells the ticket's author why the Agent stopped.
func needsInfoComment(report agent.Report) (text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "afk: Agent nie podjął się ticketu. %s\n", report.Reason)
	writeList(&b, "Pytania", report.Questions)
	return agentText(b.String())
}

// writeList appends a bold title and items as a Markdown list; nothing
// without items.
func writeList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n**%s:**\n", title)
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", item)
	}
}

// agentText prepares text from the Agent for GitHub (comments and commit
// messages): no control or format characters; no @-mentions (the Agent
// must not ping anyone) and no closing keywords (nor close foreign issues),
// both broken by a zero-width space; no HTML comments, which would hide
// text from the reviewer; at most maxGitHubText bytes. Other Markdown
// stays as is: a human reads it before merging.
func agentText(text string) (clean string) {
	clean = agent.Sanitize(text)
	clean = mention.ReplaceAllString(clean, "@\u200b$1")
	clean = closing.ReplaceAllStringFunc(clean, func(match string) (broken string) { return match[:1] + "\u200b" + match[1:] })
	clean = strings.ReplaceAll(clean, "<!--", "&lt;!--")
	if len(clean) <= maxGitHubText {
		return
	}
	cut := maxGitHubText
	for cut > 0 && !utf8.RuneStart(clean[cut]) {
		cut--
	}
	return clean[:cut] + "\n\n… (obcięte)\n"
}
