package chat

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graf0/afk/forge"
)

// fakeForge is a Forge of which the channel uses Peek, Ticket and List.
type fakeForge struct {
	forge.Forge
	tickets []forge.Ticket
	closed  int // the number of the one closed ticket
}

func (f fakeForge) Ticket(_ context.Context, n int) (ticket forge.Ticket, err error) {
	if n == f.closed {
		err = errors.New("closed")
		return
	}
	for _, t := range f.tickets {
		if t.Number == n {
			return t, nil
		}
	}
	err = errors.New("no such ticket")
	return
}

func (f fakeForge) Peek(_ context.Context, n int) (ticket forge.Ticket, open bool, err error) {
	for _, t := range f.tickets {
		if t.Number == n {
			t.Comments = nil
			return t, n != f.closed, nil
		}
	}
	err = errors.New("no such ticket")
	return
}

func (f fakeForge) List(_ context.Context, label, state string) (tickets []forge.Ticket, err error) {
	for _, t := range f.tickets {
		if label == "" || forge.ContainsLabel(t.Labels, label) {
			tickets = append(tickets, t)
		}
	}
	if state == "closed" {
		tickets = nil
	}
	return
}

// serveFake serves the channel on a unix socket and returns its path and the log.
func serveFake(t *testing.T, f forge.Forge) (socket string, logPath string) {
	t.Helper()
	dir := t.TempDir()
	socket, logPath = filepath.Join(dir, "afk.sock"), filepath.Join(dir, "forge.txt")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: newForgeServer(f, logFile).handler()}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close(); _ = logFile.Close() })
	return
}

func TestRunForge(t *testing.T) {
	socket, logPath := serveFake(t, fakeForge{tickets: []forge.Ticket{
		{Number: 3, Title: "First\x1b[31m", Body: "B", Comments: []string{"c1"}, Labels: []string{"bug", "needs-triage"}},
		{Number: 5, Title: "Second"},
	}, closed: 5})
	call := func(args ...string) (out string, err error) {
		var stdout bytes.Buffer
		err = RunForge(t.Context(), socket, args, &stdout)
		return stdout.String(), err
	}

	if out, err := call("issue", "view", "3"); err != nil || out != "# #3 First[31m\n\nB\n\n---\nKomentarz:\nc1\n" {
		t.Errorf("view = %q, %v", out, err)
	}
	if out, err := call("issue", "view", "5"); err != nil || out != "# #5 Second\n\n\n" {
		t.Errorf("view of closed = %q, %v", out, err)
	}
	if _, err := call("issue", "view", "9"); err == nil || !strings.Contains(err.Error(), "no such ticket") {
		t.Errorf("view of none: err = %v", err)
	}
	if out, err := call("issue", "list"); err != nil || out != "#3  [bug, needs-triage]  First[31m\n#5  []  Second\n" {
		t.Errorf("list = %q, %v", out, err)
	}
	if out, err := call("issue", "list", "--label", "bug", "--state", "all"); err != nil || out != "#3  [bug, needs-triage]  First[31m\n" {
		t.Errorf("list --label bug = %q, %v", out, err)
	}
	for _, args := range [][]string{{}, {"issue"}, {"issue", "view", "x"}, {"issue", "list", "--state", "bad"}, {"issue", "list", "extra"}} {
		if _, err := call(args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}

	log, _ := os.ReadFile(logPath)
	for _, want := range []string{"view #3: ok", "view #9: error: no such ticket", `list label="bug" state=all: ok`} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestRunForgeNoChannel(t *testing.T) {
	err := RunForge(t.Context(), filepath.Join(t.TempDir(), "none.sock"), []string{"issue", "list"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "kanał do afk niedostępny") {
		t.Errorf("err = %v", err)
	}
}
