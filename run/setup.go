package run

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
)

// SetupTools runs the setup commands of the Tools in the clone of a Chat's guest, Tools in name
// order, with their env (exec sets it). The output of all of them goes to
// <logPath minus .jsonl>-setup.txt, bounded like the log; the first failing
// command fails it, with the path of the file. Progress lines go to out.
func SetupTools(ctx context.Context, exec ExecFunc, cfg config.Config, logPath string, out io.Writer) (err error) {
	var setupLog *rotatingLog
	for _, name := range slices.Sorted(maps.Keys(cfg.Tool)) {
		for _, command := range cfg.Tool[name].Setup {
			if setupLog == nil {
				setupLog, err = newRotatingLog(strings.TrimSuffix(logPath, ".jsonl") + "-setup.txt")
				if err != nil {
					err = fmt.Errorf("setup output file: %w", err)
					return
				}
				defer func() { _ = setupLog.Close() }()
			}
			say(out, "setup of tool %s: %s", name, agent.Sanitize(command))
			_, _ = fmt.Fprintf(setupLog, "$ %s\n", command)
			execErr := exec(ctx, bashCommand(command), setupLog)
			if err = ctx.Err(); err != nil {
				return
			}
			if execErr != nil {
				_, _ = fmt.Fprintf(setupLog, "\n%s\n", agent.Sanitize(execErr.Error()))
				err = fmt.Errorf("setup of tool %s failed: %s (output: %s)", name, agent.Sanitize(execErr.Error()), setupLog.path)
				return
			}
		}
	}
	if setupLog != nil {
		say(out, "setup done (output: %s)", setupLog.path)
	}
	return
}
