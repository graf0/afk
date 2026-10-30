package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	// preflightBranch names the base of the preflight in the Quarantine repo.
	preflightBranch = "preflight"
	// preflightPrefix starts the name of the preflight's Stack.
	preflightPrefix = "preflight-"
)

// preflight checks what can fail for reasons that are not the Agent's, before
// the first Run (preflight in docs/reference/ralph.sh): the Stack of the base
// branch starts, and the gate passes on the clean base branch in its Sandbox.
// The credentials are checked by whoever built opts. The Stack is claimed for
// as long as it lives (the claim key is its name), so a Factory that finds its state later
// knows nobody uses it. A failing gate fails the preflight with the path of
// its output; its Stack is removed.
func preflight(ctx context.Context, opts Options, claims Claims) (err error) {
	r, err := newRun(ctx, opts)
	if err != nil {
		return
	}
	r.branch = preflightBranch
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	r.id = preflightPrefix + hex.EncodeToString(random)
	release, ok, err := claims.ClaimKeys(r.id)
	if err != nil {
		return
	}
	defer release()
	if !ok {
		err = fmt.Errorf("the claim of the preflight Stack %s is taken", r.id)
		return
	}
	if err = r.quarantine.FetchBase(ctx, opts.Base.Repo, opts.Base.Commit, r.branch); err != nil {
		return
	}
	if _, err = r.openLog(r.branch); err != nil {
		return
	}
	defer r.closeLogs()
	r.say("preflight: the gate on a clean origin/%s %s", opts.Base.Branch, opts.Base.Commit[:12])
	var failed string
	_, err = r.enter(ctx, func(ctx context.Context, g guest) (res result, err error) {
		var path string
		var ok bool
		if _, path, ok, err = r.gateAttempt(ctx, g, &res); err != nil {
			return
		}
		if !ok {
			failed = path
		}
		return
	})
	if err != nil {
		return
	}
	if failed != "" {
		err = fmt.Errorf("preflight: the gate fails on a clean origin/%s, not the Agent's fault: fix the base or [gate].cmd (output: %s)", opts.Base.Branch, failed)
	}
	return
}
