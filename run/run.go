// Package run orchestrates a Run: one Ticket from a fresh Sandbox VM to a
// result in the Quarantine repo (docs/workflow.md).
package run

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/egress"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
)

const (
	// Paths in the guest: the clone, and the ticket next to it, outside.
	guestWork   = "/work"
	guestRepo   = guestWork + "/repo"
	guestTicket = guestWork + "/TICKET.md"
	guestSpec   = guestWork + "/SPEC.md"
	// guestStart is where the guest fetches the tip of a Spec's branch.
	guestStart  = "refs/afk/start"
	guestBundle = guestWork + "/base.bundle"

	// Commit identity in the guest; the Agent has no other.
	guestGitName  = "afk agent"
	guestGitEmail = "agent@afk.invalid"

	// QuarantineName is the Quarantine repo in a Project's state directory.
	QuarantineName = "quarantine.git"
)

// Options describe one Run.
type Options struct {
	Base        Base
	Ticket      forge.Ticket
	ClaudeToken string
	Paths       image.Paths
	Out         io.Writer
	Forge       forge.Forge // Remote is where ticket-N is pushed
	AgentBinary string      // "claude" unless set (tests)
	// Resume is the VM an earlier Factory left for this Ticket (or, in a
	// Spec, for one of its children or its review); the Run continues in it
	// instead of starting a fresh one (docs/workflow.md).
	Resume *Leftover
}

// vmSetup is what a Run needs to bring up a Sandbox VM.
type vmSetup struct {
	policy  egress.Policy
	disk    string
	sandbox config.Machine
	signer  ssh.Signer
}

// run is the state of one Run.
type run struct {
	Options
	project    string
	stateDir   string // ~/.local/state/afk/<project>
	branch     string // ticket-N, in the guest and the Quarantine repo
	quarantine Quarantine
	log        *rotatingLog
	denials    *denialLog // Egress proxy denials of the session, next to log
	id         string     // machine id of the VM, random when empty
	resumed    bool       // the guest holds the Agent's earlier work: the next prompt says so

	// tip is the end of the Spec's branch in the Quarantine repo: the commit
	// the Run continues from and the one Accept moves the branch from. Empty
	// for a Ticket outside a Spec and before the first child of a Spec.
	tip string
	// spec is the Spec of the Ticket, given to the Agent as context.
	spec *forge.Ticket
}

// newRun is the state of a Run of opts.Ticket with what its VMs need.
func newRun(ctx context.Context, opts Options) (r run, setup vmSetup, err error) {
	r = run{Options: opts, branch: fmt.Sprintf("ticket-%d", opts.Ticket.Number)}
	cfg := opts.Base.Config
	if r.project, err = image.ProjectName(cfg.Forge.Repo); err != nil {
		return
	}
	r.stateDir = filepath.Join(opts.Paths.StateDir, r.project)
	if setup.policy, err = EgressPolicy(cfg); err != nil {
		return
	}
	setup.sandbox = cfg.Machine.Sandbox
	if setup.disk, err = image.LatestDisk(opts.Paths, r.project, config.SandboxMachine); err != nil {
		return
	}
	if setup.signer, err = image.SSHSigner(opts.Paths); err != nil {
		return
	}
	r.quarantine, err = OpenQuarantine(ctx, filepath.Join(r.stateDir, QuarantineName))
	return
}

// --- run ---

