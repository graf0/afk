# Przepływ Factory i Chat

Factory wzorowana na `ralph.sh` z dfmx ([`reference/ralph.sh`](reference/ralph.sh)). `afk` pracuje na Project z bieżącego katalogu: bez `.afk.toml` odmawia. Każdy Run i każdy Chat to Stack (compose z gałęzi bazowej), a Agent pracuje w jego serwisie `afk`, czyli w Sandboxie ([architecture.md](architecture.md#stack-i-sandbox)). `afk` działa tylko na pierwszym planie (ADR-0015): Stack żyje z procesem, a po wyjściu zostaje stan.

## Factory

`afk factory [selektor]` bierze tylko Ticket z labelem `[labels].ready_for_agent`: bez niego odmawia, bez flagi obejścia. Selektor: `*` (domyślny: kolejne gotowe Tickety, jak `pick()` w ralph.sh, do wyczerpania kolejki) albo lista numerów `#12 #15` (w tej kolejności, potem koniec; zamknięty albo niegotowy ticket z listy to błąd, zablokowany jest pomijany z ostrzeżeniem). Bez limitu iteracji: `*` kończy się, gdy nie ma czego wziąć; Ctrl-C przerywa (stan bieżącego Run zostaje, jak po porażce).

1. `*` czyta z Forge listę otwartych issue z labelem `ready-for-agent` (rosnąco po numerze) i każdy kandydat sprawdza na świeżym odczycie — lista po labelu bywa nieaktualna. Pomija: ticket bez labela, dziecko Spec (robi je Spec), ticket z otwartym blokerem (patrz [Blokery](#blokery)), Spec z otwartym dzieckiem bez `ready-for-agent`, Spec bez dzieci i ticket z sub-issues bez labela `[labels].spec` (trzy ostatnie z ostrzeżeniem, raz), ticket zarezerwowany przez inną Factory oraz ticket już obsłużony w tym uruchomieniu. Z selektorem-listą ticket z sub-issues bez labela `[labels].spec` to błąd (Factory staje), pozostałe pominięcia są ostrzeżeniem.
2. Rezerwacja: `flock` na `~/.local/state/afk/claims/<project>/<n>`; spec rezerwuje też swoje otwarte dzieci. Po rezerwacji ticket jest czytany jeszcze raz (inna Factory mogła go skończyć w międzyczasie). Blokada znika ze śmiercią procesu. Jeden host — dwie maszyny z `afk` na tym samym repo są poza zakresem.
3. Preflight przed pierwszym Run (nie przy pustej kolejce): poświadczenia (Forge, Claude), istnienie wszystkich labeli z `[labels]` w repo (`afk` ich nie tworzy), start Stacku z gałęzi bazowej (z budową obrazów, [architecture.md](architecture.md#stack-i-sandbox)) i bramka `[gate].cmd` na czystym branchu bazowym w jego Sandboxie. Stack preflightu jest usuwany po teście. Gdy bramka pada, to nie wina Agenta: Factory kończy się błędem ze ścieżką wyjścia bramki.
4. Spec (ticket z labelem `[labels].spec`) to ciąg Runów, jeden na każde dziecko w kolejności, na wspólnym branchu, zakończony review całości — patrz [Spec](#spec).
5. Każdy Run dostaje świeży Stack. Klon gałęzi bazowej trafia do drzewa Sandboxa, potem `afk` uruchamia serwis `afk-setup` (o ile Stack go ma; raz na Stack, po checkoucie, więc widzi pliki Projectu); jego wyjście idzie na terminal, porażka = porażka Run. Wznowienie w zostawionym stanie pomija setup. Po sukcesie Stack i jego stan są usuwane (volume'y cache Runów zostają).
6. Runy jednej Factory idą po kolei. Równoległość = kilka Factory.
7. Porażka Run (albo Ctrl-C) zatrzymuje Factory z kodem wyjścia ≠ 0; Stack idzie `down`, a stan (klon, home Agenta, volume'y) zostaje. Do inspekcji służy katalog stanu (`~/.local/state/afk/<project>/runs/ticket-<n>/repo`), a ponowne `afk factory` wznawia Run. `needs_info` Ticketu nie jest porażką: stan jest usuwany, Factory idzie dalej (także gdy `needs_info` zatrzymało Spec: pytania są na dziecku i na Spec). Nie ma TTL: nie wiadomo, ile co trwa.
8. Wznowienie: na starcie, przed kolejką, Factory szuka Runów tego Project ze stanem w `~/.local/state/afk/<project>/runs/` (katalog stanu to jedyna ewidencja; numer ticketu z nazwy `ticket-<n>`). Przed ruszeniem czegokolwiek Factory bierze rezerwację ticketu leftovera (stan żywego Run innej Factory wygląda tak samo; zajęty → pomijany) i trzyma ją dla wznowionego Run; Stack preflightu (`preflight-<id>`) ma własną rezerwację na czas życia, a jego stan po Ctrl-C jest usuwany. Z selektorem-listą ruszane są tylko leftovery wymienionych ticketów i ich Specs. Ticket zamknięty → stan usuwany, w Quarantine repo znikają refy brancha, a branch `ticket-<n>` znika z remote tylko wtedy, gdy nie ma PR i nie ruszył się od odczytu (branch z PR nigdy); dla dziecka Spec ginie tylko stan: jego praca jest na branchu Spec. Wznowienie przepisuje `TICKET.md` (i `SPEC.md`) ze świeżego odczytu. Ticket z `ready-for-agent` → Run kontynuowany w zostawionym stanie, bez ponownego klonu, z notą „wznawiasz przerwaną pracę” w prompcie pierwszej sesji (jak `resume` w ralph.sh); stan dziecka Spec wznawia Spec, a stan review — review. Stack wstaje z compose i obrazów z bazy Run zapisanej w Quarantine repo (z początku przerwanego Run), nie z dzisiejszego `origin/main`. Ticket bez labela → pominięty, stan zostaje (komunikat podaje, który katalog usunąć, by go porzucić).

Ścieżka logu Run jest wypisywana na starcie Run.

Forge: GitHub i Gitea przez REST (`net/http`), interfejs `Forge`. Token Gitei: `GITEA_TOKEN`, a bez niego `~/.local/state/afk/gitea-token` (`0600`). Słownik labeli konfigurowalny (`[labels]`), domyślnie z `docs/agents/triage-labels.md`.

#### Blokery

Bloker ticketu to każde issue z natywnej zależności Forge **lub** z linii `Blocked by: #a, #b` w treści (suma, nie pierwszeństwo — dopisanie zależności w UI nie unieważnia linii). Ticket jest odblokowany, gdy wszystkie blokery są zamknięte. Gitea z wyłączonymi zależnościami w repo: liczy się sama linia.

### Co dostaje agent

- Host zapisuje Ticket (treść, komentarze, spec, blokery) do pliku `TICKET.md` w katalogu kontekstu Sandboxa (montowanym tylko do odczytu); ścieżkę podaje prompt.
- Prompt mówi: gdzie są dane, że tracker jest celowo niedostępny, że nie wolno zgadywać (ADR-0003, ADR-0014).
- `CLAUDE_CODE_OAUTH_TOKEN` jako zmienna środowiskowa Sandboxa (plik `0600`, nie argv); Agent go widzi (ADR-0014).
- WebFetch/WebSearch wyłączone, ale sieć Sandboxa jest otwarta (ADR-0013): allowlisty nie ma.
- `claude` hosta jest montowany do Sandboxa tylko do odczytu; poza tokenem Claude'a Agent nie widzi sekretów hosta.
- Skille z repo (`.claude/skills/`): w klonie. Pluginy: tylko te zadeklarowane w Project config, patrz [Pluginy](#pluginy). Nic więcej z `~/.claude` hosta (ADR-0009).

### Raport agenta

Agent kończy sesję plikiem raportu:

- `status`: `done` | `needs_info`
- `summary`, `deviations` (odstępstwa od ticketu), `doubts` (wątpliwości dla reviewera)
- przy `needs_info`: lista pytań

`needs_info` → `afk` komentuje ticket pytaniami, ustawia `needs-info`, zdejmuje `ready-for-agent`; Stack i stan są usuwane, wyniku się nie pobiera. Agent Factory nie rozmawia z człowiekiem (bez `ask_human`): część interaktywna dzieje się w Chat, przed Factory.

Strumień Agenta (stream-json) idzie do pliku logu w `~/.local/state/afk/<project>/logs/`, a na wyjście `afk` tekst Agenta i jednolinijkowe wywołania narzędzi, bez znaków sterujących i formatujących (Sandbox jest niezaufany). Log jest rotowany: po 128 MiB przechodzi do `<log>.1` (poprzedni `.1` znika), więc na dysku najwyżej ok. 256 MiB, a Run nie jest przerywany z powodu rozmiaru; najstarsza część strumienia przepada.

### Po sesji

1. **Gate**: `[gate].cmd` z `.afk.toml` uruchamiany w Sandboxie po `done`. Porażka → sesja naprawcza Agenta z końcówką wyjścia bramki w prompcie (napraw minimalnie, nie zmieniaj zakresu), do `[gate].max_fixes` sesji (poprawki z review mają osobny budżet); wyczerpanie albo status inny niż `done` → porażka Run (stan zostaje). Wyjście każdej próby w pliku obok logu Run (`<log>-gate-N.txt`). Raport sesji naprawczej dopisuje się do raportu Run; sesja naprawcza → `ready-for-human`. Każda nowa porcja commitów z Sandboxa przechodzi tę samą walidację co pierwsza (przodek bazy, chronione ścieżki).
2. **Review** modelem `[agent].review_model`: sesja na całym branchu względem bazy, poprawia znaleziska i commituje. Poprawki → osobny commit `code review: <tytuł>` (też przez bramkę, z własnymi sesjami naprawczymi). Raport review (`findings_fixed`, `findings_skipped`) to drugi komentarz na PR; niepuste `findings_skipped` → `ready-for-human`.
3. **Git** (ADR-0002): host robi tylko import `git bundle` z Sandboxa do Quarantine repo; nigdy nie uruchamia gita w klonie agenta. Walidacja: zmiany w chronionych ścieżkach (`[protect]` i ścieżki chronione zawsze, patrz [README](../README.md#protect): to, co host albo człowiek uruchomi z gałęzi bazowej po merge'u) → odrzucenie. Sprawdzany jest każdy commit, bez wykrywania rename, z pominięciem wielkości liter; zastąpienie katalogu nadrzędnego plikiem lub symlinkiem też się liczy. `CLAUDE.md`, `.gitattributes`, `.gitmodules` są dozwolone. Project config i pliki Stacku są czytane zawsze z gałęzi bazowej (ADR-0010).
4. Push i PR wyłącznie przez `afk` z poświadczeniami hosta. Commity implementacji są squashowane w Quarantine repo do jednego commita na bazie (`<tytuł ticketu> (#N)`, treść: `summary`, autor: tożsamość gita użytkownika z jego repo), a poprawki review do drugiego, `code review: <tytuł>`, na wierzchu; oba walidowane ponownie. Push `ticket-N` z Quarantine repo idzie na `https://github.com/<repo>.git` (Gitea: `<forge.url>/<repo>.git`) tokenem Forge w nagłówku HTTP przekazanym gitowi przez zmienne `GIT_CONFIG_*` (nie na argv, nie na dysku, tylko do tego procesu gita), bez globalnego i systemowego configu gita użytkownika (żadne `insteadOf` nie przekieruje go na ssh ani inny host), bez przekierowań HTTP i bez credential helperów; tylko tworzy branch, nigdy go nie nadpisuje. Branch `ticket-N` na remote → odmowa przed startem Stacku. Potem PR (draft; na Gitei draft = prefiks tytułu `WIP:`, zdjęty przy przejściu do gotowego — nie zdejmuj go ręcznie z PR Specu w trakcie, bo Factory nie wznowi Specu, którego PR nie jest draftem) z `Closes #N`, komentarz z raportem, `ready-for-human` na PR przy odstępstwach lub wątpliwościach, zdjęcie `ready-for-agent`. Tekst Agenta na GitHubie i w commicie: bez @-wzmianek i bez słów zamykających issue (`Fixes #12` itp.) — oba łamane zero-width space — bez komentarzy HTML (`<!--` → `&lt;!--`), najwyżej 60000 bajtów; własne `Closes #N` `afk` dopisuje po tym. Porażka kroku w Forge podaje, co zrobiono, i po linii na każdy krok do zrobienia ręcznie.

### Spec

`afk factory '#N'`, gdy N ma label `[labels].spec` (zachowanie jak `do_spec` w `ralph.sh`): ciąg Runów na branchu `ticket-<spec>`, zakończony review całego brancha.

- **Dzieci**: na GitHubie sub-issues, na Gitei (bez sub-issues) referencje `#M` z listy zadań `- [ ] #M` w treści Spec — stan z odczytu issue, checkboxy ignorowane; referencja do PR albo innego repo to błąd. Otwarte dzieci rosnąco po numerze; za każdym razem pierwsze bez otwartego blokera ([Blokery](#blokery)). Otwarte dzieci wszystkie zablokowane → porażka. Każde otwarte dziecko musi mieć `ready-for-agent` (jak `pick()` w `ralph.sh`), inaczej odmowa przed pierwszą sesją: gotowość nadaje człowiek (ADR-0003, ADR-0014). Dzieci ze swoimi dziećmi nie są obsługiwane. Ticket bez labela Spec z listą zadań na Gitei to zwykły ticket — lista to checklista.
- **Run dziecka**: świeży Stack; klon startuje z końca brancha `ticket-<spec>` (pierwsze dziecko: z bazy), `origin/<baza>` w klonie zostaje bazą. Prompt dostaje dziecko (`TICKET.md`) i Spec jako kontekst (`SPEC.md`: „kontekst, nie zakres”). Bramka przechodzi dla każdego dziecka; implementacja dziecka jest squashowana do jednego commita `<tytuł dziecka> (#M)` na końcu brancha. Walidacja: wynik sesji musi zawierać koniec brancha i nie dotykać chronionych ścieżek, a po squashu cała gałąź — bazę i te same chronione ścieżki. Review dziecka nie ma; jest jedno, na końcu.
- **Po dziecku**: push brancha; po pierwszym dziecku draft PR (tytuł Spec) z `Closes #<spec>` i `Closes` dla wszystkich dzieci; komentarz z raportem dziecka na PR; `ready-for-human` na PR, gdy dziecko miało odstępstwa, wątpliwości albo sesję naprawczą bramki (od razu, bo Run może być wznowiony w innym procesie); komentarz na dziecku z odnośnikiem do PR, zamknięcie dziecka, zdjęcie z niego `ready-for-agent`.
- **`needs_info` na dziecku**: komentarz z pytaniami i `needs-info` na dziecku oraz komentarz i `needs-info` na Spec, zdjęcie `ready-for-agent` z obu, porażka Spec (Factory idzie dalej). Zrobione dzieci zostają na branchu i na PR (draft). Aby Spec ruszył ponownie, człowiek przywraca `ready-for-agent` na dziecku i na Spec.
- **Review całości**: po ostatnim dziecku jedna sesja review (jak w Run) w świeżym Stacku na końcu brancha, względem bazy; poprawki to osobny commit `code review: <tytuł Spec>`, przez bramkę i walidację. Potem: push, komentarz z raportem review, nowy opis PR (`Closes` i lista commitów brancha), `ready-for-human` przy poprawkach bramki albo pominiętych znaleziskach, PR gotowy do review (GraphQL `markPullRequestReadyForReview`: REST tego nie umie), zdjęcie `ready-for-agent` ze Spec.
- **Wznowienie**: branch `ticket-<spec>` na remote z draft PR (albo bez PR) → kontynuacja od następnego otwartego dziecka, a gdy żadne nie zostało — od review. Czubek brancha z remote wchodzi do Quarantine repo i przechodzi tę samą walidację, a baza to merge-base z bieżącą bazą (branch bazowy mógł pójść naprzód). PR niebędący draftem albo zamknięty → błąd z instrukcją (zakończ lub zamknij PR, usuń branch `git push origin --delete ticket-<spec>`, uruchom ponownie); wynik z poprzedniego procesu zostawiony w Quarantine repo (porażka kroku w Forge) trzeba usunąć ręcznie, jak przy Run.
- **Push na istniejący branch** jest bezpieczny: `--force-with-lease=<ref>:<sha>`, gdzie sha to ostatni commit, który `afk` wypchnął albo odczytał; nowy commit zawsze leży na starym, więc to tylko fast-forward, a cudzy push pomiędzy kończy się odmową. Pierwszy push tworzy branch (lease z pustym sha). Nigdy blind force.

## Chat

`afk chat <nazwa> [--expose] [--rm] [-- args claude]` tworzy Chat albo wraca do istniejącego; `afk chat ls`, `afk chat shell <nazwa>` i `afk chat rm <nazwa>` zarządzają Chatami. Nazwa: `[a-z0-9-]`, zaczyna i kończy się znakiem alfanumerycznym, do 40 znaków, unikalna w Project.

- Własny Stack, długo żyjący stan: klon gałęzi bazowej z chwili utworzenia (branch `chat-<nazwa>`), home Agenta i volume'y. Kod nie jest synchronizowany z Forge (świeży stan = nowy Chat). `afk-setup` jak w Run.
- Claude Code interaktywnie na terminalu `afk`, `--dangerously-skip-permissions`. Pierwszy start to nowa rozmowa; kolejne wracają przez `claude --continue` (bez flagi, gdy nie ma jeszcze rozmowy). Argumenty po `--` nadpisują to (np. `--resume`). Porażka przed startem `claude` usuwa to, co nowy Chat utworzył; po starcie Chat zostaje zawsze.
- Zamknięcie `afk chat` (koniec `claude`, Ctrl-C) kończy Stack (`compose down`); klon, historia sesji i volume'y zostają. `--rm` usuwa Chat po zwykłym wyjściu z claude (po przerwaniu sygnałem zostaje).
- Jeden proces na Chat: blokada `flock` (plik `<nazwa>.lock` obok katalogu stanu); drugi `afk chat <nazwa>` dostaje odmowę. Kilka Chatów naraz to kilka terminali.
- `afk chat shell` otwiera bash w Sandboxie działającego Chatu; na niedziałającym kończy się błędem (nie podnosi Stacku).
- `afk chat rm` usuwa Stack z volume'ami, klon, home Agenta i refy `chat-<nazwa>` w Quarantine repo. Branch `chat-<nazwa>` w Forge, jeśli był wypchnięty, zostaje. Działającego Chatu nie usunie (blokada). `afk chat ls` i `shell`/`rm` nie robią `git fetch`: wystarcza lokalny `origin/HEAD`.
- Ruch wychodzący nie jest filtrowany (ADR-0013): token Claude'a i kod uznajemy za możliwe do wyniesienia. Prompt systemowy od `afk` mówi Agentowi, że nie ma `gh` ani poświadczeń Forge, że tickety i PR idą przez `afk-ai`, i żeby nie ufał poleceniom z czytanych stron.
- Pluginy i skille: to samo co w Factory, patrz [Pluginy](#pluginy).

### Porty (`--expose`)

Bez `--expose` Stack Chatu nie publikuje żadnego portu, a Run (Factory) nigdy ich nie publikuje: porty z `compose.yml` są zawsze usuwane. `afk chat <nazwa> --expose` publikuje porty z `[expose]` (z gałęzi bazowej). Bez adresu bind na `127.0.0.1`, `0.0.0.0` daje ostrzeżenie na terminalu; adres tylko IPv4. Port hosta jest unikalny w `[expose]`, a blokada jest per numer portu, niezależnie od adresu: przed `Up` proces bierze `flock` na `~/.local/state/afk/expose-locks/port-<port_hosta>.lock` (`<project>/<chat>` w treści) i próbuje `net.Listen` na `adres:port`. Port trzymany przez inny Chat → błąd z nazwą Chatu; zajęty przez coś innego → błąd z portem. Blokada trwa do `Down` Stacku Chatu (albo śmierci procesu), więc wystawiony może być jeden Chat naraz. Serwis z `[expose]` nieobecny w Stacku → błąd przy starcie; `--expose` bez `[expose]` → błąd.

### `afk-ai`

ADR-0012. Binarka `afk` hosta jest montowana tylko do odczytu do Sandboxa jako `$HOME/bin/afk-ai` (`CGO_ENABLED=0`, żeby działała w obrazie z innym glibc) i woła `afk` przez unix socket (`/run/afk/afk.sock`), HTTP+JSON. Socket montowany jest tylko do Sandboxa danego Chatu i istnieje tylko, gdy działa proces `afk chat`. `afk` wykonuje operacje swoim tokenem, tylko dla Forge tego Project. `afk-ai` bez argumentów wypisuje pełną składnię; poniżej zasady. Instrukcja dla Agenta: [agents/issue-tracker.md](agents/issue-tracker.md).

- `issue view <n>` — format `TICKET.md` (ta sama sanitizacja); `issue list [--label …] [--state open|closed|all]` — `#N  [labele]  tytuł`.
- `issue create`, `issue comment` — treść ze stopką `— z Chat <nazwa>`. Komentarz na dowolnym issue, także `ready-for-agent`: trafia do `TICKET.md` Run, ale w Chat jest człowiek in-the-loop, więc to świadomy kompromis, nie dziura. Label Spec dozwolony — Chat tworzy Specy.
- `issue edit` — labele dowolnego issue; treść i tytuł tylko issue utworzonych przez ten Chat (lista numerów w pliku `issues` w stanie Chatu) i tylko dopóki nie mają `ready-for-agent`: człowiek zatwierdził tę treść, więc jej zmiana omijałaby review (liczy się label sprzed zapisu, także gdy to samo wywołanie go zdejmuje). Label Spec na cudzym tickecie zmienia się tak samo jak inne.
- `issue comment` na pull requeście jest odrzucany jak `issue edit`.
- `ready-for-agent` z Chat jest zamieniany na `needs-triage`: gotowość dla Factory nadaje człowiek. Bez zamykania issue.
- `pr create --title … [--body … | --body-file -]` (ADR-0014): commity klonu Sandboxa (`git bundle`) idą do Quarantine repo, z walidacją jak wynik Run (przodek bazy, chronione ścieżki z `[protect]` i Dockerfile'e Stacku); naruszenie → odmowa z listą commitów i ścieżek, nic nie trafia do Forge. Bez commitów względem bazy → błąd; niezacommitowane zmiany nie wchodzą do PR (ostrzeżenie). Push `chat-<nazwa>` z `--force-with-lease`, PR do gałęzi bazowej (gotowy do review); gdy PR jest już otwarty, tylko push i URL (tytuł i opis zostają). Czytanie, komentowanie i merge PR zostają po stronie człowieka.
- `stack logs <serwis> [--tail <n>]` — ogon logu serwisu Stacku Chatu (domyślnie 100 linii, max 1000; poza zakresem → błąd; wynik sanityzowany i obcięty do 256 KiB). `stack restart <serwis>` — dowolny serwis poza `afk` (Sandbox Agenta) i jednorazowym `afk-setup` (→ `stack reseed`). `stack reseed` — ponowne `afk-setup` (Stack bez tego serwisu → błąd); czyści tyle, ile czyści ten skrypt, volume'y zostają.
- `vm …` — VM testowe z `[[vm]]`, patrz [libvirt.md](libvirt.md). Factory ich nie dostaje.
- Każde wywołanie kanału (trasa, wynik), także nieznanej trasy, idzie do logu `~/.local/state/afk/<project>/logs/chat-<nazwa>-<czas>-channel.txt`.

## Pluginy

Agent dostaje wyłącznie pluginy wymienione wprost (ADR-0009): `[agent].plugins` (`<nazwa>@<marketplace>`) i `[agent].marketplaces` z `.afk.toml` oraz `enabledPlugins` z `.claude/settings.json` Project (z gałęzi bazowej). `afk` niczego nie instaluje: plugin musi być zainstalowany na hoście (`claude plugin install <nazwa>@<marketplace>`), a brak to błąd z tą komendą. Do Sandboxa trafia tylko wybrany plugin i jego marketplace (tylko do odczytu, pod tymi samymi ścieżkami) oraz przepisane metadane pluginów, które wymieniają wyłącznie je; katalog danych pluginów jest osobny dla Stacku.

Gdy efektywna lista zawiera `mattpocock-skills`, prompt Run zaczyna się od `/mattpocock-skills:implement`, a prompt review od `/mattpocock-skills:code-review <baza>` (jak w `ralph.sh`); bez niego zostają zwykłe prompty.

Z `~/.claude` nigdy nie trafia do Sandboxa: `.credentials*`, `settings.json` (hooki, uprawnienia), `projects/`, `history.jsonl`, `session-env/`, `plans/`, `file-history/`, `skills/synced/` (skille konta claude.ai zakładają connectory).

## Stan

Bez bazy danych: Stacki Chatów i Runów to katalogi `~/.local/state/afk/<project>/{chats,runs}/<nazwa>` (klon, home Agenta, eksporty compose; [architecture.md](architecture.md#ścieżki)); co działa, wiedzą procesy `afk` (blokady `flock`); tickety — Forge; rezerwacje — `flock`; logi Run — pliki stream-json.
