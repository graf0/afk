package chat

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/run"
)

// issuesName is the file of the Chat's state with the numbers of the issues it created, a line each.
const issuesName = "issues"

// --- protocol: what the Sandbox sends and the host answers ---

// createRequest is the body of POST /issues.
type createRequest struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

// commentRequest is the body of POST /issues/{n}/comments.
type commentRequest struct {
	Body string `json:"body"`
}

// editRequest is the body of PATCH /issues/{n}; a nil Title or Body is left as it is.
type editRequest struct {
	AddLabels    []string `json:"addLabels"`
	RemoveLabels []string `json:"removeLabels"`
	Title        *string  `json:"title,omitempty"`
	Body         *string  `json:"body,omitempty"`
}

// issueNumber parses the number of an issue.
func issueNumber(s string) (n int, err error) {
	n, err = strconv.Atoi(s)
	if err != nil || n <= 0 {
		err = errors.New("invalid issue number")
	}
	return
}

// --- host: the ticket routes of the channel ---

// issueChannel is what a Chat's afk-cli works on: the Project's Forge and
// labels, the Chat's name (the footer of what it writes) and the file in its
// state where the issues it creates are noted.
type issueChannel struct {
	Forge         forge.Forge
	ProjectLabels config.Labels
	Chat          string
	IssuesPath    string
}

// issueServer answers the Sandbox's `afk-cli issue` calls with the Project's Forge.
type issueServer struct {
	issueChannel
	responder
}

// issueRoutes is the ticket part of the channel.
func issueRoutes(ch issueChannel) (group routeGroup) {
	return func(mux *http.ServeMux, logger *log.Logger) {
		s := &issueServer{issueChannel: ch, responder: responder{log: logger}}
		mux.HandleFunc("GET /issues/{n}", s.view)
		mux.HandleFunc("GET /issues", s.list)
		mux.HandleFunc("POST /issues", s.create)
		mux.HandleFunc("POST /issues/{n}/comments", s.comment)
		mux.HandleFunc("PATCH /issues/{n}", s.edit)
	}
}

// request reads the issue number of r's path (when the route has one) and
// decodes its body into in (when not nil); on failure it answers 400 and
// ok is false. op names the call for the log.
func (s *issueServer) request(w http.ResponseWriter, r *http.Request, name string, in any) (n int, op string, ok bool) {
	op = name
	var err error
	if r.PathValue("n") != "" {
		op = name + " " + r.PathValue("n")
		if n, err = issueNumber(r.PathValue("n")); err == nil {
			op = fmt.Sprintf("%s #%d", name, n)
		}
	}
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	ok = in == nil || s.decode(w, r, op, in)
	return
}

// view serves `issue view <n>`: the ticket as the TICKET.md of a Run.
func (s *issueServer) view(w http.ResponseWriter, r *http.Request) {
	n, op, ok := s.request(w, r, "view", nil)
	if !ok {
		return
	}
	// Ticket refuses closed issues, which `issue list --state closed` shows: those come without comments.
	ticket, open, err := s.Forge.Peek(r.Context(), n)
	if err == nil && open {
		ticket, err = s.Forge.Ticket(r.Context(), n)
	}
	s.reply(w, op, agent.Sanitize(agent.TicketFile(ticket)), "", http.StatusBadGateway, err)
}

// list serves `issue list`: a line `#N  [labels]  title` per issue.
func (s *issueServer) list(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("label")
	state, err := forge.ParseState(r.URL.Query().Get("state"))
	op := fmt.Sprintf("list label=%q state=%s", label, state)
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	tickets, err := s.Forge.List(r.Context(), label, state)
	var b strings.Builder
	for _, ticket := range tickets {
		fmt.Fprintf(&b, "#%d  [%s]  %s\n", ticket.Number, strings.Join(ticket.Labels, ", "), ticket.Title)
	}
	s.reply(w, op, agent.Sanitize(b.String()), "", http.StatusBadGateway, err)
}

