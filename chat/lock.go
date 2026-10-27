package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const lockName = "lock"

// lockChat takes the exclusive flock of the Chat in its state directory dir
// and writes the PID of this process to the lock file, for the refusal of
// whoever comes next. The kernel drops the lock with the process, however it
// dies. A Chat already held gets an error naming the owner.
func lockChat(name, dir string) (unlock func(), err error) {
	path := filepath.Join(dir, lockName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		err = fmt.Errorf("opening the lock of Chat %s: %w", name, err)
		return
	}
	if lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockErr != nil {
		owner, _ := os.ReadFile(path)
		_ = file.Close()
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
			err = fmt.Errorf("locking Chat %s: %w", name, lockErr)
			return
		}
		err = fmt.Errorf("chat %s is in use by another afk process (PID %s)", name, strings.TrimSpace(string(owner)))
		return
	}
	if err = file.Truncate(0); err == nil {
		_, err = file.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	}
	if err != nil {
		_ = file.Close()
		err = fmt.Errorf("writing the lock of Chat %s: %w", name, err)
		return
	}
	unlock = func() { _ = file.Close() } // closing releases the lock
	return
}
