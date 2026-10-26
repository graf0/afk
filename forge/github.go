package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/graf0/afk/config"
)

const (
	// GitHubAPI is the REST API of github.com.
	GitHubAPI = "https://api.github.com"
	// githubGit is the git (smart HTTP) host of github.com.
	githubGit = "https://github.com/"

	githubAPIVersion = "2022-11-28"
	perPage          = 100
)

var _ Forge = (*GitHub)(nil)

// GitHub reads and updates tickets and pull requests of Repo through the
// GitHub REST API; GitURL is where the ticket branches are pushed.
type GitHub struct {
	APIURL string // GitHubAPI unless set (tests)
	GitURL string // https://github.com/<Repo>.git unless set (tests)
	Repo   string // owner/name
	Token  string
	Client *http.Client // http.DefaultClient unless set
}

// githubIssue is the part of the issue resource afk reads.
type githubIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct{} `json:"pull_request"`
	ParentURL   string    `json:"parent_issue_url"`
	SubIssues   struct {
		Total int `json:"total"`
	} `json:"sub_issues_summary"`
}

// ticket is the ticket as read, without comments.
func (i githubIssue) ticket() (ticket Ticket) {
	ticket = Ticket{Number: i.Number, Title: i.Title, Body: i.Body, SubIssues: i.SubIssues.Total}
	for _, label := range i.Labels {
		ticket.Labels = append(ticket.Labels, label.Name)
	}
	// .../issues/<n>
	if _, last, ok := strings.Cut(strings.TrimSuffix(i.ParentURL, "/"), "/issues/"); ok {
		ticket.Parent, _ = strconv.Atoi(last)
	}
	return
}

type githubComment struct {
	Body string `json:"body"`
}

// --- credentials ---

// HostToken returns the host's GitHub token: GITHUB_TOKEN, else `gh auth token`.
func HostToken(ctx context.Context) (token string, err error) {
	if token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return
	}
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		err = fmt.Errorf("no GITHUB_TOKEN and `gh auth token` failed: %w", err)
		return
	}
	if token = strings.TrimSpace(string(out)); token == "" {
		err = errors.New("no GITHUB_TOKEN and `gh auth token` printed nothing")
	}
	return
}

// --- tickets ---

// NewGitHub returns a GitHub client for the Project's forge.
func NewGitHub(forge config.Forge, token string) (g *GitHub, err error) {
	if forge.URL != "" {
		err = errors.New("forge.url is not supported for github (GitHub Enterprise) yet")
		return
	}
	if owner, name, ok := strings.Cut(forge.Repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		err = fmt.Errorf("invalid forge.repo %q: want owner/name", forge.Repo)
		return
	}
	g = &GitHub{APIURL: GitHubAPI, GitURL: githubGit + forge.Repo + ".git", Repo: forge.Repo, Token: token}
	return
}

// ReadyTicket reads ticket n like Ticket and refuses it without label (the
// Project's ready_for_agent): only a human makes a ticket ready (ADR-0003).
func (g *GitHub) ReadyTicket(ctx context.Context, n int, label string) (ticket Ticket, err error) {
	if label == "" {
		err = errors.New("labels.ready_for_agent is empty")
		return
	}
	ticket, err = g.ticket(ctx, n, label)
	return
}

// Ticket reads ticket n with its comments. Pull requests and closed issues are
// refused. A Spec is read too.
func (g *GitHub) Ticket(ctx context.Context, n int) (ticket Ticket, err error) {
	ticket, err = g.ticket(ctx, n, "")
	return
}

// ticket reads ticket n, refusing it without label unless label is empty.
func (g *GitHub) ticket(ctx context.Context, n int, label string) (ticket Ticket, err error) {
	var issue githubIssue
	if err = g.get(ctx, fmt.Sprintf("/repos/%s/issues/%d", g.Repo, n), &issue); err != nil {
		err = fmt.Errorf("reading ticket #%d: %w", n, err)
		return
	}
	switch {
	case issue.PullRequest != nil:
		err = fmt.Errorf("#%d is a pull request, not a ticket", n)
	case issue.State != "open":
		err = fmt.Errorf("ticket #%d is %s", n, issue.State)
	}
	if err != nil {
		return
	}
	ticket = issue.ticket()
	if label != "" && !ticket.HasLabel(label) {
		err = fmt.Errorf("ticket #%d lacks the label %q: only tickets a human marked ready run in the Factory", n, label)
		ticket = Ticket{}
		return
	}
	for page := 1; ; page++ {
		var comments []githubComment
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=%d&page=%d", g.Repo, n, perPage, page)
		if err = g.get(ctx, path, &comments); err != nil {
			err = fmt.Errorf("reading comments of ticket #%d: %w", n, err)
			return
		}
		for _, comment := range comments {
			ticket.Comments = append(ticket.Comments, comment.Body)
		}
		if len(comments) < perPage {
			return
		}
	}
}

