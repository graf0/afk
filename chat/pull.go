package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/run"
)

// --- protocol ---

// pullCreateRequest is the body of POST /pulls.
type pullCreateRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// pullChannel is what the pull request route works on: the Project's Forge,
// the name of the Chat (the footer of the description), the Chat's branch in
// the Quarantine repo, the base branch of the pull request and the Sandbox's
// clone, which Exec runs commands in.
type pullChannel struct {
	Forge      forge.Forge
	Chat       string
	Quarantine run.Quarantine
	Branch     string // chat-<name>: in the clone, the Quarantine repo and on the Forge
	BaseBranch string
	TempDir    string // holds bundles on the host while they are received
	Exec       run.ExecFunc
	// Protected is every path the result must not change; asked per call, as
	// the Stack's Dockerfiles are known only once it is up.
	Protected func() (paths []string)
}

// pullServer answers the Sandbox's `afk-cli pr` calls. Calls go one at a
// time: they move the same branch.
type pullServer struct {
	responder
	ch pullChannel
	mu sync.Mutex
}

// pullRoutes is the pull request part of the channel.
func pullRoutes(ch pullChannel) (group routeGroup) {
	return func(mux *http.ServeMux, logger *log.Logger) {
		s := &pullServer{responder: responder{log: logger}, ch: ch}
		mux.HandleFunc("POST /pulls", s.create)
	}
}

// create serves `pr create`: the commits of the Sandbox's clone go through
// the Quarantine repo and its validation (protected paths included) to
// chat-<name> on the Forge, and the pull request of that branch is opened, or
// found when there is one open. Nothing reaches the Forge before the
// validation passes.
func (s *pullServer) create(w http.ResponseWriter, r *http.Request) {
	var in pullCreateRequest
	if !s.decode(w, r, "pr create", &in) {
		return
	}
	title := titleLine(in.Title)
	if title == "" {
		s.reply(w, "pr create", "", "", http.StatusBadRequest, errors.New("title: required"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pull, warning, err := s.open(r.Context(), title, in.Body)
	op := "pr create"
	if err == nil {
		op += fmt.Sprintf(" #%d", pull.Number)
	}
	s.reply(w, op, pull.URL+"\n", warning, http.StatusBadGateway, err)
}

// open does what create says. The warning is for the Sandbox's stderr.
func (s *pullServer) open(ctx context.Context, title, body string) (pull forge.PullRequest, warning string, err error) {
	q, branch := s.ch.Quarantine, s.ch.Branch
	base, err := q.Base(ctx, branch)
	if err != nil {
		return
	}
	guest := run.GuestBranch{Quarantine: q, Name: branch, TempDir: s.ch.TempDir}
	head, err := guest.Fetch(ctx, s.ch.Exec, base, s.ch.Protected(), io.Discard)
	if err != nil {
		return
	}
	if head == base {
		err = fmt.Errorf("no commits on top of origin/%s as this Chat started (%s): commit your work first", s.ch.BaseBranch, base[:12])
		return
	}
	dirty, err := run.Dirty(ctx, s.ch.Exec)
	if err != nil {
		return
	}
	if dirty != "" {
		warning = "uncommitted changes are not in the pull request: commit them and call again"
	}
	prev, err := q.Result(ctx, branch)
	if err != nil {
		return
	}
	if err = s.push(ctx, prev, head); err != nil {
		return
	}
	pull, found, err := s.ch.Forge.FindPull(ctx, branch)
	if err != nil {
		return
	}
	if found && pull.State == "open" {
		pushed := "new commits pushed"
		if head == prev {
			pushed = "no new commits"
		}
		note := fmt.Sprintf("pull request #%d is open: %s, its title and description unchanged", pull.Number, pushed)
		warning = strings.TrimSpace(warning + "\n" + note)
		return
	}
	pull, err = s.ch.Forge.CreatePull(ctx, branch, s.ch.BaseBranch, title, withFooter(s.ch.Chat, body), false)
	return
}

// push moves the result branch from prev, where afk left it, to head and
// sends it to the Forge. The branch on the Forge must be at prev too (absent
// for the first push): what someone else put there is never overwritten. A
// failed push puts the result branch back to prev, so the next call does not
// take the Forge's branch for someone else's; a failed first push needs no
// rollback, as an absent branch on the Forge passes that check.
func (s *pullServer) push(ctx context.Context, prev, head string) (err error) {
	q, branch := s.ch.Quarantine, s.ch.Branch
	remote := s.ch.Forge.Remote()
	tip, err := q.RemoteTip(ctx, remote, branch)
	if err != nil {
		return
	}
	if tip != "" && tip != prev {
		err = fmt.Errorf("branch %s on %s is at %s, not where afk left it: someone else pushed to it", branch, remote.Repo, tip)
		return
	}
	if err = q.Accept(ctx, branch, head, prev); err != nil {
		return
	}
	if err = q.Push(ctx, remote, branch, tip); err != nil && prev != "" {
		// Also when the Sandbox gave up on the call: a result branch left
		// ahead of the Forge would read as someone else's push next time.
		err = errors.Join(err, q.Accept(context.WithoutCancel(ctx), branch, prev, head))
	}
	return
}

// --- Sandbox: afk-cli pr ---

// parsePullCreate parses `pr create`.
func parsePullCreate(args []string, stdin io.Reader) (call cliCall, err error) {
	var body string
	req := pullCreateRequest{}
	flags, text := textFlags("pr create", &body, stdin)
	flags.StringVar(&req.Title, "title", "", "")
	if err = flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(req.Title) == "" {
		err = errCLIUsage
		return
	}
	if req.Body, err = text(); err != nil {
		return
	}
	call = cliCall{method: http.MethodPost, path: "/pulls", body: req}
	return
}
