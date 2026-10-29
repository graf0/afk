package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/graf0/afk/chat"
	"github.com/graf0/afk/machine"
)

// calls records what the sidebar did, in order, in events.
type calls struct {
	events  []string
	runs    []string
	closed  []string
	chats   []string
	shells  []string
	focus   []bool
	runOut  string
	refused bool // a rm without --force is refused with exit code 3
}

func (c *calls) actions() actions {
	return actions{
		NewChat: func(name, repo string) error { c.chats = append(c.chats, name+"@"+repo); return nil },
		Shell:   func(e Entry) error { c.shells = append(c.shells, e.Name); return nil },
		Run: func(dir string, args ...string) (string, int, error) {
			c.runs = append(c.runs, dir+" "+strings.Join(args, " "))
			c.events = append(c.events, "run "+strings.Join(args, " "))
			if c.refused && !slices.Contains(args, "--force") {
				return "chat x would lose 1 path", chat.ExitNeedsForce, errors.New("exit status 3")
			}
			return c.runOut, 0, nil
		},
		Detach: func(w string) error { c.events = append(c.events, "detach "+w); return nil },
		Close: func(w string) error {
			c.events = append(c.events, "close "+w)
			c.closed = append(c.closed, w)
			return nil
		},
		Focus: func(on bool) { c.focus = append(c.focus, on) },
	}
}

// drive feeds keys to s and runs the commands they return.
func drive(s *sidebar, keys ...tea.Key) {
	for _, k := range keys {
		_, cmd := s.Update(tea.KeyPressMsg(k))
		runCmd(s, cmd)
	}
}

// runCmd runs cmd; the messages it returns go back to s, and batches and
// sequences (slices of commands) are run in order.
func runCmd(s *sidebar, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice {
		for i := range v.Len() {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				runCmd(s, c)
			}
		}
		return
	}
	if msg != nil {
		_, next := s.Update(msg)
		runCmd(s, next)
	}
}

func alt(r rune) tea.Key  { return tea.Key{Code: r, Mod: tea.ModAlt} }
func char(r rune) tea.Key { return tea.Key{Code: r, Text: string(r)} }

func promptSidebar(t *testing.T, c *calls, repo string, own string) *sidebar {
	t.Helper()
	entries := []Entry{
		{Name: "afk-chat-x", Project: "p", Kind: machine.KindChat, ID: "x", Repo: repo},
		{Name: "afk-run-1-ab", Project: "p", Kind: machine.KindRun, ID: "1-ab", Repo: repo},
	}
	return newSidebar(func() ([]Entry, error) { return entries, nil }, c.actions(), t.TempDir(), own)
}

