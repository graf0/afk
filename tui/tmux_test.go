package tui

import (
	"slices"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for out, want := range map[string][2]int{"tmux 3.5a\n": {3, 5}, "tmux next-3.6": {3, 6}, "tmux 2.9": {2, 9}, "tmux 3.3-rc": {3, 3}} {
		major, minor, err := parseVersion(out)
		if err != nil || major != want[0] || minor != want[1] {
			t.Errorf("parseVersion(%q) = %d, %d, %v; want %v", out, major, minor, err, want)
		}
	}
	if _, _, err := parseVersion("tmux master"); err == nil {
		t.Error("parseVersion(master) succeeded")
	}
}

func TestStartArgs(t *testing.T) {
	args := startArgs("/bin/afk")
	line := strings.Join(args, " ")
	for _, want := range []string{
		"-L afk -f /dev/null new-session -s afk",
		"bind-key -n M-Up send-keys -t :.0 Up",
		"bind-key -n M-Down send-keys -t :.0 Down",
		"bind-key -n M-n send-keys -t :.0 M-n",
		"bind-key -n M-e send-keys -t :.0 M-e",
		"bind-key -n M-x send-keys -t :.0 M-x",
		"bind-key -n M-s send-keys -t :.0 M-s",
		"split-window -hbd -l 32 /bin/afk sidebar",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("start args lack %q: %s", want, line)
		}
	}
	if args[len(args)-4] == ";" && args[len(args)-1] == ";" {
		t.Error("trailing separator")
	}
}

func TestSidebarSplit(t *testing.T) {
	got := sidebarSplit("afk:=chat-x", "/bin/afk", "chat-x")
	want := []string{"split-window", "-hbd", "-l", "32", "-t", "afk:=chat-x", "/bin/afk", "sidebar", "--current", "chat-x"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestConfigNoPrefix(t *testing.T) {
	var lines []string
	for _, c := range config() {
		lines = append(lines, strings.Join(c, " "))
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"prefix None", "prefix2 None", "unbind-key C-b"} {
		if !strings.Contains(all, want) {
			t.Errorf("config lacks %q", want)
		}
	}
}

func TestWithoutTmux(t *testing.T) {
	got := withoutTmux([]string{"A=1", "TMUX=/tmp/x,1,0", "TMUX_PANE=%1"})
	if !slices.Equal(got, []string{"A=1", "TMUX_PANE=%1"}) {
		t.Errorf("got %v", got)
	}
}
