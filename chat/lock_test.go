package chat

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestLockChat(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockChat("x", dir)
	if err != nil {
		t.Fatal(err)
	}
	// The second holder is refused with the PID of the first.
	_, err = lockChat("x", dir)
	if err == nil || !strings.Contains(err.Error(), "PID "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("second lock: err = %v, want a refusal naming PID %d", err, os.Getpid())
	}
	unlock()
	unlock, err = lockChat("x", dir)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock()
}
