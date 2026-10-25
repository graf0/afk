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
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/graf0/afk/agent"
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
	Forge       *forge.GitHub // GitURL is where ticket-N is pushed
	AgentBinary string        // "claude" unless set (tests)
}

// run is the state of one Run.
type run struct {
	Options
	project    string
	stateDir   string // ~/.local/state/afk/<project>
	branch     string // ticket-N, in the guest and the Quarantine repo
	quarantine Quarantine
	log        *rotatingLog
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
	r := run{Options: opts, branch: fmt.Sprintf("ticket-%d", opts.Ticket.Number)}
	cfg := opts.Base.Config
	if r.project, err = image.ProjectName(cfg.Forge.Repo); err != nil {
		return
	}
	r.stateDir = filepath.Join(opts.Paths.StateDir, r.project)
	policy, err := egress.Allowlist(slices.Concat(cfg.Network.Allow, agent.Domains))
	if err != nil {
		err = fmt.Errorf("network.allow: %w", err)
		return
	}
	disk, err := image.LatestDisk(opts.Paths, r.project)
	if err != nil {
		return
	}
	signer, err := image.SSHSigner(opts.Paths)
	if err != nil {
		return
	}
	if r.quarantine, err = OpenQuarantine(ctx, filepath.Join(r.stateDir, QuarantineName)); err != nil {
		return
	}
	if err = r.quarantine.CheckNew(ctx, r.branch); err != nil {
		return
	}
	// Fail before the VM, not after an hour of work; Push checks again.
	if err = r.checkRemote(ctx); err != nil {
		return
	}
	if err = r.quarantine.FetchBase(ctx, opts.Base.Repo, opts.Base.Commit, r.branch); err != nil {
		return
	}
	logPath, err := r.openLog()
	if err != nil {
		return
	}
	defer func() { _ = r.log.Close() }()
	r.say("ticket #%d %s", opts.Ticket.Number, agent.Sanitize(opts.Ticket.Title))
	r.say("base origin/%s %s, log %s", opts.Base.Branch, opts.Base.Commit, logPath)

	res, err := r.session(ctx, policy, disk, signer)
	if err != nil {
		return
	}
	err = r.publish(ctx, res)
	return
}

// session runs the Agent in a fresh VM and, on done, takes its result through
// the gate and the review into the Quarantine repo. The VM is destroyed on success, kept on failure.
func (r run) session(ctx context.Context, policy egress.Policy, disk string, signer ssh.Signer) (res result, err error) {
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	m, err := conn.Up(ctx, machine.Spec{
		Project:  r.project,
		Kind:     machine.KindRun,
		ID:       fmt.Sprintf("%d-%s", r.Ticket.Number, hex.EncodeToString(id)),
		BaseDisk: disk,
	})
	if err != nil {
		return
	}
	r.say("VM %s started", m.Name)
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
	if client, err = m.SSH(sshCtx, signer); err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	// Ctrl-C ends every session at once; the VM then stays like on failure.
	defer context.AfterFunc(ctx, func() { _ = client.Close() })()
	stop, err := machine.ServeEgress(ctx, client, policy)
	if err != nil {
		return
	}
	defer stop()

	if err = r.prepareGuest(ctx, client); err != nil {
		return
	}
	res, err = r.work(ctx, sshGuest{run: r, client: client})
	return
}

// prepareGuest clones the base into guestRepo on the ticket branch, with the
// base as origin/<branch>, and writes the ticket next to it.
func (r run) prepareGuest(ctx context.Context, client *ssh.Client) (err error) {
	temp, err := os.CreateTemp(r.stateDir, "base-*.bundle")
	if err != nil {
		err = fmt.Errorf("creating base bundle: %w", err)
		return
	}
	_ = temp.Close()
	defer func() { _ = os.Remove(temp.Name()) }()
	// git replaces the file (lock file + rename): open it only afterwards.
	if err = r.quarantine.Bundle(ctx, r.branch, temp.Name()); err != nil {
		return
	}
	bundle, err := os.Open(temp.Name())
	if err != nil {
		err = fmt.Errorf("opening base bundle: %w", err)
		return
	}
	defer func() { _ = bundle.Close() }()
	q := agent.ShellQuote
	origin := originPrefix + r.Base.Branch
	script := strings.Join([]string{
		"set -eu",
		`sudo install -d -o "$(id -u)" -g "$(id -g)" ` + guestWork,
		"cat > " + guestBundle,
		"git init -q -b " + q(r.branch) + " " + guestRepo,
		"cd " + guestRepo,
		"git config user.name " + q(guestGitName),
		"git config user.email " + q(guestGitEmail),
		"git fetch -q --no-tags " + guestBundle + " " + q("+"+baseRef(r.branch)+":"+origin),
		"git checkout -q -B " + q(r.branch) + " " + q(origin),
		"rm " + guestBundle,
	}, "\n")
	if err = remote(ctx, client, script, bundle, nil); err != nil {
		err = fmt.Errorf("cloning the base in the guest: %w", err)
		return
	}
	if err = remote(ctx, client, "cat > "+guestTicket, strings.NewReader(agent.TicketFile(r.Ticket)), nil); err != nil {
		err = fmt.Errorf("writing the ticket in the guest: %w", err)
		return
	}
	return
}

// --- helpers ---

const (
	maxResultSize = 1 << 30 // bundle from the guest
	maxStderr     = 4 << 10 // guest stderr kept for errors
)

func (r run) prompt() (prompt string) { return agent.Prompt(r.task()) }

// openLog creates the Run's log for the raw agent stream.
func (r *run) openLog() (path string, err error) {
	dir := filepath.Join(r.stateDir, "logs")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", dir, err)
		return
	}
	path = filepath.Join(dir, fmt.Sprintf("%s-%s.jsonl", r.branch, time.Now().UTC().Format("20060102T150405Z")))
	r.log = &rotatingLog{path: path, max: logRotateSize}
	if r.log.file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
		err = fmt.Errorf("creating log: %w", err)
	}
	return
}

// say prints a line of afk itself, prefixed "= " like ralph.sh.
func (r run) say(format string, args ...any) {
	_, _ = fmt.Fprintf(r.Out, "= "+format+"\n", args...)
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
