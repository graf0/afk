package chat

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// socketClient is an HTTP client that talks to the unix socket.
func socketClient(socket string) (client *http.Client) {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (conn net.Conn, err error) {
		conn, err = (&net.Dialer{}).DialContext(ctx, "unix", socket)
		return
	}}}
}

func get(t *testing.T, client *http.Client, method, path string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://afk"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func TestChannelUnknownRoute(t *testing.T) {
	socket, logPath, _ := serveFake(t, fakeForge{})
	client := socketClient(socket)
	// A known path with another method falls to "/" as well: 404, not 405.
	for _, tc := range []struct{ method, path string }{{"DELETE", "/issues/3"}, {"GET", "/nope"}} {
		if status, _ := get(t, client, tc.method, tc.path); status != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 404", tc.method, tc.path, status)
		}
	}
	logged, _ := os.ReadFile(logPath)
	for _, want := range []string{"DELETE /issues/3: error: no such route", "GET /nope: error: no such route"} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("log lacks %q:\n%s", want, logged)
		}
	}
}

// A route group of another part of the channel (pull requests, the Stack, the
// test VMs) is added next to the tickets without touching the server.
func TestServeChannelGroups(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "afk.sock")
	var logged bytes.Buffer
	extra := func(mux *http.ServeMux, logger *log.Logger) {
		mux.HandleFunc("POST /pr", func(w http.ResponseWriter, r *http.Request) {
			logger.Print("pr: ok")
			_ = jsonEncode(w, channelReply{Output: "opened"})
		})
	}
	stop, err := serveChannel(t.Context(), socket, &logged, issueRoutes(issueChannel{Forge: fakeForge{}}), extra)
	if err != nil {
		t.Fatal(err)
	}
	client := socketClient(socket)
	if status, body := get(t, client, "POST", "/pr"); status != http.StatusOK || !strings.Contains(body, "opened") {
		t.Errorf("POST /pr = %d %q", status, body)
	}
	if status, _ := get(t, client, "GET", "/issues?state=open"); status != http.StatusOK {
		t.Errorf("GET /issues = %d: the ticket routes are gone", status)
	}
	if !strings.Contains(logged.String(), "pr: ok") {
		t.Errorf("log = %q", logged.String())
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("socket: %v, %v; want mode 0600", info, err)
	}
	stop()
	if _, err = os.Stat(socket); err == nil {
		t.Error("the socket stays after stop")
	}
}

func TestServeChannelReplacesStaleSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "afk.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil { // left by a process that died
		t.Fatal(err)
	}
	stop, err := serveChannel(t.Context(), socket, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if status, _ := get(t, socketClient(socket), "GET", "/x"); status != http.StatusNotFound {
		t.Errorf("status %d", status)
	}
}

func TestServeChannelLongPath(t *testing.T) {
	socket := filepath.Join(t.TempDir(), strings.Repeat("a", 120), "afk.sock")
	_, err := serveChannel(t.Context(), socket, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "shorter") {
		t.Errorf("err = %v, want a refusal that says what to do", err)
	}
}
