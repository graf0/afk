package run

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/stack"
)

func TestLeftoverTicket(t *testing.T) {
	for name, want := range map[string]int{"ticket-12": 12, "preflight-ab12cd34": 0, "ticket-0": -1, "ticket-x": -1, "ticket-12-a": -1, "preflight": -1, "plan": -1} {
		n, ok := leftoverTicket(name)
		if (want >= 0) != ok || (ok && n != want) {
			t.Errorf("leftoverTicket(%q) = %d, %v; want %d", name, n, ok, want)
		}
	}
}

func TestListLeftovers(t *testing.T) {
	state := t.TempDir()
	for _, name := range []string{"ticket-12", "ticket-3", "preflight-ab12cd34", "scratch"} {
		if err := os.MkdirAll(stack.Dir(state, "o-r", stack.Run, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(stack.Dir(state, "o-r", stack.Chat, "ticket-5"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ListLeftovers(state, "o-r")
	want := []Leftover{{"preflight-ab12cd34", 0}, {"ticket-3", 3}, {"ticket-12", 12}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("ListLeftovers = %v, %v; want %v (Runs of the Factory only, by ticket)", got, err, want)
	}
}

func TestIsLeftover(t *testing.T) {
	for id, want := range map[string]bool{"12-ab12cd34": true, "0-ab12cd34": true, "ab12cd34": false, "x-1": false} {
		if got := IsLeftover(machine.Machine{ID: id, Kind: machine.KindRun}); got != want {
			t.Errorf("IsLeftover(%q) = %v, want %v", id, got, want)
		}
	}
	if IsLeftover(machine.Machine{ID: "12-ab", Kind: machine.KindChat}) {
		t.Error("a Chat is no leftover Run")
	}
}

func TestCleanBranch(t *testing.T) {
	for name, withPull := range map[string]bool{"no pull request": false, "with a pull request": true} {
		t.Run(name, func(t *testing.T) {
			root, _, base, q := acceptedResult(t)
			sh(t, root, "git --git-dir=origin.git update-ref refs/heads/ticket-1 "+base)
			fake := &fakeGitHub{issues: map[int]*fakeIssue{}}
			if withPull {
				fake.pull = map[string]any{"number": 42, "state": "open"}
			}
			g := fake.serve(t, filepath.Join(root, "origin.git"))
			var out bytes.Buffer
			if err := cleanBranch(t.Context(), g, q, "ticket-1", &out); err != nil {
				t.Fatal(err)
			}
			if got := sh(t, root, "git --git-dir="+q.Dir+" for-each-ref refs/heads refs/afk"); got != "" {
				t.Errorf("Quarantine refs left:\n%s", got)
			}
			remote := sh(t, root, "git --git-dir=origin.git for-each-ref --format='%(refname)' refs/heads/ticket-1")
			want := map[bool]string{false: "deleted", true: "stays"}[withPull]
			if withPull != (remote != "") || !strings.Contains(out.String(), want) {
				t.Errorf("remote branch %q, output %q: want it kept only with a pull request", remote, out.String())
			}
		})
	}
}
