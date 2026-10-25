package forge

import (
	"fmt"
	"slices"
	"strings"
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
type newFakeForge func(t *testing.T, issues []seedIssue) (f Forge, state func() fakeState)

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

// testContract is the suite every Forge passes: reads (tickets, lists by
// label, Spec children, blockers) and writes (pull requests, comments,
// labels, closing).
func testContract(t *testing.T, newFake newFakeForge) {
	forge := func(t *testing.T) (f Forge, state func() fakeState) {
		t.Helper()
		return newFake(t, contractIssues())
	}

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
		if err := f.CloseIssue(t.Context(), 7); err != nil {
			t.Error(err)
		}
		got := state().Issues[7]
		if got.Open || !slices.Equal(got.Labels, []string{"bug", "needs-info"}) || got.Comments[len(got.Comments)-1] != "question" || len(got.Comments) != 151 {
			t.Errorf("issue 7 = %+v", got)
		}
		if err := f.Comment(t.Context(), 99, "x"); err == nil {
			t.Error("Comment on a missing issue: want error")
		}
	})
}

func TestNew(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "t")
	f, err := New(t.Context(), config.Forge{Kind: "github", Repo: "o/r"})
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
		{Kind: "gitea", Repo: "o/r", URL: "https://gitea.example"},
		{Kind: "github", Repo: "o/r", URL: "https://ghe.example"},
		{Kind: "github", Repo: "o"},
		{Kind: "github", Repo: "o/r/x"},
	} {
		if _, err = New(t.Context(), cfg); err == nil {
			t.Errorf("New(%+v): want error", cfg)
		}
	}
}
