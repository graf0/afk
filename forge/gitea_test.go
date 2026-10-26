package forge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/graf0/afk/config"
)

// fakeGitea is a stateful fake Gitea API of o/r, token "secret": the Gitea
// half of the contract. Like Gitea it has no sub-issues: a seeded Parent
// becomes a task list in the body of the Spec, which gets the spec label.
type fakeGitea struct {
	mu     sync.Mutex
	issues map[int]*seedIssue
	labels []string // the id of a label is its index plus one
	pulls  []*seedPull

	depsDisabled bool // Gitea answers 404 to the dependencies of every issue
	ignoreLabels bool // the list ignores labels=, as Gitea does for a name it does not know
}

// newFakeGitea is the newFake of Gitea.
func newFakeGitea(t *testing.T, issues []seedIssue, labels []string) (f Forge, state func() fakeState) {
	t.Helper()
	g, fake := startFakeGitea(t, issues, labels)
	return g, fake.snapshot
}

func startFakeGitea(t *testing.T, issues []seedIssue, labels []string) (g *Gitea, fake *fakeGitea) {
	t.Helper()
	fake = &fakeGitea{issues: map[int]*seedIssue{}, labels: labels}
	for _, issue := range issues {
		fake.issues[issue.Number] = &issue
	}
	for _, issue := range issues {
		if parent := fake.issues[issue.Parent]; parent != nil {
			parent.Body += fmt.Sprintf("\n- [ ] #%d %s", issue.Number, issue.Title)
			if !slices.Contains(parent.Labels, "spec") {
				parent.Labels = append(parent.Labels, "spec")
			}
		}
	}
	server := httptest.NewServer(fake.mux())
	t.Cleanup(server.Close)
	g, err := NewGitea(config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://gitea.example"}, "secret", "spec")
	if err != nil {
		t.Fatal(err)
	}
	g.APIURL = server.URL + "/api/v1"
	return
}

func (fake *fakeGitea) snapshot() (state fakeState) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	state.Issues = map[int]seedIssue{}
	for n, issue := range fake.issues {
		state.Issues[n] = *issue
	}
	for _, pull := range fake.pulls {
		// Gitea's draft is the prefix of the title.
		title, draft := splitWIP(pull.Title)
		state.Pulls = append(state.Pulls, seedPull{Number: pull.Number, Head: pull.Head, Base: pull.Base, Title: title, Body: pull.Body, Draft: draft})
	}
	return
}

// issueJSON is the issue resource as Gitea serves it: pull_request is null
// for an issue.
func issueJSON(issue *seedIssue) (resource map[string]any) {
	labels := []map[string]string{}
	for _, label := range issue.Labels {
		labels = append(labels, map[string]string{"name": label})
	}
	resource = map[string]any{
		"number": issue.Number, "title": issue.Title, "body": issue.Body,
		"state": "closed", "labels": labels, "pull_request": nil,
	}
	if issue.Open {
		resource["state"] = "open"
	}
	if issue.IsPull {
		resource["pull_request"] = map[string]any{"merged": false}
	}
	return
}

// giteaPage returns the page of items asked by the query: page and limit,
// the limit at most 50.
func giteaPage[T any](r *http.Request, items []T) (out []T) {
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	n, size = max(n, 1), min(max(size, 1), giteaPageSize)
	first := min((n-1)*size, len(items))
	return items[first:min(first+size, len(items))]
}

