package chat

import (
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

	forgeUsage = "usage: afk-forge issue view <n> | afk-forge issue list [--label <label>] [--state open|closed|all]"
)

// --- host: the channel ---

// forgeServer answers the guest's afk-forge calls with the Project's Forge,
// one JSON document per call: {"output": …} or {"error": …}. Every call goes
// to log.
type forgeServer struct {
	forge forge.Forge
	log   *log.Logger
}

func newForgeServer(f forge.Forge, logTo io.Writer) (s *forgeServer) {
	return &forgeServer{forge: f, log: log.New(logTo, "", log.LstdFlags)}
}

func (s *forgeServer) handler() (handler http.Handler) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /issues/{n}", s.view)
	mux.HandleFunc("GET /issues", s.list)
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
	ticket, open, err := s.forge.Peek(r.Context(), n)
	if err == nil && open {
		ticket, err = s.forge.Ticket(r.Context(), n)
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
	tickets, err := s.forge.List(r.Context(), label, state)
	var b strings.Builder
	for _, ticket := range tickets {
		fmt.Fprintf(&b, "#%d  [%s]  %s\n", ticket.Number, strings.Join(ticket.Labels, ", "), ticket.Title)
	}
	s.reply(w, op, agent.Sanitize(b.String()), http.StatusBadGateway, err)
}

// reply logs the call and answers with output, or with failure (status) when err is set.
func (s *forgeServer) reply(w http.ResponseWriter, op, output string, failure int, err error) {
	body := map[string]string{"output": output}
	if err != nil {
		s.log.Printf("%s: error: %v", op, err)
		w.WriteHeader(failure)
		body = map[string]string{"error": agent.Sanitize(err.Error())}
	} else {
		s.log.Printf("%s: ok", op)
	}
	_ = json.NewEncoder(w).Encode(body)
}

// serveForge puts this binary in the guest as afk-forge and serves the Forge
// over a reverse unix socket on client, logging each call to logPath; no port
// is opened on the host. stop closes the socket, which then disappears from
// the guest, and waits for the server.
func serveForge(ctx context.Context, client *ssh.Client, f forge.Forge, logPath string) (stop func(), err error) {
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
	server := &http.Server{Handler: newForgeServer(f, logFile).handler(), BaseContext: func(net.Listener) context.Context { return ctx }}
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
// the result to stdout.
func RunForge(ctx context.Context, socket string, args []string, stdout io.Writer) (err error) {
	var path string
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
	default:
		err = errors.New(forgeUsage)
		return
	}

	client := &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (conn net.Conn, err error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://afk"+path, nil)
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
	var reply struct{ Output, Error string }
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		err = fmt.Errorf("reading the reply of afk: %w", err)
		return
	}
	if reply.Error != "" {
		err = errors.New(reply.Error)
		return
	}
	_, err = io.WriteString(stdout, reply.Output)
	return
}
