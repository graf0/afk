package tui

import (
	"cmp"
	"context"
	"fmt"
	"os"
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
	act     actions
	cwd     string // directory of the pane: the Project of M-n without a selection
	own     string // the entry this window shows, empty in the home window
	entries []Entry
	cursor  int // index in entries; -1 when nothing is selected
	status  string
	width   int

	mode   mode
	target Entry  // the entry a prompt (not modeName) was opened for
	input  string // the name typed in modeName
	dir    string // the Project directory of the Chat being named
	notice string // result of the last action; stays until the next key
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
func newSidebar(list func() ([]Entry, error), act actions, cwd, own string) (s *sidebar) {
	s = &sidebar{list: list, act: act, cwd: cwd, own: own}
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
		if s.mode != modeNone {
			cmd = s.modeKey(msg)
			break
		}
		s.notice = ""
		switch key := msg.String(); key {
		case "up", "alt+up":
			cmd = s.move(-1)
		case "down", "alt+down":
			cmd = s.move(1)
		case "alt+n", "alt+e", "alt+x", "alt+s", keyNewFactory:
			cmd = s.keyAction(key)
		}
	case resultMsg:
		s.notice = msg.notice
		if msg.askForce {
			cmd = s.enter(modeConfirmForce, s.target)
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
		}
		if msg.err == nil || msg.entries != nil { // a partial list is still the list
			s.entries = msg.entries
			s.cursor = indexOf(s.entries, cmp.Or(s.selected(), s.own)) // own may have appeared only now
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

// selectedEntry is the entry under the cursor.
func (s *sidebar) selectedEntry() (e Entry, ok bool) {
	if ok = s.cursor >= 0 && s.cursor < len(s.entries); ok {
		e = s.entries[s.cursor]
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
	cmd = func() tea.Msg { return openedMsg{s.act.Open(entry)} }
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
		b.WriteString("no Chats, Factories or abandoned Runs\n")
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
		fmt.Fprintf(&b, "%s\n", fit(clean(fmt.Sprintf("%s %s %s", mark, state, entryLabel(e))), s.width))
	}
	for _, text := range []string{s.status, s.notice, s.promptText()} {
		if text != "" {
			fmt.Fprintf(&b, "\n%s\n", wrap(clean(text), s.width))
		}
	}
	out = b.String()
	return
}

// entryLabel is the text of an entry after its marks: kind and id; a Factory
// shows its header, once ended with the exit code.
func entryLabel(e Entry) (label string) {
	label = fmt.Sprintf("%s %s", e.Kind, e.ID)
	if e.Kind == KindFactory {
		label = "factory " + cmp.Or(e.Title, e.ID)
		if !e.Running {
			label = fmt.Sprintf("factory [%s] %s", e.Exit, cmp.Or(e.Title, e.ID))
		}
	}
	return
}

// RunSidebar runs the bubbletea program of the sidebar until it is quit.
func RunSidebar(ctx context.Context, exe, own string) (err error) {
	cwd, _ := os.Getwd()
	model := newSidebar(func() ([]Entry, error) { return listEntries(ctx) }, realActions(ctx, exe), cwd, own)
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

// wrap breaks text into lines of width runes (no break when width is unknown).
func wrap(text string, width int) (wrapped string) {
	if width <= 0 {
		return text
	}
	var lines []string
	for line := range strings.SplitSeq(text, "\n") {
		r := []rune(line)
		for len(r) > width {
			lines = append(lines, string(r[:width]))
			r = r[width:]
		}
		lines = append(lines, string(r))
	}
	wrapped = strings.Join(lines, "\n")
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
