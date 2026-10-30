package run

import (
	"os"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
)

// setupRun is workRun whose Tools b and a (declared in that order) have setup.
func setupRun(t *testing.T, setupB, setupA []string) (r run, g *fakeGuest) {
	t.Helper()
	r, g, _ = workRun(t, &fakeGitHub{}, "true", 0)
	r.Base.Config.Tool = map[string]config.Tool{
		"b": {Setup: setupB},
		"a": {Setup: setupA},
		"c": {}, // no setup: nothing to run
	}
	return r, g
}

// setupTools is SetupTools as a Chat's guest calls it.
func setupTools(t *testing.T, r run, g *fakeGuest) (err error) {
	t.Helper()
	return SetupTools(t.Context(), g.Exec, r.Base.Config, r.log.path, r.Out)
}

func TestSetupOrderAndOutput(t *testing.T) {
	r, g := setupRun(t, []string{"echo b1 | tee -a ../order"}, []string{"echo a1 | tee -a ../order", "echo a2 >&2"})
	if err := setupTools(t, r, g); err != nil {
		t.Fatal(err)
	}
	var order tailBuffer
	if err := g.Exec(t.Context(), "cat ../order", &order); err != nil {
		t.Fatal(err)
	}
	// Tools alphabetically.
	if want := "a1\nb1\n"; order.String() != want {
		t.Errorf("setup order = %q, want %q", order.String(), want)
	}
	out, err := os.ReadFile(strings.TrimSuffix(r.log.path, ".jsonl") + "-setup.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"$ echo a1", "a2\n", "$ echo b1"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("setup output lacks %q:\n%s", want, out)
		}
	}
}

func TestSetupFailureIsAnError(t *testing.T) {
	r, g := setupRun(t, []string{"echo not-run > ../b"}, []string{"echo seeding", "echo broken >&2; exit 3"})
	err := setupTools(t, r, g)
	if err == nil {
		t.Fatal("want error")
	}
	path := strings.TrimSuffix(r.log.path, ".jsonl") + "-setup.txt"
	if !strings.Contains(err.Error(), "setup of tool a failed") || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v", err)
	}
	if _, statErr := os.Stat(g.dir + "/../b"); statErr == nil {
		t.Error("the setup of the next Tool ran after a failure")
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "seeding") || !strings.Contains(string(out), "exit status 3") {
		t.Errorf("setup output:\n%s", out)
	}
}

func TestSetupWithoutCommandsLeavesNoFile(t *testing.T) {
	r, g := setupRun(t, nil, nil)
	if err := setupTools(t, r, g); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSuffix(r.log.path, ".jsonl") + "-setup.txt"); err == nil {
		t.Error("setup file without any setup command")
	}
}
