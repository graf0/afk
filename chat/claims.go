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

// Kinds of claims: what a claimed name is, also the prefix of its lock file.
const (
	claimVM   = "vm"   // a libvirt domain
	claimPort = "port" // a host port of [expose]
)

// claims are the claims of one Chat on things of the host (test VMs, published
// ports) only one Chat at a time may have. A claim is an exclusive flock on a
// file per name in a directory all `afk chat` processes of the host share, so
// another process sees it, and the kernel drops it when the process dies. The
// file names the owner for the list; whether anybody holds the name is the
// flock alone, never the file.
type claims struct {
	dir   string
	kind  string // claimVM or claimPort
	owner string // "<project>/<chat>": the things are the host's, Chat names are the Project's
	mu    sync.Mutex
	held  map[string]*os.File // name -> locked file
}

// busyError is returned by claim and release for a name another Chat holds.
type busyError struct{ kind, name, owner string }

func (e busyError) Error() (text string) {
	kind := e.kind
	if kind == claimVM {
		kind = "VM"
	}
	return fmt.Sprintf("%s %s is held by Chat %s", kind, e.name, e.owner)
}

func newClaims(dir, kind, owner string) (c *claims) {
	return &claims{dir: dir, kind: kind, owner: owner, held: make(map[string]*os.File)}
}

func (cl *claims) path(name string) (path string) {
	return filepath.Join(cl.dir, cl.kind+"-"+url.PathEscape(name)+".lock")
}

// claim takes name for this Chat; one it holds already stays held.
func (cl *claims) claim(name string) (err error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.held[name] != nil {
		return
	}
	file, locked, err := cl.tryLock(name)
	if err != nil {
		return
	}
	if !locked {
		err = busyError{kind: cl.kind, name: name, owner: cl.ownerName(name)}
		return
	}
	if err = file.Truncate(0); err == nil {
		_, err = fmt.Fprintf(file, "%s\n", cl.owner)
	}
	if err != nil {
		_ = file.Close()
		err = fmt.Errorf("writing the lock of %s %s: %w", cl.kind, name, err)
		return
	}
	cl.held[name] = file
	return
}

// tryLock opens the lock file of name and takes its flock without waiting.
// When another process holds it, locked is false and the file is closed.
func (cl *claims) tryLock(name string) (file *os.File, locked bool, err error) {
	if err = os.MkdirAll(cl.dir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", cl.dir, err)
		return
	}
	if file, err = os.OpenFile(cl.path(name), os.O_CREATE|os.O_RDWR, 0o600); err != nil {
		err = fmt.Errorf("opening the lock of %s %s: %w", cl.kind, name, err)
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
		err = fmt.Errorf("locking %s %s: %w", cl.kind, name, lockErr)
	}
	return
}

// release lets go of name; held reports whether this Chat had it. A name
// another Chat holds is busyError.
func (cl *claims) release(name string) (held bool, err error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.held[name] != nil {
		cl.drop(name)
		held = true
		return
	}
	if owner, ok := cl.holder(name); ok {
		err = busyError{kind: cl.kind, name: name, owner: owner}
	}
	return
}

// drop ends the claim on name: closing the file releases the flock.
func (cl *claims) drop(name string) {
	file := cl.held[name]
	_ = file.Truncate(0)
	_ = file.Close()
	delete(cl.held, name)
}

// holder is the Chat that holds name and whether any does, as the flock
// tells: a free one is taken and given back at once. Callers hold cl.mu.
func (cl *claims) holder(name string) (owner string, ok bool) {
	if cl.held[name] != nil {
		return cl.owner, true
	}
	file, locked, err := cl.tryLock(name)
	if locked {
		_ = file.Close()
		return
	}
	if err != nil {
		return "?", true // unknown: better busy than two Chats on one name in the list
	}
	return cl.ownerName(name), true
}

// ownerName is the owner a held lock file names.
func (cl *claims) ownerName(name string) (owner string) {
	data, _ := os.ReadFile(cl.path(name))
	if owner = strings.TrimSpace(string(data)); owner == "" {
		owner = "?"
	}
	return
}

// ownerOf is holder under the lock of cl: for callers outside.
func (cl *claims) ownerOf(name string) (owner string, ok bool) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.holder(name)
}

// close releases every claim: the end of the Chat.
func (cl *claims) close() {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	for name := range cl.held {
		cl.drop(name)
	}
}
