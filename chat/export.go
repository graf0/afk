package chat

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/run"
)

// exporter is one Export of a Chat, apart from the VM it reads.
type exporter struct {
	Quarantine run.Quarantine
	Remote     forge.Remote
	Name       string
	ChatDir    string
	TempDir    string
	Out        io.Writer
}

// Export brings the commits of the Chat opts.Name to the branch chat-<name>
// on the Forge (docs/workflow.md, Eksport). It does not take the Chat's lock:
// it may run while a session is attached. A shut-off VM is started. Only
// opts.Base, Name, Paths, Forge and Out are used.
func Export(ctx context.Context, opts Options) (err error) {
	project, stateDir, chatDir, err := existingChat(opts)
	if err != nil {
		return
	}
	quarantine, err := run.OpenQuarantine(ctx, filepath.Join(stateDir, run.QuarantineName))
	if err != nil {
		return
	}
	signer, err := image.SSHSigner(opts.Paths)
	if err != nil {
		return
	}
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	m, err := startVM(conn, project, opts.Name, opts.Out)
	if err != nil {
		return
	}
	// No Egress proxy: git bundle over ssh is all the export needs.
	sshCtx, cancel := context.WithTimeout(ctx, machine.SSHTimeout)
	defer cancel()
	client, err := m.SSH(sshCtx, signer)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	err = exporter{
		Quarantine: quarantine, Remote: opts.Forge.Remote(), Name: opts.Name,
		ChatDir: chatDir, TempDir: stateDir, Out: opts.Out,
	}.export(ctx, run.NewExec(client, nil))
	return
}

// export fetches the commits of the guest behind exec, validates them and
// pushes them. The branch on the remote moves only from what the last export
// pushed (snapshot.Exported): rewritten history passes, anyone else's push
// does not. A branch deleted on the remote is created again; one already at
// the head (pushed, but not recorded) is only recorded.
func (e exporter) export(ctx context.Context, exec run.ExecFunc) (err error) {
	snap, err := readSnapshot(e.ChatDir)
	if err != nil {
		return
	}
	if snap.Protect == nil {
		err = errors.New("the Chat's snapshot has no protected paths (created by an older afk): export is refused")
		return
	}
	branch := BranchPrefix + e.Name
	base, err := e.Quarantine.Base(ctx, branch)
	if err != nil {
		return
	}
	dirty, err := run.Dirty(ctx, exec)
	if err != nil {
		return
	}
	if dirty != "" {
		say(e.Out, "warning: uncommitted changes in the guest are not exported (%d path(s))", len(strings.Split(dirty, "\n")))
	}
	guest := run.GuestBranch{Quarantine: e.Quarantine, Name: branch, TempDir: e.TempDir}
	head, err := guest.Fetch(ctx, exec, base, snap.Protect, e.Out)
	if err != nil {
		return
	}
	// Fetch returns the base itself, unvalidated, without commits: never push that.
	if head == base {
		if snap.Exported == "" {
			say(e.Out, "no new commits since the base %s, nothing to export", base[:12])
		} else {
			say(e.Out, "HEAD in the guest is back at the base %s: nothing to export, %s exported earlier stays", base[:12], snap.Exported[:12])
		}
		return
	}
	remoteTip, err := e.Quarantine.RemoteTip(ctx, e.Remote, branch)
	if err != nil {
		return
	}
	if remoteTip == head {
		if snap.Exported != head {
			snap.Exported = head
			err = snap.write(e.ChatDir)
		}
		say(e.Out, "no new commits since %s, nothing to export", head[:12])
		return
	}
	lease := snap.Exported
	if remoteTip == "" {
		lease = "" // never pushed, or deleted on the remote: create it
	}
	local, err := e.Quarantine.Result(ctx, branch)
	if err != nil {
		return
	}
	if err = e.Quarantine.Accept(ctx, branch, head, local); err != nil {
		return
	}
	say(e.Out, "pushing %s to %s", branch, e.Remote.Repo)
	if err = e.Quarantine.Push(ctx, e.Remote, branch, lease); err != nil {
		return
	}
	snap.Exported = head
	if err = snap.write(e.ChatDir); err != nil {
		return
	}
	say(e.Out, "exported %s at %s", branch, head[:12])
	return
}
