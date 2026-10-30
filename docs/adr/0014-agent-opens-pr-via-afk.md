---
status: accepted
---

# Agent w Chat otwiera PR przez `afk`; push brancha Agenta uruchamia CI

Zastępuje ADR-0003.

Agent w Chat wypycha branch `chat-<nazwa>` i otwiera PR wąską operacją kanału `afk` (ADR-0012). Wynik przechodzi przez Quarantine repo i tę samą walidację co Export i Run, łącznie z odrzuceniem zmian w chronionych ścieżkach. Factory nadal nie ma kanału: pytania idą przez ticket, a wynik wypycha `afk`. Token Claude'a jest w Sandboxie jako zmienna środowiskowa i jest osiągalny dla Agenta. To świadome ryzyko: bez Egress proxy (ADR-0013) nic go nie chroni.

## Consequences

- Każdy push brancha Agenta (`chat-<nazwa>`, `ticket-N`) uruchamia CI repo z jego sekretami na kodzie Agenta. Filtr chronionych ścieżek chroni definicje workflowów, ale nie kod, który wykonują. Projekt powinien dawać sekrety tylko workflowom na gałęzi bazowej i tagach albo za approval (environments). Opisuje to README.
- Token jest w środowisku Sandboxa, więc widzi go też gate i każdy proces w Sandboxie. To nie jest nowa ekspozycja: gate wykonuje kod napisany przez Agenta, który ma token w swojej sesji.
- Agent nadal nie nadaje `ready-for-agent` i nie merguje PR. Robi to człowiek.
