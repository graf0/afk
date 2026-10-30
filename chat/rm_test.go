package chat

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRemoveRefusals(t *testing.T) {
	l := newLifecycle(t)
	ctx := context.Background()

	if err := Remove(ctx, l.opts); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a Chat that does not exist: err = %v", err)
	}
	l.opts.Name = "Bad Name"
	if err := Remove(ctx, l.opts); err == nil || !strings.Contains(err.Error(), "invalid Chat name") {
		t.Errorf("a bad name: err = %v", err)
	}

	// A running Chat is not removed: the lock of its process refuses.
	l.opts.Name = "plan"
	c, err := newChat(ctx, l.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(c.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockChat("plan", c.lock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = Remove(ctx, l.opts); err == nil || !strings.Contains(err.Error(), "in use by another afk process") {
		t.Errorf("a running Chat: err = %v", err)
	}
	if _, err = os.Stat(c.dir); err != nil {
		t.Errorf("the state of a running Chat is gone: %v", err)
	}
}
