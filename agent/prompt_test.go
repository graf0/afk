package agent

import (
	"strings"
	"testing"

	"github.com/graf0/afk/forge"
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

func TestTicketFile(t *testing.T) {
	got := TicketFile(forge.Ticket{Number: 3, Title: "T", Body: "B", Comments: []string{"c1", "c2"}})
	want := "# #3 T\n\nB\n\n---\nKomentarz:\nc1\n\n---\nKomentarz:\nc2\n"
	if got != want {
		t.Errorf("TicketFile() = %q, want %q", got, want)
	}
}

func TestFixPrompt(t *testing.T) {
	task := Task{Branch: "ticket-5", Protected: []string{".afk.toml"}}
	prompt := FixPrompt(task, "go test ./...", "FAIL: TestX \x1b[2J\n")
	for _, want := range []string{"`go test ./...`", "FAIL: TestX", "minimalnie", "nie zmieniaj zakresu", "Commituj", "`.afk.toml`", "czyste drzewo"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "\x1b") {
		t.Error("prompt carries a control character from the gate output")
	}
}

func TestReviewPrompt(t *testing.T) {
	prompt := ReviewPrompt(Task{RepoDir: "/work/repo", Branch: "ticket-5", BaseRef: "origin/main", TicketPath: "/work/TICKET.md"})
	for _, want := range []string{"`ticket-5`", "git diff origin/main...HEAD", "`/work/TICKET.md`", "`findings_fixed`", "`findings_skipped`", "Commituj"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
}