// Execute runs opts.Ticket in a fresh Sandbox VM, imports the result into
// the Quarantine repo as refs/heads/ticket-N (validated commits on the base:
// the implementation squashed into one and, when the review fixed anything,
// a second one) and publishes it on the Forge: push, pull request, reports,
// labels. The gate and the review run before that (work.go).
// needs_info goes to the ticket instead. The branch must exist neither in
// the Quarantine repo nor on the Forge. On success and on needs_info the VM
// is destroyed before the Forge is written; on any failure in the VM (and
// Ctrl-C) it stays for `afk vm shell` / `afk vm kill`.
func Execute(ctx context.Context, opts Options) (err error) {
	if opts.Ticket.HasLabel(opts.Base.Config.Labels.Spec) {
		err = fmt.Errorf("ticket #%d is a Spec: run it with ExecuteSpec", opts.Ticket.Number)
		return
	}
	r, setup, err := newRun(ctx, opts)
	if err != nil {
		return
	}
	if err = r.quarantine.CheckNew(ctx, r.branch); err != nil {
		return
	}
	// Fail before the VM, not after an hour of work; Push checks again.
	if err = r.checkRemote(ctx); err != nil {
		return
	}
	if opts.Resume != nil {
		// The guest cloned the base of the earlier Run, not today's.
		if r.Base.Commit, err = r.quarantine.Base(ctx, r.branch); err != nil {
			return
		}
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

	res, err := r.enter(ctx, setup, r.work)
	if err != nil {
		return
	}
	err = r.publish(ctx, res)
	return
}

// enter is session in the VM of opts.Resume when there is one, else in a fresh VM.
func (r run) enter(ctx context.Context, setup vmSetup, work workFunc) (res result, err error) {
	if r.Resume == nil {
		res, err = r.session(ctx, setup, work)
		return
	}
	res, err = r.resume(ctx, setup, *r.Resume, work)
	return
}

// session runs the Agent in a fresh VM and, on done, calls work with the
// prepared guest. The VM is destroyed on success, kept on failure.
func (r run) session(ctx context.Context, setup vmSetup, work workFunc) (res result, err error) {
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	id := r.id
	if id == "" {
		random := make([]byte, 4)
		_, _ = rand.Read(random)
		id = fmt.Sprintf("%d-%s", r.Ticket.Number, hex.EncodeToString(random))
	}
	m, err := conn.Up(ctx, machine.Spec{
		Project:   r.project,
		Kind:      machine.KindRun,
		ID:        id,
		BaseDisk:  setup.disk,
		MemoryMiB: setup.sandbox.MemoryMiB,
		CPUs:      setup.sandbox.CPUs,
	})
	if err != nil {
		return
	}
	r.say("VM %s started", m.Name)
	res, err = r.inVM(ctx, conn, m, setup, true, work)
	return
}

// resume continues the Run of leftover in its VM, which holds the clone of
// the earlier Run with the Agent's work: nothing is prepared, the prompt of
// the first session says so.
func (r run) resume(ctx context.Context, setup vmSetup, leftover Leftover, work workFunc) (res result, err error) {
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	m, err := conn.Get(leftover.Machine)
	if err != nil {
		return
	}
	r.say("resuming in VM %s", m.Name)
	r.resumed = true
	res, err = r.inVM(ctx, conn, m, setup, false, work)
	return
}

// inVM runs work in the guest of m over ssh, after prepareGuest and the
// Tools' setup when prepare is set. The VM is destroyed on success, kept on
// failure.
func (r run) inVM(ctx context.Context, conn *machine.Conn, m machine.Machine, setup vmSetup, prepare bool, work workFunc) (res result, err error) {
	var client *ssh.Client
	defer func() {
		if err != nil {
			r.say("Run failed; VM %s stays for inspection:", m.Name)
			r.say("  afk vm shell %s", m.Name)
			r.say("  afk vm kill %s", m.Name)
			return
		}
		_ = client.Close()
		if err = conn.Kill(m.Name); err == nil {
			r.say("VM %s destroyed", m.Name)
		}
	}()
	sshCtx, cancel := context.WithTimeout(ctx, machine.SSHTimeout)
	defer cancel()
	if client, err = m.SSH(sshCtx, setup.signer); err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	// Ctrl-C ends every session at once; the VM then stays like on failure.
	defer context.AfterFunc(ctx, func() { _ = client.Close() })()
	stop, err := machine.ServeEgress(ctx, client, setup.policy, r.denials)
	if err != nil {
		return
	}
	defer stop()

	work = r.prepared(prepare, work)
	if prepare {
		err = r.prepareGuest(ctx, client)
	} else {
		// The ticket may have been edited since; the clone stays as it is.
		err = r.writeTicketFiles(ctx, client)
	}
	if err != nil {
		return
	}
	res, err = work(ctx, sshGuest{run: r, client: client, env: r.Base.Config.ToolEnv()})
	return
}

// prepared is work as the guest of inVM runs it: after the Tools' setup in a
// fresh VM, which follows the clone; as it is in a resumed one, whose state
// the Agent has worked on.
func (r run) prepared(prepare bool, work workFunc) (prepared workFunc) {
	if prepare {
		return r.withSetup(work)
	}
	return work
}

// prepareGuest clones the base into guestRepo on the ticket branch, with the
// base as origin/<branch>, and writes the ticket next to it. Inside a Spec
// the branch starts at its tip instead, and the Spec is written too.
func (r run) prepareGuest(ctx context.Context, client *ssh.Client) (err error) {
	if err = r.guestBranch().Clone(ctx, client, r.Base.Branch, r.start() != r.Base.Commit); err != nil {
		return
	}
	err = r.writeTicketFiles(ctx, client)
	return
}

// guestBranch is the Run's branch in the Quarantine repo.
func (r run) guestBranch() (b GuestBranch) {
	return GuestBranch{Quarantine: r.quarantine, Name: r.branch, TempDir: r.stateDir}
}

// writeTicketFiles writes the ticket, and the Spec when there is one, next to the clone.
func (r run) writeTicketFiles(ctx context.Context, client *ssh.Client) (err error) {
	if err = remote(ctx, client, "cat > "+guestTicket, strings.NewReader(agent.TicketFile(r.Ticket)), nil); err != nil {
		err = fmt.Errorf("writing the ticket in the guest: %w", err)
		return
	}
	if r.spec == nil {
		return
	}
	if err = remote(ctx, client, "cat > "+guestSpec, strings.NewReader(agent.TicketFile(*r.spec)), nil); err != nil {
		err = fmt.Errorf("writing the Spec in the guest: %w", err)
	}
	return
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
	r.denials = &denialLog{path: strings.TrimSuffix(path, ".jsonl") + "-egress.txt"}
	if r.log.file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
		err = fmt.Errorf("creating log: %w", err)
	}
	return
}

