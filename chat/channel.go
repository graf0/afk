package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/graf0/afk/agent"
)

const (
	// CLISocket is where the channel to afk appears inside the Sandbox: the
	// host's directory of the Chat's socket, mounted at /run/afk (ADR-0012).
	CLISocket = "/run/afk/afk.sock"

	// maxSocketPath is what a sun_path holds, with its terminating zero.
	maxSocketPath = 107

	// maxRequestBody caps the JSON body of a call.
	maxRequestBody = 1 << 20
)

// channelReply is the one JSON document the host answers each call with.
type channelReply struct {
	Output  string `json:"output,omitempty"`
	Data    []byte `json:"data,omitempty"` // binary output (a screenshot), base64 in the JSON
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
}

// responder answers the calls of a route group, one channelReply per call,
// and logs each.
type responder struct {
	log *log.Logger
}

// routeGroup registers the routes of one part of the channel (tickets, pull
// requests, the Stack, the test VMs) on mux. Every call of a route goes to logger.
type routeGroup func(mux *http.ServeMux, logger *log.Logger)

// channelHandler is the channel's HTTP handler: the routes of groups, and 404
// for everything else, logged to logTo like any call.
func channelHandler(logTo io.Writer, groups ...routeGroup) (handler http.Handler) {
	logger := log.New(logTo, "", log.LstdFlags)
	mux := http.NewServeMux()
	for _, register := range groups {
		register(mux, logger)
	}
	// Patterns with a method win over "/", but "/" matches every method: a
	// known path with another method lands here too (404, not the mux's 405).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		logger.Printf("%s %s: error: no such route", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_ = jsonEncode(w, channelReply{Error: "no such route"})
	})
	return mux
}

// serveChannel serves groups over the unix socket at socket, which only this
// process makes and only the Sandbox gets (mounted), logging each call to
// logTo. No port is opened on the host. A socket left by a process that died
// is replaced: the caller holds the Chat's lock. stop closes the socket and
// waits for the server.
func serveChannel(ctx context.Context, socket string, logTo io.Writer, groups ...routeGroup) (stop func(), err error) {
	if len(socket) > maxSocketPath {
		err = fmt.Errorf("the channel socket path %s is %d characters, over the %d a unix socket takes: use a shorter XDG_RUNTIME_DIR", socket, len(socket), maxSocketPath)
		return
	}
	if err = os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		err = fmt.Errorf("creating %s: %w", filepath.Dir(socket), err)
		return
	}
	if err = os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("removing the old channel socket: %w", err)
		return
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		err = fmt.Errorf("opening the channel on %s: %w", socket, err)
		return
	}
	if err = os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		err = fmt.Errorf("securing %s: %w", socket, err)
		return
	}
	server := &http.Server{
		Handler:           channelHandler(logTo, groups...),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: time.Minute,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(ln)
	}()
	stop = func() {
		_ = server.Close() // closes ln too, which unlinks the socket
		<-done
	}
	return
}

// decode reads the JSON body of r into in; on failure it answers 400 and ok
// is false.
func (s responder) decode(w http.ResponseWriter, r *http.Request, op string, in any) (ok bool) {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(in); err != nil {
		s.reply(w, op, "", "", http.StatusBadRequest, fmt.Errorf("decoding the request: %w", err))
		return
	}
	ok = true
	return
}

// reply logs the call and answers with output and a warning for the Sandbox's
// stderr, or with failure (status) when err is set.
func (s responder) reply(w http.ResponseWriter, op, output, warning string, failure int, err error) {
	body := channelReply{Output: output, Warning: warning}
	if err != nil {
		s.log.Printf("%s: error: %v", op, err)
		w.WriteHeader(failure)
		body = channelReply{Error: agent.Sanitize(err.Error())}
	} else {
		s.log.Printf("%s: ok", op)
	}
	_ = jsonEncode(w, body)
}

// jsonEncode writes v as the one JSON document of a reply.
func jsonEncode(w io.Writer, v any) (err error) { return json.NewEncoder(w).Encode(v) }
