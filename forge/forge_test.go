package forge

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/graf0/afk/config"
)

// seedIssue is an issue (or, with IsPull, a pull request) of a fake Forge as
// the contract sees it: what the suite seeds and reads back, whatever the
// Forge's API looks like.
type seedIssue struct {
	Number    int
	Title     string
	Body      string
	Open      bool
	Labels    []string
	Comments  []string
	Parent    int   // the Spec it is a sub-issue of, zero without one
	BlockedBy []int // native dependencies
	IsPull    bool
}

// seedPull is a pull request of a fake Forge; it is an issue as well.
type seedPull struct {
	Number int
	Head   string
	Base   string
	Title  string
	Body   string
	Draft  bool
}

// fakeState is what a fake Forge holds, by issue number.
type fakeState struct {
	Issues map[int]seedIssue
	Pulls  []seedPull
}

// newFake starts a fake Forge of repo o/r holding issues and returns a client
// of it and a snapshot of its state. Every implementation provides one, and
// testContract runs the same suite on it.
type newFakeForge func(t *testing.T, issues []seedIssue, labels []string) (f Forge, state func() fakeState)

func contractIssues() (issues []seedIssue) {
	var comments []string
	for i := range 150 { // more than a page
		comments = append(comments, fmt.Sprintf("comment %d", i))
	}
	return []seedIssue{
		{Number: 7, Title: "Do it", Body: "Body text", Open: true, Labels: []string{"bug", "ready-for-agent"}, Comments: comments},
		{Number: 8, Title: "PR", Open: true, Labels: []string{"ready-for-agent"}, IsPull: true},
		{Number: 9, Title: "Closed", Labels: []string{"ready-for-agent"}},
		{Number: 10, Title: "Spec", Open: true, Labels: []string{"spec"}},
		{Number: 11, Title: "Open child", Open: true, Labels: []string{"ready-for-agent"}, Parent: 10, BlockedBy: []int{20}},
		{Number: 12, Title: "Not ready", Open: true, Labels: []string{"needs-triage"}, BlockedBy: []int{20, 21}},
		{Number: 13, Title: "Closed child", Parent: 10},
		{Number: 14, Title: "Ready", Open: true, Labels: []string{"ready-for-agent"}},
		{Number: 20, Title: "Done blocker"},
		{Number: 21, Title: "Open blocker", Open: true},
	}
}

// contractLabels are the labels the repository has: more than a page.
func contractLabels() (labels []string) {
	for i := range 150 {
		labels = append(labels, fmt.Sprintf("label-%03d", i))
	}
	return append(labels, "ready-for-agent", "spec", "needs-info", "ready-for-human")
}

// testContract is the suite every Forge passes: reads and writes.
func testContract(t *testing.T, newFake newFakeForge) {
	testContractReads(t, newFake)
	testContractWrites(t, newFake)
}

// contractForge starts a fresh fake Forge with the contract's issues and labels.
func contractForge(newFake newFakeForge) (forge func(t *testing.T) (f Forge, state func() fakeState)) {
	return func(t *testing.T) (f Forge, state func() fakeState) {
		t.Helper()
		return newFake(t, contractIssues(), contractLabels())
	}
}

