# Architektura

## Pakiety

Bez `internal/`:

| Pakiet | Odpowiedzialność |
|---|---|
| `cmd/afk` | CLI |
| `config/` | `.afk.toml` |
| `forge/` | GitHub, Gitea (REST) |
| `stack/` | Stack: compose z gałęzi bazowej, override `afk`, budowa obrazów, `Up`/`Exec`/`Down`/`Remove`, szablon `compose.afk.yml` |
| `sandbox/` | Widok hosta w Sandboxie: tożsamość i token Agenta, `claude`, ustawienia i pluginy |
| `agent/` | Claude Code: prompt, sesja, raport |
| `run/` | Orkiestracja Run, Quarantine repo, rezerwacje, Factory (selektor, kolejne Run, wznowienia) |
| `chat/` | Chat: interaktywny Agent, serwer kanału `afk-cli` (ADR-0012), `[expose]`, VM testowe |

Interfejs tylko tam, gdzie są co najmniej dwie implementacje: `Forge` (GitHub, Gitea).

Stan bez bazy danych: katalogi stanu Stacków, rezerwacje `flock`, logi w plikach ([workflow.md](workflow.md#stan)).

## Zależności

- `github.com/pelletier/go-toml/v2`
- `golang.org/x/term`

Narzędzia hosta wołane jako procesy: `git`, `claude`, `podman`, `docker-compose` v2, a dla VM testowych `virsh` i `ssh-keygen` ([libvirt.md](libvirt.md)).

## Stack i Sandbox

Stack to projekt compose z `compose.yml` i `compose.afk.yml` z gałęzi bazowej (ADR-0013, ADR-0010) plus override generowany przez `afk`. Pliki są eksportowane z commita gałęzi bazowej (`git archive`), nigdy z drzewa Agenta. `afk` woła `docker-compose` v2 z `DOCKER_HOST` na sockecie rootless podmana (`$XDG_RUNTIME_DIR/podman/podman.sock`). Format plików i zmienne `${AFK_TREE}`, `${AFK_CACHE}`: [README](../README.md#project).

Serwis `afk` to Sandbox. Override nadaje mu: `command: sleep infinity`, `init: true`, `userns_mode: keep-id` (pliki w drzewie należą do użytkownika hosta), drzewo Chatu albo Runu pod tą samą ścieżką co na hoście, `$HOME` Agenta, plik środowiska `0600` (token Claude'a, tożsamość gita), `claude` hosta tylko do odczytu oraz, w Chacie, socket `afk-cli`. Porty z `compose.yml` są usuwane, publikuje je tylko `[expose]`.

Obrazy z `build:` są budowane z gałęzi bazowej, gdy ich brak. Tag to hash drzewa gita kontekstu builda, więc zmiana definicji daje nowy obraz, a niezmieniona nic nie przebudowuje.

Stack żyje z procesem `afk` (ADR-0015): `afk chat` i `afk factory` robią `compose down` przy wyjściu, a stan zostaje.

### Izolacja

- Sandbox nie widzi sekretów hosta; poświadczeń hosta (Forge, libvirt) używa wyłącznie przez socket `afk` ([workflow.md](workflow.md#afk-cli)), który dostaje sam Sandbox Chatu. Run nie ma kanału.
- Token Claude'a jest w środowisku Sandboxa i jest dla Agenta osiągalny (ADR-0014).
- Ruch wychodzący nie jest filtrowany (ADR-0013): kod Projectu i token mogą zostać wyniesione po prompt injection. Granicą jest to, co wraca na host: wynik przechodzi przez Quarantine repo i walidację (ADR-0002), a `.afk.toml`, compose i Dockerfile'e Stacku pochodzą z gałęzi bazowej.
- Host nie uruchamia gita w klonie Agenta; zmiany w chronionych ścieżkach są odrzucane ([README](../README.md#protect)).
- Każdy push brancha Agenta uruchamia CI repo na kodzie Agenta; sekrety CI tylko dla workflowów na gałęzi bazowej (ADR-0014).

## Ścieżki

Stan: `~/.local/state/afk/` (`DefaultStateDir`).

| Ścieżka | Zawartość |
|---|---|
| `<project>/chats/<nazwa>/`, `<project>/runs/<nazwa>/` | Stan Stacku (`Dir`): `repo/` (klon, drzewo Sandboxa), `home/` (home Agenta), `export/` (pliki compose z gałęzi bazowej), `override.yml`, `agent.env` (chwilowy), `setup.done`; w Chacie też `issues` (numery utworzone przez Chat), a obok katalogu plik blokady `<nazwa>.lock`. Nazwa Runu: `ticket-<n>` albo `preflight-<id>`. To jedyna ewidencja Chatów i Runów |
| `<project>/quarantine.git` | Quarantine repo (bare): `refs/afk/base/ticket-<n>` baza Run, `refs/afk/incoming/ticket-<n>` surowy wynik z Sandboxa, `refs/heads/ticket-<n>` wynik po walidacji; analogicznie `chat-<nazwa>` |
| `<project>/logs/` | Logi Run (stream-json, `-gate-N.txt`) i kanału Chatu (`-channel.txt`); poza repo: logi to niezaufana treść |
| `<project>/vm-ssh/` | Klucz ssh VM testowych (`id_ed25519`, `.pub`) |
| `claims/<project>/<n>` | Rezerwacje ticketów (`flock`) |
| `expose-locks/`, `vm-locks/` | Blokady portów `[expose]` i VM testowych (host, nie Project) |
| `claude-oauth-token`, `gitea-token` | Tokeny, `0600`, gdy brak `CLAUDE_CODE_OAUTH_TOKEN` / `GITEA_TOKEN` |
| `$XDG_RUNTIME_DIR/afk/<hash>/afk.sock` | Socket kanału Chatu, `0600` |

`<project>` to `owner-name` z `[forge].repo`. Volume'y compose (maski, cache) leżą w podmanie: projekt compose `afk_<project>_<chat|run>_<nazwa>`, cache Runów wspólny (`afk_<project>_factory`).

Quarantine repo leży w stanie użytkownika, nie w repo użytkownika: obiekty z Sandboxa nie trafiają do object store repo człowieka (jego hooki i config ich nie dotykają), a wynik przeżywa restart. Bez hooków (`core.hooksPath=/dev/null`), `transfer.fsckObjects`, nigdy checkout.
