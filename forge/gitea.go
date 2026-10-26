package forge

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/graf0/afk/config"
)

const (
	giteaTokenEnv  = "GITEA_TOKEN"
	giteaTokenFile = "gitea-token"
	// giteaPageSize is Gitea's default maximum page size (MAX_RESPONSE_ITEMS).
	giteaPageSize = 50
)

var (
	// taskItem is an item of a task list in Markdown; the checkbox is
	// ignored, the state of a child comes from its issue.
	taskItem = regexp.MustCompile(`(?m)^[ \t]*[-*+][ \t]+\[[ xX]\][ \t]+(\S.*)$`)
	// taskRef is an item that starts with a reference to an issue of this repository.
	taskRef = regexp.MustCompile(`^#(\d+)(?:\W|$)`)
	// taskForeign is an item that starts with a reference to another
	// repository: owner/repo#M or a URL.
	taskForeign = regexp.MustCompile(`^(?:https?://\S+|[\w.-]+/[\w.-]+#\d+)`)
)

var _ Forge = (*Gitea)(nil)

// Gitea reads tickets of Repo through the Gitea REST API (1.27). It has no
// native sub-issues: the children of a Spec are the `- [ ] #M` items of its
// body. Writes and pushes come in Spec #21's next ticket.
type Gitea struct {
	APIURL    string // <forge.url>/api/v1 (tests: the fake server)
	Repo      string // owner/name
	Token     string
	SpecLabel string       // [labels].spec: only a ticket with it has children
	Client    *http.Client // http.DefaultClient unless set
}

// NewGitea returns a Gitea client for the Project's forge. specLabel is
// [labels].spec. The client follows no redirects: forge.url is the address the
// user vouched for, and the token goes nowhere else.
func NewGitea(forge config.Forge, token, specLabel string) (g *Gitea, err error) {
	base, err := url.Parse(forge.URL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		err = fmt.Errorf("invalid forge.url %q: want an https URL", forge.URL)
		return
	}
	if owner, name, ok := strings.Cut(forge.Repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		err = fmt.Errorf("invalid forge.repo %q: want owner/name", forge.Repo)
		return
	}
	g = &Gitea{APIURL: strings.TrimSuffix(forge.URL, "/") + "/api/v1", Repo: forge.Repo, Token: token, SpecLabel: specLabel, Client: noRedirects()}
	return
}

// noRedirects is an HTTP client that returns a 3xx as it is.
func noRedirects() (client *http.Client) {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// giteaIssue is the part of the issue resource afk reads.
type giteaIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct{} `json:"pull_request"`
}

// ticket is the ticket as read, without comments, sub-issues and parent.
func (i giteaIssue) ticket() (ticket Ticket) {
	ticket = Ticket{Number: i.Number, Title: i.Title, Body: i.Body}
	for _, label := range i.Labels {
		ticket.Labels = append(ticket.Labels, label.Name)
	}
	return
}

type giteaComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// --- credentials ---

// GiteaToken returns the host's Gitea token: GITEA_TOKEN, else the file
// stateDir/gitea-token, which must not be readable by others.
func GiteaToken(stateDir string) (token string, err error) {
	if token = strings.TrimSpace(os.Getenv(giteaTokenEnv)); token != "" {
		if strings.ContainsAny(token, "\r\n") {
			err = errors.New("the Gitea token contains a line break")
		}
		return
	}
	path := filepath.Join(stateDir, giteaTokenFile)
	hint := fmt.Sprintf("set %s or save the token to %s (mode 0600)", giteaTokenEnv, path)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("no Gitea token: %s", hint)
		return
	}
	if err != nil {
		err = fmt.Errorf("reading Gitea token: %w", err)
		return
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		err = fmt.Errorf("refusing Gitea token file %s with mode %v: want a regular file with mode 0600", path, info.Mode())
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		err = fmt.Errorf("reading Gitea token: %w", err)
		return
	}
	if token = strings.TrimSpace(string(data)); token == "" {
		err = fmt.Errorf("token file %s is empty: %s", path, hint)
		return
	}
	if strings.ContainsAny(token, "\r\n") {
		err = errors.New("the Gitea token contains a line break")
	}
	return
}

// --- tickets ---

// ReadyTicket reads ticket n like Ticket and refuses it without label (the
// Project's ready_for_agent): only a human makes a ticket ready (ADR-0003).
func (g *Gitea) ReadyTicket(ctx context.Context, n int, label string) (ticket Ticket, err error) {
	if label == "" {
		err = errors.New("labels.ready_for_agent is empty")
		return
	}
	ticket, err = g.ticket(ctx, n, label)
	return
}

