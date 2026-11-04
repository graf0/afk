package chat

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// The routes of a Chat on a local forge (ADR-0016): its branch goes to
// origin without a pull request, and its tickets, files under forge.dir, go
// straight to the base branch.

// --- protocol ---

// ticketsPublishRequest is the body of POST /tickets/publish.
type ticketsPublishRequest struct {
	Message string `json:"message"`
}

// --- host: push ---

// pushBranch serves `push`: the commits of the Sandbox's clone go through the
// Quarantine repo and its validation to chat-<name> on origin, as for `pr
// create`, but no pull request is opened: a human merges the branch.
func (s *pullServer) pushBranch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, prev, warning, err := s.export(r.Context())
	op, output := "push", ""
	if err == nil {
		op += " " + head[:12]
		output = fmt.Sprintf("%s on %s at %s\n", s.ch.Branch, s.ch.Forge.Remote().Repo, head[:12])
		if head == prev {
			warning = strings.TrimSpace(warning + "\nno new commits")
		}
	}
	s.reply(w, op, output, warning, http.StatusBadGateway, err)
}

// --- host: tickets publish ---

// publishTickets serves `tickets publish`: the changes the Chat's branch made
// under forge.dir, and nothing else, become one commit on the base branch as
// it is on origin now, pushed there with a plain push.
func (s *pullServer) publishTickets(w http.ResponseWriter, r *http.Request) {
	var in ticketsPublishRequest
	if !s.decode(w, r, "tickets publish", &in) {
		return
	}
	if strings.TrimSpace(in.Message) == "" {
		s.reply(w, "tickets publish", "", "", http.StatusBadRequest, errors.New("message: required"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	output, warning, err := s.publish(r.Context(), in.Message)
	s.reply(w, "tickets publish", output, warning, http.StatusBadGateway, err)
}

// publish does what publishTickets says. The whole branch is validated as
// for push, protected paths included, though only forge.dir is published. A
// ticket given the ready-for-agent status is refused: only a human makes a
// ticket ready for the Factory (ADR-0014).
func (s *pullServer) publish(ctx context.Context, message string) (output, warning string, err error) {
	_, head, warning, err := s.fetch(ctx)
	if err != nil {
		return
	}
	remote, dir, base := s.ch.Forge.Remote(), s.ch.TicketsDir, s.ch.BaseBranch
	check := func(patch string) (err error) {
		if setsStatus(patch, s.ch.ReadyLabel) {
			err = fmt.Errorf("a ticket gets Status: %s, which only a human gives: use another status and tell the human; nothing was pushed", s.ch.ReadyLabel)
		}
		return
	}
	commit, err := s.ch.Quarantine.PublishDir(ctx, remote, base, s.ch.Branch, head, dir, withFooter(s.ch.Chat, message), s.ch.Ident, check)
	switch {
	case err != nil:
	case commit == "":
		output = fmt.Sprintf("nothing to publish: origin/%s has every change this branch made under %s/\n", base, dir)
	default:
		output = fmt.Sprintf("%s on %s at %s\n", base, remote.Repo, commit[:12])
	}
	return
}

// setsStatus reports whether patch adds a line "Status: <status>", ignoring
// case and spaces around.
func setsStatus(patch, status string) (sets bool) {
	return regexp.MustCompile(`(?im)^\+[ \t]*status:[ \t]*` + regexp.QuoteMeta(status) + `[ \t]*$`).MatchString(patch)
}

// --- Sandbox: afk-ai push, afk-ai tickets publish ---

// parsePush parses `push`, which takes no arguments.
func parsePush(args []string, _ io.Reader) (call cliCall, err error) {
	if len(args) != 0 {
		err = errCLIUsage
		return
	}
	call = cliCall{method: http.MethodPost, path: "/push"}
	return
}

// parseTicketsPublish parses `tickets publish -m <message>`.
func parseTicketsPublish(args []string, _ io.Reader) (call cliCall, err error) {
	req := ticketsPublishRequest{}
	flags := flag.NewFlagSet("tickets publish", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&req.Message, "m", "", "")
	if err = flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(req.Message) == "" {
		err = errCLIUsage
		return
	}
	call = cliCall{method: http.MethodPost, path: "/tickets/publish", body: req}
	return
}
