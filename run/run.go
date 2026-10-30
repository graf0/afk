// Package run orchestrates a Run: one Ticket from a fresh Sandbox to a
// result in the Quarantine repo (docs/workflow.md).
package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/stack"
)

const (
	// guestStart is where the clone fetches the tip of a Spec's branch.
	guestStart = "refs/afk/start"

	// QuarantineName is the Quarantine repo in a Project's state directory.
	QuarantineName = "quarantine.git"
)

// Options describe one Run.
type Options struct {
	Base        Base
	Ticket      forge.Ticket
	ClaudeToken string
	StateDir    string // ~/.local/state/afk
	Out         io.Writer
	Forge       forge.Forge // Remote is where ticket-N is pushed
	// AgentBinary is the Agent's command in the Sandbox, a path there or
	// relative to the clone: the host's claude (mounted read-only) unless
	// set (tests).
	AgentBinary string
	// Resume is the state an earlier Factory left for this Ticket (or, in a
	// Spec, for one of its children or its review); the Run continues in it
	// instead of starting a fresh clone (docs/workflow.md).
	Resume *Leftover
}

// run is the state of one Run.
type run struct {
	Options
	project    string
	stateDir   string // ~/.local/state/afk/<project>
	branch     string // ticket-N, in the Sandbox's clone and the Quarantine repo
	quarantine Quarantine
	log        *rotatingLog
	id         string // name of the Stack, ticket-N when empty
	resumed    bool   // the clone holds the Agent's earlier work: the next prompt says so

	// tip is the end of the Spec's branch in the Quarantine repo: the commit
	// the Run continues from and the one Accept moves the branch from. Empty
	// for a Ticket outside a Spec and before the first child of a Spec.
	tip string
	// spec is the Spec of the Ticket, given to the Agent as context.
	spec *forge.Ticket
	// stackFiles holds the Dockerfiles of the Stack, known once it is up
	// (shared by the copies of the run, as the Stack's state is).
	stackFiles *stackFiles
}

// stackFiles are the protected paths a Stack adds to [protect] (stack.Protected).
type stackFiles struct{ paths []string }

// protected is every path the result must not change: [protect], plus the
// Dockerfiles of the Stack once it is up.
func (r run) protected() (paths []string) {
	paths = r.Base.Config.Protect.Paths
	if r.stackFiles != nil {
		paths = slices.Concat(paths, r.stackFiles.paths)
	}
	return
}

// newRun is the state of a Run of opts.Ticket.
func newRun(ctx context.Context, opts Options) (r run, err error) {
	r = run{Options: opts, branch: ticketName(opts.Ticket.Number), stackFiles: &stackFiles{}}
	if r.project, err = stack.ProjectName(opts.Base.Config.Forge.Repo); err != nil {
		return
	}
	r.stateDir = filepath.Join(opts.StateDir, r.project)
	r.quarantine, err = OpenQuarantine(ctx, filepath.Join(r.stateDir, QuarantineName))
	return
}

// --- run ---

// Execute runs opts.Ticket in the Sandbox of a fresh Stack, imports the result
// into the Quarantine repo as refs/heads/ticket-N (validated commits on the
// base: the implementation squashed into one and, when the review fixed
// anything, a second one) and publishes it on the Forge: push, pull request,
// reports, labels. The gate and the review run before that (work.go).
// needs_info goes to the ticket instead. The branch must exist neither in
// the Quarantine repo nor on the Forge. On success and on needs_info the
// Stack and its state are removed before the Forge is written; on any failure
// (and Ctrl-C) the Stack goes down and its state stays, for `afk factory` to
// continue.
func Execute(ctx context.Context, opts Options) (err error) {
	if opts.Ticket.HasLabel(opts.Base.Config.Labels.Spec) {
		err = fmt.Errorf("ticket #%d is a Spec: run it with ExecuteSpec", opts.Ticket.Number)
		return
	}
	r, err := newRun(ctx, opts)
	if err != nil {
		return
	}
	if err = r.quarantine.CheckNew(ctx, r.branch); err != nil {
		return
	}
	// Fail before the Stack, not after an hour of work; Push checks again.
	if err = r.checkRemote(ctx); err != nil {
		return
	}
	if opts.Resume != nil {
		// The clone was made from the base of the earlier Run, not today's.
		if r.Base.Commit, err = r.quarantine.Base(ctx, r.branch); err != nil {
			return
		}
		r.resumed = true
	} else if err = r.quarantine.FetchBase(ctx, opts.Base.Repo, opts.Base.Commit, r.branch); err != nil {
		return
	}
	logPath, err := r.openLog(r.branch)
	if err != nil {
		return
	}
	defer r.closeLogs()
	r.say("ticket #%d %s", opts.Ticket.Number, agent.Sanitize(opts.Ticket.Title))
	r.say("base origin/%s %s, log %s", opts.Base.Branch, opts.Base.Commit, logPath)

	res, err := r.enter(ctx, r.work)
	if err != nil {
		return
	}
	err = r.publish(ctx, res)
	return
}

