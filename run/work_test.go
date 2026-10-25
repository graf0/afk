package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
)

// fakeGuest runs commands in a local clone and plays the Agent's sessions
// from a script, one function per session, in order.
type fakeGuest struct {
	t        *testing.T
	dir      string
	sessions []func() agent.Report
	prompts  []string
	models   []string
}

func (g *fakeGuest) Exec(ctx context.Context, command string, stdout io.Writer) (err error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir, cmd.Stdout = g.dir, stdout
	err = cmd.Run()
	return
}

func (g *fakeGuest) Agent(ctx context.Context, prompt, model string) (report agent.Report, err error) {
	if len(g.sessions) == 0 {
		g.t.Fatalf("unexpected agent session with the prompt:\n%s", prompt)
	}
	g.prompts, g.models = append(g.prompts, prompt), append(g.models, model)
	report = g.sessions[0]()
	g.sessions = g.sessions[1:]
	return
}

// commit makes a session that commits a file in the clone and reports done.
func (g *fakeGuest) commit(name string, report agent.Report) (session func() agent.Report) {
	return func() agent.Report {
		sh(g.t, g.dir, "echo "+name+" > "+name+" && git add "+name+" && git commit -qm "+name)
		report.Status = agent.StatusDone
		return report
	}
}

// workRun is a Run of ticket #1 whose guest is a clone of origin.git, with
// a gate of cmd and maxFixes, publishing to fake.
func workRun(t *testing.T, fake *fakeGitHub, gateCmd string, maxFixes int) (r run, g *fakeGuest, root string) {
	t.Helper()
	root, user, base := userRepo(t)
	q, err := OpenQuarantine(t.Context(), filepath.Join(t.TempDir(), "quarantine.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = q.FetchBase(t.Context(), user, base, "ticket-1"); err != nil {
		t.Fatal(err)
	}
	sh(t, root, "git clone -q origin.git agent && cd agent && git checkout -q -b ticket-1")
	cfg, err := config.Parse([]byte(testConfig + fmt.Sprintf("[agent]\nmodel = \"impl\"\nreview_model = \"rev\"\n[gate]\ncmd = '%s'\nmax_fixes = %d\n", gateCmd, maxFixes)))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	r = run{
		Options: Options{
			Base:   Base{Branch: "main", Commit: base, Config: cfg, Ident: Ident{Name: "Human", Email: "human@example.com"}},
			Ticket: forge.Ticket{Number: 1, Title: "Do it"},
			Forge:  fake.serve(t, filepath.Join(root, "origin.git")),
			Out:    &bytes.Buffer{},
		},
		stateDir:   dir,
		branch:     "ticket-1",
		quarantine: q,
		log:        &rotatingLog{path: filepath.Join(dir, "ticket-1.jsonl")},
	}
	return r, &fakeGuest{t: t, dir: filepath.Join(root, "agent")}, root
}

// failingGate fails twice, then passes; every attempt prints its number.
const failingGate = `n=$(cat ../count 2>/dev/null || echo 0); echo $((n+1)) > ../count; echo "gate attempt $n"; [ "$n" -ge 2 ]`

func TestWorkGateFixes(t *testing.T) {
	fake := &fakeGitHub{}
	r, g, root := workRun(t, fake, failingGate, 3)
	done := agent.Report{Summary: "Did it.", Deviations: []string{}}
	g.sessions = []func() agent.Report{
		g.commit("impl", done),
		g.commit("fix1", agent.Report{Summary: "Fixed 1.", Deviations: []string{}}),
		g.commit("fix2", agent.Report{Summary: "Fixed 2.", Doubts: []string{"Is it right?"}}),
		func() agent.Report { return agent.Report{Status: agent.StatusDone, Summary: "Fine."} },
	}
	res, err := r.work(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fixes != 2 {
		t.Errorf("fix sessions = %d, want 2", res.Fixes)
	}
	if want := []string{"impl", "impl", "impl", "rev"}; !slices.Equal(g.models, want) {
		t.Errorf("models = %q, want %q", g.models, want)
	}
	if !strings.Contains(g.prompts[1], "gate attempt 0") || !strings.Contains(g.prompts[2], "gate attempt 1") ||
		!strings.Contains(g.prompts[1], "minimalnie") {
		t.Errorf("fix prompts lack the gate output:\n%s\n---\n%s", g.prompts[1], g.prompts[2])
	}
	for _, want := range []string{"Did it.", "Poprawka po bramce (1): Fixed 1.", "Poprawka po bramce (2): Fixed 2."} {
		if !strings.Contains(res.Report.Summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, res.Report.Summary)
		}
	}
	if !slices.Equal(res.Report.Doubts, []string{"Is it right?"}) {
		t.Errorf("doubts = %q", res.Report.Doubts)
	}
	// The output of every attempt is kept.
	for n, want := range []string{"gate attempt 0", "gate attempt 1", "gate attempt 2"} {
		data, _ := os.ReadFile(filepath.Join(r.stateDir, fmt.Sprintf("ticket-1-gate-%d.txt", n+1)))
		if !strings.Contains(string(data), want) {
			t.Errorf("gate output %d = %q, want %q", n+1, data, want)
		}
	}
	git := "git --git-dir=" + r.quarantine.Dir + " "
	if got := sh(t, root, git+"rev-list --count "+r.Base.Commit+"..ticket-1"); got != "1" {
		t.Errorf("commits on ticket-1 = %s, want 1 (no review fixes)", got)
	}
	if err = r.publish(t.Context(), res); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /repos/o/r/pulls", "POST /repos/o/r/issues/42/comments", "POST /repos/o/r/issues/42/comments",
		"POST /repos/o/r/issues/42/labels", "DELETE /repos/o/r/issues/1/labels/ready-for-agent",
	}
	if !slices.Equal(fake.requests, want) {
		t.Errorf("requests = %q, want %q", fake.requests, want)
	}
	if fmt.Sprint(fake.bodies[3]["labels"]) != "[ready-for-human]" {
		t.Errorf("labels = %v, want ready-for-human after fix sessions", fake.bodies[3])
	}
}

