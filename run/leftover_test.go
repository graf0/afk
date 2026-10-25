package run

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/machine"
)

func TestLeftoverTicket(t *testing.T) {
	for id, want := range map[string]int{"12-ab12cd34": 12, "0-ab12cd34": 0, "ab12cd34": -1, "x-1": -1} {
		n, ok := leftoverTicket(machine.Machine{ID: id, Kind: machine.KindRun})
		if (want >= 0) != ok || (ok && n != want) {
			t.Errorf("leftoverTicket(%q) = %d, %v; want %d", id, n, ok, want)
		}
	}
	if _, ok := leftoverTicket(machine.Machine{ID: "12-ab", Kind: machine.KindChat}); ok {
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
