package forge

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/graf0/afk/config"
)

// Local is the forge of a Project that keeps its tickets as files in the
// repository (forge.kind = "local", ADR-0016): no API, so there are no
// issues and pull requests to ask for, only the git remote. That is the
// origin of the user's clone, pushed to with the user's git (config, ssh
// keys): afk holds no credentials of it.
type Local struct {
	remote Remote
}

// NewLocal returns the local forge of cfg, whose remote is the origin of the
// user's clone at repo (its root).
func NewLocal(ctx context.Context, cfg config.Forge, repo string) (f *Local, err error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "remote", "get-url", "origin").Output()
	url := strings.TrimSpace(string(out))
	if err == nil && url == "" {
		err = errors.New("empty URL")
	}
	if err != nil {
		err = fmt.Errorf("forge local: reading the URL of origin in %s: %w", repo, err)
		return
	}
	f = &Local{remote: Remote{Repo: cfg.Repo, URL: url}}
	return
}

// Remote is the origin of the user's clone, without credentials of afk.
func (f *Local) Remote() (remote Remote) { return f.remote }

// unsupported is the error of what a local forge has no API for.
func unsupported(op string) (err error) {
	return fmt.Errorf("forge local: %s: %w (tickets are files in the repository)", op, errors.ErrUnsupported)
}

// --- no API ---

func (f *Local) ReadyTicket(context.Context, int, string) (ticket Ticket, err error) {
	err = unsupported("reading a ticket")
	return
}

func (f *Local) Ticket(context.Context, int) (ticket Ticket, err error) {
	err = unsupported("reading a ticket")
	return
}

func (f *Local) Peek(context.Context, int) (ticket Ticket, open bool, err error) {
	err = unsupported("reading a ticket")
	return
}

func (f *Local) ListLabeled(context.Context, string) (numbers []int, err error) {
	err = unsupported("listing tickets")
	return
}

func (f *Local) List(context.Context, string, State) (tickets []Ticket, err error) {
	err = unsupported("listing tickets")
	return
}

func (f *Local) Labels(context.Context) (names []string, err error) {
	err = unsupported("listing labels")
	return
}

func (f *Local) SubIssues(context.Context, int) (subs []SubIssue, err error) {
	err = unsupported("listing sub-issues")
	return
}

func (f *Local) Blocked(context.Context, Ticket) (blocked bool, err error) {
	err = unsupported("reading blockers")
	return
}

func (f *Local) CreatePull(context.Context, string, string, string, string, bool) (pull PullRequest, err error) {
	err = unsupported("opening a pull request")
	return
}

func (f *Local) FindPull(context.Context, string) (pull PullRequest, found bool, err error) {
	err = unsupported("finding a pull request")
	return
}

func (f *Local) UpdatePullBody(context.Context, int, string) (err error) {
	return unsupported("editing a pull request")
}

func (f *Local) ReadyPull(context.Context, PullRequest) (err error) {
	return unsupported("marking a pull request ready")
}

func (f *Local) CloseIssue(context.Context, int) (err error) { return unsupported("closing an issue") }

func (f *Local) CreateIssue(context.Context, string, string, []string) (n int, err error) {
	err = unsupported("creating an issue")
	return
}

func (f *Local) Comment(context.Context, int, string) (err error) { return unsupported("commenting") }

func (f *Local) AddLabel(context.Context, int, string) (err error) {
	return unsupported("adding a label")
}

func (f *Local) EditIssue(context.Context, int, *string, *string) (err error) {
	return unsupported("editing an issue")
}

func (f *Local) RemoveLabel(context.Context, int, string) (err error) {
	return unsupported("removing a label")
}
