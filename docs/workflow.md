# Przepływ Factory i Chat

Factory wzorowana na `ralph.sh` z dfmx; bez TUI ma go zastąpić już w pierwszym kamieniu milowym. `afk` pracuje na Project z bieżącego katalogu — bez `.afk.toml` odmawia.

## Factory

Selektor przy starcie: `*` (domyślny — kolejne gotowe Tickety, jak `pick()` w ralph.sh, do wyczerpania kolejki) albo lista numerów `#12 #15` (w tej kolejności, potem koniec). Bez limitu iteracji; Factory zatrzymuje się ręcznie.

1. `afk` wybiera z Forge Ticket z labelem `ready-for-agent`, który nie ma otwartego blokera i nie jest zarezerwowany.
2. Rezerwacja: `flock` na `~/.local/state/afk/claims/<project>/<n>`; spec rezerwuje też swoje dzieci. Blokada znika ze śmiercią procesu. Jeden host — dwie maszyny z `afk` na tym samym repo są poza zakresem.
3. Spec (ticket z sub-issues) to ciąg sesji, jedna na każde dziecko w kolejności, zakończony review całości.
4. Każdy Run dostaje świeżą Sandbox VM (overlay na Base image). Po sukcesie VM jest niszczona.
5. Runy jednej Factory idą po kolei. Równoległość = kilka Factory.
6. Porażka Run zatrzymuje Factory; VM zostaje do inspekcji (shell w VM z TUI, `afk kill` niszczy). Ponowne uruchomienie Factory kontynuuje zostawiony Run (jak `resume` w ralph.sh). `needs_info` nie jest porażką — Factory idzie dalej. Nie ma TTL — nie wiadomo, ile co trwa.

Forge: GitHub i Gitea przez REST (`net/http`). Gitea nie ma natywnych sub-issues ani blokerów, więc relacje są konwencją w treści: `Part of #N`, `Blocked by: #N`. Słownik labeli konfigurowalny (`[labels]`), domyślnie z `docs/agents/triage-labels.md`.

### Co dostaje agent