// testContractReads is the reads of the contract: tickets, lists by label,
// labels, Spec children, blockers.
func testContractReads(t *testing.T, newFake newFakeForge) {
	forge := contractForge(newFake)

	t.Run("Ticket", func(t *testing.T) {
		f, _ := forge(t)
		ticket, err := f.Ticket(t.Context(), 7)
		if err != nil {
			t.Fatal(err)
		}
		if ticket.Number != 7 || ticket.Title != "Do it" || ticket.Body != "Body text" || !slices.Equal(ticket.Labels, []string{"bug", "ready-for-agent"}) {
			t.Errorf("ticket = %+v", ticket)
		}
		if len(ticket.Comments) != 150 || ticket.Comments[0] != "comment 0" || ticket.Comments[149] != "comment 149" {
			t.Errorf("got %d comments, want 150 in order", len(ticket.Comments))
		}
		for n, want := range map[int]string{8: "pull request", 9: "closed"} {
			if _, err = f.Ticket(t.Context(), n); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Ticket(%d): err = %v, want %q", n, err, want)
			}
		}
		if _, err = f.Ticket(t.Context(), 99); err == nil {
			t.Error("Ticket(99) of none: want error")
		}
	})

	t.Run("ReadyTicket", func(t *testing.T) {
		f, _ := forge(t)
		ticket, err := f.ReadyTicket(t.Context(), 7, "ready-for-agent")
		if err != nil || ticket.Number != 7 {
			t.Errorf("ReadyTicket(7) = %+v, %v", ticket, err)
		}
		if _, err = f.ReadyTicket(t.Context(), 12, "ready-for-agent"); err == nil || !strings.Contains(err.Error(), `"ready-for-agent"`) {
			t.Errorf("ReadyTicket(12): err = %v, want refusal naming the label", err)
		}
		if _, err = f.ReadyTicket(t.Context(), 7, ""); err == nil {
			t.Error("empty label: want error")
		}
		if _, err = f.ReadyTicket(t.Context(), 7, "go"); err == nil || !strings.Contains(err.Error(), `"go"`) {
			t.Errorf("custom label the ticket lacks: err = %v, want refusal naming it", err)
		}
		if ticket, err = f.ReadyTicket(t.Context(), 7, "bug"); err != nil || ticket.Number != 7 {
			t.Errorf("custom label the ticket has: %+v, %v", ticket, err)
		}
	})

	t.Run("Peek", func(t *testing.T) {
		f, _ := forge(t)
		ticket, open, err := f.Peek(t.Context(), 11)
		if err != nil || !open || ticket.Parent != 10 || ticket.Comments != nil {
			t.Errorf("Peek(11) = %+v, %v, %v; want an open child of #10 without comments", ticket, open, err)
		}
		ticket, open, err = f.Peek(t.Context(), 9)
		if err != nil || open || ticket.Number != 9 {
			t.Errorf("Peek(9) = %+v, %v, %v; want closed #9", ticket, open, err)
		}
		if ticket, _, err = f.Peek(t.Context(), 7); err != nil || ticket.Parent != 0 || !slices.Contains(ticket.Labels, "ready-for-agent") {
			t.Errorf("Peek(7) = %+v, %v", ticket, err)
		}
		if _, _, err = f.Peek(t.Context(), 8); err == nil || !strings.Contains(err.Error(), "pull request") {
			t.Errorf("Peek(8): err = %v, want a pull request refusal", err)
		}
		if _, _, err = f.Peek(t.Context(), 99); err == nil {
			t.Error("Peek(99) of none: want error")
		}
	})

	t.Run("ListLabeled", func(t *testing.T) {
		f, _ := forge(t)
		// Open issues only, no pull requests, ascending.
		numbers, err := f.ListLabeled(t.Context(), "ready-for-agent")
		if err != nil || !slices.Equal(numbers, []int{7, 11, 14}) {
			t.Errorf("ListLabeled = %v, %v; want [7 11 14]", numbers, err)
		}
		if numbers, err = f.ListLabeled(t.Context(), "nothing"); err != nil || len(numbers) != 0 {
			t.Errorf("ListLabeled of none = %v, %v", numbers, err)
		}
	})

	t.Run("List", func(t *testing.T) {
		f, _ := forge(t)
		numbersOf := func(label string, state State) (numbers []int) {
			tickets, err := f.List(t.Context(), label, state)
			if err != nil {
				t.Fatalf("List(%q, %q): %v", label, state, err)
			}
			for _, ticket := range tickets {
				numbers = append(numbers, ticket.Number)
			}
			return
		}
		// No pull requests (#8), ascending.
		for _, tc := range []struct {
			label string
			state State
			want  []int
		}{
			{"ready-for-agent", StateOpen, []int{7, 11, 14}},
			{"ready-for-agent", StateClosed, []int{9}},
			{"ready-for-agent", StateAll, []int{7, 9, 11, 14}},
			{"", StateClosed, []int{9, 13, 20}},
			{"needs-triage", StateOpen, []int{12}},
			{"nothing", StateAll, nil},
		} {
			if got := numbersOf(tc.label, tc.state); !slices.Equal(got, tc.want) {
				t.Errorf("List(%q, %q) = %v, want %v", tc.label, tc.state, got, tc.want)
			}
		}
		if tickets, _ := f.List(t.Context(), "bug", StateOpen); len(tickets) != 1 || tickets[0].Title != "Do it" || !slices.Equal(tickets[0].Labels, []string{"bug", "ready-for-agent"}) {
			t.Errorf("List(bug) = %+v", tickets)
		}
	})

	t.Run("Labels", func(t *testing.T) {
		f, _ := forge(t)
		names, err := f.Labels(t.Context())
		if want := contractLabels(); err != nil || !slices.Equal(slices.Sorted(slices.Values(names)), slices.Sorted(slices.Values(want))) {
			t.Errorf("Labels = %d names, %v; want the %d of the repository", len(names), err, len(want))
		}
	})

	t.Run("Spec", func(t *testing.T) {
		f, _ := forge(t)
		spec, err := f.Ticket(t.Context(), 10)
		if err != nil || spec.SubIssues != 2 {
			t.Errorf("Ticket(10) = %+v, %v; want a Spec of 2 sub-issues", spec, err)
		}
		subs, err := f.SubIssues(t.Context(), 10)
		slices.SortFunc(subs, func(a, b SubIssue) int { return a.Number - b.Number })
		want := []SubIssue{{Number: 11, Open: true, Labels: []string{"ready-for-agent"}}, {Number: 13}}
		if err != nil || !slices.EqualFunc(subs, want, func(a, b SubIssue) bool {
			return a.Number == b.Number && a.Open == b.Open && slices.Equal(a.Labels, b.Labels)
		}) {
			t.Errorf("SubIssues(10) = %+v, %v; want %+v", subs, err, want)
		}
	})

	t.Run("Blocked", func(t *testing.T) {
		f, _ := forge(t)
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
			if blocked, err := f.Blocked(t.Context(), tc.ticket); err != nil || blocked != tc.blocked {
				t.Errorf("%s: Blocked = %v, %v; want %v", name, blocked, err, tc.blocked)
			}
		}
		if _, err := f.Blocked(t.Context(), Ticket{Number: 7, Body: "Blocked by: #99"}); err == nil {
			t.Error("unknown blocker: want error")
		}
	})
}