func (fake *fakeGitea) mux() (handler http.Handler) {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	notFound := func(w http.ResponseWriter) {
		reply(w, http.StatusNotFound, map[string]string{"message": "The target couldn't be found."})
	}
	// handle serves pattern under the lock, for the token "secret" only.
	handle := func(pattern string, serve func(w http.ResponseWriter, r *http.Request)) {
		method, path, _ := strings.Cut(pattern, " ")
		mux.HandleFunc(method+" /api/v1/repos/o/r/"+path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "token secret" {
				reply(w, http.StatusUnauthorized, map[string]string{"message": "token is required"})
				return
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			serve(w, r)
		})
	}
	issue := func(pattern string, serve func(w http.ResponseWriter, r *http.Request, issue *seedIssue)) {
		handle(pattern, func(w http.ResponseWriter, r *http.Request) {
			n, _ := strconv.Atoi(r.PathValue("n"))
			found, ok := fake.issues[n]
			if !ok {
				notFound(w)
				return
			}
			serve(w, r, found)
		})
	}

	issue("GET issues/{n}", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		reply(w, http.StatusOK, issueJSON(issue))
	})
	issue("GET issues/{n}/comments", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		comments := []map[string]any{}
		for i, body := range issue.Comments {
			comments = append(comments, map[string]any{"id": issue.Number*1000 + i, "body": body})
		}
		reply(w, http.StatusOK, giteaPage(r, comments))
	})
	issue("GET issues/{n}/dependencies", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		if fake.depsDisabled {
			notFound(w)
			return
		}
		blockers := []map[string]any{}
		for _, n := range issue.BlockedBy {
			blockers = append(blockers, issueJSON(fake.issues[n]))
		}
		reply(w, http.StatusOK, giteaPage(r, blockers))
	})
	handle("GET issues", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		var listed []*seedIssue
		for _, issue := range fake.issues {
			ok := (query.Get("state") != "open" || issue.Open) &&
				(query.Get("type") != "issues" || !issue.IsPull) &&
				(fake.ignoreLabels || query.Get("labels") == "" || slices.Contains(issue.Labels, query.Get("labels")))
			if ok {
				listed = append(listed, issue)
			}
		}
		slices.SortFunc(listed, func(a, b *seedIssue) int { return b.Number - a.Number }) // Gitea: newest first
		resources := []map[string]any{}
		for _, issue := range giteaPage(r, listed) {
			resources = append(resources, issueJSON(issue))
		}
		reply(w, http.StatusOK, resources)
	})
	handle("GET labels", func(w http.ResponseWriter, r *http.Request) {
		resources := []map[string]any{}
		for i, name := range fake.labels {
			resources = append(resources, map[string]any{"id": i + 1, "name": name})
		}
		reply(w, http.StatusOK, giteaPage(r, resources))
	})
	body := func(r *http.Request) (in map[string]any) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &in)
		return
	}
	issue("PATCH issues/{n}", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		issue.Open = body(r)["state"] != "closed"
		reply(w, http.StatusCreated, issueJSON(issue))
	})
	issue("POST issues/{n}/comments", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		issue.Comments = append(issue.Comments, fmt.Sprint(body(r)["body"]))
		reply(w, http.StatusCreated, map[string]any{})
	})
	issue("POST issues/{n}/labels", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		ids, _ := body(r)["labels"].([]any)
		for _, id := range ids {
			i, ok := id.(float64) // Gitea takes ids as numbers
			if !ok || i < 1 || int(i) > len(fake.labels) {
				reply(w, http.StatusUnprocessableEntity, map[string]string{"message": "label does not exist"})
				return
			}
			issue.Labels = append(issue.Labels, fake.labels[int(i)-1])
		}
		reply(w, http.StatusOK, []string{})
	})
	issue("DELETE issues/{n}/labels/{id}", func(w http.ResponseWriter, r *http.Request, issue *seedIssue) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		i := -1
		if id >= 1 && id <= len(fake.labels) {
			i = slices.Index(issue.Labels, fake.labels[id-1])
		}
		if id < 1 || id > len(fake.labels) {
			reply(w, http.StatusUnprocessableEntity, map[string]string{"message": "label does not exist"})
			return
		}
		if i >= 0 { // a label the issue doesn't have is 204 too
			issue.Labels = slices.Delete(issue.Labels, i, i+1)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	pullJSON := func(pull *seedPull) map[string]any {
		upper := strings.ToUpper(pull.Title) // Gitea's IsWorkInProgress, default prefixes
		return map[string]any{
			"number": pull.Number, "html_url": fmt.Sprintf("https://gitea.example/o/r/pulls/%d", pull.Number),
			"state": "open", "title": pull.Title, "draft": strings.HasPrefix(upper, "WIP:") || strings.HasPrefix(upper, "[WIP]"),
			"head": map[string]any{"ref": pull.Head, "repo": map[string]string{"full_name": "o/r"}},
		}
	}
	handle("POST pulls", func(w http.ResponseWriter, r *http.Request) {
		in := body(r)
		pull := &seedPull{Number: 1, Head: fmt.Sprint(in["head"]), Base: fmt.Sprint(in["base"]), Title: fmt.Sprint(in["title"]), Body: fmt.Sprint(in["body"])}
		for n := range fake.issues {
			pull.Number = max(pull.Number, n+1)
		}
		fake.pulls = append(fake.pulls, pull)
		fake.issues[pull.Number] = &seedIssue{Number: pull.Number, Title: pull.Title, Body: pull.Body, Open: true, IsPull: true}
		reply(w, http.StatusCreated, pullJSON(pull))
	})
	handle("GET pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "all" {
			reply(w, http.StatusBadRequest, map[string]string{"message": "state"})
			return
		}
		pulls := []map[string]any{}
		for _, pull := range fake.pulls { // oldest first: the client picks the latest
			pulls = append(pulls, pullJSON(pull))
		}
		reply(w, http.StatusOK, giteaPage(r, pulls))
	})
	pullOf := func(r *http.Request) *seedPull {
		n, _ := strconv.Atoi(r.PathValue("n"))
		for _, pull := range fake.pulls {
			if pull.Number == n {
				return pull
			}
		}
		return nil
	}
	handle("GET pulls/{n}", func(w http.ResponseWriter, r *http.Request) {
		if pull := pullOf(r); pull != nil {
			reply(w, http.StatusOK, pullJSON(pull))
			return
		}
		notFound(w)
	})
	handle("PATCH pulls/{n}", func(w http.ResponseWriter, r *http.Request) {
		pull := pullOf(r)
		if pull == nil {
			notFound(w)
			return
		}
		in := body(r)
		if title, ok := in["title"].(string); ok {
			pull.Title = title
		}
		if text, ok := in["body"].(string); ok {
			pull.Body = text
		}
		reply(w, http.StatusCreated, pullJSON(pull))
	})
	return mux
}

