package run

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
)

// fakeVMs plays the VMs of a Spec: every session gets a clone of the Quarantine
// repo at the start of the Run, and the next script says what its Agent does.
type fakeVMs struct {
	t       *testing.T
	scripts []func(g *fakeGuest)
	guests  []*fakeGuest
	resumes []*Leftover // per VM: the VM it continues, nil for a fresh one
}

func (v *fakeVMs) open(ctx context.Context, r run, work workFunc) (res result, err error) {
	if len(v.scripts) == 0 {
		v.t.Fatalf("unexpected VM for #%d", r.Ticket.Number)
	}
	dir := filepath.Join(v.t.TempDir(), "repo")
	sh(v.t, filepath.Dir(dir), fmt.Sprintf("git init -q -b %[1]s repo && cd repo && git fetch -q %[2]s 'refs/*:refs/q/*' && git checkout -q -B %[1]s %[3]s",
		r.branch, r.quarantine.Dir, r.start()))
	g := &fakeGuest{t: v.t, dir: dir}
	v.scripts[0](g)
	v.scripts = v.scripts[1:]
	v.guests, v.resumes = append(v.guests, g), append(v.resumes, r.Resume)
	return work(ctx, g)
}

// child scripts a VM whose Agent commits file and reports done.
func child(file string) (script func(g *fakeGuest)) {
	return func(g *fakeGuest) {
		g.sessions = []func() agent.Report{g.commit(file, agent.Report{Summary: "Did " + file + ".", Deviations: []string{}})}
	}
}

// review scripts a VM whose Agent reviews: it commits file (none: no fixes).
func review(file string) (script func(g *fakeGuest)) {
	return func(g *fakeGuest) {
		report := agent.Report{Summary: "Reviewed.", FindingsFixed: []string{"Nit."}}
		if file == "" {
			g.sessions = []func() agent.Report{func() agent.Report { report.Status = agent.StatusDone; return report }}
			return
		}
		g.sessions = []func() agent.Report{g.commit(file, report)}
	}
}

// needsInfo scripts a VM whose Agent gives up.
func needsInfo(g *fakeGuest) {
	g.sessions = []func() agent.Report{func() agent.Report {
		return agent.Report{Status: agent.StatusNeedsInfo, Reason: "Unclear.", Questions: []string{"Which API?"}}
	}}
}

// specEnv is a user repo with origin and the Spec #9 on a fake GitHub: the
// children #10, #11 and #12, all ready for the agent.
type specEnv struct {
	root, user, base string
	fake             *fakeGitHub
}

func newSpecEnv(t *testing.T) (env specEnv) {
	t.Helper()
	env.root, env.user, env.base = userRepo(t)
	ready := func() []string { return []string{"ready-for-agent"} }
	env.fake = &fakeGitHub{issues: map[int]*fakeIssue{
		9:  {title: "Spec", open: true, labels: ready()},
		10: {title: "First", open: true, labels: ready(), parent: 9},
		11: {title: "Second", open: true, labels: ready(), parent: 9},
		12: {title: "Third", open: true, labels: ready(), parent: 9},
		20: {title: "Elsewhere", open: true},
	}}
	return
}

// spec is a Run of the Spec on a fresh Quarantine repo; vms plays its VMs.
func (env specEnv) spec(t *testing.T, vms *fakeVMs) (s *specRun) {
	t.Helper()
	q, err := OpenQuarantine(t.Context(), filepath.Join(t.TempDir(), "quarantine.git"))
	if err != nil {
		t.Fatal(err)
	}
	return env.specOn(t, vms, q)
}

