# afk

Fabryka AI: bierze tickety z Forge (GitHub, Gitea) i bez nadzoru implementuje je w kodzie. Każdy ticket dostaje osobny Sandbox, czyli kontener podman z Agentem (Claude Code), obok własnego Stacku Projectu (baza, serwer, frontend). Planowanie i eksperymenty człowiek robi z Agentem w Chatach, też odizolowanych. Pojęcia: [`GLOSSARY.md`](GLOSSARY.md), decyzje: [`docs/adr/`](docs/adr/).

> Stan: migracja z Sandbox VM na kontenery (ADR-0013) jest w toku. README opisuje docelowe zachowanie.

## Instalacja i aktualizacja

```sh
go install github.com/graf0/afk/cmd/afk@latest
```

Aktualizacja to ta sama komenda.

## Wymagania hosta

- Linux, rootless podman z włączonym socketem: `systemctl --user enable --now podman.socket`.
- `docker-compose` v2 (Debian: pakiet `docker-compose`). `afk` woła go z `DOCKER_HOST` ustawionym na socket podmana.
- `git` i `claude` na hoście. Binarka `claude` jest montowana do Sandboxa tylko do odczytu, więc obraz Sandboxa musi mieć zgodne glibc.
- Tylko dla VM testowych: libvirt (`qemu:///system`) i istniejące domeny.

## Poświadczenia

- Claude: `claude setup-token`. Token podajesz w `CLAUDE_CODE_OAUTH_TOKEN` albo w pliku `~/.local/state/afk/claude-oauth-token` (tryb `0600`).
- GitHub: `GITHUB_TOKEN` albo zalogowane `gh`.
- Gitea: `GITEA_TOKEN` albo plik `~/.local/state/afk/gitea-token` (tryb `0600`).

## Project

Repozytorium Projectu musi mieć w korzeniu trzy pliki:

- **`.afk.toml`**: konfiguracja (format niżej).
- **`compose.yml`**: Stack dev Projectu.
- **`compose.afk.yml`**: override z serwisem `afk` (Sandbox) i opcjonalnym `afk-setup`. To serwis jednorazowy, np. seed bazy, uruchamiany przy pierwszym starcie Stacku i na żądanie Agenta. W tym pliku są też katalogi maskowane (anonimowe volume'y) i cache (named volume'y).

Gdy brak `compose.afk.yml`, `afk` na terminalu proponuje utworzenie go razem z `Dockerfile.afk` z szablonu. Niczego nie commituje.

Wszystkie trzy pliki są czytane z gałęzi bazowej (`origin/HEAD`), nie z drzewa roboczego (ADR-0010). Zmiana działa więc dopiero po merge'u. Obrazy też są budowane z gałęzi bazowej, automatycznie, gdy ich brak albo gdy zmieniła się ich definicja.

## Komendy

Wszystko działa na pierwszym planie. Zamknięcie `afk` kończy Stack (`compose down`), ale stan zostaje: klon, historia sesji i volume'y. Kilka Chatów naraz uruchamiasz w kilku terminalach.

| Komenda | Opis |
|---|---|
| `afk config` | Sprawdza `.afk.toml` z drzewa roboczego i wypisuje efektywną konfigurację oraz publiczny klucz ssh dla VM testowych |
| `afk factory ['*' \| '#N' ...]` | Realizuje gotowe tickety (domyślnie wszystkie) albo wymienione, po kolei. Najpierw wznawia porzucone Runy |
| `afk chat <nazwa> [--expose] [--rm] [-- args claude]` | Tworzy Chat albo wraca do niego (`claude --continue`). `--expose` publikuje porty z `[expose]`, `--rm` usuwa Chat po wyjściu |
| `afk chat ls` | Lista Chatów Projectu |
| `afk chat shell <nazwa>` | Bash w Sandboxie działającego Chatu |
| `afk chat rm <nazwa>` | Usuwa Chat: klon, home Agenta, volume'y |

Chat pracuje na branchu `chat-<nazwa>`. Agent w Chat ma przez `afk` dostęp do:

