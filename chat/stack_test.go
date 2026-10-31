package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/graf0/afk/stack"
)

// controlStack is a Stack that records the calls of the stack routes.
type controlStack struct {
	mu          sync.Mutex
	services    []string
	servicesErr error
	calls       []string
	logs        string
	setupErr    error
}

func (f *controlStack) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *controlStack) Services(context.Context) (services []string, err error) {
	return f.services, f.servicesErr
}

func (f *controlStack) Logs(_ context.Context, service string, tail int, stdout io.Writer) (err error) {
	f.record(fmt.Sprintf("logs %s %d", service, tail))
	_, err = io.WriteString(stdout, f.logs)
	return
}

func (f *controlStack) Restart(_ context.Context, service string, out io.Writer) (err error) {
	f.record("restart " + service)
	_, err = io.WriteString(out, "Container "+service+" Restarting\n")
	return
}

func (f *controlStack) Reseed(_ context.Context, out io.Writer) (err error) {
	f.record("reseed")
	if f.setupErr != nil {
		return f.setupErr
	}
	_, err = io.WriteString(out, "seeded\n")
	return
}

func serveStack(t *testing.T, st *controlStack) (socket string) {
	t.Helper()
	socket = filepath.Join(t.TempDir(), "afk.sock")
	stop, err := serveChannel(t.Context(), socket, io.Discard, stackRoutes(st))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return
}

func callStack(t *testing.T, socket string, args ...string) (out, warning string, err error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err = RunCLI(t.Context(), socket, args, strings.NewReader(""), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// status is the HTTP status the Stack routes answer method path with.
func status(st *controlStack, method, path string) (code int) {
	rec := httptest.NewRecorder()
	channelHandler(io.Discard, stackRoutes(st)).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

func newControlStack() (st *controlStack) {
	return &controlStack{services: []string{"afk", "afk-setup", "db", "web"}, logs: "line 1\nline 2\n"}
}

func TestStackLogs(t *testing.T) {
	st := newControlStack()
	socket := serveStack(t, st)

	out, _, err := callStack(t, socket, "stack", "logs", "web")
	if err != nil || out != st.logs {
		t.Fatalf("logs = %q, %v", out, err)
	}
	if _, _, err = callStack(t, socket, "stack", "logs", "afk", "--tail", "7"); err != nil {
		t.Fatalf("logs of afk: %v", err)
	}
	maxTail := fmt.Sprint(maxLogTail)
	if _, _, err = callStack(t, socket, "stack", "logs", "db", "--tail", maxTail); err != nil {
		t.Fatalf("logs at the maximum: %v", err)
	}
	want := []string{fmt.Sprintf("logs web %d", defaultLogTail), "logs afk 7", "logs db " + maxTail}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls = %q, want %q", st.calls, want)
	}
}

func TestStackLogsSanitizes(t *testing.T) {
	st := newControlStack()
	st.logs = "ok\x1b[31m red\n"
	out, _, err := callStack(t, serveStack(t, st), "stack", "logs", "web")
	if err != nil || strings.Contains(out, "\x1b") {
		t.Errorf("logs = %q, %v: want no escape sequences", out, err)
	}
}

func TestStackLogsRejects(t *testing.T) {
	st := newControlStack()
	socket := serveStack(t, st)
	tests := map[string][]string{
		"unknown":       {"nope"},
		"option":        {"--follow"},
		"shell":         {"web; id"},
		"tail zero":     {"web", "--tail", "0"},
		"tail negative": {"web", "--tail", "-1"},
		"tail over":     {"web", "--tail", fmt.Sprint(maxLogTail + 1)},
		"tail text":     {"web", "--tail", "all"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := callStack(t, socket, append([]string{"stack", "logs"}, args...)...)
			if err == nil {
				t.Fatal("want an error")
			}
			if name == "unknown" && !strings.Contains(err.Error(), "afk, afk-setup, db, web") {
				t.Errorf("err = %v, want the allowed services", err)
			}
		})
	}
	if len(st.calls) != 0 {
		t.Errorf("calls = %q, want none", st.calls)
	}
}

func TestStackLogsStatus(t *testing.T) {
	if got := status(newControlStack(), http.MethodGet, "/stack/services/nope/logs"); got != http.StatusBadRequest {
		t.Errorf("unknown service: status = %d, want 400", got)
	}
	st := newControlStack()
	st.servicesErr = errors.New("compose down")
	if got := status(st, http.MethodGet, "/stack/services/web/logs"); got != http.StatusBadGateway {
		t.Errorf("Services failing: status = %d, want 502", got)
	}
}

func TestStackLogsCut(t *testing.T) {
	st := newControlStack()
	st.logs = "partial line\n" + strings.Repeat("x", maxOutputBytes-1) + "\n"
	out, _, err := callStack(t, serveStack(t, st), "stack", "logs", "web")
	if err != nil || strings.Contains(out, "partial") || !strings.HasPrefix(out, "[earlier output cut") {
		t.Errorf("logs = %.80q, %v: want the end from a whole line, after a note", out, err)
	}
}

func TestStackRestart(t *testing.T) {
	st := newControlStack()
	socket := serveStack(t, st)

	out, _, err := callStack(t, socket, "stack", "restart", "web")
	if err != nil || out != "Container web Restarting\nrestarted web\n" {
		t.Fatalf("restart = %q, %v", out, err)
	}
	tests := map[string]string{"afk": "Sandbox", "afk-setup": "stack reseed", "nope": "unknown", "web;id": "unknown"}
	for service, want := range tests {
		if _, _, err := callStack(t, socket, "stack", "restart", service); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("restart %q: err = %v, want one about %q", service, err, want)
		}
	}
	if want := []string{"restart web"}; !slices.Equal(st.calls, want) {
		t.Errorf("calls = %q, want %q", st.calls, want)
	}
}

func TestStackReseed(t *testing.T) {
	st := newControlStack()
	socket := serveStack(t, st)
	if out, _, err := callStack(t, socket, "stack", "reseed"); err != nil || out != "seeded\nreseeded\n" {
		t.Fatalf("reseed = %q, %v", out, err)
	}
	if want := []string{"reseed"}; !slices.Equal(st.calls, want) {
		t.Errorf("calls = %q, want %q", st.calls, want)
	}
}

func TestStackReseedWithoutSetup(t *testing.T) {
	st := newControlStack()
	st.setupErr = stack.ErrNoSetup
	_, _, err := callStack(t, serveStack(t, st), "stack", "reseed")
	if err == nil || !strings.Contains(err.Error(), "afk-setup") {
		t.Errorf("err = %v, want a clear error about afk-setup", err)
	}
	if got := status(st, http.MethodPost, "/stack/reseed"); got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got)
	}
}

func TestStackUsage(t *testing.T) {
	socket := serveStack(t, newControlStack())
	for _, args := range [][]string{{"stack"}, {"stack", "logs"}, {"stack", "restart"}, {"stack", "reseed", "x"}, {"stack", "logs", "web", "extra"}, {"stack", "logs", "web", "--tail"}, {"stack", "kill", "web"}} {
		if _, _, err := callStack(t, socket, args...); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("%q: err = %v, want usage", args, err)
		}
	}
}