// testContractWrites is the writes of the contract: pull requests, comments,
// labels, closing.
func testContractWrites(t *testing.T, newFake newFakeForge) {
	forge := contractForge(newFake)

	t.Run("pull requests", func(t *testing.T) {
		f, state := forge(t)
		if _, found, err := f.FindPull(t.Context(), "ticket-7"); err != nil || found {
			t.Errorf("FindPull of none = %v, %v", found, err)
		}
		pull, err := f.CreatePull(t.Context(), "ticket-7", "main", "Do it", "Closes #7\n", true)
		if err != nil || pull.Number == 0 {
			t.Fatalf("CreatePull = %+v, %v", pull, err)
		}
		want := seedPull{Number: pull.Number, Head: "ticket-7", Base: "main", Title: "Do it", Body: "Closes #7\n", Draft: true}
		if got := state().Pulls; !slices.Equal(got, []seedPull{want}) {
			t.Errorf("pulls = %+v, want %+v", got, want)
		}
		found, ok, err := f.FindPull(t.Context(), "ticket-7")
		if err != nil || !ok || found.Number != pull.Number || !found.Draft || found.State != "open" {
			t.Errorf("FindPull = %+v, %v, %v", found, ok, err)
		}
		if _, ok, err = f.FindPull(t.Context(), "ticket-8"); err != nil || ok {
			t.Errorf("FindPull of another branch = %v, %v", ok, err)
		}

		if err = f.UpdatePullBody(t.Context(), pull.Number, "new body"); err != nil {
			t.Error(err)
		}
		if err = f.ReadyPull(t.Context(), found); err != nil {
			t.Error(err)
		}
		want.Body, want.Draft = "new body", false
		if got := state().Pulls; !slices.Equal(got, []seedPull{want}) {
			t.Errorf("pulls = %+v, want %+v", got, want)
		}
		if found, _, _ = f.FindPull(t.Context(), "ticket-7"); found.Draft {
			t.Error("FindPull after ReadyPull: still a draft")
		}

		// A pull request takes comments and labels like an issue.
		if err = f.Comment(t.Context(), pull.Number, "report"); err != nil {
			t.Error(err)
		}
		if err = f.AddLabel(t.Context(), pull.Number, "ready-for-human"); err != nil {
			t.Error(err)
		}
		if got := state().Issues[pull.Number]; !slices.Equal(got.Comments, []string{"report"}) || !slices.Equal(got.Labels, []string{"ready-for-human"}) {
			t.Errorf("pull request as issue = %+v", got)
		}
	})

	t.Run("issues", func(t *testing.T) {
		f, state := forge(t)
		if err := f.Comment(t.Context(), 7, "question"); err != nil {
			t.Error(err)
		}
		if err := f.AddLabel(t.Context(), 7, "needs-info"); err != nil {
			t.Error(err)
		}
		if err := f.RemoveLabel(t.Context(), 7, "ready-for-agent"); err != nil {
			t.Error(err)
		}
		if err := f.RemoveLabel(t.Context(), 14, "needs-info"); err != nil { // not on the issue: fine
			t.Errorf("RemoveLabel of a missing label: %v", err)
		}
		title, text := "New title", "New body"
		if err := f.EditIssue(t.Context(), 7, &title, nil); err != nil {
			t.Error(err)
		}
		if err := f.EditIssue(t.Context(), 7, nil, &text); err != nil {
			t.Error(err)
		}
		if err := f.CloseIssue(t.Context(), 7); err != nil {
			t.Error(err)
		}
		got := state().Issues[7]
		if got.Title != title || got.Body != text {
			t.Errorf("issue 7 after EditIssue: title %q, body %q", got.Title, got.Body)
		}
		if got.Open || !slices.Equal(got.Labels, []string{"bug", "needs-info"}) || got.Comments[len(got.Comments)-1] != "question" || len(got.Comments) != 151 {
			t.Errorf("issue 7 = %+v", got)
		}
		if err := f.Comment(t.Context(), 99, "x"); err == nil {
			t.Error("Comment on a missing issue: want error")
		}
	})

	t.Run("create issue", func(t *testing.T) {
		f, state := forge(t)
		n, err := f.CreateIssue(t.Context(), "Idea", "Body", []string{"spec", "needs-info"})
		if err != nil || n <= 14 {
			t.Fatalf("CreateIssue = %d, %v", n, err)
		}
		want := seedIssue{Number: n, Title: "Idea", Body: "Body", Open: true, Labels: []string{"spec", "needs-info"}}
		if got := state().Issues[n]; !reflect.DeepEqual(got, want) {
			t.Errorf("issue = %+v, want %+v", got, want)
		}
		if _, err = f.CreateIssue(t.Context(), "No label", "", nil); err != nil {
			t.Errorf("CreateIssue without labels: %v", err)
		}
	})
}

