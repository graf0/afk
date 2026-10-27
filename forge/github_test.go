package forge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/graf0/afk/config"
)

// fakeGitHub is a stateful fake GitHub API of o/r, token "secret": the
// GitHub half of the contract.
type fakeGitHub struct {
	mu     sync.Mutex
	issues map[int]*seedIssue
	labels []string
	pulls  []*seedPull
}

// newFakeGitHub is the newFake of GitHub.
func newFakeGitHub(t *testing.T, issues []seedIssue, labels []string) (f Forge, state func() fakeState) {
	t.Helper()
	fake := &fakeGitHub{issues: map[int]*seedIssue{}, labels: labels}
	for _, issue := range issues {
		fake.issues[issue.Number] = &issue
	}
	server := httptest.NewServer(fake.mux())
	t.Cleanup(server.Close)
	g, err := NewGitHub(config.Forge{Kind: "github", Repo: "o/r"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	g.APIURL = server.URL
	return g, fake.snapshot
}

func (fake *fakeGitHub) snapshot() (state fakeState) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	state.Issues = map[int]seedIssue{}
	for n, issue := range fake.issues {
		state.Issues[n] = *issue
	}
	for _, pull := range fake.pulls {
		state.Pulls = append(state.Pulls, *pull)
	}
	return
}

func (fake *fakeGitHub) children(n int) (children []*seedIssue) {
	for _, issue := range fake.issues {
		if issue.Parent == n {
			children = append(children, issue)
		}
	}
	slices.SortFunc(children, func(a, b *seedIssue) int { return a.Number - b.Number })
	return
}

// issueJSON is the issue resource as GitHub serves it.
func (fake *fakeGitHub) issueJSON(issue *seedIssue) (resource map[string]any) {
	labels := []map[string]string{}
	for _, label := range issue.Labels {
		labels = append(labels, map[string]string{"name": label})
	}
	resource = map[string]any{
		"number": issue.Number, "title": issue.Title, "body": issue.Body,
		"state": "closed", "labels": labels,
		"sub_issues_summary": map[string]int{"total": len(fake.children(issue.Number))},
	}
	if issue.Open {
		resource["state"] = "open"
	}
	if issue.IsPull {
		resource["pull_request"] = map[string]string{"url": "x"}
	}
	if issue.Parent != 0 {
		resource["parent_issue_url"] = fmt.Sprintf("https://api.github.com/repos/o/r/issues/%d", issue.Parent)
	}
	return
}

func pullJSON(pull *seedPull) (resource map[string]any) {
	return map[string]any{
		"number": pull.Number, "html_url": fmt.Sprintf("https://github.com/o/r/pull/%d", pull.Number),
		"state": "open", "draft": pull.Draft, "node_id": fmt.Sprintf("PR_%d", pull.Number),
	}
}

// page returns the page of items asked by the query, as a JSON array.
func page[T any](r *http.Request, items []T) (out []T) {
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if n < 1 || size < 1 {
		return items
	}
	first := min((n-1)*size, len(items))
	return items[first:min(first+size, len(items))]
}

// field is in[key] as T, the zero T when it is missing or of another type.
func field[T any](in map[string]any, key string) (value T) {
	value, _ = in[key].(T)
	return
}

func (fake *fakeGitHub) mux() (handler http.Handler) {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	notFound := func(w http.ResponseWriter) { reply(w, http.StatusNotFound, map[string]string{"message": "Not Found"}) }
	// issue serves the handlers that work on issue {n}, under the lock.
	issue := func(pattern string, serve func(w http.ResponseWriter, r *http.Request, issue *seedIssue)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			n, _ := strconv.Atoi(r.PathValue("n"))
			found, ok := fake.issues[n]
			if !ok {
				notFound(w)
				return
			}
			serve(w, r, found)
		})
	}
	body := func(r *http.Request) (in map[string]any) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &in)
		return
	}

	issue("GET /repos/o/r/issues/{n}", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		reply(w, http.StatusOK, fake.issueJSON(issue))
	})
	issue("PATCH /repos/o/r/issues/{n}", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		issue.Open = body(r)["state"] != "closed"
		reply(w, http.StatusOK, fake.issueJSON(issue))
	})
	issue("GET /repos/o/r/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		comments := []map[string]string{}
		for _, comment := range page(r, issue.Comments) {
			comments = append(comments, map[string]string{"body": comment})
		}
		reply(w, http.StatusOK, comments)
	})
	issue("POST /repos/o/r/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		issue.Comments = append(issue.Comments, field[string](body(r), "body"))
		reply(w, http.StatusCreated, map[string]string{})
	})
	issue("POST /repos/o/r/issues/{n}/labels", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		for _, label := range field[[]any](body(r), "labels") {
			issue.Labels = append(issue.Labels, fmt.Sprint(label))
		}
		reply(w, http.StatusOK, []string{})
	})
	issue("DELETE /repos/o/r/issues/{n}/labels/{label}", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		i := slices.Index(issue.Labels, r.PathValue("label"))
		if i < 0 {
			reply(w, http.StatusNotFound, map[string]string{"message": "Label does not exist"})
			return
		}
		issue.Labels = slices.Delete(issue.Labels, i, i+1)
		reply(w, http.StatusOK, []string{})
	})
	issue("GET /repos/o/r/issues/{n}/sub_issues", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		subs := []map[string]any{}
		for _, child := range page(r, fake.children(issue.Number)) {
			subs = append(subs, fake.issueJSON(child))
		}
		reply(w, http.StatusOK, subs)
	})
	issue("GET /repos/o/r/issues/{n}/dependencies/blocked_by", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		blockers := []map[string]any{}
		for _, n := range page(r, issue.BlockedBy) {
			blockers = append(blockers, fake.issueJSON(fake.issues[n]))
		}
		reply(w, http.StatusOK, blockers)
	})
	mux.HandleFunc("GET /repos/o/r/labels", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		list := []map[string]string{}
		for _, name := range page(r, fake.labels) {
			list = append(list, map[string]string{"name": name})
		}
		reply(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		query := r.URL.Query()
		if !slices.Contains([]string{"open", "closed", "all"}, query.Get("state")) {
			reply(w, http.StatusBadRequest, map[string]string{"message": "state"})
			return
		}
		// Descending, pull requests included: ListLabeled and List sort and filter.
		var matching []*seedIssue
		for _, issue := range fake.issues {
			if (query.Get("state") == "all" || issue.Open == (query.Get("state") == "open")) &&
				(query.Get("labels") == "" || slices.Contains(issue.Labels, query.Get("labels"))) {
				matching = append(matching, issue)
			}
		}
		slices.SortFunc(matching, func(a, b *seedIssue) int { return b.Number - a.Number })
		list := []map[string]any{}
		for _, issue := range page(r, matching) {
			list = append(list, fake.issueJSON(issue))
		}
		reply(w, http.StatusOK, list)
	})

	mux.HandleFunc("POST /repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		in := body(r)
		issue := &seedIssue{Number: 1, Title: field[string](in, "title"), Body: field[string](in, "body"), Open: true}
		for n := range fake.issues {
			issue.Number = max(issue.Number, n+1)
		}
		for _, label := range field[[]any](in, "labels") {
			issue.Labels = append(issue.Labels, fmt.Sprint(label))
		}
		fake.issues[issue.Number] = issue
		reply(w, http.StatusCreated, fake.issueJSON(issue))
	})
	mux.HandleFunc("POST /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		in := body(r)
		pull := &seedPull{Number: 1, Head: field[string](in, "head"), Base: field[string](in, "base"), Title: field[string](in, "title"), Body: field[string](in, "body")}
		for n := range fake.issues {
			pull.Number = max(pull.Number, n+1)
		}
		pull.Draft, _ = in["draft"].(bool)
		fake.pulls = append(fake.pulls, pull)
		fake.issues[pull.Number] = &seedIssue{Number: pull.Number, Title: pull.Title, Body: pull.Body, Open: true, IsPull: true}
		reply(w, http.StatusCreated, pullJSON(pull))
	})
	mux.HandleFunc("GET /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		pulls := []map[string]any{}
		for _, pull := range slices.Backward(fake.pulls) { // latest first
			if "o:"+pull.Head == r.URL.Query().Get("head") {
				pulls = append(pulls, pullJSON(pull))
			}
		}
		reply(w, http.StatusOK, page(r, pulls))
	})
	mux.HandleFunc("PATCH /repos/o/r/pulls/{n}", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		n, _ := strconv.Atoi(r.PathValue("n"))
		for _, pull := range fake.pulls {
			if pull.Number == n {
				pull.Body = field[string](body(r), "body")
				reply(w, http.StatusOK, pullJSON(pull))
				return
			}
		}
		notFound(w)
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		id := field[map[string]any](body(r), "variables")["id"]
		for _, pull := range fake.pulls {
			if fmt.Sprintf("PR_%d", pull.Number) == id {
				pull.Draft = false
				reply(w, http.StatusOK, map[string]any{"data": map[string]any{}})
				return
			}
		}
		reply(w, http.StatusOK, map[string]any{"errors": []map[string]string{{"message": "Could not resolve to a node"}}})
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-GitHub-Api-Version") == "" {
			reply(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func TestGitHubContract(t *testing.T) {
	testContract(t, newFakeGitHub)
}

func TestGitHubNotFound(t *testing.T) {
	f, _ := newFakeGitHub(t, contractIssues(), contractLabels())
	if _, err := f.Ticket(t.Context(), 99); err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("Ticket(99): err = %v", err)
	}
	if _, err := f.Blocked(t.Context(), Ticket{Number: 7, Body: "Blocked by: #99"}); err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("unknown blocker: err = %v", err)
	}
}

func TestGitHubBadToken(t *testing.T) {
	f, _ := newFakeGitHub(t, contractIssues(), contractLabels())
	f.(*GitHub).Token = "wrong"
	if _, err := f.Ticket(t.Context(), 7); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("wrong token: err = %v", err)
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
	if n, err := g.CreateIssue(t.Context(), "idea", "text", []string{"bug"}); err != nil || n != 42 {
		t.Errorf("CreateIssue = %d, %v", n, err)
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
		`POST /repos/o/r/issues {"body":"text","labels":["bug"],"title":"idea"}`,
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
	if err != nil || !found || pull.Number != 42 || pull.State != "open" || !pull.Draft || pull.ID != "PR_x" {
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

func TestGitHubRemote(t *testing.T) {
	g, err := NewGitHub(config.Forge{Kind: "github", Repo: "o/r"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if g.GitURL != "https://github.com/o/r.git" {
		t.Errorf("GitURL = %q", g.GitURL)
	}
	testRemote(t, "x-access-token", func(gitURL string) Remote {
		g.GitURL = gitURL
		return g.Remote()
	})
}
