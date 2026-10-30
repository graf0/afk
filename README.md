# afk

Fabryka AI: bierze tickety z trackera i bez nadzoru implementuje je w kodzie, każdy w osobnej, odizolowanej maszynie wirtualnej. Pojęcia: [`GLOSSARY.md`](GLOSSARY.md), reszta dokumentacji: [`docs/`](docs/README.md).

## Wymagania hosta

- Linux x86_64 z KVM (`/dev/kvm`), libvirt (`qemu:///system`), `qemu-img`, `xorriso`, `git`; użytkownik z dostępem do gniazda libvirt (Debian: grupa `libvirt`) i w grupie `kvm` ([libvirt.md](docs/libvirt.md)).
- Katalog `/var/lib/afk` z grupą `kvm`, zapisywalny dla użytkownika: `sudo install -d -o "$USER" -g kvm -m 2775 /var/lib/afk`.
- tmux ≥ 3.3 — tylko dla `afk tui`.
- Go (wersja z `go.mod`) do instalacji: `go install ./cmd/afk` w klonie tego repo. Packera `afk` pobiera sam ([images.md](docs/images.md)).

## Poświadczenia

- Claude: `claude setup-token`, token w `CLAUDE_CODE_OAUTH_TOKEN` albo w pliku `~/.local/state/afk/claude-oauth-token` (tryb `0600`).
- Forge: GitHub — `GITHUB_TOKEN` albo zalogowane `gh`; Gitea — `GITEA_TOKEN` albo `~/.local/state/afk/gitea-token` (tryb `0600`). Szczegóły: [`[forge]`](#forge).

## Getting Started

W klonie Projectu (z `origin` w Forge):

1. Utwórz `.afk.toml`:

   ```toml
   [forge]
   kind = "github"
   repo = "owner/name"

   [gate]
   cmd = "go test ./..."
   ```

2. `afk config` — sprawdza plik i wypisuje efektywną konfigurację z wartościami domyślnymi.
3. Commit i push na gałąź bazową: komendy, które coś uruchamiają, czytają `.afk.toml` z `origin/HEAD`, nie z drzewa roboczego.
4. Załóż w Forge labele z [`[labels]`](#labels) — `afk` ich nie tworzy. GitHub, nazwy domyślne: `for l in ready-for-agent needs-info needs-triage ready-for-human wontfix spec; do gh label create --force "$l"; done`.
5. `afk image build` — buduje Base image (Factory zbuduje go też sama, gdy brak).
6. Nadaj ticketowi label `ready-for-agent` i uruchom `afk factory` (wszystkie gotowe) albo `afk factory '#12'`.
7. `afk chat new plan` — Chat do planowania w osobnej VM.
8. `afk tui` — wszystkie Chaty, Factory i porzucone Runy w jednym oknie tmux.

Przepływ Factory i Chat: [workflow.md](docs/workflow.md).

## Komendy

| Komenda | Opis |
|---|---|
| `afk` | Sprawdza `.afk.toml` z drzewa roboczego |
| `afk config` | Sprawdza `.afk.toml` i wypisuje efektywną konfigurację |
| `afk image build` | Buduje Base image Projectu |
| `afk factory ['*' \| '#N' ...]` | Realizuje gotowe tickety (domyślnie wszystkie) albo wymienione, po kolei |
| `afk chat new <nazwa> [--model …] [--effort …]` | Tworzy Chat i wchodzi do niego |
| `afk chat attach <nazwa>` | Wraca do Chat |
| `afk chat export <nazwa>` | Wypycha commity Chat na branch `chat-<nazwa>` |
| `afk chat shell <nazwa>` | Shell w VM Chat |
| `afk chat rm [--force] <nazwa>` | Usuwa Chat i jego VM |
| `afk chat list` | Lista Chatów Projectu |
| `afk vm up` | Diagnostyka: startuje Sandbox VM i sprawdza ssh |
| `afk vm list` | Lista VM Projectu |
| `afk vm shell <vm>` | Shell w VM, z Egress proxy |
| `afk vm kill <vm>` | Niszczy VM |
| `afk tui` | TUI na tmux: Chaty, Factory, porzucone Runy |
| `afk sidebar` | Lewy panel TUI, uruchamiany przez `afk tui` |

## Skróty TUI

Bez prefiksu tmux; reszta klawiszy idzie do Agenta ([workflow.md](docs/workflow.md#tui)).

| Klawisz | Akcja |
|---|---|
| `M-↑` / `M-↓` | poprzedni / następny wpis |
| `M-n` | nowy Chat (pyta o nazwę) |
| `M-F` | nowa Factory (pyta o selektor, domyślnie `*`) |
| `M-x` | zamknij Chat (eksport / usuń / anuluj) / Factory: działającą przerywa jak Ctrl-C po potwierdzeniu, zakończoną zamyka (okno) / zniszcz VM porzuconego Run |
| `M-e` | eksport Chat |
| `M-s` | shell w VM (Chat, porzucony Run; przy Factory tylko komunikat) |

## `.afk.toml`

Plik w korzeniu repozytorium Projectu; bez niego `afk` nie pracuje. Czytany z gałęzi bazowej (`origin/HEAD`), nie z drzewa roboczego (ADR-0010) — zmiana działa po merge'u. `afk config` sprawdza plik z drzewa roboczego: wypisuje wszystkie błędy z `linia:kolumna` albo efektywną konfigurację z wartościami domyślnymi. Nieznany klucz to błąd.

```toml
[forge]
kind = "github"
repo = "graf0/dfmx"

[gate]
cmd = "task lint && go test ./..."

[network]
allow = ["proxy.golang.org", "sum.golang.org"]

[machine.sandbox]
build = ["sudo apt-get install -y golang-go"]
memory = 4096

[tool.postgres]
build = [
  "sudo apt-get install -y postgresql",
  "sudo -u postgres createuser -s agent",
  "sudo -u postgres createdb -O agent app",
]
setup = ["task db:migrate", "task db:seed"]
env = { DATABASE_URL = "postgres:///app" }
prompt = "PostgreSQL na localhost (unix socket, rola agent, bez hasła), baza app. Świeże dane: task db:seed."

[tool.playwright]
build = [
  "sudo apt-get install -y nodejs npm",
  "sudo npm install -g playwright",
  "sudo PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright playwright install --with-deps chromium",
]
env = { PLAYWRIGHT_BROWSERS_PATH = "/opt/ms-playwright" }
prompt = "Zrzut ekranu: playwright screenshot <url> shot.png (globalne CLI, przeglądarka w /opt/ms-playwright)."
```

### `[forge]`

| Klucz | Typ | Domyślnie | Opis |
|---|---|---|---|
| `kind` | string | — (wymagany) | `github` albo `gitea` |
| `repo` | string | — (wymagany) | `owner/name` |
| `url` | string | — | Adres `https://` instancji; wymagany dla `gitea`, zakazany dla `github` |

Token: GitHub — `GITHUB_TOKEN`, a bez niego `gh auth token`; Gitea — `GITEA_TOKEN`, a bez niego plik `~/.local/state/afk/gitea-token`, który musi mieć tryb `0600` (luźniejszy jest odrzucany). Bez żadnego z nich `afk` kończy się błędem z obiema drogami podania tokenu. Token nie trafia do logów.

Gitea: odczyty (ticket, lista gotowych, labele, dzieci Spec, blokery) i zapisy (push, PR, komentarze, labele, zamykanie issue). Draft PR to PR z prefiksem tytułu `WIP:` (Gitea nie ma flagi draft ani GraphQL), gotowy do review = tytuł bez prefiksu; labele z `[labels]` muszą istnieć w repo (Gitea operuje na ich ID). `afk` nie podąża za przekierowaniami: `forge.url` musi być ostatecznym adresem instancji. dzieci Spec to `- [ ] #M` / `- [x] #M` z treści ticketu z labelem `[labels].spec` (checkbox jest ignorowany, stan bierze się z issue M; PR albo issue z innego repo to błąd). Spec ticketu `afk` wyznacza, przeglądając otwarte issues z tym labelem (jedno zapytanie na 50 Spec). Blokery to natywne zależności Gitei (`/issues/{n}/dependencies`) plus linie `Blocked by:`; gdy repo ma zależności wyłączone, liczy się sama linia.

### `[labels]`

Nazwy labeli w Forge dla ról; niepuste i parami różne. Muszą istnieć w repo — Factory sprawdza to przed pierwszym Run i niczego nie tworzy.

| Klucz | Domyślnie | Znaczenie |
|---|---|---|
| `ready_for_agent` | `ready-for-agent` | Ticket gotowy dla Factory; nadaje tylko człowiek |
| `needs_info` | `needs-info` | Agent zadał pytanie |
| `needs_triage` | `needs-triage` | Do oceny |
| `ready_for_human` | `ready-for-human` | PR z odstępstwami lub wątpliwościami |
| `wontfix` | `wontfix` | Nie będzie robione |
| `spec` | `spec` | Ticket jest Spec — decyduje wyłącznie ten label; dzieci to sub-issues (GitHub) albo `- [ ] #M` w treści (Gitea). Spec bez dzieci i ticket z sub-issues bez tego labela są pomijane z ostrzeżeniem (z listą `afk factory '#N'` ten drugi to błąd) |

### `[agent]`

| Klucz | Typ | Domyślnie | Opis |
|---|---|---|---|
| `model` | string | domyślny Claude Code | Model sesji pracy |
| `review_model` | string | domyślny Claude Code | Model review; jeśli oba ustawione — różne |
| `plugins` | []string | `[]` | `nazwa@marketplace`, instalowane w Base image (ADR-0009); sprawdzany tylko format — nazwę marketplace'u definiuje jego `marketplace.json`, więc istnienie sprawdza `claude` przy budowie |
| `marketplaces` | []string | `[]` | `owner/repo` albo URL gita; `claude-plugins-official` jest zawsze |

Pluginy z `enabledPlugins` w `.claude/settings.json` Projectu też trafiają do obrazu.

### `[gate]`

| Klucz | Typ | Domyślnie | Opis |
|---|---|---|---|
| `cmd` | string | — (wymagany) | Bramka w VM po pracy Agenta; bez niej Factory nie rusza |
| `max_fixes` | int ≥ 0 | `3` | Sesje naprawcze po porażce bramki |

### `[network]`

| Klucz | Typ | Domyślnie | Opis |
|---|---|---|---|
| `allow` | []string | `[]` | Hosty dostępne z Sandbox VM w Run: nazwa hosta, adres IP albo `*.domena` (poddomeny dowolnej głębokości, bez samej `domena`); bez schematu, portu i ścieżki. Budowa obrazu ma pełny internet |

Odmowy proxy są w `<log>-egress.txt` obok logu Run.

### `[protect]`

| Klucz | Typ | Opis |
|---|---|---|
| `paths` | []string | Ścieżki, których wynik Agenta nie może zmienić: plik, katalog (`dir/`), na dowolnej głębokości (`**/`); bez `..` i wiodącego `/`. Zawsze dopisane: `.afk.toml`, `.github/`, `.gitea/`, `**/.claude/`, `**/.mcp.json` |

### `[machine.<nazwa>]`

Maszyna i jej Base image. `sandbox` to Sandbox VM, w której pracuje Agent; bez tej sekcji — wartości domyślne. Inne nazwy (Tool VM) jeszcze nieobsługiwane.

| Klucz | Typ | Domyślnie | Opis |
|---|---|---|---|
| `template` | string | `debian13` | Template obrazu (`debian13`; `ws2025`, `win11` — w przygotowaniu) |
| `build` | []string | `[]` | Polecenia shell przy budowie obrazu: internet przez proxy, bez sekretów |
| `max_age` | duration | `168h` | Expiry; `"0"` — bez Expiry |
| `memory` | int | `2048` | RAM w MiB |
| `cpus` | int | `2` | vCPU |

Obraz jest budowany od nowa przed Run, gdy go brak, minęło `max_age` albo zmieniła się jego konfiguracja (`template`, `build` maszyny i jej Tooli, pluginy). `memory`, `cpus`, `max_age`, `env`, `prompt` i `setup` nie wymagają przebudowy (`max_age` działa przez wiek obrazu). `afk image build` zapisuje ten sam hash, więc ręczna budowa też jest aktualna; `afk vm up` niczego nie buduje.

### `[tool.<nazwa>]`

Tool to narzędzie, którego Agent może użyć (ADR-0011).

| Klucz | Typ | Domyślnie | Opis |
|---|---|---|---|
| `on` | string | `sandbox` | Maszyna, w której działa Tool (jeszcze tylko `sandbox`) |
| `build` | []string | `[]` | Polecenia w obrazie maszyny `on`, po `build` maszyny; Toole alfabetycznie |
| `setup` | []string | `[]` | Polecenia w `/work/repo` (klon) po klonie, przed pierwszą sesją, Toole alfabetycznie; wyjście w `<log>-setup.txt`; porażka = porażka Run (VM zostaje); wznowienie Run w zostawionej VM pomija `setup`, dziecko Spec w świeżej VM wykonuje go od nowa |
| `env` | table | `{}` | Zmienne dla `setup`, sesji Agenta i bramki; nazwa musi być poprawną nazwą zmiennej powłoki, a ta sama nazwa w dwóch Toolach to błąd; w promptach są tylko nazwy, bez wartości |
| `prompt` | string | `""` | Co Agent dostaje w sekcji „Narzędzia” promptu |

Usługi Toola w Sandbox VM działają od startu VM, każdy Run ma własną instancję. `afk` nie dodaje własnego menedżera usług: usługę uruchamia to, co zainstaluje `build` (pakiet apt z jednostką systemd startuje przy bootowaniu; w innym przypadku dodaj `sudo systemctl enable <usługa>` do `build`). Każdy Tool trafia do sekcji „Narzędzia” (bez `prompt` — samą nazwą) i nazwy jego zmiennych `env`; bez Tooli sekcji nie ma. `build` wykonuje użytkownik `agent` w bashu z `-e`, więc polecenia wymagające roota potrzebują `sudo`. Dane dostępowe to nie sekrety — w `build` i `prompt` wpisuj je wprost; prawdziwych sekretów nie umieszczaj w `.afk.toml`.
