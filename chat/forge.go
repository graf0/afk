package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
	"github.com/graf0/afk/run"
)

const (
	// ForgeBinary is the name `afk` runs as in the guest, and in $PATH there:
	// a copy of the host's binary (ADR-0012).
	ForgeBinary = "afk-forge"

	// ForgeSocket is where the channel to afk appears inside the guest.
	ForgeSocket = forgeSocketDir + "/afk.sock"

	// issuesName is the file of the Chat's state with the numbers of the issues it created, a line each.
	issuesName = "issues"
)

// --- protocol: what the guest sends and the host answers ---

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

// forgeReply is the one JSON document the host answers each call with.
type forgeReply struct {
	Output  string `json:"output,omitempty"`
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
}

// issueNumber parses the number of an issue.
func issueNumber(s string) (n int, err error) {
	n, err = strconv.Atoi(s)
	if err != nil || n <= 0 {
		err = errors.New("invalid issue number")
	}
	return
}

// --- host: the channel ---

const (
	forgeSocketDir = "/run/afk"

	maxForgeBody = 1 << 20
)

// forgeChannel is what a Chat's afk-forge works on: the Project's Forge and
// labels, the Chat's name (the footer of what it writes) and the file in its
// state where the issues it creates are noted.
type forgeChannel struct {
	Forge         forge.Forge
	ProjectLabels config.Labels
	Chat          string
	IssuesPath    string
}

// forgeServer answers the guest's afk-forge calls with the Project's Forge,
// one forgeReply per call. Every call goes to log, unknown routes too.
type forgeServer struct {
	forgeChannel
	log *log.Logger
}

func newForgeServer(ch forgeChannel, logTo io.Writer) (s *forgeServer) {
	return &forgeServer{forgeChannel: ch, log: log.New(logTo, "", log.LstdFlags)}
}

func (s *forgeServer) handler() (handler http.Handler) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /issues/{n}", s.view)
	mux.HandleFunc("GET /issues", s.list)
	mux.HandleFunc("POST /issues", s.create)
	mux.HandleFunc("POST /issues/{n}/comments", s.comment)
	mux.HandleFunc("PATCH /issues/{n}", s.edit)
	// Patterns with a method win over "/", but "/" matches every method: a
	// known path with another method lands here too (404, not the mux's 405).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r.Method+" "+r.URL.Path, "", "", http.StatusNotFound, errors.New("no such route"))
	})
	return mux
}

// request reads the issue number of r's path (when the route has one) and
// decodes its body into in (when not nil); on failure it answers 400 and
// ok is false. op names the call for the log.
func (s *forgeServer) request(w http.ResponseWriter, r *http.Request, name string, in any) (n int, op string, ok bool) {
	op = name
	var err error
	if r.PathValue("n") != "" {
		op = name + " " + r.PathValue("n")
		if n, err = issueNumber(r.PathValue("n")); err == nil {
			op = fmt.Sprintf("%s #%d", name, n)
		}
	}
	if err == nil && in != nil {
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxForgeBody)).Decode(in); err != nil {
			err = fmt.Errorf("decoding the request: %w", err)
		}
	}
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	ok = true
	return
}

