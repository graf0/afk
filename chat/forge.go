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

	forgeSocketDir = "/run/afk"

	maxForgeBody = 1 << 20

	// issuesName is the file of the Chat's state with the numbers of the issues it created, a line each.
	issuesName = "issues"

	forgeUsage = `usage:
  afk-forge issue view <n>
  afk-forge issue list [--label <label>] [--state open|closed|all]
  afk-forge issue create --title <title> [--body <text> | --body-file -] [--label <a,b>]...
  afk-forge issue comment <n> (--body <text> | --body-file -)
--body-file - reads the text from stdin; --label may repeat or list labels with commas`
)

// --- host: the channel ---

// forgeChannel is what a Chat's afk-forge works on: the Project's Forge and
// labels, the Chat's name (the footer of what it writes) and the file in its
// state where the issues it creates are noted.
type forgeChannel struct {
	Forge      forge.Forge
	Labels     config.Labels
	Chat       string
	IssuesPath string
}

// forgeServer answers the guest's afk-forge calls with the Project's Forge,
// one JSON document per call: {"output": …}, {"error": …}, and a "warning"
// for the guest's stderr. Every call goes to log.
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
	return mux
}

// view serves `issue view <n>`: the ticket as the TICKET.md of a Run.
func (s *forgeServer) view(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n <= 0 {
		s.reply(w, "view "+r.PathValue("n"), "", http.StatusBadRequest, errors.New("invalid issue number"))
		return
	}
	// Ticket refuses closed issues, which `issue list --state closed` shows: those come without comments.
	ticket, open, err := s.Forge.Peek(r.Context(), n)
	if err == nil && open {
		ticket, err = s.Forge.Ticket(r.Context(), n)
	}
	s.reply(w, fmt.Sprintf("view #%d", n), agent.Sanitize(agent.TicketFile(ticket)), http.StatusBadGateway, err)
}

// list serves `issue list`: a line `#N  [labels]  title` per issue.
func (s *forgeServer) list(w http.ResponseWriter, r *http.Request) {
	label, state := r.URL.Query().Get("label"), r.URL.Query().Get("state")
	op := fmt.Sprintf("list label=%q state=%s", label, state)
	if state != "open" && state != "closed" && state != "all" {
		s.reply(w, op, "", http.StatusBadRequest, errors.New("state: want open, closed or all"))
		return
	}
	tickets, err := s.Forge.List(r.Context(), label, state)
	var b strings.Builder
	for _, ticket := range tickets {
		fmt.Fprintf(&b, "#%d  [%s]  %s\n", ticket.Number, strings.Join(ticket.Labels, ", "), ticket.Title)
	}
	s.reply(w, op, agent.Sanitize(b.String()), http.StatusBadGateway, err)
}

// create serves `issue create`: the issue gets the footer, and the Chat notes
// its number. ready-for-agent becomes needs-triage: readiness for the Factory
// is for a human to give.
func (s *forgeServer) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title, Body string
		Labels      []string
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxForgeBody)).Decode(&in)
	if err == nil && strings.TrimSpace(in.Title) == "" {
		err = errors.New("title: required")
	}
	if err != nil {
		s.reply(w, "create", "", http.StatusBadRequest, err)
		return
	}
	labels, warning := s.triageLabels(in.Labels)
	known, err := s.Forge.Labels(r.Context())
	if err == nil {
		for _, label := range labels {
			if !forge.ContainsLabel(known, label) {
				err = fmt.Errorf("label %q does not exist in the repository", label)
				break
			}
		}
	}
	var n int
	if err == nil {
		// One line: AgentText may add a truncation note on a line of its own.
		title := strings.Join(strings.Fields(run.AgentText(in.Title)), " ")
		n, err = s.Forge.CreateIssue(r.Context(), title, s.withFooter(in.Body), labels)
	}
	op := fmt.Sprintf("create labels=%q", labels)
	if warning != "" {
		op += " (" + s.Labels.ReadyForAgent + " replaced)"
	}
	if err == nil {
		op = fmt.Sprintf("create #%d", n) + strings.TrimPrefix(op, "create")
		if err = s.noteIssue(n); err != nil {
			err = fmt.Errorf("issue #%d created, but not noted in the Chat: %w", n, err)
		}
	}
	s.replyWarning(w, op, fmt.Sprintf("#%d\n", n), warning, http.StatusBadGateway, err)
}

// comment serves `issue comment <n>`: the comment gets the footer.
func (s *forgeServer) comment(w http.ResponseWriter, r *http.Request) {
	var in struct{ Body string }
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n <= 0 {
		s.reply(w, "comment "+r.PathValue("n"), "", http.StatusBadRequest, errors.New("invalid issue number"))
		return
	}
	op := fmt.Sprintf("comment #%d", n)
	err = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxForgeBody)).Decode(&in)
	if err == nil && strings.TrimSpace(in.Body) == "" {
		err = errors.New("body: required")
	}
	if err != nil {
		s.reply(w, op, "", http.StatusBadRequest, err)
		return
	}
	s.reply(w, op, "", http.StatusBadGateway, s.Forge.Comment(r.Context(), n, s.withFooter(in.Body)))
}