// Ticket reads ticket n with its comments. Pull requests and closed issues are
// refused. A Spec is read too.
func (g *Gitea) Ticket(ctx context.Context, n int) (ticket Ticket, err error) {
	ticket, err = g.ticket(ctx, n, "")
	return
}

// ticket reads ticket n, refusing it without label unless label is empty.
func (g *Gitea) ticket(ctx context.Context, n int, label string) (ticket Ticket, err error) {
	issue, err := g.issue(ctx, n)
	if err != nil {
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
	if ticket, err = g.ticketOf(ctx, issue); err != nil {
		return
	}
	if label != "" && !ticket.HasLabel(label) {
		err = fmt.Errorf("ticket #%d lacks the label %q: only tickets a human marked ready run in the Factory", n, label)
		ticket = Ticket{}
		return
	}
	comments, err := giteaList(ctx, g, fmt.Sprintf("/repos/%s/issues/%d/comments?", g.Repo, n), func(c giteaComment) int64 { return c.ID })
	if err != nil {
		err = fmt.Errorf("reading comments of ticket #%d: %w", n, err)
		return
	}
	for _, comment := range comments {
		ticket.Comments = append(ticket.Comments, comment.Body)
	}
	return
}

// Peek reads ticket n fresh, in any state and without comments: what the
// Factory decides on (the list by label lags behind). Pull requests are refused.
func (g *Gitea) Peek(ctx context.Context, n int) (ticket Ticket, open bool, err error) {
	issue, err := g.issue(ctx, n)
	if err != nil {
		return
	}
	if issue.PullRequest != nil {
		err = fmt.Errorf("#%d is a pull request, not a ticket", n)
		return
	}
	if ticket, err = g.ticketOf(ctx, issue); err != nil {
		return
	}
	open = issue.State == "open"
	return
}

// issue reads issue (or pull request) n.
func (g *Gitea) issue(ctx context.Context, n int) (issue giteaIssue, err error) {
	if err = g.get(ctx, fmt.Sprintf("/repos/%s/issues/%d", g.Repo, n), &issue); err != nil {
		err = fmt.Errorf("reading ticket #%d: %w", n, err)
	}
	return
}

// ticketOf is the ticket of issue with its sub-issue count (a Spec: the task
// list) and its Spec.
func (g *Gitea) ticketOf(ctx context.Context, issue giteaIssue) (ticket Ticket, err error) {
	ticket = issue.ticket()
	if ticket.HasLabel(g.SpecLabel) {
		var children []int
		if children, err = specChildren(ticket); err != nil {
			return
		}
		ticket.SubIssues = len(children)
	}
	ticket.Parent, err = g.parentOf(ctx, ticket.Number)
	return
}

// ListLabeled returns the numbers of the open issues with label, ascending.
// The list lags behind labels changed a moment ago: Peek decides.
func (g *Gitea) ListLabeled(ctx context.Context, label string) (numbers []int, err error) {
	issues, err := g.listOpen(ctx, label)
	if err != nil {
		err = fmt.Errorf("listing tickets labeled %q: %w", label, err)
		return
	}
	for _, issue := range issues {
		numbers = append(numbers, issue.Number)
	}
	return
}

// listOpen returns the open issues (no pull requests) with label, ascending,
// with their bodies. Gitea may drop a filter it does not know, and whether
// names match regardless of case depends on its database: the label is
// checked here as well.
func (g *Gitea) listOpen(ctx context.Context, label string) (issues []giteaIssue, err error) {
	listed, err := giteaList(ctx, g, "/repos/"+g.Repo+"/issues?state=open&type=issues&labels="+url.QueryEscape(label)+"&", func(i giteaIssue) int { return i.Number })
	if err != nil {
		return
	}
	for _, issue := range listed {
		if issue.PullRequest == nil && hasLabel(issue.ticket().Labels, label) {
			issues = append(issues, issue)
		}
	}
	slices.SortFunc(issues, func(a, b giteaIssue) int { return a.Number - b.Number })
	return
}

// Labels lists the names of the labels of the repository.
func (g *Gitea) Labels(ctx context.Context) (names []string, err error) {
	type label struct {
		Name string `json:"name"`
	}
	labels, err := giteaList(ctx, g, "/repos/"+g.Repo+"/labels?", func(l label) string { return l.Name })
	if err != nil {
		err = fmt.Errorf("listing labels: %w", err)
		return
	}
	for _, label := range labels {
		names = append(names, label.Name)
	}
	return
}

// --- specs ---

// specChildren returns the issues of the task list of a Spec, once each in
// the order of the list. A reference to another repository, or to the Spec
// itself, is an error.
func specChildren(spec Ticket) (children []int, err error) {
	var foreign []string
	children, foreign = taskListRefs(spec.Body)
	switch {
	case len(foreign) > 0:
		err = fmt.Errorf("spec #%d lists %q: only issues of this repository (#M) can be sub-issues", spec.Number, foreign[0])
	case slices.Contains(children, spec.Number):
		err = fmt.Errorf("spec #%d lists itself as a sub-issue", spec.Number)
	}
	return
}

// taskListRefs returns the issues (#M, once each) and the references to other
// repositories found in the task list items of body. The checkbox is ignored.
func taskListRefs(body string) (refs []int, foreign []string) {
	for _, item := range taskItem.FindAllStringSubmatch(body, -1) {
		text := strings.TrimSpace(item[1])
		if m := taskRef.FindStringSubmatch(text); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && !slices.Contains(refs, n) {
				refs = append(refs, n)
			}
		} else if other := taskForeign.FindString(text); other != "" {
			foreign = append(foreign, other)
		}
	}
	return
}

