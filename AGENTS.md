- pisz do mnie po polsku
- bądź krytyczny, punktuj problemy, nie zgadzaj się, gdy uważasz że trzeba
- zmienne, funkcje, stałe, typy - nazwyaj po angielsku
- pisz prosty, idiomatyczny kod w Go, co najmniej Go 1.26
- stdlib first, zewnętrzny pakiet → zapytaj
- dopasuj istniejący styl
- testy: jeśli zmiana tego wymaga, popraw istniejące i dopisz nowe; jeden plik testów na plik `.go` (wyjątek: test integracyjny za tagiem builda `integration` w osobnym pliku `<plik>_integration_test.go`)
- przed PR-em: `task lint`
- instrukcja użytkownika > ten plik
- sygnatury funkcji z nazwanymi zwrotami: `func Name(arg type) (result type, err error)`.
- błędy: `err = fmt.Errorf("doing x: %w", err); return` — nie `return ..., fmt.Errorf(...)`.
- `context.Context` zawsze pierwszy param.
- importy: stdlib → zewnętrzne → wewnętrzne (puste linie).
- kolejność deklaracji: plik do ~300 linii — `const → var → type → func`; większy — idiom Go: typ, zaraz za nim konstruktor i metody; const/var wspólne dla pliku na górze, lokalne dla grupy na początku grupy. Funkcje dziel na logiczne grupy oddzielone komentarzem `// --- <nazwa grupy> ---`.
- narzędzia Go w `go.mod` (`tool`): `go tool task`, bez globalnej instalacji.

## Agent skills

### Issue tracker

Issues live in GitHub Issues (`graf0/afk`): via `gh` on the host, via `afk-cli` in a Chat. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.
