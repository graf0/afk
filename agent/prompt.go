package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/graf0/afk/forge"
)

// Task is what the prompt tells the Agent about its Run.
type Task struct {
	RepoDir    string   // the Agent's clone, e.g. /work/repo
	Branch     string   // checked out branch, e.g. ticket-5
	BaseRef    string   // base branch in the clone, e.g. origin/main
	TicketPath string   // ticket file, outside the clone
	SpecPath   string   // the Spec of the ticket as context, outside the clone; empty without a Spec
	Protected  []string // [protect].paths
	Plugins    []string // names of the plugins in the image, without marketplace
	Tools      []Tool   // [tool.<name>] by name; Prompt already trimmed
	Resumed    bool     // the clone holds the Agent's earlier work: the session continues it
}

// Tool is what the prompt tells the Agent about a Tool; Prompt may be empty.
type Tool struct {
	Name   string
	Prompt string
	Env    []string // names of its env variables, sorted; never the values
}

// skillsPlugin is the plugin whose implement and code-review skills the
// Run and review prompts call, like docs/reference/ralph.sh.
const skillsPlugin = "mattpocock-skills"

// skillCommand is the slash command invoking skill of skillsPlugin followed
// by args, or empty when the plugin is not in the image.
func (t Task) skillCommand(skill, args string) (command string) {
	if slices.Contains(t.Plugins, skillsPlugin) {
		command = "/" + skillsPlugin + ":" + skill + " " + args
	}
	return
}

// Prompt is the Agent's prompt for task, modelled on rules() in
// docs/reference/ralph.sh.
func Prompt(task Task) (prompt string) {
	var b strings.Builder
	b.WriteString(task.skillCommand("implement", ""))
	fmt.Fprintf(&b, "Zrealizuj ticket z pliku `%s`.\n\n", task.TicketPath)
	b.WriteString("Zasady:\n")
	fmt.Fprintf(&b, "- Pracujesz na gotowym branchu `%s` w świeżym klonie repo `%s` (bieżący katalog), odgałęzionym od `%s`. Nie zmieniaj brancha.\n", task.Branch, task.RepoDir, task.BaseRef)
	fmt.Fprintf(&b, "- Treść ticketu leży w `%s`, poza repo; nie commituj jej. To jedyne źródło wiedzy o zadaniu: tracker jest celowo niedostępny (nie masz `gh` ani poświadczeń), więc nie szukaj zgłoszeń w sieci ani w historii commitów i nie zgaduj brakujących szczegółów. Gdy ticket odsyła do czegoś, czego tam nie ma, zwróć `needs_info`. Podaj tę ścieżkę subagentom: nie dziedziczą tego promptu.\n", task.TicketPath)
	if task.SpecPath != "" {
		fmt.Fprintf(&b, "- Ticket jest częścią speca z pliku `%s` (poza repo, nie commituj go): to kontekst, nie zakres. Poprzednie tickety speca są już w historii brancha; zrób tylko ten ticket. Podaj tę ścieżkę subagentom.\n", task.SpecPath)
	}
	b.WriteString("- Nikt nie odpowie na pytania w trakcie pracy. Seamy testowe dobierz jak w najbliższych istniejących testach w repo i wymień wybór w `summary`.\n")
	b.WriteString("- Sieć działa tylko przez proxy z listą dozwolonych domen; WebFetch i WebSearch są wyłączone. Gdy do pracy potrzebujesz czegoś spoza tej listy, zwróć `needs_info`.\n")
	writeCommitRules(&b, task)
	b.WriteString("- Zwróć status `done`, gdy ticket jest zrobiony i zacommitowany: w `summary` 2-5 zdań co zrobiono, w `deviations` każde odstępstwo od ticketu z powodem (pusta lista, gdy nie było), w `doubts` rzeczy do sprawdzenia przez człowieka (opcjonalnie), w `reason` jedno zdanie. Zwykłe odstępstwo z dobrego powodu tylko zgłoś, nie zatrzymuj się.\n")
	b.WriteString("- Zwróć `needs_info`, gdy ticket jest niejasny, sprzeczny albo za duży: powód w `reason`, konkretne pytania do autora ticketu w `questions`, `summary` puste, `deviations` pusta lista — wtedy bez commitów.\n")
	writeTools(&b, task)
	writeResumeNote(&b, task)
	return b.String()
}

