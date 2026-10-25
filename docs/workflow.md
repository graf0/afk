# Przepływ Factory i Chat

Factory wzorowana na `ralph.sh` z dfmx; bez TUI ma go zastąpić już w pierwszym kamieniu milowym. `afk` pracuje na Project z bieżącego katalogu — bez `.afk.toml` odmawia.

## Factory

`afk factory '#N'` (na razie jeden numer: Ticket albo Spec) bierze tylko Ticket z labelem `[labels].ready_for_agent` — bez niego odmawia, bez flagi obejścia. Selektor przy starcie: `*` (domyślny — kolejne gotowe Tickety, jak `pick()` w ralph.sh, do wyczerpania kolejki) albo lista numerów `#12 #15` (w tej kolejności, potem koniec). Bez limitu iteracji; Factory zatrzymuje się ręcznie.

1. `afk` wybiera z Forge Ticket z labelem `ready-for-agent`, który nie ma otwartego blokera i nie jest zarezerwowany.
2. Rezerwacja: `flock` na `~/.local/state/afk/claims/<project>/<n>`; spec rezerwuje też swoje dzieci. Blokada znika ze śmiercią procesu. Jeden host — dwie maszyny z `afk` na tym samym repo są poza zakresem.
3. Spec (ticket z sub-issues) to ciąg Runów, jeden na każde dziecko w kolejności, na wspólnym branchu, zakończony review całości — patrz [Spec](#spec).
4. Każdy Run dostaje świeżą Sandbox VM (overlay na Base image). Po sukcesie VM jest niszczona.
5. Runy jednej Factory idą po kolei. Równoległość = kilka Factory.
6. Porażka Run zatrzymuje Factory; VM zostaje do inspekcji (shell w VM z TUI, `afk kill` niszczy). Ponowne uruchomienie Factory kontynuuje zostawiony Run (jak `resume` w ralph.sh). `needs_info` Ticketu nie jest porażką — VM jest niszczona, Factory idzie dalej (na dziecku Spec jest: patrz [Spec](#spec)). Nie ma TTL — nie wiadomo, ile co trwa.

Forge: GitHub i Gitea przez REST (`net/http`). Gitea nie ma natywnych sub-issues ani blokerów, więc relacje są konwencją w treści: `Part of #N`, `Blocked by: #N`. Słownik labeli konfigurowalny (`[labels]`), domyślnie z `docs/agents/triage-labels.md`.

### Co dostaje agent

- Host zapisuje Ticket (treść, komentarze, spec, blokery) do pliku w VM, np. `/work/TICKET.md`.
- Prompt mówi: gdzie są dane, że tracker jest celowo niedostępny, że nie wolno zgadywać (ADR-0003).
- `CLAUDE_CODE_OAUTH_TOKEN` jako zmienna środowiskowa sesji ssh, nigdy na dysku obrazu.
- WebFetch/WebSearch wyłączone; sieć tylko przez Egress proxy z allowlistą.
- Skille z repo (`.claude/skills/`): w klonie. Pluginy: tylko te zadeklarowane w Project config, zainstalowane w Base image przy budowie — patrz [Pluginy](#pluginy). Nic z `~/.claude` hosta (ADR-0009).

### Raport agenta

Agent kończy sesję plikiem raportu:

- `status`: `done` | `needs_info`
- `summary`, `deviations` (odstępstwa od ticketu), `doubts` (wątpliwości dla reviewera)
- przy `needs_info`: lista pytań

`needs_info` → `afk` komentuje ticket pytaniami, ustawia `needs-info`, zdejmuje `ready-for-agent`; VM jest niszczona, wyniku się nie pobiera. Agent Factory nie rozmawia z człowiekiem (bez `ask_human`): część interaktywna dzieje się w Chat, przed Factory.

Strumień Agenta (stream-json) idzie do pliku logu w `~/.local/state/afk/<project>/logs/`, a na wyjście `afk` tekst Agenta i jednolinijkowe wywołania narzędzi, bez znaków sterujących i formatujących (gość jest niezaufany). Log jest rotowany: po 128 MiB przechodzi do `<log>.1` (poprzedni `.1` znika), więc na dysku najwyżej ok. 256 MiB, a Run nie jest przerywany z powodu rozmiaru; najstarsza część strumienia przepada.

### Po sesji

1. **Gate**: `[gate].cmd` z `.afk.toml` uruchamiany w VM po `done`. Porażka → sesja naprawcza Agenta z końcówką wyjścia bramki w prompcie (napraw minimalnie, nie zmieniaj zakresu), do `[gate].max_fixes` sesji (poprawki z review mają osobny budżet); wyczerpanie albo status inny niż `done` → porażka Run (VM zostaje). Wyjście każdej próby w pliku obok logu Run (`<log>-gate-N.txt`). Raport sesji naprawczej dopisuje się do raportu Run; sesja naprawcza → `ready-for-human`. Każda nowa porcja commitów z VM przechodzi tę samą walidację co pierwsza (przodek bazy, chronione ścieżki).
2. **Review** modelem `[agent].review_model`: sesja na całym branchu względem bazy, poprawia znaleziska i commituje. Poprawki → osobny commit `code review: <tytuł>` (też przez bramkę, z własnymi sesjami naprawczymi). Raport review (`findings_fixed`, `findings_skipped`) to drugi komentarz na PR; niepuste `findings_skipped` → `ready-for-human`.
3. **Git** (ADR-0002): host robi tylko `git fetch` / import `git bundle` z VM do Quarantine repo; nigdy nie uruchamia niczego w klonie agenta. Walidacja: zmiany w chronionych ścieżkach (`[protect]`, zawsze `.afk.toml`, `.github/`, `.gitea/` oraz na dowolnej głębokości `**/.claude/` i `**/.mcp.json` — hooki, pluginy i serwery MCP Claude'a uruchamiają się potem na hoście człowieka, także z podkatalogów) → odrzucenie. Sprawdzany jest każdy commit, bez wykrywania rename, z pominięciem wielkości liter; zastąpienie katalogu nadrzędnego plikiem lub symlinkiem też się liczy. `CLAUDE.md`, `.gitattributes`, `.gitmodules` są dozwolone. `.afk.toml` czytany zawsze z gałęzi bazowej.
4. Push i PR wyłącznie przez `afk` z poświadczeniami hosta. Commity implementacji są squashowane w Quarantine repo do jednego commita na bazie (`<tytuł ticketu> (#N)`, treść: `summary`, autor: tożsamość gita użytkownika z jego repo), a poprawki review do drugiego, `code review: <tytuł>`, na wierzchu; oba walidowane ponownie. Push `ticket-N` z Quarantine repo idzie na `https://github.com/<repo>.git` tokenem Forge w nagłówku HTTP przekazanym gitowi przez zmienne `GIT_CONFIG_*` (nie na argv, nie na dysku, tylko do tego procesu gita), bez globalnego i systemowego configu gita użytkownika (żadne `insteadOf` nie przekieruje go na ssh ani inny host), bez przekierowań HTTP i bez credential helperów; tylko tworzy branch, nigdy go nie nadpisuje. Branch `ticket-N` na remote → odmowa przed startem VM. Potem PR z `Closes #N`, komentarz z raportem, `ready-for-human` na PR przy odstępstwach lub wątpliwościach, zdjęcie `ready-for-agent`. Tekst Agenta na GitHubie i w commicie: bez @-wzmianek i bez słów zamykających issue (`Fixes #12` itp.) — oba łamane zero-width space — bez komentarzy HTML (`<!--` → `&lt;!--`), najwyżej 60000 bajtów; własne `Closes #N` `afk` dopisuje po tym. Porażka kroku w Forge podaje, co zrobiono, i po linii na każdy krok do zrobienia ręcznie.

### Spec

`afk factory '#N'`, gdy N ma sub-issues (zachowanie jak `do_spec` w `ralph.sh`): ciąg Runów na branchu `ticket-<spec>`, zakończony review całego brancha.

- **Dzieci**: otwarte sub-issues rosnąco po numerze; za każdym razem pierwsze bez otwartego blokera (natywna zależność GitHuba albo linia `Blocked by: #a, #b` w treści, liczy się stan blokera). Otwarte dzieci wszystkie zablokowane → porażka. Każde otwarte dziecko musi mieć `ready-for-agent` (jak `pick()` w `ralph.sh`), inaczej odmowa przed pierwszą sesją: gotowość nadaje człowiek (ADR-0003). Dzieci ze swoimi sub-issues nie są obsługiwane.
- **Run dziecka**: świeża VM; klon startuje z końca brancha `ticket-<spec>` (pierwsze dziecko: z bazy), `origin/<baza>` w klonie zostaje bazą. Prompt dostaje dziecko (`/work/TICKET.md`) i Spec jako kontekst (`/work/SPEC.md`: „kontekst, nie zakres”). Bramka przechodzi dla każdego dziecka; implementacja dziecka jest squashowana do jednego commita `<tytuł dziecka> (#M)` na końcu brancha. Walidacja: wynik sesji musi zawierać koniec brancha i nie dotykać chronionych ścieżek, a po squashu cała gałąź — bazę i te same chronione ścieżki. Review dziecka nie ma; jest jedno, na końcu.
- **Po dziecku**: push brancha; po pierwszym dziecku draft PR (tytuł Spec) z `Closes #<spec>` i `Closes` dla wszystkich dzieci; komentarz z raportem dziecka na PR; `ready-for-human` na PR, gdy dziecko miało odstępstwa, wątpliwości albo sesję naprawczą bramki (od razu, bo Run może być wznowiony w innym procesie); komentarz na dziecku z odnośnikiem do PR, zamknięcie dziecka, zdjęcie z niego `ready-for-agent`.
- **`needs_info` na dziecku**: komentarz z pytaniami i `needs-info` na dziecku oraz komentarz i `needs-info` na Spec, zdjęcie `ready-for-agent` z obu, porażka. Zrobione dzieci zostają na branchu i na PR (draft). Aby Spec ruszył ponownie, człowiek przywraca `ready-for-agent` na dziecku i na Spec.
- **Review całości**: po ostatnim dziecku jedna sesja review (jak w Run) w świeżej VM na końcu brancha, względem bazy; poprawki to osobny commit `code review: <tytuł Spec>`, przez bramkę i walidację. Potem: push, komentarz z raportem review, nowy opis PR (`Closes` i lista commitów brancha), `ready-for-human` przy poprawkach bramki albo pominiętych znaleziskach, PR gotowy do review (GraphQL `markPullRequestReadyForReview`: REST tego nie umie), zdjęcie `ready-for-agent` ze Spec.
- **Wznowienie**: branch `ticket-<spec>` na remote z draft PR (albo bez PR) → kontynuacja od następnego otwartego dziecka, a gdy żadne nie zostało — od review. Czubek brancha z remote wchodzi do Quarantine repo i przechodzi tę samą walidację, a baza to merge-base z bieżącą bazą (branch bazowy mógł pójść naprzód). PR niebędący draftem albo zamknięty → błąd z instrukcją (zakończ lub zamknij PR, usuń branch `git push origin --delete ticket-<spec>`, uruchom ponownie); wynik z poprzedniego procesu zostawiony w Quarantine repo (porażka kroku w Forge) trzeba usunąć ręcznie, jak przy Run.
- **Push na istniejący branch** jest bezpieczny: `--force-with-lease=<ref>:<sha>`, gdzie sha to ostatni commit, który `afk` wypchnął albo odczytał; nowy commit zawsze leży na starym, więc to tylko fast-forward, a cudzy push pomiędzy kończy się odmową. Pierwszy push tworzy branch (lease z pustym sha). Nigdy blind force.

## Chat

- Własna, długo żyjąca Sandbox VM; kod to snapshot gałęzi bazowej z chwili utworzenia, bez synchronizacji (świeży stan = nowy Chat).
- Claude Code interaktywnie, `--dangerously-skip-permissions`; model i effort z `[agent]`, do nadpisania przy tworzeniu.
- Cały internet przez Egress proxy (bez adresów prywatnych i hosta). Token i kod uznajemy za możliwe do wyniesienia (ADR-0003).
- `--append-system-prompt` od `afk`: izolowana VM, brak `gh` i Forge, tickety przez `afk-forge`, kod to snapshot, eksport tylko przez `afk`, pełny internet.
- `afk-forge` w VM (`issue create|view|list|comment`) woła `afk` na hoście przez kanał ssh; `afk` wykonuje to swoim tokenem, tylko dla Forge tego Project. Label `ready-for-agent` z Chat jest zamieniany na `needs-triage` — gotowość dla Factory nadaje człowiek.
- Eksport: na akcję człowieka commity Chat idą przez Quarantine repo (ta sama walidacja co w Run) na branch `chat-<nazwa>`, bez PR.
- Pluginy i skille: to samo co w Factory, czyli co jest w obrazie i w repo; nic z `~/.claude` hosta (ADR-0009).
- Zamknięcie Chat (z TUI): opcjonalny eksport, potem zniszczenie VM. Po restarcie hosta albo crashu `afk` VM wstaje z tego samego overlayu, Agent z `claude --continue`.

## Pluginy

Agent dostaje wyłącznie pluginy wymienione wprost (ADR-0009): `[agent].plugins` (`<nazwa>@<marketplace>`) i `[agent].marketplaces` (`owner/repo` albo URL gita) z `.afk.toml` oraz `enabledPlugins` (włączone) i `extraKnownMarketplaces` (źródło `github` lub `git`) z `.claude/settings.json` Project. `afk image build` dodaje marketplace'y (`claude plugin marketplace add`, zawsze też `anthropics/claude-plugins-official`, gdy jest jakikolwiek plugin) i instaluje pluginy (`claude plugin install --scope user`); `github.com` jest dozwolony przez Egress proxy budowy. `afk` sprawdza tylko format id i że źródło nie zaczyna się od `-`, resztę (brak pluginu, marketplace'u) zgłasza `claude` przy budowie. Zmiana listy wymaga przebudowy obrazu; ustawienia są czytane z drzewa roboczego jak `.afk.toml` przy budowie, a przez Run z gałęzi bazowej (brak pliku = brak pluginów). Pluginy LSP potrzebują binarki w obrazie (np. `gopls` w `provision`).

Gdy efektywna lista zawiera `mattpocock-skills`, prompt Run zaczyna się od `/mattpocock-skills:implement`, a prompt review od `/mattpocock-skills:code-review <baza>` (jak w `ralph.sh`); bez niego zostają zwykłe prompty.

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
