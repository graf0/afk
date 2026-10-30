package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
// CreateIssue, Comment, EditIssue, AddLabel and RemoveLabel; the writes go to
// the pointed lists.
type fakeForge struct {
	forge.Forge
	tickets  []forge.Ticket
	closed   int // the number of the one closed ticket
	created  *[]forge.Ticket
	comments *[]string // "#n: body"
	pull     int       // the number of the one pull request
	edits    *[]string // "#n title=… body=…", "#n +label", "#n -label"
	remote   forge.Remote
}

func (f fakeForge) Remote() (remote forge.Remote) { return f.remote }

func (f fakeForge) EditIssue(_ context.Context, n int, title, body *string) (err error) {
	edit := fmt.Sprintf("#%d", n)
	if title != nil {
		edit += " title=" + *title
	}
	if body != nil {
		edit += " body=" + *body
	}
	*f.edits = append(*f.edits, edit)
	return
}

func (f fakeForge) AddLabel(_ context.Context, n int, label string) (err error) {
	*f.edits = append(*f.edits, fmt.Sprintf("#%d +%s", n, label))
	return
}

func (f fakeForge) RemoveLabel(_ context.Context, n int, label string) (err error) {
	*f.edits = append(*f.edits, fmt.Sprintf("#%d -%s", n, label))
	return
}

func (f fakeForge) Labels(context.Context) (names []string, err error) {
	names = []string{"bug", "needs-triage", "ready-for-agent"}
	return
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
			ticket = t
			return
		}
	}
	err = errors.New("no such ticket")
	return
}

func (f fakeForge) Peek(_ context.Context, n int) (ticket forge.Ticket, open bool, err error) {
	if n == f.pull {
		err = fmt.Errorf("#%d is a pull request, not a ticket", n)
		return
	}
	for _, t := range f.tickets {
		if t.Number == n {
			t.Comments = nil
			ticket, open = t, n != f.closed
			return
		}
	}
	err = errors.New("no such ticket")
	return
}

func (f fakeForge) List(_ context.Context, label string, state forge.State) (tickets []forge.Ticket, err error) {
	for _, t := range f.tickets {
		if label == "" || forge.ContainsLabel(t.Labels, label) {
			tickets = append(tickets, t)
		}
	}
	if state == forge.StateClosed {
		tickets = nil
	}
	return
}

// serveFake serves the ticket routes of Chat "chat-1" on a unix socket and
// returns its path, the log and the file of the Chat's issues.
func serveFake(t *testing.T, f forge.Forge) (socket, logPath, issuesPath string) {
	t.Helper()
	dir := t.TempDir()
	socket, logPath, issuesPath = filepath.Join(dir, "afk.sock"), filepath.Join(dir, "forge.txt"), filepath.Join(dir, issuesName)
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := serveChannel(t.Context(), socket, logFile, forgeRoutes(forgeChannel{
		Forge: f, ProjectLabels: config.Labels{NeedsTriage: "needs-triage", ReadyForAgent: "ready-for-agent"}, Chat: "chat-1", IssuesPath: issuesPath,
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); _ = logFile.Close() })
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
	if err == nil || !strings.Contains(err.Error(), "no channel to afk") {
		t.Errorf("err = %v", err)
	}
}

