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

// mention is the start of an @-mention of a user or team. E-mail addresses
// match too: a broken address in the Agent's text is cheaper than guessing
// GitHub's rules for what precedes a mention.
var mention = regexp.MustCompile(`@([A-Za-z0-9])`)

// step is one Forge write of publishing.
type step struct {
	name string
	do   func() error
}

// --- remote ---

// checkRemote fails when ticket-N already exists on the Forge: a Run never
// touches someone's branch, and a second Run of a ticket is a human's call.
func (r run) checkRemote(ctx context.Context) (err error) {
	exists, err := r.quarantine.RemoteHasBranch(ctx, r.Forge.GitURL, r.Forge.GitEnv(), r.branch)
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
// The steps run in order; a failure names what is done and what is left.
func (r run) publish(ctx context.Context, report agent.Report) (err error) {
	labels := r.Base.Config.Labels
	n := r.Ticket.Number
	ticket := fmt.Sprintf("#%d", n)
	var steps []step
	if report.Status == agent.StatusNeedsInfo {
		steps = append(steps, step{"comment the questions on " + ticket, func() error {
			return r.Forge.Comment(ctx, n, needsInfoComment(report))
		}})
		if labels.NeedsInfo != "" {
			steps = append(steps, step{"add " + labels.NeedsInfo + " to " + ticket, func() error {
				return r.Forge.AddLabel(ctx, n, labels.NeedsInfo)
			}})
		}
	} else {
		var pull forge.PullRequest
		steps = append(steps, step{"push " + r.branch, func() error {
			return r.quarantine.Push(ctx, r.Forge.GitURL, r.Forge.GitEnv(), r.branch)
		}}, step{"open the pull request", func() (err error) {
			pull, err = r.Forge.CreatePull(ctx, r.branch, r.Base.Branch, r.Ticket.Title, "Closes "+ticket+"\n")
			return
		}}, step{"comment the report on the pull request", func() error {
			return r.Forge.Comment(ctx, pull.Number, reportComment(report))
		}})
		if labels.ReadyForHuman != "" && (len(report.Deviations) > 0 || len(report.Doubts) > 0) {
			steps = append(steps, step{"add " + labels.ReadyForHuman + " to the pull request", func() error {
				return r.Forge.AddLabel(ctx, pull.Number, labels.ReadyForHuman)
			}})
		}
		defer func() {
			if pull.Number != 0 {
				r.say("pull request #%d: %s", pull.Number, pull.URL)
			}
		}()
	}
	steps = append(steps, step{"remove " + labels.ReadyForAgent + " from " + ticket, func() error {
		return r.Forge.RemoveLabel(ctx, n, labels.ReadyForAgent)
	}})

	for i, s := range steps {
		if err = s.do(); err != nil {
			err = fmt.Errorf("%s: %w\n  done: %s\n  left: %s", s.name, err, stepNames(steps[:i]), stepNames(steps[i:]))
			return
		}
		r.say("%s", s.name)
	}
	return
}

func stepNames(steps []step) (names string) {
	if len(steps) == 0 {
		return "nothing"
	}
	list := make([]string, len(steps))
	for i, s := range steps {
		list[i] = s.name
	}
	return strings.Join(list, ", ")
}

// --- texts ---

// commitMessage is the message of the squashed result: the ticket's title
// and number, then the Agent's summary.
func (r run) commitMessage(report agent.Report) (message string) {
	message = fmt.Sprintf("%s (#%d)\n", r.Ticket.Title, r.Ticket.Number)
	if summary := strings.TrimSpace(report.Summary); summary != "" {
		message += "\n" + forGitHub(summary) + "\n"
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
	return forGitHub(b.String())
}

// needsInfoComment tells the ticket's author why the Agent stopped.
func needsInfoComment(report agent.Report) (text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "afk: Agent nie podjął się ticketu. %s\n", report.Reason)
	writeList(&b, "Pytania", report.Questions)
	return forGitHub(b.String())
}

func writeList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n**%s:**\n", title)
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", item)
	}
}

// forGitHub prepares text from the Agent for GitHub: no control or format
// characters, no @-mentions (the Agent must not ping anyone; a zero-width
// space after "@" keeps the text readable) and at most maxGitHubText bytes.
// Markdown stays as is: a human reads it before merging.
func forGitHub(text string) (clean string) {
	clean = agent.Sanitize(text)
	clean = mention.ReplaceAllString(clean, "@\u200b$1")
	if len(clean) <= maxGitHubText {
		return
	}
	cut := maxGitHubText
	for cut > 0 && !utf8.RuneStart(clean[cut]) {
		cut--
	}
	return clean[:cut] + "\n\n… (obcięte)\n"
}
