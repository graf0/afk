package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
)

// fakeGitHub records requests as "METHOD path" with their JSON bodies and
// fails the one named by fail with 422. Every write is answered with the
// pull request 42, unless issues is set: then it also keeps issues, labels,
// sub-issues, blockers and the pull request of Specs (see fakeGitHub.answer).
type fakeGitHub struct {
	requests []string
	bodies   []map[string]any
	fail     string

	issues map[int]*fakeIssue
	pull   map[string]any // nil until created
}

// serve starts the fake API; the returned client pushes to remote.
func (fake *fakeGitHub) serve(t *testing.T, remote string) (g *forge.GitHub) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		request := req.Method + " " + req.URL.EscapedPath()
		var body map[string]any
		data, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(data, &body)
		fake.requests, fake.bodies = append(fake.requests, request), append(fake.bodies, body)
		if request == fake.fail {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = fmt.Fprint(w, `{"message":"Validation Failed"}`)
			return
		}
		if fake.issues != nil {
			if reply, ok := fake.answer(req, body); ok {
				_ = json.NewEncoder(w).Encode(reply)
				return
			}
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"number":42,"html_url":"https://github.com/o/r/pull/42"}`)
	}))
	t.Cleanup(server.Close)
	g, err := forge.NewGitHub(config.Forge{Kind: "github", Repo: "o/r"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	g.APIURL, g.GitURL = server.URL, remote
	return
}

// publishRun is a Run of ticket #1 with an accepted result, publishing to a
// fake GitHub API and to origin.git as the remote.
func publishRun(t *testing.T, fake *fakeGitHub) (r run, root string) {
	t.Helper()
	root, _, base, q := acceptedResult(t)
	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	r = run{
		Options: Options{
			Base:   Base{Branch: "main", Commit: base, Config: cfg},
			Ticket: forge.Ticket{Number: 1, Title: "Do it"},
			Forge:  fake.serve(t, filepath.Join(root, "origin.git")),
			Out:    &bytes.Buffer{},
		},
		branch:     "ticket-1",
		quarantine: q,
	}
	return
}

func TestPublishDone(t *testing.T) {
	for name, tc := range map[string]struct {
		report agent.Report
		human  bool
	}{
		"clean":      {agent.Report{Status: agent.StatusDone, Summary: "Did it, cc @boss. Fixes #12.", Deviations: []string{}}, false},
		"doubts":     {agent.Report{Status: agent.StatusDone, Summary: "Did it.", Doubts: []string{"Is X right?"}}, true},
		"deviations": {agent.Report{Status: agent.StatusDone, Summary: "Did it.", Deviations: []string{"Skipped Y."}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeGitHub{}
			r, root := publishRun(t, fake)
			if err := r.checkRemote(t.Context()); err != nil {
				t.Fatalf("checkRemote before push: %v", err)
			}
			if err := r.publish(t.Context(), result{Report: tc.report}); err != nil {
				t.Fatal(err)
			}
			want := []string{"POST /repos/o/r/pulls", "POST /repos/o/r/issues/42/comments"}
			if tc.human {
				want = append(want, "POST /repos/o/r/issues/42/labels")
			}
			want = append(want, "DELETE /repos/o/r/issues/1/labels/ready-for-agent")
			if !slices.Equal(fake.requests, want) {
				t.Fatalf("requests = %q, want %q", fake.requests, want)
			}
			pull := fake.bodies[0]
			if pull["head"] != "ticket-1" || pull["base"] != "main" || pull["title"] != "Do it" || pull["body"] != "Closes #1\n" {
				t.Errorf("pull request = %v", pull)
			}
			comment, _ := fake.bodies[1]["body"].(string)
			for _, item := range slices.Concat([]string{"Did it"}, tc.report.Deviations, tc.report.Doubts) {
				if !strings.Contains(comment, item) {
					t.Errorf("comment lacks %q:\n%s", item, comment)
				}
			}
			if strings.Contains(comment, "@boss") || strings.Contains(comment, "Fixes #12") {
				t.Errorf("comment mentions @boss or closes #12:\n%s", comment)
			}
			if tc.human && fmt.Sprint(fake.bodies[2]["labels"]) != "[ready-for-human]" {
				t.Errorf("labels = %v", fake.bodies[2])
			}
			if got, want := sh(t, root, "git --git-dir=origin.git rev-parse ticket-1"), sh(t, root, "git --git-dir="+r.quarantine.Dir+" rev-parse ticket-1"); got != want {
				t.Errorf("remote ticket-1 = %s, want %s", got, want)
			}
			if err := r.checkRemote(t.Context()); err == nil || !strings.Contains(err.Error(), "git push origin --delete ticket-1") {
				t.Errorf("checkRemote after push: err = %v, want instructions", err)
			}
		})
	}
}

func TestPublishReview(t *testing.T) {
	for name, tc := range map[string]struct {
		res   result
		human bool
	}{
		"fixed":   {result{Review: &agent.Report{Summary: "Ok.", FindingsFixed: []string{"Nil check."}}}, false},
		"skipped": {result{Review: &agent.Report{Summary: "Ok.", FindingsSkipped: []string{"Naming, cc @boss."}}}, true},
		"fixes":   {result{Review: &agent.Report{Summary: "Ok."}, Fixes: 1}, true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeGitHub{}
			r, _ := publishRun(t, fake)
			tc.res.Report = agent.Report{Status: agent.StatusDone, Summary: "Did it.", Deviations: []string{}}
			if err := r.publish(t.Context(), tc.res); err != nil {
				t.Fatal(err)
			}
			want := []string{"POST /repos/o/r/pulls", "POST /repos/o/r/issues/42/comments", "POST /repos/o/r/issues/42/comments"}
			if tc.human {
				want = append(want, "POST /repos/o/r/issues/42/labels")
			}
			want = append(want, "DELETE /repos/o/r/issues/1/labels/ready-for-agent")
			if !slices.Equal(fake.requests, want) {
				t.Fatalf("requests = %q, want %q", fake.requests, want)
			}
			comment, _ := fake.bodies[2]["body"].(string)
			for _, text := range slices.Concat([]string{"### Code review", "Ok."}, tc.res.Review.FindingsFixed) {
				if !strings.Contains(comment, text) {
					t.Errorf("review comment lacks %q:\n%s", text, comment)
				}
			}
			if strings.Contains(comment, "@boss") {
				t.Errorf("review comment mentions @boss:\n%s", comment)
			}
		})
	}
}

func TestPublishNeedsInfo(t *testing.T) {
	fake := &fakeGitHub{}
	r, root := publishRun(t, fake)
	report := agent.Report{Status: agent.StatusNeedsInfo, Reason: "Unclear.", Questions: []string{"Which API?", "Ask @owner"}}
	if err := r.publish(t.Context(), result{Report: report}); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /repos/o/r/issues/1/comments", "POST /repos/o/r/issues/1/labels", "DELETE /repos/o/r/issues/1/labels/ready-for-agent"}
	if !slices.Equal(fake.requests, want) {
		t.Fatalf("requests = %q, want %q", fake.requests, want)
	}
	comment, _ := fake.bodies[0]["body"].(string)
	for _, text := range []string{"Unclear.", "- Which API?", "@\u200bowner"} {
		if !strings.Contains(comment, text) {
			t.Errorf("comment lacks %q:\n%s", text, comment)
		}
	}
	if fmt.Sprint(fake.bodies[1]["labels"]) != "[needs-info]" {
		t.Errorf("labels = %v", fake.bodies[1])
	}
	if refs := sh(t, root, "git --git-dir=origin.git for-each-ref --format='%(refname)'"); refs != "refs/heads/main" {
		t.Errorf("remote refs = %q, want only main: no push on needs_info", refs)
	}
}

func TestPublishPartialFailure(t *testing.T) {
	fake := &fakeGitHub{fail: "POST /repos/o/r/pulls"}
	r, _ := publishRun(t, fake)
	err := r.publish(t.Context(), result{Report: agent.Report{Status: agent.StatusDone, Summary: "Did it."}})
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{
		"open the pull request:", "Validation Failed",
		"\ndone:\n  - push ticket-1\nleft, to do by hand:\n",
		"\n  - open a pull request ticket-1 → main titled \"Do it\" with the body \"Closes #1\\n\"\n",
		"\n  - comment the agent report (printed above) on the pull request\n",
		"\n  - remove the label ready-for-agent from #1",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v\nwant %q", err, want)
		}
	}
	if len(fake.requests) != 1 {
		t.Errorf("requests = %q, want only the failed one", fake.requests)
	}
	// Running again never pushes a second time.
	if err = r.checkRemote(t.Context()); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("checkRemote: err = %v", err)
	}
}

func TestCommitMessage(t *testing.T) {
	r := run{Options: Options{Ticket: forge.Ticket{Number: 6, Title: "Push, PR i needs_info"}}}
	got := r.commitMessage(agent.Report{Summary: " Pushes \x1b[2Jit, @someone. Closes #3.\n"})
	if want := "Push, PR i needs_info (#6)\n\nPushes [2Jit, @\u200bsomeone. C\u200bloses #3.\n"; got != want {
		t.Errorf("commitMessage = %q, want %q", got, want)
	}
	if got = r.commitMessage(agent.Report{}); got != "Push, PR i needs_info (#6)\n" {
		t.Errorf("without summary: %q", got)
	}
}

func TestReviewMessage(t *testing.T) {
	r := run{Options: Options{Ticket: forge.Ticket{Number: 6, Title: "Push"}}}
	if got, want := r.reviewMessage(agent.Report{Summary: " Fine. "}), "code review: Push\n\nFine.\n"; got != want {
		t.Errorf("reviewMessage = %q, want %q", got, want)
	}
	if got := r.reviewMessage(agent.Report{}); got != "code review: Push\n" {
		t.Errorf("without summary: %q", got)
	}
}

func TestAgentText(t *testing.T) {
	for in, want := range map[string]string{
		"@user, @org/team (@x) mail a@b.pl": "@\u200buser, @\u200borg/team (@\u200bx) mail a@\u200bb.pl",
		"@ alone, @-dash, `@code`":          "@ alone, @-dash, `@\u200bcode`",
		"bidi \u202eevil\u200b@user":        "bidi evil@\u200buser",
		"hidden <!-- Fixes #9 --> text":     "hidden &lt;!-- F\u200bixes #9 --> text",
		"prefixes #3, fixtures #4, fix it":  "prefixes #3, fixtures #4, fix it",
	} {
		if got := agentText(in); got != want {
			t.Errorf("agentText(%q) = %q, want %q", in, got, want)
		}
	}
	for _, keyword := range []string{"close", "closes", "closed", "fix", "fixes", "fixed", "resolve", "resolves", "resolved", "Closes", "FIXES", "Resolved"} {
		for _, ref := range []string{" #12", ": #12", " owner/repo#3", ":owner/re.po#3", " https://github.com/o/r/issues/5"} {
			in := "Text. " + keyword + ref + " more"
			want := "Text. " + keyword[:1] + "\u200b" + keyword[1:] + ref + " more"
			if got := agentText(in); got != want {
				t.Errorf("agentText(%q) = %q, want %q", in, got, want)
			}
		}
	}
	long := agentText(strings.Repeat("ą", maxGitHubText))
	if len(long) > maxGitHubText+100 || !strings.HasSuffix(long, "(obcięte)\n") || !strings.HasPrefix(long, "ąą") {
		t.Errorf("long text: %d bytes, suffix %q", len(long), long[len(long)-20:])
	}
	if strings.ContainsRune(long, '\uFFFD') || !strings.HasSuffix(strings.TrimSuffix(long, "\n\n… (obcięte)\n"), "ą") {
		t.Error("long text cut inside a rune")
	}
}
