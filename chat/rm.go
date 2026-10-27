package chat

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/image"
	"github.com/graf0/afk/machine"
	"github.com/graf0/afk/run"
)

// maxShas is how many commits a refusal names.
const maxShas = 5

var shaPattern = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// Remove destroys the Chat opts.Name (docs/workflow.md, Zamknięcie): its VM
// (with network, filter and overlay), the Quarantine refs of chat-<name> and
// its state directory, last, so that a failure midway leaves a Chat that
// Remove can still remove. Logs and the branch on the Forge stay. It takes the
// Chat's lock, so an attached session refuses it, force or not.
//
// Without force it refuses when the work would be lost: uncommitted changes,
// or HEAD other than the last export (never exported: the base). A shut-off VM
// is started for the check. A Chat without a VM has nothing to lose; a VM
// without a state directory (nothing says what was exported) is removed only
// with force. Only opts.Base, Name, Paths and Out are used.
func Remove(ctx context.Context, opts Options, force bool) (err error) {
	if err = ValidateName(opts.Name); err != nil {
		return
	}
	project, err := image.ProjectName(opts.Base.Config.Forge.Repo)
	if err != nil {
		return
	}
	stateDir := filepath.Join(opts.Paths.StateDir, project)
	chatDir := filepath.Join(stateDir, stateName, opts.Name)
	hasState := true
	if _, statErr := os.Stat(chatDir); errors.Is(statErr, fs.ErrNotExist) {
		hasState = false
	} else if statErr != nil {
		err = fmt.Errorf("reading the state of Chat %s: %w", opts.Name, statErr)
		return
	}
	if hasState {
		var unlock func()
		if unlock, err = lockChat(opts.Name, chatDir); err != nil {
			return
		}
		defer unlock()
	}
	conn, err := machine.Connect()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	machines, err := conn.List(project)
	if err != nil {
		return
	}
	i := slices.IndexFunc(machines, func(m machine.Machine) bool { return m.Kind == machine.KindChat && m.ID == opts.Name })
	if i < 0 && !hasState {
		err = fmt.Errorf("chat %s does not exist (see `afk chat list`)", opts.Name)
		return
	}
	switch {
	case force || i < 0:
	case !hasState:
		err = fmt.Errorf("chat %s has a VM but no state: what it exported is unknown, use --force to remove it", opts.Name)
		return
	default:
		if err = checkLost(ctx, conn, opts, project, stateDir, chatDir); err != nil {
			return
		}
	}

	branch := BranchPrefix + opts.Name
	if i >= 0 {
		if err = conn.Kill(machines[i].Name); err != nil {
			err = fmt.Errorf("removing VM %s: %w", machines[i].Name, err)
			return
		}
	}
	quarantine, err := run.OpenQuarantine(ctx, filepath.Join(stateDir, run.QuarantineName))
	if err != nil {
		return
	}
	if err = quarantine.Drop(ctx, branch); err != nil {
		return
	}
	if err = os.RemoveAll(chatDir); err != nil {
		err = fmt.Errorf("removing the state of Chat %s: %w", opts.Name, err)
		return
	}
	say(opts.Out, "Chat %s removed (logs and branch %s on the Forge, if exported, stay)", opts.Name, branch)
	return
}

// checkLost starts the Chat's VM when shut off and refuses when its work
// would be lost.
func checkLost(ctx context.Context, conn *machine.Conn, opts Options, project, stateDir, chatDir string) (err error) {
	snap, err := readSnapshot(chatDir)
	if err != nil {
		err = fmt.Errorf("%w (use --force to remove the Chat anyway)", err)
		return
	}
	quarantine, err := run.OpenQuarantine(ctx, filepath.Join(stateDir, run.QuarantineName))
	if err != nil {
		return
	}
	from, what := snap.Exported, "the last export"
	if from == "" {
		what = "the base"
		if from, err = quarantine.Base(ctx, BranchPrefix+opts.Name); err != nil {
			err = fmt.Errorf("%w (use --force to remove the Chat anyway)", err)
			return
		}
	}
	exec, closeExec, err := guestExec(ctx, conn, opts, project)
	if err != nil {
		return
	}
	defer closeExec()
	lost, err := unsaved(ctx, exec, from, what)
	if err == nil && lost != "" {
		err = fmt.Errorf("chat %s would lose %s: commit and export it (`afk chat export %s`) or use --force", opts.Name, lost, opts.Name)
	}
	return
}

// unsaved describes what the guest behind exec holds that from (a commit,
// what is its name for the message) does not: uncommitted paths and commits
// not reachable from from. Empty: nothing would be lost (HEAD behind from
// included). Shas from the guest are printed only when they are hex.
func unsaved(ctx context.Context, exec run.ExecFunc, from, what string) (lost string, err error) {
	dirty, err := run.Dirty(ctx, exec)
	if err != nil {
		return
	}
	var parts []string
	if dirty != "" {
		parts = append(parts, fmt.Sprintf("%d uncommitted path(s)", len(strings.Split(dirty, "\n"))))
	}
	var head strings.Builder
	if err = exec(ctx, "git rev-parse HEAD", &head); err != nil {
		err = fmt.Errorf("reading HEAD of the guest: %w", err)
		return
	}
	if tip := strings.TrimSpace(head.String()); tip != from {
		var log strings.Builder
		if logErr := exec(ctx, "git log --format=%h "+agent.ShellQuote(from+"..HEAD"), &log); logErr != nil {
			// from is not in the guest's history (rewritten): no list of commits.
			parts = append(parts, fmt.Sprintf("HEAD %s, which does not contain %s %s", shortSHA(tip), what, shortSHA(from)))
		} else if shas := strings.Fields(log.String()); len(shas) > 0 {
			list := make([]string, 0, maxShas)
			for _, sha := range shas[:min(maxShas, len(shas))] {
				list = append(list, shortSHA(sha))
			}
			if len(shas) > maxShas {
				list = append(list, "...")
			}
			parts = append(parts, fmt.Sprintf("%d commit(s) after %s %s (%s)", len(shas), what, shortSHA(from), strings.Join(list, ", ")))
		}
	}
	lost = strings.Join(parts, " and ")
	return
}

// shortSHA is the first 12 characters of a commit sha, or "?" when it is not
// hex: a sha read from the guest is untrusted text.
func shortSHA(sha string) (short string) {
	if !shaPattern.MatchString(sha) {
		return "?"
	}
	return sha[:min(12, len(sha))]
}
