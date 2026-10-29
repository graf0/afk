package tui

import (
	"cmp"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/graf0/afk/chat"
	"github.com/graf0/afk/machine"
)

// mode is what the sidebar waits for besides navigation.
const (
	modeNone         mode = iota
	modeName              // M-n: the name of the new Chat
	modeChatMenu          // M-x on a Chat: export / remove / cancel
	modeConfirmForce      // rm refused over unexported work: --force?
	modeConfirmKill       // M-x on an abandoned Run: destroy the VM?
)

// op is what run does with an entry.
const (
	opExport op = iota
	opRemove
	opRemoveForce
	opKill
)

var errNoSelection = errors.New("no entry selected")

type (
	mode int
	op   int
)

// resultMsg is the end of an action run in the background.
type resultMsg struct {
	notice   string
	askForce bool // the target may be removed with --force
}

// keyAction handles a key in the normal mode: the M-… shortcuts tmux forwards.
func (s *sidebar) keyAction(key string) (cmd tea.Cmd) {
	e, selected := s.selectedEntry()
	if key != "alt+n" && !selected {
		s.notice = errNoSelection.Error()
		return
	}
	switch key {
	case "alt+n":
		var target *Entry
		if selected {
			target = &e
		}
		project, err := projectFor(target, s.cwd)
		if err != nil {
			s.notice = err.Error()
			return
		}
		s.dir, s.input = project, ""
		cmd = s.enter(modeName, e)
	case "alt+e":
		cmd = s.run(e, opExport)
	case "alt+x":
		if e.Kind == machine.KindChat {
			cmd = s.enter(modeChatMenu, e)
		} else {
			cmd = s.enter(modeConfirmKill, e)
		}
	case "alt+s":
		cmd = func() tea.Msg { return openedMsg{s.act.Shell(e)} }
	}
	return
}

// modeKey handles a key while a prompt is open; s.target is the entry it was
// opened for.
func (s *sidebar) modeKey(msg tea.KeyPressMsg) (cmd tea.Cmd) {
	key, e := msg.String(), s.target
	switch s.mode {
	case modeName:
		switch key {
		case "esc":
			cmd = s.leave()
		case "enter":
			name, dir := s.input, s.dir
			if err := chat.ValidateName(name); err != nil {
				s.notice = err.Error()
				return
			}
			cmd = tea.Sequence(s.leave(), func() tea.Msg { return openedMsg{s.act.NewChat(name, dir)} })
		case "backspace":
			if r := []rune(s.input); len(r) > 0 {
				s.input = string(r[:len(r)-1])
			}
		default:
			s.input += msg.Text
		}
	case modeChatMenu:
		switch key {
		case "e":
			cmd = tea.Sequence(s.leave(), s.run(e, opExport))
		case "u":
			cmd = tea.Sequence(s.leave(), s.run(e, opRemove))
		default:
			cmd = s.leave()
		}
	case modeConfirmForce:
		if key == "t" {
			cmd = tea.Sequence(s.leave(), s.run(e, opRemoveForce))
		} else { // the session is gone already: the window goes, the next visit recreates it
			cmd = tea.Sequence(s.leave(), s.closeWindows(e, "closed"))
		}
	case modeConfirmKill:
		cmd = s.leave()
		if key == "t" {
			cmd = tea.Sequence(cmd, s.run(e, opKill))
		}
	}
	return
}

// enter opens a prompt for e and takes the keyboard to the sidebar pane.
func (s *sidebar) enter(m mode, e Entry) (cmd tea.Cmd) {
	s.mode, s.target, s.notice = m, e, ""
	cmd = func() tea.Msg { s.act.Focus(true); return nil }
	return
}

// leave closes the prompt and gives the keyboard back to the Agent.
func (s *sidebar) leave() (cmd tea.Cmd) {
	s.mode = modeNone
	cmd = func() tea.Msg { s.act.Focus(false); return nil }
	return
}

// run does what to e in the background with the `afk` subcommand and shows
// the result as a notice. Removing first ends the session in the Chat's
// window, which holds the Chat's lock; then the window (and the shell's) goes,
// unless rm asks about --force, which keeps the window to the answer.
func (s *sidebar) run(e Entry, what op) (cmd tea.Cmd) {
	var args []string
	var err error
	dir, ok := findProject(e.Repo)
	switch what {
	case opExport:
		args, err = exportArgs(e)
	case opRemove, opRemoveForce:
		args, err = rmArgs(e, what == opRemoveForce)
	case opKill:
		args, err = killArgs(e)
		dir, ok = "", true // kill needs no Project
	}
	if err == nil && !ok {
		err = errors.New("no Project path in the VM metadata")
	}
	if err != nil {
		s.notice = err.Error()
		return
	}
	s.notice = "working on " + e.Name + "…"
	removing := what == opRemove || what == opRemoveForce
	cmd = func() tea.Msg {
		if removing {
			if err := s.act.Detach(e.Name); err != nil {
				return resultMsg{notice: err.Error()}
			}
		}
		out, code, err := s.act.Run(dir, args...)
		if removing && what == opRemove && code == chat.ExitNeedsForce {
			return resultMsg{notice: out, askForce: true}
		}
		if err != nil {
			return s.closeAfter(removing, e, cmp.Or(out, err.Error()))
		}
		return s.closeAfter(what != opExport, e, cmp.Or(out, "done"))
	}
	return
}

// closeAfter closes the windows of e when closing is set; a failure to do so
// joins the notice.
func (s *sidebar) closeAfter(closing bool, e Entry, notice string) (msg tea.Msg) {
	if closing {
		if err := s.closeNow(e); err != nil {
			notice += "\n" + err.Error()
		}
	}
	return resultMsg{notice: notice}
}

// closeWindows is a command closing the windows of e, with notice on success.
func (s *sidebar) closeWindows(e Entry, notice string) (cmd tea.Cmd) {
	cmd = func() tea.Msg {
		if err := s.closeNow(e); err != nil {
			notice = err.Error()
		}
		return resultMsg{notice: notice}
	}
	return
}

// closeNow closes the window of e and its shell window; the latter may not
// exist, so only the first failure counts.
func (s *sidebar) closeNow(e Entry) (err error) {
	_ = s.act.Close(shellWindow(e))
	err = s.act.Close(e.Name)
	return
}

// promptText is the line under the entries for the current mode.
func (s *sidebar) promptText() (text string) {
	e := s.target
	switch s.mode {
	case modeName:
		text = "Nazwa Chatu: " + s.input + "▏ (Enter, Esc)"
	case modeChatMenu:
		text = fmt.Sprintf("Chat %s: [e]ksportuj / [u]suń / [a]nuluj", e.ID)
	case modeConfirmForce:
		text = fmt.Sprintf("Usunąć Chat %s mimo to (--force)? [t]ak / [n]ie", e.ID)
	case modeConfirmKill:
		text = fmt.Sprintf("Zniszczyć VM %s? [t]ak / [n]ie", e.Name)
	}
	return
}
