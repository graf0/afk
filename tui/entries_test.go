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
	for _, e := range groupEntries(machines) {
		got = append(got, e.Project+"/"+e.Name)
	}
	want := []string{"a/chat-y", "a/run-3-cd", "b/chat-z", "b/run-7-ab"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
