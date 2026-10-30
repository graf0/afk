package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// SocketEnv names the variable that tells afk-forge in the Sandbox where
	// the channel socket is; ForgeSocket is the default.
	SocketEnv = "AFK_SOCKET"

	// ForgeSocket is where the channel to afk appears inside the Sandbox: the
	// host's directory of the Chat's socket, mounted at /run/afk (ADR-0012).
	ForgeSocket = "/run/afk/afk.sock"

	// maxSocketPath is what a sun_path holds, with its terminating zero.
	maxSocketPath = 107
)

// routeGroup registers the routes of one part of the channel (tickets today;
// pull requests, the Stack and the test VMs are added the same way) on mux.
// Every call of a route goes to logger.
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
		_ = jsonEncode(w, forgeReply{Error: "no such route"})
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
