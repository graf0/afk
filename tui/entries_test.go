package tui

import (
	"slices"
	"testing"

	"github.com/graf0/afk/machine"
)

func TestGroupEntries(t *testing.T) {
	machines := []machine.Machine{
		{Name: "run-7-ab", Project: "b", Kind: machine.KindRun, ID: "7-ab", Running: true},
		{Name: "chat-z", Project: "b", Kind: machine.KindChat, ID: "z", Repo: "/r/b"},
		{Name: "run-up", Project: "a", Kind: machine.KindRun, ID: "dev"}, // afk vm up: no Factory Run
		{Name: "run-3-cd", Project: "a", Kind: machine.KindRun, ID: "3-cd"},
		{Name: "chat-y", Project: "a", Kind: machine.KindChat, ID: "y"},
	}
	var got []string
	for _, e := range groupEntries(machines, []Entry{{Name: "factory/z", Project: "a", Kind: KindFactory, ID: "z"}}) {
		got = append(got, e.Project+"/"+e.Name)
	}
	want := []string{"a/chat-y", "a/factory/z", "a/run-3-cd", "b/chat-z", "b/run-7-ab"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFactories(t *testing.T) {
	// window, pane id, @afk-main, @afk-kind, project, repo, dead, status, signal, title
	out := "home\t%0\t\t\t\t\t0\t\t\tscotty\n" +
		"factory/a\t%1\t\tfactory\tp\t/r\t0\t\t\tscotty\n" + // the sidebar pane: no marker
		"factory/a\t%2\t1\tfactory\tp\t/r\t0\t\t\t#12 fix it\t2m\n" +
		"factory/b\t%4\t1\tfactory\tq\t/s\t1\t3\t\tdone\n" +
		"factory/d\t%6\t1\tfactory\tq\t/s\t1\t\t2\tkilled\n" + // died from a signal
		"factory/e\t%7\t1\tfactory\tq\t/s\t1\t\t\tgone\n" +
		"shell/x\t%5\t\t\t\t\t0\t\t\tsh\n" +
		"factory/c\t%8\t1\tchat\tq\t\t0\t\t\tx\n" +
		"junk\n"
	got := parseFactories(out)
	want := []Entry{
		{Name: "factory/a", Pane: "%2", Project: "p", Kind: KindFactory, ID: "a", Repo: "/r", Running: true, Title: "#12 fix it\t2m"},
		{Name: "factory/b", Pane: "%4", Project: "q", Kind: KindFactory, ID: "b", Repo: "/s", Title: "done", Exit: "exit 3"},
		{Name: "factory/d", Pane: "%6", Project: "q", Kind: KindFactory, ID: "d", Repo: "/s", Title: "killed", Exit: "signal 2"},
		{Name: "factory/e", Pane: "%7", Project: "q", Kind: KindFactory, ID: "e", Repo: "/s", Title: "gone", Exit: "?"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