// A failed Run keeps the VM (session destroys it only without an error), so
// work must return an error and accept nothing.
func TestWorkGateExhausted(t *testing.T) {
	r, g, _ := workRun(t, &fakeGitHub{}, `echo boom; false`, 2)
	g.sessions = []func() agent.Report{
		g.commit("impl", agent.Report{Summary: "Did it.", Deviations: []string{}}),
		g.commit("fix1", agent.Report{Summary: "Fixed 1."}),
		g.commit("fix2", agent.Report{Summary: "Fixed 2."}),
	}
	res, err := r.work(t.Context(), g)
	if err == nil || !strings.Contains(err.Error(), "gate failed after 2 fix session(s)") {
		t.Fatalf("err = %v, want gate failure after 2 fix sessions", err)
	}
	if res.Fixes != 2 || res.gateRuns != 3 {
		t.Errorf("fixes = %d, gate runs = %d, want 2 and 3 (max_fixes + 1)", res.Fixes, res.gateRuns)
	}
	if err = r.quarantine.CheckNew(t.Context(), r.branch); err != nil {
		t.Errorf("a failed Run accepted a result: %v", err)
	}
}

func TestWorkFixNotDone(t *testing.T) {
	r, g, _ := workRun(t, &fakeGitHub{}, `false`, 3)
	g.sessions = []func() agent.Report{
		g.commit("impl", agent.Report{Summary: "Did it."}),
		func() agent.Report { return agent.Report{Status: agent.StatusNeedsInfo, Reason: "Need a decision."} },
	}
	if _, err := r.work(t.Context(), g); err == nil || !strings.Contains(err.Error(), "did not fix the gate (status needs_info): Need a decision.") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkReview(t *testing.T) {
	fake := &fakeGitHub{}
	r, g, root := workRun(t, fake, `true`, 3)
	g.sessions = []func() agent.Report{
		g.commit("impl", agent.Report{Summary: "Did it.", Deviations: []string{}}),
		g.commit("review", agent.Report{Summary: "Two nits.", FindingsFixed: []string{"Nit."}}),
	}
	res, err := r.work(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	git := "git --git-dir=" + r.quarantine.Dir + " "
	if got, want := sh(t, root, git+"log --format=%s "+r.Base.Commit+"..ticket-1"), "code review: Do it\nDo it (#1)"; got != want {
		t.Errorf("commits on ticket-1 =\n%s\nwant\n%s", got, want)
	}
	if got := sh(t, root, git+"show ticket-1:review"); got != "review" {
		t.Errorf("the review's file is not in the result: %q", got)
	}
	if got := sh(t, root, git+"rev-parse ticket-1~2"); got != r.Base.Commit {
		t.Errorf("ticket-1~2 = %s, want the base", got)
	}
	if !strings.Contains(g.prompts[1], "git diff origin/main...HEAD") {
		t.Errorf("review prompt:\n%s", g.prompts[1])
	}
	if err = r.publish(t.Context(), res); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /repos/o/r/pulls", "POST /repos/o/r/issues/42/comments", "POST /repos/o/r/issues/42/comments",
		"DELETE /repos/o/r/issues/1/labels/ready-for-agent",
	}
	if !slices.Equal(fake.requests, want) {
		t.Errorf("requests = %q, want %q (two comments, no ready-for-human)", fake.requests, want)
	}
	if comment, _ := fake.bodies[2]["body"].(string); !strings.Contains(comment, "- Nit.") {
		t.Errorf("review comment:\n%s", comment)
	}
}

func TestWorkReviewFixesGoThroughGateAndValidation(t *testing.T) {
	t.Run("gate", func(t *testing.T) {
		// Passes on the implementation, fails once on the review's commit.
		r, g, _ := workRun(t, &fakeGitHub{}, `[ ! -f review ] || [ -f fix ]`, 3)
		g.sessions = []func() agent.Report{
			g.commit("impl", agent.Report{Summary: "Did it."}),
			g.commit("review", agent.Report{Summary: "Nit."}),
			g.commit("fix", agent.Report{Summary: "Fixed."}),
		}
		res, err := r.work(t.Context(), g)
		if err != nil {
			t.Fatal(err)
		}
		if res.Fixes != 1 {
			t.Errorf("fix sessions = %d, want 1", res.Fixes)
		}
	})
	t.Run("protected path", func(t *testing.T) {
		r, g, _ := workRun(t, &fakeGitHub{}, `true`, 3)
		g.sessions = []func() agent.Report{
			g.commit("impl", agent.Report{Summary: "Did it."}),
			func() agent.Report {
				sh(t, g.dir, "echo x >> secrets/key && git commit -qam sneaky")
				return agent.Report{Status: agent.StatusDone}
			},
		}
		if _, err := r.work(t.Context(), g); err == nil || !strings.Contains(err.Error(), "protected paths: secrets/key") {
			t.Fatalf("err = %v, want protected paths rejection", err)
		}
		if err := r.quarantine.CheckNew(t.Context(), r.branch); err != nil {
			t.Errorf("a rejected review was accepted: %v", err)
		}
	})
}

func TestWorkNeedsInfoAndReviewNotDone(t *testing.T) {
	r, g, _ := workRun(t, &fakeGitHub{}, `true`, 3)
	g.sessions = []func() agent.Report{func() agent.Report { return agent.Report{Status: agent.StatusNeedsInfo} }}
	if res, err := r.work(t.Context(), g); err != nil || res.Report.Status != agent.StatusNeedsInfo || res.Review != nil {
		t.Errorf("needs_info: %+v, %v", res, err)
	}

	r, g, _ = workRun(t, &fakeGitHub{}, `true`, 3)
	g.sessions = []func() agent.Report{
		g.commit("impl", agent.Report{Summary: "Did it."}),
		func() agent.Report { return agent.Report{Status: agent.StatusNeedsInfo, Reason: "?"} },
	}
	if _, err := r.work(t.Context(), g); err == nil || !strings.Contains(err.Error(), "code review returned needs_info") {
		t.Errorf("review not done: err = %v", err)
	}
}

func TestResultHuman(t *testing.T) {
	for name, tc := range map[string]struct {
		res  result
		want bool
	}{
		"clean":      {result{Review: &agent.Report{FindingsFixed: []string{"x"}}}, false},
		"fix":        {result{Fixes: 1}, true},
		"skipped":    {result{Review: &agent.Report{FindingsSkipped: []string{"x"}}}, true},
		"doubts":     {result{Report: agent.Report{Doubts: []string{"x"}}}, true},
		"deviations": {result{Report: agent.Report{Deviations: []string{"x"}}}, true},
	} {
		if got := tc.res.human(); got != tc.want {
			t.Errorf("%s: human() = %v, want %v", name, got, tc.want)
		}
	}
}

func TestLastLines(t *testing.T) {
	for in, want := range map[string]string{"a\nb\nc\n": "b\nc", "a\nb": "a\nb", "": "", "a\n": "a"} {
		if got := lastLines(in, 2); got != want {
			t.Errorf("lastLines(%q, 2) = %q, want %q", in, got, want)
		}
	}
}