- Host zapisuje Ticket (treść, komentarze, spec, blokery) do pliku w VM, np. `/work/TICKET.md`.
- Prompt mówi: gdzie są dane, że tracker jest celowo niedostępny, że nie wolno zgadywać (ADR-0003).
- `CLAUDE_CODE_OAUTH_TOKEN` jako zmienna środowiskowa sesji ssh, nigdy na dysku obrazu.
- WebFetch/WebSearch wyłączone; sieć tylko przez Egress proxy z allowlistą.
- Pluginy i skille z repo (`.claude/settings.json`, `.claude/skills/`): w klonie, pluginy zainstalowane w Base image przy budowie.
- Pluginy użytkownika: tylko te z `[agent].factory_plugins` (biała lista, domyślnie pusta) — patrz [Pluginy użytkownika](#pluginy-użytkownika). Własne skille użytkownika: żadne.

### Raport agenta

Agent kończy sesję plikiem raportu:

- `status`: `done` | `needs_info`
- `summary`, `deviations` (odstępstwa od ticketu), `doubts` (wątpliwości dla reviewera)
- przy `needs_info`: lista pytań

`needs_info` → `afk` komentuje ticket pytaniami, ustawia `needs-info`, zdejmuje `ready-for-agent`. Agent Factory nie rozmawia z człowiekiem (bez `ask_human`): część interaktywna dzieje się w Chat, przed Factory.

### Po sesji

1. **Gate**: `gate_cmd` z `.afk.toml` uruchamiany w VM. Porażka → do 3 sesji naprawczych z wyjściem bramki w prompcie.
2. **Review** innym modelem.
3. **Git** (ADR-0002): host robi tylko `git fetch` / import `git bundle` z VM do Quarantine repo; nigdy nie uruchamia niczego w klonie agenta. Walidacja: zmiany w chronionych ścieżkach (`[protect]`, zawsze `.afk.toml`, `.github/`, `.gitea/`, `.claude/` — hooki i pluginy Claude'a uruchamiają się potem na hoście człowieka) → odrzucenie. `.afk.toml` czytany zawsze z gałęzi bazowej.
4. Push i PR wyłącznie przez `afk` z poświadczeniami hosta.

## Chat

- Własna, długo żyjąca Sandbox VM; kod to snapshot gałęzi bazowej z chwili utworzenia, bez synchronizacji (świeży stan = nowy Chat).
- Claude Code interaktywnie, `--dangerously-skip-permissions`; model i effort z `[agent]`, do nadpisania przy tworzeniu.
- Cały internet przez Egress proxy (bez adresów prywatnych i hosta). Token i kod uznajemy za możliwe do wyniesienia (ADR-0003).
- `--append-system-prompt` od `afk`: izolowana VM, brak `gh` i Forge, tickety przez `afk-forge`, kod to snapshot, eksport tylko przez `afk`, pełny internet.
- `afk-forge` w VM (`issue create|view|list|comment`) woła `afk` na hoście przez kanał ssh; `afk` wykonuje to swoim tokenem, tylko dla Forge tego Project. Label `ready-for-agent` z Chat jest zamieniany na `needs-triage` — gotowość dla Factory nadaje człowiek.
- Eksport: na akcję człowieka commity Chat idą przez Quarantine repo (ta sama walidacja co w Run) na branch `chat-<nazwa>`, bez PR.
- Pluginy i skille: z repo jak w Factory, plus wszystkie pluginy i własne skille użytkownika.
- Zamknięcie Chat (z TUI): opcjonalny eksport, potem zniszczenie VM. Po restarcie hosta albo crashu `afk` VM wstaje z tego samego overlayu, Agent z `claude --continue`.

## Pluginy użytkownika

Przy starcie każdego Chat i Run `afk` kopiuje do VM katalogi pluginów włączonych w `~/.claude/settings.json` hosta (wersje z `~/.claude/plugins/installed_plugins.json`) do `/home/agent/.afk/plugins/<plugin>`, a Claude Code startuje z `--plugin-dir` dla każdego — bez instalacji, bez sieci, bez przepisywania ścieżek. Przy starcie, nie w obrazie: Base image jest wspólny dla Project, a pluginy użytkownika zmieniają się częściej. Pluginy LSP potrzebują binarki w obrazie (np. `gopls` w `provision`). Chat dostaje też `~/.claude/skills/` bez `synced/`.

Z `~/.claude` nigdy nie trafia do VM: `.credentials*`, `settings.json` (hooki, uprawnienia), `projects/` (pamięć i transkrypty innych projektów), `history.jsonl`, `session-env/`, `plans/`, `file-history/`, `skills/synced/` (skille konta claude.ai, zakładają connectory).

## TUI

ADR-0008. Osobny serwer `tmux -L afk` z konfiguracją wbudowaną w `afk` (bez `~/.tmux.conf`). Lewy panel: `afk sidebar` (bubbletea) — Chaty i Factory wszystkich Project (z metadanych domen libvirt), pogrupowane po Project; nowe domyślnie dla Project z bieżącego katalogu. Każdy Chat i Factory to okno tmux z procesem `afk`, który trzyma ssh, Egress proxy i pty; zamknięcie terminala ich nie przerywa (`detach`/`attach`).

Skróty bez prefiksu, reszta klawiszy idzie do Agenta:

| Klawisz | Akcja |
|---|---|
| `M-↑` / `M-↓` | poprzedni / następny wpis |
| `M-n` | nowy Chat |
| `M-f` | nowa Factory (pyta o selektor, domyślnie `*`) |
| `M-x` | zamknij Chat / zabij Factory |
| `M-e` | eksport brancha Chat |
| `M-s` | shell w VM (np. Run po porażce) |

Widok Factory: strumień bieżącego Run tylko do odczytu — tekst Agenta, jednolinijkowe wywołania narzędzi, zdarzenia `afk` (bramka, review, push); nagłówek z numerem i tytułem ticketu, wiekiem Run i czasem od ostatniego zdarzenia.

## Stan

Bez bazy danych: które VM żyją i do czego należą — `<metadata>` domeny libvirt (Project, Chat/Run, ticket, model); co działa — okna tmux; tickety — Forge; rezerwacje — `flock`; logi Run — pliki stream-json. SQLite (`ncruces/go-sqlite3`, bez cgo) dopiero przy zapytaniach po historii.