// Peek reads ticket n fresh, in any state and without comments: what the
// Factory decides on (the list by label lags behind). Pull requests are refused.
func (g *GitHub) Peek(ctx context.Context, n int) (ticket Ticket, open bool, err error) {
	var issue githubIssue
	if err = g.get(ctx, fmt.Sprintf("/repos/%s/issues/%d", g.Repo, n), &issue); err != nil {
		err = fmt.Errorf("reading ticket #%d: %w", n, err)
		return
	}
	if issue.PullRequest != nil {
		err = fmt.Errorf("#%d is a pull request, not a ticket", n)
		return
	}
	ticket, open = issue.ticket(), issue.State == "open"
	return
}

// ListLabeled returns the numbers of the open issues with label, ascending.
// The list lags behind labels changed a moment ago: Peek decides.
func (g *GitHub) ListLabeled(ctx context.Context, label string) (numbers []int, err error) {
	for page := 1; ; page++ {
		var issues []githubIssue
		path := fmt.Sprintf("/repos/%s/issues?state=open&labels=%s&per_page=%d&page=%d", g.Repo, url.QueryEscape(label), perPage, page)
		if err = g.get(ctx, path, &issues); err != nil {
			err = fmt.Errorf("listing tickets labeled %q: %w", label, err)
			return
		}
		for _, issue := range issues {
			if issue.PullRequest == nil {
				numbers = append(numbers, issue.Number)
			}
		}
		if len(issues) < perPage {
			slices.Sort(numbers)
			return
		}
	}
}

// Labels lists the names of the labels of the repository.
func (g *GitHub) Labels(ctx context.Context) (names []string, err error) {
	for page := 1; ; page++ {
		var labels []struct {
			Name string `json:"name"`
		}
		path := fmt.Sprintf("/repos/%s/labels?per_page=%d&page=%d", g.Repo, perPage, page)
		if err = g.get(ctx, path, &labels); err != nil {
			err = fmt.Errorf("listing labels: %w", err)
			return
		}
		for _, label := range labels {
			names = append(names, label.Name)
		}
		if len(labels) < perPage {
			return
		}
	}
}

// --- specs ---

// SubIssues lists the sub-issues of Spec n, open and closed.
func (g *GitHub) SubIssues(ctx context.Context, n int) (subs []SubIssue, err error) {
	for page := 1; ; page++ {
		var issues []githubIssue
		path := fmt.Sprintf("/repos/%s/issues/%d/sub_issues?per_page=%d&page=%d", g.Repo, n, perPage, page)
		if err = g.get(ctx, path, &issues); err != nil {
			err = fmt.Errorf("reading sub-issues of #%d: %w", n, err)
			return
		}
		for _, issue := range issues {
			sub := SubIssue{Number: issue.Number, Open: issue.State == "open"}
			for _, label := range issue.Labels {
				sub.Labels = append(sub.Labels, label.Name)
			}
			subs = append(subs, sub)
		}
		if len(issues) < perPage {
			return
		}
	}
}

// Blocked reports whether ticket has an open blocker: a native dependency
// or an issue named in a "Blocked by: #a, #b" line of its body
// (docs/reference/ralph.sh, blocked). The state of the blockers is read, not
// the summary counter on the issue: that one lags after closing.
func (g *GitHub) Blocked(ctx context.Context, ticket Ticket) (blocked bool, err error) {
	for page := 1; ; page++ {
		var blockers []githubIssue
		path := fmt.Sprintf("/repos/%s/issues/%d/dependencies/blocked_by?per_page=%d&page=%d", g.Repo, ticket.Number, perPage, page)
		if err = g.get(ctx, path, &blockers); err != nil {
			err = fmt.Errorf("reading blockers of #%d: %w", ticket.Number, err)
			return
		}
		if slices.ContainsFunc(blockers, func(blocker githubIssue) bool { return blocker.State == "open" }) {
			blocked = true
			return
		}
		if len(blockers) < perPage {
			break
		}
	}
	for _, ref := range blockedByRefs(ticket.Body) {
		var blocker githubIssue
		if err = g.get(ctx, fmt.Sprintf("/repos/%s/issues/%d", g.Repo, ref), &blocker); err != nil {
			err = fmt.Errorf("reading blocker #%d of #%d: %w", ref, ticket.Number, err)
			return
		}
		if blocker.State == "open" {
			blocked = true
			return
		}
	}
	return
}

// --- git ---

// Remote is GitURL with Token in an Authorization header (see tokenRemote).
func (g *GitHub) Remote() (remote Remote) {
	return tokenRemote(g.Repo, g.GitURL, "x-access-token", g.Token)
}

// --- writes ---

// githubPull is the part of the pull request resource afk reads.
type githubPull struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	NodeID string `json:"node_id"` // for GraphQL
}

