package agent

import (
	"strings"
	"testing"
)

func TestPrompt(t *testing.T) {
	prompt := Prompt(Task{
		RepoDir:    "/work/repo",
		Branch:     "ticket-5",
		BaseRef:    "origin/main",
		TicketPath: "/work/TICKET.md",
		Protected:  []string{".afk.toml", ".claude/"},
	})
	for _, want := range []string{
		"`/work/TICKET.md`",
		"branchu `ticket-5`",
		"`/work/repo`",
		"`origin/main`",
		"celowo niedostępny",
		"nie zgaduj",
		"Commituj",
		"czyste drzewo",
		"`needs_info`",
		"`questions`",
		"`.afk.toml`, `.claude/`",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(Prompt(Task{}), "chronionych") {
		t.Error("prompt mentions protected paths without any")
	}
}