func TestRunForgeWrite(t *testing.T) {
	var created []forge.Ticket
	var comments []string
	socket, logPath, issuesPath := serveFake(t, fakeForge{created: &created, comments: &comments, pull: 11, tickets: []forge.Ticket{{Number: 7}, {Number: 8}, {Number: 9}}})
	call := func(stdin string, args ...string) (out, warning string, err error) {
		var stdout, stderr bytes.Buffer
		err = RunForge(t.Context(), socket, args, strings.NewReader(stdin), &stdout, &stderr)
		return stdout.String(), stderr.String(), err
	}

	out, warning, err := call("", "issue", "create", "--title", "Idea @bob", "--body", "Fixes #3 <!-- x -->", "--label", "bug, Ready-For-Agent,", "--label", "needs-triage")
	if err != nil || out != "#100\n" || !strings.Contains(warning, "ready-for-agent replaced with needs-triage") {
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
		{"issue", "comment", "11", "--body", "on a pull request"},
	} {
		if _, _, err = call("", args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(created) != 2 || len(comments) != 2 {
		t.Errorf("refused calls wrote: %d issues, %d comments", len(created), len(comments))
	}

	log, _ := os.ReadFile(logPath)
	for _, want := range []string{`create #100 labels=["bug" "needs-triage"] (ready-for-agent replaced): ok`, "create #101 labels=[]: ok", "comment #7: ok", "comment #8: ok", "comment #11: error: #11 is a pull request", `create labels=["nope"]: error: label "nope" does not exist`} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestRunForgeEdit(t *testing.T) {
	var edits []string
	socket, logPath, issuesPath := serveFake(t, fakeForge{edits: &edits, pull: 9, closed: 3, tickets: []forge.Ticket{{Number: 3}, {Number: 4}, {Number: 6, Labels: []string{"Ready-For-Agent"}}}})
	if err := os.WriteFile(issuesPath, []byte("4\n6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(stdin string, args ...string) (warning string, err error) {
		var stderr bytes.Buffer
		err = RunForge(t.Context(), socket, append([]string{"issue", "edit"}, args...), strings.NewReader(stdin), io.Discard, &stderr)
		return stderr.String(), err
	}

	// Labels of a (closed) issue of someone else: ready-for-agent is replaced when added, removing it is fine.
	warning, err := call("", "3", "--add-label", "bug, Ready-For-Agent,", "--remove-label", "ready-for-agent")
	if err != nil || !strings.Contains(warning, "ready-for-agent replaced with needs-triage") {
		t.Errorf("labels = %q, %v", warning, err)
	}
	if want := []string{"#3 +bug", "#3 +needs-triage", "#3 -ready-for-agent"}; !slices.Equal(edits, want) {
		t.Errorf("edits = %q, want %q", edits, want)
	}

	// Title and body of an own issue; a footer already there is not doubled.
	edits = nil
	if _, err = call("New\n\n— z Chat chat-1\n", "4", "--title", "Two\nlines @bob", "--body-file", "-", "--add-label", "bug"); err != nil {
		t.Error(err)
	}
	if _, err = call("", "4", "--body", "Plain"); err != nil {
		t.Error(err)
	}
	want := []string{"#4 title=Two lines @\u200bbob body=New\n\n— z Chat chat-1", "#4 +bug", "#4 body=Plain\n\n— z Chat chat-1"}
	if !slices.Equal(edits, want) {
		t.Errorf("edits = %q, want %q", edits, want)
	}

	// Refused before anything is written, labels included.
	edits = nil
	for _, args := range [][]string{
		{"3", "--body", "x", "--add-label", "bug"},
		{"3", "--title", "x", "--remove-label", "bug"},
		{"9", "--add-label", "bug"},
		{"4", "--add-label", "nope"},
		{"6", "--body", "x"},
		{"6", "--title", "x", "--add-label", "bug"},
		{"6", "--remove-label", "ready-for-agent", "--body", "x"},
		{"4", "--title", " "},
		{"4", "--add-label", "bug", "--remove-label", "bug"},
		{"3"},
		{"x", "--add-label", "bug"},
		{"4", "--body", "a", "--body-file", "-"},
	} {
		if _, err = call("", args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(edits) != 0 {
		t.Errorf("refused calls wrote %q", edits)
	}

	// The labels of an own ready-for-agent issue stay editable, ready-for-agent included.
	if _, err = call("", "6", "--remove-label", "ready-for-agent", "--add-label", "bug"); err != nil {
		t.Error(err)
	}
	if want := []string{"#6 +bug", "#6 -ready-for-agent"}; !slices.Equal(edits, want) {
		t.Errorf("edits = %q, want %q", edits, want)
	}

	log, _ := os.ReadFile(logPath)
	for _, want := range []string{"edit #6 add=[] remove=[] body: error: title and body: #6 is ready-for-agent", `edit #3 add=["bug" "needs-triage"] remove=["ready-for-agent"] (ready-for-agent replaced): ok`, `edit #4 add=["bug"] remove=[] title body: ok`, `edit #3 add=["bug"] remove=[] body: error: title and body: #3 was not created by this Chat`, "edit #9 add=[\"bug\"] remove=[]: error: #9 is a pull request"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}
