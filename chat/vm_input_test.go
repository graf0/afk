package chat

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// decodeEvents is the events of an input-send-event command as "type:detail" words.
func decodeEvents(t *testing.T, command string) (words []string) {
	t.Helper()
	var parsed struct {
		Execute   string `json:"execute"`
		Arguments struct {
			Events []struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			} `json:"events"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(command), &parsed); err != nil || parsed.Execute != "input-send-event" {
		t.Fatalf("command %q: %v", command, err)
	}
	for _, e := range parsed.Arguments.Events {
		switch e.Type {
		case "key":
			var k qmpKey
			_ = json.Unmarshal(e.Data, &k)
			dir := "up"
			if k.Down {
				dir = "down"
			}
			words = append(words, k.Key.Data+":"+dir)
		case "abs":
			var a qmpAbs
			_ = json.Unmarshal(e.Data, &a)
			words = append(words, a.Axis+"="+strconv.Itoa(a.Value))
		case "btn":
			var b qmpBtn
			_ = json.Unmarshal(e.Data, &b)
			dir := "up"
			if b.Down {
				dir = "down"
			}
			words = append(words, b.Button+":"+dir)
		}
	}
	return
}

func TestNormalizeKeys(t *testing.T) {
	got, err := normalizeKeys([]string{"Ctrl", "ALT", "Del"})
	if err != nil || !slices.Equal(got, []string{"ctrl", "alt", "delete"}) {
		t.Errorf("normalizeKeys = %v, %v", got, err)
	}
	for name, keys := range map[string][]string{
		"empty":        {},
		"too many":     {"a", "b", "c", "d", "e", "f", "g"},
		"unknown":      {"ctrl", "powerdown"},
		"injection":    {`a"}]}`},
		"empty string": {""},
	} {
		if _, err := normalizeKeys(keys); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := normalizeKeys(make([]string, maxKeyCombo)); err == nil {
		t.Error("empty names: want an error")
	}
}

func TestComboEvents(t *testing.T) {
	command, err := qmpCommand(comboEvents([]string{"ctrl", "alt", "delete"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ctrl:down", "alt:down", "delete:down", "delete:up", "alt:up", "ctrl:up"}
	if got := decodeEvents(t, command); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestCheckText(t *testing.T) {
	for _, ok := range []string{"hello", "Hello, World!\n\t~`", strings.Repeat("a", maxTypeChars)} {
		if err := checkText(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", maxTypeChars+1), "zażółć", "a\x00b", "a\x1bb", "a\rb"} {
		if err := checkText(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestCharKey(t *testing.T) {
	tests := []struct {
		char  byte
		qcode string
		shift bool
	}{
		{'a', "a", false}, {'Z', "z", true}, {'7', "7", false}, {'&', "7", true}, {'!', "1", true}, {')', "0", true},
		{' ', "spc", false}, {'\n', "ret", false}, {'\t', "tab", false}, {'-', "minus", false}, {'_', "minus", true},
		{'=', "equal", false}, {'+', "equal", true}, {'\\', "backslash", false}, {'|', "backslash", true},
		{':', "semicolon", true}, {'"', "apostrophe", true}, {'<', "comma", true}, {'>', "dot", true}, {'?', "slash", true},
		{'~', "grave_accent", true}, {'`', "grave_accent", false}, {'{', "bracket_left", true}, {'}', "bracket_right", true},
	}
	for _, tt := range tests {
		if q, shift := charKey(tt.char); q != tt.qcode || shift != tt.shift {
			t.Errorf("charKey(%q) = %q, %v; want %q, %v", tt.char, q, shift, tt.qcode, tt.shift)
		}
	}
	// Every printable character maps to an allowed key.
	for c := byte(' '); c <= '~'; c++ {
		if q, _ := charKey(c); !qcodes[q] {
			t.Errorf("charKey(%q) = %q, not an allowed qcode", c, q)
		}
	}
}

func TestTypeCommands(t *testing.T) {
	commands, err := typeCommands("Hi!")
	if err != nil || len(commands) != 1 {
		t.Fatalf("commands = %v, %v", commands, err)
	}
	want := []string{
		"shift:down", "h:down", "h:up", "shift:up",
		"i:down", "i:up",
		"shift:down", "1:down", "1:up", "shift:up",
	}
	if got := decodeEvents(t, commands[0]); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	long, _ := typeCommands(strings.Repeat("a", typeChunk*2+1))
	if len(long) != 3 {
		t.Errorf("%d chunks for %d characters, want 3", len(long), typeChunk*2+1)
	}
}

func TestPointEvents(t *testing.T) {
	tests := []struct {
		x, y, w, h int
		want       []string
	}{
		{0, 0, 1920, 1080, []string{"x=0", "y=0"}},
		{1919, 1079, 1920, 1080, []string{"x=32767", "y=32767"}},
		{960, 540, 1921, 1081, []string{"x=16384", "y=16384"}},
		{100, 50, 201, 101, []string{"x=16384", "y=16384"}},
	}
	for _, tt := range tests {
		events, err := pointEvents(tt.x, tt.y, tt.w, tt.h)
		if err != nil {
			t.Fatal(err)
		}
		command, _ := qmpCommand(events)
		if got := decodeEvents(t, command); !slices.Equal(got, tt.want) {
			t.Errorf("(%d,%d) in %dx%d = %v, want %v", tt.x, tt.y, tt.w, tt.h, got, tt.want)
		}
	}
	for _, out := range [][2]int{{-1, 0}, {0, -1}, {1920, 0}, {0, 1080}} {
		if _, err := pointEvents(out[0], out[1], 1920, 1080); err == nil {
			t.Errorf("%v: want an error", out)
		}
	}
}

func TestClickEvents(t *testing.T) {
	command, _ := qmpCommand(clickEvents("right"))
	if got := decodeEvents(t, command); !slices.Equal(got, []string{"right:down", "right:up"}) {
		t.Errorf("events = %v", got)
	}
}