func TestGiteaContract(t *testing.T) {
	testContract(t, newFakeGitea)
}

func TestGiteaTaskListNeedsSpecLabel(t *testing.T) {
	g, _ := startFakeGitea(t, []seedIssue{
		{Number: 1, Title: "Not a Spec", Open: true, Body: "- [ ] #2\n- [ ] #3"},
		{Number: 2, Title: "Two", Open: true},
		{Number: 3, Title: "Three", Open: true},
	}, nil)
	ticket, err := g.Ticket(t.Context(), 1)
	if err != nil || ticket.SubIssues != 0 {
		t.Errorf("Ticket(1) = %+v, %v; want no sub-issues", ticket, err)
	}
	if subs, err := g.SubIssues(t.Context(), 1); err != nil || len(subs) != 0 {
		t.Errorf("SubIssues(1) = %+v, %v; want none", subs, err)
	}
	if ticket, _, err = g.Peek(t.Context(), 2); err != nil || ticket.Parent != 0 {
		t.Errorf("Peek(2) = %+v, %v; want no Spec", ticket, err)
	}
}

func TestGiteaTaskList(t *testing.T) {
	body := `Intro #9

- [x] #2 done
  - [ ] #3 nested, indented
* [X] #2 again
+ [ ] #4: colon
- [ ] plain item, not an issue
- [ ] #5abc not a reference
- #6 not a task
`
	g, _ := startFakeGitea(t, []seedIssue{
		{Number: 1, Title: "Spec", Open: true, Labels: []string{"spec"}, Body: body},
		{Number: 2, Title: "Two"},
		{Number: 3, Title: "Three", Open: true, Labels: []string{"x"}},
		{Number: 4, Title: "Four", Open: true},
	}, nil)
	ticket, err := g.Ticket(t.Context(), 1)
	if err != nil || ticket.SubIssues != 3 {
		t.Errorf("Ticket(1) = %+v, %v; want 3 sub-issues", ticket, err)
	}
	subs, err := g.SubIssues(t.Context(), 1)
	want := []SubIssue{{Number: 2}, {Number: 3, Open: true, Labels: []string{"x"}}, {Number: 4, Open: true}}
	if err != nil || !slices.EqualFunc(subs, want, func(a, b SubIssue) bool {
		return a.Number == b.Number && a.Open == b.Open && slices.Equal(a.Labels, b.Labels)
	}) {
		t.Errorf("SubIssues(1) = %+v, %v; want %+v (state from the issue, not the checkbox)", subs, err, want)
	}
	if ticket, _, err = g.Peek(t.Context(), 3); err != nil || ticket.Parent != 1 {
		t.Errorf("Peek(3) = %+v, %v; want Spec #1", ticket, err)
	}
}