// create serves `issue create`: the issue gets the footer, and the Chat notes
// its number. ready-for-agent becomes needs-triage: readiness for the Factory
// is for a human to give.
func (s *issueServer) create(w http.ResponseWriter, r *http.Request) {
	var in createRequest
	if _, _, ok := s.request(w, r, "create", &in); !ok {
		return
	}
	title := titleLine(in.Title)
	if title == "" {
		s.reply(w, "create", "", "", http.StatusBadRequest, errors.New("title: required"))
		return
	}
	labels, replaced := s.triageLabels(in.Labels)
	err := s.checkLabels(r.Context(), labels)
	var n int
	if err == nil {
		n, err = s.Forge.CreateIssue(r.Context(), title, withFooter(s.Chat, in.Body), labels)
	}
	op := "create"
	if err == nil {
		op = fmt.Sprintf("create #%d", n)
		if err = s.noteIssue(n); err != nil {
			err = fmt.Errorf("issue #%d created, but not noted in the Chat: %w", n, err)
		}
	}
	op, warning := s.replacedNote(op+fmt.Sprintf(" labels=%q", labels), replaced)
	s.reply(w, op, fmt.Sprintf("#%d\n", n), warning, http.StatusBadGateway, err)
}

// comment serves `issue comment <n>`: the comment gets the footer, and a pull
// request is refused.
func (s *issueServer) comment(w http.ResponseWriter, r *http.Request) {
	var in commentRequest
	n, op, ok := s.request(w, r, "comment", &in)
	if !ok {
		return
	}
	if strings.TrimSpace(in.Body) == "" {
		s.reply(w, op, "", "", http.StatusBadRequest, errors.New("body: required"))
		return
	}
	// Peek refuses a pull request.
	_, _, err := s.Forge.Peek(r.Context(), n)
	if err == nil {
		err = s.Forge.Comment(r.Context(), n, withFooter(s.Chat, in.Body))
	}
	s.reply(w, op, "", "", http.StatusBadGateway, err)
}

// edit serves `issue edit <n>`: labels of any issue, title and body only of
// an issue this Chat created that is not ready-for-agent (the body keeps the
// footer). Everything is checked before the first write, but a Forge failure
// midway leaves the earlier writes. No state changes, and a pull request is
// refused.
func (s *issueServer) edit(w http.ResponseWriter, r *http.Request) {
	var in editRequest
	n, op, ok := s.request(w, r, "edit", &in)
	if !ok {
		return
	}
	var replaced bool
	in.AddLabels, replaced = s.triageLabels(in.AddLabels)
	// Removing ready-for-agent, given by a human, is fine: no triage here.
	in.RemoveLabels = cleanLabels(in.RemoveLabels)
	var err error
	if len(in.AddLabels)+len(in.RemoveLabels) == 0 && in.Title == nil && in.Body == nil {
		err = errors.New("nothing to change")
	}
	if err == nil && in.Title != nil {
		line := titleLine(*in.Title)
		if in.Title = &line; line == "" {
			err = errors.New("title: required")
		}
	}
	for _, label := range in.AddLabels {
		if err == nil && forge.ContainsLabel(in.RemoveLabels, label) {
			err = fmt.Errorf("label %q both added and removed", label)
		}
	}
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	op += fmt.Sprintf(" add=%q remove=%q", in.AddLabels, in.RemoveLabels)
	if in.Title != nil {
		op += " title"
	}
	if in.Body != nil {
		op += " body"
	}
	op, warning := s.replacedNote(op, replaced)
	s.reply(w, op, "", warning, http.StatusBadGateway, s.applyEdit(r.Context(), n, in))
}

