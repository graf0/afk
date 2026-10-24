package agent

import (
	"fmt"
	"strings"

	"github.com/graf0/afk/forge"
)

// Task is what the prompt tells the Agent about its Run.
type Task struct {
	RepoDir    string   // the Agent's clone, e.g. /work/repo
	Branch     string   // checked out branch, e.g. ticket-5
	BaseRef    string   // base branch in the clone, e.g. origin/main
	TicketPath string   // ticket file, outside the clone
	Protected  []string // [protect].paths
}

// Prompt is the Agent's prompt for task, modelled on rules() in
// docs/reference/ralph.sh.
func Prompt(task Task) (prompt string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Zrealizuj ticket z pliku `%s`.\n\n", task.TicketPath)
	b.WriteString("Zasady:\n")
	fmt.Fprintf(&b, "- Pracujesz na gotowym branchu `%s` w świeżym klonie repo `%s` (bieżący katalog), odgałęzionym od `%s`. Nie zmieniaj brancha.\n", task.Branch, task.RepoDir, task.BaseRef)
	fmt.Fprintf(&b, "- Treść ticketu leży w `%s`, poza repo; nie commituj jej. To jedyne źródło wiedzy o zadaniu: tracker jest celowo niedostępny (nie masz `gh` ani poświadczeń), więc nie szukaj zgłoszeń w sieci ani w historii commitów i nie zgaduj brakujących szczegółów. Gdy ticket odsyła do czegoś, czego tam nie ma, zwróć `needs_info`. Podaj tę ścieżkę subagentom: nie dziedziczą tego promptu.\n", task.TicketPath)
	b.WriteString("- Nikt nie odpowie na pytania w trakcie pracy. Seamy testowe dobierz jak w najbliższych istniejących testach w repo i wymień wybór w `summary`.\n")
	b.WriteString("- Sieć działa tylko przez proxy z listą dozwolonych domen; WebFetch i WebSearch są wyłączone. Gdy do pracy potrzebujesz czegoś spoza tej listy, zwróć `needs_info`.\n")
	b.WriteString("- Commituj swoją pracę. Nie pushuj i nie twórz PR (nie ma remote ani poświadczeń) — wynik pobierze `afk`.\n")
	if len(task.Protected) > 0 {
		fmt.Fprintf(&b, "- Nie zmieniaj chronionych ścieżek: %s. Commit, który ich dotyka (także zmiana trybu, symlink, przeniesienie), zostanie odrzucony w całości.\n", quoteList(task.Protected))
	}
	b.WriteString("- Na koniec zostaw czyste drzewo robocze (wszystko zacommitowane).\n")
	b.WriteString("- Zwróć status `done`, gdy ticket jest zrobiony i zacommitowany: w `summary` 2-5 zdań co zrobiono, w `deviations` każde odstępstwo od ticketu z powodem (pusta lista, gdy nie było), w `doubts` rzeczy do sprawdzenia przez człowieka (opcjonalnie), w `reason` jedno zdanie. Zwykłe odstępstwo z dobrego powodu tylko zgłoś, nie zatrzymuj się.\n")
	b.WriteString("- Zwróć `needs_info`, gdy ticket jest niejasny, sprzeczny albo za duży: powód w `reason`, konkretne pytania do autora ticketu w `questions`, `summary` puste, `deviations` pusta lista — wtedy bez commitów.\n")
	return b.String()
}

// TicketFile renders the ticket file the Agent reads: title, body, comments.
func TicketFile(ticket forge.Ticket) (text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "# #%d %s\n\n%s\n", ticket.Number, ticket.Title, ticket.Body)
	for _, comment := range ticket.Comments {
		fmt.Fprintf(&b, "\n---\nKomentarz:\n%s\n", comment)
	}
	return b.String()
}

// quoteList renders items as `a`, `b`.
func quoteList(items []string) (text string) {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = "`" + item + "`"
	}
	return strings.Join(quoted, ", ")
}
