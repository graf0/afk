---
status: accepted (transport zmieniony przez ADR-0013, patrz Update)
---

# Kanał gość → `afk`: odwrotny unix socket na ssh, `afk` jako `afk-forge`

Agent w Chat tworzy i czyta tickety, a w M4 podnosi Tool VM, więc potrzebuje drogi do `afk` na hoście. `afk` otwiera na swoim połączeniu ssh odwrotne przekierowanie na unix socket w gościu (`/run/afk/afk.sock`) i obsługuje go w procesie jako HTTP+JSON, tak jak Egress proxy (ADR-0001). Host nie otwiera żadnego portu, kanał inicjuje host i żyje tylko razem z procesem, który trzyma ssh (attach Chat); autoryzacja to sam socket: ten Chat, ten Project, Forge tego Project. Klientem w gościu jest binarka `afk` kopiowana przy attach do `/usr/local/bin/afk-forge`, która rozpoznaje tryb po `argv[0]` — wersja zawsze zgodna z hostem, bez przebudowy Base image.

## Considered Options

- **Subsystem ssh na hoście**: połączenie inicjowane z gościa, czyli gość z dostępem do sshd hosta — wbrew ADR-0001.
- **vsock**: brak na Windows, kanał inicjowany z gościa (odrzucony już w architekturze).
- **JSON na stdin/stdout jednego procesu**: jedno wywołanie na połączenie ssh, nie pasuje do wielu niezależnych wywołań z gościa.
- **`afk-forge` w Base image albo przez `go:embed`**: przebudowa obrazu przy każdej zmianie protokołu albo `go generate` przed `go build`.
- **Shim `gh` w VM**: skille działałyby bez zmian, ale trzeba by emulować flagi `gh` (`--json`, `--jq`); zamiast tego dokumentacja i skille Projectu mówią o `afk-forge`.

## Consequences

Socket istnieje tylko przy attach: shell w VM Chat nie ma kanału. Binarka `afk` trafia do niezaufanej VM — nie może zawierać sekretów. Windows (M4) wymaga cross-builda `afk`. Tool VM (M4) używa tego samego kanału.

## Update (ADR-0013)

Transport w modelu kontenerowym: zamiast odwrotnego tunelu ssh `afk` słucha na unix sockecie w runtime dir hosta (`$XDG_RUNTIME_DIR/afk/<hash>/afk.sock`, tryb `0600`) i montuje jego katalog jako `/run/afk` (socket: `/run/afk/afk.sock`) tylko do Sandboxa danego Chatu (nie do innych serwisów Stacku, nie do Runów). Host nadal nie otwiera portu dla Sandboxa, a autoryzacją jest sam socket: ten Chat, ten Project, Forge tego Project. Klientem jest binarka `afk` hosta, montowana tylko do odczytu jako `$HOME/bin/afk-forge` (rozpoznaje tryb po `argv[0]`); adres socketu bierze ze zmiennej `AFK_SOCKET`. Socket żyje z procesem `afk chat` (jeden proces na Chat, lock). Trasy to grupy (`chat.routeGroup`): tickety, PR (ADR-0014), Stack (logi, restart, reseed) i VM testowe. Sandbox Chatu ma kanał także w `afk chat shell`, bo to ten sam kontener; Run kanału nie ma.
