// Package forge talks to the Forge (GitHub, later Gitea) over REST with the
// host's credentials. Only afk on the host does that; the Agent never has
// Forge access (ADR-0003).
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/graf0/afk/config"
)

const (
	// GitHubAPI is the REST API of github.com.
	GitHubAPI = "https://api.github.com"

	githubAPIVersion = "2022-11-28"
	commentsPerPage  = 100
	maxResponseSize  = 16 << 20
	requestTimeout   = time.Minute
)

// Ticket is a ticket as the Agent gets it.
type Ticket struct {
	Number   int
	Title    string
	Body     string
	Comments []string
}

// GitHub reads tickets of Repo through the GitHub REST API.
type GitHub struct {
	APIURL string // GitHubAPI unless set (tests)
	Repo   string // owner/name
	Token  string
	Client *http.Client // http.DefaultClient unless set
}

// githubIssue is the part of the issue resource afk reads.
type githubIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	PullRequest *struct{} `json:"pull_request"`
	SubIssues   struct {
		Total int `json:"total"`
	} `json:"sub_issues_summary"`
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
	g = &GitHub{APIURL: GitHubAPI, Repo: forge.Repo, Token: token}
	return
}

// Ticket reads ticket n with its comments. Pull requests, closed issues and
// Specs (issues with sub-issues) are refused: a Run of them is not supported yet.
func (g *GitHub) Ticket(ctx context.Context, n int) (ticket Ticket, err error) {
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

// Markdown renders the ticket for the Agent: title, body, comments.
func (t Ticket) Markdown() (text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "# #%d %s\n\n%s\n", t.Number, t.Title, t.Body)
	for _, comment := range t.Comments {
		fmt.Fprintf(&b, "\n---\nKomentarz:\n%s\n", comment)
	}
	return b.String()
}

// --- http ---

// get decodes the JSON resource at path (relative to APIURL) into v.
func (g *GitHub) get(ctx context.Context, path string, v any) (err error) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		err = fmt.Errorf("building request: %w", err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("Authorization", "Bearer "+g.Token)
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("GET %s: %w", target.Path, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		err = fmt.Errorf("GET %s: reading response: %w", target.Path, err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("GET %s: %s: %s", target.Path, resp.Status, apiMessage(data))
		return
	}
	if err = json.Unmarshal(data, v); err != nil {
		err = fmt.Errorf("GET %s: decoding response: %w", target.Path, err)
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