func TestGiteaParent(t *testing.T) {
	g, _ := startFakeGitea(t, []seedIssue{
		{Number: 1, Title: "Closed Spec", Labels: []string{"spec"}, Body: "- [ ] #5"},
		{Number: 2, Title: "Spec B", Open: true, Labels: []string{"spec"}, Body: "- [ ] #5"},
		{Number: 3, Title: "Spec A", Open: true, Labels: []string{"spec"}, Body: "- [ ] #5\n- [ ] #4"},
		{Number: 4, Title: "Nested Spec", Open: true, Labels: []string{"spec"}, Body: "- [ ] #6"},
		{Number: 5, Title: "Child", Open: true},
		{Number: 6, Title: "Grandchild", Open: true},
		{Number: 7, Title: "Broken Spec", Open: true, Labels: []string{"spec"}, Body: "- [ ] o/r#5"},
	}, nil)
	for n, want := range map[int]int{5: 2, 4: 3, 6: 4, 3: 0} {
		if ticket, _, err := g.Peek(t.Context(), n); err != nil || ticket.Parent != want {
			t.Errorf("Peek(%d) = %+v, %v; want Spec #%d (the lowest open one, not itself)", n, ticket, err, want)
		}
	}
}

func TestGiteaBadTaskList(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"other repository": {"- [ ] owner/repo#5", `"owner/repo#5"`},
		"URL":              {"- [ ] https://gitea.example/o/r/issues/5 title", "https://gitea.example/o/r/issues/5"},
		"itself":           {"- [ ] #1", "itself"},
		"pull request":     {"- [ ] #8", "pull request"},
		"missing":          {"- [ ] #99", "#99"},
	} {
		g, _ := startFakeGitea(t, []seedIssue{
			{Number: 1, Title: "Spec", Open: true, Labels: []string{"spec"}, Body: tc.body},
			{Number: 8, Title: "PR", Open: true, IsPull: true},
		}, nil)
		_, err := g.SubIssues(t.Context(), 1)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: SubIssues: err = %v, want %q", name, err, tc.want)
		}
		if name == "pull request" || name == "missing" {
			continue // Ticket counts the list, SubIssues reads it
		}
		if _, err = g.Ticket(t.Context(), 1); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Ticket: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestGiteaBlockedDependenciesDisabled(t *testing.T) {
	g, fake := startFakeGitea(t, contractIssues(), contractLabels())
	fake.depsDisabled = true
	for name, tc := range map[string]struct {
		ticket  Ticket
		blocked bool
	}{
		"native ignored": {Ticket{Number: 12}, false},
		"line, open":     {Ticket{Number: 12, Body: "Blocked by: #20, #21"}, true},
		"line, closed":   {Ticket{Number: 12, Body: "Blocked by: #20"}, false},
	} {
		if blocked, err := g.Blocked(t.Context(), tc.ticket); err != nil || blocked != tc.blocked {
			t.Errorf("%s: Blocked = %v, %v; want %v", name, blocked, err, tc.blocked)
		}
	}
	// 404 of an issue that does not exist is no "disabled".
	if _, err := g.Blocked(t.Context(), Ticket{Number: 99}); err == nil || !strings.Contains(err.Error(), "couldn't be found") {
		t.Errorf("Blocked of a missing issue: err = %v", err)
	}
}

func TestGiteaBlockedUnion(t *testing.T) {
	g, _ := startFakeGitea(t, contractIssues(), contractLabels())
	// #11 has a closed native blocker (#20) and an open one in the line.
	if blocked, err := g.Blocked(t.Context(), Ticket{Number: 11, Body: "Blocked by: #21"}); err != nil || !blocked {
		t.Errorf("Blocked = %v, %v; want blocked by the line", blocked, err)
	}
	// #12 has an open native blocker and a closed one in the line.
	if blocked, err := g.Blocked(t.Context(), Ticket{Number: 12, Body: "Blocked by: #20"}); err != nil || !blocked {
		t.Errorf("Blocked = %v, %v; want blocked natively", blocked, err)
	}
}

