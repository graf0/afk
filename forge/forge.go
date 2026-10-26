// Package forge talks to the Forge (GitHub, Gitea) over REST with the
// host's credentials. Only afk on the host does that; the Agent never has
// Forge access (ADR-0003).
package forge

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/graf0/afk/config"
)

// Forge is what afk needs of the Project's Forge: tickets, Specs, the pull
// request of a Run and the git remote its branch is pushed to. GitHub is the
// first implementation; Gitea is the second (reads so far, Spec #21), which is
// what makes this an interface (docs/architecture.md).
type Forge interface {
	// ReadyTicket reads ticket n like Ticket and refuses it without label.
	ReadyTicket(ctx context.Context, n int, label string) (ticket Ticket, err error)
	// Ticket reads open issue n with its comments; a Spec is read too.
	Ticket(ctx context.Context, n int) (ticket Ticket, err error)
	// Peek reads ticket n fresh, in any state and without comments.
	Peek(ctx context.Context, n int) (ticket Ticket, open bool, err error)
	// ListLabeled returns the numbers of the open issues with label, ascending.
	ListLabeled(ctx context.Context, label string) (numbers []int, err error)
	// Labels lists the names of the labels the repository has.
	Labels(ctx context.Context) (names []string, err error)

	// SubIssues lists the sub-issues of Spec n, open and closed.
	SubIssues(ctx context.Context, n int) (subs []SubIssue, err error)
	// Blocked reports whether ticket has an open blocker.
	Blocked(ctx context.Context, ticket Ticket) (blocked bool, err error)

	// Remote is where git pushes ticket branches.
	Remote() (remote Remote)

	// CreatePull opens a pull request of branch head into base, as a draft when draft is set.
	CreatePull(ctx context.Context, head, base, title, body string, draft bool) (pull PullRequest, err error)
	// FindPull returns the latest pull request, open or not, whose head is branch.
	FindPull(ctx context.Context, branch string) (pull PullRequest, found bool, err error)
	// UpdatePullBody replaces the description of pull request n.
	UpdatePullBody(ctx context.Context, n int, body string) (err error)
	// ReadyPull marks a draft pull request ready for review.
	ReadyPull(ctx context.Context, pull PullRequest) (err error)
	// CloseIssue closes issue n as completed.
	CloseIssue(ctx context.Context, n int) (err error)
	// Comment adds a comment to issue or pull request n.
	Comment(ctx context.Context, n int, body string) (err error)
	// AddLabel adds label to issue or pull request n.
	AddLabel(ctx context.Context, n int, label string) (err error)
	// RemoveLabel removes label from issue or pull request n; a label it
	// doesn't have is no error.
	RemoveLabel(ctx context.Context, n int, label string) (err error)
}

// Ticket is a ticket read from the Forge.
type Ticket struct {
	Number   int
	Title    string
	Body     string
	Comments []string
	Labels   []string

	// SubIssues is the number of sub-issues. Whether the ticket is a Spec
	// is up to its label ([labels].spec), not to this number.
	SubIssues int
	// Parent is the Spec the ticket is a sub-issue of, zero without one.
	Parent int
}

// HasLabel reports whether the ticket carries label, ignoring case: GitHub
// and Gitea both treat label names that way.
func (t Ticket) HasLabel(name string) (has bool) { return hasLabel(t.Labels, name) }

// HasLabel reports whether the sub-issue carries label, ignoring case.
func (s SubIssue) HasLabel(name string) (has bool) { return hasLabel(s.Labels, name) }

func hasLabel(labels []string, name string) (has bool) {
	return slices.ContainsFunc(labels, func(label string) bool { return strings.EqualFold(label, name) })
}

// SubIssue is a sub-issue of a Spec as listed on the Forge.
type SubIssue struct {
	Number int
	Open   bool
	Labels []string
}

// PullRequest is a pull request of afk.
type PullRequest struct {
	Number int
	URL    string
	State  string // open or closed (merged too)
	Draft  bool
	// ID is an identifier the implementation gave the pull request and
	// needs back (GitHub: the GraphQL node id); callers pass it on as is.
	ID string
}

// Remote is where git pushes ticket branches, with the environment that
// authenticates it. Repo is owner/name on the Forge.
type Remote struct {
	Repo string
	URL  string
	Env  []string
}

// New returns the Forge of the Project's config, with the host's credentials.
// [labels].spec: Gitea has no sub-issues, so it is the label that
// makes a task list the children of a Spec. stateDir (~/.local/state/afk)
// holds the Gitea token file.
func New(ctx context.Context, cfg config.Config, stateDir string) (f Forge, err error) {
	switch cfg.Forge.Kind {
	case "github":
		var token string
		if token, err = HostToken(ctx); err != nil {
			return
		}
		f, err = NewGitHub(cfg.Forge, token)
	case "gitea":
		var token string
		if token, err = GiteaToken(stateDir); err != nil {
			return
		}
		f, err = NewGitea(cfg.Forge, token, cfg.Labels.Spec)
	default:
		err = fmt.Errorf("forge.kind %q is not supported yet", cfg.Forge.Kind)
	}
	return
}

var (
	// blockedBy is a line of a ticket's body naming its blockers: the
	// convention for Forges without native dependencies (docs/workflow.md).
	blockedBy = regexp.MustCompile(`(?im)^blocked by:(.*)$`)
	issueRef  = regexp.MustCompile(`#(\d+)`)
)

// blockedByRefs returns the issues named in the "Blocked by: #a, #b" lines of body.
func blockedByRefs(body string) (refs []int) {
	for _, line := range blockedBy.FindAllStringSubmatch(body, -1) {
		for _, ref := range issueRef.FindAllStringSubmatch(line[1], -1) {
			if n, err := strconv.Atoi(ref[1]); err == nil {
				refs = append(refs, n)
			}
		}
	}
	return
}