// applyEdit checks what edit may do, then does it. edit has the labels
// cleaned and triaged, and the title on one line.
func (s *issueServer) applyEdit(ctx context.Context, n int, edit editRequest) (err error) {
	add, remove, title, body := edit.AddLabels, edit.RemoveLabels, edit.Title, edit.Body
	// Peek refuses a pull request.
	ticket, _, err := s.Forge.Peek(ctx, n)
	if err != nil {
		return
	}
	if title != nil || body != nil {
		var own bool
		if own, err = s.created(n); err != nil {
			return
		}
		if !own {
			err = fmt.Errorf("title and body: #%d was not created by this Chat, only its labels can change", n)
			return
		}
		// A human approved this text with the label: changing it would skip the review.
		// The label as it is before any write counts, also when this call removes it.
		if ticket.HasLabel(s.ProjectLabels.ReadyForAgent) {
			err = fmt.Errorf("title and body: #%d is %s, only its labels can change", n, s.ProjectLabels.ReadyForAgent)
			return
		}
	}
	if err = s.checkLabels(ctx, add); err != nil {
		return
	}
	if title != nil || body != nil {
		if body != nil {
			signed := withFooter(s.Chat, *body)
			body = &signed
		}
		if err = s.Forge.EditIssue(ctx, n, title, body); err != nil {
			return
		}
	}
	for _, label := range add {
		if err = s.Forge.AddLabel(ctx, n, label); err != nil {
			return
		}
	}
	for _, label := range remove {
		if err = s.Forge.RemoveLabel(ctx, n, label); err != nil {
			return
		}
	}
	return
}

// checkLabels fails on a label the repository doesn't have.
func (s *issueServer) checkLabels(ctx context.Context, labels []string) (err error) {
	known, err := s.Forge.Labels(ctx)
	if err != nil {
		err = fmt.Errorf("listing the labels of the repository: %w", err)
		return
	}
	for _, label := range labels {
		if !forge.ContainsLabel(known, label) {
			err = fmt.Errorf("label %q does not exist in the repository", label)
			return
		}
	}
	return
}

// triageLabels cleans labels and replaces ready-for-agent with needs-triage
// (the names of the Project's [labels]); replaced says it did.
func (s *issueServer) triageLabels(labels []string) (out []string, replaced bool) {
	labels = cleanLabels(labels)
	for i, label := range labels {
		if strings.EqualFold(label, s.ProjectLabels.ReadyForAgent) {
			labels[i], replaced = s.ProjectLabels.NeedsTriage, true
		}
	}
	out = cleanLabels(labels)
	return
}

// replacedNote marks op as having its ready-for-agent replaced, when it was,
// and words the warning for the Sandbox's stderr.
func (s *issueServer) replacedNote(op string, replaced bool) (noted, warning string) {
	noted = op
	if replaced {
		noted += " (" + s.ProjectLabels.ReadyForAgent + " replaced)"
		warning = fmt.Sprintf("label %s replaced with %s: only a human makes an issue ready for the Factory", s.ProjectLabels.ReadyForAgent, s.ProjectLabels.NeedsTriage)
	}
	return
}

