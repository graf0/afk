// Package run orchestrates a Run: one Ticket from a fresh Sandbox VM to a
// result in the Quarantine repo (docs/workflow.md).
package run

import (
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

	sshTimeout    = 2 * time.Minute
	maxResultSize = 1 << 30 // bundle from the guest
	maxStderr     = 4 << 10 // guest stderr kept for errors
)

// Options describe one Run.
type Options struct {
	Base        Base
	Ticket      forge.Ticket
	ClaudeToken string
	Paths       image.Paths
	Out         io.Writer
	AgentBinary string // "claude" unless set (tests)
}

// run is the state of one Run.
type run struct {
	Options
	project    string
	stateDir   string // ~/.local/state/afk/<project>
	branch     string // ticket-N, in the guest and the Quarantine repo
	quarantine Quarantine
	log        *os.File
}

// --- run ---

// Execute runs opts.Ticket in a fresh Sandbox VM and imports the result into
// the Quarantine repo, as refs/heads/ticket-N once validated. Nothing is
// written to the Forge. On success and on needs_info the VM is destroyed; on
// failure (and Ctrl-C) it stays for `afk vm shell` / `afk vm kill`.
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
	if err = r.quarantine.FetchBase(ctx, opts.Base.Repo, opts.Base.Commit, r.baseRef()); err != nil {
		return
	}
	logPath, err := r.openLog()
	if err != nil {
		return
	}
	defer func() { _ = r.log.Close() }()
	r.say("ticket #%d %s", opts.Ticket.Number, agent.Sanitize(opts.Ticket.Title))
	r.say("base origin/%s %s, log %s", opts.Base.Branch, opts.Base.Commit, logPath)

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
		ID:       fmt.Sprintf("%d-%s", opts.Ticket.Number, hex.EncodeToString(id)),
		BaseDisk: disk,
	})
	if err != nil {
		return
	}
	r.say("VM %s started", m.Name)
	sshCtx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	client, err := m.SSH(sshCtx, signer)
	if err != nil {
		if killErr := conn.Kill(m.Name); killErr != nil {
			err = errors.Join(err, killErr)
		}
		return
	}
	defer func() { _ = client.Close() }()
	// Ctrl-C ends every session at once; the VM then stays like on failure.
	defer context.AfterFunc(ctx, func() { _ = client.Close() })()
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
	stop, err := machine.ServeEgress(ctx, client, policy)
	if err != nil {
		return
	}
	defer stop()

	if err = r.prepareGuest(ctx, client); err != nil {
		return
	}
	r.say("agent started")
	report, err := agent.Session{
		Token:  opts.ClaudeToken,
		Prompt: r.prompt(),
		Dir:    guestRepo,
		Model:  cfg.Agent.Model,
		Proxy:  "http://" + machine.ProxyAddr,
		Binary: opts.AgentBinary,
	}.Run(ctx, client, opts.Out, r.log)
	if err != nil {
		err = fmt.Errorf("%w (log: %s)", err, logPath)
		return
	}
	r.say("agent report:")
	_, _ = fmt.Fprint(opts.Out, report.String())
	if report.Status == agent.StatusNeedsInfo {
		r.say("needs_info: no result taken; the questions are not posted to the ticket yet")
		return
	}
	err = r.takeResult(ctx, client)
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
	if err = r.quarantine.Bundle(ctx, r.baseRef(), temp.Name()); err != nil {
		return
	}
	bundle, err := os.Open(temp.Name())
	if err != nil {
		err = fmt.Errorf("opening base bundle: %w", err)
		return
	}
	defer func() { _ = bundle.Close() }()
	q := agent.ShellQuote
	origin := "refs/remotes/origin/" + r.Base.Branch
	script := strings.Join([]string{
		"set -eu",
		`sudo install -d -o "$(id -u)" -g "$(id -g)" ` + guestWork,
		"cat > " + guestBundle,
		"git init -q -b " + q(r.branch) + " " + guestRepo,
		"cd " + guestRepo,
		"git config user.name " + q(guestGitName),
		"git config user.email " + q(guestGitEmail),
		"git fetch -q --no-tags " + guestBundle + " " + q("+"+r.baseRef()+":"+origin),
		"git checkout -q -B " + q(r.branch) + " " + q(origin),
		"rm " + guestBundle,
	}, "\n")
	if err = remote(ctx, client, script, bundle, nil); err != nil {
		err = fmt.Errorf("cloning the base in the guest: %w", err)
		return
	}
	if err = remote(ctx, client, "cat > "+guestTicket, strings.NewReader(r.Ticket.Markdown()), nil); err != nil {
		err = fmt.Errorf("writing the ticket in the guest: %w", err)
	}
	return
}

