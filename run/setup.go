package run

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/graf0/afk/agent"
)

// withSetup is work preceded by the Tools' setup, which runs in a guest whose
// clone is prepared and before any of its sessions. A VM of a resumed Run has
// had it already: its work is not wrapped.
func (r run) withSetup(work workFunc) (wrapped workFunc) {
	return func(ctx context.Context, g guest) (res result, err error) {
		if err = r.toolSetup(ctx, g); err != nil {
			return
		}
		res, err = work(ctx, g)
		return
	}
}

// toolSetup runs the setup commands of the Tools in the clone, Tools in name
// order, with their env (the guest's Exec sets it). The output of all of them
// goes to a file next to the Run's log, bounded like the log; the first
// failing command fails the Run, with the path of the file.
func (r run) toolSetup(ctx context.Context, g guest) (err error) {
	cfg := r.Base.Config
	var out *rotatingLog
	for _, name := range slices.Sorted(maps.Keys(cfg.Tool)) {
		for _, command := range cfg.Tool[name].Setup {
			if out == nil {
				if out, err = r.openSetupOutput(); err != nil {
					return
				}
				defer func() { _ = out.Close() }()
			}
			r.say("setup of tool %s: %s", name, agent.Sanitize(command))
			_, _ = fmt.Fprintf(out, "$ %s\n", command)
			execErr := g.Exec(ctx, bashCommand(command), out)
			if err = ctx.Err(); err != nil {
				return
			}
			if execErr != nil {
				_, _ = fmt.Fprintf(out, "\n%s\n", agent.Sanitize(execErr.Error()))
				err = fmt.Errorf("setup of tool %s failed: %s (output: %s)", name, agent.Sanitize(execErr.Error()), out.path)
				return
			}
		}
	}
	if out != nil {
		r.say("setup done (output: %s)", out.path)
	}
	return
}

// openSetupOutput creates <log>-setup.txt.
func (r run) openSetupOutput() (out *rotatingLog, err error) {
	out, err = newRotatingLog(strings.TrimSuffix(r.log.path, ".jsonl") + "-setup.txt")
	if err != nil {
		err = fmt.Errorf("setup output file: %w", err)
	}
	return
}