// view serves `issue view <n>`: the ticket as the TICKET.md of a Run.
func (s *forgeServer) view(w http.ResponseWriter, r *http.Request) {
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
func (s *forgeServer) list(w http.ResponseWriter, r *http.Request) {
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
func (s *forgeServer) create(w http.ResponseWriter, r *http.Request) {
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
		n, err = s.Forge.CreateIssue(r.Context(), title, s.withFooter(in.Body), labels)
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
func (s *forgeServer) comment(w http.ResponseWriter, r *http.Request) {
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
		err = s.Forge.Comment(r.Context(), n, s.withFooter(in.Body))
	}
	s.reply(w, op, "", "", http.StatusBadGateway, err)
}

// edit serves `issue edit <n>`: labels of any issue, title and body only of
// an issue this Chat created that is not ready-for-agent (the body keeps the
// footer). Everything is checked before the first write, but a Forge failure
// midway leaves the earlier writes. No state changes, and a pull request is
// refused.
func (s *forgeServer) edit(w http.ResponseWriter, r *http.Request) {
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
func (s *forgeServer) applyEdit(ctx context.Context, n int, edit editRequest) (err error) {
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
			signed := s.withFooter(*body)
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
func (s *forgeServer) checkLabels(ctx context.Context, labels []string) (err error) {
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
func (s *forgeServer) triageLabels(labels []string) (out []string, replaced bool) {
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
// and words the warning for the guest's stderr.
func (s *forgeServer) replacedNote(op string, replaced bool) (noted, warning string) {
	noted = op
	if replaced {
		noted += " (" + s.ProjectLabels.ReadyForAgent + " replaced)"
		warning = fmt.Sprintf("label %s replaced with %s: only a human makes an issue ready for the Factory", s.ProjectLabels.ReadyForAgent, s.ProjectLabels.NeedsTriage)
	}
	return
}

// withFooter sanitizes text like the Agent's text of a Run and signs it with
// the Chat, after any truncation so the footer always stays. A footer of the
// Chat already ending the text (an edit of a signed body) is not doubled.
func (s *forgeServer) withFooter(text string) (signed string) {
	footer := "— z Chat " + s.Chat
	text, found := strings.CutSuffix(strings.TrimRight(text, " \t\r\n"), footer)
	if found {
		text = strings.TrimRight(text, " \t\r\n")
	}
	signed = run.AgentText(text) + "\n\n" + footer
	return
}

// created reports whether the Chat created issue n.
func (s *forgeServer) created(n int) (own bool, err error) {
	data, err := os.ReadFile(s.IssuesPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	own = slices.Contains(strings.Fields(string(data)), strconv.Itoa(n))
	return
}

// noteIssue appends n to the issues of the Chat.
func (s *forgeServer) noteIssue(n int) (err error) {
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

// reply logs the call and answers with output and a warning for the guest's
// stderr, or with failure (status) when err is set.
func (s *forgeServer) reply(w http.ResponseWriter, op, output, warning string, failure int, err error) {
	body := forgeReply{Output: output, Warning: warning}
	if err != nil {
		s.log.Printf("%s: error: %v", op, err)
		w.WriteHeader(failure)
		body = forgeReply{Error: agent.Sanitize(err.Error())}
	} else {
		s.log.Printf("%s: ok", op)
	}
	_ = json.NewEncoder(w).Encode(body)
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

// serveForge puts this binary in the guest as afk-forge and serves the
// channel over a reverse unix socket on client, logging each call to logPath;
// no port is opened on the host. stop closes the socket, which then
// disappears from the guest, and waits for the server.
func serveForge(ctx context.Context, client *ssh.Client, ch forgeChannel, logPath string) (stop func(), err error) {
	self, err := os.Executable()
	if err != nil {
		err = fmt.Errorf("finding afk's own binary: %w", err)
		return
	}
	binary, err := os.Open(self)
	if err != nil {
		err = fmt.Errorf("opening %s: %w", self, err)
		return
	}
	defer func() { _ = binary.Close() }()
	// install replaces the file: a running afk-forge of an earlier attach is no obstacle.
	if err = run.Upload(ctx, client, `t=$(mktemp) && trap 'rm -f "$t"' EXIT && cat >"$t" && sudo install -m 0755 "$t" /usr/local/bin/`+ForgeBinary, binary); err != nil {
		err = fmt.Errorf("copying afk to the guest: %w", err)
		return
	}
	// A socket left by an attach that died is not unlinked by sshd on its own.
	if err = run.NewExec(client, nil)(ctx, "sudo install -d -o agent -m 0755 "+forgeSocketDir+" && rm -f "+ForgeSocket, io.Discard); err != nil {
		err = fmt.Errorf("preparing the guest's %s: %w", ForgeSocket, err)
		return
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		err = fmt.Errorf("opening %s: %w", logPath, err)
		return
	}
	ln, err := client.ListenUnix(ForgeSocket)
	if err != nil {
		_ = logFile.Close()
		err = fmt.Errorf("opening the channel on guest %s: %w", ForgeSocket, err)
		return
	}
	server := &http.Server{Handler: newForgeServer(ch, logFile).handler(), BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(ln)
	}()
	stop = func() {
		_ = server.Close() // closes ln too
		<-done
		_ = logFile.Close()
	}
	return
}

// --- guest: afk-forge ---

const forgeUsage = `usage:
  afk-forge issue view <n>
  afk-forge issue list [--label <label>] [--state open|closed|all]
  afk-forge issue create --title <title> [--body <text> | --body-file -] [--label <a,b>]...
  afk-forge issue comment <n> (--body <text> | --body-file -)
  afk-forge issue edit <n> [--add-label <a,b>]... [--remove-label <a,b>]... [--title <title>] [--body <text> | --body-file -]
--body-file - reads the text from stdin; --label, --add-label and --remove-label may repeat or list labels with commas;
--title and --body only on issues created by this Chat that are not ready-for-agent`

// errForgeUsage answers arguments afk-forge does not take.
var errForgeUsage = errors.New(forgeUsage)

// forgeCall is a request to the channel: what a subcommand of afk-forge asks for.
type forgeCall struct {
	method, path string
	body         any
}

// forgeCommands parse the arguments after `issue <subcommand>` into the call.
var forgeCommands = map[string]func(args []string, stdin io.Reader) (call forgeCall, err error){
	"view":    parseView,
	"list":    parseList,
	"create":  parseCreate,
	"comment": parseComment,
	"edit":    parseEdit,
}

// RunForge executes `afk-forge args` against the channel at socket and writes
// the result to stdout and the channel's warning to stderr; stdin is the text
// of --body-file -.
func RunForge(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	var parse func(args []string, stdin io.Reader) (call forgeCall, err error)
	if len(args) >= 2 && args[0] == "issue" {
		parse = forgeCommands[args[1]]
	}
	if parse == nil {
		err = errForgeUsage
		return
	}
	call, err := parse(args[2:], stdin)
	if err != nil {
		return
	}
	err = sendForge(ctx, socket, call, stdout, stderr)
	return
}

// issueArg splits args into the issue number in front, as the path of the
// issue, and the rest.
func issueArg(args []string) (path string, rest []string, err error) {
	if len(args) == 0 {
		err = errForgeUsage
		return
	}
	if _, err = issueNumber(args[0]); err != nil {
		err = errForgeUsage
		return
	}
	path, rest = "/issues/"+args[0], args[1:]
	return
}

// parseView parses `issue view <n>`.
func parseView(args []string, _ io.Reader) (call forgeCall, err error) {
	path, rest, err := issueArg(args)
	if err == nil && len(rest) != 0 {
		err = errForgeUsage
	}
	call = forgeCall{method: http.MethodGet, path: path}
	return
}

// parseList parses `issue list [--label <label>] [--state <state>]`.
func parseList(args []string, _ io.Reader) (call forgeCall, err error) {
	flags := flag.NewFlagSet("issue list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	label := flags.String("label", "", "")
	state := flags.String("state", string(forge.StateOpen), "")
	if err = flags.Parse(args); err == nil && flags.NArg() == 0 {
		_, err = forge.ParseState(*state)
	}
	if err != nil || flags.NArg() != 0 {
		err = errForgeUsage
		return
	}
	call = forgeCall{method: http.MethodGet, path: "/issues?label=" + url.QueryEscape(*label) + "&state=" + url.QueryEscape(*state)}
	return
}

// parseCreate parses `issue create`.
func parseCreate(args []string, stdin io.Reader) (call forgeCall, err error) {
	var body string
	req := createRequest{}
	flags, text := textFlags("issue create", &body, stdin)
	flags.StringVar(&req.Title, "title", "", "")
	labelsFlag(flags, "label", &req.Labels)
	if err = flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(req.Title) == "" {
		err = errForgeUsage
		return
	}
	if req.Body, err = text(); err != nil {
		return
	}
	call = forgeCall{method: http.MethodPost, path: "/issues", body: req}
	return
}

// parseComment parses `issue comment <n>`.
func parseComment(args []string, stdin io.Reader) (call forgeCall, err error) {
	path, rest, err := issueArg(args)
	if err != nil {
		return
	}
	var body string
	flags, text := textFlags("issue comment", &body, stdin)
	if flags.Parse(rest) != nil || flags.NArg() != 0 {
		err = errForgeUsage
		return
	}
	if body, err = text(); err != nil {
		return
	}
	if strings.TrimSpace(body) == "" {
		err = errForgeUsage
		return
	}
	call = forgeCall{method: http.MethodPost, path: path + "/comments", body: commentRequest{Body: body}}
	return
}

// parseEdit parses `issue edit <n>`: only the title and body given go in
// the request, an absent one stays as it is.
func parseEdit(args []string, stdin io.Reader) (call forgeCall, err error) {
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
		err = errForgeUsage
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
	call = forgeCall{method: http.MethodPatch, path: path, body: req}
	return
}

// sendForge sends call over the channel at socket and writes the reply: the
// output to stdout, the warning to stderr.
func sendForge(ctx context.Context, socket string, call forgeCall, stdout, stderr io.Writer) (err error) {
	client := &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (conn net.Conn, err error) {
			conn, err = (&net.Dialer{}).DialContext(ctx, "unix", socket)
			return
		},
	}}
	var payload io.Reader
	if call.body != nil {
		var data []byte
		if data, err = json.Marshal(call.body); err != nil {
			err = fmt.Errorf("encoding the request: %w", err)
			return
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, call.method, "http://afk"+call.path, payload)
	if err != nil {
		err = fmt.Errorf("building the request: %w", err)
		return
	}
	resp, err := client.Do(req)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		err = errors.New("no channel to afk (works only in an `afk chat attach` session)")
		return
	}
	if err != nil {
		err = fmt.Errorf("calling afk: %w", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var reply forgeReply
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		err = fmt.Errorf("reading the reply of afk: %w", err)
		return
	}
	if reply.Error != "" {
		err = errors.New(reply.Error)
		return
	}
	if reply.Warning != "" {
		_, _ = fmt.Fprintln(stderr, reply.Warning)
	}
	_, err = io.WriteString(stdout, reply.Output)
	return
}

// textFlags is the flag set of a command that takes a text: --body, or
// --body-file - for stdin. text returns it once the set is parsed.
func textFlags(name string, body *string, stdin io.Reader) (flags *flag.FlagSet, text func() (text string, err error)) {
	flags = flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(body, "body", "", "")
	file := flags.String("body-file", "", "")
	text = func() (text string, err error) {
		switch {
		case *file == "":
			text = *body
		case *file != "-" || *body != "":
			err = errForgeUsage
		default:
			var data []byte
			data, err = io.ReadAll(stdin)
			text = string(data)
		}
		return
	}
	return
}

// labelsFlag defines flag name, which may repeat and list labels with commas.
func labelsFlag(flags *flag.FlagSet, name string, labels *[]string) {
	flags.Func(name, "", func(value string) (err error) {
		*labels = append(*labels, strings.Split(value, ",")...)
		return
	})
}
