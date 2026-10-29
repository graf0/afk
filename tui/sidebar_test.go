package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/graf0/afk/machine"
)

func testSidebar(current string, opened *[]string) *sidebar {
	entries := []Entry{
		{Name: "chat-a", Project: "p", Kind: machine.KindChat, ID: "a", Running: true},
		{Name: "chat-b", Project: "p", Kind: machine.KindChat, ID: "b"},
		{Name: "run-1-x", Project: "q", Kind: machine.KindRun, ID: "1-x"},
	}
	return newSidebar(
		func() ([]Entry, error) { return entries, nil },
		actions{Open: func(e Entry) error { *opened = append(*opened, e.Name); return nil }},
		"",
		current,
	)
}

func press(s *sidebar, key tea.Key) {
	_, cmd := s.Update(tea.KeyPressMsg(key))
	if cmd != nil {
		s.Update(cmd())
	}
}

func TestSidebarKeys(t *testing.T) {
	var opened []string
	s := testSidebar("chat-a", &opened)
	press(s, tea.Key{Code: tea.KeyDown})
	press(s, tea.Key{Code: tea.KeyDown})                // the cursor went back to the own entry: chat-b again
	press(s, tea.Key{Code: tea.KeyUp, Mod: tea.ModAlt}) // above the first: nothing
	press(s, tea.Key{Code: 'x', Text: "x"})             // other keys: nothing
	if got := strings.Join(opened, ","); got != "chat-b,chat-b" {
		t.Errorf("opened %s", got)
	}
}

func TestSidebarHomeStart(t *testing.T) {
	var opened []string
	s := testSidebar("", &opened)
	press(s, tea.Key{Code: tea.KeyUp})
	if len(opened) != 1 || opened[0] != "run-1-x" {
		t.Errorf("opened %v, want the last entry", opened)
	}
}

func TestSidebarRender(t *testing.T) {
	var opened []string
	got := testSidebar("chat-b", &opened).render()
	want := "p\n  ● chat a\n> ○ chat b\nq\n  ○ run 1-x\n"
	if got != want {
		t.Errorf("render:\n%s\nwant:\n%s", got, want)
	}
}

func TestSidebarRefreshKeepsSelection(t *testing.T) {
	var opened []string
	s := testSidebar("chat-b", &opened)
	s.Update(entriesMsg{entries: []Entry{{Name: "chat-0"}, {Name: "chat-b"}}})
	if s.cursor != 1 {
		t.Errorf("cursor = %d, want 1", s.cursor)
	}
}

func TestSidebarTickWaitsForRead(t *testing.T) {
	var opened []string
	s := testSidebar("chat-a", &opened)
	if _, cmd := s.Update(entriesMsg{entries: s.entries}); cmd == nil {
		t.Error("no next tick after the read")
	}
	if _, cmd := s.Update(entriesMsg{err: errors.New("down")}); cmd == nil || s.status != "down" {
		t.Errorf("status %q, cmd %v", s.status, cmd)
	}
}

func TestSidebarInitialErrorAndClean(t *testing.T) {
	s := newSidebar(func() ([]Entry, error) { return nil, errors.New("no libvirt") }, actions{}, "", "")
	if s.status != "no libvirt" {
		t.Errorf("status %q", s.status)
	}
	s.entries = []Entry{{Name: "c", Project: "p\x1b[31m", Kind: machine.KindChat, ID: "a\u202eb"}}
	if got := s.render(); strings.ContainsAny(got, "\x1b\u202e") {
		t.Errorf("render keeps control characters: %q", got)
	}
}
