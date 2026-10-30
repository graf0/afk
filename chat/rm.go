package chat

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/graf0/afk/stack"
)

// existing is the Chat opts.Name, which must have a state directory.
func existing(ctx context.Context, opts Options) (c *chat, err error) {
	if c, err = newChat(ctx, opts); err != nil {
		return
	}
	if _, err = os.Stat(c.dir); errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("chat %s does not exist (see `afk chat ls`)", opts.Name)
	} else if err != nil {
		err = fmt.Errorf("reading the state of Chat %s: %w", opts.Name, err)
	}
	return
}

// Remove destroys the Chat opts.Name: its Stack with the volumes, the clone,
// the home of the Agent and the Chat's refs in the Quarantine repo. A branch
// on the Forge stays. It takes the Chat's lock, so a running Chat refuses it.
// Only opts.Base, Name, StateDir and Out are used.
func Remove(ctx context.Context, opts Options) (err error) {
	c, err := existing(ctx, opts)
	if err != nil {
		return
	}
	unlock, err := lockChat(opts.Name, c.lock)
	if err != nil {
		return
	}
	defer unlock()
	st, err := stack.Open(opts.StateDir, c.project, stack.Chat, opts.Name)
	if err != nil {
		return
	}
	if err = c.discard(ctx, st); err != nil {
		err = fmt.Errorf("removing chat %s: %w", opts.Name, err)
		return
	}
	c.say("Chat %s removed (branch %s on the Forge, if pushed, stays)", opts.Name, c.branch)
	return
}
