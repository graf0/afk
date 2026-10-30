package chat

import (
	"context"
	"strings"
	"testing"
)

func TestShellRefusals(t *testing.T) {
	l := newLifecycle(t)
	ctx := context.Background()
	if err := Shell(ctx, l.opts); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a Chat that does not exist: err = %v", err)
	}
	// A Chat that exists but is not running gets no shell: that would start nothing, and a Stack is not started by accident.
	if _, err := l.enter(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Shell(ctx, l.opts); err == nil || !strings.Contains(err.Error(), "not running: start it with `afk chat plan`") {
		t.Errorf("a stopped Chat: err = %v", err)
	}
}
