package forge

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
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
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
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
	for n, want := range map[int]string{8: "pull request", 9: "closed", 10: "sub-issues", 11: "Not Found"} {
		if _, err := g.Ticket(t.Context(), n); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Ticket(%d): err = %v, want %q", n, err, want)
		}
	}
	g.Token = "wrong"
	if _, err := g.Ticket(t.Context(), 7); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("wrong token: err = %v", err)
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
