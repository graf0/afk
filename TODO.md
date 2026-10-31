# TODO

Kamienie milowe po M1 (`docs/architecture.md`) z wstępnym planem. M1 to tickety #7–#10 w GitHub Issues. Przed rozbiciem kamienia na tickety: grilling i aktualizacja docs/ADR. Otwarte kwestie techniczne: `docs/open-questions.md`.

## M2 — `.afk.toml`, Base image, Gitea

Cel: Project opisuje swoje maszyny i Toole (Postgres, Playwright w Sandbox VM); Base image sam się odświeża; Gitea jako drugi Forge. Decyzje: ADR-0010, ADR-0011, `README.md` (klucze), `docs/workflow.md`, `docs/images.md`.

Domknięcie M1:

- [x] `.afk.toml` dla dfmx na `main`: `[network].allow` dla Go (`proxy.golang.org`, `sum.golang.org`), `[machine.sandbox].build` z toolchainem, `[gate].cmd`. Warunek zastąpienia `ralph.sh`.
- [x] Odmowy Egress proxy (host, port, powód) do `<log>-egress.txt`, podsumowanie na koniec Run; ścieżka logu wypisywana na starcie Run.
- [x] `disk_size = "64G"` w Template Debian (partycja w gościu dziś 10G).
- [x] `afk vm shell` dla VM po porażce: Egress proxy z tą samą polityką co Run.

`.afk.toml`:

- [x] `[image]` → `[machine.<nazwa>]` (`template`, `build`, `max_age` domyślnie `168h`, `memory`, `cpus`); `sandbox` zastrzeżone, inne nazwy odrzucane do M4.
- [x] `[tool.<nazwa>]`: `on` (tylko `sandbox`), `build`, `setup`, `env`, `prompt`; usunięcie `kind`.
- [x] Walidacja wszystkich sekcji (lista w `README.md`), wszystkie błędy naraz z `linia:kolumna`; `afk config` z drzewa roboczego.
- [x] Toole w Run: `setup` po klonie (alfabetycznie, `<log>-setup.txt`, pomijany przy wznowieniu), `env` w sesjach i bramce, sekcja „Narzędzia” w promptach.

Base image:

- [x] Konfiguracja z gałęzi bazowej także w `afk image build` i `afk vm up` (ADR-0010).
- [x] Base image w katalogu per maszyna `/var/lib/afk/base/<project>/<maszyna>/<wersja>/`.
- [x] Hash konfiguracji obrazu w katalogu wersji.
- [x] Factory buduje obraz przed Run (brak / Expiry / inny hash) pod `flock` per Project, log `image-<maszyna>-<wersja>.log`.
- [x] Sprzątanie po udanej budowie: wersje nie najnowsze, nie poprzednia najnowsza, niebędące backing file overlayu w `runs/` ani `chats/`.

Forge:

- [x] Label `[labels].spec` definiuje Spec (GitHub i Gitea); usunięcie heurystyki `## User Stories`; ticket z sub-issues bez labela pomijany z ostrzeżeniem (podany jawnie: błąd).
- [x] Preflight: wszystkie labele z `[labels]` istnieją w repo.
- [x] Gitea (REST) za interfejsem `Forge`: dzieci Spec z listy zadań `- [ ] #M`, blokery = natywne zależności + `Blocked by:`, token `GITEA_TOKEN` / `~/.local/state/afk/gitea-token`, push i PR.

## M3 — Chat, TUI, `afk-ai`

Cel: interaktywna praca z Agentem w VM i Factory z jednego okna. Decyzje: ADR-0008, ADR-0012, `docs/workflow.md` (Chat, TUI). Trzy Specy po kolei: A → B → C, potem dokumentacja.

Spec A — Chat bez TUI:

- [ ] `afk chat new|attach|export|rm|list`; nazwa wymagana (`[a-z0-9-]{1,32}`), odmowa przy kolizji z Chat lub branchem `chat-<nazwa>` na remote.
- [ ] VM Chat: snapshot gałęzi bazowej, `setup`/`env` Tooli, Egress proxy z pełnym internetem (odmowy do `chat-<nazwa>-egress.txt`), `claude` interaktywnie na pty z `--append-system-prompt`.
- [ ] `attach`: `flock`, `claude --continue` przy kolejnym, start wyłączonej VM; koniec `claude` = koniec procesu, VM zostaje.
- [ ] Eksport: commity od snapshotu przez Quarantine repo, walidacja jak w Run (całość albo nic), push z lease na ostatni eksport, bez PR.
- [ ] `rm`: odmowa bez `--force` przy niezacommitowanej albo nieeksportowanej pracy.
- [ ] Limit pamięci przy każdej nowej VM: `MemAvailable` ≥ `memory` + 2 GiB.

Spec B — `afk-ai`:

- [ ] Kanał gość → `afk`: odwrotny unix socket na ssh, HTTP+JSON, tylko przy `attach`; binarka `afk` kopiowana jako `afk-ai` (ADR-0012).
- [ ] `issue view|list|create|comment|edit`: format `TICKET.md`, stopka `— z Chat <nazwa>`, `ready-for-agent` → `needs-triage`, treść tylko issue tego Chat, log `chat-<nazwa>-forge.txt`.
- [ ] `docs/agents/*` i skille Projectu: `afk-ai` zamiast `gh`.

Spec C — TUI:

- [ ] `afk tui`: serwer `tmux -L afk` z wbudowaną konfiguracją, sprawdzenie tmux ≥ 3.3.
- [ ] Sidebar (bubbletea): Chaty i porzucone Runy z metadanych libvirt (z polem `repo`), Factory z opcji okien tmux; grupowanie po Project.
- [ ] Skróty `M-↑/↓`, `M-n`, `M-F`, `M-x`, `M-e`, `M-s`; sprawdzenie, czy Alt dochodzi do tmux w popularnych terminalach.
- [ ] Widok Factory: nagłówek przez OSC 2 (ticket, wiek Run, czas od ostatniego zdarzenia), `remain-on-exit`.

Dokumentacja:

- [ ] Zwięzły `README.md`: Getting Started (wymagania hosta, token, pierwsze `.afk.toml`, `afk image build`, `afk factory`, `afk chat`, `afk tui`), komendy, tabela skrótów TUI.

## M4 — Windows, Tool VM, sterowanie ekranem

Cel: Projekty wymagające Windowsa i GUI (spike'i w `docs/windows-images.md`, ADR-0005, ADR-0007).

- [ ] Template Windows (ws2025, win11) przez Packer CLI: ISO i SHA256 z `.afk.toml`, OpenSSH (Win11 z MSI), sysprep jako ostatni krok, UEFI + vTPM.
- [ ] Sandbox VM Windows: NVRAM per maszyna, `undefine --nvram --tpm`, klucz hosta sshd per VM (odpowiednik credentiala SMBIOS dla Windows), test izolacji z sondą PowerShell.
- [ ] Tool VM: `[machine.<nazwa>]` poza `sandbox`, Tool z `on = "<nazwa>"`, w sieci tego samego Run (filtr dopuszcza ruch w /24 — dziś jedna VM na sieć), podnoszona na żądanie Agenta przez kanał gość → `afk` (wspólny z `afk-ai`, ADR-0011), sprzątanie razem z Run.
- [ ] Sterowanie ekranem: `afk-screen` w VM przez tunel ssh, zrzuty PNG przez `DomainScreenshot` (obejście błędu XDR), wejście przez QMP `input-send-event`/`send-key`; autologon w klonie.
- [ ] Pozostałe punkty „Obrazy Windows” z open-questions.