// SubIssues lists the sub-issues of Spec n, open and closed: the issues of the
// task list of its body, each read for its state and labels. A ticket without
// the spec label has none. A pull request in the list is an error.
func (g *Gitea) SubIssues(ctx context.Context, n int) (subs []SubIssue, err error) {
	spec, err := g.issue(ctx, n)
	if err != nil {
		return
	}
	if !hasLabel(spec.ticket().Labels, g.SpecLabel) {
		return
	}
	children, err := specChildren(spec.ticket())
	if err != nil {
		return
	}
	for _, child := range children {
		var issue giteaIssue
		if issue, err = g.issue(ctx, child); err != nil {
			err = fmt.Errorf("reading sub-issue of #%d: %w", n, err)
			return
		}
		if issue.PullRequest != nil {
			err = fmt.Errorf("spec #%d lists #%d, which is a pull request, not a ticket", n, child)
			return
		}
		sub := SubIssue{Number: child, Open: issue.State == "open"}
		sub.Labels = issue.ticket().Labels
		subs = append(subs, sub)
	}
	return
}

// parentOf returns the Spec that lists ticket n in its task list, the lowest
// one if several do, zero without one. Gitea has no parent link, so this
// reads every open issue with the spec label (a page of 50 per request, bodies
// included): one request for a repository with fewer than 50 open Specs. A
// malformed task list of another Spec is not this ticket's problem.
func (g *Gitea) parentOf(ctx context.Context, n int) (parent int, err error) {
	if g.SpecLabel == "" {
		return
	}
	specs, err := g.listOpen(ctx, g.SpecLabel)
	if err != nil {
		err = fmt.Errorf("looking for the Spec of #%d: %w", n, err)
		return
	}
	for _, spec := range specs {
		if refs, _ := taskListRefs(spec.Body); spec.Number != n && slices.Contains(refs, n) {
			parent = spec.Number
			return
		}
	}
	return
}

// Blocked reports whether ticket has an open blocker: a native dependency
// or an issue named in a "Blocked by: #a, #b" line of its body. With
// dependencies disabled in the repository only the line counts.
func (g *Gitea) Blocked(ctx context.Context, ticket Ticket) (blocked bool, err error) {
	blockers, _, err := g.dependencies(ctx, ticket.Number)
	if err != nil {
		return
	}
	if slices.ContainsFunc(blockers, func(blocker giteaIssue) bool { return blocker.State == "open" }) {
		blocked = true
		return
	}
	for _, ref := range blockedByRefs(ticket.Body) {
		var blocker giteaIssue
		if blocker, err = g.issue(ctx, ref); err != nil {
			err = fmt.Errorf("reading blocker of #%d: %w", ticket.Number, err)
			return
		}
		if blocker.State == "open" {
			blocked = true
			return
		}
	}
	return
}

