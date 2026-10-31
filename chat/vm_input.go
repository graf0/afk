package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// maxKeyCombo is the most keys one combination holds.
	maxKeyCombo = 6
	// maxTypeChars is the most characters one `vm type` call types.
	maxTypeChars = 500
	// typeChunk is the characters sent to QEMU in one command.
	typeChunk = 32
	// absMax is the top of the absolute axes of QEMU's input devices (0..32767).
	absMax = 32767

	// shiftedChars and unshiftedKeys pair each shifted character with the key it shares.
	shiftedChars  = `~!@#$%^&*()_+{}|:"<>?`
	unshiftedKeys = "`1234567890-=[]\\;',./"
)

// keyAliases are the names an Agent may use besides the QEMU qcodes.
var keyAliases = map[string]string{
	"enter": "ret", "return": "ret", "space": "spc", "escape": "esc", "del": "delete", "win": "meta_l", "super": "meta_l",
	"windows": "meta_l", "control": "ctrl", "pageup": "pgup", "pagedown": "pgdn", "bksp": "backspace",
}

// qcodes are the keys the Agent may press: the names of QEMU's QKeyCode.
var qcodes = func() (set map[string]bool) {
	set = make(map[string]bool)
	names := "shift shift_r alt alt_r ctrl ctrl_r meta_l meta_r spc tab ret esc backspace delete insert home end pgup pgdn " +
		"up down left right minus equal bracket_left bracket_right backslash semicolon apostrophe grave_accent comma dot slash " +
		"caps_lock menu print"
	for name := range strings.FieldsSeq(names) {
		set[name] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		set[string(c)] = true
	}
	for c := '0'; c <= '9'; c++ {
		set[string(c)] = true
	}
	for i := 1; i <= 12; i++ {
		set[fmt.Sprintf("f%d", i)] = true
	}
	return
}()

// mouseButtons are the buttons of a click, as QEMU names them.
var mouseButtons = map[string]bool{"left": true, "right": true, "middle": true}

// punctKeys maps the characters typed without shift to their qcodes.
var punctKeys = map[byte]string{
	'`': "grave_accent", '-': "minus", '=': "equal", '[': "bracket_left", ']': "bracket_right", '\\': "backslash",
	';': "semicolon", '\'': "apostrophe", ',': "comma", '.': "dot", '/': "slash", ' ': "spc", '\n': "ret", '\t': "tab",
}

// qmpEvent is one element of input-send-event's events.
type qmpEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type qmpKey struct {
	Down bool    `json:"down"`
	Key  qmpQKey `json:"key"`
}

type qmpQKey struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type qmpAbs struct {
	Axis  string `json:"axis"`
	Value int    `json:"value"`
}

type qmpBtn struct {
	Down   bool   `json:"down"`
	Button string `json:"button"`
}

// qmpCommand is the JSON of an input-send-event command with events.
func qmpCommand(events []qmpEvent) (command string, err error) {
	data, err := json.Marshal(map[string]any{"execute": "input-send-event", "arguments": map[string]any{"events": events}})
	command = string(data)
	return
}

func keyEvent(qcode string, down bool) (event qmpEvent) {
	return qmpEvent{Type: "key", Data: qmpKey{Down: down, Key: qmpQKey{Type: "qcode", Data: qcode}}}
}

// normalizeKeys checks a combination Agent sent: every key must be allowed
// (case-insensitive, aliases resolved), at most maxKeyCombo of them.
func normalizeKeys(keys []string) (qs []string, err error) {
	if len(keys) == 0 || len(keys) > maxKeyCombo {
		err = fmt.Errorf("keys: 1 to %d keys", maxKeyCombo)
		return
	}
	for _, key := range keys {
		name := strings.ToLower(strings.TrimSpace(key))
		if alias, ok := keyAliases[name]; ok {
			name = alias
		}
		if !qcodes[name] {
			err = fmt.Errorf("key %q is not allowed", key)
			return
		}
		qs = append(qs, name)
	}
	return
}

// comboEvents presses the keys in order and releases them in reverse.
func comboEvents(qs []string) (events []qmpEvent) {
	for _, q := range qs {
		events = append(events, keyEvent(q, true))
	}
	for i := len(qs) - 1; i >= 0; i-- {
		events = append(events, keyEvent(qs[i], false))
	}
	return
}

// checkText accepts US-ASCII: printable characters, newline and tab; at most
// maxTypeChars of them.
func checkText(text string) (err error) {
	if text == "" || len(text) > maxTypeChars {
		err = fmt.Errorf("text: 1 to %d characters", maxTypeChars)
		return
	}
	for i := range len(text) {
		if c := text[i]; (c < ' ' || c > '~') && c != '\n' && c != '\t' {
			err = fmt.Errorf("text: only US-ASCII (character %d is not)", i+1)
			return
		}
	}
	return
}

// charKey is the qcode of a character of checked text and whether shift is held.
func charKey(c byte) (qcode string, shift bool) {
	switch {
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return string(c), false
	case c >= 'A' && c <= 'Z':
		return string(c + 'a' - 'A'), true
	}
	if q, ok := punctKeys[c]; ok {
		return q, false
	}
	i := strings.IndexByte(shiftedChars, c)
	base := unshiftedKeys[i]
	if q, ok := punctKeys[base]; ok {
		return q, true
	}
	return string(base), true
}

// typeCommands is the QMP commands that type text, typeChunk characters each.
func typeCommands(text string) (commands []string, err error) {
	for start := 0; start < len(text); start += typeChunk {
		var events []qmpEvent
		for i := start; i < min(start+typeChunk, len(text)); i++ {
			q, shift := charKey(text[i])
			if shift {
				events = append(events, keyEvent("shift", true))
			}
			events = append(events, keyEvent(q, true), keyEvent(q, false))
			if shift {
				events = append(events, keyEvent("shift", false))
			}
		}
		var command string
		if command, err = qmpCommand(events); err != nil {
			return
		}
		commands = append(commands, command)
	}
	return
}

// scaleAxis maps pixel p of a screen size pixels wide to QEMU's 0..absMax.
func scaleAxis(p, size int) (value int) {
	if size <= 1 {
		return 0
	}
	return (2*p*absMax + size - 1) / (2 * (size - 1))
}

// pointEvents moves the pointer to pixel (x, y) of a width x height screenshot.
func pointEvents(x, y, width, height int) (events []qmpEvent, err error) {
	if x < 0 || y < 0 || x >= width || y >= height {
		err = fmt.Errorf("point (%d, %d) is outside the screenshot of %dx%d", x, y, width, height)
		return
	}
	events = []qmpEvent{
		{Type: "abs", Data: qmpAbs{Axis: "x", Value: scaleAxis(x, width)}},
		{Type: "abs", Data: qmpAbs{Axis: "y", Value: scaleAxis(y, height)}},
	}
	return
}

// clickEvents is a press and release of button.
func clickEvents(button string) (events []qmpEvent) {
	return []qmpEvent{
		{Type: "btn", Data: qmpBtn{Down: true, Button: button}},
		{Type: "btn", Data: qmpBtn{Down: false, Button: button}},
	}
}
