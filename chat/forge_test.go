package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/forge"
)

// fakeForge is a Forge of which the channel uses Peek, Ticket, List, Labels,
// CreateIssue and Comment; the last two write to the pointed lists.
type fakeForge struct {
	forge.Forge
	tickets  []forge.Ticket
	closed   int // the number of the one closed ticket
	created  *[]forge.Ticket
	comments *[]string // "#n: body"
}

func (f fakeForge) Labels(context.Context) (names []string, err error) {
	return []string{"bug", "needs-triage", "ready-for-agent"}, nil
}

func (f fakeForge) CreateIssue(_ context.Context, title, body string, labels []string) (n int, err error) {
	n = 100 + len(*f.created)
	*f.created = append(*f.created, forge.Ticket{Number: n, Title: title, Body: body, Labels: labels})
	return
}

func (f fakeForge) Comment(_ context.Context, n int, body string) (err error) {
	*f.comments = append(*f.comments, fmt.Sprintf("#%d: %s", n, body))
	return
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

// serveFake serves the channel of Chat "chat-1" on a unix socket and returns
// its path, the log and the file of the Chat's issues.
func serveFake(t *testing.T, f forge.Forge) (socket, logPath, issuesPath string) {
	t.Helper()
	dir := t.TempDir()
	socket, logPath, issuesPath = filepath.Join(dir, "afk.sock"), filepath.Join(dir, "forge.txt"), filepath.Join(dir, issuesName)
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: newForgeServer(forgeChannel{
		Forge: f, Labels: config.Labels{NeedsTriage: "needs-triage", ReadyForAgent: "ready-for-agent"}, Chat: "chat-1", IssuesPath: issuesPath,
	}, logFile).handler()}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close(); _ = logFile.Close() })
	return
}

func TestRunForge(t *testing.T) {
	socket, logPath, _ := serveFake(t, fakeForge{tickets: []forge.Ticket{
		{Number: 3, Title: "First\x1b[31m", Body: "B", Comments: []string{"c1"}, Labels: []string{"bug", "needs-triage"}},
		{Number: 5, Title: "Second"},
	}, closed: 5})
	call := func(args ...string) (out string, err error) {
		var stdout bytes.Buffer
		err = RunForge(t.Context(), socket, args, nil, &stdout, io.Discard)
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
	for _, args := range [][]string{{}, {"issue"}, {"issue", "view", "x"}, {"issue", "list", "--state", "bad"}, {"issue", "list", "extra"}, {"issue", "create"}, {"issue", "comment", "3"}} {
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
	err := RunForge(t.Context(), filepath.Join(t.TempDir(), "none.sock"), []string{"issue", "list"}, nil, &bytes.Buffer{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "kanał do afk niedostępny") {
		t.Errorf("err = %v", err)
	}
}

func TestRunForgeWrite(t *testing.T) {
	var created []forge.Ticket
	var comments []string
	socket, logPath, issuesPath := serveFake(t, fakeForge{created: &created, comments: &comments})
	call := func(stdin string, args ...string) (out, warning string, err error) {
		var stdout, stderr bytes.Buffer
		err = RunForge(t.Context(), socket, args, strings.NewReader(stdin), &stdout, &stderr)
		return stdout.String(), stderr.String(), err
	}

	out, warning, err := call("", "issue", "create", "--title", "Idea @bob", "--body", "Fixes #3 <!-- x -->", "--label", "bug, Ready-For-Agent,", "--label", "needs-triage")
	if err != nil || out != "#100\n" || !strings.Contains(warning, "ready-for-agent zamieniony na needs-triage") {
		t.Errorf("create = %q, %q, %v", out, warning, err)
	}
	if _, warning, err = call("From stdin", "issue", "create", "--title", "Second", "--body-file", "-"); err != nil || warning != "" {
		t.Errorf("create from stdin: %q, %v", warning, err)
	}
	if _, _, err = call("Text\x1b[31m", "issue", "comment", "7", "--body-file", "-"); err != nil {
		t.Error(err)
	}
	if _, _, err = call("", "issue", "comment", "8", "--body", "hi"); err != nil {
		t.Error(err)
	}
	if _, _, err = call(strings.Repeat("x", 70000), "issue", "comment", "9", "--body-file", "-"); err != nil || !strings.HasSuffix(comments[len(comments)-1], "\n\n— z Chat chat-1") {
		t.Errorf("long comment: the footer must survive truncation, err = %v", err)
	}
	comments = comments[:len(comments)-1]

	wantCreated := []forge.Ticket{
		{Number: 100, Title: "Idea @\u200bbob", Body: "F\u200bixes #3 &lt;!-- x -->\n\n— z Chat chat-1", Labels: []string{"bug", "needs-triage"}},
		{Number: 101, Title: "Second", Body: "From stdin\n\n— z Chat chat-1"},
	}
	if !reflect.DeepEqual(created, wantCreated) {
		t.Errorf("created = %+v, want %+v", created, wantCreated)
	}
	if want := []string{"#7: Text[31m\n\n— z Chat chat-1", "#8: hi\n\n— z Chat chat-1"}; !slices.Equal(comments, want) {
		t.Errorf("comments = %q, want %q", comments, want)
	}
	if issues, _ := os.ReadFile(issuesPath); string(issues) != "100\n101\n" {
		t.Errorf("issues = %q: want the created ones only, not the comments", issues)
	}

	// Refused before anything is written.
	for _, args := range [][]string{
		{"issue", "create", "--title", "t", "--label", "nope"},
		{"issue", "create", "--title", "t", "--body", "a", "--body-file", "-"},
		{"issue", "create", "--body", "no title"},
		{"issue", "comment", "x", "--body", "b"},
		{"issue", "comment", "7", "--body", " "},
	} {
		if _, _, err = call("", args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(created) != 2 || len(comments) != 2 {
		t.Errorf("refused calls wrote: %d issues, %d comments", len(created), len(comments))
	}

	log, _ := os.ReadFile(logPath)
	for _, want := range []string{`create #100 labels=["bug" "needs-triage"] (ready-for-agent replaced): ok`, "create #101 labels=[]: ok", "comment #7: ok", "comment #8: ok", `create labels=["nope"]: error: label "nope" does not exist`} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}