// takeResult brings HEAD of the guest's clone into the Quarantine repo as a
// bundle, without running anything of the clone on the host (ADR-0002),
// validates it and accepts it as refs/heads/ticket-N.
func (r run) takeResult(ctx context.Context, client *ssh.Client) (err error) {
	var status tailBuffer
	if err = remote(ctx, client, "cd "+guestRepo+" && git status --porcelain", nil, &status); err != nil {
		err = fmt.Errorf("checking the guest's working tree: %w", err)
		return
	}
	if dirty := strings.TrimSpace(status.String()); dirty != "" {
		err = fmt.Errorf("the agent left uncommitted changes:\n%s", agent.Sanitize(dirty))
		return
	}
	file, err := os.CreateTemp(r.stateDir, "result-*.bundle")
	if err != nil {
		err = fmt.Errorf("creating result bundle: %w", err)
		return
	}
	defer func() { _ = os.Remove(file.Name()) }()
	command := "cd " + guestRepo + " && git bundle create -q - " + agent.ShellQuote(r.Base.Commit+"..HEAD")
	err = remote(ctx, client, command, nil, &limitWriter{w: file, left: maxResultSize})
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		err = fmt.Errorf("bundling the result in the guest: %w", err)
		return
	}
	head, err := r.quarantine.Import(ctx, file.Name(), "refs/afk/incoming/"+r.branch)
	if err != nil {
		return
	}
	commits, err := r.quarantine.Validate(ctx, r.Base.Commit, head, r.Base.Config.Protect.Paths)
	if err != nil {
		err = fmt.Errorf("result %s rejected: %w", head, err)
		return
	}
	if err = r.quarantine.Accept(ctx, "refs/heads/"+r.branch, head); err != nil {
		return
	}
	r.say("result valid: %d commit(s), %s in %s", commits, r.branch, r.quarantine.Dir)
	r.say("  git --git-dir=%s log %s..%s", r.quarantine.Dir, r.Base.Commit[:12], r.branch)
	return
}

// --- helpers ---

func (r run) baseRef() (ref string) { return "refs/afk/base/" + r.branch }

func (r run) prompt() (prompt string) {
	return agent.Prompt(agent.Task{
		RepoDir:    guestRepo,
		Branch:     r.branch,
		BaseRef:    "origin/" + r.Base.Branch,
		TicketPath: guestTicket,
		Protected:  r.Base.Config.Protect.Paths,
	})
}

// openLog creates the Run's log for the raw agent stream.
func (r *run) openLog() (path string, err error) {
	dir := filepath.Join(r.stateDir, "logs")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", dir, err)
		return
	}
	path = filepath.Join(dir, fmt.Sprintf("%s-%s.jsonl", r.branch, time.Now().UTC().Format("20060102T150405Z")))
	if r.log, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
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
		err = fmt.Errorf("%w: %s", err, agent.Sanitize(strings.TrimSpace(stderr.String())))
	}
	return
}

// tailBuffer keeps the last maxStderr bytes written to it.
type tailBuffer struct{ data []byte }

func (b *tailBuffer) Write(p []byte) (n int, err error) {
	b.data = append(b.data, p...)
	if len(b.data) > maxStderr {
		b.data = b.data[len(b.data)-maxStderr:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() (s string) { return string(b.data) }

// limitWriter fails once more than left bytes are written.
type limitWriter struct {
	w    io.Writer
	left int64
}

func (l *limitWriter) Write(p []byte) (n int, err error) {
	if int64(len(p)) > l.left {
		err = fmt.Errorf("result larger than %d bytes", maxResultSize)
		return
	}
	n, err = l.w.Write(p)
	l.left -= int64(n)
	return
}