// specOn is spec on the Quarantine repo q, as in a second process of afk.
func (env specEnv) specOn(t *testing.T, vms *fakeVMs, q Quarantine) (s *specRun) {
	t.Helper()
	cfg, err := config.Parse([]byte(testConfig + "[agent]\nmodel = \"impl\"\nreview_model = \"rev\"\n[gate]\ncmd = 'true'\nmax_fixes = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	vms.t = t
	return &specRun{
		run: run{
			Options: Options{
				Base:   Base{Repo: env.user, Branch: "main", Commit: env.base, Config: cfg, Ident: Ident{Name: "Human", Email: "human@example.com"}},
				Ticket: forge.Ticket{Number: 9, Title: "Spec", SubIssues: 3},
				Forge:  env.fake.serve(t, filepath.Join(env.root, "origin.git")),
				Out:    &bytes.Buffer{},
			},
			stateDir:   t.TempDir(),
			branch:     "ticket-9",
			quarantine: q,
		},
		open: vms.open,
	}
}

// writes are the requests that changed something, without git.
func (fake *fakeGitHub) writes() (writes []string) {
	for _, request := range fake.requests {
		if !strings.HasPrefix(request, "GET ") {
			writes = append(writes, request)
		}
	}
	return
}

// childWrites are the writes after a finished child: its report, the
// comment on it, closing it and taking the label off.
func childWrites(n int) (writes []string) {
	return []string{
		"POST /repos/o/r/issues/42/comments",
		fmt.Sprintf("POST /repos/o/r/issues/%d/comments", n),
		fmt.Sprintf("PATCH /repos/o/r/issues/%d", n),
		fmt.Sprintf("DELETE /repos/o/r/issues/%d/labels/ready-for-agent", n),
	}
}

func TestSpecThreeChildren(t *testing.T) {
	env := newSpecEnv(t)
	vms := &fakeVMs{scripts: []func(g *fakeGuest){child("c1"), child("c2"), child("c3"), review("fix")}}
	s := env.spec(t, vms)
	if err := s.execute(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, s.Out)
	}

	want := slices.Concat(
		[]string{"POST /repos/o/r/pulls"}, childWrites(10), childWrites(11), childWrites(12),
		[]string{"POST /repos/o/r/issues/42/comments", "PATCH /repos/o/r/pulls/42", "POST /graphql", "DELETE /repos/o/r/issues/9/labels/ready-for-agent"},
	)
	if got := env.fake.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(vms.scripts) != 0 {
		t.Errorf("%d VMs not used", len(vms.scripts))
	}

	pull := env.fake.bodies[slices.Index(env.fake.requests, "POST /repos/o/r/pulls")]
	if pull["draft"] != true || pull["head"] != "ticket-9" || pull["base"] != "main" || pull["title"] != "Spec" ||
		pull["body"] != "Closes #9\nCloses #10\nCloses #11\nCloses #12\n" {
		t.Errorf("draft pull request = %v", pull)
	}
	if env.fake.pull["draft"] != false {
		t.Error("the pull request is still a draft")
	}
	body, _ := env.fake.pull["body"].(string)
	if want := "Closes #9\nCloses #10\nCloses #11\nCloses #12\n\n- First (#10)\n- Second (#11)\n- Third (#12)\n- code review: Spec\n"; body != want {
		t.Errorf("final description = %q, want %q", body, want)
	}
	for _, n := range []int{10, 11, 12} {
		if env.fake.issues[n].open || slices.Contains(env.fake.issues[n].labels, "ready-for-agent") {
			t.Errorf("child #%d: %+v, want closed and without the label", n, env.fake.issues[n])
		}
	}
	if slices.Contains(env.fake.issues[9].labels, "ready-for-agent") {
		t.Error("the Spec keeps ready-for-agent")
	}
	if slices.Contains(env.fake.issues[9].labels, "ready-for-human") || len(env.fake.pull) == 0 {
		t.Errorf("Spec labels = %v", env.fake.issues[9].labels)
	}
	// The comment on a child links the pull request.
	if done, _ := env.fake.bodies[slices.Index(env.fake.requests, "POST /repos/o/r/issues/10/comments")]["body"].(string); !strings.Contains(done, "https://github.com/o/r/pull/42") || !strings.Contains(done, "ticket-9") {
		t.Errorf("comment on #10: %q", done)
	}
	if report, _ := env.fake.bodies[slices.Index(env.fake.requests, "POST /repos/o/r/issues/42/comments")]["body"].(string); !strings.Contains(report, "### #10 First") || !strings.Contains(report, "Did c1.") {
		t.Errorf("report comment: %q", report)
	}

	// One branch: a commit per child and the review's, each on the one before,
	// pushed to the Forge at the end.
	git := "git --git-dir=" + s.quarantine.Dir + " "
	if got, want := sh(t, env.root, git+"log --reverse --format=%s "+env.base+"..ticket-9"), "First (#10)\nSecond (#11)\nThird (#12)\ncode review: Spec"; got != want {
		t.Errorf("commits on ticket-9 =\n%s\nwant\n%s", got, want)
	}
	if got, want := sh(t, env.root, "git --git-dir=origin.git rev-parse ticket-9"), sh(t, env.root, git+"rev-parse ticket-9"); got != want {
		t.Errorf("remote ticket-9 = %s, want %s", got, want)
	}
	if got := sh(t, env.root, git+"ls-tree --name-only ticket-9 | grep -c '^c[123]$\\|^fix$'"); got != "4" {
		t.Errorf("files of all children and the review on the branch: %s of 4", got)
	}

	// Every child is told about the Spec; the review is of the Spec itself.
	for i, g := range vms.guests[:3] {
		if !strings.Contains(g.prompts[0], guestSpec) {
			t.Errorf("prompt of child %d lacks the Spec:\n%s", i+1, g.prompts[0])
		}
	}
	if strings.Contains(vms.guests[3].prompts[0], guestSpec) || !strings.Contains(vms.guests[3].prompts[0], "git diff origin/main...HEAD") {
		t.Errorf("review prompt:\n%s", vms.guests[3].prompts[0])
	}
}

func TestSpecBlocked(t *testing.T) {
	t.Run("first skipped", func(t *testing.T) {
		env := newSpecEnv(t)
		env.fake.issues[10].blockers = []int{20}
		env.fake.issues[12].body = "Blocked by: #10"
		vms := &fakeVMs{scripts: []func(g *fakeGuest){child("c2")}}
		s := env.spec(t, vms)
		err := s.execute(t.Context())
		if err == nil || !strings.Contains(err.Error(), "all are blocked") {
			t.Fatalf("err = %v, want open children all blocked", err)
		}
		// #11 was done (the first one is blocked by an open issue, the last by the first).
		if env.fake.issues[11].open || !env.fake.issues[10].open || !env.fake.issues[12].open {
			t.Errorf("open: #10 %v, #11 %v, #12 %v", env.fake.issues[10].open, env.fake.issues[11].open, env.fake.issues[12].open)
		}
		if got := sh(t, env.root, "git --git-dir=origin.git log --format=%s "+env.base+"..ticket-9"); got != "Second (#11)" {
			t.Errorf("commits on the remote ticket-9 = %q", got)
		}
		if env.fake.pull["draft"] != true {
			t.Error("the pull request is not a draft")
		}
	})
	t.Run("all blocked", func(t *testing.T) {
		env := newSpecEnv(t)
		for _, n := range []int{10, 11, 12} {
			env.fake.issues[n].blockers = []int{20}
		}
		s := env.spec(t, &fakeVMs{})
		if err := s.execute(t.Context()); err == nil || !strings.Contains(err.Error(), "all are blocked") {
			t.Fatalf("err = %v", err)
		}
		if writes := env.fake.writes(); len(writes) != 0 {
			t.Errorf("writes = %q, want none", writes)
		}
	})
	t.Run("child without the label", func(t *testing.T) {
		env := newSpecEnv(t)
		env.fake.issues[12].labels = nil
		s := env.spec(t, &fakeVMs{})
		if err := s.execute(t.Context()); err == nil || !strings.Contains(err.Error(), "#12 lacks the label") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSpecNeedsInfo(t *testing.T) {
	env := newSpecEnv(t)
	vms := &fakeVMs{scripts: []func(g *fakeGuest){child("c1"), needsInfo}}
	s := env.spec(t, vms)
	err := s.execute(t.Context())
	if err == nil || !strings.Contains(err.Error(), "child #11 needs info") || !strings.Contains(err.Error(), "stay on ticket-9") {
		t.Fatalf("err = %v", err)
	}
	want := slices.Concat(
		[]string{"POST /repos/o/r/pulls"}, childWrites(10),
		[]string{
			"POST /repos/o/r/issues/11/comments", "POST /repos/o/r/issues/11/labels", "DELETE /repos/o/r/issues/11/labels/ready-for-agent",
			"POST /repos/o/r/issues/9/comments", "POST /repos/o/r/issues/9/labels", "DELETE /repos/o/r/issues/9/labels/ready-for-agent",
		},
	)
	if got := env.fake.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, n := range []int{9, 11} {
		if issue := env.fake.issues[n]; !slices.Equal(issue.labels, []string{"needs-info"}) {
			t.Errorf("labels of #%d = %v, want only needs-info", n, issue.labels)
		}
	}
	if comment, _ := env.fake.bodies[slices.Index(env.fake.requests, "POST /repos/o/r/issues/11/comments")]["body"].(string); !strings.Contains(comment, "Unclear.") || !strings.Contains(comment, "- Which API?") {
		t.Errorf("comment on #11: %q", comment)
	}
	// The first child stays, in the Quarantine repo and on the Forge.
	git := "git --git-dir=" + s.quarantine.Dir + " "
	for _, cmd := range []string{git, "git --git-dir=origin.git "} {
		if got := sh(t, env.root, cmd+"log --format=%s "+env.base+"..ticket-9"); got != "First (#10)" {
			t.Errorf("%s: commits on ticket-9 = %q", cmd, got)
		}
	}
	if env.fake.issues[10].open {
		t.Error("#10 is still open")
	}
}

func TestSpecResume(t *testing.T) {
	for name, tc := range map[string]struct {
		sameQuarantine bool // else afk starts again with nothing, and main has moved on
	}{
		"same Quarantine":  {true},
		"fresh Quarantine": {false},
	} {
		t.Run(name, func(t *testing.T) {
			env := newSpecEnv(t)
			first := env.spec(t, &fakeVMs{scripts: []func(g *fakeGuest){child("c1"), needsInfo}})
			if err := first.execute(t.Context()); err == nil {
				t.Fatal("want needs_info")
			}
			// The human answers and makes the Spec ready again.
			for _, n := range []int{9, 11} {
				env.fake.issues[n].labels = []string{"ready-for-agent"}
			}
			q := first.quarantine
			if !tc.sameQuarantine {
				sh(t, env.user, "echo m > moved && git add moved && git commit -qm moved && git push -q origin main")
				env.base = sh(t, env.user, "git rev-parse HEAD")
				var err error
				if q, err = OpenQuarantine(t.Context(), filepath.Join(t.TempDir(), "quarantine.git")); err != nil {
					t.Fatal(err)
				}
			}
			env.fake.requests, env.fake.bodies = nil, nil

			vms := &fakeVMs{scripts: []func(g *fakeGuest){child("c2"), child("c3"), review("")}}
			s := env.specOn(t, vms, q)
			if err := s.execute(t.Context()); err != nil {
				t.Fatalf("%v\n%s", err, s.Out)
			}
			// The next child starts from the end of the branch.
			if got := sh(t, vms.guests[0].dir, "git log -1 --format=%s HEAD~1"); got != "First (#10)" {
				t.Errorf("the VM of #11 starts at %q, want the work of #10", got)
			}
			want := slices.Concat(
				childWrites(11), childWrites(12),
				[]string{"POST /repos/o/r/issues/42/comments", "PATCH /repos/o/r/pulls/42", "POST /graphql", "DELETE /repos/o/r/issues/9/labels/ready-for-agent"},
			)
			if got := env.fake.writes(); !slices.Equal(got, want) {
				t.Fatalf("writes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if env.fake.pull["draft"] != false {
				t.Error("the pull request is still a draft")
			}
			if got, want := sh(t, env.root, "git --git-dir=origin.git log --reverse --format=%s ticket-9"), "base\nFirst (#10)\nSecond (#11)\nThird (#12)"; got != want {
				t.Errorf("remote ticket-9 =\n%s\nwant\n%s", got, want)
			}
			if !tc.sameQuarantine && s.Base.Commit == env.base {
				t.Error("the base is the moved main, which the branch does not contain: want the merge base")
			}
		})
	}
}

// The VM an earlier Factory left for a child takes the Run of that child
// and nothing else; it holds the base of the earlier Run, not today's.
func TestSpecLeftover(t *testing.T) {
	env := newSpecEnv(t)
	q, err := OpenQuarantine(t.Context(), filepath.Join(t.TempDir(), "quarantine.git"))
	if err != nil {
		t.Fatal(err)
	}
	if err = q.FetchBase(t.Context(), env.user, env.base, "ticket-9"); err != nil {
		t.Fatal(err)
	}
	sh(t, env.user, "echo m > moved && git add moved && git commit -qm moved")
	moved := sh(t, env.user, "git rev-parse HEAD")

	vms := &fakeVMs{scripts: []func(g *fakeGuest){child("c1"), child("c2"), child("c3"), review("")}}
	s := env.specOn(t, vms, q)
	s.Base.Commit = moved
	leftover := Leftover{Machine: "afk-run-10-ab12", Ticket: 10}
	s.leftover = &leftover
	if err = s.execute(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, s.Out)
	}
	if len(vms.resumes) != 4 || vms.resumes[0] == nil || *vms.resumes[0] != leftover || slices.ContainsFunc(vms.resumes[1:], func(l *Leftover) bool { return l != nil }) {
		t.Errorf("resumes %v, want only the VM of #10 continued", vms.resumes)
	}
	if !strings.Contains(vms.guests[0].prompts[0], "Wznawiasz przerwaną pracę") || strings.Contains(vms.guests[1].prompts[0], "Wznawiasz") {
		t.Errorf("the note belongs in the first prompt only:\n%s\n---\n%s", vms.guests[0].prompts[0], vms.guests[1].prompts[0])
	}
	if s.Base.Commit != env.base {
		t.Errorf("base %s, want %s of the earlier Run, not the moved main %s", s.Base.Commit, env.base, moved)
	}
	if s.leftover != nil || strings.Contains(s.Out.(*bytes.Buffer).String(), "was not used") {
		t.Errorf("the leftover was used:\n%s", s.Out)
	}
}

func TestSpecLeftoverOfNothing(t *testing.T) {
	env := newSpecEnv(t)
	vms := &fakeVMs{scripts: []func(g *fakeGuest){child("c1"), child("c2"), child("c3"), review("")}}
	s := env.spec(t, vms)
	if err := s.quarantine.FetchBase(t.Context(), env.user, env.base, "ticket-9"); err != nil {
		t.Fatal(err)
	}
	s.leftover = &Leftover{Machine: "afk-run-99-ab12", Ticket: 99}
	if err := s.execute(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, s.Out)
	}
	if !strings.Contains(s.Out.(*bytes.Buffer).String(), "VM afk-run-99-ab12 of #99 was not used") {
		t.Errorf("output does not say that the VM was not used:\n%s", s.Out)
	}
}

// A Forge step fails after the push: the commit is on the branch, the child
// is open. The next run finishes the child instead of running it again.
func TestSpecResumeHalfFinishedChild(t *testing.T) {
	env := newSpecEnv(t)
	env.fake.fail = "PATCH /repos/o/r/issues/10"
	first := env.spec(t, &fakeVMs{scripts: []func(g *fakeGuest){child("c1")}})
	err := first.execute(t.Context())
	if err == nil || !strings.Contains(err.Error(), "close #10") {
		t.Fatalf("err = %v, want the failed closing of #10", err)
	}
	env.fake.fail = ""
	env.fake.requests, env.fake.bodies = nil, nil

	vms := &fakeVMs{scripts: []func(g *fakeGuest){child("c2"), child("c3"), review("")}}
	s := env.specOn(t, vms, first.quarantine)
	if err = s.execute(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, s.Out)
	}
	want := slices.Concat(
		[]string{"POST /repos/o/r/issues/10/comments", "PATCH /repos/o/r/issues/10", "DELETE /repos/o/r/issues/10/labels/ready-for-agent"},
		childWrites(11), childWrites(12),
		[]string{"POST /repos/o/r/issues/42/comments", "PATCH /repos/o/r/pulls/42", "POST /graphql", "DELETE /repos/o/r/issues/9/labels/ready-for-agent"},
	)
	if got := env.fake.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(s.Out.(*bytes.Buffer).String(), "its report is not on the pull request") {
		t.Errorf("output does not say the report is missing:\n%s", s.Out)
	}
	if got, want := sh(t, env.root, "git --git-dir=origin.git log --reverse --format=%s ticket-9"), "base\nFirst (#10)\nSecond (#11)\nThird (#12)"; got != want {
		t.Errorf("remote ticket-9 =\n%s\nwant\n%s", got, want)
	}
	if env.fake.issues[10].open {
		t.Error("#10 is still open")
	}
}

// The Quarantine repo has commits the Forge lacks: nothing is overwritten.
func TestSpecResumeLocalAhead(t *testing.T) {
	env := newSpecEnv(t)
	first := env.spec(t, &fakeVMs{scripts: []func(g *fakeGuest){child("c1"), needsInfo}})
	if err := first.execute(t.Context()); err == nil {
		t.Fatal("want needs_info")
	}
	git := "git --git-dir=" + first.quarantine.Dir + " "
	sh(t, env.root, git+"update-ref refs/heads/ticket-9 $("+git+"commit-tree ticket-9^{tree} -p ticket-9 -m extra)")
	local := sh(t, env.root, git+"rev-parse ticket-9")
	s := env.specOn(t, &fakeVMs{}, first.quarantine)
	err := s.execute(t.Context())
	if err == nil || !strings.Contains(err.Error(), "has commits that") || !strings.Contains(err.Error(), "update-ref -d refs/heads/ticket-9") {
		t.Fatalf("err = %v, want instructions", err)
	}
	if got := sh(t, env.root, git+"rev-parse ticket-9"); got != local {
		t.Errorf("the local result moved to %s", got)
	}
}

func TestSpecResumeRefused(t *testing.T) {
	for name, pull := range map[string]map[string]any{
		"ready for review": {"number": 42, "state": "open", "draft": false},
		"closed":           {"number": 42, "state": "closed", "draft": true},
	} {
		t.Run(name, func(t *testing.T) {
			env := newSpecEnv(t)
			sh(t, env.root, "git --git-dir=origin.git update-ref refs/heads/ticket-9 "+env.base)
			env.fake.pull = pull
			s := env.spec(t, &fakeVMs{})
			err := s.execute(t.Context())
			if err == nil || !strings.Contains(err.Error(), "is not an open draft") || !strings.Contains(err.Error(), "git push origin --delete ticket-9") {
				t.Fatalf("err = %v, want instructions", err)
			}
			if writes := env.fake.writes(); len(writes) != 0 {
				t.Errorf("writes = %q, want none", writes)
			}
		})
	}
}

func TestSpecNotASpec(t *testing.T) {
	env := newSpecEnv(t)
	s := env.spec(t, &fakeVMs{})
	s.Ticket.SubIssues = 0
	if err := s.execute(t.Context()); err == nil || !strings.Contains(err.Error(), "not a Spec") {
		t.Errorf("err = %v", err)
	}
}

func TestCloses(t *testing.T) {
	s := specRun{run: run{Options: Options{Ticket: forge.Ticket{Number: 9}}}, children: []int{10, 12}}
	if got, want := s.closes(), "Closes #9\nCloses #10\nCloses #12\n"; got != want {
		t.Errorf("closes = %q, want %q", got, want)
	}
}

// fakeIssue is an issue of a fake Spec.
type fakeIssue struct {
	title    string
	body     string
	open     bool
	labels   []string
	parent   int
	blockers []int
}

// answer plays the part of GitHub for Specs: reads and the writes that change
// what is read later. It reports false for the rest, which gets the default answer.
func (fake *fakeGitHub) answer(req *http.Request, body map[string]any) (reply any, ok bool) {
	// /repos/o/r/issues/<n>/...: n is the issue, the route has {n} instead.
	parts := strings.Split(req.URL.Path, "/")
	var n int
	if len(parts) > 5 && parts[4] == "issues" {
		n, _ = strconv.Atoi(parts[5])
		parts[5] = "{n}"
	}
	issue := fake.issues[n]
	route := req.Method + " " + strings.Join(parts, "/")
	list := func(match func(*fakeIssue) bool) (items []map[string]any) {
		// Descending: the code must sort.
		for _, number := range slices.Backward(slices.Sorted(maps.Keys(fake.issues))) {
			if match(fake.issues[number]) {
				items = append(items, fake.json(number))
			}
		}
		return
	}
	switch route {
	case "GET /repos/o/r/issues":
		// By label, as GitHub lists them: stale when listed is set, and with pull requests.
		var items []map[string]any
		if fake.listed != nil {
			for _, number := range fake.listed {
				items = append(items, fake.json(number))
			}
		} else {
			items = list(func(issue *fakeIssue) bool {
				return issue.open && slices.Contains(issue.labels, req.URL.Query().Get("labels"))
			})
		}
		return append(items, map[string]any{"number": 999, "state": "open", "pull_request": map[string]string{}}), true
	case "GET /repos/o/r/issues/{n}":
		return fake.json(n), true
	case "GET /repos/o/r/issues/{n}/comments":
		return []string{}, true
	case "GET /repos/o/r/issues/{n}/sub_issues":
		return list(func(child *fakeIssue) bool { return child.parent == n }), true
	case "GET /repos/o/r/issues/{n}/dependencies/blocked_by":
		blockers := []map[string]any{}
		for _, blocker := range issue.blockers {
			blockers = append(blockers, fake.json(blocker))
		}
		return blockers, true
	case "PATCH /repos/o/r/issues/{n}":
		issue.open = body["state"] != "closed"
		return fake.json(n), true
	case "POST /repos/o/r/issues/{n}/labels":
		if issue != nil {
			for _, label := range body["labels"].([]any) {
				issue.labels = append(issue.labels, label.(string))
			}
		}
	case "DELETE /repos/o/r/issues/{n}/labels/ready-for-agent":
		if issue != nil {
			issue.labels = slices.DeleteFunc(issue.labels, func(label string) bool { return label == "ready-for-agent" })
		}
	case "GET /repos/o/r/pulls":
		if fake.pull == nil {
			return []any{}, true
		}
		return []any{fake.pull}, true
	case "POST /repos/o/r/pulls":
		fake.pull = map[string]any{"number": 42, "html_url": "https://github.com/o/r/pull/42", "state": "open", "draft": body["draft"] == true, "node_id": "PR_42", "body": body["body"]}
		return fake.pull, true
	case "PATCH /repos/o/r/pulls/42":
		fake.pull["body"] = body["body"]
		return fake.pull, true
	case "POST /graphql":
		fake.pull["draft"] = false
		return map[string]any{"data": map[string]any{}}, true
	}
	return nil, false
}

// json is issue n as the API shows it.
func (fake *fakeGitHub) json(n int) (issue map[string]any) {
	i := fake.issues[n]
	state := "closed"
	if i.open {
		state = "open"
	}
	labels := []map[string]string{}
	for _, label := range i.labels {
		labels = append(labels, map[string]string{"name": label})
	}
	children := 0
	for _, child := range fake.issues {
		if child.parent == n {
			children++
		}
	}
	issue = map[string]any{"number": n, "title": i.title, "body": i.body, "state": state, "labels": labels, "sub_issues_summary": map[string]int{"total": children}}
	if i.parent != 0 {
		issue["parent_issue_url"] = fmt.Sprintf("https://api.github.com/repos/o/r/issues/%d", i.parent)
	}
	return
}
