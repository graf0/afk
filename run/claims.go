package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// Claims reserve tickets for one Factory among those of a Project: an
// flock on a file per ticket in Dir (claims/<project> in the state
// directory). The kernel drops the lock when the process dies, however it
// dies, so a claim never outlives its Factory.
type Claims struct {
	Dir string
}

// Claim reserves all numbers or none: ok is false when another Factory
// (or this one) holds any of them. release gives them back; the files stay,
// as removing one could race with a claim of it.
func (c Claims) Claim(numbers ...int) (release func(), ok bool, err error) {
	keys := make([]string, len(numbers))
	for i, n := range numbers {
		keys[i] = strconv.Itoa(n)
	}
	return c.ClaimKeys(keys...)
}

// ClaimKeys is Claim for things other than tickets, named by keys (the
// preflight): one file each, like a ticket.
func (c Claims) ClaimKeys(keys ...string) (release func(), ok bool, err error) {
	if err = os.MkdirAll(c.Dir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", c.Dir, err)
		return
	}
	var held []*os.File
	release = func() {
		for _, file := range held {
			_ = file.Close() // closing releases the lock
		}
	}
	defer func() {
		if !ok {
			release()
			release = func() {}
		}
	}()
	for _, key := range keys {
		var file *os.File
		if file, err = os.OpenFile(filepath.Join(c.Dir, key), os.O_CREATE|os.O_RDWR, 0o600); err != nil {
			err = fmt.Errorf("opening the claim %s: %w", key, err)
			return
		}
		held = append(held, file)
		if lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockErr != nil {
			if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
				err = fmt.Errorf("claiming %s: %w", key, lockErr)
			}
			return
		}
	}
	ok = true
	return
}