// created reports whether the Chat created issue n.
func (s *issueServer) created(n int) (own bool, err error) {
	data, err := os.ReadFile(s.IssuesPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	own = slices.Contains(strings.Fields(string(data)), strconv.Itoa(n))
	return
}

// noteIssue appends n to the issues of the Chat.
func (s *issueServer) noteIssue(n int) (err error) {
	file, err := os.OpenFile(s.IssuesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		err = fmt.Errorf("opening %s: %w", s.IssuesPath, err)
		return
	}
	_, err = fmt.Fprintln(file, n)
	err = errors.Join(err, file.Close())
	if err != nil {
		err = fmt.Errorf("writing %s: %w", s.IssuesPath, err)
	}
	return
}

// withFooter sanitizes text like the Agent's text of a Run and signs it with
// chat, after any truncation so the footer always stays. A footer of the
// Chat already ending the text (an edit of a signed body) is not doubled.
func withFooter(chat, text string) (signed string) {
	footer := "— z Chat " + chat
	text, found := strings.CutSuffix(strings.TrimRight(text, " \t\r\n"), footer)
	if found {
		text = strings.TrimRight(text, " \t\r\n")
	}
	signed = run.AgentText(text) + "\n\n" + footer
	return
}

// titleLine is title sanitized like the Agent's text of a Run, on one line:
// AgentText may add a truncation note on a line of its own.
func titleLine(title string) (line string) {
	line = strings.Join(strings.Fields(run.AgentText(title)), " ")
	return
}

// cleanLabels trims labels, drops the empty ones and the repeated (ignoring case).
func cleanLabels(labels []string) (out []string) {
	for _, label := range labels {
		if label = strings.TrimSpace(label); label != "" && !forge.ContainsLabel(out, label) {
			out = append(out, label)
		}
	}
	return
}

// --- Sandbox: afk-cli issue ---

// issueArg splits args into the issue number in front, as the path of the
// issue, and the rest.
func issueArg(args []string) (path string, rest []string, err error) {
	if len(args) == 0 {
		err = errCLIUsage
		return
	}
	if _, err = issueNumber(args[0]); err != nil {
		err = errCLIUsage
		return
	}
	path, rest = "/issues/"+args[0], args[1:]
	return
}

// parseIssueView parses `issue view <n>`.
func parseIssueView(args []string, _ io.Reader) (call cliCall, err error) {
	path, rest, err := issueArg(args)
	if err == nil && len(rest) != 0 {
		err = errCLIUsage
	}
	call = cliCall{method: http.MethodGet, path: path}
	return
}

// parseIssueList parses `issue list [--label <label>] [--state <state>]`.
func parseIssueList(args []string, _ io.Reader) (call cliCall, err error) {
	flags := flag.NewFlagSet("issue list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	label := flags.String("label", "", "")
	state := flags.String("state", string(forge.StateOpen), "")
	if err = flags.Parse(args); err == nil && flags.NArg() == 0 {
		_, err = forge.ParseState(*state)
	}
	if err != nil || flags.NArg() != 0 {
		err = errCLIUsage
		return
	}
	call = cliCall{method: http.MethodGet, path: "/issues?label=" + url.QueryEscape(*label) + "&state=" + url.QueryEscape(*state)}
	return
}

// parseIssueCreate parses `issue create`.
func parseIssueCreate(args []string, stdin io.Reader) (call cliCall, err error) {
	var body string
	req := createRequest{}
	flags, text := textFlags("issue create", &body, stdin)
	flags.StringVar(&req.Title, "title", "", "")
	labelsFlag(flags, "label", &req.Labels)
	if err = flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(req.Title) == "" {
		err = errCLIUsage
		return
	}
	if req.Body, err = text(); err != nil {
		return
	}
	call = cliCall{method: http.MethodPost, path: "/issues", body: req}
	return
}

// parseIssueComment parses `issue comment <n>`.
func parseIssueComment(args []string, stdin io.Reader) (call cliCall, err error) {
	path, rest, err := issueArg(args)
	if err != nil {
		return
	}
	var body string
	flags, text := textFlags("issue comment", &body, stdin)
	if flags.Parse(rest) != nil || flags.NArg() != 0 {
		err = errCLIUsage
		return
	}
	if body, err = text(); err != nil {
		return
	}
	if strings.TrimSpace(body) == "" {
		err = errCLIUsage
		return
	}
	call = cliCall{method: http.MethodPost, path: path + "/comments", body: commentRequest{Body: body}}
	return
}

// parseIssueEdit parses `issue edit <n>`: only the title and body given go in
// the request, an absent one stays as it is.
func parseIssueEdit(args []string, stdin io.Reader) (call cliCall, err error) {
	path, rest, err := issueArg(args)
	if err != nil {
		return
	}
	var body string
	req := editRequest{}
	flags, text := textFlags("issue edit", &body, stdin)
	flags.Func("title", "", func(value string) (err error) {
		req.Title = &value
		return
	})
	labelsFlag(flags, "add-label", &req.AddLabels)
	labelsFlag(flags, "remove-label", &req.RemoveLabels)
	if flags.Parse(rest) != nil || flags.NArg() != 0 {
		err = errCLIUsage
		return
	}
	var hasBody bool
	flags.Visit(func(f *flag.Flag) { hasBody = hasBody || f.Name == "body" || f.Name == "body-file" })
	if hasBody {
		if body, err = text(); err != nil {
			return
		}
		req.Body = &body
	}
	call = cliCall{method: http.MethodPatch, path: path, body: req}
	return
}
