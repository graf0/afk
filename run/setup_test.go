package run

import (
	"context"
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

func TestSetupOrderAndOutput(t *testing.T) {
	r, g := setupRun(t, []string{"echo b1 | tee -a ../order"}, []string{"echo a1 | tee -a ../order", "echo a2 >&2"})
	var sawBeforeWork string
	work := func(ctx context.Context, g guest) (res result, err error) {
		var order tailBuffer
		if err = g.Exec(ctx, "cat ../order", &order); err != nil {
			return
		}
		sawBeforeWork = order.String()
		return
	}
	if _, err := r.withSetup(work)(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	// Tools alphabetically, and all of it before the work (the sessions).
	if want := "a1\nb1\n"; sawBeforeWork != want {
		t.Errorf("setup order = %q, want %q", sawBeforeWork, want)
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

func TestSetupFailureStopsTheRun(t *testing.T) {
	r, g := setupRun(t, []string{"echo not-run > ../b"}, []string{"echo seeding", "echo broken >&2; exit 3"})
	worked := false
	work := func(context.Context, guest) (res result, err error) { worked = true; return }
	_, err := r.withSetup(work)(t.Context(), g)
	if err == nil {
		t.Fatal("want error")
	}
	path := strings.TrimSuffix(r.log.path, ".jsonl") + "-setup.txt"
	if !strings.Contains(err.Error(), "setup of tool a failed") || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v", err)
	}
	if worked {
		t.Error("work ran after a failed setup")
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
	worked := false
	work := func(context.Context, guest) (res result, err error) { worked = true; return }
	if _, err := r.withSetup(work)(t.Context(), g); err != nil || !worked {
		t.Fatalf("err = %v, worked = %v", err, worked)
	}
	if _, err := os.Stat(strings.TrimSuffix(r.log.path, ".jsonl") + "-setup.txt"); err == nil {
		t.Error("setup file without any setup command")
	}
}

// TestPreparedSkipsSetupOfResumedVM: only a fresh VM (prepare) runs the setup.
func TestPreparedSkipsSetupOfResumedVM(t *testing.T) {
	for _, prepare := range []bool{true, false} {
		r, g := setupRun(t, nil, []string{"echo ran > ../setup-ran"})
		worked := false
		work := func(context.Context, guest) (res result, err error) { worked = true; return }
		if _, err := r.prepared(prepare, work)(t.Context(), g); err != nil || !worked {
			t.Fatalf("prepare=%v: err = %v, worked = %v", prepare, err, worked)
		}
		_, statErr := os.Stat(g.dir + "/../setup-ran")
		if ran := statErr == nil; ran != prepare {
			t.Errorf("prepare=%v: setup ran = %v", prepare, ran)
		}
	}
}
