// Package forge talks to the Forge (GitHub, later Gitea) over REST with the
// host's credentials. Only afk on the host does that; the Agent never has
// Forge access (ADR-0003).
package forge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/graf0/afk/config"
)

const (
	// GitHubAPI is the REST API of github.com.
	GitHubAPI = "https://api.github.com"
	// githubGit is the git (smart HTTP) host of github.com.
	githubGit = "https://github.com/"

	githubAPIVersion = "2022-11-28"
	commentsPerPage  = 100
	maxResponseSize  = 16 << 20
	requestTimeout   = time.Minute
)

// Ticket is a ticket read from the Forge.
type Ticket struct {
	Number   int
	Title    string
	Body     string
	Comments []string
	Labels   []string
}

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
	SubIssues   struct {
		Total int `json:"total"`
	} `json:"sub_issues_summary"`
}

type githubComment struct {
	Body string `json:"body"`
}

// PullRequest is a pull request opened by afk.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
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
	if forge.Kind != "github" {
		err = fmt.Errorf("forge.kind %q is not supported yet", forge.Kind)
		return
	}
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

// GitEnv is the environment for git talking to GitURL with Token. The token
// goes in an Authorization header scoped to GitURL, through git's
// GIT_CONFIG_* variables: never on argv, never in a config file. Credential
// helpers and prompts are off, so a rejected token fails instead of asking.
func (g *GitHub) GitEnv() (env []string) {
	auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.Token))
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_TRACE_REDACT=1",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http." + g.GitURL + ".extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + auth,
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=",
	}
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

// Ticket reads ticket n with its comments. Pull requests, closed issues and
// Specs (issues with sub-issues) are refused: a Run of them is not supported yet.
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
	case issue.SubIssues.Total > 0:
		err = fmt.Errorf("ticket #%d has sub-issues: Specs are not supported yet", n)
	}
	if err != nil {
		return
	}
	ticket = Ticket{Number: issue.Number, Title: issue.Title, Body: issue.Body}
	for _, issueLabel := range issue.Labels {
		ticket.Labels = append(ticket.Labels, issueLabel.Name)
	}
	if label != "" && !slices.Contains(ticket.Labels, label) {
		err = fmt.Errorf("ticket #%d lacks the label %q: only tickets a human marked ready run in the Factory", n, label)
		ticket = Ticket{}
		return
	}
	for page := 1; ; page++ {
		var comments []githubComment
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=%d&page=%d", g.Repo, n, commentsPerPage, page)
		if err = g.get(ctx, path, &comments); err != nil {
			err = fmt.Errorf("reading comments of ticket #%d: %w", n, err)
			return
		}
		for _, comment := range comments {
			ticket.Comments = append(ticket.Comments, comment.Body)
		}
		if len(comments) < commentsPerPage {
			return
		}
	}
}

// --- writes ---

// CreatePull opens a pull request of branch head into base.
func (g *GitHub) CreatePull(ctx context.Context, head, base, title, body string) (pull PullRequest, err error) {
	in := map[string]string{"head": head, "base": base, "title": title, "body": body}
	if err = g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls", g.Repo), in, &pull); err != nil {
		err = fmt.Errorf("opening pull request of %s: %w", head, err)
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

// apiError is a non-2xx response of the GitHub API.
type apiError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *apiError) Error() (text string) {
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.Path, e.Status, http.StatusText(e.Status), e.Message)
}

// get decodes the JSON resource at path (relative to APIURL) into v.
func (g *GitHub) get(ctx context.Context, path string, v any) (err error) {
	err = g.call(ctx, http.MethodGet, path, nil, v)
	return
}

// call sends a method request to path (relative to APIURL) with in as JSON
// body (none when nil) and decodes a 2xx response into out (unless nil).
func (g *GitHub) call(ctx context.Context, method, path string, in, out any) (err error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	base, err := url.Parse(g.APIURL)
	if err != nil {
		err = fmt.Errorf("parsing API URL: %w", err)
		return
	}
	target, err := base.Parse(strings.TrimSuffix(base.Path, "/") + path)
	if err != nil {
		err = fmt.Errorf("building URL: %w", err)
		return
	}
	var body io.Reader
	if in != nil {
		var data []byte
		if data, err = json.Marshal(in); err != nil {
			err = fmt.Errorf("encoding request: %w", err)
			return
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		err = fmt.Errorf("building request: %w", err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("Authorization", "Bearer "+g.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("%s %s: %w", method, target.Path, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		err = fmt.Errorf("%s %s: reading response: %w", method, target.Path, err)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err = &apiError{Method: method, Path: target.Path, Status: resp.StatusCode, Message: apiMessage(data)}
		return
	}
	if out == nil {
		return
	}
	if err = json.Unmarshal(data, out); err != nil {
		err = fmt.Errorf("%s %s: decoding response: %w", method, target.Path, err)
	}
	return
}

// apiMessage returns the "message" of a GitHub error response, else the
// beginning of the body.
func apiMessage(data []byte) (message string) {
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &body) == nil && body.Message != "" {
		return body.Message
	}
	data = bytes.TrimSpace(data)
	if len(data) > 200 {
		data = data[:200]
	}
	return strconv.Quote(string(data))
}
