package forge

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/graf0/afk/config"
)

// fakeGitHub serves issue 7 of o/r with 150 comments (two pages) and a few
// refused issues.
func fakeGitHub(t *testing.T) (g *GitHub) {
	t.Helper()
	mux := http.NewServeMux()
	issue := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}
	mux.HandleFunc("GET /repos/o/r/issues/{n}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-GitHub-Api-Version") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		switch r.PathValue("n") {
		case "7":
			issue(w, `{"number":7,"title":"Do it","body":"Body text","state":"open","labels":[{"name":"bug"},{"name":"ready-for-agent"}],"sub_issues_summary":{"total":0}}`)
		case "12":
			issue(w, `{"number":12,"title":"Not ready","state":"open","labels":[{"name":"needs-triage"}]}`)
		case "8":
			issue(w, `{"number":8,"title":"PR","state":"open","pull_request":{"url":"x"}}`)
		case "9":
			issue(w, `{"number":9,"title":"Closed","state":"closed"}`)
		case "10":
			issue(w, `{"number":10,"title":"Spec","state":"open","sub_issues_summary":{"total":2}}`)
		case "20":
			issue(w, `{"number":20,"title":"Done blocker","state":"closed"}`)
		case "21":
			issue(w, `{"number":21,"title":"Open blocker","state":"open"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	})
	mux.HandleFunc("GET /repos/o/r/issues/10/sub_issues", func(w http.ResponseWriter, r *http.Request) {
		issue(w, `[{"number":13,"state":"closed","labels":[]},{"number":11,"state":"open","labels":[{"name":"ready-for-agent"}]}]`)
	})
	mux.HandleFunc("GET /repos/o/r/issues/10/comments", func(w http.ResponseWriter, r *http.Request) { issue(w, `[]`) })
	mux.HandleFunc("GET /repos/o/r/issues/{n}/dependencies/blocked_by", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("n") {
		case "11":
			issue(w, `[{"number":20,"state":"closed"}]`)
		case "12":
			issue(w, `[{"number":20,"state":"closed"},{"number":21,"state":"open"}]`)
		default:
			issue(w, `[]`)
		}
	})
	mux.HandleFunc("GET /repos/o/r/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		first, last := (page-1)*perPage, min(page*perPage, 150)
		var items []string
		for i := first; i < last; i++ {
			items = append(items, fmt.Sprintf(`{"body":"comment %d"}`, i))
		}
		_, _ = fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	g, err := NewGitHub(config.Forge{Kind: "github", Repo: "o/r"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	g.APIURL = server.URL
	return
}

func TestGitHubTicket(t *testing.T) {
	g := fakeGitHub(t)
	ticket, err := g.Ticket(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Number != 7 || ticket.Title != "Do it" || ticket.Body != "Body text" {
		t.Errorf("ticket = %+v", ticket)
	}
	if len(ticket.Comments) != 150 || ticket.Comments[0] != "comment 0" || ticket.Comments[149] != "comment 149" {
		t.Errorf("got %d comments, want 150 in order", len(ticket.Comments))
	}
}

func TestGitHubReadyTicket(t *testing.T) {
	g := fakeGitHub(t)
	ticket, err := g.ReadyTicket(t.Context(), 7, "ready-for-agent")
	if err != nil || ticket.Number != 7 || !slices.Equal(ticket.Labels, []string{"bug", "ready-for-agent"}) {
		t.Errorf("ReadyTicket(7) = %+v, %v", ticket, err)
	}
	if _, err = g.ReadyTicket(t.Context(), 12, "ready-for-agent"); err == nil || !strings.Contains(err.Error(), `"ready-for-agent"`) {
		t.Errorf("ReadyTicket(12): err = %v, want refusal naming the label", err)
	}
	if _, err = g.ReadyTicket(t.Context(), 7, ""); err == nil {
		t.Error("empty label: want error")
	}
	if _, err = g.ReadyTicket(t.Context(), 7, "go"); err == nil || !strings.Contains(err.Error(), `"go"`) {
		t.Errorf("custom label: err = %v", err)
	}
}

func TestGitHubTicketRefused(t *testing.T) {
	g := fakeGitHub(t)
	for n, want := range map[int]string{8: "pull request", 9: "closed", 11: "Not Found"} {
		if _, err := g.Ticket(t.Context(), n); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Ticket(%d): err = %v, want %q", n, err, want)
		}
	}
	g.Token = "wrong"
	if _, err := g.Ticket(t.Context(), 7); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("wrong token: err = %v", err)
	}
}

func TestGitHubSpec(t *testing.T) {
	g := fakeGitHub(t)
	spec, err := g.Ticket(t.Context(), 10)
	if err != nil || spec.SubIssues != 2 {
		t.Errorf("Ticket(10) = %+v, %v; want a Spec of 2 sub-issues", spec, err)
	}
	subs, err := g.SubIssues(t.Context(), 10)
	want := []SubIssue{{Number: 13}, {Number: 11, Open: true, Labels: []string{"ready-for-agent"}}}
	if err != nil || !reflect.DeepEqual(subs, want) {
		t.Errorf("SubIssues(10) = %+v, %v; want %+v", subs, err, want)
	}
	for name, tc := range map[string]struct {
		ticket  Ticket
		blocked bool
	}{
		"none":                  {Ticket{Number: 7}, false},
		"native, closed":        {Ticket{Number: 11}, false},
		"native, open":          {Ticket{Number: 12}, true},
		"body, closed":          {Ticket{Number: 7, Body: "Intro\nBlocked by: #20\n"}, false},
		"body, one open":        {Ticket{Number: 7, Body: "blocked by: #20, #21"}, true},
		"not at the line start": {Ticket{Number: 7, Body: "Not blocked by: #21"}, false},
	} {
		if blocked, err := g.Blocked(t.Context(), tc.ticket); err != nil || blocked != tc.blocked {
			t.Errorf("%s: Blocked = %v, %v; want %v", name, blocked, err, tc.blocked)
		}
	}
	if _, err = g.Blocked(t.Context(), Ticket{Number: 7, Body: "Blocked by: #99"}); err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("unknown blocker: err = %v", err)
	}
}

func TestNewGitHub(t *testing.T) {
	for _, forge := range []config.Forge{
		{Kind: "gitea", Repo: "o/r"},
		{Kind: "github", Repo: "o/r", URL: "https://ghe.example"},
		{Kind: "github", Repo: "o"},
		{Kind: "github", Repo: "o/r/x"},
	} {
		if _, err := NewGitHub(forge, "t"); err == nil {
			t.Errorf("NewGitHub(%+v): want error", forge)
		}
	}
}

func TestHostToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", " env-token\n")
	token, err := HostToken(t.Context())
	if err != nil || token != "env-token" {
		t.Errorf("HostToken = %q, %v", token, err)
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", t.TempDir()) // no gh
	if _, err := HostToken(t.Context()); err == nil || !strings.Contains(err.Error(), "gh auth token") {
		t.Errorf("without gh: err = %v", err)
	}
}

// recorder is a fake GitHub API that records write requests as
// "METHOD path body" and answers them with status (201 unless set).
type recorder struct {
	requests []string
	status   map[string]int    // by "METHOD path"
	reply    map[string]string // body of the response, by "METHOD path"; a pull request unless set
}

func (rec *recorder) serve(t *testing.T) (g *GitHub) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 && r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s: Content-Type %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		rec.requests = append(rec.requests, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(body)))
		status, ok := rec.status[r.Method+" "+r.URL.EscapedPath()]
		if !ok {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
		if status >= 300 {
			_, _ = fmt.Fprint(w, `{"message":"Nope"}`)
			return
		}
		reply, ok := rec.reply[r.Method+" "+r.URL.EscapedPath()]
		if !ok {
			reply = `{"number":42,"html_url":"https://github.com/o/r/pull/42"}`
		}
		_, _ = fmt.Fprint(w, reply)
	}))
	t.Cleanup(server.Close)
	g, err := NewGitHub(config.Forge{Kind: "github", Repo: "o/r"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	g.APIURL = server.URL
	return
}

func TestGitHubWrites(t *testing.T) {
	rec := &recorder{status: map[string]int{"DELETE /repos/o/r/issues/8/labels/ready-for-agent": http.StatusNotFound}}
	g := rec.serve(t)
	pull, err := g.CreatePull(t.Context(), "ticket-7", "main", "Do it", "Closes #7\n", false)
	if err != nil || pull.Number != 42 || pull.URL != "https://github.com/o/r/pull/42" {
		t.Errorf("CreatePull = %+v, %v", pull, err)
	}
	for _, err := range []error{
		g.Comment(t.Context(), 42, "report"),
		g.AddLabel(t.Context(), 42, "ready-for-human"),
		g.RemoveLabel(t.Context(), 7, "ready for/agent"),
		g.RemoveLabel(t.Context(), 8, "ready-for-agent"), // not on the issue: fine
		g.UpdatePullBody(t.Context(), 42, "new body"),
		g.CloseIssue(t.Context(), 7),
	} {
		if err != nil {
			t.Error(err)
		}
	}
	want := []string{
		`POST /repos/o/r/pulls {"base":"main","body":"Closes #7\n","head":"ticket-7","title":"Do it"}`,
		`POST /repos/o/r/issues/42/comments {"body":"report"}`,
		`POST /repos/o/r/issues/42/labels {"labels":["ready-for-human"]}`,
		`DELETE /repos/o/r/issues/7/labels/ready%20for%2Fagent`,
		`DELETE /repos/o/r/issues/8/labels/ready-for-agent`,
		`PATCH /repos/o/r/pulls/42 {"body":"new body"}`,
		`PATCH /repos/o/r/issues/7 {"state":"closed","state_reason":"completed"}`,
	}
	if !slices.Equal(rec.requests, want) {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(rec.requests, "\n"), strings.Join(want, "\n"))
	}

	rec.status = map[string]int{
		"POST /repos/o/r/pulls":                             http.StatusUnprocessableEntity,
		"DELETE /repos/o/r/issues/7/labels/ready-for-agent": http.StatusForbidden,
	}
	if _, err = g.CreatePull(t.Context(), "ticket-7", "main", "t", "b", false); err == nil || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("CreatePull refused: err = %v", err)
	}
	if err = g.RemoveLabel(t.Context(), 7, "ready-for-agent"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("RemoveLabel forbidden: err = %v", err)
	}
}

func TestGitHubPulls(t *testing.T) {
	rec := &recorder{reply: map[string]string{
		"GET /repos/o/r/pulls": `[{"number":42,"html_url":"https://github.com/o/r/pull/42","state":"open","draft":true,"node_id":"PR_x"}]`,
		"POST /graphql":        `{"data":null,"errors":[{"message":"Not a draft"}]}`,
	}}
	g := rec.serve(t)
	draft, err := g.CreatePull(t.Context(), "ticket-7", "main", "Do it", "Closes #7\n", true)
	if err != nil || draft.Number != 42 {
		t.Errorf("CreatePull draft = %+v, %v", draft, err)
	}
	pull, found, err := g.FindPull(t.Context(), "ticket-7")
	if err != nil || !found || pull.Number != 42 || pull.State != "open" || !pull.Draft || pull.NodeID != "PR_x" {
		t.Errorf("FindPull = %+v, %v, %v", pull, found, err)
	}
	if err = g.ReadyPull(t.Context(), pull); err == nil || !strings.Contains(err.Error(), "Not a draft") {
		t.Errorf("ReadyPull with a GraphQL error: err = %v", err)
	}
	rec.reply["POST /graphql"] = `{"data":{"markPullRequestReadyForReview":{"pullRequest":{"id":"PR_x"}}}}`
	if err = g.ReadyPull(t.Context(), pull); err != nil {
		t.Errorf("ReadyPull: %v", err)
	}
	rec.reply["GET /repos/o/r/pulls"] = `[]`
	if _, found, err = g.FindPull(t.Context(), "ticket-8"); err != nil || found {
		t.Errorf("FindPull of none = %v, %v", found, err)
	}
	graphql := `"query":"mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { id } } }","variables":{"id":"PR_x"}`
	want := []string{
		`POST /repos/o/r/pulls {"base":"main","body":"Closes #7\n","draft":true,"head":"ticket-7","title":"Do it"}`,
		`GET /repos/o/r/pulls?state=all&sort=created&direction=desc&per_page=1&head=o%3Aticket-7`,
		`POST /graphql {` + graphql + `}`,
		`POST /graphql {` + graphql + `}`,
		`GET /repos/o/r/pulls?state=all&sort=created&direction=desc&per_page=1&head=o%3Aticket-8`,
	}
	if !slices.Equal(rec.requests, want) {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(rec.requests, "\n"), strings.Join(want, "\n"))
	}
}

// TestGitHubRemote checks that git sends the token in a header to GitURL
// only, that it appears neither on argv nor in git's error, and that the
// user's global config (url.insteadOf) and redirects can't send it elsewhere.
func TestGitHubRemote(t *testing.T) {
	var (
		mu   sync.Mutex
		auth []string
	)
	record := func(request string) {
		mu.Lock()
		defer mu.Unlock()
		auth = append(auth, request)
	}
	requests := func() (got []string) {
		mu.Lock()
		defer mu.Unlock()
		got, auth = auth, nil
		return
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("other " + r.URL.Path + " " + r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(other.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r.URL.Path + " " + r.Header.Get("Authorization"))
		if strings.HasPrefix(r.URL.Path, "/moved/") {
			http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	// The user's global config sends everything for server to other.
	home := t.TempDir()
	rule := fmt.Sprintf("[url %q]\n\tinsteadOf = %s/\n\tpushInsteadOf = %s/\n", other.URL+"/", server.URL, server.URL)
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(rule), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	_ = os.Unsetenv("GIT_CONFIG_GLOBAL")
	lsRemote := func(url string, env []string) (out string, args []string) {
		cmd := exec.Command("git", "ls-remote", url)
		cmd.Env = append(os.Environ(), env...)
		data, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("ls-remote %s: want error", url)
		}
		return string(data), cmd.Args
	}
	lsRemote(server.URL+"/o/r.git", nil)
	if got := requests(); len(got) != 1 || !strings.HasPrefix(got[0], "other ") {
		t.Fatalf("without Remote's env the rule applies, got requests %q", got)
	}

	g, err := NewGitHub(config.Forge{Kind: "github", Repo: "o/r"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if g.GitURL != "https://github.com/o/r.git" {
		t.Errorf("GitURL = %q", g.GitURL)
	}
	for _, url := range []string{server.URL + "/o/r.git", server.URL + "/x/r.git", server.URL + "/moved/r.git"} {
		g.GitURL = server.URL + "/o/r.git"
		if strings.HasPrefix(url, server.URL+"/moved/") {
			g.GitURL = url
		}
		remote := g.Remote()
		out, args := lsRemote(url, remote.Env)
		if strings.Contains(out, "secret") || strings.Contains(strings.Join(args, " "), "secret") {
			t.Errorf("token leaked: %s %s", args, out)
		}
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:secret"))
	want := []string{"/o/r.git/info/refs " + basic, "/x/r.git/info/refs ", "/moved/r.git/info/refs " + basic}
	if got := requests(); !slices.Equal(got, want) {
		t.Errorf("requests with Authorization = %q, want %q", got, want)
	}
}