// triageLabels replaces ready-for-agent with needs-triage (the names of the
// Project's [labels]) and says so in warning.
func (s *forgeServer) triageLabels(labels []string) (out []string, warning string) {
	for _, label := range labels {
		if label = strings.TrimSpace(label); label == "" {
			continue
		}
		if strings.EqualFold(label, s.Labels.ReadyForAgent) {
			warning = fmt.Sprintf("label %s zamieniony na %s: gotowość dla Factory nadaje człowiek", s.Labels.ReadyForAgent, s.Labels.NeedsTriage)
			label = s.Labels.NeedsTriage
		}
		if !forge.ContainsLabel(out, label) {
			out = append(out, label)
		}
	}
	return
}

// withFooter sanitizes text like the Agent's text of a Run and signs it with
// the Chat, after any truncation so the footer always stays.
func (s *forgeServer) withFooter(text string) (signed string) {
	signed = run.AgentText(text) + "\n\n— z Chat " + s.Chat
	return
}

// noteIssue appends n to the issues of the Chat.
func (s *forgeServer) noteIssue(n int) (err error) {
	file, err := os.OpenFile(s.IssuesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, err = fmt.Fprintln(file, n)
	return errors.Join(err, file.Close())
}

// reply logs the call and answers with output, or with failure (status) when err is set.
func (s *forgeServer) reply(w http.ResponseWriter, op, output string, failure int, err error) {
	s.replyWarning(w, op, output, "", failure, err)
}

// replyWarning is reply with a warning for the guest's stderr.
func (s *forgeServer) replyWarning(w http.ResponseWriter, op, output, warning string, failure int, err error) {
	body := map[string]string{"output": output, "warning": warning}
	if err != nil {
		s.log.Printf("%s: error: %v", op, err)
		w.WriteHeader(failure)
		body = map[string]string{"error": agent.Sanitize(err.Error())}
	} else {
		s.log.Printf("%s: ok", op)
	}
	_ = json.NewEncoder(w).Encode(body)
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

// RunForge executes `afk-forge args` against the channel at socket and writes
// the result to stdout and the channel's warning to stderr; stdin is the text
// of --body-file -.
func RunForge(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	method, path, in := http.MethodGet, "", any(nil)
	switch {
	case len(args) == 3 && args[0] == "issue" && args[1] == "view":
		if n, convErr := strconv.Atoi(args[2]); convErr != nil || n <= 0 {
			err = errors.New(forgeUsage)
			return
		}
		path = "/issues/" + args[2]
	case len(args) >= 2 && args[0] == "issue" && args[1] == "list":
		fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		label := fs.String("label", "", "")
		state := fs.String("state", "open", "")
		if err = fs.Parse(args[2:]); err != nil || fs.NArg() != 0 {
			err = errors.New(forgeUsage)
			return
		}
		path = "/issues?label=" + url.QueryEscape(*label) + "&state=" + url.QueryEscape(*state)
	case len(args) >= 2 && args[0] == "issue" && args[1] == "create":
		var title, body string
		var labels []string
		fs, text := textFlags("issue create", &body, stdin)
		fs.StringVar(&title, "title", "", "")
		fs.Func("label", "", func(value string) error {
			labels = append(labels, strings.Split(value, ",")...)
			return nil
		})
		if err = fs.Parse(args[2:]); err != nil || fs.NArg() != 0 || strings.TrimSpace(title) == "" {
			err = errors.New(forgeUsage)
			return
		}
		if body, err = text(); err != nil {
			return
		}
		method, path, in = http.MethodPost, "/issues", map[string]any{"title": title, "body": body, "labels": labels}
	case len(args) >= 3 && args[0] == "issue" && args[1] == "comment":
		var body string
		n, convErr := strconv.Atoi(args[2])
		fs, text := textFlags("issue comment", &body, stdin)
		if convErr != nil || n <= 0 || fs.Parse(args[3:]) != nil || fs.NArg() != 0 {
			err = errors.New(forgeUsage)
			return
		}
		if body, err = text(); err != nil {
			return
		}
		if strings.TrimSpace(body) == "" {
			err = errors.New(forgeUsage)
			return
		}
		method, path, in = http.MethodPost, "/issues/"+args[2]+"/comments", map[string]string{"body": body}
	default:
		err = errors.New(forgeUsage)
		return
	}

	client := &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (conn net.Conn, err error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	var payload io.Reader
	if in != nil {
		var data []byte
		if data, err = json.Marshal(in); err != nil {
			return
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://afk"+path, payload)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		// Polish, as the ticket words it: the reader is the human in the Chat.
		err = errors.New("kanał do afk niedostępny (działa tylko w sesji `afk chat attach`)")
		return
	}
	if err != nil {
		err = fmt.Errorf("calling afk: %w", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var reply struct{ Output, Warning, Error string }
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
func textFlags(name string, body *string, stdin io.Reader) (fs *flag.FlagSet, text func() (text string, err error)) {
	fs = flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(body, "body", "", "")
	file := fs.String("body-file", "", "")
	text = func() (text string, err error) {
		switch {
		case *file == "":
			text = *body
		case *file != "-" || *body != "":
			err = errors.New(forgeUsage)
		default:
			var data []byte
			data, err = io.ReadAll(stdin)
			text = string(data)
		}
		return
	}
	return
}