func TestGiteaCommentsWithoutPaging(t *testing.T) {
	// A server that ignores page and limit answers every page with all of
	// the comments: the second page brings nothing new.
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if strings.HasSuffix(r.URL.Path, "/comments") {
			var comments []map[string]any
			for i := range 60 {
				comments = append(comments, map[string]any{"id": i + 1, "body": strconv.Itoa(i)})
			}
			_ = json.NewEncoder(w).Encode(comments)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/issues") { // Specs of the Parent lookup
			_, _ = w.Write([]byte("[]"))
			return
		}
		_, _ = w.Write([]byte(`{"number":1,"state":"open","pull_request":null}`))
	}))
	t.Cleanup(server.Close)
	g := &Gitea{APIURL: server.URL, Repo: "o/r", Token: "t", SpecLabel: "spec"}
	ticket, err := g.Ticket(t.Context(), 1)
	if err != nil || len(ticket.Comments) != 60 {
		t.Errorf("Ticket = %d comments, %v; want 60", len(ticket.Comments), err)
	}
	if requests > 6 {
		t.Errorf("%d requests: paging did not stop", requests)
	}
}

func TestGiteaBadToken(t *testing.T) {
	g, _ := startFakeGitea(t, contractIssues(), contractLabels())
	g.Token = "wrong-secret"
	_, err := g.Ticket(t.Context(), 7)
	if err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Errorf("wrong token: err = %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "wrong-secret") {
		t.Errorf("the token is in the error: %v", err)
	}
}

func TestGiteaPulls(t *testing.T) {
	g, fake := startFakeGitea(t, contractIssues(), contractLabels())
	ctx := t.Context()
	draft, err := g.CreatePull(ctx, "ticket-7", "main", "Do it", "b", true)
	if err != nil || !draft.Draft {
		t.Fatalf("CreatePull = %+v, %v", draft, err)
	}
	if got := fake.pulls[0].Title; got != "WIP: Do it" {
		t.Errorf("draft title = %q", got)
	}
	// An edited title and the other prefix survive the move to ready.
	fake.pulls[0].Title = "[wip] Better title"
	if err = g.ReadyPull(ctx, draft); err != nil || fake.pulls[0].Title != "Better title" {
		t.Errorf("ReadyPull: title %q, err %v", fake.pulls[0].Title, err)
	}
	if err = g.ReadyPull(ctx, draft); err != nil || fake.pulls[0].Title != "Better title" { // not a draft any more
		t.Errorf("ReadyPull of a ready pull: title %q, err %v", fake.pulls[0].Title, err)
	}
	// The latest pull request of the branch wins.
	if _, err = g.CreatePull(ctx, "ticket-7", "main", "Again", "b", false); err != nil {
		t.Fatal(err)
	}
	if found, ok, err := g.FindPull(ctx, "ticket-7"); err != nil || !ok || found.Draft || found.Number != fake.pulls[1].Number {
		t.Errorf("FindPull = %+v, %v, %v", found, ok, err)
	}
}

func TestGiteaLabels(t *testing.T) {
	g, fake := startFakeGitea(t, contractIssues(), contractLabels())
	ctx := t.Context()
	if err := g.AddLabel(ctx, 7, "no-such-label"); err == nil || !strings.Contains(err.Error(), "no such label") {
		t.Errorf("AddLabel of an unknown label: err = %v", err)
	}
	if err := g.RemoveLabel(ctx, 7, "no-such-label"); err != nil {
		t.Errorf("RemoveLabel of an unknown label: %v", err)
	}
	if err := g.RemoveLabel(ctx, 7, "spec"); err != nil { // not on the issue: Gitea answers 204
		t.Errorf("RemoveLabel of a label the issue doesn't have: %v", err)
	}
	if err := g.RemoveLabel(ctx, 999, "ready-for-agent"); err == nil { // 404 is the issue, not the label
		t.Error("RemoveLabel on a missing issue: no error")
	}
	if err := g.AddLabel(ctx, 7, "Spec"); err != nil { // ignoring case, as Gitea
		t.Error(err)
	}
	if got := fake.issues[7].Labels; !slices.Equal(got, []string{"bug", "ready-for-agent", "spec"}) {
		t.Errorf("labels = %v", got)
	}
}

