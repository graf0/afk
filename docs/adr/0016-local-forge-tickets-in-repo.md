---
status: accepted (faza A: tylko Chat)
---

# Forge `local`: tickety jako pliki w repo, Agent publikuje je na bazę przez `afk`

Project bez API Forge (np. Gitea dostępna tylko po ssh) trzyma tickety jako pliki markdown w repo, w katalogu `[forge].dir` (domyślnie `.scratch`): `<dir>/<cecha>/spec.md` i `<dir>/<cecha>/issues/NN-slug.md`, z liniami `Status:` i `Blocked by:` oraz komentarzami pod `## Comments` (konwencja dfmx). `[forge] kind = "local"` nie ma `url`. `repo` (`owner/name`) wyznacza tylko katalog stanu. Remote to `origin` klona użytkownika, a push idzie gitem użytkownika (jego config i klucze ssh). `afk` nie trzyma do niego żadnego tokenu. `forge.Local` ma tylko `Remote()`. Operacje na issues i PR zwracają `errors.ErrUnsupported`.

W Chat z `local` nie ma `afk-ai issue *` ani `afk-ai pr create`: nie ma ich w pomocy, w prompcie ani w trasach kanału. Są za to dwie komendy:

- `afk-ai push` wypycha `chat-<nazwa>` na origin z tą samą walidacją co `pr create` (bundle, Quarantine repo, chronione ścieżki), bez PR. Merge robi człowiek.
- `afk-ai tickets publish -m <opis>`: `afk` waliduje klon tak jak przy `push`, bierze zmiany brancha w `<dir>/` i nakłada je trzystronnie (`git merge-tree --write-tree --merge-base` w Quarantine repo, bez checkoutu, ADR-0002) na świeżo pobrany `origin/<baza>`. Wspólnym przodkiem scalenia jest merge-base albo ostatnia publikacja (niżej), a drugą stroną jego drzewo z `<dir>/` podmienionym na wersję z HEAD. Powstaje jeden commit z autorem z git config hosta, który `afk` wypycha zwykłym pushem. Zmiany spoza `<dir>/` nie trafiają do commita. Konflikt, `Status: <ready_for_agent>` dodany w łatce albo baza przesunięta w międzyczasie (non-fast-forward) dają błąd i nic nie zostaje wypchnięte. Przy braku zmian dostajesz komunikat. Ostatnio opublikowany HEAD brancha zostaje w Quarantine repo (`refs/afk/published/chat-<nazwa>`). Kolejna publikacja liczy zmiany od niego, a nie od merge-base, bo poprzednia publikacja jest na bazie innym commitem i jej zmiany konfliktowałyby same ze sobą.

To odpowiednik reguły dfmx „zmiany tylko w `.scratch/` idą prosto na main”. Zmienia relację z ADR-0003 i ADR-0014: Agent w Chat pisze na gałąź bazową, ale tylko w `<dir>/`, tylko przez `afk` i tylko po walidacji. Niezmiennik z ADR-0003 zostaje: Chat nigdy nie nadaje `ready-for-agent`. Przy Forge z API `afk` zamienia ten label na `needs-triage`, a tutaj odrzuca publikację.

## Considered Options

- **Agent pushuje bazę sam (poświadczenia w Sandboxie)**: Chat ma cały internet, a token może wyciec (ADR-0003). Push na gałąź bazową bez filtra ścieżek to też droga do zmiany kodu bez review.
- **Tickety przez `afk-ai issue *` mapowane na pliki**: emulacja numerów, labeli i komentarzy na plikach. Dużo kodu, a konwencja plików i tak jest Agentowi znana ze skilli.
- **Publikacja przez PR**: przy `local` nie ma API do PR. Merge ręczny na każdą zmianę ticketu to więcej pracy, niż kosztuje zasada „tylko `<dir>/` prosto na bazę”.

## Consequences

- Każdy, kto steruje Agentem (także zatruta strona), może zmieniać pliki w `<dir>/` na bazie: tworzyć, edytować i usuwać tickety. Ochrona to filtr ścieżek, blokada `ready-for-agent` i historia gita (autor to człowiek z hosta, w treści commita jest stopka Chatu). Blokada `ready-for-agent` sprawdza dodane linie `Status:` w łatce, więc obejmuje też ticket przeniesiony do innego pliku, nawet gdy status nadał mu wcześniej człowiek.
- Klon Chatu to migawka bazy z chwili utworzenia. Tickety zmienione później na bazie nie są w nim widoczne, a zmiana tego samego miejsca kończy się konfliktem przy publikacji.
- Push na bazę uruchamia CI repo, tak jak każdy push brancha Agenta (ADR-0014).
- `git apply --3way` odpadło: przy konflikcie zapisuje pliki do bieżącego katalogu nawet w gołym repo, czyli do drzewa roboczego użytkownika. `merge-tree` niczego nie zapisuje, ale wymaga gita ≥ 2.40 na hoście.
- `afk factory` z `local` kończy się błędem. Factory czytająca tickety z plików to faza B.
