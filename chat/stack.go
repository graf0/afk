package chat

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/graf0/afk/agent"
	"github.com/graf0/afk/stack"
)

const (
	// defaultLogTail and maxLogTail are the lines of a service's log a call
	// gets by default and at most.
	defaultLogTail = 100
	maxLogTail     = 1000

	// maxOutputBytes is the most of an output (a log, a restart, a reseed) a
	// reply carries: its end.
	maxOutputBytes = 256 << 10
)

// --- host: the Stack routes of the channel ---

// stackControl is what the Stack routes ask of the Chat's Stack. A service
// name is passed on only after it was found in Services. Output goes to the
// writer given, never to the terminal claude holds.
type stackControl interface {
	Services(ctx context.Context) (services []string, err error)
	Logs(ctx context.Context, service string, tail int, stdout io.Writer) (err error)
	Restart(ctx context.Context, service string, out io.Writer) (err error)
	Reseed(ctx context.Context, out io.Writer) (err error)
}

// stackServer answers the Sandbox's `afk-forge stack` calls. Restarts and
// reseeds go one at a time: they change the same services.
type stackServer struct {
	*forgeServer
	st stackControl
	mu sync.Mutex
}

// outputTail keeps the last max bytes written to it.
type outputTail struct {
	max int
	buf []byte
	cut bool
}

// stackRoutes is the Stack part of the channel.
func stackRoutes(st stackControl) (group routeGroup) {
	return func(mux *http.ServeMux, logger *log.Logger) {
		s := &stackServer{forgeServer: &forgeServer{log: logger}, st: st}
		mux.HandleFunc("GET /stack/services/{service}/logs", s.logs)
		mux.HandleFunc("POST /stack/services/{service}/restart", s.restart)
		mux.HandleFunc("POST /stack/reseed", s.reseed)
	}
}

// service is the service of r's path if the Stack has it; otherwise it
// answers 400 with the services there are, and ok is false. The name comes
// from the Agent: only a name the Stack lists is ever passed on.
func (s *stackServer) service(w http.ResponseWriter, r *http.Request, op string) (service string, ok bool) {
	service = r.PathValue("service")
	services, err := s.st.Services(r.Context())
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadGateway, err)
		return
	}
	if !slices.Contains(services, service) {
		err = fmt.Errorf("unknown service %q: the Stack has %s", service, strings.Join(services, ", "))
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	ok = true
	return
}

// logs serves `stack logs <service>`: the end of the service's log, the
// last tail lines.
func (s *stackServer) logs(w http.ResponseWriter, r *http.Request) {
	op := "stack logs " + strconv.Quote(r.PathValue("service"))
	tail, err := parseTail(r.URL.Query().Get("tail"))
	if err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, err)
		return
	}
	service, ok := s.service(w, r, op)
	if !ok {
		return
	}
	op = fmt.Sprintf("stack logs %s tail=%d", service, tail)
	out := &outputTail{max: maxOutputBytes}
	err = s.st.Logs(r.Context(), service, tail, out)
	s.reply(w, op, out.String(), "", http.StatusBadGateway, err)
}

// parseTail is the number of lines asked for: empty is the default.
func parseTail(value string) (tail int, err error) {
	if value == "" {
		tail = defaultLogTail
		return
	}
	tail, err = strconv.Atoi(value)
	if err != nil || tail < 1 || tail > maxLogTail {
		err = fmt.Errorf("tail: a number of lines from 1 to %d", maxLogTail)
	}
	return
}

// restart serves `stack restart <service>`: any service but the Sandbox and
// the one-shot afk-setup, which has no container to restart.
func (s *stackServer) restart(w http.ResponseWriter, r *http.Request) {
	op := "stack restart " + strconv.Quote(r.PathValue("service"))
	service, ok := s.service(w, r, op)
	if !ok {
		return
	}
	op = "stack restart " + service
	switch service {
	case stack.SandboxService:
		s.reply(w, op, "", "", http.StatusBadRequest, fmt.Errorf("service %s is the Sandbox you work in: it is not restarted", service))
		return
	case stack.SetupService:
		s.reply(w, op, "", "", http.StatusBadRequest, fmt.Errorf("service %s runs once, it has nothing to restart: use stack reseed", service))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &outputTail{max: maxOutputBytes}
	err := s.st.Restart(r.Context(), service, out)
	s.reply(w, op, out.String()+"restarted "+service+"\n", "", http.StatusBadGateway, err)
}

// reseed serves `stack reseed`: afk-setup runs again; its output is the reply.
func (s *stackServer) reseed(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &outputTail{max: maxOutputBytes}
	err := s.st.Reseed(r.Context(), out)
	status := http.StatusBadGateway
	if errors.Is(err, stack.ErrNoSetup) {
		status = http.StatusBadRequest
	}
	s.reply(w, "stack reseed", out.String()+"reseeded\n", "", status, err)
}

func (t *outputTail) Write(p []byte) (n int, err error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf, t.cut = t.buf[len(t.buf)-t.max:], true
	}
	n = len(p)
	return
}

// String is the output kept, sanitized; when its beginning was cut, from
// its first whole line, after a note.
func (t *outputTail) String() (text string) {
	buf := t.buf
	if t.cut {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
		text = fmt.Sprintf("[earlier output cut: the last %d KiB follow]\n", t.max>>10)
	}
	text += agent.Sanitize(string(buf))
	return
}

// --- Sandbox: afk-forge stack ---

// parseStackLogs parses `stack logs <service> [--tail <n>]`. The number is
// checked by afk, not here: the channel does not trust its caller.
func parseStackLogs(args []string, _ io.Reader) (call forgeCall, err error) {
	if len(args) == 0 {
		err = errForgeUsage
		return
	}
	flags := flag.NewFlagSet("stack logs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tail := flags.String("tail", "", "")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		err = errForgeUsage
		return
	}
	path := "/stack/services/" + url.PathEscape(args[0]) + "/logs"
	if *tail != "" {
		path += "?tail=" + url.QueryEscape(*tail)
	}
	call = forgeCall{method: http.MethodGet, path: path}
	return
}

// parseStackRestart parses `stack restart <service>`.
func parseStackRestart(args []string, _ io.Reader) (call forgeCall, err error) {
	if len(args) != 1 {
		err = errForgeUsage
		return
	}
	call = forgeCall{method: http.MethodPost, path: "/stack/services/" + url.PathEscape(args[0]) + "/restart"}
	return
}

// parseStackReseed parses `stack reseed`.
func parseStackReseed(args []string, _ io.Reader) (call forgeCall, err error) {
	if len(args) != 0 {
		err = errForgeUsage
		return
	}
	call = forgeCall{method: http.MethodPost, path: "/stack/reseed"}
	return
}