func TestNewChat(t *testing.T) {
	c := &calls{}
	s := promptSidebar(t, c, "/nonexistent", "")
	drive(s, alt('n'))
	if s.mode != modeNone || !strings.Contains(s.notice, "no Project") || len(c.focus) != 0 {
		t.Errorf("no Project: mode %d, notice %q", s.mode, s.notice)
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".afk.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s = promptSidebar(t, c, repo, "afk-chat-x")
	drive(s, alt('n'), char('a'), char('B'), char('b'), tea.Key{Code: tea.KeyBackspace}, tea.Key{Code: tea.KeyEnter})
	if s.mode != modeName || !strings.Contains(s.notice, "invalid Chat name") {
		t.Errorf("invalid name: mode %d, notice %q", s.mode, s.notice)
	}
	drive(s, tea.Key{Code: tea.KeyBackspace}, char('c'), tea.Key{Code: tea.KeyEnter})
	if !slices.Equal(c.chats, []string{"ac@" + repo}) || s.mode != modeNone || !slices.Equal(c.focus, []bool{true, false}) {
		t.Errorf("chats %v, mode %d, focus %v", c.chats, s.mode, c.focus)
	}
}

func TestExport(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".afk.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := &calls{runOut: "pushed chat-x"}
	s := promptSidebar(t, c, repo, "afk-chat-x")
	drive(s, alt('e'))
	if !slices.Equal(c.runs, []string{repo + " chat export x"}) || s.notice != "pushed chat-x" {
		t.Errorf("runs %v, notice %q", c.runs, s.notice)
	}
}

func TestRemovePaths(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".afk.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// export
	c := &calls{}
	s := promptSidebar(t, c, repo, "afk-chat-x")
	drive(s, alt('x'), char('e'))
	if !slices.Equal(c.runs, []string{repo + " chat export x"}) || s.mode != modeNone {
		t.Errorf("export path: runs %v mode %d", c.runs, s.mode)
	}
	// cancel
	c = &calls{}
	s = promptSidebar(t, c, repo, "afk-chat-x")
	drive(s, alt('x'), char('a'))
	if len(c.runs) != 0 || s.mode != modeNone {
		t.Errorf("cancel path: runs %v mode %d", c.runs, s.mode)
	}
	// remove, refused, forced
	c = &calls{refused: true}
	s = promptSidebar(t, c, repo, "afk-chat-x")
	drive(s, alt('x'), char('u'))
	if s.mode != modeConfirmForce || len(c.closed) != 0 {
		t.Fatalf("refusal: mode %d closed %v", s.mode, c.closed)
	}
	drive(s, char('n')) // declined: the window, detached already, goes too
	if s.mode != modeNone || len(c.runs) != 1 || !slices.Equal(c.closed, []string{"shell/afk-chat-x", "afk-chat-x"}) {
		t.Errorf("declined: mode %d runs %v closed %v", s.mode, c.runs, c.closed)
	}
	c.events = nil
	drive(s, alt('x'), char('u'), char('t'))
	want := []string{"detach afk-chat-x", "run chat rm x", "detach afk-chat-x", "run chat rm --force x", "close shell/afk-chat-x", "close afk-chat-x"}
	if !slices.Equal(c.events, want) {
		t.Errorf("forced: events %v, want %v", c.events, want)
	}
	// plain remove succeeds
	c = &calls{}
	s = promptSidebar(t, c, repo, "afk-chat-x")
	drive(s, alt('x'), char('u'))
	if want := []string{"detach afk-chat-x", "run chat rm x", "close shell/afk-chat-x", "close afk-chat-x"}; !slices.Equal(c.events, want) {
		t.Errorf("rm: events %v, want %v", c.events, want)
	}
}

func TestAbandonedRun(t *testing.T) {
	c := &calls{}
	s := promptSidebar(t, c, "", "afk-run-1-ab")
	drive(s, alt('x'), char('n'))
	if len(c.runs) != 0 {
		t.Errorf("kill without confirmation: %v", c.runs)
	}
	drive(s, alt('x'), char('t'))
	if !slices.Equal(c.runs, []string{" vm kill afk-run-1-ab"}) || !slices.Equal(c.closed, []string{"shell/afk-run-1-ab", "afk-run-1-ab"}) {
		t.Errorf("runs %v closed %v", c.runs, c.closed)
	}
	drive(s, alt('s'))
	if !slices.Equal(c.shells, []string{"afk-run-1-ab"}) {
		t.Errorf("shells %v", c.shells)
	}
	drive(s, alt('e'))
	if !strings.Contains(s.notice, "only a Chat") || len(c.runs) != 1 {
		t.Errorf("export of a Run: notice %q runs %v", s.notice, c.runs)
	}
}

func TestNoSelection(t *testing.T) {
	c := &calls{}
	s := promptSidebar(t, c, "", "")
	for _, k := range []rune{'e', 'x', 's'} {
		drive(s, alt(k))
		if s.notice != errNoSelection.Error() || s.mode != modeNone {
			t.Errorf("M-%c: notice %q mode %d", k, s.notice, s.mode)
		}
	}
}

func TestPromptRender(t *testing.T) {
	c := &calls{}
	s := promptSidebar(t, c, "", "afk-chat-x")
	s.width = 20
	drive(s, alt('x'))
	if got := s.render(); !strings.Contains(got, "[e]ksportuj") || !strings.Contains(got, "\n") {
		t.Errorf("render: %q", got)
	}
	for _, line := range strings.Split(s.render(), "\n") {
		if len([]rune(line)) > 20 {
			t.Errorf("line %q wider than the pane", line)
		}
	}
}