// guestBranch is the Run's branch in the Quarantine repo.
func (r run) guestBranch() (b GuestBranch) {
	return GuestBranch{Quarantine: r.quarantine, Name: r.branch, TempDir: r.stateDir}
}

// --- helpers ---

const (
	maxResultSize = 1 << 30 // bundle from the guest
	maxStderr     = 4 << 10 // guest stderr kept for errors
)

func (r run) prompt() (prompt string) { return agent.Prompt(r.task()) }

// openLog creates the log for the raw agent stream of a session, named after name.
func (r *run) openLog(name string) (path string, err error) {
	dir := filepath.Join(r.stateDir, "logs")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", dir, err)
		return
	}
	path = filepath.Join(dir, fmt.Sprintf("%s-%s.jsonl", name, time.Now().UTC().Format("20060102T150405Z")))
	r.log = &rotatingLog{path: path, max: logRotateSize}
	if r.log.file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
		err = fmt.Errorf("creating log: %w", err)
	}
	return
}

// closeLogs closes the log of the session.
func (r run) closeLogs() { _ = r.log.Close() }

// say prints a line of afk itself, prefixed "= " like ralph.sh.
func (r run) say(format string, args ...any) { say(r.Out, format, args...) }

// say prints a line of afk itself to w.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "= "+format+"\n", args...)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	data []byte
	max  int // maxStderr when zero
}

func (b *tailBuffer) Write(p []byte) (n int, err error) {
	limit := cmp.Or(b.max, maxStderr)
	b.data = append(b.data, p...)
	if len(b.data) > limit {
		b.data = b.data[len(b.data)-limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() (s string) { return string(b.data) }

// logRotateSize is the size at which the Run's log moves to <path>.1.
const logRotateSize = 128 << 20

// rotatingLog writes to path; a write that would take it past max bytes
// first moves it to path.1 (replacing the previous one). The Sandbox can't fill
// the host's disk (at most ~2×max) and a long Run is never stopped for its
// log; the oldest part of the stream is lost instead.
type rotatingLog struct {
	path string
	max  int64
	file *os.File
	size int64
}

// newRotatingLog creates the file at path, which must not exist, as a log
// bounded by logRotateSize.
func newRotatingLog(path string) (l *rotatingLog, err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		err = fmt.Errorf("creating %s: %w", filepath.Base(path), err)
		return
	}
	l = &rotatingLog{path: path, max: logRotateSize, file: file}
	return
}

func (l *rotatingLog) Write(p []byte) (n int, err error) {
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		if err = l.rotate(); err != nil {
			return
		}
	}
	n, err = l.file.Write(p)
	l.size += int64(n)
	return
}

func (l *rotatingLog) rotate() (err error) {
	if err = l.file.Close(); err != nil {
		err = fmt.Errorf("closing log: %w", err)
		return
	}
	if err = os.Rename(l.path, l.path+".1"); err != nil {
		err = fmt.Errorf("rotating log: %w", err)
		return
	}
	if l.file, err = os.OpenFile(l.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
		err = fmt.Errorf("creating log: %w", err)
		return
	}
	l.size = 0
	return
}

func (l *rotatingLog) Close() (err error) { return l.file.Close() }

// limitWriter fails once more than left bytes are written.
type limitWriter struct {
	w    io.Writer
	left int64
}

func (l *limitWriter) Write(p []byte) (n int, err error) {
	if int64(len(p)) > l.left {
		err = errors.New("output over the size limit")
		return
	}
	n, err = l.w.Write(p)
	l.left -= int64(n)
	return
}