func TestHasLabel(t *testing.T) {
	ticket := Ticket{Labels: []string{"Ready-For-Agent", "bug"}}
	sub := SubIssue{Labels: []string{"Spec"}}
	if !ticket.HasLabel("ready-for-agent") || !ticket.HasLabel("bug") || ticket.HasLabel("spec") || ticket.HasLabel("") {
		t.Errorf("Ticket.HasLabel on %v", ticket.Labels)
	}
	if !sub.HasLabel("spec") || sub.HasLabel("bug") {
		t.Errorf("SubIssue.HasLabel on %v", sub.Labels)
	}
}

// cfgOf is the config of a Project on forge, with the spec label "spec".
func cfgOf(forge config.Forge) (cfg config.Config) {
	cfg.Forge, cfg.Labels.Spec = forge, "spec"
	return
}

func TestNew(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "t")
	t.Setenv("GITEA_TOKEN", "")
	stateDir := t.TempDir()
	f, err := New(t.Context(), cfgOf(config.Forge{Kind: "github", Repo: "o/r"}), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.(*GitHub); !ok {
		t.Errorf("New(github) = %T", f)
	}
	if got := f.Remote().Repo; got != "o/r" {
		t.Errorf("Remote().Repo = %q", got)
	}
	for _, cfg := range []config.Forge{
		{Kind: "gitlab", Repo: "o/r", URL: "https://gitlab.example"},
		{Kind: "github", Repo: "o/r", URL: "https://ghe.example"},
		{Kind: "github", Repo: "o"},
		{Kind: "github", Repo: "o/r/x"},
		{Kind: "gitea", Repo: "o/r", URL: "https://gitea.example"}, // no token
	} {
		if _, err = New(t.Context(), cfgOf(cfg), stateDir); err == nil {
			t.Errorf("New(%+v): want error", cfg)
		}
	}

	t.Setenv("GITEA_TOKEN", "t")
	f, err = New(t.Context(), cfgOf(config.Forge{Kind: "gitea", Repo: "o/r", URL: "https://gitea.example"}), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if g, ok := f.(*Gitea); !ok || g.SpecLabel != "spec" || g.APIURL != "https://gitea.example/api/v1" {
		t.Errorf("New(gitea) = %#v", f)
	}
}

// testRemote checks, for a Forge whose Remote is remoteOf(gitURL) with the token
// of user:secret, that git sends the token in a header to GitURL
// only, that it appears neither on argv nor in git's error, and that the
// user's global config (url.insteadOf) and redirects can't send it elsewhere.
func testRemote(t *testing.T, user string, remoteOf func(gitURL string) Remote) {
	t.Helper()
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

	for _, url := range []string{server.URL + "/o/r.git", server.URL + "/x/r.git", server.URL + "/moved/r.git"} {
		gitURL := server.URL + "/o/r.git"
		if strings.HasPrefix(url, server.URL+"/moved/") {
			gitURL = url
		}
		remote := remoteOf(gitURL)
		out, args := lsRemote(url, remote.Env)
		if strings.Contains(out, "secret") || strings.Contains(strings.Join(args, " "), "secret") {
			t.Errorf("token leaked: %s %s", args, out)
		}
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":secret"))
	want := []string{"/o/r.git/info/refs " + basic, "/x/r.git/info/refs ", "/moved/r.git/info/refs " + basic}
	if got := requests(); !slices.Equal(got, want) {
		t.Errorf("requests with Authorization = %q, want %q", got, want)
	}
}

func TestParseState(t *testing.T) {
	for _, s := range []string{"open", "closed", "all"} {
		if state, err := ParseState(s); err != nil || string(state) != s {
			t.Errorf("ParseState(%q) = %q, %v", s, state, err)
		}
	}
	for _, s := range []string{"", "bad", "Open"} {
		if _, err := ParseState(s); err == nil {
			t.Errorf("ParseState(%q): want an error", s)
		}
	}
}