// dependencies returns the native blockers of issue n; enabled is false when
// the repository has dependencies disabled.
func (g *Gitea) dependencies(ctx context.Context, n int) (blockers []giteaIssue, enabled bool, err error) {
	blockers, err = giteaList(ctx, g, fmt.Sprintf("/repos/%s/issues/%d/dependencies?", g.Repo, n), func(i giteaIssue) int { return i.Number })
	if apiErr, ok := errors.AsType[*apiError](err); ok && apiErr.Status == http.StatusNotFound {
		// Gitea answers 404 when the repository has dependencies
		// disabled, as when the issue does not exist: telling them
		// apart takes reading the issue.
		_, err = g.issue(ctx, n)
		return
	}
	if err != nil {
		err = fmt.Errorf("reading blockers of #%d: %w", n, err)
		return
	}
	enabled = true
	return
}

// --- not yet ---

// notSupported is what the writes of Gitea answer until Spec #21's next ticket.
func notSupported(op string) (err error) {
	err = fmt.Errorf("forge gitea: %s is not supported yet", op)
	return
}

// Remote is empty: pushing to Gitea is not supported yet, and the Factory
// refuses a Gitea Project before it needs one.
func (g *Gitea) Remote() (remote Remote) {
	return Remote{Repo: g.Repo}
}

// CreatePull is not supported yet.
func (g *Gitea) CreatePull(ctx context.Context, head, base, title, body string, draft bool) (pull PullRequest, err error) {
	err = notSupported("opening a pull request")
	return
}

// FindPull is not supported yet.
func (g *Gitea) FindPull(ctx context.Context, branch string) (pull PullRequest, found bool, err error) {
	err = notSupported("looking for a pull request")
	return
}

// UpdatePullBody is not supported yet.
func (g *Gitea) UpdatePullBody(ctx context.Context, n int, body string) (err error) {
	err = notSupported("updating a pull request")
	return
}

// ReadyPull is not supported yet.
func (g *Gitea) ReadyPull(ctx context.Context, pull PullRequest) (err error) {
	err = notSupported("marking a pull request ready")
	return
}

// CloseIssue is not supported yet.
func (g *Gitea) CloseIssue(ctx context.Context, n int) (err error) {
	err = notSupported("closing an issue")
	return
}

// Comment is not supported yet.
func (g *Gitea) Comment(ctx context.Context, n int, body string) (err error) {
	err = notSupported("commenting")
	return
}

// AddLabel is not supported yet.
func (g *Gitea) AddLabel(ctx context.Context, n int, label string) (err error) {
	err = notSupported("adding a label")
	return
}

// RemoveLabel is not supported yet.
func (g *Gitea) RemoveLabel(ctx context.Context, n int, label string) (err error) {
	err = notSupported("removing a label")
	return
}

// CheckWrites refuses a Project whose Forge cannot be written to yet: the
// Factory would run its VMs and stop at the first write.
func CheckWrites(cfg config.Forge) (err error) {
	if cfg.Kind == "gitea" {
		err = errors.New("Gitea: writes (push, pull requests, comments) are not supported yet")
	}
	return
}

// --- http ---

// get decodes the JSON resource at path (relative to APIURL) into v. A
// redirect is an error: forge.url must be the final address.
func (g *Gitea) get(ctx context.Context, path string, v any) (err error) {
	header := http.Header{
		"Accept":        {"application/json"},
		"Authorization": {"token " + g.Token},
	}
	err = doJSON(ctx, g.Client, g.APIURL, http.MethodGet, path, header, nil, v)
	if apiErr, ok := errors.AsType[*apiError](err); ok && apiErr.Status >= 300 && apiErr.Status < 400 {
		err = fmt.Errorf("%w (redirects are not followed, so the token goes only to forge.url: set forge.url to the final https address of the instance)", err)
	}
	return
}

// giteaList reads every page of the list at path (ending in "?" or "&" for
// the page query) until a page brings nothing new, by id: Gitea's page size
// limit is configurable and some lists are not paged at all.
func giteaList[T any, K comparable](ctx context.Context, g *Gitea, path string, id func(T) K) (items []T, err error) {
	seen := map[K]bool{}
	for page := 1; ; page++ {
		var got []T
		if err = g.get(ctx, fmt.Sprintf("%spage=%d&limit=%d", path, page, giteaPageSize), &got); err != nil {
			return
		}
		fresh := 0
		for _, item := range got {
			if k := id(item); !seen[k] {
				seen[k] = true
				items = append(items, item)
				fresh++
			}
		}
		if fresh == 0 {
			return
		}
	}
}
