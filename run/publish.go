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
	exists, err := r.quarantine.RemoteHasBranch(ctx, r.Forge.Remote(), r.branch)
	if err != nil || !exists {
		return
	}
	err = fmt.Errorf("branch %s already exists on %s: finish or close its pull request, or delete it (git push origin --delete %s), then run again",
		r.branch, r.Forge.Repo, r.branch)
	return
}

// --- publish ---

// publish writes the outcome of the Run to the Forge. done: push of
// ticket-N, pull request, report comment, ready_for_human when the Agent
// deviated or doubts, ready_for_agent off the ticket. needs_info: the
// questions on the ticket, needs_info on and ready_for_agent off; no push.
// The steps run in order; a failure lists what is done and, one line per
// step, what is left to do by hand.
func (r run) publish(ctx context.Context, report agent.Report) (err error) {
	labels := r.Base.Config.Labels
	n := r.Ticket.Number
	ticket := fmt.Sprintf("#%d", n)
	var steps []step
	if report.Status == agent.StatusNeedsInfo {
		steps = append(steps, step{
			name: "comment the questions on " + ticket,
			todo: "comment the agent's reason and questions (printed above) on " + ticket,
			do:   func() (err error) { return r.Forge.Comment(ctx, n, needsInfoComment(report)) },
		})
		if labels.NeedsInfo != "" {
			steps = append(steps, step{
				name: "add " + labels.NeedsInfo + " to " + ticket,
				todo: "add the label " + labels.NeedsInfo + " to " + ticket,
				do:   func() (err error) { return r.Forge.AddLabel(ctx, n, labels.NeedsInfo) },
			})
		}
	} else {
		var pull forge.PullRequest
		// afk's own closing keyword: added after agentText, it must work.
		body := "Closes " + ticket + "\n"
		steps = append(steps, step{
			name: "push " + r.branch,
			todo: fmt.Sprintf("git --git-dir=%s push %s %s", r.quarantine.Dir, r.Forge.GitURL, r.branch),
			do:   func() (err error) { return r.quarantine.Push(ctx, r.Forge.Remote(), r.branch) },
		}, step{
			name: "open the pull request",
			todo: fmt.Sprintf("open a pull request %s → %s titled %q with the body %q", r.branch, r.Base.Branch, r.Ticket.Title, body),
			do: func() (err error) {
				pull, err = r.Forge.CreatePull(ctx, r.branch, r.Base.Branch, r.Ticket.Title, body)
				return
			},
		}, step{
			name: "comment the report on the pull request",
			todo: "comment the agent report (printed above) on the pull request",
			do:   func() (err error) { return r.Forge.Comment(ctx, pull.Number, reportComment(report)) },
		})
		if labels.ReadyForHuman != "" && (len(report.Deviations) > 0 || len(report.Doubts) > 0) {
			steps = append(steps, step{
				name: "add " + labels.ReadyForHuman + " to the pull request",
				todo: "add the label " + labels.ReadyForHuman + " to the pull request",
				do:   func() (err error) { return r.Forge.AddLabel(ctx, pull.Number, labels.ReadyForHuman) },
			})
		}
		defer func() {
			if pull.Number != 0 {
				r.say("pull request #%d: %s", pull.Number, pull.URL)
			}
		}()
	}
	steps = append(steps, step{
		name: "remove " + labels.ReadyForAgent + " from " + ticket,
		todo: "remove the label " + labels.ReadyForAgent + " from " + ticket,
		do:   func() (err error) { return r.Forge.RemoveLabel(ctx, n, labels.ReadyForAgent) },
	})

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

// reportComment is the Agent's report on the pull request (report() in
// docs/reference/ralph.sh).
func reportComment(report agent.Report) (text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "### Implementacja\n\n%s\n", report.Summary)
	writeList(&b, "Odstępstwa od ticketu", report.Deviations)
	writeList(&b, "Wątpliwości do sprawdzenia", report.Doubts)
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
