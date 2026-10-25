package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/graf0/afk/machine"
)

// preflightBranch names the base of the preflight in the Quarantine repo.
const preflightBranch = "preflight"

// preflight checks what can fail for reasons that are not the Agent's, before
// the first Run (preflight in docs/reference/ralph.sh): a Base image and an ssh
// key exist, and the gate passes on the clean base branch in a fresh VM. The
// credentials are checked by whoever built opts. The VM is claimed for as long
// as it lives (preflightKey), so a Factory that finds it later knows nobody uses it. A failing gate fails the
// preflight with the path of its output; its VM is destroyed.
func preflight(ctx context.Context, opts Options, claims Claims) (err error) {
	r, setup, err := newRun(ctx, opts)
	if err != nil {
		return
	}
	r.branch = preflightBranch
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	r.id = "0-" + hex.EncodeToString(random)
	name, err := machine.Name(machine.KindRun, r.id)
	if err != nil {
		return
	}
	release, ok, err := claims.ClaimKeys(preflightKey(name))
	if err != nil {
		return
	}
	defer release()
	if !ok {
		err = fmt.Errorf("the claim of the preflight VM %s is taken", name)
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
	_, err = r.session(ctx, setup, func(ctx context.Context, g guest) (res result, err error) {
		var path string
		var ok bool
		if _, path, ok, err = r.gateAttempt(ctx, g, &res); err == nil && !ok {
			failed = path
		}
		return
	})
	if err == nil && failed != "" {
		err = fmt.Errorf("preflight: the gate fails on a clean origin/%s, not the Agent's fault: fix the base or [gate].cmd (output: %s)", opts.Base.Branch, failed)
	}
	return
}

// preflightKey is the claim of the preflight VM called machine.
func preflightKey(machine string) (key string) { return "preflight-" + machine }