- ticketów: czytanie, komentarze, labele, tworzenie; nigdy nie nadaje `ready-for-agent`;
- PR z brancha Chatu;
- logów, restartu i reseedu Stacku;
- VM testowych: ekran, klawiatura, mysz, snapshoty.

Run Factory ma ten sam Stack, ale bez tych operacji.

**CI:** każdy push brancha Agenta (`chat-<nazwa>`, `ticket-N`) uruchamia CI repo na kodzie Agenta. Sekrety dawaj tylko workflowom na gałęzi bazowej i tagach albo za approval (ADR-0014).

## `.afk.toml`

Nieznany klucz to błąd. `afk config` wypisuje wszystkie błędy z `linia:kolumna`.

```toml
[forge]
kind = "github"
repo = "graf0/dfmx"

[agent]
model = "opus"
plugins = ["mattpocock-skills@claude-plugins-official"]

[gate]
cmd = "go build ./... && go vet ./... && go test ./..."

[expose]
web = "8080:5173"
api = "192.168.122.1:8081:8080"
ssh = "2222:22"

[[vm]]
name = "win11"
user = "admin"
os = "windows"

[protect]
paths = ["scripts/deploy/"]
```

### `[forge]`

| Klucz | Domyślnie | Opis |
|---|---|---|
| `kind` | wymagany | `github` albo `gitea` |
| `repo` | wymagany | `owner/name` |
| `url` | brak | Adres `https://` instancji: wymagany dla `gitea`, zakazany dla `github`. Musi to być adres ostateczny, bez przekierowań |

Gitea nie ma sub-issues ani draftów. Dzieci Spec to linie `- [ ] #M` w treści, a draft PR to tytuł z prefiksem `WIP:`.

### `[labels]`

Nazwy labeli w Forge. Muszą istnieć w repo, `afk` ich nie tworzy.

| Klucz | Domyślnie |
|---|---|
| `ready_for_agent` | `ready-for-agent` |
| `needs_info` | `needs-info` |
| `needs_triage` | `needs-triage` |
| `ready_for_human` | `ready-for-human` |
| `wontfix` | `wontfix` |
| `spec` | `spec` |

### `[agent]`

| Klucz | Domyślnie | Opis |
|---|---|---|
| `model` | domyślny Claude Code | Model sesji pracy |
| `review_model` | domyślny Claude Code | Model review |
| `plugins` | `[]` | `nazwa@marketplace`, udostępniane Agentowi w Sandboxie |
| `marketplaces` | `[]` | `owner/repo` albo URL gita; `claude-plugins-official` jest zawsze |

### `[gate]`

| Klucz | Domyślnie | Opis |
|---|---|---|
| `cmd` | wymagany | Bramka w Sandboxie po pracy Agenta |
| `max_fixes` | `3` | Liczba sesji naprawczych po porażce bramki |

### `[expose]`

Porty Stacku publikowane tylko przy `afk chat --expose`. Porty są stałe, więc w danej chwili wystawiony może być jeden Chat. Klucz to nazwa serwisu compose, wartość to `[adres:]port_hosta:port_kontenera`. Domyślny adres to `127.0.0.1`. Do VM testowej użyj adresu mostka libvirt (np. `192.168.122.1`). Przy `0.0.0.0` `afk` ostrzega.

### `[[vm]]`

VM testowe dostępne dla Agenta w Chat (Factory ich nie dostaje). `afk` ich nie tworzy. Klucz ssh generuje `afk`, a jego publiczną część wypisuje `afk config`.

| Klucz | Opis |
|---|---|
| `name` | Nazwa domeny libvirt |
| `user` | Konto do ssh |
| `os` | `windows` albo `linux` |

### `[protect]`

| Klucz | Opis |
|---|---|
| `paths` | Ścieżki, których wynik Agenta nie może zmienić: plik, katalog (`dir/`) albo dowolna głębokość (`**/`) |

Zawsze chronione są: `.afk.toml`, `compose.yml`, `compose.afk.yml`, Dockerfile'e z `build:` w compose, `.github/`, `.gitea/`, `**/.claude/` i `**/.mcp.json`.
