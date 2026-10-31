package chat

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// vmLocks are the claims of one Chat on test VMs. A claim is an exclusive
// flock on a file per libvirt domain in a directory all `afk chat` processes
// of the host share, so another process sees it, and the kernel drops it when
// the process dies. The file names the owner for the list; whether anybody
// holds the VM is the flock alone, never the file.
type vmLocks struct {
	dir   string
	owner string // "<project>/<chat>": a domain is the host's, Chat names are the Project's
	mu    sync.Mutex
	held  map[string]*os.File // domain -> locked file
}

// vmBusyError is returned by claim and release for a VM another Chat holds.
type vmBusyError struct{ vm, owner string }

func (e vmBusyError) Error() (text string) {
	return fmt.Sprintf("VM %s is held by Chat %s", e.vm, e.owner)
}

func newVMLocks(dir, owner string) (locks *vmLocks) {
	return &vmLocks{dir: dir, owner: owner, held: make(map[string]*os.File)}
}

func (l *vmLocks) path(vm string) (path string) {
	return filepath.Join(l.dir, "vm-"+url.PathEscape(vm)+".lock")
}

// claim takes vm for this Chat; one it holds already stays held.
func (l *vmLocks) claim(vm string) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[vm] != nil {
		return
	}
	file, locked, err := l.tryLock(vm)
	if err != nil {
		return
	}
	if !locked {
		err = vmBusyError{vm: vm, owner: l.ownerName(vm)}
		return
	}
	if err = file.Truncate(0); err == nil {
		_, err = fmt.Fprintf(file, "%s\n", l.owner)
	}
	if err != nil {
		_ = file.Close()
		err = fmt.Errorf("writing the lock of VM %s: %w", vm, err)
		return
	}
	l.held[vm] = file
	return
}

// tryLock opens the lock file of vm and takes its flock without waiting. When
// another process holds it, locked is false and the file is closed.
func (l *vmLocks) tryLock(vm string) (file *os.File, locked bool, err error) {
	if err = os.MkdirAll(l.dir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", l.dir, err)
		return
	}
	if file, err = os.OpenFile(l.path(vm), os.O_CREATE|os.O_RDWR, 0o600); err != nil {
		err = fmt.Errorf("opening the lock of VM %s: %w", vm, err)
		return
	}
	lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case lockErr == nil:
		locked = true
	case errors.Is(lockErr, syscall.EWOULDBLOCK):
		_ = file.Close()
		file = nil
	default:
		_ = file.Close()
		file = nil
		err = fmt.Errorf("locking VM %s: %w", vm, lockErr)
	}
	return
}

// release lets go of vm; held reports whether this Chat had it. A VM another
// Chat holds is vmBusyError.
func (l *vmLocks) release(vm string) (held bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[vm] != nil {
		l.drop(vm)
		held = true
		return
	}
	if owner, ok := l.holder(vm); ok {
		err = vmBusyError{vm: vm, owner: owner}
	}
	return
}

// drop ends the claim on vm: closing the file releases the flock.
func (l *vmLocks) drop(vm string) {
	file := l.held[vm]
	_ = file.Truncate(0)
	_ = file.Close()
	delete(l.held, vm)
}

// holder is the Chat that holds vm and whether any does, as the flock tells:
// a free one is taken and given back at once. Callers hold l.mu.
func (l *vmLocks) holder(vm string) (owner string, ok bool) {
	if l.held[vm] != nil {
		return l.owner, true
	}
	file, locked, err := l.tryLock(vm)
	if locked {
		_ = file.Close()
		return
	}
	if err != nil {
		return "?", true // unknown: better busy than two Chats on one VM in the list
	}
	return l.ownerName(vm), true
}

// ownerName is the owner a held lock file names.
func (l *vmLocks) ownerName(vm string) (owner string) {
	data, _ := os.ReadFile(l.path(vm))
	if owner = strings.TrimSpace(string(data)); owner == "" {
		owner = "?"
	}
	return
}

// ownerOf is holder under the lock of l: for callers outside.
func (l *vmLocks) ownerOf(vm string) (owner string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.holder(vm)
}

// close releases every claim: the end of the Chat.
func (l *vmLocks) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for vm := range l.held {
		l.drop(vm)
	}
}