// closeLogs closes the logs of the session and, when the Egress proxy denied
// anything, says how many times and where the denials are.
func (r run) closeLogs() {
	_ = r.log.Close()
	if denied, _ := r.denials.Close(); denied > 0 {
		r.say("egress: %d denied, see %s", denied, r.denials.path)
	}
}

// say prints a line of afk itself, prefixed "= " like ralph.sh.
func (r run) say(format string, args ...any) { say(r.Out, format, args...) }

// say prints a line of afk itself to w.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "= "+format+"\n", args...)
}

// remote runs command in the guest; its stderr (sanitized, truncated) goes
// into the error. Cancelling ctx closes the session.
func remote(ctx context.Context, client *ssh.Client, command string, stdin io.Reader, stdout io.Writer) (err error) {
	session, err := client.NewSession()
	if err != nil {
		err = fmt.Errorf("opening ssh session: %w", err)
		return
	}
	defer func() { _ = session.Close() }()
	defer context.AfterFunc(ctx, func() { _ = session.Close() })()
	var stderr tailBuffer
	session.Stdin, session.Stdout, session.Stderr = stdin, stdout, &stderr
	if err = session.Run(command); err != nil {
		// Both the exit message and stderr come from the guest.
		err = fmt.Errorf("%s: %s", agent.Sanitize(err.Error()), agent.Sanitize(strings.TrimSpace(stderr.String())))
	}
	return
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
// first moves it to path.1 (replacing the previous one). The guest can't fill
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

// denialLog is the file of the Egress proxy's denials, one line per Write.
// The file is created by the first denial: a Run without any leaves none.
type denialLog struct {
	path string

	mu    sync.Mutex
	out   *rotatingLog // bounded like the log: the guest decides how many it causes
	count int
}

func (l *denialLog) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out == nil {
		if l.out, err = newRotatingLog(l.path); err != nil {
			return
		}
	}
	if n, err = l.out.Write(p); err == nil {
		l.count++
	}
	return
}

// Close closes the file and returns the number of denials written.
func (l *denialLog) Close() (denied int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	denied = l.count
	if l.out != nil {
		err = l.out.Close()
	}
	return
}
