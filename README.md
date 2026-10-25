# afk

Fabryka AI: bierze tickety z Forge (GitHub, Gitea) i implementuje je bez nadzoru, każdy w osobnej, odizolowanej VM. Pojęcia: [`GLOSSARY.md`](GLOSSARY.md), reszta dokumentacji: [`docs/`](docs/README.md).

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
]
setup = ["task db:migrate", "task db:seed"]
env = { DATABASE_URL = "postgres:///app" }
prompt = "PostgreSQL na localhost (unix socket, rola agent, bez hasła), baza app. Świeże dane: task db:seed."

[tool.playwright]
build = ["npx -y playwright install --with-deps chromium"]
prompt = "Zrzut ekranu: npx playwright screenshot <url> shot.png"
```

### `[forge]`

| Klucz | Typ | Domyślnie | Opis |
|---|---|---|---|
| `kind` | string | — (wymagany) | `github` albo `gitea` |
| `repo` | string | — (wymagany) | `owner/name` |
| `url` | string | — | Adres `https://` instancji; wymagany dla `gitea`, zakazany dla `github` |

Token: GitHub — `GITHUB_TOKEN`, a bez niego `gh auth token`; Gitea — `GITEA_TOKEN`, a bez niego `~/.local/state/afk/gitea-token` (`0600`).

### `[labels]`

Nazwy labeli w Forge dla ról; niepuste i parami różne. Muszą istnieć w repo — Factory sprawdza to przed pierwszym Run i niczego nie tworzy.

| Klucz | Domyślnie | Znaczenie |
|---|---|---|
| `ready_for_agent` | `ready-for-agent` | Ticket gotowy dla Factory; nadaje tylko człowiek |
| `needs_info` | `needs-info` | Agent zadał pytanie |
| `needs_triage` | `needs-triage` | Do oceny |
| `ready_for_human` | `ready-for-human` | PR z odstępstwami lub wątpliwościami |
| `wontfix` | `wontfix` | Nie będzie robione |
| `spec` | `spec` | Ticket jest Spec: dzieci to sub-issues (GitHub) albo `- [ ] #M` w treści (Gitea) |

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

Obraz jest budowany od nowa przed Run, gdy go brak, minęło `max_age` albo zmieniła się jego konfiguracja (`template`, `build` maszyny i jej Tooli, pluginy). `memory`, `cpus`, `env`, `prompt` i `setup` nie wymagają przebudowy.

### `[tool.<nazwa>]`

Tool to narzędzie, którego Agent może użyć (ADR-0011).

| Klucz | Typ | Domyślnie | Opis |
|---|---|---|---|
| `on` | string | `sandbox` | Maszyna, w której działa Tool (jeszcze tylko `sandbox`) |
| `build` | []string | `[]` | Polecenia w obrazie maszyny `on`, po `build` maszyny; Toole alfabetycznie |
| `setup` | []string | `[]` | Polecenia w `/work` po klonie, przed pierwszą sesją, Toole alfabetycznie; porażka = porażka Run |
| `env` | table | `{}` | Zmienne dla `setup`, sesji Agenta i bramki; ta sama nazwa w dwóch Toolach to błąd |
| `prompt` | string | `""` | Co Agent dostaje w sekcji „Narzędzia” promptu |

Usługi Toola w Sandbox VM działają od startu VM, każdy Run ma własną instancję. Dane dostępowe to nie sekrety — w `build` i `prompt` wpisuj je wprost; prawdziwych sekretów nie umieszczaj w `.afk.toml`.
