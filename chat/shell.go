package chat

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/graf0/afk/stack"
)

// Shell opens bash in the Sandbox of the running Chat opts.Name, beside its
// session. A Chat that does not run is an error: a shell does not start a Stack
// (that is `afk chat <name>`). It takes no lock. Only opts.Base, Name, StateDir,
// In, Out and Err are used.
func Shell(ctx context.Context, opts Options) (err error) {
	c, err := existing(ctx, opts)
	if err != nil {
		return
	}
	if !running(c.dir) {
		err = fmt.Errorf("chat %s is not running: start it with `afk chat %s`", opts.Name, opts.Name)
		return
	}
	st, err := stack.Open(opts.StateDir, c.project, stack.Chat, opts.Name)
	if err != nil {
		return
	}
	err = st.Attach(ctx, `export PATH="$HOME/bin:$PATH"; exec bash`, opts.In, opts.Out, opts.Err)
	// What the shell last ran is its exit status: no failure of afk.
	if exitErr := (&exec.ExitError{}); errors.As(err, &exitErr) {
		err = nil
	}
	return
}