// FixPrompt is the prompt of a fix session: the gate failed on the Agent's
// work (gatePrompt() in docs/reference/ralph.sh). output is the end of the
// gate's output, from the guest.
func FixPrompt(task Task, gateCmd, output string) (prompt string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Bramka `%s` nie przeszła na Twojej pracy w tym klonie. Koniec jej outputu:\n\n%s\n\n", gateCmd, Sanitize(strings.TrimSpace(output)))
	b.WriteString("Napraw przyczynę minimalnie, nie zmieniaj zakresu ticketu, uruchom bramkę i zacommituj poprawkę. Zwróć `needs_info` tylko wtedy, gdy naprawa jest niemożliwa bez decyzji człowieka.\n\n")
	b.WriteString("Zasady:\n")
	writeCommitRules(&b, task)
	b.WriteString("- Zwróć status `done`, gdy bramka przechodzi i poprawka jest zacommitowana: w `summary` 1-3 zdania co poprawiono, w `deviations` pusta lista, w `reason` jedno zdanie.\n")
	writeTools(&b, task)
	return b.String()
}

// ReviewPrompt is the prompt of the review session: the whole branch
// against the base (review() in docs/reference/ralph.sh).
func ReviewPrompt(task Task) (prompt string) {
	var b strings.Builder
	if command := task.skillCommand("code-review", task.BaseRef); command != "" {
		b.WriteString(command + "\n\n")
	}
	fmt.Fprintf(&b, "Zrób code review całego brancha `%s` względem `%s` (zakres zmian: `git diff %s...HEAD`), w bieżącym klonie `%s`. Sprawdź poprawność, zgodność z ticketem w `%s` (poza repo, nie commituj go; to jedyne źródło wiedzy o zadaniu, nie szukaj zgłoszeń w sieci) oraz zgodność z konwencjami repo. Podaj te ścieżki subagentom: nie dziedziczą tego promptu.\n\n", task.Branch, task.BaseRef, task.BaseRef, task.RepoDir, task.TicketPath)
	b.WriteString("Po review popraw wszystkie znaleziska i zacommituj poprawki. Jeśli nie ma czego poprawiać, nie commituj. Nie zmieniaj brancha.\n\n")
	b.WriteString("Zasady:\n")
	writeCommitRules(&b, task)
	b.WriteString("- Zwróć status `done`: w `summary` krótki werdykt, w `findings_fixed` co poprawiłeś, w `findings_skipped` czego świadomie nie poprawiłeś i dlaczego, w `deviations` pusta lista, w `reason` jedno zdanie.\n")
	writeTools(&b, task)
	writeResumeNote(&b, task)
	return b.String()
}

// writeTools appends the "Narzędzia" section: each Tool, with its prompt if
// any (continuation lines indented) and the names of its env variables.
// Without Tools there is no section.
func writeTools(b *strings.Builder, task Task) {
	if len(task.Tools) == 0 {
		return
	}
	b.WriteString("\nNarzędzia dostępne w tej maszynie:\n")
	for _, tool := range task.Tools {
		fmt.Fprintf(b, "- `%s`", tool.Name)
		if tool.Prompt != "" {
			fmt.Fprintf(b, ": %s", strings.ReplaceAll(tool.Prompt, "\n", "\n  "))
		}
		if len(tool.Env) > 0 {
			fmt.Fprintf(b, "\n  Zmienne środowiskowe: %s", quoteList(tool.Env))
		}
		b.WriteString("\n")
	}
}

// writeResumeNote appends the note of a resumed session (resume_note in
// docs/reference/ralph.sh, resume_clone).
func writeResumeNote(b *strings.Builder, task Task) {
	if !task.Resumed {
		return
	}
	fmt.Fprintf(b, "\nWznawiasz przerwaną pracę: klon zawiera Twój wcześniejszy stan (commity, być może niezacommitowane zmiany). Sprawdź `git log %s..HEAD` i `git status`, ustal, na jakim etapie skończyłeś, i dokończ tylko to, co zostało; nie zaczynaj od nowa.\n", task.BaseRef)
}

// writeCommitRules appends the rules every session follows about git.
func writeCommitRules(b *strings.Builder, task Task) {
	b.WriteString("- Commituj swoją pracę. Nie pushuj i nie twórz PR (nie ma remote ani poświadczeń) — wynik pobierze `afk`.\n")
	if len(task.Protected) > 0 {
		fmt.Fprintf(b, "- Nie zmieniaj chronionych ścieżek: %s. Commit, który ich dotyka (także zmiana trybu, symlink, przeniesienie), zostanie odrzucony w całości.\n", quoteList(task.Protected))
	}
	b.WriteString("- Na koniec zostaw czyste drzewo robocze (wszystko zacommitowane).\n")
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