func TestGiteaRemote(t *testing.T) {
	g, err := NewGitea(config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://gitea.example/"}, "secret", "spec")
	if err != nil {
		t.Fatal(err)
	}
	if g.GitURL != "https://gitea.example/o/r.git" {
		t.Errorf("GitURL = %q", g.GitURL)
	}
	testRemote(t, "afk", func(gitURL string) Remote {
		g.GitURL = gitURL
		return g.Remote()
	})
}

func TestGiteaLabelFilterOnTheClient(t *testing.T) {
	g, fake := startFakeGitea(t, contractIssues(), contractLabels())
	fake.ignoreLabels = true
	numbers, err := g.ListLabeled(t.Context(), "ready-for-agent")
	if err != nil || !slices.Equal(numbers, []int{7, 11, 14}) {
		t.Errorf("ListLabeled = %v, %v; want [7 11 14]", numbers, err)
	}
	if ticket, _, err := g.Peek(t.Context(), 7); err != nil || ticket.Parent != 0 {
		t.Errorf("Peek(7) = %+v, %v; want no Spec (every open issue was listed as one)", ticket, err)
	}
	if ticket, _, err := g.Peek(t.Context(), 11); err != nil || ticket.Parent != 10 {
		t.Errorf("Peek(11) = %+v, %v; want Spec #10", ticket, err)
	}
}

func TestGiteaNoRedirects(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
	}))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(server.Close)
	g, err := NewGitea(config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://gitea.example"}, "secret", "spec")
	if err != nil {
		t.Fatal(err)
	}
	g.APIURL = server.URL + "/api/v1"
	_, err = g.Ticket(t.Context(), 1)
	if err == nil || !strings.Contains(err.Error(), "301") || !strings.Contains(err.Error(), "forge.url") {
		t.Errorf("err = %v, want the redirect refused naming forge.url", err)
	}
	if followed {
		t.Error("the redirect was followed")
	}
}

func TestNewGitea(t *testing.T) {
	g, err := NewGitea(config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://gitea.example/prefix/"}, "t", "spec")
	if err != nil || g.APIURL != "https://gitea.example/prefix/api/v1" || g.Repo != "o/r" {
		t.Errorf("NewGitea = %+v, %v", g, err)
	}
	for _, cfg := range []config.Forge{
		{Repo: "o/r"},
		{Repo: "o/r", URL: "http://gitea.example"},
		{Repo: "o/r", URL: "gitea.example"},
		{Repo: "o", URL: "https://gitea.example"},
		{Repo: "o/r/x", URL: "https://gitea.example"},
	} {
		if _, err = NewGitea(cfg, "t", "spec"); err == nil {
			t.Errorf("NewGitea(%+v): want error", cfg)
		}
	}
}

func TestGiteaToken(t *testing.T) {
	write := func(t *testing.T, dir, content string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(dir, giteaTokenFile)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("env", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "from-file\n", 0o600)
		t.Setenv(giteaTokenEnv, " from-env\n")
		if token, err := GiteaToken(dir); err != nil || token != "from-env" {
			t.Errorf("GiteaToken = %q, %v; want the env over the file", token, err)
		}
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "from-file\n", 0o600)
		t.Setenv(giteaTokenEnv, "")
		if token, err := GiteaToken(dir); err != nil || token != "from-file" {
			t.Errorf("GiteaToken = %q, %v", token, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(giteaTokenEnv, "")
		_, err := GiteaToken(dir)
		if err == nil || !strings.Contains(err.Error(), giteaTokenEnv) || !strings.Contains(err.Error(), filepath.Join(dir, giteaTokenFile)) {
			t.Errorf("err = %v, want both ways named", err)
		}
	})
	t.Run("bad mode", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660} {
			dir := t.TempDir()
			write(t, dir, "secret", mode)
			t.Setenv(giteaTokenEnv, "")
			token, err := GiteaToken(dir)
			if err == nil || !strings.Contains(err.Error(), "0600") || token != "" {
				t.Errorf("mode %v: %q, %v", mode, token, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Errorf("the token is in the error: %v", err)
			}
		}
	})
	t.Run("empty and line break", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, " \n", 0o600)
		t.Setenv(giteaTokenEnv, "")
		if _, err := GiteaToken(dir); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("empty file: err = %v", err)
		}
		write(t, dir, "a\nb", 0o600)
		if _, err := GiteaToken(dir); err == nil || !strings.Contains(err.Error(), "line break") {
			t.Errorf("two lines: err = %v", err)
		}
	})
}
