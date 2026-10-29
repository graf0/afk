package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

const refreshEvery = 3 * time.Second

// sidebar is the bubbletea model of the left pane. Every tmux window has its
// own: Up/Down (sent by tmux for M-↑/↓) select the neighbour of the entry the
// window shows and open that entry's window, whose own sidebar starts on it.
type sidebar struct {
	list    func() ([]Entry, error)
	open    func(Entry) error
	own     string // the entry this window shows, empty in the home window
	entries []Entry
	cursor  int // index in entries; -1 when nothing is selected
	status  string
	width   int
}

type (
	entriesMsg struct {
		entries []Entry
		err     error
	}
	tickMsg   time.Time
	openedMsg struct{ err error }
)

// newSidebar makes a sidebar of list showing the entry own ("" for none).
func newSidebar(list func() ([]Entry, error), open func(Entry) error, own string) (s *sidebar) {
	s = &sidebar{list: list, open: open, own: own}
	var err error
	if s.entries, err = list(); err != nil {
		s.status = err.Error()
	}
	s.cursor = indexOf(s.entries, own)
	return
}

func (s *sidebar) Init() (cmd tea.Cmd) {
	cmd = s.tick()
	return
}

func (s *sidebar) tick() (cmd tea.Cmd) {
	cmd = tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
	return
}

func (s *sidebar) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	model = s
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = msg.Width
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "alt+up":
			cmd = s.move(-1)
		case "down", "alt+down":
			cmd = s.move(1)
		}
	case tickMsg:
		cmd = func() tea.Msg {
			entries, err := s.list()
			return entriesMsg{entries, err}
		}
	case entriesMsg:
		s.status = ""
		if msg.err != nil {
			s.status = msg.err.Error()
		} else {
			s.entries = msg.entries
			s.cursor = indexOf(s.entries, s.selected())
		}
		cmd = s.tick() // after the read, so slow libvirt never overlaps it
	case openedMsg:
		s.cursor = indexOf(s.entries, s.own) // this window still shows its own entry
		if msg.err != nil {
			s.status = msg.err.Error()
		}
	}
	return
}

// selected is the name of the entry under the cursor, empty for none.
func (s *sidebar) selected() (name string) {
	if s.cursor >= 0 && s.cursor < len(s.entries) {
		name = s.entries[s.cursor].Name
	}
	return
}

// move opens the neighbour entry (none shown: the first for Down, the last
// for Up); at the ends nothing happens.
func (s *sidebar) move(delta int) (cmd tea.Cmd) {
	if len(s.entries) == 0 {
		return
	}
	next := s.cursor + delta
	if s.cursor < 0 && delta < 0 {
		next = len(s.entries) - 1
	}
	if next < 0 || next >= len(s.entries) || next == s.cursor {
		return
	}
	entry := s.entries[next]
	cmd = func() tea.Msg { return openedMsg{s.open(entry)} }
	return
}

func (s *sidebar) View() (view tea.View) {
	view = tea.NewView(s.render())
	return
}

// render lists the entries under a header per Project; the entry of the
// window is marked with ">", a running VM with "●", a shut off one with "○".
// Names come from libvirt metadata: control characters are dropped.
func (s *sidebar) render() (out string) {
	var b strings.Builder
	if len(s.entries) == 0 {
		b.WriteString("no Chats or abandoned Runs\n")
	}
	var project string
	for i, e := range s.entries {
		if i == 0 || e.Project != project {
			project = e.Project
			fmt.Fprintf(&b, "%s\n", fit(clean(project), s.width))
		}
		mark, state := " ", "○"
		if i == s.cursor {
			mark = ">"
		}
		if e.Running {
			state = "●"
		}
		fmt.Fprintf(&b, "%s\n", fit(clean(fmt.Sprintf("%s %s %s %s", mark, state, e.Kind, e.ID)), s.width))
	}
	if s.status != "" {
		fmt.Fprintf(&b, "\n%s", fit(clean(s.status), s.width))
	}
	out = b.String()
	return
}

// RunSidebar runs the bubbletea program of the sidebar until it is quit.
func RunSidebar(ctx context.Context, exe, own string) (err error) {
	model := newSidebar(listEntries, func(e Entry) error { return openEntry(ctx, exe, e) }, own)
	_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
	return
}

func indexOf(entries []Entry, name string) (index int) {
	index = -1
	for i, e := range entries {
		if e.Name == name {
			index = i
		}
	}
	return
}

// fit cuts line to width runes (no cut when width is unknown).
func fit(line string, width int) (cut string) {
	cut = line
	if r := []rune(line); width > 0 && len(r) > width {
		cut = string(r[:width])
	}
	return
}

// clean drops control and format characters (escape sequences, bidi marks).
func clean(s string) (cleaned string) {
	cleaned = strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return -1
		}
		return r
	}, s)
	return
}