func (p githubPull) pull() (pull PullRequest) {
	return PullRequest{Number: p.Number, URL: p.URL, State: p.State, Draft: p.Draft, ID: p.NodeID}
}

// CreatePull opens a pull request of branch head into base, as a draft when draft is set.
func (g *GitHub) CreatePull(ctx context.Context, head, base, title, body string, draft bool) (pull PullRequest, err error) {
	in := map[string]any{"head": head, "base": base, "title": title, "body": body}
	if draft {
		in["draft"] = true
	}
	var created githubPull
	if err = g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls", g.Repo), in, &created); err != nil {
		err = fmt.Errorf("opening pull request of %s: %w", head, err)
		return
	}
	pull = created.pull()
	return
}

// FindPull returns the latest pull request, open or not, whose head is branch of Repo.
func (g *GitHub) FindPull(ctx context.Context, branch string) (pull PullRequest, found bool, err error) {
	owner, _, _ := strings.Cut(g.Repo, "/")
	path := fmt.Sprintf("/repos/%s/pulls?state=all&sort=created&direction=desc&per_page=1&head=%s", g.Repo, url.QueryEscape(owner+":"+branch))
	var pulls []githubPull
	if err = g.get(ctx, path, &pulls); err != nil {
		err = fmt.Errorf("looking for the pull request of %s: %w", branch, err)
		return
	}
	if found = len(pulls) > 0; found {
		pull = pulls[0].pull()
	}
	return
}

// UpdatePullBody replaces the description of pull request n.
func (g *GitHub) UpdatePullBody(ctx context.Context, n int, body string) (err error) {
	in := map[string]string{"body": body}
	if err = g.call(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", g.Repo, n), in, nil); err != nil {
		err = fmt.Errorf("updating pull request #%d: %w", n, err)
	}
	return
}

// ReadyPull marks a draft pull request ready for review. Only GraphQL can.
func (g *GitHub) ReadyPull(ctx context.Context, pull PullRequest) (err error) {
	in := map[string]any{
		"query":     "mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { id } } }",
		"variables": map[string]string{"id": pull.ID},
	}
	// GraphQL reports errors in a 200 response.
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err = g.call(ctx, http.MethodPost, "/graphql", in, &out); err == nil && len(out.Errors) > 0 {
		err = errors.New(out.Errors[0].Message)
	}
	if err != nil {
		err = fmt.Errorf("marking pull request #%d ready for review: %w", pull.Number, err)
	}
	return
}

// CloseIssue closes issue n as completed.
func (g *GitHub) CloseIssue(ctx context.Context, n int) (err error) {
	in := map[string]string{"state": "closed", "state_reason": "completed"}
	if err = g.call(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", g.Repo, n), in, nil); err != nil {
		err = fmt.Errorf("closing #%d: %w", n, err)
	}
	return
}

// Comment adds a comment to issue or pull request n.
func (g *GitHub) Comment(ctx context.Context, n int, body string) (err error) {
	in := map[string]string{"body": body}
	if err = g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", g.Repo, n), in, nil); err != nil {
		err = fmt.Errorf("commenting on #%d: %w", n, err)
	}
	return
}

// AddLabel adds label to issue or pull request n.
func (g *GitHub) AddLabel(ctx context.Context, n int, label string) (err error) {
	in := map[string][]string{"labels": {label}}
	if err = g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/labels", g.Repo, n), in, nil); err != nil {
		err = fmt.Errorf("adding label %q to #%d: %w", label, n, err)
	}
	return
}

// RemoveLabel removes label from issue or pull request n; a label it
// doesn't have is no error.
func (g *GitHub) RemoveLabel(ctx context.Context, n int, label string) (err error) {
	path := fmt.Sprintf("/repos/%s/issues/%d/labels/%s", g.Repo, n, url.PathEscape(label))
	err = g.call(ctx, http.MethodDelete, path, nil, nil)
	if apiErr, ok := errors.AsType[*apiError](err); ok && apiErr.Status == http.StatusNotFound {
		err = nil
	}
	if err != nil {
		err = fmt.Errorf("removing label %q from #%d: %w", label, n, err)
	}
	return
}

// --- http ---

// get decodes the JSON resource at path (relative to APIURL) into v.
func (g *GitHub) get(ctx context.Context, path string, v any) (err error) {
	err = g.call(ctx, http.MethodGet, path, nil, v)
	return
}

// call sends a method request to path (relative to APIURL) with in as JSON
// body (none when nil) and decodes a 2xx response into out (unless nil).
func (g *GitHub) call(ctx context.Context, method, path string, in, out any) (err error) {
	header := http.Header{
		"Accept":               {"application/vnd.github+json"},
		"X-GitHub-Api-Version": {githubAPIVersion},
		"Authorization":        {"Bearer " + g.Token},
	}
	err = doJSON(ctx, g.Client, g.APIURL, method, path, header, in, out)
	return
}
