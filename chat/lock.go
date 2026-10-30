package chat

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// lockPath is the lock file of the Chat whose state directory is dir: beside
// it, so that whoever holds the lock decides alone whether the Chat exists.
func lockPath(dir string) (path string) { return dir + ".lock" }

// lockChat takes the exclusive flock on the file path and writes the PID of
// this process to it, for the refusal of whoever comes next. The kernel drops
// the lock with the process, however it dies. A Chat already held gets an
// error naming the owner.
func lockChat(name, path string) (unlock func(), err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		err = fmt.Errorf("creating the lock of Chat %s: %w", name, err)
		return
	}
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

// running reports whether a process serves the channel of the Chat whose
// state directory is dir: the Chat is running, its Stack lives with that
// process. The probe takes no lock, so it never makes a starting afk see its
// Chat as in use; a socket left by a process that died refuses the connection.
func running(dir string) (ok bool) {
	socket, err := socketPath(dir)
	if err != nil {
		return
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return
	}
	_ = conn.Close()
	return true
}
